# WakeHub v0.2.0 API 与数据模型契约（M0 冻结版）

- **版本**：v0.2.0（M0 契约，冻结后作为 M1–M6 的实施依据）
- **基线**：v0.1.0，main 单 commit `f7e3df4`；审计报告 `docs/audit-v0.2.0.md`
- **状态**：待评审。评审通过前，任何模块不得凭猜测调用未定接口；契约变更须走评审并同步更新本文档。
- **范围**：本文只定接口与数据模型。实现细节（函数签名、内部包划分）由各模块自定，但不得违背本契约的 wire 格式与状态机。

## 0. 契约原则

1. **契约先行**：前后端按本文并行；M6 可先按契约做 UI 与 mock，后端接口落地后联调。
2. **与审计一致**：本契约只采纳审计报告核实为属实/部分属实的缺口。任务书中已被证伪的子项**不在契约内**，见 §14（不实项清单）：
   - 不存在“页面切换重复定时器”问题（全文无 `setInterval`），不设相关接口；
   - `sleep.go` 不存在任意 Shell 执行（全文无 `os/exec`），契约只处理已证实的 SSRF；
   - bcrypt DefaultCost 已是经评估的安全哈希，**不强制换 Argon2id**；
   - Dashboard 统计卡片数据真实，不存在虚假统计问题。
3. **不删功能**：已验证的 WOL 与扫描链路只修不推翻；所有 v0.1.0 已有端点除非本节明确标记，否则保留。
4. **不写无主功能**：凡未排进 M1–M6 的功能，不进本契约；顺延项集中列在 §14，并写明顺延理由。
5. **破坏性变更集中声明**：v0.2.0 仍是 0.x，允许破坏性变更，但必须在本节列清，前端 M6 与升级文档（M7）按此处理。**不设向后兼容垫片**，旧客户端升级后须重新登录。

### 0.1 v0.2.0 破坏性变更清单

| 变更 | v0.1.0 | v0.2.0 | 影响 |
|---|---|---|---|
| 登录响应 | 返回 `{token, username}`，前端存 `localStorage` | 只发 HttpOnly Cookie，不再返回 token（§2.1） | 旧前端/脚本须重新登录；`localStorage` 里的旧 token 作废 |
| 会话 token 格式 | `base64(username\|expiry).sig`，无 jti | JSON payload + `jti` + `iat`（§2.3），旧格式直接拒收 | 升级后全部会话失效，须重新登录一次 |
| 管理员认证 | Bearer Header | 仅 Cookie（§2.2），不再接受 `Authorization: Bearer` | 任何外部脚本须改走 Cookie 或等个人 API Token（顺延，§14） |
| 设备列表响应 | 裸数组 | 分页信封 `{items, total, page, page_size}`（§1.4） | M6 按新结构取数 |
| 批量唤醒响应 | `{total, success}` | §5.2 的完整结构 | M6 按新结构渲染 |
| 扫描 | 同步 `POST /scan` 直接返回结果 | 异步任务：创建 → 轮询进度 → 导入（§7） | M6 扫描向导按异步流程实现 |
| 设备 `agent_id` | 缺省静默为 `local` | 必填，无默认值；`local` 不再是合法值（§4.1） | 旧设备行按 §4.6 与迁移文档处理 |
| 内置 `local` 节点 | 种子 Agent 行 + 进程内直发 | 种子行由迁移 003 删除；全部任务经注册 Agent 执行（§3.6） | 单机无 Agent 容器时无法唤醒，部署须带 local agent 容器（标准安装本就包含） |
| 全局 Agent Token | `WAKEHUB_AGENT_TOKEN` 全局共用 | 废止；改为每 Agent 独立 token + 入网 token（§3.1） | 全部 Agent 升级后须重新入网（runbook 见迁移文档 §4） |
| 定时任务创建 | `action` 任意值（实际被忽略） | 仅允许 `wake` / `sleep`，其余 400（§8.1） | — |
| 更新设备不存在 | 返回成功 | 404（§1.3、§4.4） | — |

## 1. 通用约定

### 1.1 响应信封（不变）

沿用 v0.1.0 的 `apiResponse`，所有接口（包括错误）统一使用：

```json
{ "code": 0, "message": "ok", "data": { "...": "..." } }
```

- 成功：HTTP 200/201，`code: 0`，`message: "ok"`（创建类接口用 `"created"`，删除用 `"deleted"`，与 v0.1.0 一致）。
- 失败：HTTP 状态码见 §1.3，`code` **等于 HTTP 状态码**（与 v0.1.0 的做法一致：`code` 字段即 HTTP 状态），`message` 为中文可读信息，`data` 为 `null` 或含 `errors` 的对象。
- 时间：所有时间字段均为 RFC 3339 UTC（`2026-11-05T22:45:22Z`）；服务端统一存 UTC，UI 显示本地时区。
- ID：全部为 UUID v4 字符串。
- 空列表：返回 `[]`，永不返回 `null`（v0.1.0 扫描空结果曾返回 `null` 致前端崩溃，此为硬性规定）。

### 1.2 结构化校验错误

设备/任务/Agent 创建与更新失败时，`data` 必须携带机器可读的字段级错误，前端据此在字段旁渲染：

```json
{
  "code": 400,
  "message": "设备信息校验失败",
  "data": {
    "errors": [
      { "field": "mac", "code": "invalid_format", "message": "MAC 地址格式错误，应为 AA:BB:CC:DD:EE:FF" },
      { "field": "agent_id", "code": "not_found", "message": "Agent agent-x 不存在" }
    ]
  }
}
```

错误 `code` 枚举（本契约全局共用，新增时须更新此表）：

| code | 含义 | 典型 HTTP |
|---|---|---|
| `required` | 字段缺失或为空 | 400 |
| `invalid_format` | 格式错误（MAC/IP/CIDR/枚举/用户名） | 400 |
| `out_of_range` | 数值或长度越界（端口 1–65535 等） | 400 |
| `invalid_value` | 值不在允许集合内 | 400 |
| `not_found` | 引用的对象不存在（设备/任务/Agent/分组） | 404 |
| `conflict` | 冲突（MAC 重复、Agent ID 已存在） | 409 |
| `not_authorized` | Agent 存在但未获批 | 400 |
| `agent_offline` | Agent 已获批但当前未连接 | 502（操作时）/ 警告（创建时） |
| `agent_disabled` | Agent 已禁用 | 400 / 502 |
| `agent_not_approved` | WS 握手时 Agent 未获批 | 403 |
| `agent_id_mismatch` | token 与 `X-Agent-ID` 不绑定 | 403 |
| `agent_not_assigned` | 设备未关联任何有效 Agent | 400 |
| `device_disabled` | 设备已禁用 | 400（操作时为跳过原因） |
| `invalid_mac` / `invalid_cidr` | 与 `invalid_format` 同义的细化码，用于批量结果 `reason` | — |
| `cidr_not_allowed` | 网段不在 Agent 白名单内 | 400 |
| `scan_in_progress` | 该 Agent 已有进行中的扫描 | 409 |
| `unsupported_action` | 定时任务 action 不在白名单 | 400 |
| `empty_target` | 目标为空（空分组、目标不存在） | 400 |
| `rate_limited` | 限流 | 429 |
| `invalid_token` / `token_revoked` / `token_expired` | 会话凭证问题 | 401 |
| `wrong_password` | 改密时当前密码错误 | 400 |
| `weak_password` | 新密码不满足强度 | 400 |

### 1.3 HTTP 状态码规范

| 场景 | HTTP | `code` |
|---|---|---|
| 成功（查询/操作） | 200 | 0 |
| 创建成功 | 201 | 0 |
| 校验失败 | 400 | 400 |
| 未登录 / 会话失效或被撤销 | 401 | 401 |
| 已登录但无权操作（预留，v0.2.0 单管理员下基本不用） | 403 | 403 |
| 对象不存在（设备/任务/Agent/分组/扫描任务/运行记录） | 404 | 404 |
| 冲突（MAC 重复、Agent ID 重复、扫描进行中） | 409 | 409 |
| 限流 | 429 | 429 |
| 内部错误 | 500 | 500 |
| 下游执行失败（Agent 离线、dispatch 失败、检测失败） | 502 | 502 |

硬性规定：

- 更新/删除不存在的对象必须返回 **404**，不许返回成功（修复审计 B-N5）。
- 更新失败必须返回具体错误，不许一律 400（区分校验 400 / 不存在 404 / 内部 500）。
- `withAuth` 不得把内部错误原文返回客户端（修复 B-N15）：认证失败统一返回固定文案（§2.2）。
- 未知 API 路径返回 404 + 信封，不许返回默认 404 页面。
- 请求 Body 上限 **1 MiB**（`MaxBytesReader`），超限返回 **413** + `code: 413`（M1）。

### 1.4 分页规范

所有列表接口（设备、日志、任务运行记录、扫描任务）统一使用：

- 查询参数：`page`（从 1 开始，默认 1）、`page_size`（默认 50，最大 200，超限钳制为 200）。
- 响应 `data` 统一为：

```json
{ "items": [], "total": 0, "page": 1, "page_size": 50 }
```

- `total` 为过滤后的总数（不是本页条数）。
- 排序：设备按 `created_at DESC`；日志按 `created_at DESC`；运行记录按 `started_at DESC`；扫描任务按 `started_at DESC`。v0.2.0 不开放自定义排序参数（顺延）。

### 1.5 认证总则

- 管理端 API（除登录与 Agent WS 外）全部要求登录态（现状已是如此，保持）。
- 登录态凭证见 §2，不再使用 `Authorization: Bearer`。
- 审计：全部写操作（创建/更新/删除/唤醒/睡眠/扫描/导入/审批/吊销/改密/退出）写审计日志；读操作不审计（现状如此，保持）。
- 通用 API 限流（M1）：每个已登录会话每分钟最多 **600** 次请求，超限返回 429 `rate_limited`；登录接口另有独立限流（§2.5）。
- 幂等性：v0.2.0 **不实现通用 `Idempotency-Key`**（无存储设计，见 §14）。重试安全由自然键保障：设备创建按 MAC 去重（409）、扫描导入按 MAC 去重（§7.3）、批量唤醒重发魔术包本身无害、定时任务创建不去重（调用方避免重复提交，前端加防重复提交）。
- 危险操作二次确认是**前端职责**（删除、关机、睡眠统一用确认弹窗，M6）；API 层不另设确认参数。

### 1.6 审计事件命名（新增事件，M5 落地）

现有事件保持不变（`login_success`、`login_failed`、`device_create`、`device_update`、`device_delete`、`device_wake`、`device_wake_failed`、`device_batch_wake`、`device_sleep`、`device_sleep_failed`、`group_create`、`network_scan`、`schedule_create`、`schedule_delete`）。v0.2.0 新增：

| 事件 | 触发点 |
|---|---|
| `auth_password_change` | 改密成功 |
| `auth_logout` | 退出登录 |
| `device_check` | 状态检测（v0.1.0 缺失，M5 补） |
| `device_check_failed` | 状态检测失败（派发错误/结果解析失败/非法状态；M5 与 `device_check` 对称补齐） |
| `scan_import` | 扫描导入 |
| `scan_cancel` | 扫描取消 |
| `agent_register` | Agent 首次注册（pending 创建） |
| `agent_approve` / `agent_reject` | 审批通过 / 驳回 |
| `agent_revoke` / `agent_reissue` | token 吊销 / 重新签发 |
| `agent_disable` / `agent_enable` | 禁用 / 启用 |
| `agent_delete` | 删除节点 |
| `agent_update` | Agent 白名单变更（M2 已写，M5 评审确认并入表） |
| `agent_connect` / `agent_disconnect` | Agent 上线 / 掉线（掉线由服务端心跳超时判定后记录） |
| `schedule_pause` / `schedule_resume` | 任务暂停 / 恢复 |
| `schedule_update` | 任务更新（M4 已写，M5 评审确认并入表） |
| `schedule_run` | 每次定时执行完成（成功/失败/跳过均记） |
| `audit_cleanup` | 审计日志定期清理（actor 为 `system`，仅实际删行时记） |

审计 `detail` 不得含明文密码、完整 token、完整 Cookie（只记 token 后 4 位或 jti 前 8 位用于定位）。

---

## 2. 认证（M1）

### 2.1 设计决策：HttpOnly Cookie + SameSite=Lax，同源 SPA

**决策**：v0.2.0 登录态使用 HttpOnly Cookie，不再把长期 Bearer Token 交给前端 `localStorage`。

