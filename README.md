# WakeHub (v0.2.11)

自托管网络唤醒管理平台：Server + 多 Agent 架构，集中管理多网段设备的 Wake-on-LAN
唤醒、在线状态检测、定时唤醒与受控远程关机/睡眠。

开源协议：MIT（见 `LICENSE`）。GitHub：https://github.com/moye-lab/wakehub ｜ Docker Hub：`moyelab/wakehub`（随 `v*` tag 自动构建发布，amd64 + arm64）。

## 架构

```
浏览器 / PWA (Vue 3)
        │  HTTP（局域网内）+ HttpOnly Cookie 会话
        ▼
WakeHub Server (Go, :8080)
  ├─ REST API /api/v1/*        设备、分组、定时任务、审计日志
  ├─ WebSocket /api/v1/ws/agent  Agent 注册、心跳与任务下发
  ├─ Scheduler (robfig/cron)   持久化定时唤醒任务
  └─ SQLite (WAL)              设备、Agent、任务、审计日志
        │  WebSocket
        ▼
WakeHub Agent × N (Go, 各局域网一台)
  ├─ WOL 魔术包 (UDP 广播)
  ├─ 状态检测 (ping / TCP / ARP 缓存)
  ├─ 局域网扫描（TCP 轻触 + ARP 缓存，见“已知限制”）
  └─ 受控关机/睡眠 (HTTP Sleep-on-LAN 端点)
```

关键约束：**WOL 魔术包是二层广播，只在同一广播域内生效，跨网段、跨
VLAN、跨公网都到不了目标网卡。** 因此每个需要唤醒设备的局域网都要部署
一个 Agent；Server 只负责集中管理与下发指令，真正的发包动作由目标设备
所在网段的 Agent 执行。v0.2.0 已退役内置 `local` 节点（Server 不再进程内
直发），单机单网段场景也须跑一个 local agent 容器——标准安装（模式 1）
已包含。

被唤醒设备还需满足：主板 BIOS/UEFI 开启 Wake-on-LAN、网卡驱动开启
“允许此设备唤醒计算机”、关机后网口灯仍亮（待机供电正常）。

**安全模型（先读）：** Server 与浏览器、Agent 之间均为**纯 HTTP 明文**
（无内置 TLS），请只在可信局域网内使用；公网访问请走反向代理或
Cloudflare Tunnel 做 TLS 终止。登录会话是 HttpOnly Cookie（24 小时，
改密后旧会话全部失效）；每个 Agent 有独立 token + 管理员审批才能接
入；睡眠端点无签名认证，仅限可信局域网使用。详见 `docs/security.md`。

## 功能

- 设备管理：按分组管理设备（名称 / MAC / IP / 广播地址 / WOL 端口 /
  检测方式 / 睡眠端点），统一校验、分页列表、MAC 冲突 409（网络内唯一，
  同一 MAC 可在不同网络各建一台）
- 一键唤醒 / 批量唤醒：102 字节标准 WOL 魔术包，重发次数可配；
  严格区分“魔术包已发送（sent）”与“设备已启动（booted）”，后者须回检
- 状态检测：ping / TCP 端口 / ARP 缓存三种方式；检测异常不判离线，
  Agent 容器内 ping 需 `CAP_NET_RAW`（compose 已默认授予 agent）
- 局域网扫描：异步任务（创建 → 轮询进度 → 取消 → 勾选导入），按 Agent
  白名单校验网段，跳过 `.0`/`.255`，结果去重、已存在设备可识别，
  厂商 OUI 识别（27 条常见前缀，未命中显示 `Unknown Vendor`）
- 定时任务：Cron 表达式 + IANA 时区，持久化到 SQLite，重启后自动恢复；
  仅开放 `wake` 动作；每次执行有独立 Run ID 与每设备结果，空目标记
  `skipped` 而非假成功；支持暂停/恢复、单次任务
- Agent 节点：WebSocket 长连接 + 心跳，断线指数退避重连；每 Agent 独立
  token（`wha_`），新 Agent 须管理员审批（自助注册）或预签发后接入；
  吊销一个 Agent 的 token 不影响其他 Agent
