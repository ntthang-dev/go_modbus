package main // *** Đảm bảo đây là dòng đầu tiên ***

import (
	"context"
	"flag"
	"fmt"
	"io"
	stlog "log" // Đổi tên log chuẩn
	"log/slog"  // <<< Sử dụng slog
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	// !!! THAY 'modbus_register_slave' bằng tên module của bạn !!!
	"modbus_register_slave/config"
	"modbus_register_slave/modbusclient"
	"modbus_register_slave/portmanager"
	"modbus_register_slave/storage"

	"github.com/goburrow/modbus"
	// Bỏ import termui
)

var (
	configFile = flag.String("config", "config.yaml", "Đường dẫn tới file cấu hình YAML")
	logDir     = "logs_go_refactored"
	// Bỏ cờ TUI
)

// --- Hàm setup slog cho trạng thái/lỗi ---
// Logger này CHỈ ghi ra file
func SetupSlogStatusLogger(cfg *config.LoggingConfig, logDir string) (*slog.Logger, *os.File) {
	var level slog.Level
	switch strings.ToLower(cfg.Level) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	ts := time.Now().Format("20060102_150405")
	statusLogFilename := fmt.Sprintf(cfg.StatusErrorLogFile, ts)
	statusLogPath := filepath.Join(logDir, statusLogFilename)
	logFile, errLogFile := os.OpenFile(statusLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)

	var handler slog.Handler
	if errLogFile == nil {
		stlog.Printf("Status/Error log sẽ được ghi tại: %s (Level: %s)", statusLogPath, level.String())
		handler = slog.NewTextHandler(logFile, &slog.HandlerOptions{
			Level: level,
			ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
				if a.Key == slog.TimeKey {
					a.Value = slog.StringValue(a.Value.Time().Format("2006-01-02 15:04:05.000"))
				}
				return a
			},
		})
	} else {
		stlog.Printf("Lỗi mở file log status/error '%s': %v. Log trạng thái/lỗi sẽ bị mất.", statusLogPath, errLogFile)
		handler = slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: level})
	}
	logger := slog.New(handler)
	return logger, logFile
}

// --- Hàm setup slog cho dữ liệu ---
func SetupSlogDataLogger(cfg *config.LoggingConfig, logDir string) (*slog.Logger, *os.File) {
	ts := time.Now().Format("20060102_150405")
	dataLogFilename := fmt.Sprintf(cfg.DataLogFile, ts)
	jsonLogPath := filepath.Join(logDir, dataLogFilename)
	logFile, errLogFile := os.OpenFile(jsonLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)

	var handler slog.Handler
	if errLogFile == nil {
		stlog.Printf("Data log (JSON) sẽ được ghi tại: %s", jsonLogPath)
		handler = slog.NewJSONHandler(logFile, &slog.HandlerOptions{Level: slog.LevelInfo})
	} else {
		stlog.Printf("Lỗi mở file data log JSON '%s': %v. Data logger sẽ không ghi file.", jsonLogPath, errLogFile)
		handler = slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelInfo})
	}
	logger := slog.New(handler)
	return logger, logFile
}

// --- Hàm Chính ---
func main() {
	flag.Parse()

	cfg, err := config.LoadConfig(*configFile)
	if err != nil {
		stlog.Fatalf("FATAL: Lỗi tải cấu hình từ '%s': %v", *configFile, err)
	}
	if err := os.MkdirAll(logDir, 0755); err != nil {
		stlog.Fatalf("FATAL: Lỗi tạo thư mục log '%s': %v", logDir, err)
	}

	// Thiết lập logger (chỉ có chế độ Console cho phiên bản này)
	statusLogger, statusLogFile := SetupSlogStatusLogger(&cfg.Logging, logDir) // Logger này chỉ ghi file
	if statusLogFile != nil {
		defer statusLogFile.Close()
	}
	dataLogger, dataLogFile := SetupSlogDataLogger(&cfg.Logging, logDir)
	if dataLogFile != nil {
		defer dataLogFile.Close()
	}

	// Không set default logger nữa

	// Chạy trực tiếp logic Console Mode
	runConsoleMode(cfg, statusLogger, dataLogger)
}

