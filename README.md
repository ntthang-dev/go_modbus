# Chương trình Go Modbus RTU Client đọc dữ liệu từ Thiết bị Đo lường

## 1. Giới thiệu

### Mục đích
Chương trình này được viết bằng ngôn ngữ Go (Golang) với mục đích kết nối và đọc dữ liệu từ một thiết bị hỗ trợ giao thức Modbus RTU (ví dụ: đồng hồ đo điện Schneider Electric PM series, cảm biến công nghiệp...) thông qua cổng COM (sử dụng bộ chuyển đổi USB-to-RS485 nếu cần) trên hệ điều hành Windows. Dữ liệu đọc được sẽ hiển thị trên màn hình console và được ghi log dưới dạng file JSON và tùy chọn file CSV để lưu trữ và phân tích sau này.

### Đối tượng sử dụng
Tài liệu và code này hướng đến những người mới bắt đầu tìm hiểu về:
* Giao thức Modbus RTU.
* Lập trình với ngôn ngữ Go.
* Giao tiếp với phần cứng qua cổng serial trên Windows.

### Công nghệ sử dụng
* **Ngôn ngữ:** Go (Golang) phiên bản 1.17.6 trở lên.
* **Thư viện Modbus:** `github.com/goburrow/modbus` - Một thư viện phổ biến để làm việc với Modbus trong Go.
* **Logging:**
    * `log`: Gói log chuẩn của Go cho các thông báo cơ bản.
    * `github.com/sirupsen/logrus`: Thư viện bên thứ ba cho structured logging (ghi log có cấu trúc dạng JSON).
* **Giao tiếp:** Modbus RTU qua cổng COM (Serial Port) trên Windows (bao gồm cả cổng vật lý và cổng ảo như `com0com`).
* **Lưu trữ log:** File JSON Lines (`.log`) và tùy chọn file CSV (`.csv`).

## 2. Modbus là gì? (Giải thích đơn giản)

Hãy tưởng tượng bạn có một thiết bị đo lường (như đồng hồ điện) và bạn muốn lấy thông số từ nó bằng máy tính. Modbus là một "ngôn ngữ" (giao thức) chung để máy tính (gọi là **Master**) có thể "nói chuyện" và yêu cầu thông tin từ thiết bị đo (gọi là **Slave**).

* **Master:** Là chương trình chúng ta viết bằng Go. Nó chủ động gửi yêu cầu hỏi dữ liệu.
* **Slave:** Là thiết bị đo lường (đồng hồ, cảm biến...). Nó lắng nghe yêu cầu từ Master và trả lời.
* **Modbus RTU:** Là một cách truyền "ngôn ngữ" Modbus qua đường dây vật lý kiểu cũ gọi là cổng serial (RS485 hoặc RS232). Dữ liệu được gửi dưới dạng nhị phân (binary). (Còn có Modbus TCP dùng mạng Ethernet). Chương trình này dùng Modbus RTU.
* **Thanh ghi (Registers):** Dữ liệu trên thiết bị Slave được lưu trữ trong các "ô nhớ" gọi là thanh ghi. Mỗi thanh ghi có một **địa chỉ** duy nhất để Master biết cần đọc/ghi vào đâu. Có nhiều loại thanh ghi, nhưng chương trình này tập trung vào **Holding Registers** (thường dùng để đọc và ghi giá trị cấu hình hoặc đo lường).
* **Địa chỉ Thanh ghi (0-based vs 1-based):** Đây là điểm dễ gây nhầm lẫn.
    * **Tài liệu thiết bị:** Thường ghi địa chỉ bắt đầu từ 1 (1-based) hoặc theo chuẩn (ví dụ: Holding Register bắt đầu từ 40001). Ví dụ: thanh ghi đầu tiên là 40001.
    * **Thư viện Modbus (Go, Python...):** Khi lập trình, các hàm đọc/ghi thường yêu cầu địa chỉ bắt đầu từ 0 (0-based). Ví dụ: để đọc thanh ghi 40001, bạn cần truyền số `0` vào hàm. Để đọc thanh ghi 40010, bạn truyền số `9`.
    * **Trong code này:** Chúng ta dùng hằng số `addressBase = 1`. Điều này cho phép bạn nhập địa chỉ **1-based** (giống tài liệu) vào danh sách `registersToRead`. Code sẽ tự động trừ đi 1 trước khi gọi hàm của thư viện Modbus. Nếu tài liệu của bạn dùng địa chỉ 0-based, hãy đổi `addressBase` thành `0`.

## 3. Go Lang là gì? (Giải thích ngắn gọn)

