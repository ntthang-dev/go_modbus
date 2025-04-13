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

	// !!! THAY 'testmod' bằng tên module của bạn !!!
	"testmod/config"

	"github.com/sirupsen/logrus"
)

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
		if strVal, ok := value.(string); ok {
			if strings.Contains(strVal, "ERROR") || strings.Contains(strVal, "INVALID") || strings.Contains(strVal, "N/A_") {
				isErrorValue = true
				errorDataCount++
			}
		}
		if isErrorValue {
			logFields[key] = value // Giữ nguyên chuỗi lỗi/N/A
		} else {
			validDataCount++
			logFields[key] = cleanValue
		} // Ghi giá trị đã làm sạch
	}
	logFields["registers_ok"] = validDataCount
	logFields["registers_error"] = errorDataCount

	lw.logger.WithFields(logFields).Info("Modbus Data Read") // Ghi vào dataLogger
	return nil
}

// Close không cần làm gì đặc biệt vì logger được quản lý bên ngoài.
func (lw *LogrusWriter) Close() error {
	log.Println("LogrusWriter không cần đóng tài nguyên riêng.")
	return nil
}

// --- Hàm setup logger riêng cho trạng thái/lỗi ---
func SetupStatusLogger(cfg *config.LoggingConfig, logDir string) (*logrus.Logger, *os.File) {
	logger := logrus.New()
	level, err := logrus.ParseLevel(cfg.Level)
	if err != nil {
		level = logrus.InfoLevel
	}
	logger.SetLevel(level)

	ts := time.Now().Format("20060102_150405")
	statusLogFilename := fmt.Sprintf(cfg.StatusErrorLogFile, ts) // Dùng mẫu tên file từ config
	statusLogPath := filepath.Join(logDir, statusLogFilename)

	logFile, errLogrus := os.OpenFile(statusLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if errLogrus == nil {
		mw := io.MultiWriter(os.Stdout, logFile)
		logger.SetOutput(mw)
		logger.SetFormatter(&logrus.TextFormatter{FullTimestamp: true, TimestampFormat: "2006-01-02 15:04:05", ForceColors: true})
		log.Printf("Status/Error log sẽ được ghi tại: %s và hiển thị trên Console (Level: %s)", statusLogPath, level.String())
		return logger, logFile
	} else {
		log.Printf("Lỗi mở file log status/error '%s': %v. Log sẽ chỉ ghi ra Console.", statusLogPath, errLogrus)
		logger.SetOutput(os.Stdout)
		logger.SetFormatter(&logrus.TextFormatter{FullTimestamp: true, ForceColors: true})
		return logger, nil
	}
}

// --- Hàm setup logger riêng cho dữ liệu ---
func SetupDataLogger(cfg *config.LoggingConfig, logDir string) (*logrus.Logger, *os.File) {
	logger := logrus.New()
	logger.SetLevel(logrus.InfoLevel) // Data logger thường chỉ cần INFO

	ts := time.Now().Format("20060102_150405")
	// *** SỬA: Sử dụng tên file cố định hoặc mẫu khác cho data log ***
	dataLogFilename := fmt.Sprintf("modbus_data_logrus_%s.log", ts) // Tên file khác với status log
	jsonLogPath := filepath.Join(logDir, dataLogFilename)

	logFile, errLogrus := os.OpenFile(jsonLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if errLogrus == nil {
		logger.SetOutput(logFile) // Chỉ ghi ra file JSON
		logger.SetFormatter(&logrus.JSONFormatter{TimestampFormat: time.RFC3339Nano})
		log.Printf("Data log (JSON) sẽ được ghi tại: %s", jsonLogPath)
		return logger, logFile
	} else {
		log.Printf("Lỗi mở file data log JSON '%s': %v. Data logger sẽ không ghi file.", jsonLogPath, errLogrus)
		logger.SetOutput(io.Discard) // Không ghi đi đâu cả nếu lỗi
		return logger, nil
	}
}
