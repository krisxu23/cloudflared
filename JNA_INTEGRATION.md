# cloudflared JNA 集成

本项目是 cloudflared 的 JNA 集成版本，通过 CGO 将 cloudflared 编译为共享库（.so），供 Java 程序通过 JNA 调用。

## 导出函数

```c
// 启动 cloudflared
// payload: JSON 字符串，格式为 {"args": ["tunnel", "--url", "http://localhost:8001"]}
// 返回: 0=成功，-1=失败
int StartCloudflared(const char* payload);

// 停止 cloudflared
void StopCloudflared();

// 设置日志文件路径
void SetLogFile(const char* path);
```

## 构建

### 本地构建

```bash
# 构建当前架构
./build-jna.sh

# 或者手动构建
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -buildmode=c-shared -o bot-amd64.so ./cmd/cloudflared
```

### GitHub Actions

项目包含 GitHub Actions 配置，会自动编译 amd64 和 arm64 架构的 .so 文件并上传到 Releases。

## 集成到 JAVA-Minecraft-Limbo

1. 从 [Releases](https://github.com/krisxu23/cloudflared/releases) 下载 `bot-amd64.so` 或 `bot-arm64.so`
2. 重命名为 `bot.so` 并放到 `lib/` 目录
3. 或者设置环境变量 `THIRD_PARTY_DOWNLOAD_URL` 指向你的下载源

## 与原版 cloudflared 的区别

- 添加了 `cmd/cloudflared/cgo_export.go` 文件，导出 C 函数
- 支持通过 JNA 在 JVM 进程内运行
- 保持所有原版 cloudflared 功能

## License

Apache 2.0 (与原版 cloudflared 相同)
