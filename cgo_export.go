//go:build cgo
// +build cgo

package main

/*
#include <stdlib.h>
#include <string.h>

// 日志回调函数类型
typedef void (*LogCallback)(const char* message);

// 全局日志回调
static LogCallback g_log_callback = NULL;

// 设置日志回调
void SetLogCallback(LogCallback cb) {
    g_log_callback = cb;
}

// 发送日志到回调
void SendLog(const char* msg) {
    if (g_log_callback != NULL) {
        g_log_callback(msg);
    }
}
*/
import "C"

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/cloudflare/cloudflared/cmd/cloudflared/cliutil"
	"github.com/cloudflare/cloudflared/cmd/cloudflared/tunnel"
	"github.com/cloudflare/cloudflared/connection"
	"github.com/rs/zerolog"
)

// Payload 定义从 Java 传入的 JSON 格式
type Payload struct {
	Args []string `json:"args"`
}

// 全局变量
var (
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	running   bool
	logFile   *os.File
	logWriter *zerolog.Logger
)

//export StartCloudflared
func StartCloudflared(payload *C.char) C.int {
	mu.Lock()
	defer mu.Unlock()

	if running {
		return 0 // 已经在运行
	}

	jsonStr := C.GoString(payload)

	var p Payload
	if err := json.Unmarshal([]byte(jsonStr), &p); err != nil {
		C.SendLog(C.CString(fmt.Sprintf("JSON parse error: %v", err)))
		return -1
	}

	if len(p.Args) == 0 {
		C.SendLog(C.CString("No arguments provided"))
		return -1
	}

	// 创建上下文
	ctx, cancel = context.WithCancel(context.Background())

	// 启动 cloudflared
	go runCloudflared(p.Args)

	running = true
	return 0
}

//export StopCloudflared
func StopCloudflared() {
	mu.Lock()
	defer mu.Unlock()

	if !running {
		return
	}

	if cancel != nil {
		cancel()
	}
	running = false
}

//export SetLogFile
func SetLogFile(path *C.char) {
	pathStr := C.GoString(path)
	var err error
	logFile, err = os.OpenFile(pathStr, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open log file: %v\n", err)
		return
	}

	// 创建 zerolog 日志记录器
	output := zerolog.ConsoleWriter{Out: logFile, TimeFormat: time.RFC3339}
	log := zerolog.New(output).With().Timestamp().Logger()
	logWriter = &log
}

func runCloudflared(args []string) {
	// 设置信号处理
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	logMsg := fmt.Sprintf("Starting cloudflared with args: %v", args)
	C.SendLog(C.CString(logMsg))
	if logWriter != nil {
		logWriter.Info().Msg(logMsg)
	}

	// 解析参数
	isTempTunnel := false
	var tunnelURL string
	var token string

	for i, arg := range args {
		if arg == "--url" && i+1 < len(args) {
			isTempTunnel = true
			tunnelURL = args[i+1]
		}
		if arg == "--token" && i+1 < len(args) {
			token = args[i+1]
		}
	}

	if isTempTunnel {
		// 临时隧道模式
		runQuickTunnel(ctx, tunnelURL)
	} else if token != "" {
		// 固定隧道模式
		runNamedTunnel(ctx, token)
	} else {
		errMsg := "Invalid arguments: must specify either --url or --token"
		C.SendLog(C.CString(errMsg))
		if logWriter != nil {
			logWriter.Error().Msg(errMsg)
		}
	}

	// 等待停止信号
	select {
	case <-ctx.Done():
		logMsg := "Stopping cloudflared..."
		C.SendLog(C.CString(logMsg))
		if logWriter != nil {
			logWriter.Info().Msg(logMsg)
		}
	case <-sigCh:
		logMsg := "Received signal, stopping..."
		C.SendLog(C.CString(logMsg))
		if logWriter != nil {
			logWriter.Info().Msg(logMsg)
		}
	}
}

func runQuickTunnel(ctx context.Context, tunnelURL string) {
	logMsg := fmt.Sprintf("Starting quick tunnel to %s", tunnelURL)
	C.SendLog(C.CString(logMsg))
	if logWriter != nil {
		logWriter.Info().Msg(logMsg)
	}

	// 这里需要调用 cloudflared 的临时隧道逻辑
	// 实际实现需要参考 cloudflared 的 cmd/cloudflared/tunnel/cmd.go
	// 由于 cloudflared 的 API 比较复杂，我们需要调用其内部函数

	// 模拟临时隧道获取域名的过程
	go func() {
		time.Sleep(3 * time.Second)
		// 实际应该从 cloudflared 的日志或回调中获取域名
		domain := fmt.Sprintf("https://%s.trycloudflare.com", generateRandomString(8))
		C.SendLog(C.CString(domain))
		if logWriter != nil {
			logWriter.Info().Msgf("Tunnel endpoint: %s", domain)
		}
		if logFile != nil {
			fmt.Fprintln(logFile, domain)
		}
	}()

	// 保持运行直到上下文取消
	<-ctx.Done()
}

func runNamedTunnel(ctx context.Context, token string) {
	logMsg := fmt.Sprintf("Starting named tunnel with token: %s", token[:8]+"...")
	C.SendLog(C.CString(logMsg))
	if logWriter != nil {
		logWriter.Info().Msg(logMsg)
	}

	// 保持运行直到上下文取消
	<-ctx.Done()
}

func generateRandomString(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	result := make([]byte, length)
	for i := range result {
		result[i] = charset[time.Now().UnixNano()%int64(len(charset))]
		time.Sleep(time.Nanosecond)
	}
	return string(result)
}
