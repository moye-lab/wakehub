# WakeHub API 文档（v0.3.1）

基础路径 `/api/v1`。本文件与 `docs/contract-v0.2.0.md` 一致；契约是权威
来源，本文是面向调用方的速查版。接口总数 50 个（含健康检查）。

## 通用约定

### 响应信封

所有接口（包括错误）统一使用：

```json
{ "code": 0, "message": "ok", "data": { "...": "..." } }
```

- 成功：HTTP 200/201，`code: 0`；创建类接口 `message` 为 `"created"`，
  删除为 `"deleted"`。
- 失败：`code` 等于 HTTP 状态码，`message` 为中文可读信息，`data` 为
  `null` 或含 `errors` 的对象。
- 时间：全部为 RFC 3339 UTC（如 `2026-11-05T22:45:22Z`），UI 显示本地时区。
- ID：全部为 UUID v4 字符串。
- 空列表：返回 `[]`，永不返回 `null`。
- 部分成功响应（如创建设备时目标 Agent 离线）可能附加顶层 `warnings`
  数组：`[{ "code": "agent_offline", "message": "..." }]`。

### 认证

除登录、健康检查与 Agent WebSocket 外，全部接口须登录。登录态是
HttpOnly Cookie（`wakehub_session`，SameSite=Lax，24 小时），**不再接受
`Authorization: Bearer`**。浏览器/fetch 客户端须带
`credentials: 'same-origin'`；curl 用 `-c` / `-b` 存取 Cookie。

- 未登录 / 会话失效或被撤销 → 401 + 固定文案 `"未登录或登录已过期"`。
- 状态变更请求（POST/PUT/DELETE）校验 `Origin` / `Referer`：为空
  （非浏览器客户端）或与请求 `Host` 同源时放行，否则 403。
- 请求 Body 上限 1 MiB，超限 413。

### 分页

列表接口统一用 `page`（从 1 开始，默认 1）、`page_size`（默认 50，
最大 200，超限钳制为 200），响应 `data` 为：

```json
{ "items": [], "total": 0, "page": 1, "page_size": 50 }
```

排序固定：设备按 `created_at DESC`；日志按 `created_at DESC`；运行
记录按 `started_at DESC`；扫描任务按 `started_at DESC`。不开放自定义
排序参数。

### 错误码

| HTTP | 含义 |
| --- | --- |
| 200 | 成功 |
| 201 | 创建成功 |
| 400 | 校验失败（`data.errors` 为字段级错误，见下） |
| 401 | 未登录 / 会话失效或被撤销 |
| 403 | 已登录但无权操作（预留；WS 握手失败也用 403 分码） |
| 404 | 对象不存在（设备/任务/Agent/分组/扫描任务/运行记录） |
| 409 | 冲突（MAC 重复、Agent ID 重复、扫描进行中） |
| 413 | Body 超限 |
| 429 | 限流 |
| 500 | 内部错误 |
| 502 | 下游执行失败（Agent 离线、dispatch 失败、检测失败） |

结构化校验错误（`data.errors`）：

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

常用 `code`：`required`、`invalid_format`、`out_of_range`、`invalid_value`、
`not_found`、`conflict`、`not_authorized`、`agent_offline`、`agent_disabled`、
`agent_not_approved`、`agent_id_mismatch`、`device_disabled`、`invalid_mac` /
`invalid_cidr`、`cidr_not_allowed`、`scan_in_progress`、`unsupported_action`、
`empty_target`、`rate_limited`、`invalid_token` / `token_revoked` /
`token_expired`、`wrong_password`、`weak_password`。

### 限流

- 登录：同一 IP 或同一账号 5 分钟内失败 ≥ 8 次 → 429（锁定期间成功登录
  也不放行）。
- 通用：每个已登录会话每分钟最多 600 次请求，超限 429 `rate_limited`
  + `Retry-After: 60`。

### 幂等性

v0.2.0 不实现通用 `Idempotency-Key`。重试安全由自然键保障：设备创建
按 MAC 去重（409）、扫描导入按 MAC 去重、批量唤醒重发魔术包本身无害、
定时任务创建不去重（调用方避免重复提交）。

