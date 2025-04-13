// Package modbusclient đóng gói logic giao tiếp Modbus cho một thiết bị.
package modbusclient

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	// !!! THAY 'testmod' bằng tên module của bạn !!!
	"testmod/config"
	"testmod/storage"

	"github.com/goburrow/modbus"
	"github.com/sirupsen/logrus"
)

// Device đại diện cho một thiết bị Modbus cần giám sát.
type Device struct {
	config    config.DeviceConfig   // Cấu hình của thiết bị
	registers []config.RegisterInfo // Danh sách thanh ghi đã đọc từ file CSV
	handler   *modbus.RTUClientHandler
	client    modbus.Client
	writer    storage.DataWriter // Interface để ghi dữ liệu (có thể là MultiWriter)
	statusLog *logrus.Entry      // Logger riêng cho trạng thái/lỗi
	mu        sync.Mutex         // Bảo vệ truy cập client/handler
}

// NewDevice tạo một đối tượng Device mới.
func NewDevice(cfg config.DeviceConfig, regs []config.RegisterInfo, writer storage.DataWriter, statusLogger *logrus.Logger) *Device {
	logger := statusLogger.WithField("device", cfg.Name) // Thêm context vào logger
	return &Device{config: cfg, registers: regs, writer: writer, statusLog: logger}
}

// Connect thiết lập kết nối Modbus RTU.
func (d *Device) Connect() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if strings.ToLower(d.config.Connection.Type) != "rtu" {
		return fmt.Errorf("loại kết nối '%s' không được hỗ trợ", d.config.Connection.Type)
	}

	windowsPortPath := d.config.Connection.GetWindowsPortPath()
	d.statusLog.Infof("Đang thử kết nối tới %s (Path: %s)...", d.config.Connection.Port, windowsPortPath)

	if d.handler == nil { // Chỉ khởi tạo lần đầu
		d.handler = modbus.NewRTUClientHandler(windowsPortPath)
		d.handler.BaudRate = d.config.Connection.BaudRate
		d.handler.DataBits = d.config.Connection.DataBits
		d.handler.Parity = d.config.Connection.Parity
		d.handler.StopBits = d.config.Connection.StopBits
		d.handler.SlaveId = d.config.Connection.SlaveID
		d.handler.Timeout = d.config.Connection.GetTimeout()
	} else { // Cập nhật lại thông số nếu cần (ví dụ khi reload config)
		d.handler.SlaveId = d.config.Connection.SlaveID
		d.handler.Timeout = d.config.Connection.GetTimeout()
		// Lưu ý: Thay đổi các thông số serial khác (baud, parity...) sau khi đã tạo handler có thể không hiệu quả
		// Cần tạo handler mới nếu các thông số đó thay đổi.
	}

	err := d.handler.Connect()
	if err != nil {
		d.statusLog.WithError(err).Error("Kết nối Modbus thất bại")
		d.client = nil
		return err
	}
	if d.client == nil {
		d.client = modbus.NewClient(d.handler)
	}
	d.statusLog.Info(">>> Kết nối Modbus thành công!")
	return nil
}

// Close đóng kết nối Modbus.
func (d *Device) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.handler != nil {
		d.statusLog.Info("Đang đóng kết nối Modbus...")
		// d.client = nil // Đặt client về nil
		// Thư viện goburrow không có IsClose() công khai, cứ gọi Close()
		return d.handler.Close()
	}
	return nil
}

