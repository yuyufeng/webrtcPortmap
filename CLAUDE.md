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

# Agent（启用终端；-id 可选，缺省由服务器自动生成，名称即身份；
#        不带任何参数启动则进入交互式向导，见下文「Agent 启动向导」）
./bin/agent -name "我的客户端" -owner-hash <user_hash> -password <local_password> \
  -signal http://saiboot.com:8443 -terminal -terminal-shell powershell

# CLI 客户端：端口映射（也可用 -user-hash <hash> 代替 -username/-user-password）
./bin/client -signal http://host:8443 -user-hash <hash> \
  -agent <agent_id> -agent-password <pwd> -map 127.0.0.1:18080=http
# CLI 客户端：交互终端
./bin/client -signal http://host:8443 -user-hash <hash> \
  -agent <agent_id> -agent-password <pwd> -term
# 不带 -agent 时进入交互模式：列出你名下 agent，按编号选、再输入 agent 密码
```

Agent 启用终端时启动日志会出现：`[Agent] Embedded terminal: ENABLED (shell=..., buffer=256KB)`。
Web 端鉴权成功后出现「🖥️ 远程终端」卡片，点“打开终端”即可。

## 安全模型（四层）

1. 信令层：`-token`/`-signal-token` 限制 Agent 注册；
2. 账户层：Controller/Client 需先登录拿 session 才能查询 agent（账户名+密码，**或 `user-hash`**；登录失败按 IP 升级封禁，见 `cmd/signaling/ipguard.go`）；
3. 归属层：Agent 启动须带有效 `owner_hash`；
4. 本地鉴权层：DataChannel 建立后由 **Agent 亲自**对控制端做挑战-响应校验（**agent 出挑战、自己 VerifyResponse**；密码变更无需通知服务器）。详见下文「鉴权方向互换」。

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

## 身份模型与鉴权加固（2026-06-09 大改，四端联动）

把「agent_id 去用户化」+「认证加固」一并落地，涉及 agent / client / signaling / 前端。

### Agent 身份：名称为主、agent_id 自动管理
- **名称（display_name）= 某 owner 下的逻辑身份**：同 owner 同名即同一 agent，重启/重连按名称命中并复用记录与 agent_id（`store.go: UpsertAgentByUserHash` 名称优先匹配 + `findAgentByOwnerNameLocked`）。
- **agent_id = 全局唯一内部句柄**：缺省或与已有记录冲突时自动生成 `agent-xxxxxxxxxx`（`randomAgentID`），不再必填、不在界面显示。
- **agent 采用服务器返回的 agent_id**（`register()` 里 `a.id = result.AgentID`）——鉴权密钥由 `password+id` 两端各自派生，必须一致，否则握手失败。
- `agent -id` 改为可选；Web 卡片/连接弹窗/启动命令都不再露出 agent_id。

### Agent 启动向导（零参数）
- 不带任何参数启动 agent → 交互式向导（`len(os.Args)==1` 触发，`runStartupWizard`）：依次收集 信令地址 / 归属 token(owner-hash) / 名称 / 可选 id / 密码(隐藏 term.ReadPassword) / 终端选项 / 启动方式，并按名称生成 `start-<name>.{bat,sh}` 启动脚本（**含明文密码**，已提示保管）。带任意 flag 仍走原非交互流程。

### Client 第一层身份：user-hash 登录
- 新增 `POST /auth/login-by-hash {user_hash}` → 换 session token（`handleClientLoginByHash` + `store.GetUserByHash`）。
- client 新增 `-user-hash`（与 `-username/-user-password` 二选一，`loginByHash()`）。
- Web 每个 agent 卡片有「📋 命令」按钮，弹窗给出已填好 `-user-hash` / `-agent <id>` 的 client 连接命令（交互 / `-term` / `-map`，Win+Linux 双版本，**密码占位放行尾**）。
- ⚠️ user-hash 现为 client 第一层 **bearer 凭据，敏感度等同账户口令，勿公开**（复制命令里是明文）。

### 登录防爆破 IP 封禁（`cmd/signaling/ipguard.go`）
- 同来源 IP 在 `/auth/login`、`/auth/login-by-hash` 连续失败 **5 次**即封禁，时长升级 **5→10→30 分钟**（封顶），命中返回 `429 + Retry-After`。成功登录清零计数。
- 取 IP 默认只信 `RemoteAddr`（防 X-Forwarded-For 伪造绕过）；**`-trust-proxy-header`** 开启后才采信 XFF/X-Real-IP（仅部署在可信反代后用）。

### 鉴权方向互换：agent 亲自校验密码（治本反爆破）
- 旧握手「控制端出挑战并校验、agent 听结果」三个隐患：agent 不是校验方收不到「密码错误」；对端拿到 agent 的 HMAC 可**离线爆破**；甚至直接发 `AuthResult{success:true}` 即可绕过。
- 现**互换角色**：agent 作为 initiator 出挑战、自己 `VerifyResponse`；控制端/CLI/浏览器作为响应方，收到挑战才用密码算响应、由 **agent** 判定成败。
  - `cmd/agent`：handshaker `initiator=true`，通道就绪即 `CreateChallenge` 下发；收 `AuthResponse`→`HandleResponse` 校验，错误记日志并断开。
  - `cmd/client`：`initiator=false`，收 `AuthChallenge`→响应、收 `AuthResult`→置位。
  - `controller.js`：onopen 不再发挑战；`type1`→`deriveKey` 算响应发 `type2`，`type3`→成功/失败 UI。crypto 两端一致 `portableHash256("resp|"+key+"|"+challenge+"|"+ts)`。
- 已 P2P 端到端验证：正确密码→agent 放行；错误密码→agent 亲自拒绝、对端拿不到 config。
- ⚠️ **动了鉴权协议：agent/client/浏览器前端必须同时升级**，新旧混用会互相干等握手失败。

### 连接抢占（last-connection-wins）
- agent `handleOffer` 改为新连接强制抢占旧会话（原「有活跃会话即拒绝」会导致某用户忘记断开后，换设备/换地点永久连不上）。`cleanup()` 对终端只 Detach 不杀进程，抢占=无缝重连。信令层早有 force 接管（`/controller|client/connect` 的 `force` + Web/CLI 确认弹窗）。

### Web 使用动画指南
- `cmd/signaling/web/static/guide.html`：以「内网跑 AI CLI、人在外面想接回去」痛点切入的单文件动画说明（纯 CSS/SVG，go:embed 嵌入，访问 `/guide.html`，主控制台首页有入口）。

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
- **协议版本（`protocol.ProtocolVersion`）**：凡改动 **DataChannel 应用层鉴权方向或消息语义**，
  必须把它 **+1**，并让 agent / client / 前端 `controller.js`（`PROTOCOL_VERSION`）三端同步同值。
  DataChannel 打开后两端各发一次 `Hello{version}`，不一致会明确提示并断开（见「鉴权方向互换」），
  否则新旧混用会握手失败且原因不明。当前为 **v2**（agent 作为校验方出挑战）。
- **跨平台代码**用文件级 build tag（参考 `cmd/client/resize_*.go`），避免在共用文件里引用平台专有符号
  （如 `syscall.SIGWINCH` 在 Windows 不存在）。
- 构建用 `CGO_ENABLED=0`，go-pty 在 Unix/Windows 均为纯 Go，无需 cgo。
- `cmd/signaling/main.go.bak`、`bin/` 等为历史/产物，勿作为现行代码参考。
