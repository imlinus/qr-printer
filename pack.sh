#!/bin/bash

# Target directory
mkdir -p build

echo "Building for Linux..."
go build -o build/qr-printer-linux src/main.go
sudo setcap cap_net_raw,cap_net_admin+eip build/qr-printer-linux

echo "Building for Windows..."
GOOS=windows GOARCH=amd64 go build -ldflags="-s -w -H windowsgui" -o build/qr-printer-windows.exe src/main.go

echo "All builds finished in build/ directory."
echo "- build/qr-printer-linux (Linux with BT capabilities)"
echo "- build/qr-printer-windows.exe (Windows silent GUI version)"
