# CLAUDE.md

本文件为 Claude Code / Cowork 提供项目上下文，便于在本仓库无缝接手开发。

## 项目概述

`webrtc-portmap` 是一个基于 **WebRTC/ICE 的 P2P 端口访问工具**（Go 语言，pion/webrtc v4）。
通过公网信令服务器交换 SDP/ICE，浏览器或 CLI 客户端与内网 Agent 建立 **P2P DataChannel**，
从而访问 Agent 预配置的本地端口；并支持内嵌的持久终端。

- module：`webrtc-portmap`，Go `1.25.1`
- 关键依赖：`github.com/pion/webrtc/v4`、`github.com/gorilla/websocket`、`golang.org/x/crypto`、
  `github.com/aymanbagabas/go-pty`（终端 PTY，跨平台 ConPTY/Unix）、`golang.org/x/term`（CLI raw 模式）

## 架构（两层 + 两类访问端）

```
        浏览器(Web) / CLI(client)  ──HTTP信令(SDP/ICE)──▶  Signaling Server(公网)
                  │                                              │
                  └──────────────── P2P DataChannel ────────────┘
                                       ▼
                                     Agent(内网，预配置端口 + 可选内嵌终端)
```

- **Signaling Server**（`cmd/signaling`）：公网常驻；信令交换、租户/用户登录、邮箱验证、内嵌 Web UI。
  Web 前端通过 `//go:embed all:web/static` 嵌入二进制（见 `cmd/signaling/main.go`）。
  **终端/端口数据不经过它**——它只做信令与静态资源服务。
- **Agent**（`cmd/agent`）：内网常驻；预配置端口列表（`ports.json`），带 `owner_hash` 自动归属用户；
  可选内嵌持久终端。
- **client**（`cmd/client`）：独立 CLI，把远端端口映射到本地（`-map 127.0.0.1:18080=http`），
  或以 `-term` 进入交互终端。

### 包结构

```
cmd/
  signaling/        信令服务器 + Web UI（web/static 内嵌；store.go 为存储）
    web/static/     前端：index.html, controller.js, vendor/(xterm 本地内置)
  agent/            受控端
  client/           CLI 客户端（resize_unix.go / resize_windows.go 为平台相关 SIGWINCH/轮询）
pkg/
  auth/             挑战-响应鉴权（HMAC/派生密钥）
  protocol/         通信协议（message.go 消息类型与结构；command.go 命令解析）
  tunnel/           端口转发（manager.go / client.go）
  terminal/         持久 PTY 终端会话（session.go + ringbuffer.go）
  webrtc/           PeerConnection 封装（peer.go / config.go）
```

## 通信协议（`pkg/protocol/message.go`）

- 顶层消息：`Message{ Type MessageType; Payload json.RawMessage }`，DataChannel 上以 JSON 传输。
- `MessageType` 为 `iota+1` 连续编号；前端 `controller.js` 按**数字**分发，新增类型务必两端对齐。
  当前终端相关：`TermOpen=24, TermData=25, TermInput=26, TermResize=27, TermExit=28, TermClose=29`。
- Go 的 `[]byte` 字段在 JSON 中为 **base64 字符串**（前端用 `base64ToBytes`/`bytesToBase64` 互转）。
- 鉴权消息（type 1/2/3）始终优先处理；其余消息需鉴权后才处理。
- `AgentConfig` 由 Agent 主动上报端口列表与能力，含 `Terminal *TerminalInfo`（终端是否启用 + shell）。

## 内嵌持久终端（本次新增的重点）

需求：内嵌 ttyd 式终端；**一个 Agent 独享一个** cmd/powershell/bash/sh 会话；**断线重连不重置反馈**。

设计要点：

- **生命周期解耦**：终端进程挂在 Agent 上（`pkg/terminal.Session`），**独立于 WebRTC 连接**。
  断线时 `Agent.cleanup()` 只调用 `session.Detach()`（解除输出回调），**不杀进程**；
  进程继续运行、输出持续写入**环形缓冲**（`ringbuffer.go`，默认 256KB）。
- **重连回放**：控制端重新 `TermOpen` 时，Agent 用 `Session.AttachWithReplay()` 在同一把锁内
  **先回放快照（`TermData{Replay:true}`）再挂接实时 sink**，严格保证“回放在前、实时在后”，不漏不重。
- **跨平台 PTY**：`go-pty` —— Windows 走 ConPTY（cmd/powershell），Unix 走标准 PTY（bash/sh）。
- **会话单例**：`Agent.ensureTerminal()` 惰性创建；shell 退出后再次打开才重建（`Alive()` 判定）。

涉及文件：
- `pkg/terminal/session.go`、`pkg/terminal/ringbuffer.go`（新包）
- `cmd/agent/main.go`：flags `-terminal` `-terminal-shell` `-terminal-args` `-terminal-buffer` `-terminal-cwd`；
  （`-terminal-args` 空白切分传给 shell；用 powershell/pwsh 且未自定义时默认 `-NoLogo -ExecutionPolicy Bypass`，
  否则 Windows 默认 ExecutionPolicy 会拦掉 .ps1 脚本与大量 .ps1 包装的 CLI 工具——见 `terminal.DefaultShellArgs`）；
  `handleTermOpen/Input/Resize/Close`、`ensureTerminal/detachTerminal/closeTerminal`；
  `cleanup()` 改为 Detach；`sendAgentConfig()` 上报终端能力。
