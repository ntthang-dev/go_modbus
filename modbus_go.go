package main

import (
	"encoding/binary"
	"encoding/csv"
	"fmt"
	"io"
	"log" // Log chuẩn
	"math"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/goburrow/modbus"
	"github.com/sirupsen/logrus" // Structured logging cho Go < 1.21
)

// --- Cấu hình Kết nối (Thiết bị thực trên Windows) ---
const (
	portNameSimple = "COM3"
	baudRate       = 19200
	dataBits       = 8
	parity         = "N"
	stopBits       = 1
	slaveID        = byte(1)
	timeoutMs      = 1000
)

// --- Cấu hình Address Base ---
const (
	addressBase = 1 // Sử dụng địa chỉ 1-based
)

// --- Cấu hình file log ---
const (
	logDir           = "logs_go_final"
	logCSVFile       = "modbus_data_go_%s.csv"
	logJSONFile      = "modbus_data_go_%s.log"
	enableCSVLogging = true
	logLevel         = logrus.InfoLevel // Đổi thành DebugLevel nếu cần xem chi tiết giải mã
)

// --- Định nghĩa cấu trúc thông tin thanh ghi ---
type RegisterInfo struct {
	Name    string
	Address uint16 // Địa chỉ Modbus (1-based theo addressBase=1)
	Type    string // Kiểu dữ liệu ("FLOAT32", "INT16U", "UTF8", "DATETIME", "CUSTOM_PF", "INT32U", "INT16", "INT64")
	Length  uint16 // Số lượng thanh ghi Modbus
}

