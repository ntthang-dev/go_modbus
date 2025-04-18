// Package storage định nghĩa interface và các cách lưu trữ dữ liệu Modbus.
package storage

import (
	"encoding/csv"
	"fmt"
	stlog "log" // Log chuẩn cho thông báo khởi tạo/đóng file
	"log/slog"  // Sử dụng slog cho log lỗi ghi CSV
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	// !!! THAY 'modbus_register_slave' bằng tên module của bạn !!!
	"modbus_register_slave/config"
)

// CsvWriter triển khai DataWriter để ghi dữ liệu vào file CSV.
type CsvWriter struct {
	filePath string
	file     *os.File
	writer   *csv.Writer
	headers  []string
	mu       sync.Mutex
}

// NewCsvWriter tạo một CsvWriter mới cho một thiết bị cụ thể.
func NewCsvWriter(enable bool, logDir string, deviceName string, registers []config.RegisterInfo) (*CsvWriter, error) {
	if !enable {
		return nil, nil
	}
	if len(registers) == 0 {
		return nil, fmt.Errorf("danh sách thanh ghi rỗng, không thể tạo CSV writer cho '%s'", deviceName)
	}

	ts := time.Now().Format("20060102_150405")
	csvFilename := fmt.Sprintf("device_%s_data_%s.csv", strings.ReplaceAll(deviceName, " ", "_"), ts)
	filePath := filepath.Join(logDir, csvFilename)

	file, err := os.Create(filePath)
	if err != nil {
		return nil, fmt.Errorf("lỗi tạo file log CSV '%s': %w", filePath, err)
	}

	writer := csv.NewWriter(file)
	headers := []string{"Timestamp"}
	for _, reg := range registers {
		headers = append(headers, reg.Name)
	}

	if err := writer.Write(headers); err != nil {
		file.Close()
		return nil, fmt.Errorf("lỗi ghi CSV header cho '%s': %w", filePath, err)
	}
	writer.Flush()
	stlog.Printf("Log CSV cho thiết bị '%s' sẽ được ghi tại: %s", deviceName, filePath) // Dùng log chuẩn

	return &CsvWriter{filePath: filePath, file: file, writer: writer, headers: headers}, nil
}

// WriteData ghi dữ liệu vào file CSV.
func (cw *CsvWriter) WriteData(deviceName string, deviceTags map[string]string, data map[string]interface{}, timestamp time.Time) error {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	if cw.writer == nil {
		return fmt.Errorf("CSV writer chưa được khởi tạo cho file %s", cw.filePath)
	}

	row := make([]string, len(cw.headers))
	for i, headerName := range cw.headers {
		if i == 0 {
			row[i] = timestamp.Format("2006-01-02 15:04:05.000")
		} else {
			value, ok := data[headerName]
			if !ok {
				row[i] = "MISSING"
			} else {
				row[i] = fmt.Sprintf("%v", value)
			}
		}
	}

	if err := cw.writer.Write(row); err != nil {
		// Sử dụng slog mặc định (đã được set ở main) để ghi lỗi
		slog.Error("Lỗi ghi dòng CSV", slog.String("csv_file", cw.filePath), slog.Any("error", err))
		return err
	}
	cw.writer.Flush()
	return cw.writer.Error()
}

// Close đóng file CSV.
func (cw *CsvWriter) Close() error {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	if cw.file != nil {
		stlog.Printf("Đang đóng file log CSV: %s...", cw.filePath) // Dùng log chuẩn
		cw.writer.Flush()
		err := cw.file.Close()
		cw.file = nil
		cw.writer = nil
		if err != nil {
			stlog.Printf("Lỗi khi đóng file CSV '%s': %v", cw.filePath, err)
			return err
		}
		stlog.Printf("Đã đóng file log CSV: %s.", cw.filePath)
	}
	return nil
}
