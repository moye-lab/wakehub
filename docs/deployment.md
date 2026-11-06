# WakeHub 部署文档（v0.2.11）

## 部署方式

### 推荐：install.sh 一键安装（Docker Compose）

```bash
cd wakehub
bash install.sh
```

安装器先问部署模式（1=server+local agent，2=仅 server，3=仅 agent），
再逐项确认端口、管理员密码等（已有 `.env` 的值按回车保留），写配置前
打印打码确认页，装完轮询健康接口与登录接口确认就绪；模式 1 还会自动
审批本机 local agent。重复运行安全（配置保留、服务重建）。

要求：Docker 与 Docker Compose v2 已安装；安装器不自动安装 Docker。
请以 docker 组成员身份直接跑，不要 `sudo`。

### 手动：Docker Compose

```bash
cd wakehub
cp .env.example .env
# 编辑 .env：把 WAKEHUB_ADMIN_PASSWORD / WAKEHUB_SECRET_KEY /
# WAKEHUB_AGENT_ENROLL_TOKEN 改成随机值（占位值会使服务端拒绝启动）
docker compose up -d --build
docker compose logs -f wakehub-server
```

不要直接 `make docker-up`：它跳过 install.sh 的 `.env` 生成与校验，
会以占位值启动并被服务端拒绝。

### 多架构构建

Dockerfile 是多架构的（`golang` / `node` / `alpine` 基础镜像均为多架构；
`GOARCH` 取 buildx 的 `TARGETARCH`）：

```bash
docker buildx build --platform linux/amd64,linux/arm64 -t wakehub:0.2.11 .
```

arm64 机器上直接 `docker compose up -d --build` 即可（本机构建目标架构）。

构建源（v0.2.9 问题 17）：Dockerfile 用 `ARG NPM_REGISTRY` /
`ARG GOPROXY`（默认国内源），不要写死在 RUN 里；覆盖方式（二选一）：

```bash
# 方式一：.env 里写（compose build.args 会取用，install.sh 重跑也保留）
NPM_REGISTRY=https://registry.npmjs.org
GOPROXY=https://proxy.golang.org,direct
# 方式二：单次构建传参
docker build --build-arg NPM_REGISTRY=https://registry.npmjs.org \
             --build-arg GOPROXY=https://proxy.golang.org,direct .
```

## 服务与端口

| 服务 | 网络 | 端口 | 说明 |
| --- | --- | --- | --- |
| `wakehub-server` | bridge | `${WAKEHUB_BIND_ADDRESS:-0.0.0.0}:${WAKEHUB_PORT:-8080}:8080` | REST + WS + 前端同端口 |

绑定地址（v0.2.9 问题 18）：默认 `0.0.0.0`（监听全部网卡）。只在
“仅本机访问”时才改 `127.0.0.1`；从另一台机器访问时（如经 Cloudflare
隧道连本机 IP）改回环会直接断连，勿改。
| `wakehub-agent-local` | host | — | WOL 是二层广播，必须与目标设备同一广播域 |

健康检查：`GET /api/v1/health`（公开）。compose 里 server 有
`healthcheck`，agent `depends_on: condition: service_healthy`。
不要用 `GET /` 做健康检查口。

## 数据与持久化

- 命名卷 `wakehub_data` 挂在 Server 的 `/app/data`：数据库（`wakehub.db`，WAL
  模式，0600）与审计、SSH 密文等全部服务端数据。
- 命名卷 `wakehub_agent_data` 挂在 Local Agent 的 `/app/data`：仅 Agent token
  文件（`agent-token`，0600）。v0.2.9 起两卷分离，Agent 触不到数据库文件。
- 从 v0.2.8 及更早版本升级：先跑 `bash scripts/migrate-agent-volume.sh`
  把旧卷 Token 复制到新卷（保留内容与 0600 权限，旧卷原样保留），再
  `docker compose up -d --build`。详见 `docs/MIGRATION-v0.2.9.md`（含回滚）。