// --- Danh sách các thanh ghi cần đọc ---
// !!! QUAN TRỌNG: HÃY KIỂM TRA LẠI CÁC ĐỊA CHỈ (1-based) VÀ ĐỘ DÀI (Length) NÀY VỚI TÀI LIỆU THIẾT BỊ !!!
var registersToRead = []RegisterInfo{
	// --- Device Info ---
	{"Meter_Model", 30, "UTF8", 10},  // !!! Xác nhận lại Address/Length/Type !!!
	{"Manufacturer", 70, "UTF8", 10}, // !!! Xác nhận lại Address/Length/Type !!!
	// --- Date/Time ---
	{"Peak_Demand_Date_time", 3804, "DATETIME", 4}, // !!! Xác nhận lại Address/Length/Format !!!
	// --- Energy(Inst) --- // Năng lượng tức thời (Float32)
	{"AE_Delivered", 2700, "FLOAT32", 2},
	{"AE_Received", 2702, "FLOAT32", 2},
	{"AE_Del_Plus_Rec", 2704, "FLOAT32", 2},
	{"AE_Del_Minus_Rec", 2706, "FLOAT32", 2},
	{"RE_Delivered", 2708, "FLOAT32", 2},
	{"RE_Received", 2710, "FLOAT32", 2},
	{"RE_Del_Plus_Rec", 2712, "FLOAT32", 2},
	{"RE_Del_Minus_Rec", 2714, "FLOAT32", 2},
	{"APE_Delivered", 2716, "FLOAT32", 2},
	{"APE_Received", 2718, "FLOAT32", 2},
	{"APE_Del_Plus_Rec", 2720, "FLOAT32", 2},
	{"APE_Del_Minus_Rec", 2722, "FLOAT32", 2},
	// --- Current ---
	{"Current_A", 3000, "FLOAT32", 2},
	{"Current_B", 3002, "FLOAT32", 2},
	{"Current_C", 3004, "FLOAT32", 2},
	{"Current_N", 3006, "FLOAT32", 2},
	{"Current_G", 3008, "FLOAT32", 2},
	{"Current_Avg", 3010, "FLOAT32", 2},
	{"Current_Unbalance_A", 3012, "FLOAT32", 2},
	{"Current_Unbalance_B", 3014, "FLOAT32", 2},
	{"Current_Unbalance_C", 3016, "FLOAT32", 2},
	{"Current_Unbalance_Worst", 3018, "FLOAT32", 2},
	// --- Voltage ---
	{"Voltage_AB", 3020, "FLOAT32", 2},
	{"Voltage_BC", 3022, "FLOAT32", 2},
	{"Voltage_CA", 3024, "FLOAT32", 2},
	{"Voltage_LLAvg", 3026, "FLOAT32", 2},
	{"Voltage_AN", 3028, "FLOAT32", 2},
	{"Voltage_BN", 3030, "FLOAT32", 2},
	{"Voltage_CN", 3032, "FLOAT32", 2},
	{"Voltage_LNAvg", 3036, "FLOAT32", 2}, // Đã sửa địa chỉ
	{"Voltage_Unbalance_AB", 3038, "FLOAT32", 2},
	{"Voltage_Unbalance_BC", 3040, "FLOAT32", 2},
	{"Voltage_Unbalance_CA", 3042, "FLOAT32", 2},
	{"Voltage_Unbalance_LL_Worst", 3044, "FLOAT32", 2},
	{"Voltage_Unbalance_AN", 3046, "FLOAT32", 2},
	{"Voltage_Unbalance_BN", 3048, "FLOAT32", 2},
	{"Voltage_Unbalance_CN", 3050, "FLOAT32", 2},
	{"Voltage_Unbalance_LN_Worst", 3052, "FLOAT32", 2},
	// --- Power ---
	{"ActivePower_A", 3054, "FLOAT32", 2},
	{"ActivePower_B", 3056, "FLOAT32", 2},
	{"ActivePower_C", 3058, "FLOAT32", 2},
	{"ActivePower_Total", 3060, "FLOAT32", 2},
	{"ReactivePower_A", 3062, "FLOAT32", 2},
	{"ReactivePower_B", 3064, "FLOAT32", 2},
	{"ReactivePower_C", 3066, "FLOAT32", 2},
	{"ReactivePower_Total", 3068, "FLOAT32", 2},
	{"ApparentPower_A", 3070, "FLOAT32", 2},
	{"ApparentPower_B", 3072, "FLOAT32", 2},
	{"ApparentPower_C", 3074, "FLOAT32", 2},
	{"ApparentPower_Total", 3076, "FLOAT32", 2},
	// --- PowerFactor ---
	{"PF_A", 3078, "CUSTOM_PF", 2},     // Length=2
	{"PF_B", 3080, "CUSTOM_PF", 2},     // Length=2
	{"PF_C", 3082, "CUSTOM_PF", 2},     // Length=2
	{"PF_Total", 3084, "CUSTOM_PF", 2}, // Length=2
	{"DPF_A", 3086, "CUSTOM_PF", 2},    // Displacement PF, Length=2
	{"DPF_B", 3088, "CUSTOM_PF", 2},
	{"DPF_C", 3090, "CUSTOM_PF", 2},
	{"DPF_Total", 3092, "CUSTOM_PF", 2},
	{"PF_Total_IEC_F32", 3192, "FLOAT32", 2}, // Alternate PF
	{"PF_Total_IEEE_F32", 3194, "FLOAT32", 2},
	{"PF_Total_IEC_I16", 3196, "INT16", 1},
	{"PF_Total_IEEE_I16", 3197, "INT16", 1},
	// --- Frequency ---
	{"Frequency", 3110, "FLOAT32", 2},
	// --- Energy(Accum) --- // Năng lượng Tích lũy (Int64)
	{"Accum_Energy_Reset_Time", 3200, "DATETIME", 4},
	{"Accum_AE_Del", 3204, "INT64", 4},
	{"Accum_AE_Rec", 3208, "INT64", 4},
	{"Accum_AE_Sum", 3212, "INT64", 4},
	{"Accum_AE_Net", 3216, "INT64", 4},
	{"Accum_RE_Del", 3220, "INT64", 4},
	{"Accum_RE_Rec", 3224, "INT64", 4},
	{"Accum_RE_Sum", 3228, "INT64", 4},
	{"Accum_RE_Net", 3232, "INT64", 4},
	{"Accum_APE_Del", 3236, "INT64", 4},
	{"Accum_APE_Rec", 3240, "INT64", 4},
	{"Accum_APE_Sum", 3244, "INT64", 4},
	{"Accum_APE_Net", 3248, "INT64", 4},
	// --- Settings ---
	{"Pwr_Dem_Interval_Dur", 3702, "INT16U", 1},
	{"Cur_Dem_Interval_Dur", 3712, "INT16U", 1},
	{"RS485_Proto", 6500, "INT16U", 1},
	{"RS485_Addr", 6501, "INT16U", 1},
	{"RS485_Baud", 6502, "INT16U", 1},
	{"RS485_Parity", 6503, "INT16U", 1},
}

