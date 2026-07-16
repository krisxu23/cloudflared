//go:build cgo
// +build cgo

package main

/*
#include <stdlib.h>
#include <string.h>

// 日志回调函数类型
typedef void (*LogCallback)(const char* message);

// 全局日志回调（使用 weak 避免多编译单元冲突）
__attribute__((weak)) LogCallback g_log_callback = NULL;

// 设置日志回调
__attribute__((weak)) void SetLogCallback(LogCallback cb) {
    g_log_callback = cb;
}

// 发送日志到回调
__attribute__((weak)) void SendLog(const char* msg) {
    if (g_log_callback != NULL) {
        g_log_callback(msg);
    }
}
*/
import "C"

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/cloudflare/cloudflared/cmd/cloudflared/cliutil"
	"github.com/cloudflare/cloudflared/cmd/cloudflared/tunnel"
	"github.com/cloudflare/cloudflared/connection"
	"github.com/rs/zerolog"
	"github.com/urfave/cli/v2"
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

	// 用 channel 传递启动结果
	// runCloudflared 成功时阻塞在 select 上（不写 channel），失败时写入 error 并 return
	// StartCloudflared 等待最多 15 秒：收到 error 说明启动失败，超时说明正在后台运行
	errCh := make(chan error, 1)
	go runCloudflared(p.Args, errCh)

	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()

	select {
	case err := <-errCh:
		// runCloudflared 在启动阶段就返回了，说明失败
		running = false
		if cancel != nil {
			cancel()
		}
		if err != nil {
			C.SendLog(C.CString(fmt.Sprintf("cloudflared startup failed: %v", err)))
		} else {
			C.SendLog(C.CString("cloudflared exited unexpectedly during startup"))
		}
		return -1
	case <-timer.C:
		// 15 秒内未返回，说明 cloudflared 正在后台运行
		running = true
		return 0
	}
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

func runCloudflared(args []string, errCh chan<- error) {
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
	var protocol string = "http2"
	var edgeIPVersion string = "auto"
	var logFilePath string

	for i, arg := range args {
		if arg == "--url" && i+1 < len(args) {
			isTempTunnel = true
			tunnelURL = args[i+1]
		}
		if arg == "--token" && i+1 < len(args) {
			token = args[i+1]
		}
		if arg == "--protocol" && i+1 < len(args) {
			protocol = args[i+1]
		}
		if arg == "--edge-ip-version" && i+1 < len(args) {
			edgeIPVersion = args[i+1]
		}
		if arg == "--logfile" && i+1 < len(args) {
			logFilePath = args[i+1]
		}
	}

	// 创建 CLI 上下文（需要非 nil FlagSet，否则 Set() 会 panic）
	app := createApp()
	flagSet := flag.NewFlagSet("cloudflared", flag.ContinueOnError)
	for _, f := range app.Flags {
		f.Apply(flagSet)
	}
	cliCtx := cli.NewContext(app, flagSet, nil)
	
	// 设置参数
	if isTempTunnel {
		cliCtx.Set("url", tunnelURL)
	} else if token != "" {
		cliCtx.Set("token", token)
	}
	cliCtx.Set("protocol", protocol)
	cliCtx.Set("edge-ip-version", edgeIPVersion)
	cliCtx.Set("no-autoupdate", "true")

	// 创建构建信息
	buildInfo := cliutil.GetBuildInfo("cgo", "2025.8.1")

	// 初始化 tunnel 包全局变量
	// 正常二进制模式下 main() 会调用 tunnel.Init()，但 cgo 库模式下 main() 不会执行，
	// 导致 buildInfo 和 graceShutdownC 全局变量为 nil，引发空指针 panic（cmd.go:488）
	graceShutdownC := make(chan struct{})
	tunnel.Init(buildInfo, graceShutdownC)

	// 创建日志记录器
	var log *zerolog.Logger
	if logWriter != nil {
		log = logWriter
	} else if logFilePath != "" {
		f, fErr := os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if fErr == nil {
			output := zerolog.ConsoleWriter{Out: f, TimeFormat: time.RFC3339}
			l := zerolog.New(output).With().Timestamp().Logger()
			log = &l
		}
	}
	if log == nil {
		output := zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339}
		l := zerolog.New(output).With().Timestamp().Logger()
		log = &l
	}

	// 创建隧道属性
	var namedTunnel *connection.TunnelProperties
	if token != "" {
		// Cloudflare token 是 base64 编码的 JSON，先解码再解析
		decoded, decErr := base64.StdEncoding.DecodeString(token)
		if decErr != nil {
			decoded, decErr = base64.URLEncoding.DecodeString(token)
		}
		if decErr != nil {
			errMsg := fmt.Sprintf("Failed to decode tunnel token: %v", decErr)
			C.SendLog(C.CString(errMsg))
			if logWriter != nil {
				logWriter.Error().Msg(errMsg)
			}
			return
		}
		tunnelToken := connection.TunnelToken{}
		if err := json.Unmarshal(decoded, &tunnelToken); err != nil {
			errMsg := fmt.Sprintf("Failed to parse tunnel token: %v", err)
			C.SendLog(C.CString(errMsg))
			if logWriter != nil {
				logWriter.Error().Msg(errMsg)
			}
			return
		}
		namedTunnel = &connection.TunnelProperties{
			Credentials: tunnelToken.Credentials(),
		}
	} else {
		namedTunnel = &connection.TunnelProperties{}
	}

	// 启动服务器
	err := tunnel.StartServer(cliCtx, buildInfo, namedTunnel, log)
	if err != nil {
		errMsg := fmt.Sprintf("Failed to start cloudflared: %v", err)
		C.SendLog(C.CString(errMsg))
		if logWriter != nil {
			logWriter.Error().Msg(errMsg)
		}
		errCh <- err
		return
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

func createApp() *cli.App {
	app := cli.NewApp()
	app.Name = "cloudflared"
	app.Usage = "Cloudflare Tunnel"
	app.Flags = []cli.Flag{
		&cli.StringFlag{
			Name:  "url",
			Usage: "Origin URL",
		},
		&cli.StringFlag{
			Name:  "token",
			Usage: "Tunnel token",
		},
		&cli.StringFlag{
			Name:  "protocol",
			Usage: "Protocol",
			Value: "http2",
		},
		&cli.StringFlag{
			Name:  "edge-ip-version",
			Usage: "Edge IP version",
			Value: "auto",
		},
		&cli.BoolFlag{
			Name:  "no-autoupdate",
			Usage: "Disable auto update",
		},
	}
	return app
}
