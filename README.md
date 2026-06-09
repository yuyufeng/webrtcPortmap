# WebRTC/ICE 端口访问工具

一个基于WebRTC/ICE的P2P端口访问工具，支持租户、用户登录、Agent 自动归属，以及 Agent 本地密码鉴权。

当前分成两类访问模型：
- `Web`：浏览器访问，只做 HTTP/HTML/WS 适配与预览
- `Client`：独立 CLI 客户端，只做 TCP 端口映射，不做 HTTP 适配

## 架构设计

采用两层架构：**信令服务器 (Signaling Server) + 受控端 (Agent)**

```
                           ┌─────────────────────┐
                           │   Signaling Server  │
                           │  (信令服务 + Web UI) │
                           │   需要公网IP/域名    │
                           └──────────┬──────────┘
                                      │
              HTTP信令 (SDP/ICE交换)  │
                                      │
           ┌──────────────────────────┼──────────────────────────┐
           │                          │                          │
           ▼                          ▼                          ▼
┌──────────────────┐      ┌──────────────────┐      ┌──────────────────┐
│   Browser (Web)  │◄────►│      Agent       │      │      Agent       │
│   (浏览器访问)    │  P2P  │  (预配置端口列表)  │      │  (预配置端口列表)  │
│                  │       │                  │      │                  │
│ 1. 查看Agent列表 │       │ - SSH: 127.0.0.1:22      │ - HTTP: 127.0.0.1:80
│ 2. 选择Agent     │       │ - HTTP: 127.0.0.1:80     │ - HTTPS: 127.0.0.1:443
│ 3. 输入密码鉴权  │       │ - MySQL: 127.0.0.1:3306  │
│ 4. 访问端口      │       │                  │      │
└──────────────────┘      └──────────────────┘      └──────────────────┘
      笔记本/手机               内网服务器A                内网服务器B
```

**工作流程：**
1. **Agent** 启动时预配置端口列表（SSH、HTTP等）
2. **用户** 在信令服务中注册/登录，固定归属于租户 `convnet`
3. 登录后获取当前用户唯一 `user_hash`
4. Agent 启动时带上 `owner_hash + display_name` 自动归属到当前用户
5. Web 端只能看到当前账户曾经连接过的 Agent，在线/离线按当前状态显示
6. 选择 Agent，输入本地密码进行鉴权
7. 鉴权通过后，Web 端显示 Agent 开放的端口列表并访问

**部署角色说明：**
| 角色 | 部署位置 | 运行方式 | 功能 |
|------|----------|----------|------|
| **Signaling** | 有公网IP的服务器 | 常驻服务 | 信令服务 + Web UI |
| **Agent** | 被访问的内网机器 | 常驻服务 | 预配置端口，等待 Web/CLI 访问 |

## 特性

- **租户与用户体系**: Controller 按租户和账户登录，只能看自己的 Agent
- **自动归属**: Agent 启动时携带 `owner_hash` 自动归属到账户
- **邮箱验证**: 支持验证码发送，可配置是否强制邮箱验证
- **Agent预配置**: Agent启动时配置允许访问的端口列表
- **P2P直连**: Web浏览器直接与Agent建立WebRTC P2P连接
- **NAT穿透**: 支持STUN/TURN服务器，可穿透大多数NAT
- **加密鉴权**: 基于密码的挑战-响应鉴权机制
- **双访问方式**: 支持浏览器访问，也支持独立 CLI 客户端做本地端口映射
- **端口级控制**: Agent可精确控制每个端口的访问权限
- **内嵌持久终端**: Agent 可内嵌 ttyd 式的持久 shell（cmd/powershell/bash/sh），断线重连不重置、自动回放历史输出

## 快速开始

### 1. 构建

通常需要编译三个程序：

```bash
# Windows
.\build.bat

# Linux/macOS
chmod +x build.sh
./build.sh
```

或手动构建：

```bash
# 构建信令服务器（包含Web UI）
go build -o bin/signaling ./cmd/signaling

# 构建Agent
go build -o bin/agent ./cmd/agent

# 构建独立客户端
go build -o bin/client ./cmd/client
```

### 2. 启动信令服务器

```bash
./bin/signaling \
  -addr 0.0.0.0:8443 \
  -token MySecretToken \
  -data data/signaling.json \
  -email-verify-enabled=true \
  -email-verify-required=false
```

访问 `http://localhost:8443/` 查看 Web 界面。首次使用先注册用户，再复制自己的 `user_hash` 给 agent。