理由：

- 审计 F-N1 已证实：`wakehub_token` 存 `localStorage`，任意 XSS 即可窃取长期有效的 Bearer Token。Cookie + HttpOnly 使 JS 读不到凭证，从根本上消除该窃取路径。
- WakeHub 前端是与 Server **同源部署**的 SPA（`web/dist` 由 Server 同端口托管，生产构建用相对路径 `/api`，见审计 F-N8），不存在跨站调用 API 的合法场景，Cookie 方案无跨域代价。
- SameSite=Lax 阻止跨站 POST 携带 Cookie；再叠加 §2.6 的 Origin 校验，CSRF 风险已覆盖（见 §2.6，不再引入独立 CSRF token）。
- 代价：外部脚本/非浏览器客户端无法再用 Bearer 调用 API。v0.2.0 接受该代价（个人 API Token 顺延到后续版本，§14）；升级文档须写明。

### 2.2 登录态规则

- Cookie 名：`wakehub_session`。
- 属性：`Path=/; HttpOnly; SameSite=Lax; Max-Age=86400`（24h，与 token TTL 一致）。
- `Secure`：当请求本身是 TLS（`r.TLS != nil`）或环境变量 `WAKEHUB_COOKIE_SECURE=1` 时自动附加；纯 HTTP 局域网部署不附加（否则 Cookie 不会被浏览器接受）。
- `withAuth` 只读 Cookie，不再读 `Authorization` 头。Cookie 缺失/非法/过期/被撤销 → **401** + 固定文案 `"未登录或登录已过期"`（不得外泄内部错误原文，修复 B-N15）。
- 前端 `apiFetch` 不再手动拼 `Authorization` 头；`fetch` 须带 `credentials: 'same-origin'`；收到 401 时清除本地登录态并跳登录页。
- 登录页不得再明文写出默认密码（修复 F-N2）；用户名不再预填 `admin`。

### 2.3 会话 Token 格式（jti + 可撤销）

Token 明文格式（签发后只存在于 Cookie 值中，永不进日志与 JSON 响应）：

```
base64url(JSON payload) + "." + base64url(HMAC-SHA256(secret, payload_bytes))
```

payload（JSON）：

```json
{ "sub": "admin", "jti": "9f1c2a3e-...", "iat": 1792450000, "exp": 1792536400 }
```

- `sub`：用户名；`jti`：本次会话唯一 ID（UUID v4）；`iat`/`exp`：Unix 秒；TTL **24 小时**（与 v0.1.0 一致）。
- 校验顺序：格式 → 签名（恒定时间比较）→ `exp` → `jti` 不在撤销表 → `iat >= password_changed_at`（改密后旧会话失效，§2.4）。
- 旧格式 token（`username|expiry`，无 jti）**直接拒收**；升级后全部会话失效，用户须重新登录一次（升级文档写明）。
- 用户名校验（修复 B-N14）：用户名只允许 `^[A-Za-z0-9._-]{1,64}$`。含 `|` 等字符的用户名在种子/改名时即被拒绝；`EnsureAdmin` 与改密接口均执行该校验。
- 用户不存在时须做一次 dummy bcrypt 比较再返回（修复 B-N6 的时序侧信道）：返回文案与密码错误完全一致（`"用户名或密码错误"`），不得区分“用户不存在”与“密码错误”。
- **不做滑动续期**：24h 到期必须重新登录。滑动续期顺延（§14）。
- bcrypt 保留 `bcrypt.DefaultCost`，**不换 Argon2id**（审计结论：bcrypt 已是经评估的安全哈希；换算法无安全收益，只增迁移成本）。

### 2.4 认证接口

#### POST /api/v1/auth/login

请求：

```json
{ "username": "admin", "password": "••••••••" }
```

- Body 解码失败 → 400。
- 成功：HTTP 200 + `Set-Cookie: wakehub_session=...`，响应体：

```json
{ "code": 0, "message": "ok", "data": { "username": "admin", "expires_at": "2026-11-06T22:45:22Z" } }
```

**不再返回 `token` 字段**（破坏性变更，§0.1）。

- 失败：用户名或密码错误 → 401 + `"用户名或密码错误"`；限流触发 → 429 + `code: "rate_limited"`（见 §2.5）。失败须写 `login_failed` 审计（沿用现状）。
- 登录成功写 `login_success` 审计；审计的 `ip_address` 必须是客户端 IP（不含端口，见 §2.5）。

#### POST /api/v1/auth/logout

- 须登录。服务端把当前会话的 `jti` 写入撤销表（`revoked_tokens`，迁移 002），并下发清除 Cookie：`Set-Cookie: wakehub_session=; Path=/; HttpOnly; SameSite=Lax; Max-Age=0`。
- 响应：`{ "code": 0, "message": "ok", "data": null }`，写 `auth_logout` 审计。
- 前端退出后跳登录页。

#### POST /api/v1/auth/change-password

- 须登录。请求：

```json
{ "current_password": "••••••••", "new_password": "••••••••" }
```

- 校验：
  - 当前密码错误 → 400 + `errors: [{ "field": "current_password", "code": "wrong_password", "message": "当前密码不正确" }]`。
  - 新密码强度：长度 ≥ 8 位；不得与用户名相同；不得等于任何已知占位值（`wakehub123` 及 §2.7 的密钥占位值）。不满足 → 400 + `errors: [{ "field": "new_password", "code": "weak_password", "message": "新密码长度至少 8 位" }]`。
  - 新密码 bcrypt 哈希入库；`users.password_changed_at` 置为当前时间。
- 效果：**全部会话立即失效**（含当前会话，靠 `iat >= password_changed_at` 判定；撤销表不需逐条写 jti）。响应成功后前端须跳登录页重新登录。
- 写 `auth_password_change` 审计。响应：`{ "code": 0, "message": "密码已修改，请重新登录", "data": null }`。
- **禁止删库改密**：改密是正常接口，任何情况下不得要求用户删除数据库（任务书第 4 项硬性要求；README 的删库改密说明由 M7 删除）。

#### GET /api/v1/auth/me

- 须登录。返回当前会话信息，供前端判断登录态与设置页展示：

```json
{ "code": 0, "message": "ok", "data": { "username": "admin", "role": "admin", "issued_at": "2026-11-05T22:45:22Z", "expires_at": "2026-11-06T22:45:22Z" } }
```

#### 密码重置（无 HTTP 接口，CLI 方式）

**决策**：v0.2.0 不提供“邮箱找回 / 重置链接”式 HTTP 重置接口——单管理员自托管产品无邮件基础设施，HTTP 重置链接反而引入新的攻击面。

重置方式（M1 实现，install.sh 暴露）：

```
wakehub-server reset-password
```

- 交互式读取两次新密码（终端不回显），校验强度同改密（≥ 8 位），bcrypt 入库并更新 `password_changed_at`（全部会话失效）。
- 仅限能登录服务器本机 / 能执行容器命令的运维人员使用；这是自托管产品的标准做法。
- 升级文档与安全说明（M7）须写明该命令。

### 2.5 登录限流与客户端 IP

修复审计第 13 项（限流名存实亡）与 B-N15：

- 限流键：**客户端 IP（不含端口）+ 账号** 双维度。IP 从 `net.SplitHostPort(r.RemoteAddr)` 取 host 部分；`X-Forwarded-For` **默认不信任、不解析**。
- 反向代理场景：仅当 `WAKEHUB_TRUSTED_PROXIES`（逗号分隔的 CIDR 列表，如 `192.168.1.0/24`）配置且直连方 IP 在列表内时，才取 `X-Forwarded-For` 的第一个 IP；否则忽略该头（防伪造）。未配置该变量时，反代后所有用户共享代理 IP 的问题仍然存在——这是显式要求运维配置的项，文档写明。
- 阈值（沿用 v0.1.0 的数值）：同一 IP 或同一账号 **5 分钟内失败 ≥ 8 次** → 锁定，返回 **429** + `code: "rate_limited"` + `"登录失败次数过多，请稍后再试"`。锁定期间的成功登录也不放行。
- 内存上限：限流 map 设硬上限 **10,000 个 key**；每 5 分钟清理过期记录；超限时淘汰最旧 key（不得无限增长，修复审计第 13 项的内存泄漏）。
- 审计的 `ip_address` 与限流键使用同一提取逻辑（修复审计“同 bug 波及审计日志”）。

### 2.6 CSRF 策略

**决策**：v0.2.0 不引入独立 CSRF token，CSRF 防护由两层组成：

1. `SameSite=Lax` Cookie：跨站 POST 不携带 Cookie；
2. 状态变更请求（POST/PUT/DELETE/PATCH）校验 `Origin` / `Referer`：为空（非浏览器客户端）或与请求 `Host` 同源时放行；否则 403。

理由：同源 SPA 无合法跨站写场景；独立 CSRF token 增加前后端双份状态与轮换逻辑，收益有限。若后续开放跨站 API 调用或个人 API Token，再补 CSRF token（§14）。

### 2.7 首次初始化与启动拒绝（M1）

- 安装器（M7）负责生成随机值：管理员密码、Server 密钥、入网 token 均为安装时随机生成；安装器不得再以 `wakehub123` 为默认密码。
- 服务端启动时**拒绝占位值**（M1）：以下任一环境变量等于占位值时，服务直接 fatal 退出，拒绝启动：
  - `WAKEHUB_ADMIN_PASSWORD` ∈ {`wakehub123`}
  - `WAKEHUB_SECRET_KEY` ∈ {`change-me-in-production-wakehub-secret`、`replace-with-strong-secret-key`、`replace-with-random-secret-key`}
  - `WAKEHUB_AGENT_ENROLL_TOKEN` ∈ {`local-agent-secret-token`、`replace-with-random-agent-token`}
  （三处互不一致的占位值全覆盖，见审计 M1 与 D 报告 §1。）
- `users` 表为空时才用环境变量种子管理员；表非空时环境变量里的密码被忽略（现状如此，保持）——因此**已部署的老库不受启动拒绝影响**，改密走 §2.4 接口。
- 种子用户名同样受 §2.3 的字符集校验。

---

## 3. Agent 身份与 WS 协议（M2）

### 3.1 设计决策：每 Agent 独立 token + 入网 token，两条入网路径

**现状问题**（审计第 2 项，P0）：全部 Agent 共用一个全局 `WAKEHUB_AGENT_TOKEN`，身份完全由客户端自报的 `X-Agent-ID` 决定，持全局 token 可冒认任意 Agent（含内置 `local`）；`agent_tokens` 表建了却零代码引用。

**v0.2.0 模型**：

| 凭证 | 用途 | 存储 | 生命周期 |
|---|---|---|---|
| per-agent token | Agent 日常连接的唯一身份凭证，每个 Agent 独立 | 服务端只存 SHA-256 哈希（`agent_tokens.token_hash`）；明文只在签发时返回一次 | 可单独吊销 / 重新签发；吊销只影响该 Agent |
| 入网 token（enrollment token） | 新 Agent 首次自助注册时证明“我是被允许入网的设备” | 服务端环境变量 `WAKEHUB_AGENT_ENROLL_TOKEN`（install.sh 随机生成进 `.env`）；Agent 侧只在首次注册时使用 | 泄露后须在服务端轮换（改 `.env` 并重启 server）；**不能用于日常连接** |

入网分两条路径（两者都产生同一状态机，见 §3.2）：

- **路径 A（管理员预签发，主推）**：管理员在 UI/API 创建 Agent（§3.4 `POST /api/v1/agents`）→ 服务端生成 per-agent token，明文一次性返回 → 运维把 token 写进 Agent 的 `WAKEHUB_AGENT_TOKEN` → Agent 连接即 `connected`。确定性最高，适合固定节点。
- **路径 B（自助注册 + 审批）**：Agent 持入网 token 连接 → 服务端建 pending 行，WS 进入“待审批模式”（只许心跳，不下发任务）→ 管理员在 UI 审批 → 服务端签发 per-agent token，经 WS 推送给 Agent（§3.5），Agent 持久化后重连。适合现场部署、批量加节点。

### 3.2 Agent 注册审批状态机

API 对外暴露的 `status` 是**审批状态与连接状态的合并值**，派生规则（按优先级）：

```
disabled（approval_status=disabled）
  → pending（approval_status=pending）
  → connected（approved 且当前有 live WS 会话）
  → approved（approved 且当前离线）
```

存储层（迁移 003，见 `docs/migration-002-003.md`）：