// --- Hàm chạy chế độ Console Log cơ bản ---
func runConsoleMode(cfg *config.Config, statusLogger *slog.Logger, dataLogger *slog.Logger) {
	stlog.Println("--- Khởi chạy Gateway Modbus (Console Mode) ---") // Dùng log chuẩn

	// Khởi tạo Storage Backends
	var globalDataWriters []storage.DataWriter
	slogDataWriter, errL := storage.NewSlogDataWriter(dataLogger)
	if errL != nil {
		statusLogger.Error("Lỗi khởi tạo Slog Data Writer", slog.Any("error", errL))
	} else {
		globalDataWriters = append(globalDataWriters, slogDataWriter)
	}
	multiDataWriter := storage.NewMultiWriter(globalDataWriters...)
	defer func() { statusLogger.Info("Đóng storage data writers..."); multiDataWriter.Close() }()

	// Khởi tạo Port Managers
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
		stlog.Printf("Khởi tạo Port Manager cho cổng %s...", portName) // Dùng log chuẩn
		manager := portmanager.NewManager(portCfg, statusLogger)
		portManagers[portName] = manager
		wgPortManagers.Add(1)
		go func(m *portmanager.Manager) { defer wgPortManagers.Done(); m.Run(ctx) }(manager)
	}

	// Khởi tạo và Chạy Goroutine cho từng Thiết bị Logic
	var wgDevices sync.WaitGroup
	activeDevices := 0
	allCsvWriters := []*storage.CsvWriter{}
	consoleDataChan := make(chan modbusclient.UIUpdate, len(cfg.Devices)*5)

	for i := range cfg.Devices {
		deviceCfg := &cfg.Devices[i]
		if !deviceCfg.Enabled {
			continue
		}
		portMgr, ok := portManagers[deviceCfg.Connection.Port]
		if !ok {
			statusLogger.Error("Lỗi logic: Không tìm thấy Port Manager", slog.String("port", deviceCfg.Connection.Port), slog.String("device", deviceCfg.Name))
			continue
		}
		if deviceCfg.RegisterListFile == "" {
			statusLogger.Error("Thiết bị thiếu 'register_list_file'. Bỏ qua.", slog.String("device", deviceCfg.Name))
			continue
		}
		csvPath := filepath.Join(filepath.Dir(*configFile), deviceCfg.RegisterListFile)
		registers, err := config.LoadRegistersFromCSV(csvPath)
		if err != nil {
			statusLogger.Error("Lỗi đọc thanh ghi từ CSV. Bỏ qua thiết bị.", slog.String("device", deviceCfg.Name), slog.String("file", csvPath), slog.Any("error", err))
			continue
		}
		if len(registers) == 0 {
			statusLogger.Warn("Danh sách thanh ghi rỗng. Bỏ qua thiết bị.", slog.String("device", deviceCfg.Name))
			continue
		}
		stlog.Printf("Đã đọc %d thanh ghi cho thiết bị '%s'\n", len(registers), deviceCfg.Name) // Dùng log chuẩn

		deviceSpecificWriters := []storage.DataWriter{}
		deviceSpecificWriters = append(deviceSpecificWriters, globalDataWriters...)
		var devCsvWriter *storage.CsvWriter
		if cfg.Logging.EnableCSV {
			devCsvWriter, err = storage.NewCsvWriter(true, logDir, deviceCfg.Name, registers)
			if err != nil {
				statusLogger.Error("Lỗi khởi tạo CSV Writer", slog.String("device", deviceCfg.Name), slog.Any("error", err))
			} else if devCsvWriter != nil {
				deviceSpecificWriters = append(deviceSpecificWriters, devCsvWriter)
				allCsvWriters = append(allCsvWriters, devCsvWriter)
			}
		}
		deviceMultiWriter := storage.NewMultiWriter(deviceSpecificWriters...)

		// Gọi NewDevice đúng 6 tham số, uiChan là consoleDataChan
		device := modbusclient.NewDevice(*deviceCfg, registers, deviceMultiWriter, statusLogger, portMgr.GetRequestChannel(), consoleDataChan)

		wgDevices.Add(1)
		go device.RunPollingLoop(ctx, &wgDevices)
		activeDevices++
	}

	defer func() {
		stlog.Println("Đang đóng các file CSV...")
		for _, cw := range allCsvWriters {
			if cw != nil {
				cw.Close()
			}
		}
		stlog.Println("Đã đóng CSV.")
	}()

	if activeDevices == 0 {
		stlog.Println("WARN: Không có thiết bị nào được kích hoạt. Thoát.")
		cancel()
	} else {
		stlog.Printf("INFO: Đã khởi chạy %d goroutine. Nhấn Ctrl+C để dừng.\n", activeDevices)
	}

	// Goroutine riêng để xử lý và in ra Console
	var consoleWg sync.WaitGroup
	consoleWg.Add(1)
	go printToConsole(ctx, consoleDataChan, cfg.Devices, &consoleWg) // Truyền WaitGroup vào

	// Chờ Tín hiệu Dừng
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan
	stlog.Println("WARN: Đã nhận tín hiệu dừng...")
	cancel() // Gửi tín hiệu dừng cho tất cả

	stlog.Println("INFO: Đang chờ các goroutine Device...")
	wgDevices.Wait()
	stlog.Println("INFO: Goroutine Device đã dừng.")
	stlog.Println("INFO: Đang chờ các goroutine Port Manager...")
	wgPortManagers.Wait()
	stlog.Println("INFO: Goroutine Port Manager đã dừng.")

	stlog.Println("INFO: Đang đóng kênh dữ liệu console...")
	close(consoleDataChan) // Đóng channel TRƯỚC KHI chờ Console Printer

	stlog.Println("INFO: Đang chờ goroutine Console Printer...")
	consoleWg.Wait()
	stlog.Println("INFO: Goroutine Console Printer đã dừng.") // Chờ wg
	stlog.Println("INFO: Chương trình kết thúc (Console Mode).")
}