// RunPollingLoop là vòng lặp chính để đọc dữ liệu định kỳ.
func (d *Device) RunPollingLoop(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	d.statusLog.Info("Bắt đầu vòng lặp đọc dữ liệu...")
	ticker := time.NewTicker(d.config.GetPollingInterval())
	defer ticker.Stop()

	for {
		// Thử kết nối nếu client đang nil
		d.mu.Lock()
		clientNeedsConnect := (d.client == nil)
		d.mu.Unlock()
		if clientNeedsConnect {
			if err := d.Connect(); err != nil {
				// Lỗi đã log, đợi lần tick sau
				select {
				case <-time.After(5 * time.Second): // Đợi 5s trước khi thử lại ngay lập tức
					continue
				case <-ctx.Done():
					d.statusLog.Info("Nhận tín hiệu dừng trong khi chờ kết nối lại.")
					return
				}
			}
		}

		// Chờ ticker hoặc tín hiệu dừng
		select {
		case <-ctx.Done():
			d.statusLog.Info("Nhận tín hiệu dừng, kết thúc vòng lặp đọc.")
			d.Close()
			return
		case <-ticker.C:
			d.mu.Lock() // Lock để lấy client
			currentClient := d.client
			d.mu.Unlock()

			if currentClient == nil {
				d.statusLog.Warn("Bỏ qua chu kỳ đọc vì client là nil (đang chờ kết nối lại).")
				continue
			}

			startTime := time.Now()
			data := d.readAllRegisters(currentClient) // Gọi hàm đọc
			readDuration := time.Since(startTime)

			if d.writer != nil {
				err := d.writer.WriteData(d.config.Name, d.config.Tags, data, startTime)
				if err != nil {
					d.statusLog.WithError(err).Error("Lỗi khi ghi dữ liệu bằng DataWriter")
				} else {
					d.statusLog.WithField("duration_ms", readDuration.Milliseconds()).Info("Hoàn thành chu kỳ đọc và ghi dữ liệu")
				}
			} else {
				d.statusLog.Warn("Không có DataWriter nào được cấu hình để ghi dữ liệu.")
			}
		}
	}
}

// readAllRegisters đọc tất cả thanh ghi được định nghĩa.
func (d *Device) readAllRegisters(client modbus.Client) map[string]interface{} {
	results := make(map[string]interface{})
	if len(d.registers) == 0 {
		return results
	}

	addressBase := d.config.Connection.AddressBase

	for _, regInfo := range d.registers {
		address_1based := regInfo.Address
		readCount := regInfo.Length
		if address_1based < uint16(addressBase) {
			d.statusLog.Errorf("Địa chỉ cấu hình %d (%s) nhỏ hơn addressBase %d", address_1based, regInfo.Name, addressBase)
			results[regInfo.Name] = "INVALID_ADDR_CFG"
			continue
		}
		address_0based := address_1based - uint16(addressBase)

		d.statusLog.WithFields(logrus.Fields{
			"register_name": regInfo.Name, "address_1based": address_1based, "address_0based": address_0based,
			"count_regs": readCount, "data_type": regInfo.Type,
		}).Debug("Chuẩn bị đọc thanh ghi/cụm")

		var readBytes []byte
		var err error
		// *** TODO: Thêm logic chọn hàm đọc dựa vào regInfo.RegisterType nếu cấu hình hỗ trợ ***
		// Hiện tại chỉ đọc Holding Registers
		readBytes, err = client.ReadHoldingRegisters(address_0based, readCount)

		if err != nil {
			d.handleModbusError(err)
			results[regInfo.Name] = "READ_ERROR"
			time.Sleep(50 * time.Millisecond)
			continue
		}
		expectedBytes := int(readCount) * 2
		if len(readBytes) != expectedBytes {
			d.statusLog.WithFields(logrus.Fields{
				"register_name": regInfo.Name, "address_0based": address_0based, "count_regs": readCount,
				"received_bytes": len(readBytes), "expected_bytes": expectedBytes,
			}).Error("Lỗi độ dài dữ liệu đọc")
			results[regInfo.Name] = "LENGTH_ERROR"
			continue
		}
		decodedValue, decodeErr := d.decodeBytes(readBytes, regInfo)
		if decodeErr != nil {
			d.statusLog.WithError(decodeErr).WithFields(logrus.Fields{
				"register_name": regInfo.Name, "raw_bytes_hex": fmt.Sprintf("%x", readBytes),
			}).Error("Lỗi giải mã thanh ghi")
			results[regInfo.Name] = "DECODE_ERROR"
		} else {
			results[regInfo.Name] = decodedValue
		}
		time.Sleep(20 * time.Millisecond)
	}
	return results
}

