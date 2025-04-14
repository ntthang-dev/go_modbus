// Package modbusclient đóng gói logic giao tiếp Modbus cho một thiết bị.
package modbusclient

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"math/rand" // Cần cho Jitter
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
	"github.com/sirupsen/logrus"
)

// Device đại diện cho một thiết bị Modbus logic (theo Slave ID).
type Device struct {
	config      config.DeviceConfig
	registers   []config.RegisterInfo
	writer      storage.DataWriter
	statusLog   *logrus.Entry
	requestChan chan<- portmanager.ModbusRequest
	mu          sync.Mutex
}

// NewDevice tạo một đối tượng Device mới.
func NewDevice(cfg config.DeviceConfig, regs []config.RegisterInfo, writer storage.DataWriter, statusLogger *logrus.Logger, reqChan chan<- portmanager.ModbusRequest) *Device {
	logger := statusLogger.WithField("device", cfg.Name)
	for k, v := range cfg.Tags {
		logger = logger.WithField(fmt.Sprintf("tag_%s", k), v)
	}
	return &Device{config: cfg, registers: regs, writer: writer, statusLog: logger, requestChan: reqChan}
}

// RunPollingLoop là vòng lặp chính để đọc dữ liệu định kỳ.
func (d *Device) RunPollingLoop(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	d.statusLog.Info("Bắt đầu vòng lặp đọc dữ liệu...")
	jitter := time.Duration(rand.Intn(200)) * time.Millisecond
	initialDelay := 100*time.Millisecond + jitter
	time.Sleep(initialDelay)
	ticker := time.NewTicker(d.config.GetPollingInterval() + jitter)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			d.statusLog.Info("Nhận tín hiệu dừng, kết thúc vòng lặp đọc.")
			return
		case <-ticker.C:
			startTime := time.Now()
			data := d.readAllRegistersViaManager(ctx) // Gọi hàm đọc qua Port Manager
			readDuration := time.Since(startTime)

			if data != nil && d.writer != nil {
				err := d.writer.WriteData(d.config.Name, d.config.Tags, data, startTime)
				if err != nil {
					d.statusLog.WithError(err).Error("Lỗi khi ghi dữ liệu bằng DataWriter")
				} else {
					errorCount := 0
					for _, v := range data {
						if strVal, ok := v.(string); ok {
							if strings.Contains(strVal, "ERROR") || strings.Contains(strVal, "INVALID") || strings.Contains(strVal, "N/A_") {
								errorCount++
							}
						}
					}
					d.statusLog.WithFields(logrus.Fields{"duration_ms": readDuration.Milliseconds(), "errors": errorCount}).Info("Hoàn thành chu kỳ đọc và ghi dữ liệu")
				}
			} else if data == nil {
				d.statusLog.Warn("Chu kỳ đọc bị hủy, không ghi dữ liệu.")
			} else {
				d.statusLog.Warn("Không có DataWriter nào được cấu hình để ghi dữ liệu.")
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

	fmt.Printf("\n==================== Đọc thiết bị %s (%s) ====================\n", d.config.Name, time.Now().Format("15:04:05"))
	currentGroupForConsole := ""

	for _, regInfo := range d.registers {
		// --- Logic in header nhóm console ---
		groupGuess := regInfo.Name
		if idx := strings.Index(regInfo.Name, "_"); idx > 0 {
			groupGuess = regInfo.Name[:idx]
			if strings.HasPrefix(regInfo.Name, "PF_") || strings.HasPrefix(regInfo.Name, "DPF_") {
				groupGuess = "PowerFactor"
			}
			if strings.HasPrefix(regInfo.Name, "AE_") || strings.HasPrefix(regInfo.Name, "RE_") || strings.HasPrefix(regInfo.Name, "APE_") {
				groupGuess = "Energy(Inst)"
			}
			if strings.HasPrefix(regInfo.Name, "Accum_") {
				groupGuess = "Energy(Accum)"
			}
			if strings.HasPrefix(regInfo.Name, "RS485_") || strings.HasPrefix(regInfo.Name, "Pwr_Dem_") || strings.HasPrefix(regInfo.Name, "Cur_Dem_") {
				groupGuess = "Settings"
			}
			if strings.HasPrefix(regInfo.Name, "Meter_") || strings.HasPrefix(regInfo.Name, "Manufacturer") {
				groupGuess = "Device Info"
			}
			if strings.HasSuffix(regInfo.Name, "_7reg") || strings.HasPrefix(regInfo.Name, "Peak_Demand_") {
				groupGuess = "Date/Time"
			}
			if strings.HasPrefix(regInfo.Name, "Voltage_Unbalance") {
				groupGuess = "Voltage Unbalance"
			}
			if strings.HasPrefix(regInfo.Name, "Current_Unbalance") {
				groupGuess = "Current Unbalance"
			}
		} else if regInfo.Name == "Frequency" {
			groupGuess = "Frequency"
		}
		if groupGuess != currentGroupForConsole {
			if currentGroupForConsole != "" {
				fmt.Println("------------------------------------------")
			}
			fmt.Printf("--- %s ---\n", groupGuess)
			currentGroupForConsole = groupGuess
		}
		// --- Kết thúc logic nhóm console ---

		select {
		case <-ctx.Done():
			d.statusLog.Info("Context bị hủy trong quá trình đọc thanh ghi.")
			return nil
		default:
		}

		address_1based := regInfo.Address
		readCount := regInfo.Length
		if address_1based < uint16(addressBase) {
			d.statusLog.Errorf("Địa chỉ cấu hình %d (%s) nhỏ hơn addressBase %d", address_1based, regInfo.Name, addressBase)
			results[regInfo.Name] = "INVALID_ADDR_CFG"
			continue
		}
		address_0based := address_1based - uint16(addressBase)

		d.statusLog.WithFields(logrus.Fields{"register_name": regInfo.Name, "address_0based": address_0based, "count_regs": readCount}).Debug("Chuẩn bị gửi yêu cầu đọc tới Port Manager")

		replyChan := make(chan portmanager.ModbusResponse, 1)
		request := portmanager.ModbusRequest{
			SlaveID: d.config.Connection.SlaveID, IsWrite: false,
			Address: address_0based, Quantity: readCount,
			FunctionCode: 3, // Mặc định đọc Holding Register
			ReplyChan:    replyChan,
		}

		var decodedValue interface{} = "INIT_ERROR"
		select {
		case d.requestChan <- request:
			replyTimeout := d.config.Connection.GetTimeout() + 1000*time.Millisecond
			select {
			case response := <-replyChan:
				if response.Err != nil {
					d.handleModbusError(response.Err)
					decodedValue = "READ_ERROR"
				} else {
					expectedBytes := int(readCount) * 2
					if len(response.Result) != expectedBytes {
						d.statusLog.WithFields(logrus.Fields{"register_name": regInfo.Name, "received_bytes": len(response.Result), "expected_bytes": expectedBytes}).Error("Lỗi độ dài dữ liệu đọc (từ Port Manager)")
						decodedValue = "LENGTH_ERROR"
					} else {
						var decodeErr error
						decodedValue, decodeErr = d.decodeBytes(response.Result, regInfo)
						if decodeErr != nil {
							d.statusLog.WithError(decodeErr).WithFields(logrus.Fields{"register_name": regInfo.Name, "raw_bytes_hex": fmt.Sprintf("%x", response.Result)}).Error("Lỗi giải mã thanh ghi")
							decodedValue = "DECODE_ERROR"
						}
					}
				}
			case <-time.After(replyTimeout):
				d.statusLog.WithFields(logrus.Fields{"register_name": regInfo.Name, "timeout_ms": replyTimeout.Milliseconds()}).Error("Timeout khi chờ phản hồi từ Port Manager")
				decodedValue = "MANAGER_TIMEOUT"
			case <-ctx.Done():
				d.statusLog.Warnf("Bị hủy khi đang chờ phản hồi cho thanh ghi %s", regInfo.Name)
				decodedValue = "CANCELLED"
				return nil
			}
		case <-ctx.Done():
			d.statusLog.Warnf("Bị hủy trước khi gửi yêu cầu đọc thanh ghi %s", regInfo.Name)
			decodedValue = "CANCELLED"
			return nil
		case <-time.After(2 * time.Second):
			d.statusLog.Warnf("Timeout khi gửi yêu cầu đọc thanh ghi %s vào channel của Port Manager", regInfo.Name)
			decodedValue = "CHANNEL_TIMEOUT"
		}

		results[regInfo.Name] = decodedValue
		displayValue := "ERROR_STATE"
		prefix := ""
		isErrorOrNA := false
		if strVal, isString := decodedValue.(string); isString {
			if strings.Contains(strVal, "ERROR") || strings.Contains(strVal, "INVALID") || strings.Contains(strVal, "N/A_") || strings.Contains(strVal, "TIMEOUT") || strings.Contains(strVal, "CANCELLED") {
				prefix = "[LỖI/NA] "
				isErrorOrNA = true
			}
		}
		switch v := decodedValue.(type) {
		case float32:
			fv64 := float64(v)
			if math.IsNaN(fv64) || math.IsInf(fv64, 0) {
				prefix = "[NaN] "
				isErrorOrNA = true
			}
		case float64:
			if math.IsNaN(v) || math.IsInf(v, 0) {
				prefix = "[NaN] "
				isErrorOrNA = true
			}
		}
		if isErrorOrNA {
			displayValue = fmt.Sprintf("%v", decodedValue)
		} else {
			switch v := decodedValue.(type) {
			case float32:
				displayValue = fmt.Sprintf("%.4f", v)
			case float64:
				displayValue = fmt.Sprintf("%.4f", v)
			case string:
				displayValue = fmt.Sprintf("%q", v)
			default:
				displayValue = fmt.Sprintf("%v", v)
			}
		}
		fmt.Printf("%-30s: %s%s\n", regInfo.Name, prefix, displayValue)
	}
	fmt.Println("==================================================================")
	return results
}

// decodeBytes giải mã dữ liệu byte dựa trên RegisterInfo.
func (d *Device) decodeBytes(data []byte, regInfo config.RegisterInfo) (interface{}, error) {
	byteOrder := binary.BigEndian
	d.statusLog.WithFields(logrus.Fields{
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
			d.statusLog.WithFields(logrus.Fields{"register_name": regInfo.Name}).Warn("Giá trị N/A (0xFFC00000) được trả về cho FLOAT32")
			return float32(0.0), nil
		}
		return math.Float32frombits(bits), nil
	case "INT16U":
		if len(data) != 2 {
			return nil, fmt.Errorf("INT16U cần 2 bytes, nhận %d", len(data))
		}
		val := byteOrder.Uint16(data)
		if val == 0xFFFF {
			d.statusLog.WithFields(logrus.Fields{"register_name": regInfo.Name}).Warn("Giá trị N/A (0xFFFF) được trả về cho INT16U")
			return uint16(0), nil
		}
		return val, nil
	case "INT16":
		if len(data) != 2 {
			return nil, fmt.Errorf("INT16 cần 2 bytes, nhận %d", len(data))
		}
		val := byteOrder.Uint16(data)
		if val == 0x8000 {
			d.statusLog.WithFields(logrus.Fields{"register_name": regInfo.Name}).Warn("Giá trị N/A (0x8000) được trả về cho INT16")
			return int16(0), nil
		}
		return int16(val), nil
	case "INT32U":
		if len(data) != 4 {
			return nil, fmt.Errorf("INT32U cần 4 bytes, nhận %d", len(data))
		}
		val := byteOrder.Uint32(data)
		if val == 0xFFFFFFFF {
			d.statusLog.WithFields(logrus.Fields{"register_name": regInfo.Name}).Warn("Giá trị N/A (0xFFFFFFFF) được trả về cho INT32U")
			return uint32(0), nil
		}
		return val, nil
	case "INT32":
		if len(data) != 4 {
			return nil, fmt.Errorf("INT32 cần 4 bytes, nhận %d", len(data))
		}
		val := byteOrder.Uint32(data)
		if val == 0x80000000 {
			d.statusLog.WithFields(logrus.Fields{"register_name": regInfo.Name}).Warn("Giá trị N/A (0x80000000) được trả về cho INT32")
			return int32(0), nil
		}
		return int32(val), nil
	case "INT64":
		if len(data) != 8 {
			return nil, fmt.Errorf("INT64 cần 8 bytes, nhận %d", len(data))
		}
		val := byteOrder.Uint64(data)
		if val == 0x8000000000000000 {
			d.statusLog.WithFields(logrus.Fields{"register_name": regInfo.Name}).Warn("Giá trị N/A (0x8000...) được trả về cho INT64")
			return int64(0), nil
		}
		return int64(val), nil
	case "FLOAT64":
		if len(data) != 8 {
			return nil, fmt.Errorf("FLOAT64 cần 8 bytes, nhận %d", len(data))
		}
		bits := byteOrder.Uint64(data)
		if bits == 0xFFF8000000000000 {
			d.statusLog.WithFields(logrus.Fields{"register_name": regInfo.Name}).Warn("Giá trị N/A (0xFFF8...) được trả về cho FLOAT64")
			return float64(0.0), nil
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
		if len(decodedString) == 0 && len(data) > 0 {
			allZeros := true
			for _, b := range data {
				if b != 0x00 {
					allZeros = false
					break
				}
			}
			if allZeros {
				d.statusLog.WithFields(logrus.Fields{"register_name": regInfo.Name}).Warn("Giá trị N/A (0x00) được trả về cho UTF8")
				return "", nil
			}
		}
		if strings.ContainsRune(decodedString, '\uFFFD') {
			d.statusLog.WithFields(logrus.Fields{"register_name": regInfo.Name, "raw_bytes_hex": fmt.Sprintf("%x", data), "decoded_string": decodedString}).Warn("Chuỗi UTF8 giải mã có thể chứa ký tự không hợp lệ")
		}
		return decodedString, nil
	case "DATETIME": // IEC 870-5-4
		if len(data) != 8 {
			return nil, fmt.Errorf("DATETIME IEC 870-5-4 cần 8 bytes, nhận %d", len(data))
		}
		if byteOrder.Uint64(data) == 0xFFFFFFFFFFFFFFFF {
			d.statusLog.WithFields(logrus.Fields{"register_name": regInfo.Name}).Warn("Giá trị N/A (0xFFFF...) được trả về cho DATETIME")
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
		d.statusLog.WithFields(logrus.Fields{"register_name": regInfo.Name, "year": year, "month": month, "day": day, "hour": hour, "minute": minute, "millisecond": millisecond, "raw_bytes_hex": fmt.Sprintf("%x", data)}).Debug("Giải mã DATETIME (IEC 870-5-4)")
		if month == 0 || month > 12 || day == 0 || day > 31 || hour > 23 || minute > 59 || millisecond > 59999 || year < 1970 || year > 2127 {
			d.statusLog.WithFields(logrus.Fields{"register_name": regInfo.Name, "year": year, "month": month, "day": day, "hour": hour, "minute": minute, "millisecond": millisecond}).Warn("Giá trị DATETIME (IEC) đọc được không hợp lệ")
			return fmt.Sprintf("INVALID_IEC_DATE(Y:%d M:%d D:%d)", year, month, day), nil
		}
		dt := time.Date(year, time.Month(month), day, hour, minute, 0, millisecond*1000000, time.Local)
		return dt.Format("2006-01-02 15:04:00.000"), nil

	// *** THÊM LẠI CASE CUSTOM_PF ***
	case "4Q_FP_PF": // Tên kiểu dữ liệu từ nhà sản xuất
		// Đọc 4 bytes (Length=2) nhưng chỉ giải mã 2 byte đầu theo logic cũ
		if len(data) != 4 {
			return nil, fmt.Errorf("4Q_FP_PF cần 4 bytes (Length=2), nhận %d", len(data))
		}
		rawValue := byteOrder.Uint16(data[0:2])
		signedValue := int16(rawValue)
		// !!! GIẢ ĐỊNH SCALING FACTOR - CẦN KIỂM TRA LẠI VỚI THỰC TẾ !!!
		scalingFactor := 10000.0
		regValFloat := float64(signedValue) / scalingFactor
		d.statusLog.WithFields(logrus.Fields{
			"register_name": regInfo.Name, "raw_uint16_used": rawValue,
			"ignored_bytes_hex": fmt.Sprintf("%x", data[2:4]), // Log các byte bị bỏ qua
			"scaled_float":      regValFloat, "scaling_factor_assumed": scalingFactor,
		}).Debug("Giải mã 4Q_FP_PF (Dùng 2 byte đầu / Length=2, Scaling Factor là giả định)")

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

	default:
		d.statusLog.Warnf("Kiểu dữ liệu '%s' cho thanh ghi '%s' chưa được hỗ trợ giải mã.", regInfo.Type, regInfo.Name)
		return fmt.Sprintf("UNSUPPORTED_TYPE(%s)", regInfo.Type), nil
	}
}

// handleModbusError xử lý và log lỗi Modbus dùng logger của device.
func (d *Device) handleModbusError(err error) {
	timeoutMs := d.config.Connection.TimeoutMs
	slaveID := d.config.Connection.SlaveID
	logger := d.statusLog
	if err == nil {
		return
	}
	if mbErr, ok := err.(*modbus.ModbusError); ok {
		logger.WithError(err).WithFields(logrus.Fields{"slave_id": int(slaveID), "exception_code": mbErr.ExceptionCode, "exception_msg": getModbusExceptionMessage(mbErr.ExceptionCode)}).Error("Lỗi Modbus từ Slave")
	} else if os.IsTimeout(err) {
		logger.WithError(err).WithFields(logrus.Fields{"slave_id": int(slaveID), "timeout_ms": timeoutMs}).Warn("Timeout khi chờ phản hồi từ Slave (os.IsTimeout)")
	} else if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		logger.WithError(err).WithFields(logrus.Fields{"slave_id": int(slaveID), "timeout_ms": timeoutMs}).Warn("Timeout mạng khi chờ phản hồi từ Slave (net.Error)")
	} else {
		if strings.Contains(err.Error(), "Access is denied") {
			logger.WithError(err).WithField("error_type", fmt.Sprintf("%T", err)).Error("Lỗi truy cập cổng COM bị từ chối")
		} else if strings.Contains(err.Error(), "The handle is invalid") {
			logger.WithError(err).WithField("error_type", fmt.Sprintf("%T", err)).Error("Lỗi Handle không hợp lệ (Cổng COM?)")
		} else if strings.Contains(err.Error(), "The parameter is incorrect") {
			logger.WithError(err).WithField("error_type", fmt.Sprintf("%T", err)).Error("Lỗi Tham số không chính xác (Cấu hình cổng COM?)")
		} else {
			logger.WithError(err).WithField("error_type", fmt.Sprintf("%T", err)).Warn("Lỗi giao tiếp khác")
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

// --- Cần thêm import này nếu chưa có trong storage package ---
// Hoặc import storage và dùng storage.SanitizeValue
// package storage // Giả sử có file storage/utils.go chẳng hạn

// SanitizeValue xử lý các giá trị đặc biệt (NaN, Inf) và lỗi chuỗi
func SanitizeValue(value interface{}) interface{} {
	if strVal, ok := value.(string); ok {
		if strings.Contains(strVal, "ERROR") || strings.Contains(strVal, "INVALID") || strings.Contains(strVal, "N/A_") {
			return nil
		}
	}
	switch v := value.(type) {
	case float32:
		fv64 := float64(v)
		if math.IsNaN(fv64) || math.IsInf(fv64, 0) {
			return nil
		}
		return math.Round(fv64*10000) / 10000
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil
		}
		return math.Round(v*10000) / 10000
	default:
		return v
	}
}

// // --- Cần thêm import này nếu chưa có ---
//  import "math/rand" // Cần cho Jitter
// // import "sort" // Cần nếu sắp xếp header CSV
