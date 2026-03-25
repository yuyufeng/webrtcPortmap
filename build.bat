@echo off
chcp 65001 >nul
title WebRTC PortMap Build Tool

echo ============================================
echo   WebRTC PortMap Build Tool (New Architecture)
echo ============================================
echo.

set BUILD_DIR=bin
set CGO_ENABLED=0

if not exist %BUILD_DIR% mkdir %BUILD_DIR%

echo [1/3] Building signaling server (with Web UI)...
go build -ldflags="-s -w" -o %BUILD_DIR%\signaling.exe .\cmd\signaling
if %errorlevel% neq 0 (
    echo [ERROR] Failed to build signaling server
    exit /b 1
)
echo [OK] signaling.exe built successfully
echo.

echo [2/3] Building agent (with port configuration)...
go build -ldflags="-s -w" -o %BUILD_DIR%\agent.exe .\cmd\agent
if %errorlevel% neq 0 (
    echo [ERROR] Failed to build agent
    exit /b 1
)
echo [OK] agent.exe built successfully
echo.

echo [3/3] Building client (CLI)...
go build -ldflags="-s -w" -o %BUILD_DIR%\client.exe .\cmd\client
if %errorlevel% neq 0 (
    echo [ERROR] Failed to build client
    exit /b 1
)
echo [OK] client.exe built successfully
echo.

echo ============================================
echo   Build completed successfully!
echo ============================================
echo.
echo Output files in %BUILD_DIR%\:
dir /b %BUILD_DIR%\*.exe
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
echo Optional agent flags:
echo   -ports config.json  Load port configuration from file
echo.
pause
