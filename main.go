package main

import (
	"context"
	"flag"
	"fmt"
	"io" // Cần cho io.MultiWriter, io.Discard
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	// !!! THAY 'modbus_register_slave' bằng tên module của bạn !!!
	"modbus_register_slave/config"
	"modbus_register_slave/modbusclient"
	"modbus_register_slave/portmanager"
	"modbus_register_slave/storage"

	"github.com/sirupsen/logrus"
)

var (
	configFile = flag.String("config", "config.yaml", "Đường dẫn tới file cấu hình YAML")
	logDir     = "logs_go_refactored" // Thư mục log mặc định
)

// *** THÊM: Hàm setup logger riêng cho trạng thái/lỗi (Di chuyển từ storage) ***
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
	logOutputWriters := []io.Writer{os.Stdout} // Luôn log status ra console (ít nhất là INFO)
	if errLogrus == nil {
		logOutputWriters = append(logOutputWriters, logFile) // Thêm file nếu mở thành công
		log.Printf("Status/Error log sẽ được ghi tại: %s (Level: %s)", statusLogPath, level.String())
	} else {
		log.Printf("Lỗi mở file log status/error '%s': %v. Log sẽ chỉ ghi ra Console.", statusLogPath, errLogrus)
	}

	// Tạo MultiWriter cho status logger
	mw := io.MultiWriter(logOutputWriters...)
	logger.SetOutput(mw)
	// Dùng TextFormatter cho dễ đọc cả trên console và file
	logger.SetFormatter(&logrus.TextFormatter{
		FullTimestamp:   true,
		TimestampFormat: "2006-01-02 15:04:05",
		ForceColors:     true,               // Có màu trên console
		DisableColors:   (errLogrus != nil), // Tắt màu nếu chỉ ghi ra file (khi console lỗi)
	})

	// Không cần thêm Hook nữa vì TextFormatter đã đủ cho cả console và file
	return logger, logFile // Trả về logger và file handle (có thể là nil)
}

// *** THÊM: Hàm setup logger riêng cho dữ liệu (Di chuyển từ storage) ***
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

