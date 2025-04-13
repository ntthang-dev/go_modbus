package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"

	// !!! THAY 'testmod' bằng tên module của bạn !!!
	"testmod/config"
	"testmod/modbusclient"
	"testmod/storage"
)

var (
	configFile = flag.String("config", "config.yaml", "Đường dẫn tới file cấu hình YAML")
	logDir     = "logs_go_refactored" // Thư mục log mặc định (có thể đưa vào config)
)

func main() {
	flag.Parse()

	// --- 1. Tải Cấu hình YAML chính ---
	cfg, err := config.LoadConfig(*configFile)
	if err != nil {
		log.Fatalf("FATAL: Lỗi tải cấu hình từ '%s': %v", *configFile, err)
	}

	// --- 2. Thiết lập Logging ---
	// Tạo thư mục log chung nếu chưa có
	if err := os.MkdirAll(logDir, 0755); err != nil {
		log.Fatalf("FATAL: Lỗi tạo thư mục log '%s': %v", logDir, err)
	}

	// Tạo Status/Error Logger (ghi ra console và file riêng)
	statusLogger, statusLogFile := storage.SetupStatusLogger(&cfg.Logging, logDir)
	if statusLogFile != nil {
		defer statusLogFile.Close()
	}
	statusLogger.Info("--- Khởi chạy Gateway Modbus ---")

	// Tạo Data Logger (chỉ ghi JSON ra file)
	dataLogger, dataLogFile := storage.SetupDataLogger(&cfg.Logging, logDir)
	if dataLogFile != nil {
		defer dataLogFile.Close()
	}

	// --- 3. Khởi tạo Storage Backends cho Dữ liệu ---
	var dataWriters []storage.DataWriter

	// Logrus Writer cho dữ liệu (dùng dataLogger)
	logrusDataWriter, errL := storage.NewLogrusWriter(dataLogger)
	if errL != nil {
		statusLogger.WithError(errL).Error("Lỗi khởi tạo Logrus Data Writer")
	} else {
		dataWriters = append(dataWriters, logrusDataWriter)
	}

	// InfluxDB Writer (Bỏ qua vì đã loại bỏ khỏi config)
	// influxWriter, errI := storage.NewInfluxWriter(&cfg.Storage.InfluxDB) ...

	// Tạo MultiWriter để ghi dữ liệu vào các backend được kích hoạt
	multiDataWriter := storage.NewMultiWriter(dataWriters...)
	// Defer Close cho MultiWriter sẽ gọi Close của từng writer bên trong
	defer func() {
		statusLogger.Info("Đang đóng các storage data writers...")
		if err := multiDataWriter.Close(); err != nil {
			statusLogger.WithError(err).Error("Lỗi khi đóng storage data writers")
		} else {
			statusLogger.Info("Đã đóng các storage data writers.")
		}
	}()

	// --- 4. Khởi tạo và Chạy Goroutine cho từng Thiết bị ---
	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // Đảm bảo context được cancel khi main thoát

	activeDevices := 0
	var allCsvWriters []*storage.CsvWriter // Slice để quản lý các CSV writer cần đóng

	for _, deviceCfg := range cfg.Devices {
		if !deviceCfg.Enabled {
			statusLogger.Warnf("Thiết bị '%s' đang bị vô hiệu hóa trong cấu hình.", deviceCfg.Name)
			continue
		}

		// Đọc danh sách thanh ghi từ file CSV
		if deviceCfg.RegisterListFile == "" {
			statusLogger.Errorf("Thiết bị '%s' thiếu cấu hình 'register_list_file'. Bỏ qua.", deviceCfg.Name)
			continue
		}
		// Đường dẫn file CSV được coi là tương đối so với file config YAML
		csvPath := filepath.Join(filepath.Dir(*configFile), deviceCfg.RegisterListFile)
		registers, err := config.LoadRegistersFromCSV(csvPath)
		if err != nil {
			statusLogger.WithError(err).Errorf("Không thể đọc danh sách thanh ghi cho thiết bị '%s' từ file '%s'. Bỏ qua thiết bị này.", deviceCfg.Name, csvPath)
			continue
		}
		if len(registers) == 0 {
			statusLogger.Warnf("Danh sách thanh ghi cho thiết bị '%s' rỗng. Bỏ qua.", deviceCfg.Name)
			continue
		}
		statusLogger.Infof("Đã đọc %d thanh ghi cho thiết bị '%s'", len(registers), deviceCfg.Name)

		// Tạo các writer riêng cho thiết bị này (bao gồm cả CSV nếu bật)
		deviceSpecificWriters := []storage.DataWriter{}
		deviceSpecificWriters = append(deviceSpecificWriters, dataWriters...) // Bắt đầu với các writer chung (hiện chỉ có Logrus JSON)

		var devCsvWriter *storage.CsvWriter // Biến tạm cho CSV writer của device này
		if cfg.Logging.EnableCSV {
			devCsvWriter, err = storage.NewCsvWriter(true, logDir, deviceCfg.Name, registers)
			if err != nil {
				statusLogger.WithError(err).Errorf("Lỗi khởi tạo CSV Writer cho thiết bị '%s'", deviceCfg.Name)
			} else if devCsvWriter != nil {
				deviceSpecificWriters = append(deviceSpecificWriters, devCsvWriter)
				allCsvWriters = append(allCsvWriters, devCsvWriter) // Thêm vào danh sách để đóng sau
			}
		}
		// Tạo multi writer riêng cho thiết bị này
		deviceMultiWriter := storage.NewMultiWriter(deviceSpecificWriters...)

		// Tạo đối tượng Device, truyền vào statusLogger và deviceMultiWriter
		device := modbusclient.NewDevice(deviceCfg, registers, deviceMultiWriter, statusLogger)

		wg.Add(1)
		go device.RunPollingLoop(ctx, &wg) // Chạy trong goroutine
		activeDevices++
	}

	// Defer việc đóng tất cả các file CSV đã tạo
	defer func() {
		statusLogger.Info("Đang đóng các file CSV...")
		for _, cw := range allCsvWriters {
			if cw != nil {
				cw.Close() // Gọi hàm Close của CsvWriter
			}
		}
		statusLogger.Info("Đã đóng các file CSV.")
	}()

	if activeDevices == 0 {
		statusLogger.Warn("Không có thiết bị nào được kích hoạt trong file cấu hình. Thoát chương trình.")
		cancel()
		return
	}

	statusLogger.Infof("Đã khởi chạy %d goroutine cho các thiết bị. Nhấn Ctrl+C để dừng.", activeDevices)

	// --- 5. Chờ Tín hiệu Dừng ---
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan // Chờ Ctrl+C

	statusLogger.Warn("Đã nhận tín hiệu dừng. Đang yêu cầu các goroutine kết thúc...")
	cancel() // Gửi tín hiệu hủy

	statusLogger.Info("Đang chờ các goroutine hoàn thành...")
	wg.Wait() // Chờ tất cả dừng

	statusLogger.Info("Tất cả goroutine đã dừng. Chương trình kết thúc.")
}
