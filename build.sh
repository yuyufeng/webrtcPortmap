#!/bin/bash

# WebRTC PortMap Build Script for Linux/macOS

set -e

BUILD_DIR="bin"
export CGO_ENABLED=0

echo "============================================"
echo "   WebRTC PortMap Build Tool"
echo "============================================"
echo ""

mkdir -p $BUILD_DIR

echo "[1/3] Building signaling server (with Web UI)..."
go build -ldflags="-s -w" -o $BUILD_DIR/signaling ./cmd/signaling
echo "[OK] signaling built successfully"
echo ""

echo "[2/3] Building agent..."
go build -ldflags="-s -w" -o $BUILD_DIR/agent ./cmd/agent
echo "[OK] agent built successfully"
echo ""

echo "[3/3] Building controller (CLI)..."
go build -ldflags="-s -w" -o $BUILD_DIR/controller ./cmd/controller
echo "[OK] controller built successfully"
echo ""

echo "============================================"
echo "   Build completed successfully!"
echo "============================================"
echo ""
echo "Output files in $BUILD_DIR/:"
ls -lh $BUILD_DIR/
echo ""
echo "Usage:"
echo "  1. Start signaling server:  ./$BUILD_DIR/signaling -addr 0.0.0.0:8443"
echo "  2. Start agent:             ./$BUILD_DIR/agent -id myagent -password mypass -signal http://localhost:8443"
echo "  3. Start controller:        ./$BUILD_DIR/controller -signal http://localhost:8443 -agent-id myagent"
echo "  4. Web UI:                  http://localhost:8443/"