// Biến toàn cục
var running = true
var csvWriter *csv.Writer
var csvFile *os.File
var logFile *os.File

// --- Hàm xử lý tín hiệu dừng (Ctrl+C) ---
func signalHandler(sig os.Signal) {
	log.Printf("Nhận tín hiệu %v, đang dừng chương trình...", sig)
	running = false
}

// --- Hàm tạo thư mục và file log ---
func setupLogging() error {
	if err := os.MkdirAll(logDir, 0755); err != nil {
		log.Printf("Lỗi tạo thư mục log '%s': %v", logDir, err)
		return err
	}
	ts := time.Now().Format("20060102_150405")

	jsonLogPath := filepath.Join(logDir, fmt.Sprintf(logJSONFile, ts))
	var errLogrus error
	logFile, errLogrus = os.OpenFile(jsonLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if errLogrus == nil {
		mw := io.MultiWriter(os.Stdout, logFile)
		logrus.SetOutput(mw)
		logrus.SetFormatter(&logrus.JSONFormatter{TimestampFormat: time.RFC3339Nano})
		logrus.SetLevel(logLevel)
		log.Printf("Structured log (JSON) sẽ được ghi tại: %s và hiển thị trên Console (Level: %s)", jsonLogPath, logLevel.String())
	} else {
		log.Printf("Lỗi mở file log JSON '%s': %v. Logrus sẽ chỉ ghi ra Console.", jsonLogPath, errLogrus)
		logrus.SetOutput(os.Stdout)
		logrus.SetFormatter(&logrus.TextFormatter{FullTimestamp: true, ForceColors: true})
		logrus.SetLevel(logLevel)
	}

	if enableCSVLogging {
		csvLogPath := filepath.Join(logDir, fmt.Sprintf(logCSVFile, ts))
		var csvErr error
		csvFile, csvErr = os.Create(csvLogPath)
		if csvErr != nil {
			log.Printf("Lỗi tạo file log CSV '%s': %v", csvLogPath, csvErr)
		} else {
			csvWriter = csv.NewWriter(csvFile)
			headers := []string{"Timestamp"}
			activeRegisterNames := []string{}
			for _, reg := range registersToRead {
				activeRegisterNames = append(activeRegisterNames, reg.Name)
			}
			headers = append(headers, activeRegisterNames...)
			if err := csvWriter.Write(headers); err != nil {
				log.Printf("Lỗi ghi CSV header: %v", err)
			} else {
				csvWriter.Flush()
				log.Printf("Log CSV sẽ được ghi tại: %s", csvLogPath)
			}
		}
	}
	return nil
}

// --- Hàm đóng các file log ---
func closeLogs() {
	if csvFile != nil {
		csvWriter.Flush()
		csvFile.Close()
		log.Println("Đã đóng file log CSV.")
	}
	if logFile != nil {
		logFile.Close()
		log.Println("Đã đóng file log JSON của Logrus.")
	}
}

// --- Các hàm giải mã dữ liệu ---
func decodeBytes(data []byte, regInfo RegisterInfo) (interface{}, error) {
	byteOrder := binary.BigEndian
	logrus.WithFields(logrus.Fields{
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
			logrus.WithFields(logrus.Fields{"register_name": regInfo.Name, "expected_bytes": expectedLen, "received_bytes": len(data)}).Warn("UTF8 nhận được ít byte hơn mong đợi")
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
			logrus.WithFields(logrus.Fields{"register_name": regInfo.Name, "raw_bytes_hex": fmt.Sprintf("%x", data)}).Warn("Phát hiện dữ liệu UTF8 không hợp lệ (pattern 0x8000)")
			return "INVALID_UTF8_DATA", nil
		}
		decodedString := strings.TrimRight(string(data), "\x00")
		if strings.ContainsRune(decodedString, '\uFFFD') {
			logrus.WithFields(logrus.Fields{"register_name": regInfo.Name, "raw_bytes_hex": fmt.Sprintf("%x", data), "decoded_string": decodedString}).Warn("Chuỗi UTF8 giải mã có thể chứa ký tự không hợp lệ")
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
		logrus.WithFields(logrus.Fields{"register_name": regInfo.Name, "year": year, "month": month, "day": day, "hour": hour, "minute": minute, "millisecond": millisecond, "raw_bytes_hex": fmt.Sprintf("%x", data)}).Debug("Giải mã DATETIME (IEC 870-5-4)")
		if month == 0 || month > 12 || day == 0 || day > 31 || hour > 23 || minute > 59 || millisecond > 59999 || year < 1970 || year > 2127 {
			logrus.WithFields(logrus.Fields{"register_name": regInfo.Name, "year": year, "month": month, "day": day, "hour": hour, "minute": minute, "millisecond": millisecond}).Warn("Giá trị DATETIME (IEC) đọc được không hợp lệ")
			return fmt.Sprintf("INVALID_IEC_DATE(Y:%d M:%d D:%d)", year, month, day), nil
		}
		dt := time.Date(year, time.Month(month), day, hour, minute, 0, millisecond*1000000, time.Local)
		return dt.Format("2006-01-02 15:04:00.000"), nil
	case "CUSTOM_PF": // Length=2 (4 bytes)
		if len(data) != 4 {
			return nil, fmt.Errorf("CUSTOM_PF cần 4 bytes (Length=2), nhận %d", len(data))
		}
		rawValue := byteOrder.Uint16(data[0:2])
		signedValue := int16(rawValue)
		scalingFactor := 10000.0 // !!! Giả định !!!
		regValFloat := float64(signedValue) / scalingFactor
		logrus.WithFields(logrus.Fields{"register_name": regInfo.Name, "raw_uint16_used": rawValue, "ignored_bytes_hex": fmt.Sprintf("%x", data[2:4]), "scaled_float": regValFloat, "scaling_factor_assumed": scalingFactor}).Debug("Giải mã CUSTOM_PF (Dùng 2 byte đầu / Length=2, Scaling Factor là giả định)")
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
		logrus.Warnf("Kiểu dữ liệu '%s' cho thanh ghi '%s' chưa được hỗ trợ giải mã.", regInfo.Type, regInfo.Name)
		return fmt.Sprintf("UNSUPPORTED_TYPE(%s)", regInfo.Type), nil
	}
}

// --- Hàm đọc tất cả thanh ghi (Đọc từng mục) ---
func readAllRegisters(client modbus.Client) map[string]interface{} {
	results := make(map[string]interface{})
	if len(registersToRead) == 0 {
		return results
	}

	// *** THÊM: Biến để theo dõi nhóm cho console output ***
	var currentGroupForConsole string

	for _, regInfo := range registersToRead {
		// --- Logic in header nhóm cho Console ---
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
			} // Nhóm riêng Unbalance
			if strings.HasPrefix(regInfo.Name, "Current_Unbalance") {
				groupGuess = "Current Unbalance"
			} // Nhóm riêng Unbalance
		} else if regInfo.Name == "Frequency" {
			groupGuess = "Frequency"
		}

		// Thêm vào map kết quả để dùng cho console output sau
		results[regInfo.Name] = "PENDING_READ" // Đánh dấu là đang chờ đọc
		if groupGuess != currentGroupForConsole {
			// Lưu lại tên nhóm để dùng khi in console
			// Thêm một entry đặc biệt vào map để đánh dấu điểm bắt đầu nhóm
			results[fmt.Sprintf("GROUP_HEADER_%s", groupGuess)] = groupGuess
			currentGroupForConsole = groupGuess
		}
		// --- Kết thúc logic nhóm console ---

		address_1based := regInfo.Address
		readCount := regInfo.Length
		if address_1based < uint16(addressBase) {
			logrus.Errorf("Địa chỉ cấu hình %d (%s) nhỏ hơn addressBase %d", address_1based, regInfo.Name, addressBase)
			results[regInfo.Name] = "INVALID_ADDR_CFG"
			continue
		}
		address_0based := address_1based - uint16(addressBase)

		logrus.WithFields(logrus.Fields{
			"register_name": regInfo.Name, "address_1based": address_1based, "address_0based": address_0based,
			"count_regs": readCount, "data_type": regInfo.Type,
		}).Debug("Chuẩn bị đọc thanh ghi/cụm")

		readBytes, err := client.ReadHoldingRegisters(address_0based, readCount)
		if err != nil {
			handleModbusError(err, slaveID, timeoutMs)
			results[regInfo.Name] = "READ_ERROR"
			time.Sleep(50 * time.Millisecond)
			continue
		}
		expectedBytes := int(readCount) * 2
		if len(readBytes) != expectedBytes {
			logrus.WithFields(logrus.Fields{
				"register_name": regInfo.Name, "address_0based": address_0based, "count_regs": readCount,
				"received_bytes": len(readBytes), "expected_bytes": expectedBytes,
			}).Error("Lỗi độ dài dữ liệu đọc")
			results[regInfo.Name] = "LENGTH_ERROR"
			continue
		}
		decodedValue, decodeErr := decodeBytes(readBytes, regInfo)
		if decodeErr != nil {
			logrus.WithError(decodeErr).WithFields(logrus.Fields{
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

// --- Hàm Chính ---
func main() {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() { sig := <-sigs; signalHandler(sig) }()

	if err := setupLogging(); err != nil {
		log.Println("!!! Lỗi nghiêm trọng khi thiết lập logging. Chương trình sẽ thoát.")
		return
	}
	defer closeLogs()

	log.Println("--- Bắt đầu chương trình Modbus Go Client (Kết nối thiết bị thực) ---")

	windowsPortPath := fmt.Sprintf(`\\.\%s`, portNameSimple)
	log.Printf("Sử dụng đường dẫn cổng: %s", windowsPortPath)

	handler := modbus.NewRTUClientHandler(windowsPortPath)
	handler.BaudRate = baudRate
	handler.DataBits = dataBits
	handler.Parity = parity
	handler.StopBits = stopBits
	handler.SlaveId = slaveID
	handler.Timeout = time.Duration(timeoutMs) * time.Millisecond

	var client modbus.Client
	var connectErr error
	var readCycleCount uint64 = 0

	for running {
		if client == nil {
			log.Printf("Đang thử kết nối tới %s...", portNameSimple)
			connectErr = handler.Connect()
			if connectErr != nil {
				logrus.WithError(connectErr).WithField("port", windowsPortPath).Error("Không thể kết nối Modbus")
				log.Printf("Sẽ thử lại sau 5 giây...")
				waitUntil := time.Now().Add(5 * time.Second)
				for running && time.Now().Before(waitUntil) {
					time.Sleep(100 * time.Millisecond)
				}
				if !running {
					break
				}
				continue
			}
			log.Println(">>> Kết nối thành công!")
			client = modbus.NewClient(handler)
		}

		if client != nil {
			readCycleCount++
			startTime := time.Now()
			data := readAllRegisters(client) // Hàm đọc trả về map data
			readDuration := time.Since(startTime)

			// --- Hiển thị Console với Nhóm ---
			fmt.Printf("\n==================== Lần đọc thứ %d (%s) ====================\n", readCycleCount, startTime.Format("15:04:05"))
			currentGroup := ""
			// Lặp qua danh sách gốc để giữ thứ tự và lấy tên nhóm
			for _, regInfo := range registersToRead {
				// Logic tách tên nhóm (giống trong readAllRegisters)
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

				// In header nhóm nếu thay đổi
				if groupGuess != currentGroup {
					// In dòng phân cách nếu không phải nhóm đầu tiên
					if currentGroup != "" {
						fmt.Println("------------------------------------------")
					}
					fmt.Printf("--- %s ---\n", groupGuess)
					currentGroup = groupGuess
				}

				value, ok := data[regInfo.Name]
				displayValue := "NOT_IN_RESULT"
				prefix := ""
				if ok {
					if strVal, isString := value.(string); isString {
						if strings.Contains(strVal, "ERROR") || strings.Contains(strVal, "INVALID") {
							prefix = "[LỖI] "
						}
					}
					switch v := value.(type) {
					case float32:
						fv64 := float64(v)
						if math.IsNaN(fv64) || math.IsInf(fv64, 0) {
							prefix = "[NaN] "
							displayValue = fmt.Sprintf("%v", v)
						} else {
							displayValue = fmt.Sprintf("%.4f", v)
						}
					case float64:
						if math.IsNaN(v) || math.IsInf(v, 0) {
							prefix = "[NaN] "
							displayValue = fmt.Sprintf("%v", v)
						} else {
							displayValue = fmt.Sprintf("%.4f", v)
						}
					case string:
						displayValue = fmt.Sprintf("%q", v)
					default:
						displayValue = fmt.Sprintf("%v", v)
					}
				} else {
					prefix = "[LỖI] "
				}
				fmt.Printf("%-30s: %s%s\n", regInfo.Name, prefix, displayValue) // Tăng độ rộng tên
			}
			fmt.Println("==================================================================")

			// --- Ghi Log Dữ liệu ---
			logTimestamp := startTime.Format(time.RFC3339Nano)
			logFields := logrus.Fields{
				"timestamp_rfc3339": logTimestamp, "read_duration_ms": readDuration.Milliseconds(), "slave_id": int(slaveID), "read_cycle": readCycleCount,
			}
			validDataCount := 0
			errorDataCount := 0
			activeRegistersMap := make(map[string]bool)
			for _, regInfo := range registersToRead {
				activeRegistersMap[regInfo.Name] = true
			}
			for key, value := range data {
				if _, isActive := activeRegistersMap[key]; !isActive {
					continue
				}
				isErrorValue := false
				if strVal, ok := value.(string); ok {
					if strings.Contains(strVal, "ERROR") || strings.Contains(strVal, "INVALID") {
						isErrorValue = true
						errorDataCount++
					}
				}
				if isErrorValue {
					logFields[key] = value
				} else {
					validDataCount++
					cleanValue := SanitizeValue(value)
					logFields[key] = cleanValue
				}
			}
			logFields["registers_total_attempted"] = len(registersToRead)
			logFields["registers_ok"] = validDataCount
			logFields["registers_error"] = errorDataCount
			logrus.WithFields(logFields).Info("Modbus Data Read")

			if enableCSVLogging && csvWriter != nil {
				row := []string{startTime.Format("2006-01-02 15:04:05.000")}
				for _, regInfo := range registersToRead {
					val, _ := data[regInfo.Name]
					row = append(row, fmt.Sprintf("%v", val))
				}
				if err := csvWriter.Write(row); err != nil {
					logrus.WithError(err).Error("Lỗi ghi dòng CSV")
				}
				csvWriter.Flush()
			}
		} else {
			log.Println("Lỗi logic: client là nil sau khi kiểm tra kết nối.")
			time.Sleep(5 * time.Second)
			continue
		}
		sleepDuration := 1 * time.Second
		waitUntil := time.Now().Add(sleepDuration)
		for running && time.Now().Before(waitUntil) {
			time.Sleep(100 * time.Millisecond)
		}
	}
	log.Println("Vòng lặp chính kết thúc.")
}

// --- Các hàm phụ trợ (handleModbusError, getModbusExceptionMessage, SanitizeValue) ---
// (Giữ nguyên như phiên bản trước)
func handleModbusError(err error, slaveID byte, timeoutMs int) {
	if mbErr, ok := err.(*modbus.ModbusError); ok {
		logrus.WithError(err).WithFields(logrus.Fields{
			"slave_id": int(slaveID), "exception_code": mbErr.ExceptionCode, "exception_msg": getModbusExceptionMessage(mbErr.ExceptionCode),
		}).Error("Lỗi Modbus từ Slave")
	} else if os.IsTimeout(err) {
		logrus.WithError(err).WithFields(logrus.Fields{
			"slave_id": int(slaveID), "timeout_ms": timeoutMs,
		}).Warn("Timeout khi chờ phản hồi từ Slave (os.IsTimeout)")
	} else if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		logrus.WithError(err).WithFields(logrus.Fields{
			"slave_id": int(slaveID), "timeout_ms": timeoutMs,
		}).Warn("Timeout mạng khi chờ phản hồi từ Slave (net.Error)")
	} else {
		logrus.WithError(err).WithField("error_type", fmt.Sprintf("%T", err)).Warn("Lỗi giao tiếp khác")
	}
}

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

func SanitizeValue(value interface{}) interface{} {
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
