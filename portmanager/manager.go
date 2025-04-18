// Package portmanager quản lý truy cập tuần tự vào một cổng COM vật lý
package portmanager

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog" // Sử dụng slog
	"sync"
	"time"

	// !!! THAY 'modbus_register_slave' bằng tên module của bạn !!!
	"modbus_register_slave/config"

	"github.com/goburrow/modbus"
)

// ModbusRequest đại diện cho một yêu cầu đọc/ghi Modbus
type ModbusRequest struct {
	SlaveID      byte
	FunctionCode int    // FC: 1, 2, 3, 4, 5, 6, 15, 16
	Address      uint16 // Địa chỉ bắt đầu (0-based)
	Quantity     uint16 // Số lượng thanh ghi/coil
	WriteData    []byte // Dữ liệu cần ghi (cho lệnh ghi)
	ReplyChan    chan ModbusResponse
}

// ModbusResponse chứa kết quả hoặc lỗi của một yêu cầu
type ModbusResponse struct {
	Result []byte
	Err    error
}

// Manager quản lý một cổng COM vật lý duy nhất
type Manager struct {
	portName    string
	portCfg     config.ConnectionConfig
	handler     *modbus.RTUClientHandler
	client      modbus.Client
	requestChan chan ModbusRequest
	log         *slog.Logger // *** Đổi sang slog.Logger ***
	mu          sync.Mutex
}

// NewManager tạo một Port Manager mới
func NewManager(portCfg config.ConnectionConfig, statusLogger *slog.Logger) *Manager { // *** Nhận slog.Logger ***
	logger := statusLogger.With(slog.String("port", portCfg.Port))
	return &Manager{
		portName:    portCfg.Port,
		portCfg:     portCfg,
		requestChan: make(chan ModbusRequest, 20),
		log:         logger,
	}
}

// GetRequestChannel trả về kênh để các Device gửi yêu cầu vào
func (m *Manager) GetRequestChannel() chan<- ModbusRequest {
	return m.requestChan
}

// connect (internal) thực hiện kết nối hoặc kiểm tra kết nối
func (m *Manager) connect() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.handler == nil {
		windowsPortPath := m.portCfg.GetWindowsPortPath()
		m.log.Info("Khởi tạo handler", slog.String("path", windowsPortPath))
		m.handler = modbus.NewRTUClientHandler(windowsPortPath)
		m.handler.BaudRate = m.portCfg.BaudRate
		m.handler.DataBits = m.portCfg.DataBits
		m.handler.Parity = m.portCfg.Parity
		m.handler.StopBits = m.portCfg.StopBits
		m.handler.Timeout = m.portCfg.GetTimeout()
	}
	m.log.Debug("Đang gọi handler.Connect()...")
	err := m.handler.Connect()
	if err != nil {
		m.log.Error("Gọi handler.Connect() thất bại", slog.Any("error", err))
		if m.handler != nil {
			m.handler.Close()
			m.handler = nil
		}
		m.client = nil
		return err
	}
	if m.client == nil {
		m.log.Debug("Tạo modbus client mới.")
		m.client = modbus.NewClient(m.handler)
	}
	m.log.Debug("Kết nối handler thành công hoặc đã kết nối.")
	return nil
}

