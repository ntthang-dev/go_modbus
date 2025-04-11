Sơ đồ này mô tả luồng hoạt động chính của chương trình Go đọc dữ liệu Modbus RTU từ thiết bị thực.

```mermaid
graph TD
    A[Bắt đầu: main()] --> B{Thiết lập Logging (logrus, CSV)};
    B --> C(Định nghĩa Thanh ghi: registersToRead);
    C --> D(Khởi tạo Modbus Handler: \\.\COM3, 19200/8N1);
    D --> E{Vòng lặp Chính (while running)};
    E -- Kiểm tra Kết nối --> F{Client đã kết nối?};
    F -- Chưa --> G(Thử handler.Connect());
    G -- Thất bại --> H(Log Lỗi & Đợi 5s);
    H --> E;
    G -- Thành công --> I(Tạo Modbus Client);
    F -- Đã kết nối --> I;
    E -- Chu kỳ đọc --> I;
    I --> J(Gọi readAllRegisters);
    J -- Gửi Yêu cầu Đọc --> K(client.ReadHoldingRegisters);
    K --> L[OS/Serial Port: \\.\COM3];
    L --> M((Thiết bị Modbus Thực));
    M --> L;
    L --> K;
    K -- Nhận Bytes --> J;
    J -- Gọi decodeBytes --> N(Giải mã Dữ liệu: decodeBytes);
    N -- Dữ liệu đã giải mã --> J;
    J -- Trả về Map Dữ liệu --> O{Xử lý trong main};
    O --> P(Hiển thị Console: fmt.Printf);
    O --> Q(Ghi Log: logrus / CSV);
    P --> R(Chờ Chu kỳ tiếp theo: time.Sleep);
    Q --> R;
    R --> E;

    X[Ctrl+C Signal] --> Y(Dừng Vòng lặp: running=false);
    E -- running=false --> Z(Đóng Kết nối & Logs: closeLogs);
    Z --> Z1[Kết thúc];

    C --> J; // Định nghĩa thanh ghi được dùng trong hàm đọc
    style M fill:#ccf,stroke:#333,stroke-width:2px

Giải thích sơ đồ:

Bắt đầu (main): Chương trình khởi chạy.

Thiết lập Logging: Cấu hình logrus và csv writer.

Định nghĩa Thanh ghi: Nạp danh sách registersToRead.

Khởi tạo Modbus Handler: Cấu hình thông số cổng COM, tốc độ baud...

Vòng lặp Chính: Chương trình chạy liên tục cho đến khi nhận tín hiệu dừng.

Kiểm tra/Thử Kết nối: Nếu chưa có kết nối Modbus (client == nil), thử kết nối (handler.Connect()). 

Nếu lỗi thì ghi log, đợi rồi thử lại. 

Nếu thành công thì tạo client.Gọi readAllRegisters: Thực hiện logic đọc toàn bộ thanh ghi đã định nghĩa.

Giao tiếp Modbus: readAllRegisters gọi hàm client.ReadHoldingRegisters của thư viện, thư viện giao tiếp với OS/Cổng Serial, và cuối cùng là Thiết bị Modbus Thực.

Giải mã Dữ liệu: readAllRegisters nhận byte thô về và gọi decodeBytes để chuyển đổi thành các kiểu dữ liệu phù hợp.

Xử lý trong main: Hàm main nhận map dữ liệu đã giải mã.Hiển thị Console: Dùng fmt.Printf để in kết quả ra màn hình.

Ghi Log: Dùng logrus và csvWriter để ghi dữ liệu vào file.

Chờ Chu kỳ tiếp theo: Dừng một khoảng thời gian (time.Sleep) trước khi lặp lại.

Xử lý Dừng (Ctrl+C): Bắt tín hiệu, đặt biến running thành false để thoát vòng lặp.

Đóng Kết nối & Logs: Gọi handler.Close() và closeLogs() trước khi kết thúc.

Kết thúc.