func main() {
	flag.Parse()

	// --- 1. Tải Cấu hình YAML chính ---
	cfg, err := config.LoadConfig(*configFile)
	if err != nil {
		log.Fatalf("FATAL: Lỗi tải cấu hình từ '%s': %v", *configFile, err)
	}

	// --- 2. Thiết lập Logging (Sử dụng hàm vừa chuyển vào) ---
	if err := os.MkdirAll(logDir, 0755); err != nil {
		log.Fatalf("FATAL: Lỗi tạo thư mục log '%s': %v", logDir, err)
	}
	statusLogger, statusLogFile := SetupStatusLogger(&cfg.Logging, logDir)
	if statusLogFile != nil {
		defer statusLogFile.Close()
	}
	dataLogger, dataLogFile := SetupDataLogger(&cfg.Logging, logDir)
	if dataLogFile != nil {
		defer dataLogFile.Close()
	}
	statusLogger.Info("--- Khởi chạy Gateway Modbus (Refactored + PortManager) ---")

	// --- 3. Khởi tạo Storage Backends cho Dữ liệu ---
	var globalDataWriters []storage.DataWriter
	logrusDataWriter, errL := storage.NewLogrusWriter(dataLogger) // Truyền dataLogger
	if errL != nil {
		statusLogger.WithError(errL).Error("Lỗi khởi tạo Logrus Data Writer")
	} else {
		globalDataWriters = append(globalDataWriters, logrusDataWriter)
	}
	multiDataWriter := storage.NewMultiWriter(globalDataWriters...)
	defer func() {
		statusLogger.Info("Đang đóng các storage data writers...")
		if err := multiDataWriter.Close(); err != nil {
			statusLogger.WithError(err).Error("Lỗi khi đóng storage data writers")
		} else {
			statusLogger.Info("Đã đóng các storage data writers.")
		}
	}()

	// --- 4. Khởi tạo Port Managers ---
	portManagers := make(map[string]*portmanager.Manager)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wgPortManagers sync.WaitGroup
	devicesByPort := make(map[string][]config.DeviceConfig)
	for _, devCfg := range cfg.Devices {
		if devCfg.Enabled {
			devicesByPort[devCfg.Connection.Port] = append(devicesByPort[devCfg.Connection.Port], devCfg)
		}
	}

	for portName, devicesOnPort := range devicesByPort {
		if len(devicesOnPort) == 0 {
			continue
		}
		portCfg := devicesOnPort[0].Connection
		statusLogger.Infof("Khởi tạo Port Manager cho cổng %s...", portName)
		manager := portmanager.NewManager(portCfg, statusLogger) // Truyền statusLogger
		portManagers[portName] = manager
		wgPortManagers.Add(1)
		go func(m *portmanager.Manager) { defer wgPortManagers.Done(); m.Run(ctx) }(manager)
	}

	// --- 5. Khởi tạo và Chạy Goroutine cho từng Thiết bị Logic ---
	var wgDevices sync.WaitGroup
	activeDevices := 0
	allCsvWriters := []*storage.CsvWriter{}

	for i := range cfg.Devices {
		deviceCfg := &cfg.Devices[i]
		if !deviceCfg.Enabled {
			continue
		}

		portMgr, ok := portManagers[deviceCfg.Connection.Port]
		if !ok {
			statusLogger.Errorf("Lỗi logic: Không tìm thấy Port Manager cho cổng %s của '%s'. Bỏ qua.", deviceCfg.Connection.Port, deviceCfg.Name)
			continue
		}
		if deviceCfg.RegisterListFile == "" {
			statusLogger.Errorf("Thiết bị '%s' thiếu 'register_list_file'. Bỏ qua.", deviceCfg.Name)
			continue
		}

		csvPath := filepath.Join(filepath.Dir(*configFile), deviceCfg.RegisterListFile)
		registers, err := config.LoadRegistersFromCSV(csvPath)
		if err != nil {
			statusLogger.WithError(err).Errorf("Lỗi đọc thanh ghi cho '%s' từ '%s'. Bỏ qua.", deviceCfg.Name, csvPath)
			continue
		}
		if len(registers) == 0 {
			statusLogger.Warnf("Danh sách thanh ghi rỗng cho '%s'. Bỏ qua.", deviceCfg.Name)
			continue
		}
		statusLogger.Infof("Đã đọc %d thanh ghi cho thiết bị '%s'", len(registers), deviceCfg.Name)

		// Tạo các writer riêng cho thiết bị này
		deviceSpecificWriters := []storage.DataWriter{}
		deviceSpecificWriters = append(deviceSpecificWriters, globalDataWriters...)

		var devCsvWriter *storage.CsvWriter
		if cfg.Logging.EnableCSV {
			devCsvWriter, err = storage.NewCsvWriter(true, logDir, deviceCfg.Name, registers)
			if err != nil {
				statusLogger.WithError(err).Errorf("Lỗi khởi tạo CSV Writer cho '%s'", deviceCfg.Name)
			} else if devCsvWriter != nil {
				deviceSpecificWriters = append(deviceSpecificWriters, devCsvWriter)
				allCsvWriters = append(allCsvWriters, devCsvWriter)
			}
		}
		deviceMultiWriter := storage.NewMultiWriter(deviceSpecificWriters...)

		// Tạo đối tượng Device, truyền statusLogger và request channel
		device := modbusclient.NewDevice(*deviceCfg, registers, deviceMultiWriter, statusLogger, portMgr.GetRequestChannel())

		wgDevices.Add(1)
		go device.RunPollingLoop(ctx, &wgDevices)
		activeDevices++
	}

	defer func() {
		statusLogger.Info("Đang đóng các file CSV...")
		for _, cw := range allCsvWriters {
			if cw != nil {
				cw.Close()
			}
		}
		statusLogger.Info("Đã đóng các file CSV.")
	}()

	if activeDevices == 0 {
		statusLogger.Warn("Không có thiết bị nào được kích hoạt. Thoát chương trình.")
		cancel()
	} else {
		statusLogger.Infof("Đã khởi chạy %d goroutine cho các thiết bị. Nhấn Ctrl+C để dừng.", activeDevices)
	}

	// --- 6. Chờ Tín hiệu Dừng ---
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	statusLogger.Warn("Đã nhận tín hiệu dừng. Đang yêu cầu các goroutine kết thúc...")
	cancel()

	statusLogger.Info("Đang chờ các goroutine Device hoàn thành...")
	wgDevices.Wait()
	statusLogger.Info("Các goroutine Device đã dừng.")

	statusLogger.Info("Đang chờ các goroutine Port Manager hoàn thành...")
	wgPortManagers.Wait()
	statusLogger.Info("Các goroutine Port Manager đã dừng.")

	statusLogger.Info("Chương trình kết thúc.")
}