// --- Định nghĩa hàm printToConsole (cho chế độ -console) ---
// *** SỬA: Thêm tham số wg *sync.WaitGroup và gọi defer wg.Done() ***
func printToConsole(ctx context.Context, dataChan <-chan modbusclient.UIUpdate, deviceConfigs []config.DeviceConfig, wg *sync.WaitGroup) {
	defer wg.Done() // <<< THÊM DÒNG NÀY
	stlog.Println("INFO: Console printer goroutine started.")
	defer stlog.Println("INFO: Console printer goroutine stopped.")

	lastDeviceData := make(map[string]map[string]interface{})
	lastDeviceUpdate := make(map[string]time.Time)
	deviceRegisters := make(map[string][]config.RegisterInfo)

	for _, devCfg := range deviceConfigs {
		if devCfg.Enabled {
			lastDeviceData[devCfg.Name] = make(map[string]interface{})
			lastDeviceUpdate[devCfg.Name] = time.Time{}
			csvPath := filepath.Join(filepath.Dir(*configFile), devCfg.RegisterListFile)
			regs, err := config.LoadRegistersFromCSV(csvPath)
			if err == nil {
				deviceRegisters[devCfg.Name] = regs
			} else {
				stlog.Printf("ERROR: Lỗi đọc CSV cho '%s' trong console printer: %v\n", devCfg.Name, err)
			}
		}
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case update, ok := <-dataChan:
			if !ok {
				return
			} // Thoát khi channel đóng
			if !update.IsStatus {
				lastDeviceData[update.DeviceName] = update.Data
				lastDeviceUpdate[update.DeviceName] = update.Timestamp
			}
		case <-ticker.C:
			fmt.Println("\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n")
			fmt.Println("========================= CONSOLE MONITOR =========================")
			fmt.Println("Thời gian cập nhật:", time.Now().Format("15:04:05"))
			fmt.Println("-----------------------------------------------------------------")

			deviceNames := make([]string, 0, len(lastDeviceData))
			for name := range lastDeviceData {
				deviceNames = append(deviceNames, name)
			}
			sort.Strings(deviceNames)

			for _, devName := range deviceNames {
				fmt.Printf("\n========== Thiết bị: %s (Cập nhật lần cuối: %s) ==========\n", devName, lastDeviceUpdate[devName].Format("15:04:05"))
				data, dataOk := lastDeviceData[devName]
				registers, regsOk := deviceRegisters[devName]
				if !dataOk || !regsOk || len(data) == 0 {
					fmt.Println("  (Chưa có dữ liệu hoặc lỗi cấu hình thanh ghi)")
					continue
				}

				currentGroup := ""
				sort.SliceStable(registers, func(i, j int) bool { return registers[i].Address < registers[j].Address })
				for _, regInfo := range registers {
					groupGuess := regInfo.Name
					if idx := strings.Index(regInfo.Name, "_"); idx > 0 {
						groupGuess = regInfo.Name[:idx] /* ... logic nhóm ... */
					}
					if regInfo.Name == "Frequency" {
						groupGuess = "Frequency"
					}
					if groupGuess != currentGroup {
						if currentGroup != "" {
							fmt.Println("  ---------------------------------------")
						}
						fmt.Printf("  --- %s ---\n", groupGuess)
						currentGroup = groupGuess
					}

					value, ok := data[regInfo.Name]
					displayValue := "N/A"
					prefix := ""
					if ok {
						isErrorOrNA := false
						if strVal, isString := value.(string); isString {
							if strings.Contains(strVal, "ERROR") || strings.Contains(strVal, "INVALID") || strings.Contains(strVal, "N/A_") || strings.Contains(strVal, "TIMEOUT") || strings.Contains(strVal, "CANCELLED") {
								prefix = "[LỖI/NA] "
								isErrorOrNA = true
							}
						}
						switch v := value.(type) {
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
							displayValue = fmt.Sprintf("%v", value)
						} else {
							switch v := value.(type) {
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
					} else {
						prefix = "[LỖI] "
						displayValue = "MISSING"
					}
					fmt.Printf("  %-30s: %s%s\n", regInfo.Name, prefix, displayValue)
				}
			}
			fmt.Println("=================================================================")
		}
	}
}

// --- Hàm chạy chế độ TUI (Placeholder) ---
func runTUI(cfg *config.Config, statusLogger *slog.Logger, dataLogger *slog.Logger) error {
	slog.Error("Chế độ TUI chưa được triển khai trong phiên bản này.") // Dùng slog
	fmt.Println("Chế độ TUI chưa được triển khai đầy đủ.")
	// fmt.Println("Vui lòng chạy lại mà không có cờ -tui.") // Bỏ dòng này
	time.Sleep(3 * time.Second)
	return fmt.Errorf("TUI mode not fully implemented yet")
}

// --- Các hàm phụ trợ (getModbusExceptionMessage, SanitizeValue) ---
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

// SanitizeValue giờ nằm trong package storage
// func SanitizeValue(value interface{}) interface{} { return storage.SanitizeValue(value) }

// --- Cần thêm import này nếu chưa có ---