// decodeBytes giải mã dữ liệu byte dựa trên RegisterInfo.
func (d *Device) decodeBytes(data []byte, regInfo config.RegisterInfo) (interface{}, error) {
	byteOrder := binary.BigEndian
	d.statusLog.WithFields(logrus.Fields{ // Dùng statusLog cho debug giải mã
		"register_name": regInfo.Name, "data_type": regInfo.Type,
		"raw_bytes_hex": fmt.Sprintf("%x", data), "byte_length": len(data),
	}).Debug("Giải mã dữ liệu thanh ghi")

	switch regInfo.Type {
	case "FLOAT32":
		if len(data) != 4 {
			return nil, fmt.Errorf("FLOAT32 cần 4 bytes, nhận %d", len(data))
		}
		bits := byteOrder.Uint32(data)
		if bits == 0xFFC00000 {
			return "N/A_FLOAT32", nil
		}
		return math.Float32frombits(bits), nil
	case "INT16U":
		if len(data) != 2 {
			return nil, fmt.Errorf("INT16U cần 2 bytes, nhận %d", len(data))
		}
		val := byteOrder.Uint16(data)
		if val == 0xFFFF {
			return "N/A_INT16U", nil
		}
		return val, nil
	case "INT16":
		if len(data) != 2 {
			return nil, fmt.Errorf("INT16 cần 2 bytes, nhận %d", len(data))
		}
		val := byteOrder.Uint16(data)
		if val == 0x8000 {
			return "N/A_INT16", nil
		}
		return int16(val), nil
	case "INT32U":
		if len(data) != 4 {
			return nil, fmt.Errorf("INT32U cần 4 bytes, nhận %d", len(data))
		}
		val := byteOrder.Uint32(data)
		if val == 0xFFFFFFFF {
			return "N/A_INT32U", nil
		}
		return val, nil
	case "INT32":
		if len(data) != 4 {
			return nil, fmt.Errorf("INT32 cần 4 bytes, nhận %d", len(data))
		}
		val := byteOrder.Uint32(data)
		if val == 0x80000000 {
			return "N/A_INT32", nil
		}
		return int32(val), nil
	case "INT64":
		if len(data) != 8 {
			return nil, fmt.Errorf("INT64 cần 8 bytes, nhận %d", len(data))
		}
		val := byteOrder.Uint64(data)
		if val == 0x8000000000000000 {
			return "N/A_INT64", nil
		}
		return int64(val), nil
	case "FLOAT64":
		if len(data) != 8 {
			return nil, fmt.Errorf("FLOAT64 cần 8 bytes, nhận %d", len(data))
		}
		bits := byteOrder.Uint64(data)
		if bits == 0xFFF8000000000000 {
			return "N/A_FLOAT64", nil
		}
		return math.Float64frombits(bits), nil
	case "UTF8":
		expectedLen := int(regInfo.Length) * 2
		if len(data) != expectedLen {
			if len(data) > expectedLen || len(data)%2 != 0 {
				return nil, fmt.Errorf("UTF8 length %d cần %d bytes (hoặc ít hơn, chẵn), nhận %d", regInfo.Length, expectedLen, len(data))
			}
			d.statusLog.WithFields(logrus.Fields{"register_name": regInfo.Name, "expected_bytes": expectedLen, "received_bytes": len(data)}).Warn("UTF8 nhận được ít byte hơn mong đợi")
		}
		isGarbled := true
		if len(data) >= 2 {
			for i := 0; i < len(data); i += 2 {
				if i+1 < len(data) {
					if byteOrder.Uint16(data[i:i+2]) != 0x8000 {
						isGarbled = false
						break
					}
				} else {
					isGarbled = false
					break
				}
			}
		} else {
			isGarbled = false
		}
		if isGarbled && len(data) > 0 {
			d.statusLog.WithFields(logrus.Fields{"register_name": regInfo.Name, "raw_bytes_hex": fmt.Sprintf("%x", data)}).Warn("Phát hiện dữ liệu UTF8 không hợp lệ (pattern 0x8000)")
			return "INVALID_UTF8_DATA", nil
		}
		decodedString := strings.TrimRight(string(data), "\x00")
		if strings.ContainsRune(decodedString, '\uFFFD') {
			d.statusLog.WithFields(logrus.Fields{"register_name": regInfo.Name, "raw_bytes_hex": fmt.Sprintf("%x", data), "decoded_string": decodedString}).Warn("Chuỗi UTF8 giải mã có thể chứa ký tự không hợp lệ")
		}
		return decodedString, nil
	case "DATETIME": // IEC 870-5-4
		if len(data) != 8 {
			return nil, fmt.Errorf("DATETIME IEC 870-5-4 cần 8 bytes, nhận %d", len(data))
		}
		if byteOrder.Uint64(data) == 0xFFFFFFFFFFFFFFFF {
			return "N/A_DATETIME", nil
		}
		word1 := byteOrder.Uint16(data[0:2])
		word2 := byteOrder.Uint16(data[2:4])
		word3 := byteOrder.Uint16(data[4:6])
		word4 := byteOrder.Uint16(data[6:8])
		year7bit := int(word1 & 0x7F)
		year := 2000 + year7bit
		day := int((word2 >> 0) & 0x1F)
		month := int((word2 >> 8) & 0x0F)
		minute := int((word3 >> 0) & 0x3F)
		hour := int((word3 >> 8) & 0x1F)
		millisecond := int(word4)
		d.statusLog.WithFields(logrus.Fields{"register_name": regInfo.Name, "year": year, "month": month, "day": day, "hour": hour, "minute": minute, "millisecond": millisecond, "raw_bytes_hex": fmt.Sprintf("%x", data)}).Debug("Giải mã DATETIME (IEC 870-5-4)")
		if month == 0 || month > 12 || day == 0 || day > 31 || hour > 23 || minute > 59 || millisecond > 59999 || year < 1970 || year > 2127 {
			d.statusLog.WithFields(logrus.Fields{"register_name": regInfo.Name, "year": year, "month": month, "day": day, "hour": hour, "minute": minute, "millisecond": millisecond}).Warn("Giá trị DATETIME (IEC) đọc được không hợp lệ")
			return fmt.Sprintf("INVALID_IEC_DATE(Y:%d M:%d D:%d)", year, month, day), nil
		}
		dt := time.Date(year, time.Month(month), day, hour, minute, 0, millisecond*1000000, time.Local)
		return dt.Format("2006-01-02 15:04:00.000"), nil
	// Bỏ CUSTOM_PF vì đã đổi thành FLOAT32 trong danh sách thanh ghi
	// case "CUSTOM_PF": ...
	default:
		d.statusLog.Warnf("Kiểu dữ liệu '%s' cho thanh ghi '%s' chưa được hỗ trợ giải mã.", regInfo.Type, regInfo.Name)
		return fmt.Sprintf("UNSUPPORTED_TYPE(%s)", regInfo.Type), nil
	}
}