- `agents.approval_status`：`pending | approved | disabled`（新增列；`authorized` 列保留为派生镜像，标记废弃，见迁移文档）。
- `agents.status`：`online | offline`（沿用现状，由会话与心跳维护）。
- 状态转移（全部写审计）：

```
            首次自助注册（入网 token）
  （无） ──────────────────────────────────► pending ──approve──► approved ──WS 连接──► connected
     ▲                                       │  ▲                   │  ▲                 │  │
     │                                       │  │                   │  │                 │  ▼ 掉线/心跳超时
     │                                       │  │                   │  │                 └────────► approved
     │                                  reject│  │ re-enroll         │  │ disable
     └──────── delete（行删除）◄──────────────┘  │                   │  ▼
                                             pending ◄──────────────┘
                                                        disabled ──enable──► approved
```

- `reject`：删除 pending 行（不保留 `rejected` 状态，减少状态膨胀；驳回即删除，审计留痕）。
- `revoke-token`：只吊销 token，不改审批状态；在线会话立即断开，Agent 变 `approved`（离线）；重新签发后可再连。
- `disable`：审批状态变 `disabled`，在线会话立即断开；`enable` 恢复为 `approved`（token 不变；若 token 已被吊销，须重新签发）。
- **重入网（token 丢失）**：已获批的 Agent 持入网 token 再次连接 → 旧 per-agent token 作废，Agent 回到 `pending`，须重新审批。这是 token 丢失后的唯一恢复路径（管理员必须重新审批，防止冒认者静默接管）。
- `local` 冒认（修复 B-N8）：`local` 是保留 ID，**任何远程 Agent 不得以 `X-Agent-ID: local` 注册**；握手时直接 403 `agent_id_mismatch`。内置 `local` 种子行由迁移 003 删除（§3.6）。

### 3.3 per-agent token 规范

- 明文格式：`wha_` + `base64url(32 字节随机数)`（约 47 字符）。前缀 `wha_` 用于与管理员会话 token、入网 token 相区分，日志与报错中只允许出现前缀 + 后 4 位。
- 服务端只存 `SHA-256(明文 token)` 的 hex（`agent_tokens.token_hash`，沿用现状列名）；**永不存明文**。
- 每 Agent 同时只有**一个有效 token**（`agent_tokens.agent_id UNIQUE` 保持不变）。
- 校验：查哈希 → 恒定时间比较（修复 B-N7，`subtle.ConstantTimeCompare`）→ 查 `revoked=0` → 查 Agent 审批状态 → 查 token 绑定的 `agent_id` 与 `X-Agent-ID` 一致（不一致 → 403 `agent_id_mismatch`）。
- **重新签发 = 轮换**（决策）：v0.2.0 只暴露一个端点 `POST /api/v1/agents/{id}/reissue-token`（§3.4）。重新签发是原子替换：新哈希写入同行，旧 token 立即失效，无宽限期。理由：单 token 存储无法实现“新旧并存宽限期”；带宽限期的多 token 轮换需 `agent_tokens` 支持多行，是后续版本的事（§14）。任务书的“重新签发”与“凭证轮换”在 v0.2.0 由同一操作满足。
- token 泄露隔离（验收标准）：吊销 A 的 token 后，B 的连接、任务下发、扫描均不受影响（M2 验收用例）。

### 3.4 Agent 管理 REST 接口（M2，全部须登录）

#### GET /api/v1/agents

返回全部 Agent（按 `created_at ASC`，沿用现状顺序；v0.2.0 不分页，节点数少）：

```json
{
  "code": 0, "message": "ok",
  "data": [
    {
      "id": "agent-home-lan",
      "name": "Home-HostNetwork-Agent",
      "subnet": "192.168.1.1/24",
      "allowed_subnets": ["192.168.1.0/24"],
      "status": "connected",
      "authorized": true,
      "ip": "192.168.1.65",
      "os": "linux",
      "arch": "amd64",
      "version": "0.2.0",
      "last_seen_at": "2026-11-05T22:45:22Z",
      "last_heartbeat_at": "2026-11-05T22:45:20Z",
      "created_at": "2026-11-05T22:38:00Z"
    }
  ]
}
```

- `status`：§3.2 的合并值 `pending | approved | connected | disabled`。
- `authorized`：保留字段，派生值（`approval_status=approved` 时为 true），标记废弃，仅供过渡期旧客户端读取；M6 不得依赖它。
- `ip` / `os` / `arch`：握手时由 Agent 上报（§3.5），未上报为空字符串。
- `allowed_subnets`：该 Agent 被允许扫描的网段白名单（§7.2；默认含注册时上报的 subnet）。
- `last_heartbeat_at`：最近一次应用层心跳；`last_seen_at`：最近一次任意 WS 活动（含连接建立）。

#### POST /api/v1/agents（预签发，路径 A）

请求：

```json
{ "agent_id": "agent-home-lan", "name": "Home-HostNetwork-Agent", "subnet": "192.168.1.1/24" }
```

- `agent_id` 可选；省略时服务端生成 `agent-` + 8 位随机小写字母数字。提供时须匹配 `^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`，且不得为保留值 `local`（否则 400 `invalid_value`）。
- `name` 必填（1–64 字符）；`subnet` 可选，须为合法 CIDR（否则 400 `invalid_format`）。
- 已存在同 ID → 409 `conflict`。
- 成功：**201**，创建时直接为 `approved`（管理员主动创建即视为已审批，不经过 pending），并一次性返回明文 token：

```json
{
  "code": 0, "message": "created",
  "data": {
    "agent": { "id": "agent-home-lan", "name": "Home-HostNetwork-Agent", "status": "approved", "...": "..." },
    "token": "wha_7f3a9c...（仅此一次返回）"
  }
}
```

- UI 必须把 token 以“仅显示一次、可复制”的方式突出展示，并提示运维写入 Agent 的 `WAKEHUB_AGENT_TOKEN`。服务端**永不再次返回**该 token 明文。
- 写 `agent_register` 审计（actor 为管理员，detail 含 agent id，不含 token）。

#### GET /api/v1/agents/{id}

返回单个 Agent（同 §3.4 列表项结构）。不存在 → 404。

#### POST /api/v1/agents/{id}/approve（审批通过）

- 仅 `pending` 可审批；非 pending → 400 `invalid_value`（message 注明当前状态）。
- 效果：`approval_status` → `approved`，签发新的 per-agent token（旧 token 若存在则作废）。
- 响应 201（创建了新凭证，语义同签发）：

```json
{ "code": 0, "message": "ok", "data": { "agent": { "...": "..." }, "token": "wha_...（仅此一次返回）", "delivered_via": "http" } }
```

- 若该 Agent 当前正以 pending 模式连接着，服务端**同时**经 WS 推送 `registration_approved`（§3.5），`delivered_via` 为 `"websocket+http"`；Agent 收到后自动持久化并重连。
- 写 `agent_approve` 审计。

#### POST /api/v1/agents/{id}/reject（驳回）

- 仅 `pending` 可驳回。效果：删除该 Agent 行（含 token 与白名单，FK 级联）；若正以 pending 模式连接，服务端先发 `registration_rejected` 再以 4403 关闭 WS。
- 响应：`{ "code": 0, "message": "deleted", "data": null }`。写 `agent_reject` 审计。

#### POST /api/v1/agents/{id}/revoke-token（吊销 token）

- 吊销当前 per-agent token（`revoked=1`）；若 Agent 在线，服务端立即以 4403 关闭其 WS。
- 审批状态不变。响应：`{ "code": 0, "message": "ok", "data": { "agent_id": "...", "revoked": true } }`。写 `agent_revoke` 审计。
- 吊销后该 Agent 须经 `reissue-token` 重新签发才能再连（或走路径 B 重入网）。

#### POST /api/v1/agents/{id}/reissue-token（重新签发，即轮换）

- 生成新 token，原子替换旧哈希（旧 token 立即失效，无宽限期，见 §3.3 决策）；若 Agent 在线，服务端关闭其旧会话（新 token 生效后重连）。
- 响应 201：`{ "code": 0, "message": "ok", "data": { "agent_id": "...", "token": "wha_...（仅此一次返回）" } }`。写 `agent_reissue` 审计。

#### POST /api/v1/agents/{id}/disable · POST /api/v1/agents/{id}/enable

- `disable`：`approval_status` → `disabled`，在线会话以 4403 立即断开；其设备仍保留关联，唤醒等操作返回 502 `agent_disabled`。写 `agent_disable` 审计。
- `enable`：`disabled` → `approved`（token 不变；若 token 已被吊销，须另行重新签发）。写 `agent_enable` 审计。
- 非法转移（如对 `pending` 执行 disable）→ 400 `invalid_value`。

#### DELETE /api/v1/agents/{id}

- 删除 Agent 行、token、白名单（FK 级联）。其设备**不删除**，`agent_id` 原样保留；后续对这些设备的操作返回 502 `agent_not_found`（设备详情页须提示“所属 Agent 已删除，请重新分配”，M6）。
- 响应：`{ "code": 0, "message": "deleted", "data": null }`。写 `agent_delete` 审计（v0.1.0 无此事件，新增）。

#### POST /api/v1/agents/{id}/subnets · DELETE /api/v1/agents/{id}/subnets

管理扫描白名单（§7.2）。请求：`{ "cidr": "192.168.1.0/24" }`。

- `cidr` 须为私有 IPv4 CIDR 且掩码 ≥ /24（与扫描校验同一规则）；否则 400 `invalid_cidr`。
- POST 已存在 → 409 `conflict`；DELETE 不存在 → 404。成功返回更新后的 `allowed_subnets` 数组。
- 注册时上报的 `subnet` 会自动写入白名单（迁移 003 对既有行同样回填）。

### 3.5 WS 握手与消息协议

#### 端点与握手

- 端点不变：`GET /api/v1/ws/agent`（不设 Cookie 认证，Agent 是非浏览器客户端）。
- Agent → Server 请求头：

| 头 | 必填 | 说明 |
|---|---|---|
| `X-Agent-Token` | 是 | per-agent token（日常）或入网 token（首次注册/重入网） |
| `X-Agent-ID` | 是 | 与 token 绑定的 Agent ID；`local` 保留，远程使用直接 403 |
| `X-Agent-Name` | 否 | 展示名（注册时写入；已存在行不覆盖已有 name） |
| `X-Agent-Subnet` | 否 | Agent 所在网段 CIDR（注册时写入白名单） |
| `X-Agent-Version` | 否 | Agent 版本（如 `0.2.0`） |
| `X-Agent-OS` / `X-Agent-Arch` | 否 | 如 `linux` / `amd64` |

- 服务端按 token 类型分流：
  1. **per-agent token**：验哈希（恒定时间）→ 未吊销 → 审批状态为 `approved` → `agent_id` 绑定一致 → 101 升级，注册会话（替换旧连接；旧连接关闭不得误删新会话，M2 竞态修复的验收点）。
  2. **入网 token**：验入网密钥（恒定时间）→ 新 ID 则建 pending 行（101 升级，进待审批模式）；已存在且为 `approved` 的 ID 则回退为 pending 并作废旧 token（§3.2 重入网）；已存在且为 `disabled` 的 ID → 403。
- 握手失败（升级前）：HTTP 状态 + JSON 信封（不用纯文本 `http.Error`，便于 Agent 与排障）：

| 场景 | HTTP | body `message` / code |
|---|---|---|
| 缺 `X-Agent-Token` 或 `X-Agent-ID` | 400 | `缺少 Agent 身份凭证` |
| token 无效 | 401 | `Agent 凭证无效` |
| token 已吊销 | 403 | `Agent 凭证已被吊销`（code `token_revoked`） |
| Agent 未获批（pending 持 per-agent token） | 403 | `Agent 尚未获批`（code `agent_not_approved`） |
| token 与 `X-Agent-ID` 不绑定 / 冒认 `local` | 403 | `Agent 身份与凭证不匹配`（code `agent_id_mismatch`） |
| Agent 已禁用 | 403 | `Agent 已被禁用`（code `agent_disabled`） |

