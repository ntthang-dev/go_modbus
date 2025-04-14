// Package portmanager quản lý truy cập tuần tự vào một cổng COM vật lý
package portmanager

import (
	"context"
	"encoding/binary"
	"fmt"
	"sync"
	"time"

	// !!! THAY 'modbus_register_slave' bằng tên module của bạn !!!
	"modbus_register_slave/config"

	"github.com/goburrow/modbus"
	"github.com/sirupsen/logrus"
)

// ModbusRequest đại diện cho một yêu cầu đọc/ghi Modbus
type ModbusRequest struct {
	SlaveID   byte
	IsWrite   bool   // true nếu là ghi, false nếu là đọc
	Address   uint16 // Địa chỉ bắt đầu (0-based)
	Quantity  uint16 // Số lượng thanh ghi/coil
	WriteData []byte // Dữ liệu cần ghi (cho lệnh ghi)
	// *** THÊM: Function Code để xác định loại thao tác ***
	FunctionCode int                 // Ví dụ: 3 (ReadHolding), 4 (ReadInput), 6 (WriteSingleReg), 16 (WriteMultiReg)...
	ReplyChan    chan ModbusResponse // Channel để gửi trả kết quả
}

// ModbusResponse chứa kết quả hoặc lỗi của một yêu cầu
type ModbusResponse struct {
	Result []byte // Dữ liệu đọc được hoặc phản hồi ghi
	Err    error
}

// Manager quản lý một cổng COM vật lý duy nhất
type Manager struct {
	portName    string // Chỉ để logging
	portCfg     config.ConnectionConfig
	handler     *modbus.RTUClientHandler
	client      modbus.Client
	requestChan chan ModbusRequest
	log         *logrus.Entry
	mu          sync.Mutex
}

// NewManager tạo một Port Manager mới
func NewManager(portCfg config.ConnectionConfig, statusLogger *logrus.Logger) *Manager {
	logger := statusLogger.WithField("port", portCfg.Port)
	return &Manager{
		portName:    portCfg.Port,
		portCfg:     portCfg,
		requestChan: make(chan ModbusRequest, 20), // Tăng buffer channel
		log:         logger,
	}
}

// GetRequestChannel trả về kênh để các Device gửi yêu cầu vào
func (m *Manager) GetRequestChannel() chan<- ModbusRequest {
	return m.requestChan
}

// connect (internal) thực hiện kết nối hoặc kiểm tra kết nối
func (m *Manager) connect() error {
	m.mu.Lock() // Lock toàn bộ quá trình kiểm tra và kết nối
	defer m.mu.Unlock()

	if m.handler == nil {
		windowsPortPath := m.portCfg.GetWindowsPortPath()
		m.log.Infof("Khởi tạo handler cho %s", windowsPortPath)
		m.handler = modbus.NewRTUClientHandler(windowsPortPath)
		m.handler.BaudRate = m.portCfg.BaudRate
		m.handler.DataBits = m.portCfg.DataBits
		m.handler.Parity = m.portCfg.Parity
		m.handler.StopBits = m.portCfg.StopBits
		m.handler.Timeout = m.portCfg.GetTimeout()
	}

	// Thử kết nối (hàm Connect của goburrow tự xử lý nếu đã kết nối)
	err := m.handler.Connect()
	if err != nil {
		m.log.WithError(err).Error("PortManager: Kết nối thất bại")
		// Đóng handler cũ nếu có lỗi để lần sau tạo lại
		if m.handler != nil {
			m.handler.Close() // Cố gắng đóng
			m.handler = nil
		}
		m.client = nil
		return err
	}

	if m.client == nil {
		m.client = modbus.NewClient(m.handler)
	}
	return nil
}

// Close đóng kết nối của Port Manager
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.handler != nil {
		m.log.Info("Đang đóng kết nối Port Manager...")
		err := m.handler.Close()
		m.handler = nil // Đặt lại để lần sau tạo mới
		m.client = nil
		return err
	}
	return nil
}

