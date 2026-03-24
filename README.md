# WebRTC/ICE 端口访问工具

一个基于WebRTC/ICE的P2P端口访问工具，Agent预配置端口，Web端鉴权后直接访问。

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
2. **Web** 端通过信令服务器查看在线Agent列表
3. 选择Agent，输入密码进行鉴权
4. 鉴权通过后，Web端显示Agent开放的端口列表
5. 点击端口即可建立P2P连接并访问

**部署角色说明：**
| 角色 | 部署位置 | 运行方式 | 功能 |
|------|----------|----------|------|
| **Signaling** | 有公网IP的服务器 | 常驻服务 | 信令服务 + Web UI |
| **Agent** | 被访问的内网机器 | 常驻服务 | 预配置端口，等待Web端访问 |

## 特性

- **Agent预配置**: Agent启动时配置允许访问的端口列表
- **P2P直连**: Web浏览器直接与Agent建立WebRTC P2P连接
- **NAT穿透**: 支持STUN/TURN服务器，可穿透大多数NAT
- **加密鉴权**: 基于密码的挑战-响应鉴权机制
- **纯Web访问**: 无需安装客户端，浏览器直接访问
- **端口级控制**: Agent可精确控制每个端口的访问权限

## 快速开始

### 1. 构建

只需要编译**两个程序**：

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
```

### 2. 启动信令服务器

```bash
./bin/signaling -addr 0.0.0.0:8443 -token MySecretToken
```

访问 `http://localhost:8443/` 查看Web界面。

### 3. 启动Agent（使用默认端口配置）

```bash
./bin/agent -id myserver -password mysecret -signal http://signaling.example.com:8443 -signal-token MySecretToken
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
./bin/agent -id myserver -password mysecret -signal http://signaling.example.com:8443 -ports ports.json
```

### 5. Web端访问

1. 打开浏览器访问 `http://signaling.example.com:8443/`
2. 配置信令服务器URL，点击"查看在线Agent"
3. 选择要访问的Agent
4. 输入Agent密码进行鉴权
5. 鉴权通过后，点击端口进行访问

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

1. **信令层**: 可选的 `-token` 参数防止未授权访问信令服务
2. **Agent注册**: Agent ID唯一，需要正确的密码才能注册/更新
3. **Web访问**: 每次连接需要输入Agent密码进行鉴权

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
│   └── agent/              # 受控端（预配置端口）
├── pkg/
│   ├── auth/               # 加密鉴权
│   ├── protocol/           # 通信协议
│   ├── tunnel/             # 端口转发
│   └── webrtc/             # WebRTC封装
├── config/
│   └── ports.json          # 端口配置示例
├── bin/
│   ├── signaling           # 信令服务器
│   └── agent               # 受控端
├── build.bat               # Windows构建脚本
├── build.sh                # Linux/macOS构建脚本
└── README.md
```

## 使用流程示例

```bash
# 1. 启动信令服务器
./signaling -addr 0.0.0.0:8443 -token MySecretToken

# 2. 在服务器A启动Agent（开放SSH和HTTP）
./agent -id server-a -password secret123 \
    -signal http://signaling.example.com:8443 \
    -signal-token MySecretToken

# 3. 在服务器B启动Agent（开放MySQL和Redis）
./agent -id server-b -password secret456 \
    -signal http://signaling.example.com:8443 \
    -signal-token MySecretToken \
    -ports custom-ports.json

# 4. 浏览器访问 http://signaling.example.com:8443/
#    - 查看在线Agent列表（server-a, server-b）
#    - 选择 server-a，输入密码 secret123
#    - 鉴权成功后显示：SSH(22), HTTP(80)
#    - 点击 SSH 端口进行连接
```

## 局限性与未来改进

1. **TCP转发**: 当前主要实现TCP端口访问
2. **Web Console**: 完善HTTP请求的Web Console功能
3. **流量统计**: 添加端口访问日志和统计
4. **多用户**: 支持不同用户访问不同端口

## License

MIT
