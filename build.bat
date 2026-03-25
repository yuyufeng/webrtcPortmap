@echo off
chcp 65001 >nul
title WebRTC PortMap Build Tool

echo ============================================
echo   WebRTC PortMap Build Tool (New Architecture)
echo ============================================
echo.

set BUILD_DIR=bin
set CGO_ENABLED=0
set GOARCH=amd64

if not exist %BUILD_DIR% mkdir %BUILD_DIR%

echo [1/9] Building signaling server for Windows...
set GOOS=windows
go build -ldflags="-s -w" -o %BUILD_DIR%\signaling.exe .\cmd\signaling
if %errorlevel% neq 0 (
    echo [ERROR] Failed to build signaling server for Windows
    exit /b 1
)
echo [OK] signaling.exe built successfully
echo.

echo [2/9] Building agent for Windows...
go build -ldflags="-s -w" -o %BUILD_DIR%\agent.exe .\cmd\agent
if %errorlevel% neq 0 (
    echo [ERROR] Failed to build agent for Windows
    exit /b 1
)
echo [OK] agent.exe built successfully
echo.

echo [3/9] Building client for Windows...
go build -ldflags="-s -w" -o %BUILD_DIR%\client.exe .\cmd\client
if %errorlevel% neq 0 (
    echo [ERROR] Failed to build client for Windows
    exit /b 1
)
echo [OK] client.exe built successfully
echo.

echo [4/9] Building signaling server for Linux...
set GOOS=linux
go build -ldflags="-s -w" -o %BUILD_DIR%\signaling-linux-amd64 .\cmd\signaling
if %errorlevel% neq 0 (
    echo [ERROR] Failed to build signaling server for Linux
    exit /b 1
)
echo [OK] signaling-linux-amd64 built successfully
echo.

echo [5/9] Building agent for Linux...
go build -ldflags="-s -w" -o %BUILD_DIR%\agent-linux-amd64 .\cmd\agent
if %errorlevel% neq 0 (
    echo [ERROR] Failed to build agent for Linux
    exit /b 1
)
echo [OK] agent-linux-amd64 built successfully
echo.

echo [6/9] Building client for Linux...
go build -ldflags="-s -w" -o %BUILD_DIR%\client-linux-amd64 .\cmd\client
if %errorlevel% neq 0 (
    echo [ERROR] Failed to build client for Linux
    exit /b 1
)
echo [OK] client-linux-amd64 built successfully
echo.

echo [7/9] Building signaling server for macOS...
set GOOS=darwin
go build -ldflags="-s -w" -o %BUILD_DIR%\signaling-darwin-amd64 .\cmd\signaling
if %errorlevel% neq 0 (
    echo [ERROR] Failed to build signaling server for macOS
    exit /b 1
)
echo [OK] signaling-darwin-amd64 built successfully
echo.

echo [8/9] Building agent for macOS...
go build -ldflags="-s -w" -o %BUILD_DIR%\agent-darwin-amd64 .\cmd\agent
if %errorlevel% neq 0 (
    echo [ERROR] Failed to build agent for macOS
    exit /b 1
)
echo [OK] agent-darwin-amd64 built successfully
echo.

echo [9/9] Building client for macOS...
go build -ldflags="-s -w" -o %BUILD_DIR%\client-darwin-amd64 .\cmd\client
if %errorlevel% neq 0 (
    echo [ERROR] Failed to build client for macOS
    exit /b 1
)
echo [OK] client-darwin-amd64 built successfully
echo.

set GOOS=

echo ============================================
echo   Build completed successfully!
echo ============================================
echo.
echo Output files in %BUILD_DIR%\:
dir /b %BUILD_DIR%\
echo.
echo New Architecture:
echo   - Agent pre-configures ports (ssh, http, etc.)
echo   - Web UI connects and authenticates
echo   - After auth, user can access configured ports
echo.
echo Usage:
echo   1. Start signaling:  .\%BUILD_DIR%\signaling.exe -addr 0.0.0.0:8443
echo   2. Start agent:      .\%BUILD_DIR%\agent.exe -id myagent -name "My Agent" -owner-hash ^<user_hash^> -password mypass
echo   3. Start client:     .\%BUILD_DIR%\client.exe -signal http://localhost:8443 -username demo -user-password demo -agent myagent -agent-password mypass -map 127.0.0.1:18080=http
echo   4. Web UI:           http://localhost:8443/
echo.
echo Linux output files:
echo   %BUILD_DIR%\signaling-linux-amd64
echo   %BUILD_DIR%\agent-linux-amd64
echo   %BUILD_DIR%\client-linux-amd64
echo.
echo macOS output files:
echo   %BUILD_DIR%\signaling-darwin-amd64
echo   %BUILD_DIR%\agent-darwin-amd64
echo   %BUILD_DIR%\client-darwin-amd64
echo.
echo Optional agent flags:
echo   -ports config.json  Load port configuration from file
echo.
pause