### 3. 启动Agent（使用默认端口配置）

```bash
./bin/agent -id myserver -name "My Server" -owner-hash <user_hash> -password mysecret -signal http://signaling.example.com:8443 -signal-token MySecretToken
```

默认开放的端口：
- SSH: 127.0.0.1:22
- HTTP: 127.0.0.1:80
- HTTPS: 127.0.0.1:443

### 4. 使用自定义端口配置

创建 `ports.json`:

```json
{
  "ports": [
    {
      "id": "ssh",
      "name": "SSH Server",
      "local_addr": "127.0.0.1:22",
      "protocol": "tcp",
      "description": "SSH remote access",
      "allow_access": true
    },
    {
      "id": "web",
      "name": "Web Server",
      "local_addr": "127.0.0.1:8080",
      "protocol": "tcp",
      "description": "Internal web app",
      "allow_access": true
    },
    {
      "id": "mysql",
      "name": "MySQL",
      "local_addr": "127.0.0.1:3306",
      "protocol": "tcp",
      "description": "Database (admin only)",
      "allow_access": false
    }
  ]
}
```

启动Agent：

```bash
./bin/agent -id myserver -name "My Server" -owner-hash <user_hash> -password mysecret -signal http://signaling.example.com:8443 -signal-token MySecretToken -ports ports.json
```

### 5. Web端访问

1. 打开浏览器访问 `http://signaling.example.com:8443/`
2. 注册并登录用户
3. 从页面复制当前用户的 `user_hash`
4. 启动 agent，并携带 `-owner-hash <user_hash> -name <昵称>`
5. agent 第一次上线后，会自动出现在“我的 Agent”列表
6. 输入 Agent 本地密码进行鉴权
7. 鉴权通过后点击端口进行访问

### 6. CLI 客户端映射本地端口

如果希望像传统客户端一样把远端服务映射到本地端口，可使用独立 CLI 客户端：

```bash
./bin/client \
  -signal http://signaling.example.com:8443 \
  -username demo \
  -user-password demo123 \
  -agent myserver \
  -agent-password mysecret \
  -map 127.0.0.1:18080=http \
  -map 127.0.0.1:18443=https
```

启动后本地访问：
- `http://127.0.0.1:18080` -> Agent 的 `http`
- `https://127.0.0.1:18443` -> Agent 的 `https`

## 内嵌终端（持久 PTY）

Agent 可内嵌一个**持久的** shell 会话（类似 ttyd），通过 WebRTC DataChannel 提供远程终端。
特点：

- **一个 Agent 独享一个会话**：cmd / powershell / bash / sh 任选其一，整机共用同一个会话。
- **断线不重置反馈**：终端进程的生命周期独立于 WebRTC 连接。连接断开时只解除输出回调，进程继续运行，输出持续写入环形缓冲；重新连接后自动回放历史输出，恢复到断线前的画面。
- **跨平台**：Windows 走 ConPTY（cmd/powershell），Linux/macOS 走标准 PTY（bash/sh）。

### 启动带终端的 Agent

```bash
./bin/agent -id myserver -name "My Server" -owner-hash <user_hash> -password mysecret \
  -signal http://signaling.example.com:8443 -signal-token MySecretToken \
  -terminal -terminal-shell bash
```

终端相关参数：

| 参数 | 说明 | 默认 |
|------|------|------|
| `-terminal` | 启用内嵌终端 | 关闭 |
| `-terminal-shell` | `cmd`/`powershell`/`bash`/`sh` 或完整路径 | 按平台自动选择（Windows=cmd，Unix=$SHELL/bash/sh） |
| `-terminal-buffer` | 回放环形缓冲大小（KB） | 256 |
| `-terminal-cwd` | 终端工作目录 | 当前目录 |

### Web 端使用

鉴权成功后，若 Agent 启用了终端，页面会出现「🖥️ 远程终端」卡片，点击「打开终端」即可（基于 xterm.js）。
断线后重新连接并鉴权，终端会自动重新附着并回放历史输出。

> xterm.js 已**本地内置**：构建脚本会在编译前调用 `fetch-xterm.sh` / `fetch-xterm.bat` 把资源下载到 `cmd/signaling/web/static/vendor/`，再通过 `go:embed` 一并嵌入信令服务器二进制。**运行/部署时不依赖外网或 CDN**，只需构建机在编译时能访问一次 jsdelivr。若构建机也离线，手动放置这三个文件到 vendor 目录即可：`xterm.min.js`、`xterm.min.css`、`xterm-addon-fit.min.js`。

