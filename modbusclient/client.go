// Package modbusclient đóng gói logic cho một thiết bị Modbus logic.
package modbusclient

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog" // <<< Sử dụng slog
	"math"
	"math/rand"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	// !!! THAY 'modbus_register_slave' bằng tên module của bạn !!!
	"modbus_register_slave/config"
	"modbus_register_slave/portmanager"
	"modbus_register_slave/storage"

	"github.com/goburrow/modbus"
)

// UIUpdate định nghĩa cấu trúc để gửi cập nhật lên UI hoặc Console Printer
type UIUpdate struct {
	DeviceName string
	Timestamp  time.Time
	Data       map[string]interface{}
	IsStatus   bool
	StatusMsg  string
	IsError    bool
}

// Device đại diện cho một thiết bị Modbus logic (theo Slave ID).
type Device struct {
	config       config.DeviceConfig
	registers    []config.RegisterInfo
	writer       storage.DataWriter
	statusLog    *slog.Logger // <<< Đổi sang slog.Logger
	requestChan  chan<- portmanager.ModbusRequest
	uiUpdateChan chan<- UIUpdate // Kênh gửi cập nhật (có thể là nil)
	mu           sync.Mutex
}

// NewDevice tạo một đối tượng Device mới.
func NewDevice(cfg config.DeviceConfig, regs []config.RegisterInfo, writer storage.DataWriter, statusLogger *slog.Logger, reqChan chan<- portmanager.ModbusRequest, uiChan chan<- UIUpdate) *Device {
	logArgs := []any{slog.String("device", cfg.Name)}
	for k, v := range cfg.Tags {
		logArgs = append(logArgs, slog.String(fmt.Sprintf("tag_%s", k), v))
	}
	logger := statusLogger.With(logArgs...)
	return &Device{config: cfg, registers: regs, writer: writer, statusLog: logger, requestChan: reqChan, uiUpdateChan: uiChan}
}

// sendStatusUpdate gửi thông báo trạng thái/lỗi lên kênh UI nếu kênh tồn tại và log bằng slog.
func (d *Device) sendStatusUpdate(message string, isError bool) {
	if d.uiUpdateChan != nil {
		update := UIUpdate{DeviceName: d.config.Name, Timestamp: time.Now(), IsStatus: true, StatusMsg: message, IsError: isError}
		select {
		case d.uiUpdateChan <- update:
		default:
			d.statusLog.Warn("Kênh cập nhật UI/Console bị đầy, bỏ qua thông báo trạng thái.")
		}
	}
	if isError {
		d.statusLog.Error(message)
	} else {
		d.statusLog.Info(message)
	}
}

// sendDataUpdate gửi dữ liệu đọc được lên kênh UI nếu kênh tồn tại.
func (d *Device) sendDataUpdate(data map[string]interface{}, timestamp time.Time) {
	if d.uiUpdateChan != nil {
		update := UIUpdate{DeviceName: d.config.Name, Timestamp: timestamp, Data: data, IsStatus: false}
		select {
		case d.uiUpdateChan <- update:
		default:
			d.statusLog.Warn("Kênh cập nhật UI/Console bị đầy, bỏ qua cập nhật dữ liệu.")
		}
	}
}

// RunPollingLoop là vòng lặp chính để đọc dữ liệu định kỳ.
func (d *Device) RunPollingLoop(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	d.sendStatusUpdate("Bắt đầu vòng lặp đọc dữ liệu...", false)
	jitter := time.Duration(rand.Intn(200)) * time.Millisecond
	initialDelay := 100*time.Millisecond + jitter
	time.Sleep(initialDelay)
	ticker := time.NewTicker(d.config.GetPollingInterval() + jitter)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			d.sendStatusUpdate("Nhận tín hiệu dừng, kết thúc vòng lặp đọc.", false)
			return
		case <-ticker.C:
			startTime := time.Now()
			data := d.readAllRegistersViaManager(ctx)
			readDuration := time.Since(startTime)

			if data != nil {
				d.sendDataUpdate(data, startTime)
				if d.writer != nil {
					err := d.writer.WriteData(d.config.Name, d.config.Tags, data, startTime)
					if err != nil {
						d.sendStatusUpdate(fmt.Sprintf("Lỗi ghi dữ liệu: %v", err), true)
					} else {
						errorCount := 0
						for _, v := range data {
							if strVal, ok := v.(string); ok {
								if strings.Contains(strVal, "ERROR") || strings.Contains(strVal, "INVALID") || strings.Contains(strVal, "N/A_") {
									errorCount++
								}
							}
						}
						statusMsg := fmt.Sprintf("Hoàn thành chu kỳ đọc (Lỗi thanh ghi: %d). Duration: %d ms", errorCount, readDuration.Milliseconds())
						d.sendStatusUpdate(statusMsg, errorCount > 0)
					}
				} else {
					d.statusLog.Warn("Không có DataWriter nào được cấu hình để ghi dữ liệu.")
				}
			} else {
				d.statusLog.Warn("Chu kỳ đọc bị hủy, không ghi dữ liệu.")
			}
		}
	}
}

