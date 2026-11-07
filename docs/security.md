# WakeHub 安全说明（v0.3.3）

本文如实描述 v0.3.3 的安全机制与已知边界。不宣称未实现的功能。

## 部署形态与信任假设

- WakeHub 是**局域网内自托管**产品。Server 与浏览器、Agent 之间均为
  **纯 HTTP 明文**（Server 不直出 TLS，见契约 §13 顺延清单）。
- 信任假设：部署网络是可信局域网。**不要把 8080 直接暴露到公网。**
  公网访问请走反向代理（Nginx/Caddy）或 Cloudflare Tunnel 做 TLS 终止，
  并配置 `WAKEHUB_TRUSTED_PROXIES`（否则限流与审计 IP 会取代理 IP）。
- 反代做 TLS 终止时（v0.2.9 问题 4）：只要反代在
  `WAKEHUB_TRUSTED_PROXIES` 内且送来 `X-Forwarded-Proto: https`，
  会话 Cookie 自动附加 Secure（`WAKEHUB_COOKIE_SECURE=1` 不再是必需
  项，只在反代不在信任列表时才需要它强制附加）；局域网纯 HTTP 请求
  不附加 Secure（否则浏览器不接受 Cookie）。非可信方送来的
  `X-Forwarded-Proto` 一律不采信。

## 管理员认证

- 单管理员模型，无多用户与 RBAC（顺延）。
- 登录会话：HttpOnly Cookie（`wakehub_session`，SameSite=Lax，24 小时
  TTL，无滑动续期）。Cookie 值是带 `jti` 的 HMAC 签名 JSON payload，
  服务端只认签名 + 未过期 + `jti` 不在撤销表 + `iat >= password_changed_at`。
- 前端不再把 token 存 `localStorage`（v0.1.0 的 XSS 窃取路径已消除）；
  不再接受 `Authorization: Bearer`。
- 改密后**全部会话立即失效**（含当前会话），须重新登录。
- 密码：bcrypt 存储（DefaultCost，不换 Argon2id——审计结论：bcrypt 已是
  经评估的安全哈希）。新密码长度 ≥ 8 位，不得与用户名相同，不得等于
  已知占位值。忘记密码时用 CLI 重置，不删库：
  `docker compose exec wakehub-server /app/wakehub-server reset-password`。
- 登录限流：同一 IP 或同一账号 5 分钟内失败 ≥ 8 次 → 429 锁定；
  限流键取客户端 IP（不含端口）。`X-Forwarded-For` 默认不信任、不解析；
  仅当 `WAKEHUB_TRUSTED_PROXIES` 配置且直连方 IP 在列表内时才采纳。
  未配信任列表时反代后的用户共享代理 IP 限流（见 deployment.md）。
  失败记录只存内存（`failureStore` 接口，内存实现），重启后清空；
  成功登录清除记录；容量上限 10000 个 key、超限淘汰最旧；
  5 分钟清理 ticker 删过期记录。需要跨重启持久化时换存储实现，
  不为单机部署引入 Redis。
- 通用 API 限流：每会话 600 次/分钟，超限 429 + `Retry-After: 60`。
- CSRF：SameSite=Lax + 状态变更请求的 Origin/Referer 校验；不引入
  独立 CSRF token（同源 SPA 无合法跨站写场景）。
- 用户名只允许 `^[A-Za-z0-9._-]{1,64}$`；用户不存在时走 dummy bcrypt，
  返回文案与密码错误完全一致（不区分“用户不存在”与“密码错误”）。

## 启动拒绝（占位密钥）

以下任一环境变量等于占位值时，服务端直接拒绝启动（老库不豁免）：

| 变量 | 占位值 |
| --- | --- |
| `WAKEHUB_ADMIN_PASSWORD` | `wakehub123`、`replace-with-a-strong-admin-password` |
| `WAKEHUB_SECRET_KEY` | `change-me-in-production-wakehub-secret`、`replace-with-strong-secret-key`、`replace-with-random-secret-key` |
| `WAKEHUB_AGENT_ENROLL_TOKEN` | `local-agent-secret-token`、`replace-with-random-agent-token` |