- 受控远程关机/睡眠：仅允许 shutdown / sleep / hibernate 三种白名单
  操作，两种适配器：HTTP Sleep-on-LAN 端点（设备上预先部署）或 SSH
  直连（Linux / Windows，密码加密存储、主机密钥 TOFU）；目标须为私有
  IP 或回环地址，禁公网与元数据地址、禁重定向，且须落在执行 Agent 的
  管理网段内（v0.2.9 问题 7）
- 安全：bcrypt 管理员密码、HttpOnly Cookie 会话（可撤销）、登录限流
  （同一 IP + 同一账号 5 分钟失败 8 次后拒绝）、通用 API 限流
  （每会话 600 次/分钟）、占位密钥拒绝启动、全部写操作写审计日志
  （筛选 + 分页，保留 90 天）

## 快速启动（一键安装，推荐）

```bash
git clone https://github.com/moye-lab/wakehub.git
cd wakehub
bash install.sh
```

安装器会先问部署模式（1=本机 docker compose 部署 server+local agent，
2=仅 server，3=仅 agent），再逐项确认端口、管理员密码等配置（已有
`.env` 的值按回车保留），写配置前会打印打码确认页，装完后轮询健康接口
与登录接口确认服务就绪；模式 1 还会自动审批本机 local agent。
重复运行 install.sh 是安全的（配置保留、服务重建）。

要求：已安装 Docker 与 Docker Compose v2；8080 端口空闲（可在安装时
改）。安装器不会自动安装 Docker，缺失时会给出手动安装指引并退出。
请以 docker 组成员身份直接跑，不要 `sudo`（sudo 运行时生成的 `.env`
属主会被改回原用户，但仍建议直接跑）。

管理员密码：安装时自设（至少 8 位）或留空由安装器随机生成（只显示
一次，请立即记下）；**首次登录后请在网页右上角改密**。忘记密码时：
`docker compose exec wakehub-server /app/wakehub-server reset-password`
（交互式重置，不删库）。

## Docker Hub 预构建镜像

镜像随 GitHub `v*` tag 自动构建发布到 Docker Hub（`moyelab/wakehub`，
amd64 + arm64 双架构），无需本地构建：

```bash
docker pull moyelab/wakehub:0.2.11
```

首个镜像随 `v0.2.11` tag 发布后可用；届时 `install.sh` 将默认拉取预构
建镜像。自己构建仍可用 `docker compose up -d --build`（见下）。

## 手动启动（Docker Compose）

```bash
cd wakehub
cp .env.example .env
# 编辑 .env：把 WAKEHUB_ADMIN_PASSWORD / WAKEHUB_SECRET_KEY /
# WAKEHUB_AGENT_ENROLL_TOKEN 改成随机值（占位值会使服务端拒绝启动）
docker compose up -d --build
docker compose logs -f wakehub-server
```

打开 http://服务器IP:8080 ，用安装时设置的账号密码登录。
不要直接 `make docker-up` 做首次安装：它要求 `.env` 已存在（没有就
直接拒绝），请先跑 install.sh 生成配置。

## 本地开发（无 Docker）

```bash
cd wakehub
go mod tidy
go test ./...          # 后端测试（含 -race 建议另跑）
go vet ./...
cd web && npm ci && npm run build && npm run typecheck && npm test
make build-go          # 编译 bin/wakehub-server 与 bin/wakehub-agent
```

直接跑 server 须先给三个密钥类变量设随机值，否则启动拒绝校验会拦住：

```bash
export WAKEHUB_ADMIN_PASSWORD="$(openssl rand -hex 12)"
export WAKEHUB_SECRET_KEY="$(openssl rand -hex 32)"
export WAKEHUB_AGENT_ENROLL_TOKEN="$(openssl rand -hex 16)"
make run-server        # go run ./cmd/server（监听 :8080）
make run-agent         # go run ./cmd/agent（另开终端；Agent 侧另需配 token）
```

## 环境变量