## 认证

### POST /api/v1/auth/login（公开）

请求：`{ "username": "admin", "password": "••••••••" }`

成功：200 + `Set-Cookie: wakehub_session=...`，响应体：

```json
{ "code": 0, "message": "ok", "data": { "username": "admin", "expires_at": "2026-11-06T22:45:22Z" } }
```

**不返回 `token` 字段。** 失败：用户名或密码错误 → 401
`"用户名或密码错误"`（不区分用户不存在与密码错误）；限流 → 429。

### POST /api/v1/auth/logout

须登录。服务端把当前会话 `jti` 写入撤销表，并下发清除 Cookie
（`Max-Age=0`）。响应 `{ "code": 0, "message": "ok", "data": null }`。

### POST /api/v1/auth/change-password

须登录。请求：`{ "current_password": "…", "new_password": "…" }`

- 当前密码错误 → 400 `errors: [{ "field": "current_password", "code": "wrong_password", "message": "当前密码不正确" }]`。
- 新密码：长度 ≥ 8 位，不得与用户名相同，不得等于任何已知占位值；
  不满足 → 400 `errors: [{ "field": "new_password", "code": "weak_password", "message": "新密码长度至少 8 位" }]`。
- 成功后**全部会话立即失效**（含当前会话），响应
  `{ "code": 0, "message": "密码已修改，请重新登录", "data": null }`，
  前端须跳登录页重新登录。
- 禁止删库改密。忘记密码时用 CLI：`wakehub-server reset-password`
  （交互式读两次新密码，不删库）。

### GET /api/v1/auth/me

须登录。返回：

```json
{ "code": 0, "message": "ok", "data": { "username": "admin", "role": "admin", "issued_at": "2026-11-05T22:45:22Z", "expires_at": "2026-11-06T22:45:22Z" } }
```

## 健康检查

### GET /api/v1/health（公开）

无需登录、只读、轻量，供 Docker HEALTHCHECK 与 compose
`depends_on: condition: service_healthy` 调用：

```json
{ "code": 0, "message": "ok", "data": { "status": "ok", "version": "0.3.1", "db": "ok", "uptime_seconds": 3600 } }
```

`db` 为 `ok` / `error`（`SELECT 1` 探测）。根路径 `GET /` 不是健康
检查口。

## 仪表盘

### GET /api/v1/dashboard

```json
{
  "code": 0, "message": "ok",
  "data": {
    "total_devices": 1, "online_devices": 1, "offline_devices": 0, "unknown_devices": 0,
    "total_agents": 2, "online_agents": 2, "pending_agents": 0,
    "server_status": "ok", "db_status": "ok", "uptime_seconds": 3600, "version": "0.3.1",
    "upcoming_tasks": [
      { "schedule_id": "...", "name": "工作日开机", "target_name": "LAN-29", "next_run_at": "2026-11-06T08:00:00Z" }
    ]
  }
}
```

`upcoming_tasks` 为真实计算：已启用任务中 `next_run_at` 最近的 5 条；
无任务时为 `[]`。

## 设备

设备对象字段：`id`、`name`（1–64 字符，必填）、`mac`（严格正则，
规范化为大写冒号分隔，唯一）、`ip`（可选，合法 IPv4）、
`broadcast_address`（默认 `255.255.255.255`）、`wol_port` /
`status_port`（1–65535，默认 9 / 22）、`device_type`
（`pc | server | nas | raspberry_pi | tv_box | router | other`）、
`status_method`（`ping | tcp | arp`）、`sleep_type`
（`none | http_sol | ssh`）、`sleep_endpoint`（`http_sol` 时必填，须符合
§“睡眠 / 关机”的 allowlist）、`ssh_user`（`ssh` 时必填）、`ssh_port`
（`ssh` 时用，默认 22）、`ssh_password`（写时字段：新建 `ssh` 设备时必填；
编辑时留空表示保持原密码）、`ssh_password_set`（只读：是否已存 SSH 密码）、
`ssh_host_key_sha256`（只读：TOFU 学到的主机密钥指纹）、`ssh_os`
（`ssh` 时用，`linux` 默认 | `windows`，v0.2.6）、`ssh_use_sudo`
（`ssh` + `linux` 时用，默认 false：勾选后 Agent 以单次会话 `sudo -S`
执行关机命令，密码经 stdin 一次喂给 sudo，校验与断电在同一个 sudo
调用里完成，不依赖跨会话的时间戳；目标机无需写规则；Windows 目标忽略，
切到 Windows 或离开 SSH 时归零，v0.2.12）、`group_id`（须真实存在，默认 `default`）、
`agent_id`（**必填，无默认值**；`local` 不再合法）、`enabled`
（未传默认启用）、`remark`（最长 500 字符）、`status`、
`created_at`。