Go (hay Golang) là một ngôn ngữ lập trình hiện đại được phát triển bởi Google. Nó có các đặc điểm nổi bật:

* **Biên dịch:** Code Go được dịch trực tiếp ra mã máy, giúp chương trình chạy nhanh.
* **Cú pháp rõ ràng:** Ngôn ngữ được thiết kế đơn giản, dễ đọc, dễ học hơn so với một số ngôn ngữ khác như C++.
* **Hỗ trợ đồng thời (Concurrency):** Go rất mạnh trong việc xử lý nhiều tác vụ cùng lúc (sử dụng goroutine và channel), rất phù hợp cho các ứng dụng mạng, hệ thống, và xử lý I/O như đọc cổng serial.
* **Thư viện chuẩn mạnh mẽ:** Cung cấp sẵn nhiều công cụ hữu ích.

Go là lựa chọn tốt cho dự án này vì hiệu năng tốt, xử lý I/O hiệu quả và cộng đồng phát triển mạnh mẽ.

## 4. Cấu trúc Chương trình

Chương trình được viết trong một file Go duy nhất (ví dụ: `modbus_go.go`) và có các thành phần chính:

* **Constants (Hằng số):** Nằm ở đầu file, dùng để cấu hình các thông số kết nối (`portNameSimple`, `baudRate`, `parity`, `stopBits`, `slaveID`, `timeoutMs`), cấu hình địa chỉ (`addressBase`), và cấu hình logging (`logDir`, `logLevel`...).
* **Struct `RegisterInfo`:** Định nghĩa cấu trúc để lưu thông tin về mỗi thanh ghi cần đọc:
    * `Name`: Tên gợi nhớ (dùng trong log và hiển thị).
    * `Address`: Địa chỉ Modbus (theo `addressBase`).
    * `Type`: Kiểu dữ liệu cần giải mã (ví dụ: "FLOAT32", "INT16U", "UTF8", "DATETIME", "CUSTOM_PF").
    * `Length`: Số lượng thanh ghi Modbus (16-bit) mà kiểu dữ liệu này chiếm dụng (ví dụ: FLOAT32 cần 2 thanh ghi nên Length=2, INT16U cần 1 thanh ghi nên Length=1).
* **Slice `registersToRead`:** Đây là **danh sách quan trọng nhất** bạn cần chỉnh sửa. Nó chứa các đối tượng `RegisterInfo` cho tất cả các thanh ghi bạn muốn chương trình đọc từ thiết bị. **BẠN PHẢI KIỂM TRA VÀ ĐIỀN THÔNG TIN CHÍNH XÁC TỪ TÀI LIỆU THIẾT BỊ VÀO ĐÂY.**
* **Hàm `main()`:**
    * Thiết lập xử lý tín hiệu dừng (Ctrl+C).
    * Gọi `setupLogging()` để cấu hình `logrus` và tùy chọn `csv`.
    * Tạo đường dẫn cổng COM chuẩn cho Windows (`\\.\COMx`).
    * Khởi tạo Modbus RTU handler và client bằng thư viện `goburrow/modbus`.
    * Bắt đầu vòng lặp `for running`:
        * Kiểm tra và thực hiện kết nối (`handler.Connect()`) nếu chưa kết nối hoặc bị mất kết nối. Có logic thử lại sau 5 giây.
        * Nếu kết nối thành công, gọi `readAllRegisters()` để đọc dữ liệu.
        * **Hiển thị Console:** In kết quả đọc được (hoặc lỗi) ra màn hình theo từng nhóm cho dễ nhìn.
        * **Ghi Log:** Chuẩn bị dữ liệu (xử lý NaN/Inf), ghi structured log bằng `logrus` và ghi file CSV (nếu bật).
        * Dừng 1 giây (`time.Sleep`) trước khi lặp lại.
    * Gọi `closeLogs()` khi chương trình kết thúc.
* **Hàm `readAllRegisters()`:**
    * Lặp qua danh sách `registersToRead`.
    * Tính toán địa chỉ 0-based từ địa chỉ 1-based và `addressBase`.
    * Gọi `client.ReadHoldingRegisters()` để đọc từng thanh ghi hoặc cụm nhỏ (dựa trên `Length` trong `RegisterInfo`).
    * Gọi `decodeBytes()` để giải mã dữ liệu nhận được.
    * Trả về một map chứa tên thanh ghi và giá trị đã giải mã (hoặc thông báo lỗi).