install.sh 会在安装时生成随机值；已部署的老库须按 `docs/upgrade.md`
§“升级前：处理 .env 占位值”手工替换。注意：`WAKEHUB_ADMIN_PASSWORD`
只在 `users` 表为空时起种子作用；已部署的老库改密走改密接口，改
`.env` 里的密码不影响已存的管理员密码。

## Agent 身份与审批

- 每 Agent 独立 token：明文格式 `wha_` + base64url(32 字节随机数)，
  服务端只存 SHA-256 哈希，永不存明文；每 Agent 同时只有一个有效 token。
- 新 Agent 入网两条路径：管理员预签发（路径 A，一次性返回 token）或
  自助注册 + 管理员审批（路径 B）。`pending` 状态的 Agent 只许心跳，
  不下发任务。
- 吊销一个 Agent 的 token 只影响该 Agent（在线会话立即以 4403 断开）；
  重新签发即轮换，旧 token 立即失效，无宽限期。
- token 与 `agent_id` 绑定；冒认 `local` 直接 403。`local` 是保留 ID，
  内置 `local` 节点已退役。
- token 丢失后的唯一恢复路径：持入网 token 重入网 → 旧 token 作废 →
  回到 `pending` → 管理员重新审批。
- 入网 token（`WAKEHUB_AGENT_ENROLL_TOKEN`）只用于首次注册，不能用于
  日常连接；泄露后改 `.env` 并重启 server 轮换。
- `WAKEHUB_AGENT_TOKEN` 这个变量名现仅为 **Agent 侧** token：首次注册
  时填入网 token（与服务端 `WAKEHUB_AGENT_ENROLL_TOKEN` 相同的值）；
  获批后 Agent 自动改用 token 文件（`WAKEHUB_AGENT_TOKEN_FILE`，
  默认 `./data/agent-token`，0600）里的 per-agent token，该变量即失效；
  预签发时填 `wha_` token。**服务端 v0.2.0 不再读取它**（死代码已删）。
- WS 握手：token 无效 401、已吊销 403、未获批 403、ID 不绑定 403、
  已禁用 403；Origin 非空时须同源或在 `WAKEHUB_WS_ALLOWED_ORIGINS`
  白名单内（Origin 校验不是身份认证，通过后仍须持有效 token）。
- WSS：Agent 连 `wss://` 时必须验证服务端证书，禁止默认跳过
  （仅显式 `WAKEHUB_AGENT_TLS_SKIP_VERIFY=1` 时跳过，且启动日志输出警告）。
- 明文 WS（v0.2.9 问题 3）：`ws://` 仅回环（127.0.0.0/8、`::1`、
  `localhost`）默认放行；连非回环地址时默认拒绝启动，须改用 `wss://`
  或显式设置 `WAKEHUB_AGENT_ALLOW_INSECURE_WS=1`（启动日志输出警告）。
  本开关与 `TLS_SKIP_VERIFY` 相互独立：前者管“是否加密传输”，后者管
  “是否验证证书”。注意 Server 本身不终止 TLS（见“已知不做”），`wss://`
  要求 Server 在 TLS 反向代理（或隧道）之后；纯局域网 HTTP 部署用
  `ws://` + 本开关 = 1 即可。`install.sh` 模式 3（远端 Agent）默认
  提示 `wss://`，输了非回环 `ws://` 会当场警告并让用户确认是否放行。
- 心跳 15s，服务端 45s 未收到心跳判定僵死、关闭连接、置 offline；
  重连指数退避 1s→60s（±25% 抖动）。

## 睡眠 / 关机

- 仅允许 `shutdown | sleep | hibernate` 三种白名单操作，两种适配器：
  `http_sol`（设备上预先部署的 HTTP Sleep-on-LAN 端点）与 `ssh`
  （v0.2.5，Linux / Windows 直连 SSH，见下）。
- 端点 allowlist：主机须为私有 IPv4（10/8、172.16/12、192.168/16）或
  回环（127.0.0.0/8），禁公网、禁 169.254.0.0/16（云元数据段）、禁
  userinfo；端口 1–65535；禁跟随重定向；超时 5s；HTTPS 优先。