- **Origin 策略**（修复审计第 3 项 `CheckOrigin` 恒 true）：`Origin` 为空 → 放行（非浏览器客户端，如 Agent）；`Origin` 非空（浏览器）→ 仅当 Origin 的 host 与请求 `Host` 相同，或在 `WAKEHUB_WS_ALLOWED_ORIGINS`（逗号分隔的完整 origin，如 `https://wake.example.com`）白名单内时放行，否则 403。**Origin 校验不是身份认证**：通过 Origin 校验的请求仍须持有效 token。
- **WSS / TLS**（审计第 5 项）：Agent 连接 `wss://` 时**必须验证服务端证书**，禁止默认跳过验证（`InsecureSkipVerify` 默认为 false；仅当显式配置 `WAKEHUB_AGENT_TLS_SKIP_VERIFY=1` 时才跳过，且启动日志须输出警告）。v0.2.0 的 TLS 终止由反向代理承担（部署文档 M7 写明）；Server 本身直出 TLS 不在 v0.2.0 范围（§14）。

#### 消息协议（`Message` 结构不变）

```json
{ "id": "uuid", "type": "wake", "payload": { "...": "..." } }
```

- `id`：每次任务唯一 UUID；Agent 回包 `result` 必须原样回传同一 `id`。
- 现有类型保持：`heartbeat`、`heartbeat_ack`、`wake`、`check`、`scan`、`sleep`、`result`。payload 结构不变（`WakePayload` / `CheckPayload` / `ScanPayload` / `SleepPayload` / `ResultPayload`，见 `hub.go`）。
- 新增类型：

| 类型 | 方向 | payload | 说明 |
|---|---|---|---|
| `registration_pending` | Server → Agent | `{ "agent_id": "...", "status": "pending", "message": "等待管理员审批" }` | pending 模式建立后服务端首条消息 |
| `registration_approved` | Server → Agent | `{ "agent_id": "...", "token": "wha_...（仅此一次）" }` | 审批通过时推送；Agent 须持久化后重连 |
| `registration_rejected` | Server → Agent | `{ "agent_id": "...", "message": "注册已被驳回" }` | 驳回时推送，随后服务端以 4403 关闭 |
| `cancel` | Server → Agent | `{ "task_id": "<待取消任务的 id>" }` | 取消在途任务（扫描/批量唤醒，M3/M4）；Agent 尽力中止并回 `result{success:false, error:"cancelled"}` |

- **去重 / 防重复执行**（审计第 5 项）：Agent 维护最近 5 分钟内已处理任务 id 的集合（上限 1000 条，超限淘汰最旧）；重复 id 的任务直接回放缓存结果，不重复执行。
- **消息上限**：双向 `SetReadLimit(1 MiB)`；超限服务端关闭连接（1009）。
- **心跳与僵死清理**（审计第 5、14 项）：
  - 应用层心跳：Agent 每 15s 发 `heartbeat`，服务端回 `heartbeat_ack` 并更新 `last_heartbeat_at`；服务端 45s 未收到心跳即判定僵死，关闭连接、置 offline、写 `agent_disconnect` 审计。
  - WS 层：服务端每 30s 发 Ping；Agent 须回 Pong（gorilla 默认处理）；服务端读超时 45s。
  - Agent 侧：45s 未收到任何服务端消息（ack/任务/Ping）即判定连接已死，主动重连。
- **重连**：指数退避 + 随机抖动：首次 1s，翻倍至上限 60s，抖动 ±25%；连接成功后复位。固定 5s 重连废止（审计第 5、14 项）。
- **任务执行**：每连接独立子 ctx；任务并发上限 **8**（超限的任务直接回 `result{success:false, error:"agent busy"}`）；任务结果发送须监听 ctx，连接断开时不得永久阻塞（修复审计第 14 项的 `sendChan` 泄漏）；服务端任务等待上限与 Agent 执行上限分开配置（v0.2.9 问题 6）：服务端默认 15s（`WAKEHUB_SERVER_TASK_TIMEOUT`）、Agent 默认 10s（`WAKEHUB_AGENT_TASK_TIMEOUT`），服务端应比 Agent 长（建议至少 +5s），否则回包可能在到达前被取消；服务端取消须向 Agent 传播 `cancel`（§5.2 批量取消即走此通道），且只发给实际承载任务的会话。
- **WS 关闭码**：1000 正常；4401 未授权；4403 禁止（吊销/禁用/驳回/未获批）；1009 消息超限。

### 3.6 内置 `local` 节点的退役

**决策**：v0.2.0 删除“进程内本地执行”与种子 `local` Agent 行。

理由：

- `local` 是三重含义混淆的根源：种子行 ID、设备默认 `agent_id`、hub 的进程内执行短路（审计第 7 项、B-N8）。三处默认值互不一致（`local` / `agent-home-lan` / `agent-lan-01`），且远程 Agent 可冒认 `local` 投毒 Agents 列表。
- 标准安装（模式 1）本来就包含 host 网络的 local agent 容器（`agent-home-lan`），它就是一个普通 Agent；进程内直发没有不可替代的价值，却让 Server 容器直接获得发包能力、模糊了网络边界（审计第 19 项要求 Server 与 Agent 网络能力明确分离）。
- 迁移 003 删除种子行；`hub.Dispatch` 的 `""` / `"local"` 短路删除；全部任务必须经注册 Agent 执行。
- 单二进制开发模式（`make run-server` 无 Agent）下唤醒不可用——这是有意的：开发时用 `make run-agent` 另起 Agent（README 的本地开发节已是如此）。
- 遗留设备行（`agent_id='local'` 或 `''`）的处理见 §4.6 与迁移文档 §4.4。

---

## 4. 设备（M3）

### 4.1 设备模型（v0.2.0）

沿用 v0.1.0 的 `Device` 字段，变化如下：

| 字段 | 变化 |
|---|---|
| `agent_id` | **必填，无默认值**。空值创建时 400 `required`。`local` 不再是合法值（引用不存在的 Agent → `not_found`） |
| `mac` | 校验不变（严格正则，规范化为大写冒号分隔）；**新增 MAC 唯一约束**：同一 MAC 已存在 → 409 `conflict`（M3 在验证层强制；DB 唯一索引由迁移 005 补，见附录 B） |
| `ip` | 可为空；提供时必须为合法 IPv4（`net.ParseIP` 且 `To4() != nil`），否则 400 `invalid_format` |
| `broadcast_address` | 可为空（默认 `255.255.255.255`）；提供时必须为合法 IPv4 |
| `wol_port` / `status_port` | 1–65535，越界 → 400 `out_of_range`（**不再静默钳制**，修复 B-N11；默认值 wol 9 / status 22 保持） |
| `device_type` | 枚举：`pc | server | nas | raspberry_pi | tv_box | router | other`；非法值 → 400 `invalid_value`。既有行若存有枚举外的值，原样返回、UI 按 `other` 渲染 |
| `status_method` | 枚举：`ping | tcp | arp`；非法值 → 400 `invalid_value` |
| `sleep_type` | 枚举：`none | http_sol`；非法值 → 400 `invalid_value` |
| `sleep_endpoint` | `sleep_type=http_sol` 时必填，须符合 §12 的 allowlist；`none` 时可为空 |
| `group_id` | 须真实存在（默认 `default`）；不存在 → 400 `not_found` |
| `enabled` | 语义不变（`*bool`，未传默认启用） |
| `remark` | 可选，最长 500 字符，超限 → 400 `out_of_range` |
| `name` | 必填，trim 后 1–64 字符 |

### 4.2 统一验证层（M3）

- 新增/修改共用同一验证函数（M3 落在 `internal/device/validate.go` 或 repository 内同名函数，由 M3 定；契约只定行为）。
- 校验顺序：必填 → 格式/范围/枚举 → 引用存在性（Agent/Group）→ 业务规则（Agent 已获批、MAC 唯一）。
- Agent 校验：
  - 不存在 → 400 `errors: [{field: "agent_id", code: "not_found"}]`。
  - 存在但 `pending` / `disabled` → 400 `code: "not_authorized"`（pending）/ `"agent_disabled"`（disabled）。
  - 存在且已获批但**当前离线** → **不是校验错误**：创建/更新放行，但响应中附加 `warnings`（见 §4.3），前端须明确提示“目标 Agent 当前离线，唤醒将失败”。
- 校验失败一律返回 §1.2 的结构化错误，`message` 为 `"设备信息校验失败"`，`data.errors` 按字段顺序排列。
- 校验错误与 DB 约束保持一致：验证层通过但 DB 拒绝（如竞态下 MAC 重复）时，同样映射为 409 `conflict`，不许把原始 DB 错误文本返回客户端。

### 4.3 设备 CRUD 接口

#### GET /api/v1/devices

- 查询参数：`group_id`（现状）、`q`（现状，名称/IP/MAC 模糊）、`status`（新增：`online | offline | unknown | checking`）、`agent_id`（新增）、`page` / `page_size`（§1.4）。
- 响应改为分页信封（破坏性变更，§0.1）：

```json
{ "code": 0, "message": "ok", "data": { "items": [ { "id": "...", "name": "LAN-29", "mac": "AA:BB:CC:DD:EE:01", "ip": "192.168.1.29", "agent_id": "agent-home-lan", "status": "online", "...": "..." } ], "total": 1, "page": 1, "page_size": 50 } }
```

#### POST /api/v1/devices

- 请求体为设备对象（`id`/`status`/`created_at` 等服务端字段由服务端生成或忽略，客户端传了也无效）。
- 成功：201 + 创建后的完整设备行（含服务端生成的 `id`、`status: "unknown"`、`created_at`）。
- Agent 离线警告：成功响应中附加 `warnings`（`data` 仍为设备对象时，`warnings` 放顶层信封外？——决策：`warnings` 放在 `data` 同级的信封字段外不破坏结构，改为放在响应体顶层新增字段 `warnings`）：

```json
{ "code": 0, "message": "created", "data": { "...": "..." }, "warnings": [{ "code": "agent_offline", "message": "目标 Agent agent-home-lan 当前离线，设备已创建但暂时无法唤醒" }] }
```

（信封新增可选 `warnings` 数组，无警告时省略。同样适用于更新接口。）

- 失败：校验错误 → 400 + §1.2 结构；MAC 重复 → 409。

#### GET /api/v1/devices/{id}

- 不变；不存在 → 404 `"设备不存在"`（现状已是 404，保持）。

#### PUT /api/v1/devices/{id}

- **部分更新语义**（修复任务书 §25“编辑时不能意外丢失未修改字段”）：只更新请求体中**出现**的字段，未出现的字段保持原值。实现方式：服务端按 JSON key 存在性合并（`enabled` 等指针字段沿用现状做法；显式传空字符串表示清空该字段，如 `sleep_endpoint: ""`）。
- 成功：200 + **重查后的数据库行**（修复 B-N4，不许返回请求体）；不存在 → **404**（修复 B-N5）。
- 失败：校验错误 → 400 + §1.2 结构；MAC 与他人重复 → 409。
- 更新时 `agent_id` 同样走 §4.2 的 Agent 校验；Agent 离线同样附加 `warnings`。

#### DELETE /api/v1/devices/{id}

- 不存在 → **404**（修复 B-N5）。成功 → 200 `"deleted"`。写 `device_delete` 审计。

### 4.4 统一操作校验（M3，第 15 项）

单设备唤醒/检测/睡眠、批量唤醒、定时任务执行**共用同一校验链**（M3 抽为共享函数），按顺序检查，任一步失败即按表返回，不再各入口各行其是：

| 检查 | 失败时的行为 |
|---|---|
| 设备存在 | 404 `not_found`（单设备操作）；批量/定时中该项记 `skipped`，reason `device_not_found` |
| 设备启用 | 单设备操作 → 400 `device_disabled`；批量/定时 → `skipped`，reason `device_disabled`（**统一为跳过**，修复 v0.1.0 单设备允许唤醒禁用设备、批量跳过的分裂行为） |
| Agent 存在 | 502 `agent_not_found`（单设备）；批量/定时 → `failed`，reason `agent_not_found` |
| Agent 已获批 | 502 `agent_not_approved` / `agent_disabled`；批量/定时 → `failed`，同 reason |
| Agent 在线 | 502 `agent_offline`；批量/定时 → `failed`，reason `agent_offline` |
| 设备属于该 Agent | 由 `agent_id` 字段天然保证；跨 Agent 调用不存在（dispatch 只按设备归属 Agent 发） |
| 操作类型允许 | 睡眠 `action` 白名单（§12）；定时 `action` 白名单（§8.1） |

### 4.5 设备状态检测接口

#### POST /api/v1/devices/{id}/check

- 校验链同 §4.4；设备不存在 → 404；Agent 离线 → 502 `agent_offline`。
- 成功：200 + 检测结果（`status.Result`，现状字段保持，M3 补齐语义）：