// readAllRegistersViaManager gửi yêu cầu đọc tuần tự tới Port Manager
func (d *Device) readAllRegistersViaManager(ctx context.Context) map[string]interface{} {
	results := make(map[string]interface{})
	if len(d.registers) == 0 {
		return results
	}
	addressBase := d.config.Connection.AddressBase

	for _, regInfo := range d.registers {
		select {
		case <-ctx.Done():
			d.statusLog.Info("Context bị hủy trong quá trình đọc thanh ghi.")
			return nil
		default:
		}

		address_1based := regInfo.Address
		readCount := regInfo.Length
		if address_1based < uint16(addressBase) {
			errMsg := fmt.Sprintf("Địa chỉ cấu hình %d (%s) nhỏ hơn addressBase %d", address_1based, regInfo.Name, addressBase)
			d.statusLog.Error(errMsg)
			results[regInfo.Name] = "INVALID_ADDR_CFG"
			continue
		}
		address_0based := address_1based - uint16(addressBase)

		reqArgs := []any{"register_name", regInfo.Name, "address_0based", address_0based, "count_regs", readCount}
		d.statusLog.Debug("Chuẩn bị gửi yêu cầu đọc tới Port Manager", reqArgs...)

		replyChan := make(chan portmanager.ModbusResponse, 1)
		request := portmanager.ModbusRequest{
			SlaveID:      d.config.Connection.SlaveID,
			Address:      address_0based,
			Quantity:     readCount,
			FunctionCode: 3, // Mặc định đọc Holding Register (FC03)
			ReplyChan:    replyChan,
		}
		// TODO: Xác định Function Code (FC04?) dựa trên regInfo.RegisterType hoặc address range

		var decodedValue interface{} = "INIT_ERROR"
		select {
		case d.requestChan <- request:
			d.statusLog.Debug("Đã gửi yêu cầu, đang chờ phản hồi trên replyChan...", reqArgs...)
			replyTimeout := d.config.Connection.GetTimeout() + 2000*time.Millisecond
			select {
			case response := <-replyChan:
				d.statusLog.Debug("Đã nhận phản hồi từ Port Manager.", append(reqArgs, slog.Any("error", response.Err))...)
				if response.Err != nil {
					d.handleModbusError(response.Err, regInfo.Name, address_0based)
					decodedValue = "READ_ERROR"
				} else {
					expectedBytes := int(readCount) * 2
					if len(response.Result) != expectedBytes {
						d.statusLog.Error("Lỗi độ dài dữ liệu đọc (từ Port Manager)", append(reqArgs, slog.Int("received_bytes", len(response.Result)), slog.Int("expected_bytes", expectedBytes))...)
						decodedValue = "LENGTH_ERROR"
					} else {
						var decodeErr error
						decodedValue, decodeErr = d.decodeBytes(response.Result, regInfo)
						if decodeErr != nil {
							d.statusLog.Error("Lỗi giải mã thanh ghi", append(reqArgs, slog.String("raw_bytes_hex", fmt.Sprintf("%x", response.Result)), slog.Any("error", decodeErr))...)
							decodedValue = "DECODE_ERROR"
						}
					}
				}
			case <-time.After(replyTimeout):
				d.statusLog.Error("Timeout khi chờ phản hồi từ Port Manager", append(reqArgs, slog.Duration("timeout", replyTimeout))...)
				decodedValue = "MANAGER_TIMEOUT"
			case <-ctx.Done():
				d.statusLog.Warn("Bị hủy khi đang chờ phản hồi", reqArgs...)
				decodedValue = "CANCELLED"
				return nil
			}
		case <-ctx.Done():
			d.statusLog.Warn("Bị hủy trước khi gửi yêu cầu", reqArgs...)
			decodedValue = "CANCELLED"
			return nil
		case <-time.After(2 * time.Second):
			d.statusLog.Warn("Timeout khi gửi yêu cầu vào channel của Port Manager", reqArgs...)
			decodedValue = "CHANNEL_TIMEOUT"
		}
		results[regInfo.Name] = decodedValue
	}
	return results
}