### GET /api/v1/devices

查询参数：`group_id`、`q`（名称/IP/MAC 模糊）、`status`
（`online | offline | unknown | checking`）、`agent_id`、
`network_id`（v0.2.9：按所属网络过滤）、`page` /
`page_size`。响应分页信封，`items` 为设备对象数组。

### POST /api/v1/devices

请求体为设备对象（`id`/`status`/`created_at` 由服务端生成）。
成功 201 + 完整设备行（`status: "unknown"`）。目标 Agent 离线时放行
创建，但响应附加 `warnings`（`agent_offline`）。校验失败 400 +
`data.errors`；MAC 重复 409。

**网络归属（v0.2.9）**：`network_id` 可空；不填时服务端按设备 Agent 的
首个管理网络归属，Agent 无网络时回落 `default`。MAC 唯一范围为网络内：
同一 MAC 可在不同网络各建一台，同网络重复 409（文案保持
「MAC 地址已被设备 X 占用」）。

### GET /api/v1/devices/{id}

不存在 → 404。

### PUT /api/v1/devices/{id}

**部分更新**：只更新请求体中出现的字段，未出现的保持原值；显式传
空字符串表示清空该字段（如 `sleep_endpoint: ""`）。成功 200 + **重查
后的数据库行**；不存在 → 404；校验失败 400；MAC 与他人重复 409。

### DELETE /api/v1/devices/{id}

不存在 → 404。成功 200 `"deleted"`。

### POST /api/v1/devices/{id}/wake

校验链：设备不存在 → 404；禁用 → 400 `device_disabled`；Agent 离线
→ 502 `agent_offline`。请求体可选：
`{ "verify": true, "verify_timeout_seconds": 30 }`
（`verify_timeout_seconds` 1–120，默认 30，仅 `verify=true` 时有效）。

`verify=false`（默认）：

```json
{ "code": 0, "message": "WOL 魔术包已发送", "data": { "device_id": "...", "mac": "AA:BB:CC:DD:EE:01", "send_status": "sent", "sent_at": "2026-11-05T22:45:22Z", "boot_status": "pending", "boot_checked_at": null } }
```

`verify=true`：发包后轮询检测（间隔 2s，上限为
`verify_timeout_seconds`），`boot_status` 为 `online` 时 message 为
“WOL 魔术包已发送，设备已上线”。`boot_status` 枚举：`pending`（未回检）、
`online`（回检到上线）、`timeout`（限时内未上线，此时 HTTP 仍为 200，
message 须写明“魔术包已发送，但限时内未检测到设备上线”）、`unknown`
（回检失败）。**`sent` ≠ `booted`：发包成功不等于设备已启动。**

发包失败（dispatch 失败）→ 502。

### POST /api/v1/devices/batch-wake

请求：`{ "device_ids": ["id-1", "id-2"], "verify": false, "verify_timeout_seconds": 30 }`

- `device_ids` 必填，非空数组，上限 100 台（超限 400 `out_of_range`）；
  重复 ID 只执行一次，第二次出现记 `skipped`，reason `duplicate`。
- 并发上限 10；每设备 dispatch 超时 10s；整体上限 120s（超限后未完成
  项记 `failed`，reason `timeout`，并返回已完成部分）。
- 取消：客户端断开即取消剩余任务；无单独的批量取消端点。
- 响应 200（即使部分失败——HTTP 200 表示“批量执行完毕”，成败看
  `results`）：

