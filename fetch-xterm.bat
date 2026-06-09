@echo off
chcp 65001 >nul
REM 下载 xterm.js 前端资源到 web\static\vendor\，供 go:embed 嵌入信令服务器二进制。
REM 运行时即可完全离线（不依赖 CDN）。构建脚本会在编译前自动调用本脚本。

setlocal
set VENDOR_DIR=%~dp0cmd\signaling\web\static\vendor
set XTERM=https://cdn.jsdelivr.net/npm/xterm@5.3.0
set FIT=https://cdn.jsdelivr.net/npm/xterm-addon-fit@0.8.0

if not exist "%VENDOR_DIR%" mkdir "%VENDOR_DIR%"

echo Vendoring xterm.js into %VENDOR_DIR% ...
powershell -NoProfile -Command "$ErrorActionPreference='Stop'; $d='%VENDOR_DIR%'; Invoke-WebRequest '%XTERM%/lib/xterm.min.js' -OutFile (Join-Path $d 'xterm.min.js'); Invoke-WebRequest '%XTERM%/css/xterm.min.css' -OutFile (Join-Path $d 'xterm.min.css'); Invoke-WebRequest '%FIT%/lib/xterm-addon-fit.min.js' -OutFile (Join-Path $d 'xterm-addon-fit.min.js')"
if %errorlevel% neq 0 (
    echo [ERROR] Failed to vendor xterm assets
    exit /b 1
)
echo [OK] xterm assets vendored.
