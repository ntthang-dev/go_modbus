// Package storage định nghĩa interface và các cách lưu trữ dữ liệu Modbus.
package storage

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	// !!! THAY 'modbus_register_slave' bằng tên module của bạn !!!
	"modbus_register_slave/config"

	"github.com/sirupsen/logrus"
)

// --- Định nghĩa Hook để lọc log ra Console ---

// ConsoleHook chỉ gửi các level log cụ thể ra một Writer (ví dụ: os.Stdout).
type ConsoleHook struct {
	Writer    io.Writer
	LogLevels []logrus.Level   // Các level được phép ghi ra console
	Formatter logrus.Formatter // Sử dụng Formatter riêng cho console nếu muốn khác file
}

// Fire được gọi bởi logrus cho mỗi entry log.
// Nó kiểm tra level và ghi ra Writer nếu được phép.
func (hook *ConsoleHook) Fire(entry *logrus.Entry) error {
	isLevelAllowed := false
	for _, level := range hook.LogLevels {
		if entry.Level == level {
			isLevelAllowed = true
			break
		}
	}

	if isLevelAllowed {
		// Sử dụng formatter của hook (hoặc formatter mặc định của entry nếu hook.Formatter là nil)
		formatter := hook.Formatter
		if formatter == nil {
			// Nếu hook không có formatter riêng, thử lấy formatter của logger gốc
			// Tuy nhiên, cách đơn giản hơn là luôn định nghĩa formatter cho hook khi tạo nó
			// Hoặc dùng một formatter mặc định đơn giản ở đây
			formatter = &logrus.TextFormatter{DisableColors: true} // Formatter text đơn giản
		}

		// Định dạng entry thành bytes
		lineBytes, err := formatter.Format(entry)
		if err != nil {
			// Ghi lỗi định dạng ra stderr (tránh vòng lặp vô hạn nếu writer của hook là stderr)
			fmt.Fprintf(os.Stderr, "Lỗi định dạng log cho ConsoleHook: %v\n", err)
			return err
		}
		// Ghi bytes đã định dạng ra Writer của hook
		_, err = hook.Writer.Write(lineBytes)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Lỗi ghi log của ConsoleHook: %v\n", err)
			return err
		}
	}

	return nil
}

// Levels trả về các level mà hook này quan tâm.
func (hook *ConsoleHook) Levels() []logrus.Level {
	return hook.LogLevels // Hook này chỉ quan tâm đến các level được cấu hình
}

// --- LogrusWriter ---

// LogrusWriter triển khai DataWriter để ghi log bằng Logrus.
type LogrusWriter struct {
	logger *logrus.Logger // Logger được truyền từ ngoài vào (dataLogger)
}

// NewLogrusWriter tạo một LogrusWriter mới.
func NewLogrusWriter(logger *logrus.Logger) (*LogrusWriter, error) {
	if logger == nil {
		return nil, fmt.Errorf("logger không được là nil khi tạo LogrusWriter")
	}
	return &LogrusWriter{logger: logger}, nil
}

// WriteData ghi dữ liệu vào log bằng Logrus.
// Logic xử lý data, đếm lỗi nằm ở đây.
func (lw *LogrusWriter) WriteData(deviceName string, deviceTags map[string]string, data map[string]interface{}, timestamp time.Time) error {
	logFields := logrus.Fields{
		"device_name":       deviceName,
		"timestamp_rfc3339": timestamp.Format(time.RFC3339Nano),
	}
	for k, v := range deviceTags {
		logFields[fmt.Sprintf("tag_%s", k)] = v
	}

	validDataCount := 0
	errorDataCount := 0
	for key, value := range data {
		cleanValue := SanitizeValue(value)
		isErrorValue := false
		// Kiểm tra giá trị gốc trước khi sanitize để đếm lỗi
		if strVal, ok := value.(string); ok {
			if strings.Contains(strVal, "ERROR") || strings.Contains(strVal, "INVALID") || strings.Contains(strVal, "N/A_") {
				isErrorValue = true
				errorDataCount++
			}
		} else if cleanValue == nil { // SanitizeValue trả về nil cho NaN/Inf
			isErrorValue = true
			errorDataCount++
		}

		if isErrorValue {
			logFields[key] = value // Log giá trị gốc nếu là lỗi/NA/NaN/Inf
		} else {
			validDataCount++
			logFields[key] = cleanValue
		} // Log giá trị đã làm sạch
	}
	logFields["registers_ok"] = validDataCount
	logFields["registers_error"] = errorDataCount

	// Ghi log bằng logger đã được truyền vào (dataLogger)
	lw.logger.WithFields(logFields).Info("Modbus Data Read")
	return nil
}