// Run là vòng lặp chính của Port Manager
func (m *Manager) Run(ctx context.Context) {
	m.log.Info("Port Manager bắt đầu hoạt động...")
	defer m.log.Info("Port Manager đã dừng.")
	defer m.Close()

	// Thử kết nối lần đầu khi khởi động
	if err := m.connect(); err != nil {
		m.log.Warn("Port Manager: Kết nối ban đầu thất bại, sẽ thử lại khi có yêu cầu.")
	}

	for {
		select {
		case <-ctx.Done():
			m.log.Info("Port Manager nhận tín hiệu dừng.")
			return
		case request := <-m.requestChan:
			m.log.WithFields(logrus.Fields{
				"slave_id": request.SlaveID, "fc": request.FunctionCode, "addr": request.Address, "qty": request.Quantity,
			}).Debug("Port Manager nhận yêu cầu")

			var response ModbusResponse

			// --- Thực hiện giao dịch Modbus (cần lock) ---
			m.mu.Lock()

			// 1. Đảm bảo đã kết nối (thử lại nếu cần)
			if m.client == nil {
				if err := m.connect(); err != nil {
					m.mu.Unlock()
					response.Err = fmt.Errorf("Port Manager không thể kết nối: %w", err)
					request.ReplyChan <- response
					continue
				}
			}

			// 2. Đặt đúng Slave ID
			m.handler.SlaveId = request.SlaveID

			// 3. Thực hiện đọc hoặc ghi dựa trên Function Code
			switch request.FunctionCode {
			case 3: // Read Holding Registers
				response.Result, response.Err = m.client.ReadHoldingRegisters(request.Address, request.Quantity)
			case 4: // Read Input Registers
				response.Result, response.Err = m.client.ReadInputRegisters(request.Address, request.Quantity)
			case 1: // Read Coils
				response.Result, response.Err = m.client.ReadCoils(request.Address, request.Quantity)
			case 2: // Read Discrete Inputs
				response.Result, response.Err = m.client.ReadDiscreteInputs(request.Address, request.Quantity)
			case 6: // Write Single Register
				if len(request.WriteData) == 2 {
					value := binary.BigEndian.Uint16(request.WriteData)
					response.Result, response.Err = m.client.WriteSingleRegister(request.Address, value)
				} else {
					response.Err = fmt.Errorf("FC06 yêu cầu đúng 2 bytes dữ liệu")
				}
			case 16: // Write Multiple Registers
				if len(request.WriteData) == int(request.Quantity)*2 {
					response.Result, response.Err = m.client.WriteMultipleRegisters(request.Address, request.Quantity, request.WriteData)
				} else {
					response.Err = fmt.Errorf("FC16 yêu cầu %d bytes dữ liệu, nhận được %d", int(request.Quantity)*2, len(request.WriteData))
				}
			case 5: // Write Single Coil
				if len(request.WriteData) == 2 { // Giá trị 0xFF00 hoặc 0x0000
					value := binary.BigEndian.Uint16(request.WriteData)
					response.Result, response.Err = m.client.WriteSingleCoil(request.Address, value)
				} else {
					response.Err = fmt.Errorf("FC05 yêu cầu đúng 2 bytes dữ liệu (0xFF00 hoặc 0x0000)")
				}
			case 15: // Write Multiple Coils
				// Quantity ở đây là số lượng coil, WriteData chứa packed bits
				if len(request.WriteData) > 0 {
					response.Result, response.Err = m.client.WriteMultipleCoils(request.Address, request.Quantity, request.WriteData)
				} else {
					response.Err = fmt.Errorf("FC15 yêu cầu dữ liệu coil (WriteData)")
				}
			default:
				response.Err = fmt.Errorf("function code %d không được hỗ trợ bởi Port Manager", request.FunctionCode)
			}

			m.mu.Unlock() // Mở khóa sau giao dịch

			// 4. Gửi phản hồi
			if response.Err != nil {
				m.log.WithError(response.Err).WithFields(logrus.Fields{
					"slave_id": request.SlaveID, "fc": request.FunctionCode, "addr": request.Address, "qty": request.Quantity,
				}).Error("Port Manager: Giao dịch Modbus thất bại")
			} else {
				m.log.WithFields(logrus.Fields{
					"slave_id": request.SlaveID, "fc": request.FunctionCode, "addr": request.Address, "qty": request.Quantity, "resp_len": len(response.Result),
				}).Debug("Port Manager: Giao dịch Modbus thành công")
			}

			// Gửi phản hồi không chặn, nếu kênh reply đầy thì bỏ qua (goroutine yêu cầu có thể đã timeout)
			select {
			case request.ReplyChan <- response:
			default:
				m.log.Warnf("Kênh phản hồi cho yêu cầu (Slave %d, Addr %d) bị đầy hoặc không có người nhận.", request.SlaveID, request.Address)
			}

			// Delay nhỏ giữa các giao dịch trên cùng một bus
			time.Sleep(30 * time.Millisecond) // Có thể điều chỉnh hoặc đưa vào config
		}
	}
}
