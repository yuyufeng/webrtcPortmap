#!/usr/bin/env bash
# 卸载 signaling 服务。默认保留配置与数据；加 --purge 一并删除。
# 用法： sudo ./uninstall.sh [--purge]
set -euo pipefail
if [[ "$(id -u)" -ne 0 ]]; then echo "[ERROR] 请用 root 运行" >&2; exit 1; fi

echo "[*] 停止并禁用服务..."
systemctl disable --now signaling.service 2>/dev/null || true
rm -f /etc/systemd/system/signaling.service
systemctl daemon-reload
rm -f /usr/local/bin/signaling

if [[ "${1:-}" == "--purge" ]]; then
  echo "[*] --purge：删除配置、数据与系统用户..."
  rm -rf /etc/webrtc-portmap /var/lib/webrtc-portmap
  userdel webrtc-portmap 2>/dev/null || true
  groupdel webrtc-portmap 2>/dev/null || true
  echo "[OK] 已彻底卸载。"
else
  echo "[OK] 已卸载服务（保留 /etc/webrtc-portmap 与 /var/lib/webrtc-portmap）。"
  echo "    彻底清除： sudo ./uninstall.sh --purge"
fi