* **Hàm `decodeBytes()`:**
    * Nhận dữ liệu dạng `[]byte` và `RegisterInfo`.
    * Dựa vào `regInfo.Type`, chọn logic giải mã phù hợp (dùng `encoding/binary` cho các kiểu số, xử lý chuỗi cho `UTF8`, xử lý bit cho `DATETIME` theo chuẩn IEC, logic tùy chỉnh cho `CUSTOM_PF`).
    * Xử lý giá trị N/A theo định nghĩa kiểu dữ liệu.
    * **Quan trọng:** Hàm này giả định thứ tự byte là **Big Endian** (phổ biến trong Modbus) và giả định **scaling factor** cho `CUSTOM_PF`. Bạn có thể cần sửa lại nếu thiết bị của bạn dùng Little Endian hoặc có scaling factor khác.
* **Hàm `setupLogging()`, `closeLogs()`:** Quản lý việc tạo thư mục log, cấu hình `logrus` (ghi JSON ra console và file), cấu hình `csv.Writer` (ghi CSV), và đóng file khi kết thúc.
* **Hàm `handleModbusError()`, `getModbusExceptionMessage()`:** Giúp ghi log lỗi Modbus hoặc lỗi giao tiếp khác một cách chi tiết và dễ hiểu hơn.
* **Hàm `signalHandler()`:** Bắt tín hiệu Ctrl+C để dừng vòng lặp chính một cách mềm mại.
* **Hàm `SanitizeValue()`:** Xử lý giá trị NaN/Inf trước khi ghi log JSON.

## 5. Hướng dẫn Cài đặt và Chạy