### CLI 端使用

```bash
./bin/client -signal http://signaling.example.com:8443 \
  -username demo -user-password demo123 \
  -agent myserver -agent-password mysecret -term
```

进入后本地控制台进入 raw 模式，与远端 shell 双向交互；按 **Ctrl-]** 退出（仅断开本地连接，远端会话保持运行，下次 `-term` 重连可回放）。

## Agent端口配置

Agent通过JSON文件配置端口：

```json
{
  "ports": [
    {
      "id": "唯一标识",
      "name": "显示名称",
      "local_addr": "本地地址:端口",
      "protocol": "tcp或udp",
      "description": "描述信息",
      "allow_access": true或false
    }
  ]
}
```

**字段说明：**
| 字段 | 说明 | 示例 |
|------|------|------|
| `id` | 端口唯一标识 | `ssh`, `http`, `mysql` |
| `name` | 显示名称 | `SSH Server`, `Web Service` |
| `local_addr` | Agent本地地址 | `127.0.0.1:22`, `192.168.1.100:8080` |
| `protocol` | 协议类型 | `tcp`, `udp` |
| `description` | 描述信息 | `SSH remote access` |
| `allow_access` | 是否允许访问 | `true`, `false` |

## 安全说明

### 三层鉴权

1. **信令层**: 可选的 `-token` 参数用于限制 agent 向信令服务注册
2. **账户层**: Controller/Client 必须先以租户用户登录，拿到 session 后才能查询 agent
3. **归属层**: Agent 启动时必须携带有效的 `owner_hash`，服务器据此自动归属到账户
4. **本地鉴权层**: 每次 WebRTC DataChannel 建立后，仍需输入 Agent 本地密码完成鉴权；本地密码变更无需通知服务器

### 端口访问控制

- Agent完全控制哪些端口可以被访问（`allow_access`）
- Web端只能看到 `allow_access: true` 的端口
- 即使知道Agent ID和密码，也只能访问允许的端口

## 项目结构

```
webrtc-portmap/
├── cmd/
│   ├── signaling/          # 信令服务器（内含Web UI）
│   │   └── web/static/     # 嵌入的Web前端
│   ├── agent/              # 受控端（预配置端口）
│   └── client/             # 独立CLI客户端（本地端口映射）
├── pkg/
│   ├── auth/               # 加密鉴权
│   ├── protocol/           # 通信协议
│   ├── tunnel/             # 端口转发
│   ├── terminal/           # 持久 PTY 终端会话（跨平台 + 环形缓冲回放）
│   └── webrtc/             # WebRTC封装
├── config/
│   └── ports.json          # 端口配置示例
├── bin/
│   ├── signaling           # 信令服务器
│   ├── agent               # 受控端
│   └── client              # 独立CLI客户端
├── build.bat               # Windows构建脚本
├── build.sh                # Linux/macOS构建脚本
└── README.md
```

## 使用流程示例

```bash
# 1. 启动信令服务器
./signaling -addr 0.0.0.0:8443 -token MySecretToken

# 2. 在服务器A启动Agent（开放SSH和HTTP）
./agent -id server-a -name "Server A" -owner-hash <user_hash> -password secret123 \
    -signal http://signaling.example.com:8443 \
    -signal-token MySecretToken

# 3. 在服务器B启动Agent（开放MySQL和Redis）
./agent -id server-b -name "Server B" -owner-hash <user_hash> -password secret456 \
    -signal http://signaling.example.com:8443 \
    -signal-token MySecretToken \
    -ports custom-ports.json

# 4. 使用独立客户端映射本地端口
./client -signal http://signaling.example.com:8443 \
    -username demo \
    -user-password demo123 \
    -agent server-a \
    -agent-password secret123 \
    -map 127.0.0.1:18080=http

# 5. 浏览器访问 http://signaling.example.com:8443/
#    - 查看在线Agent列表（server-a, server-b）
#    - 选择 server-a，输入密码 secret123
#    - 鉴权成功后显示：SSH(22), HTTP(80)
#    - 点击 SSH 端口进行连接
```

## 局限性与未来改进

1. **TCP转发**: 当前主要实现TCP端口访问
2. **Web Console**: 完善HTTP请求的Web Console功能
3. **流量统计**: 添加端口访问日志和统计
4. **多用户共享**: 当前按“当前账户登记的 agent”隔离，尚未做 agent 共享授权

## License

MIT