```json
{ "code": 0, "message": "ok", "data": { "state": "online", "method": "ping", "checked_at": "2026-11-05T22:45:22Z", "latency_ms": 12, "error_reason": "" } }
```

- `state` ∈ `online | offline | unknown | checking`；**`checking` 在 v0.2.0 的同步检测中不出现**（检测是同步完成的；`checking` 保留给设备行的过渡状态与未来异步检测，见 §14）。
- 检测异常（Agent 返回错误、解析失败）→ **502，不写脏状态入库**（修复 B-N2：解析失败时不得把 `status` 写成空字符串）；检测结果为 `offline`/`unknown` 时照常入库并返回 200（检测出离线是正常结果，不是接口失败）。
- `TCP 端口关闭 ≠ 设备关机`（审计第 10 项）：TCP 检测端口关闭时，`state` 为 `unknown` + `error_reason` 注明“端口关闭，无法据此判定关机”，不得记 `offline`。
- 检测须写 `device_check` 审计（v0.1.0 缺失，M5 补）。

### 4.6 遗留数据：`agent_id='local'` / `''` 的设备

- 迁移 003 删除种子 `local` 行，但**不改写** `devices.agent_id`（见迁移文档 §4.4）。
- v0.2.0 运行时对此类设备的处理：
  - 列表/详情正常返回，`agent_id` 原样返回；前端据 Agent 列表查不到对应行时显示“未分配 Agent”，并引导重新分配（M6）。
  - 唤醒/检测/睡眠/批量/定时：校验链在“Agent 存在”一步失败 → 单设备 502 `agent_not_found`；批量记 `failed`，reason `agent_not_found`。
  - 更新设备时若 `agent_id` 仍为 `local`/空 → 按 §4.2 校验失败（`required` / `not_found`），强制重新选择。
- 该状态是**过渡态**，不是合法态；升级文档须写明手工修复路径（设备编辑页重选 Agent）。

---

## 5. 唤醒（M3）

### 5.1 核心语义：`sent` ≠ `booted`

v0.2.0 全面区分两个成功概念（任务书第 8 项硬性要求）：

- **魔术包发送成功（`sent`）**：Agent 已把 WOL 魔术包写出到目标广播域。这是唤醒接口能直接证明的唯一事实。
- **设备实际启动成功（`booted`）**：发包后在限定时间内检测到设备上线（ping/配置的检测方式回 `online`）。这需要**回检**，不是发包成功的推论。

任何接口、响应字段、审计文案、UI 文案都不得把 `sent` 写成“设备已启动 / 已唤醒”。UI 必须把两者渲染为两个独立状态（M6）。

### 5.2 单设备唤醒

#### POST /api/v1/devices/{id}/wake

- 校验链同 §4.4；设备不存在 → 404；禁用 → 400 `device_disabled`；Agent 离线 → 502 `agent_offline`。
- 请求体可选：`{ "verify": true, "verify_timeout_seconds": 30 }`（两者都省略时 `verify=false`）。
  - `verify_timeout_seconds`：1–120，默认 30；仅 `verify=true` 时有效。
- `verify=false`（默认）：Agent 回包成功即返回：

```json
{ "code": 0, "message": "WOL 魔术包已发送", "data": { "device_id": "...", "mac": "AA:BB:CC:DD:EE:01", "send_status": "sent", "sent_at": "2026-11-05T22:45:22Z", "boot_status": "pending", "boot_checked_at": null } }
```

- `verify=true`：发包后服务端轮询检测（复用设备的 `status_method`，间隔 2s，上限为 `verify_timeout_seconds`），再返回：

```json
{ "code": 0, "message": "WOL 魔术包已发送，设备已上线", "data": { "device_id": "...", "mac": "AA:BB:CC:DD:EE:01", "send_status": "sent", "sent_at": "2026-11-05T22:45:22Z", "boot_status": "online", "boot_checked_at": "2026-11-05T22:45:31Z" } }
```

- `boot_status` 枚举：`pending`（未回检）、`online`（回检到上线）、`timeout`（限时内未上线）、`unknown`（回检失败，如检测方式不可用）。`boot_status=timeout` 时 HTTP 仍为 200（发包本身成功），`message` 须写明“魔术包已发送，但限时内未检测到设备上线”。
- 发包失败（dispatch 失败）→ 502，写 `device_wake_failed` 审计（沿用现状）；成功写 `device_wake` 审计，detail 须写 `"发送 WOL 魔术包至 <MAC>"`（沿用现状文案，不得写成“设备已唤醒”）。
- `wake_tasks` 行：`status` 只记录发包结果（`success`/`failed`）；回检结果不改写该行（回检结论只出现在本次响应与设备 `status` 字段里）。

### 5.3 批量唤醒

#### POST /api/v1/devices/batch-wake

请求：

```json
{ "device_ids": ["id-1", "id-2", "id-3"], "verify": false, "verify_timeout_seconds": 30 }
```

- `device_ids`：必填，非空数组，上限 **100** 台（超限 → 400 `out_of_range`）；重复 ID 只执行一次，第二次出现记 `skipped`，reason `duplicate`。
- `verify` 语义同 §5.2；`verify=true` 时批量整体仍为同步接口（见下文超时）。
- 执行模型：并发上限 **10**（信号量）；每设备 dispatch 超时 10s（`TaskTimeout`）；**整体上限 120s**（超限后未完成项记 `failed`，reason `timeout`，并返回已完成部分——不得丢弃部分结果）。
- 取消：客户端断开即取消剩余任务（ctx 传播）；v0.2.0 **不设单独的批量取消端点**（同步接口，断开即取消，见 §14）。
- 响应（200，即使部分失败——批量接口的 HTTP 200 表示“批量执行完毕”，成败看 `results`）：

```json
{
  "code": 0,
  "message": "批量唤醒执行完毕",
  "data": {
    "batch_id": "b3e1a7c2-...",
    "total": 3,
    "success": 2,
    "failed": 0,
    "skipped": 1,
    "results": [
      { "device_id": "id-1", "name": "LAN-29", "mac": "AA:BB:CC:DD:EE:01", "status": "sent", "reason": "", "sent_at": "2026-11-05T22:45:22Z", "boot_status": "pending", "boot_checked_at": null },
      { "device_id": "id-2", "name": "PC-02", "mac": "AA:BB:CC:DD:EE:FF", "status": "skipped", "reason": "device_disabled", "sent_at": null, "boot_status": null, "boot_checked_at": null },
      { "device_id": "id-3", "name": "NAS", "mac": "11:22:33:44:55:66", "status": "failed", "reason": "agent_offline", "sent_at": null, "boot_status": null, "boot_checked_at": null }
    ],
    "started_at": "2026-11-05T22:45:20Z",
    "finished_at": "2026-11-05T22:45:23Z",
    "duration_ms": 3120
  }
}
```

- 字段定义：
  - `batch_id`：本次请求的 UUID。**仅为本次请求的关联 ID**（返回给客户端、写入审计 detail）；v0.2.0 不设批量任务持久化表（`wake_tasks` 仍按每设备一行记录，`trigger_type=batch`）。批量历史查询顺延（§14）。
  - `success`：魔术包成功发出数（**定义为 `sent` 数，不是“已启动”数**；UI 必须渲染为“已发送”，见 §5.1）。
  - `failed`：发包失败数（Agent 离线、MAC 错误、dispatch 失败、超时）。
  - `skipped`：未执行数（设备不存在、已禁用、重复 ID）。
  - 每设备 `status` ∈ `sent | failed | skipped`；`reason` 为空字符串或 §1.2 的 reason 码（`device_not_found` / `device_disabled` / `duplicate` / `agent_offline` / `agent_not_found` / `agent_disabled` / `invalid_mac` / 具体 dispatch 错误文本——dispatch 错误文本原样进 `reason`，但不得含敏感信息）。
  - `boot_status` 仅 `verify=true` 且 `status=sent` 时回检填值；其余为 `null`（`status=skipped/failed` 时）或 `"pending"`（`verify=false` 时）。
  - `started_at` / `finished_at`：RFC 3339 UTC；`duration_ms`：整数毫秒。
- 审计：写一条 `device_batch_wake` 聚合记录（沿用现状），detail 须含 `batch_id` 与 `成功/失败/跳过` 计数；每设备发包成功仍各写 `wake_tasks` 行（沿用现状）。

---

## 6. 睡眠 / 关机（M3）

#### POST /api/v1/devices/{id}/sleep

- 校验链同 §4.4（设备不存在 → 404；禁用 → 400 `device_disabled`；Agent 离线 → 502）。
- 请求体：`{ "action": "sleep" }`，`action` ∈ `shutdown | sleep | hibernate`（白名单，现状 `ValidateAction` 保持）。**Body 解码失败必须返回 400，禁止默认执行**（修复 B-N1：v0.1.0 畸形 JSON 会默认执行睡眠）。
- 响应：200 + `{ "code": 0, "message": "远程关机/睡眠指令已执行", "data": null }`；Agent 执行失败 → 502（v0.1.0 此处误用 400，M3 统一为 502）。
- 审计：`device_sleep` / `device_sleep_failed`（沿用现状）。

### 6.1 Sleep Endpoint 安全约束（M3，SSRF）

设备 `sleep_endpoint` 的校验与执行约束（审计第 6 项属实部分）：

- 格式：`http://` 或 `https://` 开头；**优先 HTTPS**（http 允许但 UI 应提示“未加密”，M6）。
- 主机：必须为**私有 IPv4**（10/8、172.16/12、192.168/16）或**回环地址**（127.0.0.0/8；Sleep-on-LAN 服务可能跑在 Agent 本机上，回环不可路由出本机，攻击面窄于整个局域网段）；禁止公网地址、禁止 `169.254.0.0/16`（云元数据段）、禁止 URL 中带 userinfo（`http://user:pass@host`）。
- 端口：1–65535；未显式写端口时 http 默认 80、https 默认 443。
- 请求：`POST <endpoint>/<action>`，空 body，超时 5s（现状保持）；**禁止跟随重定向**（`CheckRedirect` 返回 `http.ErrUseLastResponse`，3xx 即视为失败；修复现状默认跟随 10 次的问题）。
- 适配器：仅 `http_sol`；`sleep_type=none` 时调用睡眠接口直接 400 `invalid_value`（“设备未配置受控远程关机适配器”）。
- Linux/Windows 本地关机适配器：v0.2.0 **不新增**（无 WakeHub 自有 sleep agent 可对接，见 §6.2）；仍只支持 `http_sol`。
- 审计：全部睡眠操作写审计（沿用现状）。

### 6.2 签名认证：v0.2.0 明确不做，预留格式

**决策**：任务书第 6 项要求的“签名认证”在 v0.2.0 **不实现**，只预留 wire 格式。

理由：签名需要对端（Sleep-on-LAN 服务）按同一算法验签；WakeHub 没有自有的 sleep agent 实现，市面上也没有统一的验签标准。单方面在请求头里加一个无人校验的 HMAC 是安全表演，不是安全机制；且签名密钥无处可存——每设备密钥需要新增 `devices.sleep_secret` 列，该列不在 M0 的 002/003 迁移范围内（002 认证、003 Agent），强行塞入会打乱迁移编号与模块分工。

预留格式（后续版本实现 sleep agent 时启用，届时不改 API）：

```
X-WakeHub-Signature: HMAC-SHA256_hex(sleep_secret, action + "\n" + timestamp)
X-WakeHub-Timestamp: <unix 秒>
```

- 时间偏差容忍 ±300s；密钥届时由迁移 004/005 的 `devices.sleep_secret` 列承载。
- v0.2.0 的睡眠安全由 §6.1 的 allowlist + 禁重定向 + 超时 + 审计承担；安全说明（M7）须如实写清“v0.2.0 睡眠端点无签名认证，仅限可信局域网内使用”。

### 6.3 SSH 适配器（v0.2.5）：兑现 §6.1 的延期项

§6.1 曾写“Linux/Windows 本地关机适配器：v0.2.0 **不新增**”。v0.2.5
新增其中 Linux 一半：SSH 关机适配器，目标上不装任何监听服务。
**v0.2.6 补上 Windows 一半**：设备字段 `ssh_os`（`linux` 默认 |
`windows`，迁移 007；非法值校验拒绝），Agent 按目标系统下发命令
（Windows OpenSSH 默认 shell 为 cmd.exe：`shutdown` → `shutdown /s /t 1`；
`sleep` → `start "" /b rundll32.exe powrprof.dll,SetSuspendState 0,1,0`；
`hibernate` → `start "" /b shutdown /h`；前台先 `where` 检查工具存在）。
Windows 目标须开启 OpenSSH Server（管理员 PowerShell 一次操作，
安装时自动放行防火墙、服务开机自启、无黑窗口）；SSH 用 Windows 登录
账户的密码（不是 PIN）。

