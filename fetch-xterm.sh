#!/bin/bash
# 下载 xterm.js 前端资源到 web/static/vendor/，供 go:embed 嵌入信令服务器二进制。
# 运行时即可完全离线（不依赖 CDN）。构建脚本会在编译前自动调用本脚本。

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
VENDOR_DIR="$SCRIPT_DIR/cmd/signaling/web/static/vendor"
XTERM_VER="5.3.0"
FIT_VER="0.8.0"
BASE_XTERM="https://cdn.jsdelivr.net/npm/xterm@${XTERM_VER}"
BASE_FIT="https://cdn.jsdelivr.net/npm/xterm-addon-fit@${FIT_VER}"

mkdir -p "$VENDOR_DIR"

dl() {
    local url="$1" out="$2"
    echo "  fetch $(basename "$out")"
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL "$url" -o "$out"
    elif command -v wget >/dev/null 2>&1; then
        wget -q "$url" -O "$out"
    else
        echo "[ERROR] need curl or wget" >&2
        return 1
    fi
}

echo "Vendoring xterm.js into $VENDOR_DIR ..."
dl "$BASE_XTERM/lib/xterm.min.js"          "$VENDOR_DIR/xterm.min.js"
dl "$BASE_XTERM/css/xterm.min.css"         "$VENDOR_DIR/xterm.min.css"
dl "$BASE_FIT/lib/xterm-addon-fit.min.js"  "$VENDOR_DIR/xterm-addon-fit.min.js"
echo "[OK] xterm assets vendored."