### Server

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `WAKEHUB_LISTEN_ADDR` | `:8080` | Server 监听地址 |
| `WAKEHUB_BIND_ADDRESS` | `0.0.0.0` | 宿主机侧绑定地址（compose 端口映射；仅本机访问时才改 `127.0.0.1`，从另一台机器访问时改回环会断连，v0.2.9 问题 18） |
| `WAKEHUB_SERVER_TASK_TIMEOUT` | `15s` | 服务端等 Agent 任务结果的上限（Go 时长；应比 Agent 执行上限长，v0.2.9 问题 6） |
| `WAKEHUB_STATUS_CHECK_INTERVAL` | `300` | 周期自动检测间隔（秒；0 禁用，1–29 钳制为 30，V-N2） |
| `WAKEHUB_DB_PATH` | `./data/wakehub.db` | SQLite 路径 |
| `WAKEHUB_MIGRATIONS_PATH` | `./migrations/001_init.sql` | 迁移文件所在目录由该路径导出（M5） |
| `WAKEHUB_WEB_DIST` | `./web/dist` | 前端构建产物目录 |
| `WAKEHUB_ADMIN_USER` | `admin` | 初始管理员账号（仅首次建库时生效） |
| `WAKEHUB_ADMIN_PASSWORD` | `wakehub123`（占位，拒绝启动） | 初始管理员密码（仅首次建库时生效） |
| `WAKEHUB_SECRET_KEY` | 示例值（占位，拒绝启动） | 会话签名密钥，生产必须改随机值 |
| `WAKEHUB_AGENT_ENROLL_TOKEN` | 示例值（占位，拒绝启动） | Agent 入网 token |
| `WAKEHUB_AGENT_TOKEN` | — | 服务端 v0.2.0 不再读取（见 Agent 表） |
| `WAKEHUB_WOL_RETRIES` | `3` | 每次唤醒重发魔术包次数 |
| `WAKEHUB_TRUSTED_PROXIES` | 空（不信任） | 逗号分隔 CIDR，仅此时才信任 `X-Forwarded-For` |
| `WAKEHUB_WS_ALLOWED_ORIGINS` | 空 | 逗号分隔的完整 origin 白名单 |
| `WAKEHUB_COOKIE_SECURE` | `0` | 设为 `1` 时强制 Cookie Secure（仅 HTTPS 部署） |

镜像构建参数（Dockerfile `ARG`，`.env` 可覆盖，默认国内源）：
`NPM_REGISTRY`（默认 `https://registry.npmmirror.com`）、
`GOPROXY`（默认 `https://goproxy.cn,direct`）。海外机器或源不可达时
改回官方源（v0.2.9 问题 17）。

### Agent

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `WAKEHUB_SERVER_WS_URL` | `ws://127.0.0.1:8080/api/v1/ws/agent` | Server WebSocket 地址 |
| `WAKEHUB_AGENT_ID` | `agent-home-lan` | 节点唯一 ID（install.sh 会显式配置） |
| `WAKEHUB_AGENT_NAME` | `Home-HostNetwork-Agent` | 节点显示名 |
| `WAKEHUB_AGENT_TOKEN` | 示例值 | Agent 侧 token：首次注册时填入网 token（与服务端 `WAKEHUB_AGENT_ENROLL_TOKEN` 相同的值）；获批后自动改用 token 文件里的 per-agent token；预签发时填 `wha_` token |
| `WAKEHUB_AGENT_TOKEN_FILE` | `./data/agent-token` | 获批后下发的 per-agent token 持久化路径（0600） |
| `WAKEHUB_AGENT_SUBNET` | 无（必填） | 节点负责的网段（须与本机局域网一致；install.sh 自动检测，多网卡时编号选；启动时校验是否在本机网卡上，不符拒绝启动，`WAKEHUB_AGENT_SKIP_SUBNET_CHECK=1` 可跳过，v0.2.9 问题 8/19） |
| `WAKEHUB_AGENT_TASK_TIMEOUT` | `10s` | Agent 执行单个任务的上限（Go 时长；应比服务端等待上限短，v0.2.9 问题 6） |
| `WAKEHUB_AGENT_ALLOW_INSECURE_WS` | `0` | 设为 `1` 才允许明文 `ws://` 连非回环 Server（v0.2.9 问题 3；Server 本身不终止 TLS，`wss://` 须走反代/隧道） |
| `WAKEHUB_AGENT_TLS_SKIP_VERIFY` | `0` | 设为 `1` 时跳过服务端证书验证（仅测试用） |

## API 列表