- 适配器：`sleep_type` 增加 `ssh`（`none | http_sol | ssh`）；设备字段
  `ssh_user`（必填）、`ssh_port`（0 视为未填、默认 22；其余 1–65535）、
  `ssh_password`（写时字段：新建必填；编辑留空表示保持原密码）、
  `ssh_password_set`（只读）、`ssh_host_key_sha256`（TOFU 指纹，只读）。
  凭据密文列 `ssh_password_enc` 永不序列化返回。迁移 006。
- 凭据加密：AES-256-GCM，密钥 = SHA-256(`"wakehub-ssh-v1" + 0x00 + SECRET_KEY`)，
  格式 base64(nonce || ciphertext)；明文永不落库、不进响应、不进日志；
  `SECRET_KEY` 更换后旧密文无法解密，sleep 接口返回 400 提示重新填写保存。
- 主机密钥 TOFU：首次连接记录指纹（Agent 回传，服务端持久化），之后每次
  校验，不符即拒绝；改密码即清空指纹重新信任；离开 SSH 适配器即清空
  已存凭据与指纹（只保留用户名/端口）。
- 目标约束：主机必须是私有 IPv4（10/8、172.16/12、192.168/16）或回环
  （127.0.0.0/8），禁主机名、禁公网、禁 169.254.0.0/16；端口 1–65535。
  主机取设备行 `ip`，不接受客户端任意指定。
- 执行：`POST /devices/{id}/sleep` 校验链同 §6（动作白名单 → 适配器配置
  → §4.4）；服务端解密密码后经 Hub 下发 `sleep` 任务
  （`adapter_type:"ssh"` + `ssh_host/ssh_port/ssh_user/ssh_password/ssh_known_host_key/ssh_target_os`）；
  Agent 用 SSH（密码 + 键盘交互认证）直连目标，先前台检查关机工具存在，
  再以后台方式延迟 1 秒执行（Linux：`shutdown` → `systemctl poweroff || poweroff || shutdown -h now`；
  `sleep` → `systemctl suspend`；`hibernate` → `systemctl hibernate`。
  Windows：见上）。
  成功仅表示“指令已发起”，不等同于已断电（sent ≠ booted 同理）。
- 审计：`device_sleep` / `device_sleep_failed`（沿用现状）；Agent 执行失败 → 502。
- 限制：定时任务已支持 `sleep`（v0.3.2 起）；目标系统仅 linux/windows；
  密码经 server→Agent 的 WS 通道下发，跨不可信网络部署 Agent 时须用 `wss://`。

---

## 7. 局域网扫描（M4）

### 7.1 设计决策：异步扫描任务 + 轮询进度

**现状问题**（审计第 11、26 项）：`POST /scan` 是同步接口（12s 上下文），无进度、无取消、无导入接口；前端“导入”只是填表单。

**v0.2.0 模型**：扫描是异步任务。创建 → 返回 `scan_id` → 前端轮询 `GET /scan/{id}` 看进度 → 完成/取消后调导入接口。v0.2.0 **不用 WS/SSE 向浏览器推送进度**（轮询即可满足，间隔 1s；推送是后续优化，§14）。

扫描任务状态：`running | completed | failed | cancelled`。

### 7.2 扫描白名单

- 每 Agent 有 `allowed_subnets` 白名单（§3.4，表 `agent_allowed_subnets`，迁移 003）。
- 扫描的 CIDR 必须**等于白名单中某一项，或是其子网**（如白名单 `192.168.1.0/24`，可扫 `192.168.1.0/25`，不可扫 `192.168.1.0/16` 或 `192.168.1.0/24`）。
- 另须满足基础规则（现状保持）：私有 IPv4、掩码 ≥ /24（即地址数 ≤ 256）。
- 违反 → 400：CIDR 格式/范围问题用 `invalid_cidr`；不在白名单用 `cidr_not_allowed`。
- 注册时上报的 `subnet` 自动进白名单；管理员可经 §3.4 的 subnets 接口增删。

### 7.3 扫描接口

#### POST /api/v1/scan

请求：

```json
{ "agent_id": "agent-home-lan", "cidr": "192.168.1.0/24" }
```

- 校验：`agent_id` 必填（不再默认 `local`）；Agent 不存在 → 404；未获批/已禁用 → 400 `not_authorized` / `agent_disabled`；离线 → 502 `agent_offline`；CIDR 校验见 §7.2。
- 并发限制：**每 Agent 同时只能有 1 个进行中的扫描**，已有时返回 409 `scan_in_progress`（body 含进行中的 `scan_id`，前端可直接跳去轮询它）。
- 成功：**202**（异步创建，语义为“已接受”）：

```json
{ "code": 0, "message": "扫描任务已创建", "data": { "scan_id": "s-9f1c2a3e-...", "agent_id": "agent-home-lan", "cidr": "192.168.1.0/24", "status": "running", "started_at": "2026-11-05T22:45:22Z" } }
```

- 写 `network_scan` 审计（沿用现状）。

#### GET /api/v1/scan/{scan_id}

```json
{
  "code": 0, "message": "ok",
  "data": {
    "scan_id": "s-9f1c2a3e-...",
    "agent_id": "agent-home-lan",
    "cidr": "192.168.1.0/24",
    "status": "completed",
    "progress": { "scanned": 254, "total": 254, "found": 6, "failed": 0 },
    "results": [
      { "ip": "192.168.1.29", "mac": "AA:BB:CC:DD:EE:01", "vendor": "Realtek", "first_seen_at": "2026-11-05T22:45:24Z", "exists": true, "existing_device_id": "dev-...", "agent_id": "agent-home-lan", "subnet": "192.168.1.0/24" }
    ],
    "started_at": "2026-11-05T22:45:22Z",
    "finished_at": "2026-11-05T22:45:26Z",
    "duration_ms": 4120,
    "error": ""
  }
}
```

- `status=running` 时 `results` 为 `[]`（进行中不返回部分结果；`progress.found` 可提前看到发现数）。
- `status=failed` 时 `error` 为失败原因，`results` 为 `[]`。
- `status=cancelled` 时 `results` 为取消前已确认的发现（可导入），`progress` 为取消时快照。
- `progress.total`：网段内主机数（不含网络地址 `.0` 与广播地址 `.255`——M4 修复现状扫 `.0`/`.255` 的问题）；`scanned`：已探测数；`found`：已发现数；`failed`：探测任务本身失败数（下发错误、Agent 忙耗尽重试、结果解析失败；单台主机无响应/端口关闭是正常阴性结果，不计入 failed，不中断扫描）。
- 不存在 → 404。
- **存储**：扫描任务只保存在服务端内存（Hub 内），不落库；服务重启后任务丢失（前端轮询到 404 即视为任务已失效）。保留最近 **20** 个已完成/失败/取消的任务供查询；`GET /api/v1/scan`（列表）返回这 20 个的摘要（不含 `results`），同样分页信封。决策理由：扫描是一次性运维动作，结果的持久形态是导入后的设备行；为扫描单设一张表与迁移的收益不足（M4 若有异议可在评审时提出，改落库须补迁移编号）。

#### DELETE /api/v1/scan/{scan_id}

- 取消进行中的扫描：服务端向 Agent 发 `cancel` 消息（§3.5），任务置 `cancelled`，已发现结果保留可导入。
- 已完成/失败/取消的任务上调用 → 400 `invalid_value`（“任务已结束，无法取消”）；不存在 → 404。
- 成功：200 + 取消后的任务对象（同 §7.3 查询结构）。写 `scan_cancel` 审计。

#### POST /api/v1/scan/import

请求：

```json
{
  "scan_id": "s-9f1c2a3e-...",
  "devices": [
    { "ip": "192.168.1.29", "mac": "AA:BB:CC:DD:EE:01", "name": "LAN-29" },
    { "ip": "192.168.1.46", "mac": "AA:BB:CC:DD:EE:02" }
  ]
}
```

- `scan_id` 必填（须存在；任务状态不限，但 `running` 时导入 → 400 `invalid_value`“扫描尚未完成”）。
- `devices`：必填，非空数组，上限 **100** 条。`name` 可选，省略时服务端生成 `LAN-<IP 末段>`（如 `192.168.1.29` → `LAN-29`，与 v0.1.0 实测的命名一致）。
- 导入的每台设备：`agent_id` = 该扫描任务的执行 Agent（**自动关联，禁止客户端指定其他 Agent**，修复审计第 7 项“扫描结果与执行 Agent 无任何关联记录”）；`status` 初始 `unknown`；`enabled` 默认 true；其余字段取设备默认值（广播 `255.255.255.255`、WOL 端口 9、检测 `ping`）。
- 导入是**逐条 best-effort**（不是全有或全无）：

```json
{
  "code": 0, "message": "导入完成",
  "data": {
    "scan_id": "s-9f1c2a3e-...",
    "agent_id": "agent-home-lan",
    "imported": 1,
    "skipped": 1,
    "results": [
      { "ip": "192.168.1.46", "mac": "AA:BB:CC:DD:EE:02", "device_id": "dev-new-...", "status": "created", "reason": "" },
      { "ip": "192.168.1.29", "mac": "AA:BB:CC:DD:EE:01", "device_id": "dev-...", "status": "skipped", "reason": "already_exists" }
    ]
  }
}
```

- `status` ∈ `created | skipped`；`reason` ∈ `"" | already_exists`（MAC 已存在，按规范化 MAC 去重）/ `invalid_mac` / `invalid_ip` / `invalid_name`。
- 导入成功写 `scan_import` 审计（detail 含 scan_id 与导入数）。
- 前端“选择导入”勾选后调此接口，不再是“填表单再手动新增”（修复审计第 26 项）。

### 7.4 扫描结果对象（`DiscoveredDevice` v0.2.0）

在 v0.1.0 的 `{ip, mac, vendor}` 基础上扩展（M4）：

| 字段 | 说明 |
|---|---|
| `ip` / `mac` / `vendor` | 不变；`vendor` 为 `Unknown Vendor` 时照常返回（OUI 表 13 条硬编码的覆盖范围由 M7 在 README 如实注明，M4 可扩充但不强制） |
| `first_seen_at` | 本次扫描中首次发现的时间（RFC 3339 UTC） |
| `exists` | 该 MAC 是否已在设备表（按规范化 MAC 判断） |
| `existing_device_id` | 已存在时为设备 id，否则 `null` |
| `agent_id` | 执行扫描的 Agent |
| `subnet` | 本次扫描的 CIDR |

---

## 8. 定时任务（M4）

### 8.1 任务模型（v0.2.0）

| 字段 | 规则 |
|---|---|
| `name` | 必填，1–64 字符 |
| `cron_expr` | 必填；**创建时即解析校验**，非法 → 400 `invalid_format`（不再等 Reload 才记日志跳过，修复审计第 9 项） |
| `timezone` | 可选，默认 `UTC`；须为合法 IANA 时区（`LoadLocation` 成功），非法 → 400 `invalid_format` |
| `target_type` | `device \| group` |
| `target_id` | 设备 id 或分组 id；须真实存在，否则 400 `not_found`；`group` 为空分组 → 400 `empty_target` |
| `action` | 开放 `wake` / `sleep`（v0.3.2 起开放 sleep；空默认 `wake`）；其他值 → 400 `unsupported_action` |
| `retry_count` | 0–5，默认 1；越界 → 400 `out_of_range` |
| `run_once` | 可选布尔，默认 false；为 true 时首次执行后自动禁用（单次任务，见 §8.4） |
| `enabled` | 布尔，默认 true |

- `schedule_kind`（单次/每日/每周）是**前端可视化创建器的概念，不进 API**：前端把“每日 08:00”编译为 cron `0 8 * * *` 再提交；“每周一三五 22:00”编译为 `0 22 * * 1,3,5`。API 只收 cron。
- `next_run_at`：服务端计算（cron 下次触发时间，按任务时区换算为 UTC 返回）；禁用任务或 cron 非法时为 `null`。
- 任务 DTO：

