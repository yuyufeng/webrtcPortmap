# Linux 部署（systemd）

把 **signaling 信令服务**（含内嵌 TURN 中转 + Web 控制台）作为 systemd 服务常驻运行。

## 目录文件

| 文件 | 作用 |
|---|---|
| `signaling.service` | systemd 单元 |
| `signaling.env.example` | 启动参数模板（装机时复制为 `/etc/webrtc-portmap/signaling.env`）|
| `admins.json.example` | 管理员名单模板 |
| `install.sh` | 一键安装（建用户/目录、装二进制+配置+单元、开机自启并启动）|
| `update.sh` | 升级二进制并重启（不动配置/数据）|
| `uninstall.sh` | 卸载（`--purge` 连配置数据一起删）|

## 安装步骤

1. 在构建机执行 `buildserver.bat`（或 `go build`）得到 **`bin/signaling-linux-amd64`**。
2. 把该二进制和本 `deploy/` 目录一起拷到服务器，例如：
   ```bash
   scp bin/signaling-linux-amd64 deploy/* user@host:/opt/wp-deploy/
   ```
3. 服务器上运行：
   ```bash
   cd /opt/wp-deploy
   chmod +x install.sh update.sh uninstall.sh
   sudo ./install.sh ./signaling-linux-amd64
   ```
4. **编辑配置**（必改 TURN 的公网 IP 与 secret）：
   ```bash
   sudo nano /etc/webrtc-portmap/signaling.env      # -turn-public-ip / -turn-secret
   sudo nano /etc/webrtc-portmap/admins.json         # 管理员用户名
   sudo systemctl restart signaling
   ```

## 路径约定

- 二进制：`/usr/local/bin/signaling`
- 配置：`/etc/webrtc-portmap/{signaling.env,admins.json}`
- 数据：`/var/lib/webrtc-portmap/signaling.json`（**用户/agent/额度持久化，勿删；换二进制不要动它**）
- 运行用户：`webrtc-portmap`（系统账户，无登录）

## 运维

```bash
systemctl status signaling
systemctl restart signaling
journalctl -u signaling -f         # 实时日志
sudo ./update.sh ./signaling-linux-amd64   # 部署新版本
```

## 防火墙（必须放行）

| 端口 | 协议 | 用途 |
|---|---|---|
| 8443 | TCP | 信令 + Web 控制台（按 `-addr` 调整）|
| 3478 | UDP | TURN 中继（**必须**，UDP）|
| 3478 | TCP | TURN over TCP（建议，UDP 被封时回退）|

> `-turn-public-ip` 必须填**客户端可达的公网 IP**，否则 relay 候选不可用、直连失败时无法中转。
> `-turn-secret` 必须固定为一串随机长字符串；留空会每次启动随机生成，导致重启后旧凭据失效。

## 不需要 TURN？

编辑 `signaling.env`，从 `SIGNALING_ARGS` 删掉所有 `-turn-*` 与 `-admin-config` 即可，仅保留信令与 Web。