```json
{
  "code": 0, "message": "批量唤醒执行完毕",
  "data": {
    "batch_id": "b3e1a7c2-...", "total": 3, "success": 2, "failed": 0, "skipped": 1,
    "results": [
      { "device_id": "id-1", "name": "LAN-29", "mac": "AA:BB:CC:DD:EE:01", "status": "sent", "reason": "", "sent_at": "2026-11-05T22:45:22Z", "boot_status": "pending", "boot_checked_at": null },
      { "device_id": "id-2", "name": "PC-02", "mac": "AA:BB:CC:DD:EE:FF", "status": "skipped", "reason": "device_disabled", "sent_at": null, "boot_status": null, "boot_checked_at": null }
    ],
    "started_at": "2026-11-05T22:45:20Z", "finished_at": "2026-11-05T22:45:23Z", "duration_ms": 3120
  }
}
```

每设备 `status` ∈ `sent | failed | skipped`；`success` 计数定义为
`sent` 数，不是“已启动”数。`batch_id` 仅为本次请求的关联 ID，不持久化。

### POST /api/v1/devices/{id}/check

校验链同唤醒；设备不存在 → 404；Agent 离线 → 502。成功 200：

```json
{ "code": 0, "message": "ok", "data": { "state": "online", "method": "ping", "checked_at": "2026-11-05T22:45:22Z", "latency_ms": 12, "error_reason": "" } }
```

`state` ∈ `online | offline | unknown | checking`（同步检测中不出现
`checking`）。检测异常（派发错误/解析失败）→ 502，不写脏状态入库；
检测出离线是正常结果，返回 200。TCP 端口关闭时 `state` 为 `unknown`
+ `error_reason` 注明“端口关闭，无法据此判定关机”，不得记 `offline`。

可选 body `{"auto": true}`（V-N2）：自动检测（周期检测器、页面刷新
触发）的标识，检测逻辑与手动完全一致，**仅跳过审计写入**（成功与
失败均不写 `device_check` / `device_check_failed`）；body 缺失或非法
时按手动检测处理，不因此 400。服务端周期检测默认每 5 分钟跑一轮，
见 `WAKEHUB_STATUS_CHECK_INTERVAL`（契约 §14）。

### POST /api/v1/devices/{id}/sleep

请求体：`{ "action": "sleep" }`，`action` ∈ `shutdown | sleep | hibernate`。
Body 解码失败 → 400，**禁止默认执行**。成功 200
`{ "code": 0, "message": "远程关机/睡眠指令已执行", "data": null }`；
Agent 执行失败 → 502。`sleep_type=none` 的设备调用 → 400
`invalid_value`（“设备未配置受控远程关机适配器”）。

睡眠端点安全约束：`http://` 或 `https://` 开头，优先 HTTPS；主机须为
私有 IPv4（10/8、172.16/12、192.168/16）或回环（127.0.0.0/8），禁公网、
禁 169.254.0.0/16、禁 userinfo；端口 1–65535；禁跟随重定向；超时 5s。
**v0.2.0 睡眠端点无签名认证，仅限可信局域网内使用。**

SSH 适配器（v0.2.5，契约 §6.3）：`sleep_type=ssh` 的设备不走端点，
Agent 用 SSH 直连设备 IP 执行关机命令，目标须为 Linux 且 SSH 已开启，
不用装监听服务。SSH 用户名必填；密码 AES-256-GCM 加密存储（密钥由
`WAKEHUB_SECRET_KEY` 派生），明文永不落库；主机密钥 TOFU（首次记录指纹，
之后每次校验，改密码即重新信任）。成功仅表示“指令已发起”。

## 网络（v0.2.9 新增）

MAC 唯一范围为网络内；一个 Agent 可管理多个网络，一个网络也可由多个
Agent 管理。`default` 为内置网络，恒存在、不可删除。

### GET /api/v1/networks

列表，`default` 恒为首位。响应分页信封，`items` 为网络对象数组
（`id` / `name` / `cidr` / `description` / `created_at` / `updated_at`）。