- `docker compose down` 保留卷；`docker compose down -v` **删库删 token**，
  执行前先备份。
- 镜像以非 root 用户（uid 10001）运行；若改用宿主机目录 bind-mount
  代替命名卷，须把目录属主改成 10001，否则 server 写不了库。
- Agent token 文件丢失时，Agent 持入网 token 重入网 → 回到 `pending` →
  管理员重新审批（旧 token 作废）。

## Agent 入网两种路径

- **路径 A（预签发，主推）**：管理员在 UI/API 创建 Agent
  （`POST /api/v1/agents`）→ 一次性拿到 `wha_` token → 写进 Agent 的
  `WAKEHUB_AGENT_TOKEN` → Agent 连接即 `connected`。
- **路径 B（自助注册 + 审批）**：Agent 持入网 token
  （`WAKEHUB_AGENT_TOKEN` = 服务端 `WAKEHUB_AGENT_ENROLL_TOKEN` 的值）
  连接 → 进入 `pending` → 管理员在 UI 审批 → token 经 WS 推送，
  Agent 持久化到 token 文件后重连。
- 模式 1 安装时，install.sh 会自动审批本机 local agent；模式 3 的
  Agent 须等远端管理员审批。

## 备份与恢复

```bash
bash scripts/backup.sh backup [目标目录]   # 备份数据库（+ .env）为 tar.gz（0600）
bash scripts/backup.sh restore <备份包> [--include-env] [--force]
```

- 备份用 sqlite3 联机备份（含 WAL 内容）；无 sqlite3 时退回 python3
  的 backup API，最后退回 cp（须停服务）。
- 恢复前先停服务；恢复时先把当前库另存为 `.bak-<时间戳>`；`.env`
  默认不恢复（避免覆盖现役密钥），须显式加 `--include-env`。
- 建议：升级前、改配置前各备一次；备份包内含数据库与密钥，请妥善保管。

## 反向代理与 TLS

Server 本身只出纯 HTTP。公网访问请走反向代理或 Cloudflare Tunnel
做 TLS 终止。反代须在 `WAKEHUB_TRUSTED_PROXIES` 内，Cookie Secure
才会按 `X-Forwarded-Proto` 自动附加（v0.2.9 问题 4）；未配信任列表
时反代后所有用户共享代理 IP 限流。示例（Nginx，反代到 server 的 8080）：

```nginx
server {
    listen 443 ssl;
    server_name wake.example.com;
    # ... ssl_certificate / ssl_certificate_key ...
    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        # WebSocket 升级（Agent 用 WS 连 server 时经过反代才需要）
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
    }
}
```

并在 `.env` 里配：

```bash
WAKEHUB_TRUSTED_PROXIES=127.0.0.1/32   # 仅信任反代本机，否则限流与审计 IP 取代理 IP
# WAKEHUB_COOKIE_SECURE=1              # 反代在信任列表内时 Secure 自动附加，本项可不设；
                                        # 仅当反代不在信任列表时才需要它强制附加（局域网 HTTP 保持 0）
WAKEHUB_WS_ALLOWED_ORIGINS=https://wake.example.com
WAKEHUB_STATUS_CHECK_INTERVAL=300      # 周期状态检测间隔（秒），默认 300；0 为禁用（V-N2）
```

Agent 连公网 `wss://` 时必须验证服务端证书（默认验证；仅测试场景可
显式 `WAKEHUB_AGENT_TLS_SKIP_VERIFY=1`，生产不要开）。

## 常用运维命令

```bash
docker compose ps                        # 服务状态
docker compose logs -f                   # 全部日志
docker compose logs -f wakehub-server    # 仅 server
docker compose logs -f wakehub-agent-local
docker compose restart wakehub-server    # 重启 server
docker compose down                      # 停服务（数据保留）
docker compose down -v                   # 停服务并删数据（先备份！）
curl http://127.0.0.1:8080/api/v1/health # 健康检查
docker compose exec wakehub-server /app/wakehub-server reset-password  # 忘记密码时重置
```