// handleModbusError xử lý và log lỗi Modbus dùng logger của device.
func (d *Device) handleModbusError(err error) {
	timeoutMs := d.config.Connection.TimeoutMs
	slaveID := d.config.Connection.SlaveID
	logger := d.statusLog // Sử dụng status logger

	if mbErr, ok := err.(*modbus.ModbusError); ok {
		logger.WithError(err).WithFields(logrus.Fields{
			"slave_id": int(slaveID), "exception_code": mbErr.ExceptionCode, "exception_msg": getModbusExceptionMessage(mbErr.ExceptionCode),
		}).Error("Lỗi Modbus từ Slave")
	} else if os.IsTimeout(err) {
		logger.WithError(err).WithFields(logrus.Fields{
			"slave_id": int(slaveID), "timeout_ms": timeoutMs,
		}).Warn("Timeout khi chờ phản hồi từ Slave (os.IsTimeout)")
	} else if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		logger.WithError(err).WithFields(logrus.Fields{
			"slave_id": int(slaveID), "timeout_ms": timeoutMs,
		}).Warn("Timeout mạng khi chờ phản hồi từ Slave (net.Error)")
	} else {
		logger.WithError(err).WithField("error_type", fmt.Sprintf("%T", err)).Warn("Lỗi giao tiếp khác")
	}
}

// getModbusExceptionMessage trả về mô tả lỗi (hàm helper chung).
func getModbusExceptionMessage(code byte) string {
	switch code {
	case modbus.ExceptionCodeIllegalFunction:
		return "Illegal Function"
	case modbus.ExceptionCodeIllegalDataAddress:
		return "Illegal Data Address"
	case modbus.ExceptionCodeIllegalDataValue:
		return "Illegal Data Value"
	case modbus.ExceptionCodeServerDeviceFailure:
		return "Server Device Failure"
	case modbus.ExceptionCodeAcknowledge:
		return "Acknowledge"
	case modbus.ExceptionCodeServerDeviceBusy:
		return "Server Device Busy"
	case modbus.ExceptionCodeGatewayPathUnavailable:
		return "Gateway Path Unavailable"
	case modbus.ExceptionCodeGatewayTargetDeviceFailedToRespond:
		return "Gateway Target Device Failed To Respond"
	default:
		return "Unknown Exception Code (" + strconv.Itoa(int(code)) + ")"
	}
}