基础路径 `/api/v1`，除登录、健康检查与 Agent 注册外全部需要登录
（HttpOnly Cookie 会话，不再接受 `Authorization: Bearer`）。全部
50 个端点见 `docs/api.md`；响应统一为 `{code, message, data}` 信封，
列表接口统一为 `{items, total, page, page_size}` 分页信封。

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/v1/auth/login` | 管理员登录，下发 HttpOnly Cookie（不返回 token） |
| POST | `/api/v1/auth/logout` | 退出，撤销当前会话 |
| POST | `/api/v1/auth/change-password` | 改密（旧会话全部失效） |
| GET | `/api/v1/auth/me` | 当前登录信息 |
| GET | `/api/v1/health` | 健康检查（公开）：`{status, version, db, uptime_seconds}` |
| GET | `/api/v1/ws/agent` | Agent WebSocket 注册 |
| GET | `/api/v1/dashboard` | 仪表盘统计（含待审批 Agent 数、即将执行的任务） |
| GET | `/api/v1/devices` | 设备列表（分页 + 筛选） |
| POST | `/api/v1/devices` | 新增设备 |
| GET | `/api/v1/devices/{id}` | 设备详情 |
| PUT | `/api/v1/devices/{id}` | 更新设备（不存在 → 404） |
| DELETE | `/api/v1/devices/{id}` | 删除设备（不存在 → 404） |
| POST | `/api/v1/devices/{id}/wake` | 唤醒单个设备（可选 `verify` 回检） |
| POST | `/api/v1/devices/batch-wake` | 批量唤醒（per-device 结果） |
| POST | `/api/v1/devices/{id}/check` | 状态检测 |
| POST | `/api/v1/devices/{id}/sleep` | 关机/睡眠（仅 shutdown/sleep/hibernate） |
| GET | `/api/v1/groups` | 分组列表（分页信封） |
| POST | `/api/v1/groups` | 新建分组 |
| GET | `/api/v1/networks` | 网络列表 |
| POST | `/api/v1/networks` | 新建网络 |
| GET | `/api/v1/networks/{id}` | 网络详情 |
| PUT | `/api/v1/networks/{id}` | 更新网络 |
| DELETE | `/api/v1/networks/{id}` | 删除网络（默认网络与被设备/Agent 引用时拒绝） |
| GET | `/api/v1/agents/{id}/networks` | 某 Agent 管理的网络 |
| PUT | `/api/v1/agents/{id}/networks` | 设置某 Agent 管理的网络 |
| GET | `/api/v1/agents` | Agent 节点列表 |
| POST | `/api/v1/agents` | 预签发 Agent（路径 A，一次性返回 token） |
| GET | `/api/v1/agents/{id}` | Agent 详情 |
| POST | `/api/v1/agents/{id}/approve` | 审批通过（路径 B） |
| POST | `/api/v1/agents/{id}/reject` | 驳回注册 |
| POST | `/api/v1/agents/{id}/revoke-token` | 吊销 token |
| POST | `/api/v1/agents/{id}/reissue-token` | 重新签发（轮换） |
| POST | `/api/v1/agents/{id}/disable` | 禁用 Agent |
| POST | `/api/v1/agents/{id}/enable` | 启用 Agent |
| DELETE | `/api/v1/agents/{id}` | 删除 Agent |
| POST | `/api/v1/agents/{id}/subnets` | 增加扫描白名单网段 |
| DELETE | `/api/v1/agents/{id}/subnets` | 删除扫描白名单网段 |
| POST | `/api/v1/scan` | 创建异步扫描任务（202 + scan_id） |
| GET | `/api/v1/scan` | 扫描任务列表（内存，最近 20 个） |
| GET | `/api/v1/scan/{scan_id}` | 扫描进度查询 |
| DELETE | `/api/v1/scan/{scan_id}` | 取消扫描 |
| POST | `/api/v1/scan/import` | 导入扫描结果（逐条 best-effort） |
| GET | `/api/v1/tasks` | 定时任务列表（分页信封） |
| POST | `/api/v1/tasks` | 新建定时任务（仅 `wake`） |
| PUT | `/api/v1/tasks/{id}` | 更新定时任务 |
| DELETE | `/api/v1/tasks/{id}` | 删除定时任务（不存在 → 404） |
| POST | `/api/v1/tasks/{id}/pause` | 暂停任务 |
| POST | `/api/v1/tasks/{id}/resume` | 恢复任务 |
| GET | `/api/v1/tasks/{id}/runs` | 任务执行历史（分页） |
| GET | `/api/v1/logs` | 审计日志（筛选 + 分页） |

示例（Cookie 会话；curl 须用 `-c` / `-b` 存取 Cookie）：

```bash
curl -s -c cookies.txt -X POST http://127.0.0.1:8080/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"<安装时设置的密码>"}'