// Close không cần làm gì đặc biệt vì logger được quản lý bên ngoài.
func (lw *LogrusWriter) Close() error {
	log.Println("LogrusWriter không cần đóng tài nguyên riêng.")
	return nil
}

// --- Hàm setup logger riêng cho trạng thái/lỗi ---
// Di chuyển hàm này sang main.go hoặc package riêng
func SetupStatusLogger(cfg *config.LoggingConfig, logDir string) (*logrus.Logger, *os.File) {
	logger := logrus.New()
	level, err := logrus.ParseLevel(cfg.Level)
	if err != nil {
		level = logrus.InfoLevel
	}
	logger.SetLevel(level)

	ts := time.Now().Format("20060102_150405")
	statusLogFilename := fmt.Sprintf(cfg.StatusErrorLogFile, ts)
	statusLogPath := filepath.Join(logDir, statusLogFilename)

	logFile, errLogrus := os.OpenFile(statusLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if errLogrus != nil {
		// Nếu không mở được file, chỉ log ra console
		log.Printf("Lỗi mở file log status/error '%s': %v. Log sẽ chỉ ghi ra Console.", statusLogPath, errLogrus)
		logger.SetOutput(os.Stdout)
		logger.SetFormatter(&logrus.TextFormatter{FullTimestamp: true, TimestampFormat: "2006-01-02 15:04:05", ForceColors: true})
		return logger, nil // Trả về nil file handle
	}

	// Nếu mở file thành công, chỉ ghi log vào file (không ghi ra stdout nữa)
	logger.SetOutput(logFile)
	// Dùng TextFormatter cho file log status/error để dễ đọc hơn JSON? (Tùy chọn)
	logger.SetFormatter(&logrus.TextFormatter{
		FullTimestamp:   true,
		TimestampFormat: "2006-01-02 15:04:05",
		DisableColors:   true, // Không cần màu trong file
	})

	// Thêm Hook để ghi một số level nhất định ra Console
	logger.AddHook(&ConsoleHook{
		Writer: os.Stdout,
		// Chỉ ghi INFO, PANIC, FATAL ra Console, bỏ qua WARN, ERROR, DEBUG, TRACE
		LogLevels: []logrus.Level{logrus.InfoLevel, logrus.PanicLevel, logrus.FatalLevel},
		Formatter: &logrus.TextFormatter{ // Dùng TextFormatter cho Console
			FullTimestamp:   true,
			TimestampFormat: "2006-01-02 15:04:05",
			ForceColors:     true,
		},
	})

	log.Printf("Status/Error log sẽ được ghi tại: %s (Level: %s). Console chỉ hiển thị INFO.", statusLogPath, level.String())
	return logger, logFile // Trả về logger và file handle
}

// --- Hàm setup logger riêng cho dữ liệu ---
// Di chuyển hàm này sang main.go hoặc package riêng
func SetupDataLogger(cfg *config.LoggingConfig, logDir string) (*logrus.Logger, *os.File) {
	logger := logrus.New()
	logger.SetLevel(logrus.InfoLevel) // Data logger thường chỉ cần INFO

	ts := time.Now().Format("20060102_150405")
	dataLogFilename := fmt.Sprintf("modbus_data_logrus_%s.log", ts)
	jsonLogPath := filepath.Join(logDir, dataLogFilename)

	logFile, errLogrus := os.OpenFile(jsonLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if errLogrus == nil {
		logger.SetOutput(logFile) // Chỉ ghi ra file JSON
		logger.SetFormatter(&logrus.JSONFormatter{TimestampFormat: time.RFC3339Nano})
		log.Printf("Data log (JSON) sẽ được ghi tại: %s", jsonLogPath)
		return logger, logFile
	} else {
		log.Printf("Lỗi mở file data log JSON '%s': %v. Data logger sẽ không ghi file.", jsonLogPath, errLogrus)
		logger.SetOutput(io.Discard)
		return logger, nil
	}
}
