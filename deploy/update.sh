#!/usr/bin/env bash
# 升级 signaling 二进制并重启（不动配置与数据）。
# 用法： sudo ./update.sh [binary_path]
set -euo pipefail

BIN_DST="/usr/local/bin/signaling"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

BIN_SRC="${1:-}"
if [[ -z "$BIN_SRC" ]]; then
  for cand in "$SCRIPT_DIR/signaling-linux-amd64" "$SCRIPT_DIR/../bin/signaling-linux-amd64"; do
    [[ -f "$cand" ]] && BIN_SRC="$cand" && break
  done
fi
if [[ -z "$BIN_SRC" || ! -f "$BIN_SRC" ]]; then
  echo "[ERROR] 找不到新二进制；传路径：sudo ./update.sh /path/to/signaling-linux-amd64" >&2
  exit 1
fi
if [[ "$(id -u)" -ne 0 ]]; then echo "[ERROR] 请用 root 运行" >&2; exit 1; fi

echo "[*] 安装新二进制 -> $BIN_DST"
install -m 0755 "$BIN_SRC" "$BIN_DST"
echo "[*] 重启服务..."
systemctl restart signaling.service
sleep 1
systemctl --no-pager status signaling.service | head -n 6
echo "[OK] 已升级。journalctl -u signaling -f 查看日志。"