```json
{ "id": "...", "name": "工作日开机", "cron_expr": "0 8 * * 1-5", "timezone": "Asia/Shanghai", "target_type": "device", "target_id": "dev-...", "target_name": "LAN-29", "action": "wake", "retry_count": 1, "run_once": false, "enabled": true, "last_run_at": "2026-11-05T08:00:01Z", "last_run_status": "success", "next_run_at": "2026-11-06T08:00:00Z", "created_at": "2026-11-05T22:38:00Z" }
```

- `target_name`：服务端解析的目标展示名（设备名/分组名），目标被删后为 `null`。
- `last_run_status`：最近一次运行的状态（`success | failed | skipped | partial`，无运行记录时为 `null`）。

### 8.2 任务接口

#### GET /api/v1/tasks

- 现状返回裸数组 → 改为分页信封（§1.4），`page` / `page_size`。其余不变。

#### POST /api/v1/tasks

- 请求体为任务对象；`enabled` 未传默认 true。
- 校验失败 → 400 + §1.2 结构。成功：201 + 任务 DTO（含计算出的 `next_run_at`）。
- 创建后服务端立即重载调度器（沿用现状 `Reload`）。

#### PUT /api/v1/tasks/{id}

- 新增接口（v0.1.0 只有创建/删除）。部分更新语义同 §4.3（只改出现字段）；改动 `cron_expr` / `timezone` / `target_*` / `action` / `retry_count` / `run_once` 时重新走 §8.1 全量校验；不存在 → 404。
- 成功：200 + 重查后的任务 DTO。改动后重载调度器。

#### DELETE /api/v1/tasks/{id}

- 不存在 → **404**（修复 B-N5）。成功 → 200 `"deleted"`，重载调度器。

#### POST /api/v1/tasks/{id}/pause · POST /api/v1/tasks/{id}/resume

- `pause`：`enabled` → false，重载调度器，写 `schedule_pause` 审计。已是暂停态 → 400 `invalid_value`。
- `resume`：`enabled` → true，重载调度器，写 `schedule_resume` 审计。已是启用态 → 400 `invalid_value`。
- 响应：200 + 任务 DTO。
- （`enabled` 字段仍可经 PUT 修改；pause/resume 是语义明确的快捷操作，前端优先用这两个端点。）

### 8.3 运行记录

#### GET /api/v1/tasks/{id}/runs

- 分页（§1.4）。不存在的任务 → 404。

```json
{
  "code": 0, "message": "ok",
  "data": {
    "items": [
      {
        "id": "run-...",
        "schedule_id": "...",
        "status": "partial",
        "output": "2 台成功，1 台失败：Agent 离线",
        "devices": [
          { "device_id": "dev-1", "name": "LAN-29", "status": "success", "reason": "" },
          { "device_id": "dev-2", "name": "NAS", "status": "failed", "reason": "agent_offline" }
        ],
        "started_at": "2026-11-05T08:00:00Z",
        "finished_at": "2026-11-05T08:00:03Z",
        "duration_ms": 3120
      }
    ],
    "total": 1, "page": 1, "page_size": 50
  }
}
```

- 运行状态：`success`（全部成功）/ `partial`（部分成功）/ `failed`（全部失败）/ `skipped`（零目标或全部跳过）。
- **空目标 / 全禁用不再记 success**（审计第 9 项）：目标解析为零台设备 → `skipped`，output `"无可执行设备"`；全部跳过（禁用）→ `skipped`，output 注明跳过原因。两者都不再写 `"唤醒指令已下发"` 的假成功。
- 每设备结果：`status` ∈ `success | failed | skipped`，`reason` 同 §5.3 的 reason 码。
- 存储：`task_runs` 表扩展 + 新增 `task_run_devices` 表，见附录 B（迁移 004）。
- 每次执行一个独立 Run ID；同一任务的重叠执行被跳过并记一条 `skipped`（output `"上一次执行尚未结束"`）。

### 8.4 执行语义（M4）

- `action` 真正参与执行；`wake` / `sleep` 以外的值在创建时已被拒绝，执行期不再有默认值兜底。
- 重试：`retry_count` 为**失败后重试次数**（0 = 不重试）；重试间隔 500ms 且**响应 ctx 取消**（修复 `time.Sleep` 不响应 ctx）；重试上限 5。
- `sleep`：按设备配置的睡眠适配器执行关机（SSH / http_sol），校验与执行路径与手动睡眠完全一致（密码解密、端点/IP 范围校验、TOFU 学习、审计 `device_sleep` / `device_sleep_failed`）；未配置适配器的设备记 `skipped`，不重试；成功行只设 `SentAt`，不设 `BootStatus`。
- WOL 重发次数：统一用 `WAKEHUB_WOL_RETRIES`（修复 D-N2：定时任务不再硬编码 `Count: 3`）。
- 单次运行总超时 **60s**；每设备 dispatch 10s（`TaskTimeout`）。
- `run_once=true` 的任务首次执行完成后（无论成败）自动置 `enabled=false` 并重载调度器。
- `last_run_at` / `last_run_status` 每次执行后更新；`next_run_at` 由调度器按 cron 实时计算。
- 调度器启动失败：**致命错误**，服务拒绝启动（修复 B-N9；由 M1 在启动路径顺带修，见审计串行表备注）。
- 夏令时：时区按 IANA 库处理，cron 下次触发时间由 `robfig/cron` + `LoadLocation` 计算；不存在“跳过/重复触发由 WakeHub 自行定义”的情况，文档如实说明。

---

## 9. 操作日志（M5）

### 9.1 查询接口

#### GET /api/v1/logs

查询参数（全部可选）：

| 参数 | 说明 |
|---|---|
| `from` / `to` | RFC 3339 时间范围；`from` 含端点、`to` 含端点 |
| `action` | 操作类型精确匹配（如 `device_wake`）；多值可用逗号分隔（`device_wake,device_sleep`） |
| `target_type` | `device \| group \| agent \| schedule \| subnet \| user` |
| `target_id` | 目标对象 id |
| `actor` | 操作者用户名 |
| `result` | `success \| failed`（见下文派生规则） |
| `page` / `page_size` | §1.4（默认 50，最大 200） |

响应：分页信封；`items` 为 `LogEntry`：

```json
{ "id": "...", "actor": "admin", "action": "device_wake", "result": "success", "target_type": "device", "target_id": "dev-...", "detail": "发送 WOL 魔术包至 AA:BB:CC:DD:EE:01", "ip_address": "192.168.1.51", "created_at": "2026-11-05T22:45:22Z" }
```

- `result` 字段是**响应期派生**（不新增存储列）：`action` 以 `_failed` 结尾 → `failed`，其余 → `success`。失败事件清单以 §1.6 的命名为准（`login_failed`、`device_wake_failed`、`device_sleep_failed` 等）。
- 兼容：不带任何参数时等价于 v0.1.0 的“最近 N 条”，但 `page_size` 默认 50（v0.1.0 固定 80；M6 按分页取数）。
- 审计写入失败不得静默丢弃（M5：`Record` 返回错误，调用方记日志；审计表写失败本身也要进系统日志）。

### 9.2 日志清理（M5）

- 保留策略（M5 落地，数值由 M5 定，契约只定接口）：默认保留 **90 天**，超期由定期任务清理；清理动作本身写一条 `audit_cleanup` 系统审计（actor 为 `system`）。
- v0.2.0 不提供“按条件删除日志”的 API（防审计湮灭）；清理只按时间。

---

## 10. Dashboard（M5）

#### GET /api/v1/dashboard

在 v0.1.0 的 `DashboardStats` 基础上扩展（M5 把计数改为 SQL 聚合，不再全表加载，修复 B-N13）：

```json
{
  "code": 0, "message": "ok",
  "data": {
    "total_devices": 1,
    "online_devices": 1,
    "offline_devices": 0,
    "unknown_devices": 0,
    "total_agents": 2,
    "online_agents": 2,
    "pending_agents": 0,
    "server_status": "ok",
    "db_status": "ok",
    "uptime_seconds": 3600,
    "version": "0.2.0",
    "upcoming_tasks": [
      { "schedule_id": "...", "name": "工作日开机", "target_name": "LAN-29", "next_run_at": "2026-11-06T08:00:00Z" }
    ]
  }
}
```

- 新增：`pending_agents`（待审批 Agent 数，供 UI 角标）、`server_status` / `db_status`（`"ok"` 或 `"error"`，后者同时应在系统日志留痕）、`uptime_seconds`（进程启动至今秒数）、`version`（服务端版本 `0.2.0`）。
- `upcoming_tasks`：**真实计算**（修复 v0.1.0 的死数据）：取已启用任务中 `next_run_at` 最近的 **5** 条，含 `schedule_id` / `name` / `target_name` / `next_run_at`；无任务时为 `[]`。
- 既有 6 项计数语义不变（`unknown` 仍含状态为空等未识别值，但 B-N2 修复后不再出现空字符串状态）。

---

## 11. 分组（无大改）

- `GET /api/v1/groups`：返回裸数组改为分页信封（§1.4；分组数少，`page_size` 默认 50 即可）。
- `POST /api/v1/groups`：不变（名称必填、唯一，冲突 → 409 `conflict`，M3 统一错误映射）。
- v0.2.0 **不新增**分组更新/删除接口（M6 不做分组管理页；分组删除涉设备归属迁移，顺延，§14）。

---

## 12. 配置与部署相关契约

### 12.1 环境变量（M1/M2/M7 共用）

| 变量 | v0.1.0 | v0.2.0 | 说明 |
|---|---|---|---|
| `WAKEHUB_ADMIN_USER` | 默认 `admin` | 不变 | 种子用户名；受 §2.3 字符集校验 |
| `WAKEHUB_ADMIN_PASSWORD` | 默认 `wakehub123` | **无默认值语义**：为空或等于占位值时拒绝启动（§2.7）；install.sh 随机生成 | 仅种子时使用 |
| `WAKEHUB_SECRET_KEY` | 固定示例值 | 同上，占位值拒绝启动 | 会话 HMAC 密钥；轮换后旧会话失效 |
| `WAKEHUB_AGENT_TOKEN` | 全局 Agent token | **服务端废止**；Agent 侧保留原名，含义改为 per-agent token | 服务端 `.env` 迁移：install.sh 把旧值复制为 `WAKEHUB_AGENT_ENROLL_TOKEN`（见迁移文档 §4.3） |
| `WAKEHUB_AGENT_ENROLL_TOKEN` | — | 新增 | 入网 token；占位值拒绝启动；install.sh 随机生成 |
| `WAKEHUB_WS_ALLOWED_ORIGINS` | — | 新增 | 逗号分隔的完整 origin 白名单（§3.5）；默认空 |
| `WAKEHUB_TRUSTED_PROXIES` | — | 新增 | 逗号分隔 CIDR；仅当配置时才信任 `X-Forwarded-For`（§2.5）；默认空（不信任） |
| `WAKEHUB_COOKIE_SECURE` | — | 新增 | `1` 时强制 Cookie Secure；默认按请求是否 TLS 自动判断 |
| `WAKEHUB_AGENT_ID` / `WAKEHUB_AGENT_NAME` / `WAKEHUB_AGENT_SUBNET` | 有默认值（`agent-lan-01` 等） | **取消代码默认值的不一致**：Agent ID/名/网段由 install.sh 或运维显式配置；未配置时 Agent 拒绝启动并提示（不再用与服务端不一致的默认值静默运行） | 修复审计第 7 项“三处默认值互不一致” |
| `WAKEHUB_SERVER_WS_URL` | 默认 `ws://127.0.0.1:8080/...` | 不变（本地默认）；公网部署须改为 `wss://`（§3.5） | — |
| `WAKEHUB_AGENT_TOKEN_FILE` | — | 新增（Agent 侧） | 路径 B 审批后 token 的持久化文件，默认 `./data/agent-token`，权限 0600 |
| `WAKEHUB_AGENT_TLS_SKIP_VERIFY` | — | 新增（Agent 侧） | 默认 `0`；显式为 `1` 时跳过服务端证书验证并记警告日志 |
| `WAKEHUB_STATUS_CHECK_INTERVAL` | — | 新增（V-N2） | 周期状态检测间隔（秒）；默认 `300`；`0` 为禁用；1–29 钳制为 30；非法值回落 300 |

### 12.2 健康检查（M7 落地，契约只定路径）

- 新增 `GET /api/v1/health`（公开，无需登录）：

```json
{ "code": 0, "message": "ok", "data": { "status": "ok", "version": "0.2.0", "db": "ok", "uptime_seconds": 3600 } }
```