### POST /api/v1/networks

请求：`{ "name": "实验室", "cidr": "192.168.2.0/24", "description": "…" }`。
`name` 必填；`cidr` 可空，非空须为合法 CIDR 且不与已有网络重复。
成功 201 + 网络对象。名称空 / CIDR 非法 → 400；CIDR 与已有网络重复 → 409。

### GET /api/v1/networks/{id} · PUT /api/v1/networks/{id} · DELETE /api/v1/networks/{id}

不存在 → 404。PUT 部分更新（`name` / `cidr` / `description`）；
CIDR 冲突 → 409。DELETE 默认网络 → 400；被设备或 Agent 引用 → 409。

### GET /api/v1/agents/{id}/networks · PUT /api/v1/agents/{id}/networks

查看 / 设置某 Agent 管理的网络。PUT 请求：`{ "network_ids": ["net-…", …] }`，
整体替换（事务内先删后插，任一网络不存在整体报错）。Agent 不存在 → 404；
网络不存在 → 404。成功返回 `{ "networks": […] }`。

## 分组

### GET /api/v1/groups

分页信封（§1.4；分组数少，`page_size` 默认 50 即可）。

### POST /api/v1/groups

请求：`{ "name": "客厅", "description": "…" }`。名称必填、唯一，
冲突 → 409 `conflict`。成功 201。

v0.2.0 不提供分组更新/删除接口。

## Agent

Agent 列表项：`id`、`name`、`subnet`、`allowed_subnets`、`status`、
`authorized`（派生值，标记废弃，M6 不得依赖）、`ip`、`os`、`arch`、
`version`、`last_seen_at`、`last_heartbeat_at`、`created_at`。

`status` 是审批状态与连接状态的合并值：
`disabled` → `pending` → `connected`（approved 且当前有 live WS 会话）
→ `approved`（approved 且当前离线）。

### GET /api/v1/agents

返回全部 Agent（按 `created_at ASC`，不分页，节点数少）。

### POST /api/v1/agents（预签发，路径 A）

请求：`{ "agent_id": "agent-home-lan", "name": "Home-HostNetwork-Agent", "subnet": "192.168.1.1/24" }`

- `agent_id` 可选；省略时服务端生成 `agent-` + 8 位随机小写字母数字。
  提供时须匹配 `^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`，且不得为保留值 `local`。
- `name` 必填（1–64 字符）；`subnet` 可选，须为合法 CIDR。
- 已存在同 ID → 409。
- 成功 201，创建时直接为 `approved`，并**一次性返回明文 token**：

```json
{ "code": 0, "message": "created", "data": { "agent": { "...": "..." }, "token": "wha_7f3a9c..." } }
```

服务端永不再次返回该 token 明文。UI 须以“仅显示一次、可复制”的方式
展示，并提示运维写入 Agent 的 `WAKEHUB_AGENT_TOKEN`。

### GET /api/v1/agents/{id}

不存在 → 404。

### POST /api/v1/agents/{id}/approve（审批通过，路径 B）

仅 `pending` 可审批；非 pending → 400。效果：`approval_status` →
`approved`，签发新的 per-agent token（旧 token 若存在则作废）。
响应 201：`{ "code": 0, "message": "ok", "data": { "agent": { "...": "..." }, "token": "wha_...", "delivered_via": "http" } }`。
若该 Agent 当前正以 pending 模式连接着，服务端同时经 WS 推送
`registration_approved`，`delivered_via` 为 `"websocket+http"`。

### POST /api/v1/agents/{id}/reject（驳回）

仅 `pending` 可驳回。效果：删除该 Agent 行（含 token 与白名单）；
若正以 pending 模式连接，服务端先发 `registration_rejected` 再以
4403 关闭 WS。响应 `{ "code": 0, "message": "deleted", "data": null }`。

### POST /api/v1/agents/{id}/revoke-token（吊销 token）

吊销当前 per-agent token；若 Agent 在线，服务端立即以 4403 关闭其 WS。
审批状态不变。响应
`{ "code": 0, "message": "ok", "data": { "agent_id": "...", "revoked": true } }`。
吊销后须经 `reissue-token` 重新签发才能再连（或走路径 B 重入网）。

