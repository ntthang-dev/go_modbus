// Package config xử lý việc đọc và xác thực cấu hình ứng dụng.
package config

import (
	"encoding/csv"
	"fmt"
	stlog "log" // Dùng log chuẩn cho cảnh báo khi đọc config
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// RegisterInfo định nghĩa thông tin cho một thanh ghi Modbus
type RegisterInfo struct {
	Name    string `yaml:"name" csv:"Name"`
	Address uint16 `yaml:"address" csv:"Address"`
	Type    string `yaml:"type" csv:"Type"`
	Length  uint16 `yaml:"length" csv:"Length"`
}

// ConnectionConfig định nghĩa cấu hình kết nối Modbus
type ConnectionConfig struct {
	Type        string `yaml:"type"`
	Port        string `yaml:"port"`
	BaudRate    int    `yaml:"baudrate"`
	DataBits    int    `yaml:"databits"`
	Parity      string `yaml:"parity"`
	StopBits    int    `yaml:"stopbits"`
	SlaveID     byte   `yaml:"slaveid"`
	TimeoutMs   int    `yaml:"timeout_ms"`
	AddressBase int    `yaml:"address_base"`
}

// DeviceConfig định nghĩa cấu hình cho một thiết bị Modbus
type DeviceConfig struct {
	Name              string            `yaml:"name"`
	Enabled           bool              `yaml:"enabled"`
	Tags              map[string]string `yaml:"tags"`
	Connection        ConnectionConfig  `yaml:"connection"`
	PollingIntervalMs int               `yaml:"polling_interval_ms"`
	RegisterListFile  string            `yaml:"register_list_file"`
}

// StorageConfig (Hiện tại trống)
type StorageConfig struct{}

// LoggingConfig cấu hình logging chung
type LoggingConfig struct {
	Level              string `yaml:"level"`
	EnableCSV          bool   `yaml:"enable_csv"`
	StatusErrorLogFile string `yaml:"status_error_log_file"`
	DataLogFile        string `yaml:"data_log_file"`
}

// Config là cấu trúc tổng
type Config struct {
	Logging LoggingConfig  `yaml:"logging"`
	Storage StorageConfig  `yaml:"storage"`
	Devices []DeviceConfig `yaml:"devices"`
}

// LoadConfig đọc file cấu hình YAML chính
func LoadConfig(filePath string) (*Config, error) {
	yamlFile, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("lỗi đọc file config '%s': %w", filePath, err)
	}
	var cfg Config
	err = yaml.Unmarshal(yamlFile, &cfg)
	if err != nil {
		return nil, fmt.Errorf("lỗi giải mã YAML từ file '%s': %w", filePath, err)
	}

	if cfg.Logging.Level == "" {
		cfg.Logging.Level = "info"
	}
	if cfg.Logging.StatusErrorLogFile == "" {
		cfg.Logging.StatusErrorLogFile = "gateway_status_%s.log"
	}
	if cfg.Logging.DataLogFile == "" {
		cfg.Logging.DataLogFile = "modbus_data_slog_%s.log"
	}

	for i := range cfg.Devices {
		dev := &cfg.Devices[i]
		if dev.Connection.TimeoutMs <= 0 {
			dev.Connection.TimeoutMs = 1000
		}
		if dev.Connection.DataBits == 0 {
			dev.Connection.DataBits = 8
		}
		if dev.Connection.StopBits == 0 {
			dev.Connection.StopBits = 1
		}
		if dev.Connection.Parity == "" {
			dev.Connection.Parity = "N"
		}
		if dev.PollingIntervalMs <= 0 {
			dev.PollingIntervalMs = 1000
		}
		if dev.RegisterListFile == "" && dev.Enabled {
			stlog.Printf("CẢNH BÁO: Thiết bị '%s' được kích hoạt nhưng thiếu 'register_list_file'.\n", dev.Name)
		}
	}
	return &cfg, nil
}

// LoadRegistersFromCSV đọc danh sách thanh ghi từ file CSV
func LoadRegistersFromCSV(filePath string) ([]RegisterInfo, error) {
	csvFile, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("lỗi mở file CSV '%s': %w", filePath, err)
	}
	defer csvFile.Close()

	reader := csv.NewReader(csvFile)
	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("lỗi đọc header file CSV '%s': %w", filePath, err)
	}
	colIndex := make(map[string]int)
	for i, h := range header {
		colIndex[strings.TrimSpace(h)] = i
	}
	requiredCols := []string{"Name", "Address", "Type", "Length"}
	for _, reqCol := range requiredCols {
		if _, ok := colIndex[reqCol]; !ok {
			return nil, fmt.Errorf("lỗi file CSV '%s': thiếu cột header '%s'", filePath, reqCol)
		}
	}

	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("lỗi đọc nội dung file CSV '%s': %w", filePath, err)
	}

	var registers []RegisterInfo
	for i, record := range records {
		lineNumber := i + 2
		if len(record) != len(header) {
			stlog.Printf("Cảnh báo: File CSV '%s' dòng %d: số cột (%d) không khớp header (%d). Bỏ qua dòng.\n", filePath, lineNumber, len(record), len(header))
			continue
		}

		var reg RegisterInfo
		reg.Name = strings.TrimSpace(record[colIndex["Name"]])
		addrStr := strings.TrimSpace(record[colIndex["Address"]])
		reg.Type = strings.TrimSpace(strings.ToUpper(record[colIndex["Type"]]))
		lenStr := strings.TrimSpace(record[colIndex["Length"]])

		if reg.Name == "" || reg.Type == "" || addrStr == "" || lenStr == "" {
			stlog.Printf("Cảnh báo: File CSV '%s' dòng %d: thiếu thông tin cột (Name, Address, Type, Length). Bỏ qua.\n", filePath, lineNumber)
			continue
		}

		addr, errA := strconv.ParseUint(addrStr, 10, 16)
		if errA != nil {
			stlog.Printf("Cảnh báo: File CSV '%s' dòng %d: Address '%s' không hợp lệ. Bỏ qua.\n", filePath, lineNumber, addrStr)
			continue
		}
		reg.Address = uint16(addr)

		lenVal, errL := strconv.ParseUint(lenStr, 10, 16)
		if errL != nil {
			stlog.Printf("Cảnh báo: File CSV '%s' dòng %d: Length '%s' không hợp lệ. Bỏ qua.\n", filePath, lineNumber, lenStr)
			continue
		}
		reg.Length = uint16(lenVal)

		if reg.Length == 0 {
			stlog.Printf("Cảnh báo: File CSV '%s' dòng %d: Length=0 không hợp lệ. Bỏ qua.\n", filePath, lineNumber)
			continue
		}
		registers = append(registers, reg)
	}

	if len(registers) == 0 {
		return nil, fmt.Errorf("không tìm thấy định nghĩa thanh ghi hợp lệ nào trong file CSV '%s'", filePath)
	}
	stlog.Printf("Đã đọc %d thanh ghi từ file '%s'\n", len(registers), filePath) // Dùng log chuẩn
	return registers, nil
}

// GetTimeout trả về timeout dạng time.Duration
func (c *ConnectionConfig) GetTimeout() time.Duration {
	return time.Duration(c.TimeoutMs) * time.Millisecond
}

// GetPollingInterval trả về polling interval dạng time.Duration
func (d *DeviceConfig) GetPollingInterval() time.Duration {
	return time.Duration(d.PollingIntervalMs) * time.Millisecond
}

// GetWindowsPortPath trả về đường dẫn cổng COM cho Windows
func (c *ConnectionConfig) GetWindowsPortPath() string {
	return fmt.Sprintf(`\\.\%s`, c.Port)
}
