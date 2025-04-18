# Go Modbus Gateway (RTU Multi-Slave) 🚀

## Giới thiệu 📖

Đây là một ứng dụng Gateway Modbus được viết bằng Go (phiên bản 1.21+), được thiết kế để đọc dữ liệu từ nhiều thiết bị Modbus RTU Slave khác nhau được kết nối trên cùng một đường truyền vật lý (ví dụ: RS485 multi-drop thông qua một cổng COM).

Mục tiêu chính của dự án là tạo ra một gateway đáng tin cậy, linh hoạt và có khả năng mở rộng để:
* 📊 **Thu thập dữ liệu** từ các thiết bị công nghiệp (đồng hồ điện, cảm biến...).
* 🌐 **Cung cấp dữ liệu** đó cho các hệ thống giám sát và điều khiển từ xa (thông qua Modbus TCP trong tương lai).
* ⚙️ **Hỗ trợ cấu hình linh hoạt** qua file.
* 📝 **Ghi log chi tiết** và có cấu trúc.
* 🔮 *(Tương lai)* **Hỗ trợ điều khiển thiết bị** và lưu trữ dữ liệu lịch sử.

## Tính năng Hiện tại (Sau Giai đoạn 1) ✅

* **Đọc Đa Slave (Multi-Drop):** 🧩 Có khả năng đọc dữ liệu từ nhiều Modbus RTU Slave trên cùng một cổng Serial (COM port) bằng cách sử dụng cơ chế quản lý truy cập tuần tự (Port Manager).
* **Cấu hình Linh hoạt:** 🛠️
    * Thông tin kết nối (cổng COM, baudrate, parity...), danh sách thiết bị, và cài đặt logging được quản lý qua file `config.yaml`.
    * Danh sách thanh ghi (register map) cho từng loại thiết bị được định nghĩa trong các file `.csv` riêng biệt, dễ dàng thêm/sửa đổi.
* **Hỗ trợ Đa dạng Kiểu dữ liệu:** 📐 Giải mã các kiểu dữ liệu Modbus phổ biến:
    * Số thực: `FLOAT32`, `FLOAT64`
    * Số nguyên: `INT16U`, `INT16`, `INT32U`, `INT32`, `INT64`
    * Chuỗi: `UTF8`
    * Thời gian: `DATETIME` (theo chuẩn IEC 870-5-4)
    * Bitmap: `BITMAP16`, `BITMAP32` (trả về giá trị `uint16`/`uint32` thô)
    * Tùy chỉnh: `CUSTOM_PF` (logic riêng cho Power Factor)
* **Xử lý Giá trị N/A:** ❌ Tự động phát hiện và xử lý các giá trị "Not Available" trả về từ thiết bị (thường trả về giá trị 0 và ghi log cảnh báo vào file).
* **Logging có Cấu trúc (`slog`):** 🖋️
    * Sử dụng thư viện `log/slog` chuẩn của Go (từ 1.21).
    * **Log Trạng thái/Lỗi:** Ghi chi tiết các sự kiện hoạt động, lỗi kết nối, lỗi Modbus vào file `gateway_status_[timestamp].log` với định dạng Text.
    * **Log Dữ liệu:** Ghi dữ liệu đọc được từ các thiết bị vào file `modbus_data_slog_[timestamp].log` với định dạng **JSON**, thuận tiện cho việc xử lý tự động sau này.
    * **(Tùy chọn) Log CSV:** Có thể bật/tắt việc ghi dữ liệu song song ra các file `.csv` riêng biệt cho từng thiết bị (`device_[name]_data_[timestamp].csv`).
* **Chế độ Console:** 🖥️ Chạy ở chế độ dòng lệnh, định kỳ in ra bảng dữ liệu đọc được từ các thiết bị một cách có cấu trúc, không bị xen kẽ. Log trạng thái/lỗi chi tiết được ghi vào file riêng, không làm rối màn hình console.
* **Shutdown Mềm:** 🛑 Xử lý tín hiệu `Ctrl+C` (SIGINT/SIGTERM) để đóng các kết nối và file log một cách an toàn trước khi thoát.

## Cấu trúc Chương trình 🏗️

Chương trình được tổ chức thành các package để tăng tính module hóa của go:

* `main`: Điểm vào chính của ứng dụng, xử lý tham số dòng lệnh, khởi tạo các thành phần, quản lý vòng đời ứng dụng và logic hiển thị console.
* `config`: Định nghĩa các struct cấu hình (YAML, CSV) và cung cấp hàm để đọc, xác thực cấu hình từ file.
* `portmanager`: Quản lý việc truy cập tuần tự vào cổng COM vật lý, đảm bảo chỉ có một giao dịch Modbus xảy ra tại một thời điểm trên bus.
* `modbusclient`: Đóng gói logic cho một thiết bị Modbus cụ thể, bao gồm vòng lặp đọc dữ liệu định kỳ, gửi yêu cầu đến `PortManager`, giải mã dữ liệu (`decodeBytes`), và gửi dữ liệu/trạng thái đến `main` qua channel.
* `storage`: Định nghĩa interface `DataWriter` và cung cấp các implementation cụ thể cho việc ghi dữ liệu (`SlogDataWriter`, `CsvWriter`). Chứa các hàm tiện ích như `SanitizeValue`.