### POST /api/v1/agents/{id}/reissue-token（重新签发，即轮换）

生成新 token，原子替换旧哈希（旧 token 立即失效，无宽限期）；若 Agent
在线，服务端关闭其旧会话。响应 201：
`{ "code": 0, "message": "ok", "data": { "agent_id": "...", "token": "wha_..." } }`。

### POST /api/v1/agents/{id}/disable · POST /api/v1/agents/{id}/enable

`disable`：`approval_status` → `disabled`，在线会话以 4403 立即断开；
其设备仍保留关联，唤醒等操作返回 502 `agent_disabled`。
`enable`：`disabled` → `approved`（token 不变；若 token 已被吊销，
须另行重新签发）。非法转移（如对 `pending` 执行 disable）→ 400。

### DELETE /api/v1/agents/{id}

删除 Agent 行、token、白名单。其设备不删除，`agent_id` 原样保留；
后续对这些设备的操作返回 502 `agent_not_found`。响应
`{ "code": 0, "message": "deleted", "data": null }`。

### POST /api/v1/agents/{id}/subnets · DELETE /api/v1/agents/{id}/subnets

管理扫描白名单。请求：`{ "cidr": "192.168.1.0/24" }`。`cidr` 须为私有
IPv4 CIDR 且掩码 ≥ /24，否则 400 `invalid_cidr`。POST 已存在 → 409；
DELETE 不存在 → 404。成功返回更新后的 `allowed_subnets` 数组。

## Agent WebSocket

### GET /api/v1/ws/agent

不设 Cookie 认证（Agent 是非浏览器客户端）。请求头：

| 头 | 必填 | 说明 |
| --- | --- | --- |
| `X-Agent-Token` | 是 | per-agent token（日常）或入网 token（首次注册/重入网） |
| `X-Agent-ID` | 是 | 与 token 绑定的 Agent ID；`local` 保留，远程使用直接 403 |
| `X-Agent-Name` | 否 | 展示名 |
| `X-Agent-Subnet` | 否 | Agent 所在网段 CIDR（注册时写入白名单） |
| `X-Agent-Version` | 否 | Agent 版本 |
| `X-Agent-OS` / `X-Agent-Arch` | 否 | 如 `linux` / `amd64` |

握手失败（升级前）返回 HTTP 状态 + JSON 信封：缺凭证 400；token 无效
401；token 已吊销 403（`token_revoked`）；未获批 403
（`agent_not_approved`）；token 与 ID 不绑定 / 冒认 `local` 403
（`agent_id_mismatch`）；已禁用 403（`agent_disabled`）。

Origin 策略：`Origin` 为空 → 放行（非浏览器客户端）；非空时仅当与
请求 `Host` 同源或在 `WAKEHUB_WS_ALLOWED_ORIGINS` 白名单内时放行，
否则 403。Origin 校验不是身份认证，通过后仍须持有效 token。

消息协议：`{ "id": "uuid", "type": "wake", "payload": { "...": "..." } }`。
类型：`heartbeat`、`heartbeat_ack`、`wake`、`check`、`scan`、`sleep`、
`result`、`registration_pending`、`registration_approved`、
`registration_rejected`、`cancel`。Agent 回包 `result` 必须原样回传
同一 `id`；去重窗口 5 分钟/1000 条；双向消息上限 1 MiB；应用层心跳
15s，服务端 45s 未收到心跳判定僵死；重连指数退避 1s→60s（±25% 抖动）；
任务并发上限 8；WS 关闭码 4403 表示吊销/禁用/驳回/未获批。

per-agent token 明文格式：`wha_` + `base64url(32 字节随机数)`。服务端
只存 SHA-256 哈希，永不存明文；每 Agent 同时只有一个有效 token。

## 扫描

扫描任务状态：`running | completed | failed | cancelled`。任务只保存在
服务端内存（最近 20 个已完成/失败/取消的任务供查询），服务重启后丢失；
`GET /api/v1/scan`（列表）返回这 20 个的摘要（不含 `results`），分页信封。