- 管理网段范围（v0.2.9 问题 7，最小一致版）：睡眠下发时目标还须落在
  执行 Agent 的管理网段内（管理网络与 Agent 的 subnet 字段取并集；
  Agent 未声明网段时只做上面那条基线校验）。回环 127/8 始终放行
  （它是 Agent 本机的地址）。设备级目标即设备存的端点/IP，只认这
  一个——睡眠请求的 body 里没有端点字段，调用方无法把请求发往别的地址；SSH 适配器的目标 IP 同样受范围校验。
- **v0.2.0 睡眠端点无签名认证**：没有对端验签实现，单方面加 HMAC 是
  安全表演，不是安全机制（wire 格式已预留，后续版本实现自有 sleep
  agent 时启用）。**仅限可信局域网内使用。**
- SSH 适配器（v0.2.5，契约 §6.3）：密码 AES-256-GCM 加密存储，密钥由
  `WAKEHUB_SECRET_KEY` 派生（`internal/secret`），明文永不落库、不进
  响应、不进日志；`SECRET_KEY` 更换后旧密文无法解密，接口提示重新填写
  保存。主机密钥 TOFU（首次记录指纹，之后每次校验，不符即拒绝；改密码
  即重新信任；离开 SSH 即清空凭据与指纹）。SSH 目标仅限私有 IPv4 / 回环，
  禁主机名、公网、169.254 元数据段；命令字符串固定写死，不拼接外部输入。
  密码经 server→Agent 的 WS 通道下发，跨不可信网络部署 Agent 时须用
  `wss://`。

## 扫描与 WOL

- 扫描的 CIDR 必须等于 Agent 白名单中某一项或是其子网，且为私有 IPv4、
  掩码 ≥ /24；公网网段拒绝扫描。
- 扫描是 TCP 轻触 + 读 ARP 缓存，不是真主动 ARP（需 raw socket 特权，
  容器内默认做不到）；静默设备可能扫不到。OUI 识别仅覆盖 27 条常见前缀。
- WOL 魔术包是二层广播，只在同一广播域生效；Agent 须与目标设备在同一
  广播域（local agent 用 host 网络）。

## 审计

- 全部写操作写审计日志（创建/更新/删除/唤醒/睡眠/扫描/导入/审批/
  吊销/改密/退出等），读操作不审计。事件清单见契约 §1.6。
- `detail` 不得含明文密码、完整 token、完整 Cookie（只记 token 后 4 位
  或 jti 前 8 位用于定位）。
- 审计日志保留 90 天定期清理；不提供按条件删除日志的 API（防审计湮灭）。
- 审计写入失败不再静默丢弃（调用方记系统日志）。

## 容器安全

- 单容器 vs 双容器隔离：单容器是便捷部署——Server 与 Agent 以同一用户
  （wakehub，uid 10001）跑在同一个容器里，共享 `/app/data`（数据库、
  SSH 密文、密钥与 Agent token），Agent 进程能读到数据库文件。需要隔离
  时用 Compose 部署：Server 与 Agent 是两个容器、两个数据卷（Server 用
  `wakehub_data`，Agent 只挂 `wakehub_agent_data`、仅存 agent-token
  文件），Agent 触不到数据库文件。详见 `docs/deployment.md`。
- 镜像以非 root 用户（uid 10001）运行；server 与 agent 均
  `cap_drop: [ALL]` + 只读根文件系统；server 另有 `no-new-privileges`。
- agent 另加 `cap_add: [NET_RAW]`（ping 检测需发 ICMP；v0.2.0 的
  `local` 节点已退役，ping 在 Agent 侧执行），且 agent **不能**开
  `no-new-privileges`：`/bin/ping` 的文件能力 `cap_net_raw+ep` 靠 exec
  时授予，`no-new-privileges` 会让内核忽略文件能力（V-N1 实测）。
- 数据库文件权限 0600；备份包权限 0600（内含数据库与密钥，请妥善保管）。
- 数据目录、备份目录、Agent token 目录均为 0700（v0.2.9 问题 11；
  已存在的旧目录在启动时也会被收紧）。
- `.env` 权限 0600；不要提交进仓库（已进 `.gitignore`）。

## 已知不做（不要误以为有）

- 多用户 / RBAC、个人 API Token、服务端直出 TLS、Sleep 签名认证、
  真 ARP 主动扫描：均不在 v0.3.1，见契约 §13。
