#!/bin/bash
set -e

echo "Building cloudflared as shared library (.so) for JNA..."

# 检测架构
ARCH=$(uname -m)
if [ "$ARCH" = "x86_64" ]; then
    GOARCH="amd64"
elif [ "$ARCH" = "aarch64" ] || [ "$ARCH" = "arm64" ]; then
    GOARCH="arm64"
else
    echo "Unsupported architecture: $ARCH"
    exit 1
fi

echo "Building for $GOARCH..."

# 构建为共享库
CGO_ENABLED=1 GOOS=linux GOARCH=$GOARCH go build -buildmode=c-shared -o bot-$GOARCH.so ./cmd/cloudflared

echo "Build complete!"
echo "Generated: bot-$GOARCH.so"
ls -lh bot-$GOARCH.so

# 生成头文件（可选）
echo "Header file generated: bot-$GOARCH.h"