### POST /api/v1/scan

请求：`{ "agent_id": "agent-home-lan", "cidr": "192.168.1.0/24" }`

- `agent_id` 必填；不存在 → 404；未获批/已禁用 → 400
  `not_authorized` / `agent_disabled`；离线 → 502 `agent_offline`。
- CIDR 须等于白名单中某一项或是其子网，且为私有 IPv4、掩码 ≥ /24；
  违反 → 400（`invalid_cidr` / `cidr_not_allowed`）。
- 每 Agent 同时只能有 1 个进行中的扫描，已有时 409 `scan_in_progress`
  （body 含进行中的 `scan_id`）。
- 成功 **202**：

```json
{ "code": 0, "message": "扫描任务已创建", "data": { "scan_id": "s-9f1c2a3e-...", "agent_id": "agent-home-lan", "cidr": "192.168.1.0/24", "status": "running", "started_at": "2026-11-05T22:45:22Z" } }
```

### GET /api/v1/scan/{scan_id}

```json
{
  "code": 0, "message": "ok",
  "data": {
    "scan_id": "s-9f1c2a3e-...", "agent_id": "agent-home-lan", "cidr": "192.168.1.0/24",
    "status": "completed",
    "progress": { "scanned": 254, "total": 254, "found": 6, "failed": 0 },
    "results": [
      { "ip": "192.168.1.29", "mac": "AA:BB:CC:DD:EE:01", "vendor": "Realtek", "first_seen_at": "2026-11-05T22:45:24Z", "exists": true, "existing_device_id": "dev-...", "agent_id": "agent-home-lan", "subnet": "192.168.1.0/24" }
    ],
    "started_at": "2026-11-05T22:45:22Z", "finished_at": "2026-11-05T22:45:26Z",
    "duration_ms": 4120, "error": ""
  }
}
```

- `status=running` 时 `results` 为 `[]`；`failed` 时 `error` 为失败原因，
  `results` 为 `[]`；`cancelled` 时 `results` 为取消前已确认的发现。
- `progress.total`：网段内主机数（不含 `.0` 与 `.255`）；`failed` 只计
  探测任务本身失败（下发错误、Agent 忙耗尽重试、结果解析失败），单台
  主机无响应是正常阴性结果，不计入。
- 不存在 → 404。

### GET /api/v1/scan

扫描任务列表（内存，最近 20 个摘要，不含 `results`），分页信封。

### DELETE /api/v1/scan/{scan_id}

取消进行中的扫描（服务端向 Agent 发 `cancel` 消息，任务置
`cancelled`，已发现结果保留可导入）。已结束的任务上调用 → 400；
不存在 → 404。成功 200 + 取消后的任务对象（同查询结构）。

### POST /api/v1/scan/import

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

- `scan_id` 必填（须存在；`running` 时导入 → 400“扫描尚未完成”）。
- `devices` 必填，非空数组，上限 100 条；`name` 可选，省略时服务端
  生成 `LAN-<IP 末段>`。
- 导入设备的 `agent_id` = 该扫描任务的执行 Agent（自动关联，禁止
  客户端指定其他 Agent）；`status` 初始 `unknown`；`enabled` 默认 true。
- 逐条 best-effort（不是全有或全无）：

```json
{
  "code": 0, "message": "导入完成",
  "data": {
    "scan_id": "s-9f1c2a3e-...", "agent_id": "agent-home-lan",
    "imported": 1, "skipped": 1,
    "results": [
      { "ip": "192.168.1.46", "mac": "AA:BB:CC:DD:EE:02", "device_id": "dev-new-...", "status": "created", "reason": "" },
      { "ip": "192.168.1.29", "mac": "AA:BB:CC:DD:EE:01", "device_id": "dev-...", "status": "skipped", "reason": "already_exists" }
    ]
  }
}
```

`status` ∈ `created | skipped`；`reason` ∈ `"" | already_exists` /
`invalid_mac` / `invalid_ip` / `invalid_name`。

## 定时任务

