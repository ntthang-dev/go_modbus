package storage

import (
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// SlogDataWriter triển khai DataWriter để ghi log bằng slog.
type SlogDataWriter struct {
	logger *slog.Logger // Logger được truyền từ ngoài vào (dataLogger)
}

// NewSlogDataWriter tạo một SlogDataWriter mới.
func NewSlogDataWriter(logger *slog.Logger) (*SlogDataWriter, error) {
	if logger == nil {
		return nil, fmt.Errorf("logger không được là nil khi tạo SlogDataWriter")
	}
	return &SlogDataWriter{logger: logger}, nil
}

// WriteData ghi dữ liệu vào log bằng slog.
func (sw *SlogDataWriter) WriteData(deviceName string, deviceTags map[string]string, data map[string]interface{}, timestamp time.Time) error {
	// Chuyển đổi map thành slice []any cho slog
	args := make([]any, 0, len(data)*2+4+len(deviceTags)*2)
	args = append(args, slog.String("device_name", deviceName))
	args = append(args, slog.Time("timestamp", timestamp)) // Sử dụng slog.Time

	for k, v := range deviceTags {
		args = append(args, slog.String(fmt.Sprintf("tag_%s", k), v))
	}

	validDataCount := 0
	errorDataCount := 0
	for key, value := range data {
		cleanValue := SanitizeValue(value) // Sử dụng hàm SanitizeValue từ package storage
		isErrorValue := false
		if strVal, ok := value.(string); ok {
			if strings.Contains(strVal, "ERROR") || strings.Contains(strVal, "INVALID") || strings.Contains(strVal, "N/A_") {
				isErrorValue = true
				errorDataCount++
			}
		}
		if cleanValue == nil && !isErrorValue {
			isErrorValue = true
			errorDataCount++
		}

		if isErrorValue {
			args = append(args, slog.Any(key, value)) // Ghi giá trị lỗi gốc
		} else {
			validDataCount++
			args = append(args, slog.Any(key, cleanValue))
		} // Ghi giá trị đã làm sạch
	}
	args = append(args, slog.Int("registers_ok", validDataCount))
	args = append(args, slog.Int("registers_error", errorDataCount))

	// Ghi log bằng dataLogger đã được truyền vào
	sw.logger.Info("Modbus Data Read", args...)
	return nil
}

// Close không cần làm gì đặc biệt vì logger được quản lý bên ngoài.
func (sw *SlogDataWriter) Close() error {
	// Có thể dùng logger mặc định nếu đã set, hoặc không cần log gì ở đây
	// slog.Debug("SlogDataWriter không cần đóng tài nguyên riêng.")
	return nil
}