### Yêu cầu Hệ thống
* **Hệ điều hành:** Windows.
* **Go:** Phiên bản 1.17.6 trở lên (khuyến nghị cài bản mới nhất). Tải tại: [https://go.dev/dl/](https://go.dev/dl/)
* **Git:** Cần thiết để tải các thư viện Go. Tải tại: [https://git-scm.com/](https://git-scm.com/)
* **Thiết bị Modbus RTU:** Thiết bị thực tế bạn muốn đọc dữ liệu.
* **Bộ chuyển đổi USB-to-RS485:** Nếu máy tính không có cổng RS485/RS232 trực tiếp, bạn cần bộ chuyển đổi này và cài đặt driver tương ứng cho nó trên Windows.
* **(Tùy chọn) Phần mềm Cổng COM ảo:** Nếu muốn thử nghiệm mà không có thiết bị thực, bạn cần phần mềm tạo cặp cổng COM ảo. **`com0com` được khuyến nghị** (tải trên SourceForge) vì các thử nghiệm trước cho thấy nó tương thích tốt hơn với thư viện Go so với một số phần mềm khác.

### Các bước Cài đặt
1.  **Lấy Code:** Tải hoặc clone code từ nơi lưu trữ về máy tính của bạn.
2.  **Mở Terminal:** Mở Command Prompt (cmd) hoặc PowerShell trong thư mục gốc của dự án (nơi chứa file `.go`).
3.  **Khởi tạo Module (Nếu là dự án mới):**
    ```bash
    go mod init <tên_module_của_bạn>
    # Ví dụ: go mod init mymodbusreader
    ```
4.  **Tải Thư viện:**
    ```bash
    go get [github.com/goburrow/modbus](https://github.com/goburrow/modbus)
    go get [github.com/sirupsen/logrus](https://github.com/sirupsen/logrus)
    # encoding/csv, encoding/binary, và các gói chuẩn khác đã có sẵn
    ```
5.  **Dọn dẹp Dependencies:**
    ```bash
    go mod tidy
    ```

### Cấu hình Chương trình
Đây là bước **quan trọng nhất** để chương trình chạy đúng với thiết bị của bạn. Mở file code Go (ví dụ `modbus_go.go`) và chỉnh sửa các phần sau:

1.  **Hằng số Kết nối:**
    * `portNameSimple`: Đặt thành tên cổng COM mà bộ chuyển đổi USB-to-RS485 của bạn được nhận diện trên Windows (ví dụ: "COM3", "COM4"...). Kiểm tra trong Device Manager.
    * `baudRate`: Đặt đúng tốc độ baud của thiết bị (ví dụ: 19200, 9600...).
    * `parity`: Đặt đúng parity ("N", "E", "O").
    * `stopBits`: Đặt đúng stop bits (1 hoặc 2).
    * `slaveID`: Đặt đúng Slave ID của thiết bị Modbus.
    * `timeoutMs`: Thời gian chờ phản hồi (ms), có thể tăng nếu mạng chậm hoặc thiết bị xử lý lâu.
2.  **Hằng số `addressBase`:**
    * Đặt là `1` nếu địa chỉ bạn nhập vào `registersToRead` là địa chỉ 1-based (giống tài liệu).
    * Đặt là `0` nếu địa chỉ bạn nhập vào `registersToRead` đã là địa chỉ 0-based.
3.  **Slice `registersToRead`:**
    * **Xác minh từng dòng:** Đối chiếu **từng** thanh ghi trong danh sách này với tài liệu **chính thức** của thiết bị.
    * **`Address`:** Đảm bảo đúng địa chỉ (theo `addressBase` bạn đã chọn).
    * **`Type`:** Đảm bảo đúng kiểu dữ liệu (`FLOAT32`, `INT16U`, `INT16`, `INT32U`, `INT32`, `INT64`, `UTF8`, `DATETIME`, `CUSTOM_PF`...).
    * **`Length`:** Đảm bảo đúng số lượng thanh ghi 16-bit mà kiểu dữ liệu đó chiếm dụng (ví dụ: FLOAT32/INT32U/INT32 là 2, INT64 là 4, INT16U/INT16/CUSTOM_PF là 1, UTF8 tùy độ dài chuỗi, DATETIME là 4). **Sai `Length` là nguyên nhân phổ biến gây lỗi Exception 3.**
    * Thêm/bớt/sửa các thanh ghi theo nhu cầu của bạn.

### Chạy Chương trình
1.  **Kết nối Phần cứng:** Đảm bảo thiết bị Modbus được nối đúng vào bộ chuyển đổi USB-to-RS485 và bộ chuyển đổi được cắm vào máy tính.
2.  **Chạy lệnh:** Mở terminal trong thư mục dự án và chạy:
    ```bash
    go run .\tên_file_go_của_bạn.go
    # Ví dụ: go run .\modbus_go.go
    ```
3.  **Quan sát:**
    * **Console:** Theo dõi các thông báo kết nối, lỗi (nếu có), và quan trọng nhất là bảng giá trị các thanh ghi được in ra sau mỗi chu kỳ đọc.
    * **File Log:** Kiểm tra thư mục `logs_go_final` (hoặc tên bạn đặt). Sẽ có file `.log` chứa structured log dạng JSON và file `.csv` chứa dữ liệu dạng bảng (nếu `enableCSVLogging = true`).

4.  **Dừng chương trình:** Nhấn `Ctrl + C` trong cửa sổ terminal. Chương trình sẽ bắt tín hiệu, dừng vòng lặp đọc và đóng các kết nối/file log.

## 6. Giải thích Output

* **Console:**
    * `--- Bắt đầu chương trình...`: Thông báo khởi động.
    * `Sử dụng đường dẫn cổng: \\.\COMx`: Hiển thị đường dẫn Windows đang dùng.
    * `Đang thử kết nối...`: Thông báo khi cố gắng kết nối.
    * `>>> Kết nối thành công!`: Thông báo kết nối OK.
    * `Lỗi Modbus từ Slave...`, `Timeout...`, `Lỗi giao tiếp khác...`: Các thông báo lỗi chi tiết từ `logrus`.
    * `--- Giá trị đọc được lúc HH:MM:SS ---`: Bắt đầu khối hiển thị dữ liệu của một chu kỳ đọc.
    * `--- TênNhóm ---`: Tiêu đề nhóm các thanh ghi liên quan.
    * `TênThanhGhi : GiáTrị`: Hiển thị giá trị đọc được.
        * `[LỖI] READ_ERROR/DECODE_ERROR/...`: Nếu có lỗi khi đọc/giải mã thanh ghi đó.
        * `[NaN] NaN`: Nếu giá trị đọc được là NaN (Not a Number).
        * Giá trị số thực được làm tròn 4 chữ số thập phân.
        * Chuỗi được đặt trong dấu `""`.
    * `====================================`: Kết thúc khối hiển thị.
* **File Log JSON (`.log`):** Mỗi dòng là một bản ghi JSON chứa:
    * `time`: Timestamp chi tiết (RFC3339Nano).
    * `level`: Cấp độ log (`info`, `warn`, `error`, `debug`...).
    * `msg`: Thông báo chính (`Modbus Data Read`, `Lỗi Modbus từ Slave`...).
    * `device`: Tên thiết bị (nếu dùng cấu trúc package).
    * `slave_id`: Slave ID.
    * `read_duration_ms`: Thời gian đọc dữ liệu (ms).
    * `registers_ok`, `registers_error`, `registers_total_attempted`: Thống kê số thanh ghi đọc thành công/lỗi.
    * **Các trường dữ liệu:** Tên thanh ghi làm key, giá trị đọc được làm value (NaN/Inf được ghi là `null`).
    * Các trường lỗi bổ sung (`error`, `exception_code`...).
* **File Log CSV (`.csv`):**
    * Dòng đầu tiên là header (Timestamp và tên các thanh ghi).
    * Mỗi dòng tiếp theo chứa timestamp và giá trị của các thanh ghi tại thời điểm đó. Lỗi hoặc NaN/Inf được ghi dưới dạng chuỗi (`READ_ERROR`, `NaN`...).

## 7. Xử lý Lỗi thường gặp

* **`KHÔNG THỂ KẾT NỐI tới cổng COMx: The system cannot find the file specified.`:** Windows không tìm thấy cổng COM bạn chỉ định. Kiểm tra lại `portNameSimple` trong code, đảm bảo driver USB-to-Serial đã cài đúng và cổng COM xuất hiện trong Device Manager.
* **`KHÔNG THỂ KẾT NỐI tới cổng COMx: The parameter is incorrect.`:** Cổng COM tồn tại nhưng không thể cấu hình đúng. Thường do xung đột driver hoặc vấn đề với cổng COM ảo (nếu dùng). **Thử dùng `com0com` nếu đang dùng cổng ảo.**
* **`KHÔNG THỂ KẾT NỐI tới cổng COMx: Access is denied.`:** Không có quyền truy cập cổng COM. Thử chạy terminal với quyền Administrator.
* **`Lỗi Modbus từ Slave: exception '2' (Illegal Data Address)`:** Địa chỉ (`Address`) bạn yêu cầu đọc không tồn tại trên thiết bị, hoặc `addressBase` của bạn bị sai. Kiểm tra lại địa chỉ 0-based/1-based với manual.
* **`Lỗi Modbus từ Slave: exception '3' (Illegal Data Value)`:** Số lượng thanh ghi (`Length`) bạn yêu cầu đọc không hợp lệ cho địa chỉ bắt đầu đó. **Kiểm tra lại `Length` cho từng thanh ghi** trong `registersToRead` với manual. Đây là lỗi bạn đã gặp với thanh ghi PF.
* **`Timeout khi chờ phản hồi...`:** Thiết bị không trả lời kịp thời gian `timeoutMs`. Nguyên nhân có thể do: sai Slave ID, đường truyền RS485 nhiễu/lỗi cáp, thiết bị bị treo, `timeoutMs` quá ngắn.
* **`Lỗi giải mã thanh ghi` / `INVALID_...` / Giá trị đọc về không đúng:** Kiểm tra lại `Type` và `Length` của thanh ghi trong `registersToRead`. Kiểm tra logic trong hàm `decodeBytes` (đặc biệt là byte order và scaling factor nếu có).
* **Dữ liệu UTF8 bị lỗi (`INVALID_UTF8_DATA` hoặc `\ufffd`):** Kiểm tra `Address`, `Length` của thanh ghi chuỗi. Có thể dữ liệu trên thiết bị thực sự không phải UTF8 hợp lệ hoặc thứ tự byte khác.

## 8. Hướng phát triển tiếp

Chương trình hiện tại là một nền tảng tốt. Bạn có thể mở rộng thêm:

* **Tái cấu trúc thành Packages:** Chia code thành các package `config`, `modbusclient`, `storage` để dễ quản lý và mở rộng.
* **Đọc cấu hình từ File:** Sử dụng package `config` để đọc toàn bộ cấu hình (thiết bị, thanh ghi, logging, database) từ file YAML hoặc JSON.
* **Lưu vào Database:** Triển khai `storage.DataWriter` để ghi dữ liệu vào InfluxDB, TimescaleDB hoặc SQL database khác.
* **Thêm chức năng Ghi (Write):** Viết thêm hàm để ghi giá trị vào các thanh ghi cho phép (Writable Registers).
* **Giao diện Người dùng:** Xây dựng giao diện Web (dùng Go standard library hoặc framework như Gin, Echo) hoặc giao diện Desktop (dùng Fyne, Gio) để hiển thị dữ liệu trực quan hơn.
* **Xử lý lỗi nâng cao:** Thêm cơ chế retry thông minh hơn, cảnh báo chi tiết hơn.
* **Hỗ trợ nhiều thiết bị:** Mở rộng để đọc từ nhiều Slave ID hoặc nhiều cổng COM khác nhau đồng thời (sử dụng goroutine).

Hy vọng tài liệu này sẽ giúp bạn hiểu rõ hơn về chương trình!
