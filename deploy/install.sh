#!/usr/bin/env bash
# WebRTC PortMap Signaling —— Linux systemd 一键安装/升级脚本
#
# 用法（在解压目录、与脚本同级放好 signaling-linux-amd64 后，root 运行）：
#   sudo ./install.sh [binary_path]
# 例：
#   sudo ./install.sh ./signaling-linux-amd64
#
# 幂等：可重复运行；已存在的配置文件不会被覆盖（只装示例的副本）。
set -euo pipefail

SVC_USER="webrtc-portmap"
SVC_GROUP="webrtc-portmap"
BIN_DST="/usr/local/bin/signaling"
CONF_DIR="/etc/webrtc-portmap"
DATA_DIR="/var/lib/webrtc-portmap"
UNIT_DST="/etc/systemd/system/signaling.service"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ---- 找到二进制 ----
BIN_SRC="${1:-}"
if [[ -z "$BIN_SRC" ]]; then
  for cand in "$SCRIPT_DIR/signaling-linux-amd64" "$SCRIPT_DIR/../bin/signaling-linux-amd64" "$SCRIPT_DIR/signaling"; do
    [[ -f "$cand" ]] && BIN_SRC="$cand" && break
  done
fi
if [[ -z "$BIN_SRC" || ! -f "$BIN_SRC" ]]; then
  echo "[ERROR] 找不到 signaling 二进制。请把 signaling-linux-amd64 放到脚本同目录，或显式传路径：" >&2
  echo "        sudo ./install.sh /path/to/signaling-linux-amd64" >&2
  exit 1
fi

if [[ "$(id -u)" -ne 0 ]]; then
  echo "[ERROR] 请用 root 运行（sudo ./install.sh）" >&2
  exit 1
fi

echo "[1/6] 创建系统用户与目录..."
if ! getent group "$SVC_GROUP" >/dev/null; then groupadd --system "$SVC_GROUP"; fi
if ! id -u "$SVC_USER" >/dev/null 2>&1; then
  useradd --system --gid "$SVC_GROUP" --home-dir "$DATA_DIR" --shell /usr/sbin/nologin "$SVC_USER"
fi
install -d -o "$SVC_USER" -g "$SVC_GROUP" -m 0750 "$DATA_DIR"
install -d -m 0755 "$CONF_DIR"

echo "[2/6] 安装二进制 -> $BIN_DST"
install -m 0755 "$BIN_SRC" "$BIN_DST"

echo "[3/6] 安装配置（已存在则不覆盖）..."
if [[ ! -f "$CONF_DIR/signaling.env" ]]; then
  install -m 0640 -g "$SVC_GROUP" "$SCRIPT_DIR/signaling.env.example" "$CONF_DIR/signaling.env"
  echo "       已写入 $CONF_DIR/signaling.env —— 请编辑 -turn-public-ip 与 -turn-secret！"
else
  echo "       保留已有 $CONF_DIR/signaling.env"
fi
if [[ ! -f "$CONF_DIR/admins.json" ]]; then
  install -m 0644 "$SCRIPT_DIR/admins.json.example" "$CONF_DIR/admins.json"
  echo "       已写入 $CONF_DIR/admins.json —— 请填入管理员用户名"
else
  echo "       保留已有 $CONF_DIR/admins.json"
fi

echo "[4/6] 安装 systemd 单元 -> $UNIT_DST"
install -m 0644 "$SCRIPT_DIR/signaling.service" "$UNIT_DST"

echo "[5/6] 重载 systemd 并设为开机自启..."
systemctl daemon-reload
systemctl enable signaling.service >/dev/null

echo "[6/6] 启动/重启服务..."
systemctl restart signaling.service
sleep 1
systemctl --no-pager --full status signaling.service || true

cat <<EOF

============================================================
 安装完成。
------------------------------------------------------------
 二进制 : $BIN_DST
 配置   : $CONF_DIR/signaling.env   (改完跑 systemctl restart signaling)
          $CONF_DIR/admins.json
 数据   : $DATA_DIR/signaling.json  (用户/agent/额度持久化，勿删)
------------------------------------------------------------
 常用命令：
   systemctl status signaling
   systemctl restart signaling
   journalctl -u signaling -f          # 实时日志
------------------------------------------------------------
 防火墙放行（按你的工具）：
   TCP 8443        # 信令 + Web 控制台
   UDP 3478        # TURN 中继（必须，UDP）
   TCP 3478        # TURN over TCP（建议）
============================================================
EOF