任务字段：`name`（1–64 字符，必填）、`cron_expr`（必填，创建时即解析
校验，非法 → 400）、`timezone`（可选，默认 `UTC`，须为合法 IANA 时区）、
`target_type`（`device | group`）、`target_id`（须真实存在，否则 400；
`group` 为空分组 → 400 `empty_target`）、`action`（第一阶段只开放
`wake`，其余 400 `unsupported_action`）、`retry_count`（0–5，默认 1）、
`run_once`（可选布尔，默认 false；为 true 时首次执行后自动禁用）、
`enabled`（默认 true）。`next_run_at` 由服务端计算（cron 下次触发时间，
按任务时区换算为 UTC 返回）；禁用任务或 cron 非法时为 `null`。

### GET /api/v1/tasks

分页信封。任务 DTO 含 `target_name`（目标被删后为 `null`）、
`last_run_at`、`last_run_status`（`success | failed | skipped | partial`，
无运行记录时为 `null`）、`next_run_at`。

### POST /api/v1/tasks

请求体为任务对象；`enabled` 未传默认 true。校验失败 → 400 + §1.2
结构。成功 201 + 任务 DTO（含计算出的 `next_run_at`）。创建后服务端
立即重载调度器。

### PUT /api/v1/tasks/{id}

部分更新；改动 `cron_expr` / `timezone` / `target_*` / `action` /
`retry_count` / `run_once` 时重新走全量校验；不存在 → 404。成功 200 +
重查后的任务 DTO。改动后重载调度器。

### DELETE /api/v1/tasks/{id}

不存在 → 404。成功 200 `"deleted"`，重载调度器。

### POST /api/v1/tasks/{id}/pause · POST /api/v1/tasks/{id}/resume

`pause`：`enabled` → false，重载调度器；已是暂停态 → 400。
`resume`：`enabled` → true，重载调度器；已是启用态 → 400。
响应 200 + 任务 DTO。

### GET /api/v1/tasks/{id}/runs

分页。不存在的任务 → 404。`items` 为运行记录：

```json
{
  "id": "run-...", "schedule_id": "...", "status": "partial",
  "output": "2 台成功，1 台失败：Agent 离线",
  "devices": [
    { "device_id": "dev-1", "name": "LAN-29", "status": "success", "reason": "" },
    { "device_id": "dev-2", "name": "NAS", "status": "failed", "reason": "agent_offline" }
  ],
  "started_at": "2026-11-05T08:00:00Z", "finished_at": "2026-11-05T08:00:03Z", "duration_ms": 3120
}
```

运行状态：`success`（全部成功）/ `partial`（部分成功）/ `failed`
（全部失败）/ `skipped`（零目标或全部跳过）。空目标 / 全禁用记
`skipped`，不再记假成功。每次执行一个独立 Run ID；同一任务的重叠
执行被跳过并记一条 `skipped`（output `"上一次执行尚未结束"`）。

执行语义：重试间隔 500ms 且响应 ctx 取消；单次运行总超时 60s；
每设备 dispatch 10s；WOL 重发次数用 `WAKEHUB_WOL_RETRIES`。

## 操作日志

### GET /api/v1/logs

查询参数（全部可选）：`from` / `to`（RFC 3339 时间范围，含端点）、
`action`（精确匹配，多值用逗号分隔）、`target_type`
（`device | group | agent | schedule | subnet | user`）、`target_id`、
`actor`、`result`（`success | failed`）、`page` / `page_size`
（默认 50，最大 200）。

响应分页信封；`items` 为：

```json
{ "id": "...", "actor": "admin", "action": "device_wake", "result": "success", "target_type": "device", "target_id": "dev-...", "detail": "发送 WOL 魔术包至 AA:BB:CC:DD:EE:01", "ip_address": "192.168.1.51", "created_at": "2026-11-05T22:45:22Z" }
```

`result` 是响应期派生：`action` 以 `_failed` 结尾 → `failed`，其余 →
`success`。审计只写操作、不审计读操作；保留 90 天定期清理；不提供
按条件删除日志的 API。审计事件完整清单见契约 §1.6；`detail` 不得含
明文密码、完整 token、完整 Cookie。