- `db` 为 `ok`/`error`（`SELECT 1` 探测）。Docker `HEALTHCHECK` 与 compose `depends_on: condition: service_healthy` 调此接口（M7）。
- 根路径 `GET /` 保持现状（SPA 或纯文本提示），不作为健康检查口（修复 M7“就绪门改为调登录/健康接口而非 `GET /`”）。

---

## 13. 明确不做 / 顺延清单（v0.2.0 之外）

本节所列均**不在** v0.2.0 契约内；任何模块不得自行实现其中接口，须等后续版本立项。README 与 UI 不得宣称其中功能（任务书原则第 7 条）。

| 顺延项 | 理由 | 前置条件 |
|---|---|---|
| 多用户 / RBAC / “无权限用户”验收 | v0.2.0 保持单管理员模型（任务书第 34 项的该验收项在审计中已确认当前无法验收）；权限模型是独立设计工作 | 后续版本的用户与权限设计 |
| 个人 API Token（`api_tokens` 表） | 表已存在但零代码引用；Bearer 体系随 Cookie 化废止，个人 token 需另行设计签发/展示/吊销流程 | 后续版本的 API token 设计 |
| 服务端直出 TLS（`ListenAndServeTLS`） | v0.2.0 的 TLS 终止由反向代理承担；直出 TLS 需证书管理（自签/ACME）设计 | 后续版本的证书管理设计 |
| 带宽限期的 token 轮换（新旧并存） | 单 token 存储无法实现；需 `agent_tokens` 支持多行 | 后续版本的多 token 存储 |
| CSRF token（独立） | SameSite=Lax + Origin 校验已覆盖同源 SPA 场景 | 开放跨站 API 调用时 |
| 滑动续期（refresh token） | 增加 token 生命周期复杂度；24h 固定 TTL 可接受 | 后续版本 |
| WS/SSE 向浏览器推送扫描进度 | 1s 轮询已满足；推送增加连接管理复杂度 | 后续版本 |
| 批量唤醒历史查询 / 批量取消端点 | `batch_id` 本次请求内有效（§5.3）；取消靠客户端断开 | 批量任务持久化设计 |
| 分组更新/删除 | 涉设备归属迁移 | 后续版本 |
| Sleep 签名认证 | 无对端验签实现，见 §6.2；wire 格式已预留 | WakeHub 自有 sleep agent |
| Linux/Windows 本地关机适配器 | 同上，无对端实现 | 同上 |
| 真 ARP 主动扫描 | 需 raw socket / `CAP_NET_RAW` 特权，容器与平台限制大；v0.2.0 先做到白名单/进度/取消/去重/导入（§7），真 ARP 按 M4 评估结论降级或实现 | M4 的特权能力评估 |
| 自定义排序参数 | 分页已覆盖主要需求 | 后续版本 |
| 通用 `Idempotency-Key` | 无存储设计；重试安全由自然键保障（§1.5） | 后续版本 |
| 审计日志按条件删除 API | 防审计湮灭；只按时间清理（§9.2） | — |

### 13.1 任务书不实子项（审计结论，不进契约）

- “页面切换重复定时器”：代码中不存在，契约不设相关修复；真实问题是 `refreshAll` 无竞态防护与 `Promise.all` 全有或全无，由 M6 按契约的 API Client 规范处理（`allSettled` + `AbortController`，见审计 §六）。
- “不允许通过 API 执行任意 Shell 命令”：`sleep.go` 无 `os/exec`，该风险不存在；契约只处理 SSRF（§6.1）。
- “管理员密码使用 Argon2id”：bcrypt 已评估可用，不换算法（§2.3）。
- “避免无意义的统计卡片”：卡片数据真实，不虚构问题；Dashboard 改版见 §10。
- “开启外键 / WAL / UTC 存储”：三项均已实现，不重复立项；缺失的是迁移版本机制与缺失的 FK/索引（迁移文档与附录 B）。
- “每个连接使用独立 Context”：已实现一半；剩余部分（每连接子 ctx、发送阻塞）由 M2 按 §3.5 实现，不另立契约条目。

---

## 14. 自动状态检测（V-N2，VM 验证后追加）

v0.2.0 初版只有手动检测：设备状态只在点"检测"、唤醒回检、导入时更新，
面板上的在线/离线会过期。VM 验证（2026-11-06）确认缺口后追加两条
自动检测路径，二者互补，任一关闭都不影响另一条。

### 14.1 服务端周期检测

- 服务端后台按 `WAKEHUB_STATUS_CHECK_INTERVAL`（秒，默认 300，`0` 禁用）
  对全部已启用设备做一轮检测；启动后立即跑一轮，之后按间隔跑；
  上一轮未跑完时下一轮跳过，不叠加。
- 只检测已启用设备；无 Agent 或 Agent 离线时跳过，不写脏状态。
- 检测失败（下发失败、结果解析失败、非法状态）不改库：保留上次已知
  状态，不把"没测到"谎报成离线（与 §4.5 同款语义）。
- 单周期内并发上限 5；每轮记一行汇总日志（共/检测/在线/离线/未知/跳过）。
- **周期检测不写审计**：每轮每台都写 `device_check` 会把审计日志刷爆；
  状态本身已入库（`last_checked_at` 可查）。

### 14.2 页面刷新触发检测

- 设备页、仪表盘加载完成后，前端自动检测当前页全部已启用设备
  （并发上限 5），结果逐行更新到列表与统计计数上；单台失败不阻断
  其余，保留旧状态。
- 触发方式为已有端点 `POST /api/v1/devices/{id}/check`，body
  `{"auto": true}`；手动点"检测"不带该字段。
- `auto=true` 时后端检测逻辑与手动完全一致，**仅跳过审计写入**
  （成功与失败均不写 `device_check` / `device_check_failed`）；
  body 缺失或非法时按手动检测处理，不因此 400。

---

## 附录 A：路由总表（v0.2.0）

`A` = 须登录（Cookie）；`P` = 公开；`W` = Agent WS（token 头）。

| 方法 | 路径 | 认证 | 变化 |
|---|---|---|---|
| POST | `/api/v1/auth/login` | P | 不再返回 token，改发 Cookie（§2.4） |
| POST | `/api/v1/auth/logout` | A | 新增 |
| POST | `/api/v1/auth/change-password` | A | 新增 |
| GET | `/api/v1/auth/me` | A | 新增 |
| GET | `/api/v1/health` | P | 新增 |
| GET | `/api/v1/ws/agent` | W | 握手协议见 §3.5 |
| GET | `/api/v1/dashboard` | A | 字段扩展（§10） |
| GET | `/api/v1/devices` | A | 分页 + `status`/`agent_id` 筛选（§4.3） |
| POST | `/api/v1/devices` | A | 统一验证 + `warnings`（§4.3） |
| GET | `/api/v1/devices/{id}` | A | 不变 |
| PUT | `/api/v1/devices/{id}` | A | 部分更新 + 返回重查行 + 404（§4.3） |
| DELETE | `/api/v1/devices/{id}` | A | 404（§4.3） |
| POST | `/api/v1/devices/{id}/wake` | A | `verify` 回检（§5.2） |
| POST | `/api/v1/devices/batch-wake` | A | 新响应结构（§5.3） |
| POST | `/api/v1/devices/{id}/check` | A | 不写脏状态 + 审计（§4.5） |
| POST | `/api/v1/devices/{id}/sleep` | A | Body 解码失败不再默认执行；失败 502（§6） |
| GET | `/api/v1/groups` | A | 分页信封（§11） |
| POST | `/api/v1/groups` | A | 冲突 409（§11） |
| GET | `/api/v1/agents` | A | 新字段（§3.4） |
| POST | `/api/v1/agents` | A | 新增（预签发） |
| GET | `/api/v1/agents/{id}` | A | 新增 |
| POST | `/api/v1/agents/{id}/approve` | A | 新增 |
| POST | `/api/v1/agents/{id}/reject` | A | 新增 |
| POST | `/api/v1/agents/{id}/revoke-token` | A | 新增 |
| POST | `/api/v1/agents/{id}/reissue-token` | A | 新增 |
| POST | `/api/v1/agents/{id}/disable` | A | 新增 |
| POST | `/api/v1/agents/{id}/enable` | A | 新增 |
| POST | `/api/v1/agents/{id}/subnets` | A | 新增 |
| DELETE | `/api/v1/agents/{id}/subnets` | A | 新增 |
| DELETE | `/api/v1/agents/{id}` | A | 新增 |
| POST | `/api/v1/scan` | A | 异步 202（§7.3） |
| GET | `/api/v1/scan` | A | 新增（列表，内存） |
| GET | `/api/v1/scan/{scan_id}` | A | 新增（进度查询） |
| DELETE | `/api/v1/scan/{scan_id}` | A | 新增（取消） |
| POST | `/api/v1/scan/import` | A | 新增 |
| GET | `/api/v1/tasks` | A | 分页信封（§8.2） |
| POST | `/api/v1/tasks` | A | 创建时校验（§8.2） |
| PUT | `/api/v1/tasks/{id}` | A | 新增 |
| DELETE | `/api/v1/tasks/{id}` | A | 404（§8.2） |
| POST | `/api/v1/tasks/{id}/pause` | A | 新增 |
| POST | `/api/v1/tasks/{id}/resume` | A | 新增 |
| GET | `/api/v1/tasks/{id}/runs` | A | 新增 |
| GET | `/api/v1/logs` | A | 筛选 + 分页（§9.1） |

## 附录 B：迁移 004 / 005 预告（M4/M5，不属本文档的 002/003 范围）

本契约引用但不在 `docs/migration-002-003.md` 内编号的迁移（由对应模块在实施时写正式 SQL）：

- **004（M4，任务运行记录）**：`task_runs` 加列 `started_at` / `finished_at` / `duration_ms`；新建 `task_run_devices` 表（`id, run_id FK→task_runs, device_id, device_name, status, reason, sent_at, boot_status`）；`schedules` 加列 `run_once INTEGER DEFAULT 0`。**编号说明**：审计原计划 004 为 indexes/FK；因运行记录表是 M4 的功能依赖，004 改为运行记录，原 indexes/FK 顺延为 005（见迁移文档 §6）。
- **005（M5，索引与外键）**：补索引（`wake_tasks.device_id`、`task_runs.schedule_id`、`schedules.target_id`、`audit_logs.actor`、`devices.mac` 唯一索引）；补外键（`devices.agent_id/group_id`、`schedules.target_id`）并处理既有孤儿数据；`devices` 的 MAC 唯一约束落在此处（M3 先在验证层强制）。

## 附录 C：审计条目 → 契约章节对照

| 审计条目 | 契约章节 |
|---|---|
| 第 1 项（重连竞态） | §3.5（会话替换比对由 M2 实现；契约只定握手与状态机） |
| 第 2 项（全局 token / 审批） | §3.1–§3.5 |
| 第 3 项（CheckOrigin） | §3.5 Origin 策略 |
| 第 4 项（默认凭证 / 改密 / 撤销） | §2 |
| 第 5 项（Agent 安全通信） | §3.5 |
| 第 6 项（Sleep SSRF） | §6 |
| 第 7 项（设备 Agent 默认分配） | §3.6、§4.1–§4.2、§4.6 |
| 第 8 项（批量唤醒） | §5 |
| 第 9 项（Scheduler） | §8 |
| 第 10 项（状态检测） | §4.5 |
| 第 11 项（扫描） | §7 |
| 第 12 项（设备验证） | §4.1–§4.2 |
| 第 13 项（限流） | §2.5 |
| 第 14 项（Agent goroutine 泄漏） | §3.5 |
| 第 15 项（统一校验） | §4.4 |
| 第 16 项（API 规范） | §1 |
| 第 17 项（数据库） | 迁移文档 + 附录 B |
| 第 18 项（日志与审计） | §1.6、§9 |
| 第 19 项（Docker） | §12（健康检查路径；其余由 M7 按审计实施） |
| 第 20–30 项（UI/UX） | 由 M6 按本契约的接口与响应结构实现；本契约不规定像素与布局 |
| 第 31–34 项（测试与验收） | M8；本契约的每个接口都是验收输入 |
| B-N1～B-N15、D-N1～D-N5、F-N1～F-N8 | 行内引用处已逐项标注；未标注者属实现细节，由对应模块按审计报告处理 |

---

*契约结束。评审意见请直接改本文档并注明日期与修改人；评审通过后冻结，M1–M6 按此开工。*