## 常见问题

**Q：启动即退出，日志报“拒绝启动：WAKEHUB_…仍为占位值”？**
A：`.env` 里仍有占位值。按 `docs/upgrade.md`“升级前：处理 .env 占位值”
手工替换三个值（或重跑 `bash install.sh`，它会生成缺失的随机值；
已存在的占位值不会被自动覆盖，须手工替换）。

**Q：登录时提示“用户名或密码错误”，但密码没错？**
A：先确认密码不是 `.env` 里的占位值；已部署的老库改 `.env` 里的密码
不生效（只在建库时起种子作用），须用改密接口或 `reset-password` CLI。
另确认 Cookie 未被浏览器拦截（须同源访问）。

**Q：Agent 一直 pending，点审批没反应？**
A：审批后 token 经 WS 推送给 Agent，Agent 持久化后重连；若 Agent
容器日志出现“注册已获批，新凭证已保存，正在重连”即成功。若 Agent
始终不出现，先查 agent 日志里的握手状态码：401 是 token 无效、
403 可能是已禁用或冒认 `local`。

**Q：设备唤醒返回 502 agent_offline？**
A：目标设备所属 Agent 当前离线。检查对应 Agent 容器的运行状态与
WS 地址（`WAKEHUB_SERVER_WS_URL`），以及 server 的 Agents 页状态。

**Q：设备唤醒返回 502 agent_not_found？**
A：设备归属的 Agent 不存在（被删，或遗留的 `local` / 空归属）。
在设备编辑页重选 Agent。

**Q：扫描返回 400 cidr_not_allowed？**
A：扫描的 CIDR 不在该 Agent 的白名单内。白名单默认含注册时上报的
网段；管理员可在 Agents 页增删白名单网段。

**Q：扫描扫不到某台设备？**
A：扫描是 TCP 轻触 + 读 ARP 缓存，不是真主动 ARP；静默设备（无开放
端口、不响应 TCP）可能扫不到。可改用该设备的检测方式确认在线状态，
或手动新增设备。

**Q：ping 检测总失败，但设备明明在线？**
A：Agent 容器内 ping 需 `CAP_NET_RAW`（compose 已默认授予 agent）。
若自行部署 Agent 容器，须加 `cap_add: [NET_RAW]`；或把检测方式改
为 TCP。

**Q：定时任务到了时间没执行？**
A：先查任务的 runs 历史（`GET /api/v1/tasks/{id}/runs`）：`skipped`
表示空目标/全禁用；`failed` 看每设备 reason。调度器启动失败是致命
错误，服务会拒绝启动——若服务能跑，调度器就是活的。

**Q：`docker compose up` 报 `WAKEHUB_…未设置，请先跑 bash install.sh`？**
A：compose 的密钥类变量无默认值，未设置时直接报错（有意为之，不以
占位值静默启动）。跑 `bash install.sh` 生成 `.env` 即可。

## 验证清单（首次部署 / VM 验证用）

以下在本机沙箱**未**实测（沙箱跑不起容器：`runc setns: operation not
permitted`；且无 docker CLI），须在用户 VM / 真机上逐项验证：

- [ ] `docker compose config` 通过
- [ ] `docker compose up -d --build` 构建成功（amd64 与 arm64 各一）
- [ ] `docker compose ps` 两服务 healthy/running，server 健康检查通过
- [ ] 登录成功（Cookie 下发），Dashboard 字段正常
- [ ] Local Agent 自动获批并 `connected`
- [ ] 扫描发现真实设备并导入，唤醒真实设备（冷启动开机）
- [ ] 多 Agent 跨网段（各 Agent 只扫自己白名单内的网段）
- [ ] `docker compose down` 后重起，数据与 token 文件保留
- [ ] 备份脚本往返（备份 → 删库 → 恢复 → 数据回来）