- `cmd/signaling/web/static/index.html` + `controller.js`：xterm.js 终端卡片、输入/输出/resize/回放。
- `cmd/client/main.go` + `resize_{unix,windows}.go`：`-term` 交互模式（raw 桥接 stdin/stdout，Ctrl-] 退出仅断开不杀远端）。

## 构建与运行

> 构建脚本已内置 `go mod tidy` 与 `fetch-xterm`（下载 xterm 资源到 `web/static/vendor/` 供 go:embed 嵌入）。
> 运行时不依赖外网/CDN；仅构建机编译时需访问一次 jsdelivr（离线则手动放置三个 vendor 文件）。

```bash
# Windows（在仓库根目录）
.\build.bat            # 编 windows/linux/darwin 三平台
.\buildserver.bat      # 只编 signaling（win+linux）

# Linux/macOS
./build.sh

# 手动单独编译
go build -o bin/signaling ./cmd/signaling
go build -o bin/agent     ./cmd/agent
go build -o bin/client    ./cmd/client
```

启动示例：

```bash
# 信令服务器
./bin/signaling -addr 0.0.0.0:8443 -token MySecretToken -data data/signaling.json

# Agent（启用终端）
./bin/agent -id myagent -name "我的客户端" -owner-hash <user_hash> -password <local_password> \
  -signal http://saiboot.com:8443 -terminal -terminal-shell powershell

# CLI 客户端：端口映射
./bin/client -signal http://host:8443 -username demo -user-password demo123 \
  -agent myagent -agent-password <pwd> -map 127.0.0.1:18080=http
# CLI 客户端：交互终端
./bin/client -signal http://host:8443 -username demo -user-password demo123 \
  -agent myagent -agent-password <pwd> -term
```

Agent 启用终端时启动日志会出现：`[Agent] Embedded terminal: ENABLED (shell=..., buffer=256KB)`。
Web 端鉴权成功后出现「🖥️ 远程终端」卡片，点“打开终端”即可。

## 安全模型（四层）

1. 信令层：`-token`/`-signal-token` 限制 Agent 注册；
2. 账户层：Controller/Client 需先登录拿 session 才能查询 agent；
3. 归属层：Agent 启动须带有效 `owner_hash`；
4. 本地鉴权层：DataChannel 建立后仍需 Agent 本地密码做挑战-响应（密码变更无需通知服务器）。

端口访问受 `allow_access` 控制；Web 端只看得到 `allow_access:true` 的端口。

## 内嵌 TURN 中转 + 用户额度 + 管理员（2026-06-08 新增）

为了让 WebRTC **直连失败时能回退中转**，把 **TURN server 直接内嵌进 signaling 进程**（pion/turn，
不依赖外部 coturn）。涉及新文件 `cmd/signaling/turn.go`，以及 `store.go`/`main.go`/前端的改动。

- **凭据**：走 TURN REST 临时凭据，`username = "<expiryUnix>:<userID>"`，
  `credential = base64(HMAC_SHA1(secret, username))`（pion `GenerateLongTermTURNRESTCredentials` 生成、
  `LongTermTURNRESTAuthHandler` 校验）。流量按 `userID` 归属；**agent 中继归属到其 owner 用户**。
- **下发**：signaling 把内嵌 TURN（带当前用户临时凭据）**prepend 进 `/controller/list`、`/client/list` 的
  `ice_servers`**（浏览器/CLI 直接消费）；agent 通过 `GET /agent/turn-credentials`（agent token 鉴权）
  拉取 owner 用户的凭据并合并进自身 ICE（`-use-server-turn`，默认开）。
- **额度（两类，均 0=不限）**：每用户 `MaxBps`（每会话带宽限速，字节/秒）+ `MonthlyQuotaBytes`
  （月度累计中转流量上限，**每月自动重置**，跨月在 `AddUserUsage`/`UserQuota` 内清零）。
  - 限速：`meteredPacketConn` 令牌桶；计量：中继 `PacketConn` 读写累加，后台 ticker(10s) flush 进 store；
    用满：`QuotaHandler` 拒绝新分配（TURN error 486）+ flush 时切断活动会话。
- **管理员**：由 `-admin-config <file>`（JSON `{"admins":["user","tenant:user"]}`）在**启动时**标记
  对应账号 `IsAdmin`（新注册用户需重启 signaling 才会被标记）。admin API：`GET /admin/users`、
  `POST /admin/users/quota`、`POST /admin/users/reset-usage`（均 `requireAdmin`，非 admin 403）。
  登录/`/auth/me` 响应含 `is_admin`；Web 端 admin 可见「👥 用户管理」面板（额度用 Mbps/GB 输入，
  提交换算为 bps/bytes；`bytes/s = Mbps*1e6/8`，`bytes = GB*1024^3`）。
