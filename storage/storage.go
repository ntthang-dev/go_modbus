// Package storage định nghĩa interface và các cách lưu trữ dữ liệu Modbus.
package storage

import (
	// Sử dụng slog nếu cần log trong package này
	"math"
	"strings"
	"time"
	// !!! THAY 'modbus_register_slave' bằng tên module của bạn !!!
)

// DataWriter là interface chung cho các backend lưu trữ dữ liệu Modbus.
type DataWriter interface {
	WriteData(deviceName string, deviceTags map[string]string, data map[string]interface{}, timestamp time.Time) error
	Close() error
}

// NoOpWriter không làm gì cả.
type NoOpWriter struct{}

func (n *NoOpWriter) WriteData(deviceName string, deviceTags map[string]string, data map[string]interface{}, timestamp time.Time) error {
	return nil
}
func (n *NoOpWriter) Close() error { return nil }

// MultiWriter cho phép ghi vào nhiều DataWriter.
type MultiWriter struct{ writers []DataWriter }

func NewMultiWriter(writers ...DataWriter) *MultiWriter {
	mw := &MultiWriter{}
	for _, w := range writers {
		if w != nil {
			mw.writers = append(mw.writers, w)
		}
	}
	return mw
}
func (mw *MultiWriter) WriteData(deviceName string, deviceTags map[string]string, data map[string]interface{}, timestamp time.Time) error {
	var firstErr error
	for _, w := range mw.writers {
		if err := w.WriteData(deviceName, deviceTags, data, timestamp); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
func (mw *MultiWriter) Close() error {
	var firstErr error
	for _, w := range mw.writers {
		if err := w.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// SanitizeValue xử lý các giá trị đặc biệt (NaN, Inf) và lỗi dạng chuỗi.
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

// --- Xóa bỏ code thừa ---
