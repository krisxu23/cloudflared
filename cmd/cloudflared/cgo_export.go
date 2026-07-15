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
	"encoding/json"
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
	var protocol string = "http2"
	var edgeIPVersion string = "auto"

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
	}

	// 创建 CLI 上下文
	app := createApp()
	cliCtx := cli.NewContext(app, nil, nil)
	
	// 设置参数
	if isTempTunnel {
		cliCtx.Set("url", tunnelURL)
	} else if token != "" {
		cliCtx.Set(tunnel.TunnelTokenFlag, token)
	}
	cliCtx.Set("protocol", protocol)
	cliCtx.Set("edge-ip-version", edgeIPVersion)
	cliCtx.Set("no-autoupdate", "true")

	// 创建构建信息
	buildInfo := cliutil.GetBuildInfo("cgo", "2025.8.1")

	// 创建日志记录器
	log := logWriter
	if log == nil {
		output := zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339}
		l := zerolog.New(output).With().Timestamp().Logger()
		log = &l
	}

	// 创建隧道属性
	var namedTunnel *connection.TunnelProperties
	if token != "" {
		// 解析 token 为 credentials
		tunnelToken := connection.TunnelToken{}
		if err := json.Unmarshal([]byte(token), &tunnelToken); err != nil {
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