## Cấu hình

1.  [**`config.yaml`:**](config.yaml)
    * `logging`: Cấu hình level log (`debug`, `info`, `warn`, `error`), bật/tắt ghi CSV, mẫu tên file log.
    * `devices`: Danh sách các thiết bị Modbus cần giám sát. Mỗi device bao gồm:
        * `name`: Tên định danh duy nhất cho thiết bị.
        * `enabled`: `true` hoặc `false` để bật/tắt thiết bị.
        * `tags`: Các thẻ (metadata) tùy chọn để gắn cho dữ liệu (ví dụ: vị trí, panel).
        * `register_list_file`: Tên file CSV chứa danh sách thanh ghi cho thiết bị này.
        * `connection`: Thông số kết nối Modbus RTU (port, baudrate, databits, parity, stopbits, slaveid, timeout_ms, address_base).
        * `polling_interval_ms`: Chu kỳ đọc dữ liệu cho thiết bị này (tính bằng mili giây).
    * Xem file `config.yaml` mẫu để biết chi tiết.

2.  **`registers_*.csv`:**
    * Mỗi file CSV định nghĩa danh sách thanh ghi cho một loại thiết bị.
    * Các cột bắt buộc:
        * `Name`: Tên định danh cho thanh ghi (sẽ là key trong dữ liệu log).
        * `Address`: Địa chỉ Modbus của thanh ghi (**1-based**, tức là địa chỉ đọc từ tài liệu thiết bị).
        * `Type`: Kiểu dữ liệu của thanh ghi (xem danh sách các kiểu được hỗ trợ ở trên). **Phải viết hoa.**
        * `Length`: Số lượng thanh ghi 16-bit cần đọc. Ví dụ: `FLOAT32` cần `Length: 2`, `INT16U` cần `Length: 1`, `BITMAP32` cần `Length: 2`.
    * Xem file `registers_pm5xxx.csv` mẫu.

## Yêu cầu Hệ thống

* Go phiên bản **1.21** trở lên đã được cài đặt.
* Hệ điều hành Windows hoặc Linux.
* Cổng Serial (COM port trên Windows, `/dev/tty...` trên Linux) đã được cấu hình đúng và kết nối vật lý tới các thiết bị Modbus RTU thông qua bộ chuyển đổi RS485 phù hợp.
* Thông tin chính xác về Slave ID, địa chỉ thanh ghi, kiểu dữ liệu của các thiết bị Modbus.

## Xây dựng và Chạy

1.  **Lấy code:** Clone repository về máy.
2.  **Di chuyển vào thư mục gốc:** `cd path/to/your/project`
3.  **Cài đặt Dependencies:**
    ```bash
    go mod tidy
    ```
4.  **Kiểm tra Biên dịch:**
    ```bash
    go build ./...
    ```
5.  **Biên dịch ra file thực thi:**
    ```bash
    # Cho Windows
    go build -o ModbusGateway.exe .
    # Cho Linux
    go build -o modbus-gateway .
    ```
6.  **Chạy chương trình (Console Mode):**
    ```bash
    # Windows
    .\ModbusGateway.exe -config config.yaml
    # Linux
    ./modbus-gateway -config config.yaml
    ```
    *(Chương trình sẽ chạy ở chế độ console, in dữ liệu ra màn hình và ghi log vào file)*
7.  Nhấn `Ctrl+C` để dừng chương trình.

## Lộ trình Phát triển (Các Giai đoạn Tiếp theo)

* ~~[x]**Giai đoạn 1:** 🚀 Nâng cấp Nền tảng & Ổn định Console Mode.~~
* [ ] **Giai đoạn 2:** ✅ Viết Unit Test & Thiết lập CI.
* [ ] **Giai đoạn 3:** 💾 Lưu trữ & Đệm Dữ liệu Cục bộ Tin cậy (SQLite/Queue).
* [ ] **Giai đoạn 4:** 🌐 Modbus TCP Server & Điều khiển Cơ bản.
* [ ] **Giai đoạn 5:** 🖥️ Giao diện Người dùng (TUI hoặc Web UI).
* [ ] **Giai đoạn 6:** ✨ Hoàn thiện & Nâng cao (TLS, Variable Send Rate, Snapshot...).
* [ ] **Giai đoạn 7:** 🍓 Deployment & Tối ưu hóa Raspberry Pi.

## Đóng góp / Giấy phép
[Giấy phép MIT](https://opensource.org/licenses/MIT)

*(Dự án này được cấp phép theo Giấy phép MIT. Xem file [LICENSE](/LICENSE) để biết thêm chi tiết.)*

*(Thêm thông tin về đóng góp hoặc vấn đề khác nếu cần)*