curl -s -b cookies.txt http://127.0.0.1:8080/api/v1/dashboard
curl -s -b cookies.txt http://127.0.0.1:8080/api/v1/devices
```

## 备份与恢复

```bash
bash scripts/backup.sh backup [目标目录]   # 备份数据库（+ .env）为 tar.gz（0600）
bash scripts/backup.sh restore <备份包>     # 恢复前先停服务；.env 默认不恢复
```

升级、恢复与回滚流程见 `docs/upgrade.md` 与 `docs/deployment.md`。

## 目录结构

```
wakehub/
├── cmd/server/main.go        Server 入口（迁移运行器、启动拒绝校验）
├── cmd/agent/main.go         Agent 入口
├── internal/
│   ├── api/router.go         REST API 与路由
│   ├── agent/hub.go          Agent 连接中心与任务下发
│   ├── agent/client.go       Agent 客户端（WebSocket + 心跳 + 重连）
│   ├── agent/store.go        Agent 数据层（token 哈希、审批状态机）
│   ├── auth/auth.go          登录、Cookie 会话、限流
│   ├── audit/audit.go        审计日志（筛选/分页/清理）
│   ├── config/config.go      环境变量配置与启动拒绝校验
│   ├── db/migrate.go         迁移运行器（001–009，含迁移前备份与数量核对）
│   ├── device/repository.go  设备/分组/Agent/定时任务存储
│   ├── device/validate.go    设备统一验证层
│   ├── scheduler/scheduler.go 持久化 Cron 调度与执行记录
│   ├── scanner/scanner.go    局域网扫描（TCP 轻触 + ARP 缓存）
│   ├── sleep/sleep.go        受控关机/睡眠适配器（SSRF 防护）
│   ├── status/status.go      设备在线检测
│   └── wol/wol.go            WOL 魔术包构建与发送
├── web/                      Vue 3 + TypeScript + Vite + Tailwind 前端（PWA）
├── migrations/               SQLite 迁移 001–009
├── scripts/backup.sh         备份 / 恢复脚本
├── Dockerfile                多阶段构建（前端 + Go + alpine 运行）
├── docker-compose.yml        server + local agent（host 网络）
├── install.sh                一键安装脚本
├── docs/                     契约、审计报告、API/安全/升级/部署文档
└── Makefile
```

## 已知限制（v0.2.11）

- 单管理员账号，无多用户与个人 API Token（见 `docs/contract-v0.2.0.md` §13 顺延清单）。
- Server 与 Agent 之间无 TLS；浏览器到 Server 也是纯 HTTP。公网部署
  请走反向代理或 Tunnel 做 TLS 终止，不要直接暴露 8080 到公网。
- 睡眠端点无签名认证，仅限可信局域网内使用（见 `docs/security.md`）。
- 扫描是 TCP 轻触 + 读 ARP 缓存，不是真主动 ARP（需 raw socket 特权，
  容器内默认做不到）；静默设备（无开放端口、不响应 TCP）可能扫不到。
- OUI 厂商识别仅覆盖 27 条常见前缀，未命中显示 `Unknown Vendor`。
- 扫描任务只保存在服务端内存（最近 20 个），服务重启后丢失；导入后
  的设备行才是持久结果。
- 定时任务第一阶段只开放 `wake` 动作；分组暂无更新/删除接口。
- 前端 PWA 的 Service Worker 只缓存静态资源，`/api/` 请求不缓存。

## License

MIT（见 `LICENSE` 文件全文）。

白话说明：

- **谁都可以用**：个人用、商用、改代码、再发布，都免费，不用问作者。
- **必须保留署名**：源码里自带的 `LICENSE` 文件和版权声明
  （Copyright (c) 2026 moye-lab）不能删；改了代码再发，也要带上这份声明。
- **不能冒认**：不能把代码说成是自己写的，也不能拿原作者的名字给你的改版背书。
- **没有担保**：软件按“原样”提供，作者不对使用结果负责。
- **贡献欢迎**：提 issue 或 PR 都可以；PR 默认同样按 MIT 授权。