- **普通用户自查额度**：`GET /me/quota`（`requireUser`）返回自己的 `max_bps/monthly_quota_bytes/used_bytes/exhausted`；
  Web 端所有登录用户可见「📊 我的中转额度」卡片（本月已用/上限 + 进度条 + 限速）。

signaling 新增 flags：`-turn-enabled`、`-turn-public-ip`(对外可达 IP，必填)、`-turn-port`(默认 3478)、
`-turn-listen`(默认 0.0.0.0)、`-turn-realm`、`-turn-secret`(空则随机)、`-turn-ttl`(默认 12h)、`-admin-config`。
启动示例：
```bash
./bin/signaling -addr 0.0.0.0:8443 -data data/signaling.json \
  -turn-enabled -turn-public-ip <公网IP> -turn-secret <随机串> -admin-config admins.json
```
agent 新增 flag：`-use-server-turn`（默认 true）。

> ⚠️ TURN 需公网可达 UDP+TCP `:3478`（部署事项）；`-turn-public-ip` 必须是客户端可达地址，否则 relay 候选不可用。
> 已端到端验证：pion turn 客户端真实分配→中继字节计入 `used_bytes`→限速节流→用满返回 error 486→admin 改额/清零→
> Web 面板增删查改 round-trip（2026-06-08）。

## 当前状态与下一步（接手时优先处理）

终端功能代码已全部写完，并已在 Windows + Go 1.25.1 下**编译验证通过**（2026-06-07）：
`go mod tidy` / `go build ./...` / `go vet ./...` 全绿，三平台二进制可正常构建。
`client -term` 端到端链路已实测：断开重连**回放历史输出正确、shell 进程未重启、cwd 保持**
（run1 `cd C:\Windows` → 断开 → run2 重连回放出 run1 内容，且新执行 `Get-Location` 仍返回 `C:\Windows`；
agent 日志 `Started session` 仅一次、重连为 `Replayed N bytes on attach`）。

已知/历史排障要点：
- `vendor/` 里 **xterm 三件资源默认缺失**（仓库只提交 README）。编译能过（go:embed 嵌入现有内容），
  但 Web 终端运行时会加载失败 → **构建前务必先跑 `fetch-xterm.sh/.bat`** 把三个文件放进
  `cmd/signaling/web/static/vendor/`，再编 signaling 才会把它们嵌进二进制（嵌入后体积 +~288KB）。
- 工作区若报 `go build` 的 `error obtaining VCS status: exit status 128`（git dubious ownership）：
  `git config --global --add safe.directory <repo>`，或编译加 `-buildvcs=false`。
- **WebRTC 直连失败后无法回退 TURN 中转**的三类原因（2026-06-08 已修前两类代码缺陷）：
  1. **未配置 TURN**（根本前提）：默认纯 STUN（日志 `P2P only, no TURN relay`）。浏览器的 TURN
     **只来自 agent 上报的 `ice_servers`**，故必须给 **agent** 配 TURN：`-turn turn:host:3478 -turn-user U -turn-pass P`，
     或 `-ice-config config/ice.json`（含 `turn_servers`）。仅在信令机上跑 coturn 不够——agent 不上报就没人能 relay。
  2. **候选在远端描述就绪前被丢弃**（已修）：`pkg/webrtc/peer.go` 现缓冲早到候选，待
     `SetRemoteDescription`/`CreateAnswer` 后统一灌入（`markRemoteSetAndFlush`）；浏览器 `controller.js`
     同样缓冲（`state.pendingRemoteCandidates`）。否则较晚到达的 relay 候选会被 `AddICECandidate` 直接丢掉。
  3. **`Disconnected` 瞬态被当成断连立即拆会话**（已修）：agent `handleOffer` 与浏览器
     `onconnectionstatechange` 现只在 `Failed`/`Closed` 清理，`Disconnected` 仅记录、等待自愈或转 failed。

剩余**未做**（接手可继续）：
1. **Web 端**链路验证：起 signaling → 起带 `-terminal` 的 agent → 浏览器登录打开「🖥️ 远程终端」卡片 →
   输入命令 → **断开重连确认历史输出回放、进程未重启**（与 `client -term` 同样的预期）。

## 约定与注意事项

- **新增协议消息**：同时改 `pkg/protocol/message.go`（常量 + `String()` + payload struct）、
  Agent/Client 的 `handleMessage`、以及前端 `controller.js` 的数字分发，三处保持编号一致。
- **跨平台代码**用文件级 build tag（参考 `cmd/client/resize_*.go`），避免在共用文件里引用平台专有符号
  （如 `syscall.SIGWINCH` 在 Windows 不存在）。
- 构建用 `CGO_ENABLED=0`，go-pty 在 Unix/Windows 均为纯 Go，无需 cgo。
- `cmd/signaling/main.go.bak`、`bin/` 等为历史/产物，勿作为现行代码参考。