// Close đóng kết nối của Port Manager
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.handler != nil {
		m.log.Info("Đang đóng kết nối Port Manager...")
		err := m.handler.Close()
		m.handler = nil
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

	for {
		select {
		case <-ctx.Done():
			m.log.Info("Port Manager nhận tín hiệu dừng.")
			return
		case request := <-m.requestChan:
			reqArgs := []any{ // Tạo slice any cho slog
				slog.Uint64("slave_id", uint64(request.SlaveID)),
				slog.Int("fc", request.FunctionCode),
				slog.Uint64("addr", uint64(request.Address)),
				slog.Uint64("qty", uint64(request.Quantity)),
			}
			m.log.Debug("Port Manager: Nhận yêu cầu", reqArgs...)
			var response ModbusResponse

			// 1. Kiểm tra và thử kết nối lại nếu cần
			m.mu.Lock()
			clientIsNil := (m.client == nil)
			m.mu.Unlock()
			if clientIsNil {
				m.log.Warn("Client đang nil, thử kết nối lại...")
				if err := m.connect(); err != nil {
					response.Err = fmt.Errorf("Port Manager không thể kết nối lại: %w", err)
					m.log.Error("Gửi lỗi kết nối về cho client", slog.Any("error", response.Err))
					select {
					case request.ReplyChan <- response:
					default:
						m.log.Warn("Kênh phản hồi bị đầy khi gửi lỗi kết nối.", reqArgs...)
					}
					continue
				}
				m.log.Info("Kết nối lại thành công.")
			}

			// 2. Lock để thực hiện giao dịch
			m.mu.Lock()

			if m.client == nil { // Kiểm tra lại client sau khi connect
				m.mu.Unlock()
				response.Err = fmt.Errorf("Port Manager client vẫn là nil sau connect")
				m.log.Error(response.Err.Error())
				select {
				case request.ReplyChan <- response:
				default:
					m.log.Warn("Kênh phản hồi bị đầy khi gửi lỗi client nil.", reqArgs...)
				}
				continue
			}

			m.handler.SlaveId = request.SlaveID
			m.log.Debug("Bắt đầu thực hiện giao dịch Modbus...", reqArgs...)
			startTime := time.Now()

			// 3. Thực hiện đọc hoặc ghi
			switch request.FunctionCode {
			case 3:
				response.Result, response.Err = m.client.ReadHoldingRegisters(request.Address, request.Quantity)
			case 1:
				response.Result, response.Err = m.client.ReadCoils(request.Address, request.Quantity)
			case 2:
				response.Result, response.Err = m.client.ReadDiscreteInputs(request.Address, request.Quantity)
			case 4:
				response.Result, response.Err = m.client.ReadInputRegisters(request.Address, request.Quantity)
			case 5:
				if len(request.WriteData) == 2 {
					value := binary.BigEndian.Uint16(request.WriteData)
					response.Result, response.Err = m.client.WriteSingleCoil(request.Address, value)
				} else {
					response.Err = fmt.Errorf("FC05 yêu cầu đúng 2 bytes dữ liệu (0xFF00 hoặc 0x0000)")
				}
			case 6:
				if len(request.WriteData) == 2 {
					value := binary.BigEndian.Uint16(request.WriteData)
					response.Result, response.Err = m.client.WriteSingleRegister(request.Address, value)
				} else {
					response.Err = fmt.Errorf("FC06 yêu cầu đúng 2 bytes dữ liệu")
				}
			case 15:
				if len(request.WriteData) > 0 {
					response.Result, response.Err = m.client.WriteMultipleCoils(request.Address, request.Quantity, request.WriteData)
				} else {
					response.Err = fmt.Errorf("FC15 yêu cầu dữ liệu coil (WriteData)")
				}
			case 16:
				if len(request.WriteData) == int(request.Quantity)*2 {
					response.Result, response.Err = m.client.WriteMultipleRegisters(request.Address, request.Quantity, request.WriteData)
				} else {
					response.Err = fmt.Errorf("FC16 yêu cầu %d bytes dữ liệu, nhận được %d", int(request.Quantity)*2, len(request.WriteData))
				}
			default:
				response.Err = fmt.Errorf("function code %d không được hỗ trợ", request.FunctionCode)
			}
			duration := time.Since(startTime)
			logAfterCallArgs := append(reqArgs, slog.Duration("duration", duration))

			if response.Err != nil {
				m.log.Error("Giao dịch Modbus thất bại", append(logAfterCallArgs, slog.Any("error", response.Err))...)
			} else {
				m.log.Debug("Giao dịch Modbus thành công", append(logAfterCallArgs, slog.Int("resp_len", len(response.Result)))...)
			}

			m.mu.Unlock() // Mở khóa

			// 4. Gửi phản hồi
			m.log.Debug("Chuẩn bị gửi phản hồi về replyChan...", reqArgs...)
			select {
			case request.ReplyChan <- response:
				m.log.Debug("Đã gửi phản hồi thành công.", reqArgs...)
			default:
				m.log.Warn("Kênh phản hồi bị đầy hoặc không có người nhận.", reqArgs...)
			}

			// Delay nhỏ giữa các giao dịch
			time.Sleep(5 * time.Millisecond)
		}
	}
}

// --- Xóa bỏ code thừa ---
