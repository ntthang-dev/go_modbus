module modbus_register_slave

go 1.21 // <<< Nâng cấp phiên bản Go

// toolchain go1.21.0 // (Tùy chọn) Chỉ định toolchain cụ thể

require (
	github.com/goburrow/modbus v0.1.0
	// github.com/sirupsen/logrus v1.9.3 // <<< Đã loại bỏ logrus
	gopkg.in/yaml.v3 v3.0.1
)

require github.com/goburrow/serial v0.1.0 // indirect