// decodeBytes giải mã dữ liệu byte dựa trên RegisterInfo.
func (d *Device) decodeBytes(data []byte, regInfo config.RegisterInfo) (interface{}, error) {
	byteOrder := binary.BigEndian
	logArgs := []any{"register_name", regInfo.Name, "data_type", regInfo.Type, "raw_bytes_hex", fmt.Sprintf("%x", data), "byte_length", len(data)}
	d.statusLog.Debug("Giải mã dữ liệu thanh ghi", logArgs...)

	switch regInfo.Type {
	case "FLOAT32":
		if len(data) != 4 {
			return nil, fmt.Errorf("FLOAT32 cần 4 bytes, nhận %d", len(data))
		}
		bits := byteOrder.Uint32(data)
		if bits == 0xFFC00000 {
			d.statusLog.Warn("Giá trị N/A (0xFFC00000) được trả về cho FLOAT32", "register_name", regInfo.Name)
			return float32(0.0), nil
		}
		return math.Float32frombits(bits), nil
	case "INT16U":
		if len(data) != 2 {
			return nil, fmt.Errorf("INT16U cần 2 bytes, nhận %d", len(data))
		}
		val := byteOrder.Uint16(data)
		if val == 0xFFFF {
			d.statusLog.Warn("Giá trị N/A (0xFFFF) được trả về cho INT16U", "register_name", regInfo.Name)
			return uint16(0), nil
		}
		return val, nil
	case "INT16":
		if len(data) != 2 {
			return nil, fmt.Errorf("INT16 cần 2 bytes, nhận %d", len(data))
		}
		val := byteOrder.Uint16(data)
		if val == 0x8000 {
			d.statusLog.Warn("Giá trị N/A (0x8000) được trả về cho INT16", "register_name", regInfo.Name)
			return int16(0), nil
		}
		return int16(val), nil
	case "INT32U":
		if len(data) != 4 {
			return nil, fmt.Errorf("INT32U cần 4 bytes, nhận %d", len(data))
		}
		val := byteOrder.Uint32(data)
		if val == 0xFFFFFFFF {
			d.statusLog.Warn("Giá trị N/A (0xFFFFFFFF) được trả về cho INT32U", "register_name", regInfo.Name)
			return uint32(0), nil
		}
		return val, nil
	case "INT32":
		if len(data) != 4 {
			return nil, fmt.Errorf("INT32 cần 4 bytes, nhận %d", len(data))
		}
		val := byteOrder.Uint32(data)
		if val == 0x80000000 {
			d.statusLog.Warn("Giá trị N/A (0x80000000) được trả về cho INT32", "register_name", regInfo.Name)
			return int32(0), nil
		}
		return int32(val), nil
	case "INT64":
		if len(data) != 8 {
			return nil, fmt.Errorf("INT64 cần 8 bytes, nhận %d", len(data))
		}
		val := byteOrder.Uint64(data)
		if val == 0x8000000000000000 {
			d.statusLog.Warn("Giá trị N/A (0x8000...) được trả về cho INT64", "register_name", regInfo.Name)
			return int64(0), nil
		}
		return int64(val), nil
	case "FLOAT64":
		if len(data) != 8 {
			return nil, fmt.Errorf("FLOAT64 cần 8 bytes, nhận %d", len(data))
		}
		bits := byteOrder.Uint64(data)
		if bits == 0xFFF8000000000000 {
			d.statusLog.Warn("Giá trị N/A (0xFFF8...) được trả về cho FLOAT64", "register_name", regInfo.Name)
			return float64(0.0), nil
		}
		return math.Float64frombits(bits), nil
	case "UTF8":
		expectedLen := int(regInfo.Length) * 2
		if len(data) != expectedLen {
			if len(data) > expectedLen || len(data)%2 != 0 {
				return nil, fmt.Errorf("UTF8 length %d cần %d bytes (hoặc ít hơn, chẵn), nhận %d", regInfo.Length, expectedLen, len(data))
			}
			d.statusLog.Warn("UTF8 nhận được ít byte hơn mong đợi", "register_name", regInfo.Name, "expected_bytes", expectedLen, "received_bytes", len(data))
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
			d.statusLog.Warn("Phát hiện dữ liệu UTF8 không hợp lệ (pattern 0x8000)", "register_name", regInfo.Name, "raw_bytes_hex", fmt.Sprintf("%x", data))
			return "INVALID_UTF8_DATA", nil
		}
		decodedString := strings.TrimRight(string(data), "\x00")
		if len(decodedString) == 0 && len(data) > 0 {
			allZeros := true
			for _, b := range data {
				if b != 0x00 {
					allZeros = false
					break
				}
			}
			if allZeros {
				d.statusLog.Warn("Giá trị N/A (0x00) được trả về cho UTF8", "register_name", regInfo.Name)
				return "", nil
			}
		}
		if strings.ContainsRune(decodedString, '\uFFFD') {
			d.statusLog.Warn("Chuỗi UTF8 giải mã có thể chứa ký tự không hợp lệ", "register_name", regInfo.Name, "raw_bytes_hex", fmt.Sprintf("%x", data), "decoded_string", decodedString)
		}
		return decodedString, nil
	case "DATETIME": // IEC 870-5-4
		if len(data) != 8 {
			return nil, fmt.Errorf("DATETIME IEC 870-5-4 cần 8 bytes, nhận %d", len(data))
		}
		if byteOrder.Uint64(data) == 0xFFFFFFFFFFFFFFFF {
			d.statusLog.Warn("Giá trị N/A (0xFFFF...) được trả về cho DATETIME", "register_name", regInfo.Name)
			return "", nil
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
		dtArgs := []any{"register_name", regInfo.Name, "year", year, "month", month, "day", day, "hour", hour, "minute", minute, "millisecond", millisecond, "raw_bytes_hex", fmt.Sprintf("%x", data)}
		d.statusLog.Debug("Giải mã DATETIME (IEC 870-5-4)", dtArgs...)
		if month == 0 || month > 12 || day == 0 || day > 31 || hour > 23 || minute > 59 || millisecond > 59999 || year < 1970 || year > 2127 {
			d.statusLog.Warn("Giá trị DATETIME (IEC) đọc được không hợp lệ", dtArgs...)
			return fmt.Sprintf("INVALID_IEC_DATE(Y:%d M:%d D:%d)", year, month, day), nil
		}
		dt := time.Date(year, time.Month(month), day, hour, minute, 0, millisecond*1000000, time.Local)
		return dt.Format("2006-01-02 15:04:00.000"), nil

	case "CUSTOM_PF": // Length=2 (4 bytes) theo register list
		if len(data) != 4 {
			return nil, fmt.Errorf("CUSTOM_PF cần 4 bytes (Length=2), nhận %d", len(data))
		}
		rawValue := byteOrder.Uint16(data[0:2])
		signedValue := int16(rawValue)
		scalingFactor := 10000.0 // !!! Giả định !!!
		regValFloat := float64(signedValue) / scalingFactor
		d.statusLog.Debug("Giải mã CUSTOM_PF (Dùng 2 byte đầu / Length=2, Scaling Factor là giả định)",
			"register_name", regInfo.Name, "raw_uint16_used", rawValue,
			"ignored_bytes_hex", fmt.Sprintf("%x", data[2:4]),
			"scaled_float", regValFloat, "scaling_factor_assumed", scalingFactor)
		var pfValue float64
		epsilon := 0.00001
		if regValFloat > 1.0 {
			pfValue = 2.0 - regValFloat
		} else if regValFloat < -1.0 {
			pfValue = -2.0 - regValFloat
		} else if math.Abs(regValFloat-1.0) < epsilon || math.Abs(regValFloat-(-1.0)) < epsilon {
			pfValue = regValFloat
		} else {
			pfValue = regValFloat
		}
		return pfValue, nil

	// *** THÊM CASE CHO BITMAP ***
	case "BITMAP16": // Đọc 1 thanh ghi (Length=1), trả về uint16
		if len(data) != 2 {
			return nil, fmt.Errorf("BITMAP16 cần 2 bytes (Length=1), nhận %d", len(data))
		}
		val := byteOrder.Uint16(data)
		if val == 0xFFFF { // Giả định 0xFFFF là N/A cho BITMAP16 (giống INT16U)
			d.statusLog.Warn("Giá trị N/A (0xFFFF) được trả về cho BITMAP16", "register_name", regInfo.Name)
			return uint16(0), nil
		}
		return val, nil // Trả về giá trị uint16 thô

	case "BITMAP32": // Đọc 2 thanh ghi (Length=2), trả về uint32
		if len(data) != 4 {
			return nil, fmt.Errorf("BITMAP32 cần 4 bytes (Length=2), nhận %d", len(data))
		}
		val := byteOrder.Uint32(data)
		if val == 0xFFFFFFFF { // Giả định 0xFFFFFFFF là N/A cho BITMAP32 (giống INT32U)
			d.statusLog.Warn("Giá trị N/A (0xFFFFFFFF) được trả về cho BITMAP32", "register_name", regInfo.Name)
			return uint32(0), nil
		}
		return val, nil // Trả về giá trị uint32 thô

	default:
		d.statusLog.Warn("Kiểu dữ liệu chưa được hỗ trợ giải mã.", "register_name", regInfo.Name, "data_type", regInfo.Type)
		return fmt.Sprintf("UNSUPPORTED_TYPE(%s)", regInfo.Type), nil
	}
}

// handleModbusError xử lý và log lỗi Modbus dùng logger của device.
func (d *Device) handleModbusError(err error, regName string, regAddr uint16) {
	logger := d.statusLog.With(slog.String("register_name", regName), slog.Uint64("register_addr_0based", uint64(regAddr)))
	if err == nil {
		return
	}
	errMsgStr := fmt.Sprintf("Lỗi Modbus khi đọc %s: %v", regName, err)
	d.sendStatusUpdate(errMsgStr, true)

	if mbErr, ok := err.(*modbus.ModbusError); ok {
		logger.Error("Lỗi Modbus từ Slave", slog.Int("slave_id", int(d.config.Connection.SlaveID)), slog.Int("exception_code", int(mbErr.ExceptionCode)), slog.String("exception_msg", getModbusExceptionMessage(mbErr.ExceptionCode)), slog.Any("error", err))
	} else if os.IsTimeout(err) {
		logger.Warn("Timeout khi chờ phản hồi từ Slave (os.IsTimeout)", slog.Int("slave_id", int(d.config.Connection.SlaveID)), slog.Duration("timeout", d.config.Connection.GetTimeout()), slog.Any("error", err))
	} else if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		logger.Warn("Timeout mạng khi chờ phản hồi từ Slave (net.Error)", slog.Int("slave_id", int(d.config.Connection.SlaveID)), slog.Duration("timeout", d.config.Connection.GetTimeout()), slog.Any("error", err))
	} else {
		errMsg := err.Error()
		logArgs := []any{slog.String("error_type", fmt.Sprintf("%T", err)), slog.Any("error", err)}
		if strings.Contains(errMsg, "Access is denied") {
			logger.Error("Lỗi truy cập cổng COM bị từ chối", logArgs...)
		} else if strings.Contains(errMsg, "The handle is invalid") {
			logger.Error("Lỗi Handle không hợp lệ (Cổng COM?)", logArgs...)
		} else if strings.Contains(errMsg, "The parameter is incorrect") {
			logger.Error("Lỗi Tham số không chính xác (Cấu hình cổng COM?)", logArgs...)
		} else {
			logger.Warn("Lỗi giao tiếp khác", logArgs...)
		}
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

// GetConfig trả về cấu hình của device
func (d *Device) GetConfig() config.DeviceConfig { return d.config }

// GetRegisters trả về danh sách thanh ghi của device
func (d *Device) GetRegisters() []config.RegisterInfo { return d.registers }

// GetDataWriter trả về data writer của device
func (d *Device) GetDataWriter() storage.DataWriter { return d.writer }

// ReadDataViaManager là hàm public để console mode có thể gọi (wrapper)
func (d *Device) ReadDataViaManager(ctx context.Context) (map[string]interface{}, error) {
	data := d.readAllRegistersViaManager(ctx)
	if data == nil {
		return nil, fmt.Errorf("context cancelled during read")
	}
	for _, v := range data {
		if strVal, ok := v.(string); ok {
			if strings.Contains(strVal, "ERROR") || strings.Contains(strVal, "TIMEOUT") || strings.Contains(strVal, "CANCELLED") {
				return data, fmt.Errorf("one or more registers failed to read")
			}
		}
	}
	return data, nil
}
