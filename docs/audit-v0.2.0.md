# WakeHub v0.2.0 升级审计报告

- **审计基线**：`~/workspace/wakehub`，main 分支单 commit `f7e3df4`（36 个受控文件，共 5958 行），v0.1.0
- **审计日期**：2026-11-05
- **审计方式**：只读。三路并行通读全部 36 个文件（后端 Go 16 个文件、前端 web/ 10 个文件、部署与文档 9 个文件），逐项对照任务书核实；结论均以 `文件:行号/函数名` 为证据
- **背景事实**：v0.1.0 已在用户 VM（Ubuntu，192.168.1.65，局域网 192.168.1.0/24）实测闭环——Docker 构建、安装、扫描、导入、唤醒均可用；本机沙箱跑不起 Docker 容器（runc setns 报错）
- **本报告不改代码**，只给核实结论与实施计划

## Verdict 口径

- **属实**：任务书指出的问题 / 缺口真实存在于代码中
- **部分属实**：部分子项已满足或描述有偏差，其余缺口真实存在
- **不实**：任务书对现状的描述与代码不符（该问题不存在或已解决）
- 严重程度：P0 = 可导致接管 / 核心功能失效；P1 = 真实安全或功能缺陷；P2 = 正确性 / 健壮性问题
- 修复难度按改动范围评：低（数行～一个函数）、中（跨函数 / 跨文件）、高（架构级 / 需特权能力）

## 总览

| 分类 | 属实 | 部分属实 | 不实 |
|---|---|---|---|
| P0（任务书第 1–7 项） | 6（1、2、3、4、5、7） | 1（6） | 0 |
| P1 后端（第 8–19 项） | 7（8、9、10、11、12、13、15） | 5（14、16、17、18、19） | 0 |
| UI/UX（第 20–30 项） | 5（25、26、27、28、30） | 6（20、21、22、23、24、29） | 0 |
| 前端工程（§六） | 1 | 0 | 0 |
| 测试与验收（第 31–34 项） | 2（31、32） | 2（33、34） | 0 |
| **合计** | **21** | **14** | **0** |

**没有整项不实的任务书条目。** 但有 6 处子项描述与现状不符（见“任务书夸大 / 不实的子项”一节），以及 README 本身有 4 处夸大宣称（见同节）。

---

## 一、逐项核实表

### P0：严重 Bug 与安全问题（第 1–7 项）

| # | 任务书 claim | Verdict | 证据 | 严重 / 难度 |
|---|---|---|---|---|
| 1 | Agent 重连竞态：旧连接关闭误删新连接 | **属实** | `internal/agent/hub.go:83-101` `RegisterConn`：L86-87 持锁关闭旧连接，L89 替换新 session；L94-99 的 `defer` 无条件 `delete(h.sessions, agentID)` + `SetAgentStatus("offline")`。旧连接 `ReadJSON` 报错返回后，其 defer 删掉新连接的 session 并置 offline。无 session 身份比对，无任何并发/重连测试 | P0 / 低 |
| 2 | 多 Agent 共用一个全局 Token | **属实** | `internal/api/router.go:132-140` `handleAgentWS`：`token != s.cfg.AgentRegToken` 单一全局比对；`internal/config/config.go:44,55` 服务端与 Agent 默认值同为 `"local-agent-secret-token"`；身份完全由客户端 `X-Agent-ID` 头决定（router.go:137），持全局 Token 可冒认任意 AgentID；`migrations/001_init.sql:31-38` 的 `agent_tokens` 表（`token_hash`、`revoked`、FK 级联）**存在但全仓零 Go 代码引用**；`hub.go:91` 注册时 `UpsertAgent(..., true)` 恒授信，无 pending→approved 流程、无签发/吊销/轮换接口 | P0 / 中 |
| 3 | WebSocket `CheckOrigin` 恒返回 true | **属实** | `internal/api/router.go:53-56`：`CheckOrigin: func(r *http.Request) bool { return true }`，逐字属实。无 allowlist，无 Origin 与认证分离设计 | P1 / 低 |
| 4 | 默认密码 admin/wakehub123 与固定示例密钥；无初始化流程、无改密、无重置、无撤销 | **属实** | `internal/config/config.go:41-43`：默认 `admin` / `wakehub123` / `change-me-in-production-wakehub-secret` / `local-agent-secret-token`，无启动拒绝；`internal/auth/auth.go:33-48` `EnsureAdmin` 仅 `users` 表为空时播种，之后永不强制改密；Token 为无状态 HMAC（`VerifyToken` L69-91，仅验签名 + 24h 过期），无 jti、无吊销表，泄露后 24h 内无法作废；全仓 grep `change-password\|ChangePassword\|改密\|修改密码` 在 `internal/`、`web/src/`、`cmd/` 中零命中；`README.md:80-82` 明确要求“改密码需删 `data/wakehub.db` 重建”。**例外**：哈希本身是 bcrypt DefaultCost（auth.go:41,62），属“经评估的安全哈希”，Argon2id 子项不扣分；另全仓无 Cookie，故 HttpOnly/Secure/SameSite 是“不存在”而非“配错” | P0 / 中 |
| 5 | Agent 与 Server 默认明文 WebSocket；无退避、无心跳超时、无消息上限 | **属实** | `config.go:54` 默认 `ws://127.0.0.1:8080/...`；`cmd/server/main.go:53` `ListenAndServe` 纯 HTTP，无 TLS 配置项；`client.go:61` `websocket.DefaultDialer` 无 TLS 配置；重连固定 5s（`client.go:48`），无指数退避/抖动；应用层心跳 15s 存在（`client.go:73-81`，`hub.go:109-111`），但**无 WS Ping/Pong、无读超时、无心跳超时清理**，服务端从不判定僵死连接；两侧均无 `SetReadLimit`；消息 ID 存在（`hub.go:145-149` 每任务 uuid）但无去重/防重复执行；任务超时客户端 8s（`client.go:105`）与服务端 10s（config.go TaskTimeout）不一致，服务端取消不向 Agent 传播 | P1（公网暴露则升 P0）/ 中 |
| 6 | Sleep-on-LAN SSRF 与命令执行风险 | **部分属实** | 属实：`internal/sleep/sleep.go:37` 仅校验 `http://`/`https://` 前缀，**无主机/端口 allowlist、无内网与元数据地址拦截**，`http.Client` 未设 `CheckRedirect`（L45，仅 `Timeout: 5s`），默认跟随最多 10 次重定向——SSRF 成立，可打内网服务与 `169.254.169.254`；无签名认证；http 与 https 同等接受；无 Linux/Windows 本地适配器，只有 `http_sol`（L55-56）。不属实/已满足：sleep.go 全文无 `os/exec`，无任意 Shell 执行；`ValidateAction`（L21-29）白名单仅 shutdown/sleep/hibernate；权限检查靠 `withAuth` 单管理员（router.go:77）+ 审计（router.go:332,336） | P1 / 中 |
| 7 | 设备 Agent 默认分配：表单默认 `local`，实际 Local Agent 用 `agent-home-lan` | **属实** | 后端：`internal/device/repository.go:205-206` `CreateDevice`：`AgentID == ""` → 静默 `"local"`；`internal/agent/hub.go:130-132` `Dispatch`：`""` 或 `"local"` → 本地直接执行；无 Agent 存在性/授权校验；`router.go:376-396` `handleNetworkScan` 只返回发现列表，**后端无导入接口**，扫描结果与执行 Agent 无任何关联记录。前端：`web/src/App.vue:50` `newDevice.agent_id: 'local'`，`:69` `scanForm.agent_id: 'local'`；前端全文无 `agent-home-lan`；后端三处默认值互不一致：`docker-compose.yml:28` 与 `install.sh:114`、`.env.example:10` 是 `agent-home-lan`，`internal/config/config.go:53` 是 `agent-lan-01`，`README.md:118` 写 `agent-lan-01`——`local` 与三者全都不匹配。更严重：`agents` 列表为空时 `App.vue:404-406` 的 `<select>` 无任何 `<option>`，`v-model` 仍是 `local`，`createDevice`（`:163-176`）会直接提交归属不存在 Agent 的设备；全前端无“Agent 离线警告”、无“优先展示在线 Agent”排序 | P1 / 低～中 |

### P1：核心功能逻辑与可靠性（第 8–15 项）

| # | 任务书 claim | Verdict | 证据 | 严重 / 难度 |
|---|---|---|---|---|
| 8 | 批量唤醒无每设备结果、无统计、无任务 ID、无取消；发送成功被当作启动成功 | **属实** | `internal/api/router.go:247-276` `handleBatchWake`：L258-260 查不到或禁用设备直接 `continue`，**静默丢弃**，无失败/跳过明细；响应仅 `{total, success}`（L275），无每设备结果、无批量任务 ID、无耗时、无取消；串行循环，无并发控制也无总超时（每设备 5s 上下文，N 台可累积 N×5s）。`handleWakeDevice`（L243-244）`Dispatch` 成功即记 success；`hub.go` wake 分支只要 `wol.Send` 写出成功就返回 `Success: true`，从不回检设备是否上线 | P1 / 低 |
| 9 | Scheduler：Action 不参与执行；空目标/全禁用假成功；无暂停/重试上限/Run ID/历史 | **属实** | `internal/scheduler/scheduler.go:87-135` `executeSchedule`：L108-113 **硬编码 Dispatch `"wake"`，`s.Action` 从未被读取**；`repository.go:398-400` `CreateSchedule` 只把空 Action 默认成 wake，不校验 Action/Cron/目标；`router.go:407-422` 创建时仅判 Name/CronExpr 非空，无效 Cron 要到 `Reload` 才记日志跳过。`statusStr` 默认 `"success"`、output 默认“唤醒指令已下发”（L101-102）；目标查不到（L92-101 错误被忽略）或全被 `continue` 跳过（L104）时仍写一条 success 的 `task_runs`（L131-134）。无暂停/恢复接口（仅 Enabled 字段 + DELETE 路由，router.go:84-85）；单次/每日/每周无独立语义（只有 Cron）；重试无上限、`time.Sleep(500ms)` 不响应 ctx（L118）；整个组共用 15s 上下文（L88），组大时中途过期；无防重复/防重叠；每设备无独立结果、无每设备 Run ID；无下次执行时间与历史查询接口。已实现的部分：时区经 `CRON_TZ` + `LoadLocation` 校验（scheduler.go:56-77），`last_run_at` 更新（L130） | P1 / 中 |
| 10 | 状态检测：ARP 只是读缓存；三种检测均不完整 | **属实** | `internal/status/status.go:99-122` `checkARP` 只读 `/proc/net/arp`，无主动 ARP 请求、无网卡指定、无缓存过期判断；Ping 走 `exec.CommandContext("ping",...)`（L81），权限不足时识别为 Unknown（L89-93）是亮点，但失败原因只有固定文案“ICMP Ping 无响应”，不区分不可达/超时；TCP（L49-61）端口关闭即判 Offline——与“端口关闭≠关机”相悖，默认端口 22；`StateChecking`（L20）定义后**从未返回**；返回结构有 `LatencyMs`/`ErrorReason`，但无检测耗时以外的元数据 | P2 / 中 |
| 11 | 扫描靠 TCP 80 间接触发 ARP，非真正主动 ARP 扫描 | **属实** | `internal/scanner/scanner.go:38-72` `ScanSubnet`：L62 对每主机 TCP 80 `DialContext` 250ms 以“轻触刷新 ARP”，随后 `readARPTable`（L74-117）读 `/proc/net/arp`——与 claim 逐字相符。校验只有 `ones < 24` 拒绝与 `ip.IsPrivate()`（L44-49），无按 Agent 的网段白名单、无 Agent 本地接口识别；并发信号量 32（L53）存在，但 `sem <- struct{}{}`（L57）不响应 ctx 取消，且循环会扫网络地址 `.0` 与广播地址 `.255`；无进度/取消/结果预览接口；OUI 表仅 13 条硬编码（L22-36）；后端无导入接口（见第 7 项），“防重复导入/手动勾选”无从谈起 | P2 / 高（真 ARP 需 raw socket/特权） |
| 12 | 设备管理无统一验证层 | **属实** | `repository.go:193-253` `CreateDevice`：仅校验名称非空（L194）与 MAC（`wol.ValidateMAC`，L197，严格正则，wol.go:9）；**无 IP 校验**（无 `net.ParseIP`）、无广播地址校验、端口仅 `<=0` 时给默认值（WOLPort→9、StatusPort→22，L211-221），无 1–65535 上限（超限在 `wol.go:68` 被静默钳制为 9）；不校验 Agent/Group 是否真实存在或已授权、不校验 SleepEndpoint/DeviceType/StatusMethod 枚举。`UpdateDevice`（L255-274）连名称非空都不校验，且**不检查存在性**（无 `RowsAffected` 校验，全仓 grep 为空）；`router.go:196-216` 更新/删除不存在的 ID 均返回成功，更新错误一律映射 400、从无 404。DB 侧：devices 表无 agent_id/group_id 外键、无端口 CHECK、无 MAC 唯一约束（仅普通索引，migration L56-58） | P1 / 低～中 |
| 13 | 登录限流键用了含随机源端口的 RemoteAddr | **属实** | `internal/api/router.go:119` 把 `r.RemoteAddr`（`IP:随机源端口`）直接传给 `Authenticate`；`internal/auth/auth.go:106-124` 以该完整字符串为限流键——每次 TCP 连接源端口都不同，8 次/5 分钟阈值（L116）实际不可达，**限流名存实亡**。无 `X-Forwarded-For` 处理（既不信任也不解析，反向代理后所有用户共享代理 IP，同样失效）；无账号维度限流；`failures` map 只清理被查询到的 key（L109-116），`recordFailure` 对每个新 key 无限追加、无全局清理/容量上限，内存随不同源端口增长。已实现：登录失败有审计（router.go:121） | P1 / 低 |
| 14 | Agent Goroutine 泄漏 | **部分属实** | 属实：`client.go:104-113` 每条任务 `go func(m Message)` 无并发上限；任务结果 `sendChan <- Message`（L109）是**无条件阻塞发送**——写协程退出（ctx 取消/连接断开）后，在途任务协程永久阻塞泄漏；`sendChan`（L68，缓冲 32）从不关闭；无 `SetReadDeadline`/`SetReadLimit`/Pong 处理；重连固定 5s（L48）。部分：每连接独立 Context 只实现一半——`connectAndServe` 复用父 ctx、任务 ctx 带 8s 超时（L105），但无每连接子 ctx，重连间不隔离；写路径已串行化（`writeMu`，L22-30，旧版无锁双写已修） | P1 / 低～中 |
| 15 | 各入口权限与状态校验不一致，无统一校验逻辑 | **属实** | 全仓无共享校验函数。三个入口三种规则：单设备唤醒（router.go:218-245）只查存在性，**禁用设备也能唤醒**；批量唤醒（L258-260）跳过禁用；调度器（scheduler.go:104）跳过禁用。`handleCheckDevice`/`handleSleepDevice` 均不查启用状态；没有任何入口在 Dispatch 前校验 Agent 存在/已授权/在线（仅靠 sessions map 缺失报错，hub.go:134-137）；无“设备属于该 Agent”校验；无操作类型白名单 | P1 / 中 |

### P1：后端工程质量（第 16–19 项）

| # | 任务书 claim | Verdict | 证据 | 严重 / 难度 |
|---|---|---|---|---|
| 16 | API 安全与规范化缺失 | **部分属实** | 已实现：统一 `apiResponse{code,message,data}`（router.go:460-467）、除登录/Agent WS/SPA 外全部 `withAuth`（router.go:64-86）。缺失属实：无任何 Body 尺寸上限（`MaxBytesReader` grep 为空）；JSON 解码错误被忽略三处——`handleSleepDevice`（router.go:315）、`handleCheckDevice`（L300）、`handleNetworkScan`（L393）；仅 `ReadHeaderTimeout: 5s`（main.go:48），无 ReadTimeout/WriteTimeout/IdleTimeout；错误码映射混乱（更新恒 400、删除恒 500、创建 400 含 DB 错误）；`ListDevices` 无分页、`ListLogs` 固定 80 条且无筛选/排序参数；无 API 限流、无幂等键、无敏感操作二次确认；审计只覆盖写操作，读操作无审计 | P1 / 中 |
| 17 | 数据库：无正式 Migration 版本机制等 | **部分属实** | 已实现：`migrations/001_init.sql:1-2` 有 `PRAGMA foreign_keys = ON` 与 `journal_mode = WAL`；部分索引存在（devices 的 agent/group/mac、audit_logs 的 created_at）；写路径多用 `time.Now().UTC()`。缺失属实：**无正式 Migration 版本机制**——`InitDB`（repository.go:88-109）每次启动全量执行单个 `001_init.sql`，`IF NOT EXISTS` 使既有库的 schema 变更无法生效，实质只能删库升级；无 `busy_timeout`；所谓连接池是 `SetMaxOpenConns(1)`（L98），且外键 pragma 按连接生效、仅靠单连接数维系；缺索引（`wake_tasks.device_id`、`task_runs.schedule_id`）；devices 表无 agent/group 外键；删除依赖 FK 级联（`DeleteDevice` L276-279、`DeleteSchedule` L416-419）却无兜底；无备份/恢复/升级前备份接口；无敏感数据加密；DB 目录 `MkdirAll 0755`、文件权限无显式设置 | P1 / 中 |
| 18 | 日志与审计不完整 | **部分属实** | 已实现：`audit.go:29-62` 的 `Record`/`ListRecent`，覆盖登录成功/失败、设备增改删、唤醒、批量唤醒（仅一条聚合记录）、睡眠、分组创建、扫描、定时创建/删除；审计 detail 不含明文密码/完整 Token。缺失属实：`Record` 用 `_, _ =` 丢弃写入错误（audit.go:30-35），审计丢失无声无息；**缺失项**：设备状态检测、Agent 注册/吊销/连接、定时任务执行（只有 task_runs 行）、所有读操作、系统异常；`ListRecent` 仅按时间倒序取 N 条（router.go:433-441 固定 80），无按时间/设备/操作类型筛选、无分页参数；无日志清理/留存策略；无统一结构化日志（`log.Printf` 散落在 main.go、scheduler.go、client.go） | P2 / 低～中 |
| 19 | Docker 部署优化 | **部分属实** | 已实现：无 `privileged: true`；Local Agent `network_mode: host`（docker-compose.yml:24）；Server 与 Agent 网络分离（server 走 bridge + 端口映射，agent 走 host）；数据目录命名卷持久化（`wakehub_data:/app/data`，`Dockerfile:28` `VOLUME ["/app/data"]`）；三阶段构建，最终 `alpine:3.20` 仅含两二进制 + 前端产物。缺失属实：**无 HEALTHCHECK**（Dockerfile 全文 30 行无 `HEALTHCHECK`；compose 两服务均无 `healthcheck`；`depends_on` 无 `condition: service_healthy`）；**无 AMD64/ARM64 配置**（无 `--platform`、`platform:` 或 buildx 配置）；**容器全程 root**，无 `USER`、无 `cap_add`/`cap_drop`、无 `read_only`、`no-new-privileges`（最小权限未做，不是“给了多余权限”，而是“什么都没收”）；**生产可用默认密码直接启动**（compose `:13-16` 与 config 默认值使无 `.env` 时直接以 `admin/wakehub123` + 固定示例密钥启动，无启动校验拦截）；无 `.dockerignore`（构建上下文含 `web/node_modules/`、`bin/`、`.git`）；`Dockerfile:4-6` 只拷 `package.json` 不拷 `package-lock.json`，`npm install`（L5，非 `npm ci`）不可复现。措辞级偏差：Go `log` 默认写 stderr 而非 stdout，但 Docker 同时采集两者，实际运维无影响 | P1 / 低～中 |

### UI/UX（第 20–30 项）

| # | 任务书 claim | Verdict | 证据 | 严重 / 难度 |
|---|---|---|---|---|
| 20 | 视觉规范缺失：无主题、无 Design Tokens、状态只用颜色等 | **部分属实** | 属实：**只有深色**——`App.vue:283` 根节点 `bg-slate-950 text-slate-100`，`index.html:11` 的 `<body>` 同样写死 `bg-slate-950`；全文无主题切换按钮、无 `prefers-color-scheme`、无 CSS 变量；`style.css` 仅 3 行 Tailwind 指令，`tailwind.config.js:5-7` 的 `theme.extend` 是空对象——没有任何 Design Tokens，颜色全是行内 `slate/blue/emerald/rose/amber` 散写。**状态并非只用颜色，任务书此点表述过严**：设备状态徽标 `App.vue:434-441`、Agent 徽标 `:467` 都是颜色 + 文字（`{{ d.status }}`），但文字是后端原值英文（`online/offline/unknown`），且**无图标**——全文无任何 `<svg>`/图标/emoji。**“无意义统计卡片”不成立**：Dashboard 6 张卡（`App.vue:339-364`）全部取自真实接口 `/api/v1/dashboard`，不是虚假统计；但卡片只有数字、无趋势/说明，信息密度低。另：全文无 `gradient` 类，视觉问题在另一头——每个卡片都是 `bg-slate-900 border border-slate-800 rounded-xl` 的重复边框，层级主要靠边框 + 灰度区分 | P1 / 中 |
| 21 | 桌面端应为左侧固定导航 + 顶部标题栏 + 右侧内容 | **部分属实** | 现状是**顶部标题栏 + 横向 Tab 按钮 + 内容区**，不是左侧固定导航。`App.vue:311-334`：标题“WakeHub 控制台”+ 一排 Tab 按钮（仪表盘/设备管理/Agent 节点/定时任务/局域网扫描/审计日志，`:318-330`，`activeTab` 切换 `v-if` 区块）+ 刷新/退出按钮；无左侧导航、无 Settings 入口。顶部栏缺失：无页面标题（各 section 只有小字 `h2`）、无系统状态、无 Agent 在线状态、无主题切换、无用户菜单（只有“退出”按钮 `:332`）。唯一符合的点：内容区 `max-w-7xl w-full mx-auto px-4`（`:311`），算统一最大宽度；表格/搜索/筛选/分页——**表格不存在**（设备是卡片网格 `:427`，日志是 div 列表 `:521`），搜索见第 24 项，分页全站为零 | P1 / 中 |
| 22 | 移动端优先重构 | **部分属实** | 无底部导航，全站唯一导航是顶部横向 Tab（`App.vue:318-330`）；无“更多”入口、无移动端聚合页。**操作按钮全不达标**：Tab/唤醒/检测/删除/查询均为 `py-1.5`（≈28px 高，`:328`、`:376-377`、`:449-451`）；扫描“选择导入”`py-1`（≈26px，`:513`）；最大的登录按钮 `py-2.5` 也仅 ≈42px（`:303`）；全文无 `44`、`h-11`、`h-12`、`min-h-[44`。**输入框不达标**：全部 `py-2 text-xs`（≈34px 高、12px 字，`:401-403`、`:419`、`:478-480`、`:505`），12px 字体在 iOS 上聚焦会触发自动缩放。**无 iOS Safe Area**：`index.html:5` viewport 是 `width=device-width, initial-scale=1.0`，无 `viewport-fit=cover`；全文无 `safe-area-inset-*`、`env(`。**无表单分组/折叠、无抽屉/弹窗组件**，全站表单都在当前页。**二次确认部分缺失**：删除设备有原生 `confirm`（`App.vue:199`），删除任务**无确认**（`deleteTask`，`:240-249` 直接删），关机功能不存在。**加载反馈弱**：无任何 loading/disabled/skeleton 状态；仅 `showToast` 文字提示（`:73-79`），且成功与失败**同一样式**（蓝色条，`:285`），无法区分；扫描进行中只有一句 toast（`:253`），无进度条/禁用态，可重复点击。符合的点：设备列表移动端默认 1 列（网格 `grid md:grid-cols-2 lg:grid-cols-3`，`:427`）；操作全是 click 按钮，不依赖 Hover；但**无任何防溢出措施**：全文无 `truncate`/`break-all`/`overflow-x`，长 MAC/IP 以 `font-mono` 裸排（`:442-446`、`:511`），窄屏下存在挤出风险（未实测渲染，仅代码层面无防护） | P1 / 低～中（触控尺寸与 Safe Area 为 P1） |
| 23 | Dashboard 首页重构 | **部分属实** | 现有内容（`App.vue:337-396`）：6 张统计卡（设备总数/在线/离线/未知/Agent 总数/在线 Agent，`:339-364`）+ 快捷唤醒列表（`:366-398`）+ 最近 8 条审计日志（`:382-396`）。**数据真实**：全部来自 `/api/v1/dashboard`、`/api/v1/devices`、`/api/v1/logs`，无虚假统计；但**缺失** Server 状态、DB 状态、运行时间——`stats` 初始值（`:27-38`）里根本没有这些字段。**“即将执行的任务”取了但没渲染**：`stats.upcoming_tasks` 在初始值（`:36`）和接口返回中都存在，全模板零引用——死数据。**“常用设备”不成立**：快捷唤醒列的是**全部**设备（`v-for="d in devices"`，`:371`），无常用/收藏/使用频率排序。刷新：仅手动“刷新”按钮（`:331`），无自动刷新（全文无 `setInterval`）；无加载状态 | P1 / 低～中 |
| 24 | Devices 设备管理页面重构 | **部分属实** | 设备卡片（`App.vue:427-455`）：有名称、状态徽标、MAC、IP、Agent、检测方式、WOL 端口、上次错误（`:442-446`）；**缺**：类型图标（`device_type` 字段存在于接口定义 `:12` 与新建默认值 `'pc'`（`:53`），但表单无类型选择、卡片无渲染）、最后检测时间（只有 `last_status_error`）。操作仅唤醒/状态检测/删除（`:449-451`），**无编辑、无详情页、无关机、无睡眠**。搜索**半残**：`filteredDevices`（`:275-276`）是 `computed(() => devices.value)`，完全忽略 `searchQuery`；搜索只在回车/点“查询”时作为 `?q=` 发给后端（`:146`、`:419`）；无分组、无状态/类型筛选、无分页。移动端一列属实（`:427` 默认 1 列），唤醒主按钮较明显（`:449` `flex-1 bg-blue-600`），但删除按钮同行并列，无“更多”收纳 | P1 / 中 |
| 25 | 新增/编辑设备分组表单 | **属实** | **要求基本全未实现**：无分组、无折叠——单个 grid 表单（`App.vue:400-415`），广播地址/WOL 端口/检测方式与基础字段平铺；字段缺失：接口定义的 `status_port`、`sleep_type`、`sleep_endpoint`、`group_id`、`enabled`、`remark` 在表单中**无输入框**（`remark`/`status_port` 只在 `newDevice` 默认值里，`:59`、`:58`，用户改不了）；`device_type` 同样无选择框，永远是 `'pc'`；MAC 无格式化、无实时校验（`App.vue:402` 只有 `required` + placeholder，无 `@input` 格式化、无前端校验，错误只靠后端返回进 toast）；默认 Agent 错误且无离线警告（见第 7 项）；**编辑不存在**：全文无编辑函数/编辑表单。唯一符合的点：保存后调 `refreshAll` 刷新列表（`:174`）。注：此项缺口比任务书措辞更大——不是“重构表单”，而是“编辑功能从零新建” | P1 / 中 |
| 26 | 扫描页面向导化 | **属实** | 现状（`App.vue:498-516`）：一个 Agent 下拉 + 一个 CIDR 自由文本框 + “开始 ARP 扫描”按钮，无向导步骤；扫描中只有 toast 文字（`:253`），无进度。结果行（`:511-514`）只显示 IP/MAC/厂商，**无**是否已存在、网段、发现时间；**无**复选框/全选/反选/批量导入；逐个“选择导入”也**不是真导入**——`importScannedDevice`（`:266-273`）只是把 IP/MAC/名称填进设备表单并切 Tab，仍需用户手动点“新增设备”，且会**静默覆盖表单中未保存的内容**。CIDR 是自由文本输入（`:505`），无“授权网段”概念，前端不校验网段是否在 Agent 授权范围内 | P1 / 中 |
| 27 | Agents 节点管理页面 | **属实** | 现状（`App.vue:458-471`）：仅展示名称/ID（`:464`）、网段（`a.subnet`）、版本（`a.version`）、在线状态徽标（`:467`）。**缺**：IP、OS、CPU 架构、最后心跳、管理网段（只有 `subnet` 一个字符串）、连接状态详情；**无**添加/注册授权/吊销 Token/重新签发/禁用/删除/节点日志/节点设备等任何操作——该 section 是纯只读列表 | P1 / 中 |
| 28 | Schedules 定时任务页面 | **属实** | 现状（`App.vue:476-496`）：任务名 + Cron 自由文本 + 时区自由文本 + 目标设备下拉（`:478-483`），无可视化创建（无单次/每日/每周选择器、无动作选择——动作隐含为唤醒、无启用开关）。列表行（`:491-493`）只显示名称、Cron、时区、目标 ID；**无**最近执行/下次执行/结果/失败原因/历史；删除无确认（见第 22 项）；无底部抽屉/独立页面 | P1 / 中 |
| 29 | Activity Logs 操作日志页面 | **部分属实** | 现有（`App.vue:519-530`，Dashboard 内另有最近 8 条 `:382-396`）：显示操作类型、详情、操作者、IP、时间（`:523-526`）。**缺**：成功/失败筛选（日志行无成功失败字段渲染）、任何筛选控件、分页（全量渲染 `v-for="l in logs"`）、详情展开。**不泄露密码/Token**：前端渲染的字段中未见密码/Token（`detail` 是后端提供的字符串，前端未做脱敏处理——若后端 `detail` 含敏感信息，前端会原样输出，此为后端责任） | P2 / 低～中 |
| 30 | Settings 设置页面 | **属实** | **页面不存在**：`activeTab` 联合类型（`App.vue:26`）只有 6 个值：`dashboard | devices | agents | tasks | scan | logs`；模板 6 个 section 与之一一对应，无 Settings 入口、组件或路由。基础/安全/用户管理/API Token/通知/网络/DB 备份/系统信息/关于——**全部不存在** | P1 / 中 |

### 前端工程重构（§六，未编号）

| 任务书 claim | Verdict | 证据 | 严重 / 难度 |
|---|---|---|---|
| 所有业务写在 App.vue 中，需按模块拆分 + Router + Pinia + 统一 API Client 等 | **属实** | `App.vue` 532 行，含全部接口定义（`Device`，`:4-22`，其余实体全是 `any`）、全部状态（`:24-71`）、全部 API 与业务函数（`apiFetch`、登录、CRUD、扫描、任务，`:73-279`）、全部 6 个页面模板（`:283-530`）。`src/` 下只有 3 个文件：`App.vue`、`main.ts`、`style.css`。`main.ts`（11 行）只 `createApp(App).mount('#app')` 并注册 SW（`:5-9`）；`package.json` 依赖只有 `vue`，无 `vue-router`、`pinia`、`axios`；所谓“API Client”只是 `App.vue:80-98` 内联的 `apiFetch`；Toast 是单个 `ref` + 固定 div（`:27`、`:73-79`、`:285`），无 Toast/Dialog/表单校验/状态徽标/分页等公共组件；设备卡片是模板内联 `v-for`，无组件。**重复请求**：每次操作后都全量 `refreshAll`（5 个接口 `Promise.all`，`:144-150`），唤醒/检测/删除/新建后各调一次；无请求去重/缓存。**但“页面切换造成重复定时器”不成立**：全文无 `setInterval`，无自动刷新，故无重复定时器问题；相关实锤另有两处：① 无 `AbortController`/取消逻辑，`refreshAll` 的 `Promise.all` 无竞态防护，快速连续点击“刷新”会以到达顺序覆盖状态；② `showToast` 的 `setTimeout`（`:75`）在组件卸载时不清（全文无 `onUnmounted`），属轻微泄漏；③ `Promise.all` 是“全有或全无”，任一接口失败则 5 个接口结果全丢（`:151-157` 整个 catch），应为 `allSettled` | P0（结构性）/ 高 |

### 测试与验收（第 31–34 项）

| # | 任务书 claim | Verdict | 证据 | 严重 / 难度 |
|---|---|---|---|---|
| 31 | Go 后端测试严重不足 | **属实** | 全仓仅有的测试是 `internal/wol/wol_test.go` 的两个函数；`tests/` 为空目录；无 Agent 注册/重连/并发、Token 吊销、登录限流、API 权限、定时任务、数据库迁移、备份恢复、Sleep 安全等任何测试；无 race 测试 | P1 / 中 |
| 32 | 前端测试缺失 | **属实** | `package.json` 的 scripts 只有 3 条：`build: vite build`、`dev: vite`、`preview: vite preview`——无 `test`、`typecheck`、`lint`；无 vitest/jest/playwright/cypress 依赖。**无 `tsconfig.json`**（`web/` 下不存在），`typescript` 虽在 devDependencies（`^5.6.3`）但无任何脚本调用它；`build` 是纯 `vite build`（esbuild 只剥离类型、不做类型检查），即 TypeScript 类型错误**永远不会在构建时被发现**。无任何测试文件：`web/`（排除 `node_modules`、`dist`）共 10 个文件，零 `*.test.*` / `*.spec.*` | P1 / 中 |
| 33 | Docker 验收 | **部分属实** | 验收要求本身合理，但**当前有两项验收无物可验**：无 healthcheck 定义（见第 19 项），Healthcheck 验证须先修代码；无 ARM64 配置，ARM64 构建须先配 buildx。另见“验证矩阵”：`docker compose config` 本机可做；`build/up/ps/持久化/唤醒/远程 Agent` 必须到用户 VM/真实局域网 | P1 / — |
| 34 | 真实网络测试（12 项） | **部分属实** | 12 项中仅第 1 项（同一局域网唤醒）已在用户 VM 上验证通过（2026-11-05，LAN-29 一键唤醒发包成功，审计日志 device_wake）；其余 11 项（不同网段唤醒、两个 Agent 管理不同局域网、Agent 断网恢复、Server/Agent 重启恢复、设备离线检测、扫描、多设备批量唤醒、定时任务、手机网络远程唤醒、无权限用户无法执行危险操作）**均未验证**。另：当前单管理员模型下“无权限用户”不存在，该项须等用户管理/权限模型落地后才能验收 | P1 / — |

---

## 二、任务书夸大 / 不实的子项（无整项不实）

| 子项 | 任务书的说法 | 实际情况 |
|---|---|---|
| §六“页面切换造成重复定时器” | 列为须修复的问题 | 全文无 `setInterval`，无自动刷新，该问题不存在。真实问题是 `refreshAll` 无竞态防护与 `Promise.all` 全有或全无（见 §六） |
| 第 6 项“不允许通过 API 执行任意 Shell 命令” | 列为须修复的风险 | `sleep.go` 全文无 `os/exec`，该风险不存在。真实风险是 SSRF（见第 6 项） |
| 第 4 项“管理员密码使用 Argon2id” | 暗示当前哈希不安全 | 当前已用 bcrypt DefaultCost（`auth.go:41,62`），属经充分评估的安全哈希；真正缺失的是强制改密、重置、撤销（见第 4 项） |
| 第 20 项“避免无意义的统计卡片” | 暗示当前卡片虚假 | 6 张卡全部取自真实接口 `/api/v1/dashboard`，数据真实；真正的问题是信息密度低、无趋势/说明 |
| 第 17 项“开启外键 / WAL / UTC 存储” | 列为缺失项 | 三项均已实现：`001_init.sql:1-2` 的 `PRAGMA foreign_keys = ON` + `journal_mode = WAL`（单连接下实际生效）、写路径统一 `time.Now().UTC()`；真正缺失的是 Migration 版本机制、缺失的 FK 与索引、备份 |
| 第 14 项“每个连接使用独立 Context” | 暗示完全缺失 | 已实现一半：任务 ctx 带 8s 超时（`client.go:105`），写路径已串行化（`writeMu`）；真正缺失的是每连接子 ctx、发送通道阻塞泄漏、无并发上限 |

### README 本身的夸大宣称（任务书原则第 7 条命中，属审计新发现的文档问题）

| README 宣称 | 实际情况 |
|---|---|
| 架构图“HTTPS + Bearer Token”（`README.md:10`） | **不实**。Server 是纯 HTTP：`cmd/server/main.go` 用 `http.Server` + `ListenAndServe`，全仓库 grep `ListenAndServeTLS\|TLS` 在 `cmd/`、`internal/` 零命中；且 `README.md:196` 自相矛盾地承认“Server 与 Agent 之间暂无 TLS”。浏览器到 Server、Agent 到 Server 均为明文 |
| “登录限流（同一 IP 5 分钟失败 8 次后拒绝）”（`README.md:50-51`） | **不实（实现使其失效）**。限流逻辑本身存在（`auth.go:106-117`），但调用方传错 key：`router.go:119` 传 `r.RemoteAddr`（`IP:端口`），同一攻击 IP 每次都是新 key，限流永不触发；同 bug 波及审计日志的 `ip_address` 字段 |
| “厂商 OUI 识别”（`README.md:43`） | **部分属实**。`scanner.go:21-35` 仅 13 条硬编码前缀，其余一律 `Unknown Vendor` |
| “全部管理操作写审计日志”（`README.md:51-52`） | **部分属实**。`POST /api/v1/devices/{id}/check`（状态检测，`router.go:278-305`）全程无 `Record` 调用；读操作未审计尚可接受，“全部”一词不成立 |
| 交付物缺失 | 仓库仅 `README.md` 一个文档文件；CHANGELOG.md、升级文档、安全说明、独立 API 文档、独立安装部署文档**全部不存在**；`docs/`、`scripts/`、`tests/` 均为空目录 |

---

## 三、任务书未提及的新发现（28 项，已去重）

去重说明：三路审计共报 34 条新发现，其中部署路的“限流失效”（= 第 13 项）、“CheckOrigin 全放行”（= 第 3 项）、“占位密钥互不一致”（= 第 4 项子项）、“agent_tokens 死表”（= 第 2 项子项）与后端/前端报告重复，已合并，不重复计数。

### 后端（15 项，B-N1～B-N15）

| # | 问题 | 证据 | 严重 |
|---|---|---|---|
| B-N1 | `handleSleepDevice` 忽略 Body 解码错误，畸形请求默认执行睡眠 | `router.go:312-317`：`_ = json.NewDecoder(r.Body).Decode(&req)`，随后 `req.Action == ""` 即默认 `"sleep"` 并下发——POST 任意非法 JSON 都会触发睡眠 | P1 |
| B-N2 | `handleCheckDevice` 忽略 Agent 结果解析，写脏状态入库 | `router.go:299-301`：`_ = json.Unmarshal(res.Data, &st)` 后直接 `UpdateDeviceStatus(d.ID, string(st.State), ...)`；解析失败时 `st.State == ""`，devices.status 被写成空字符串（不在 online/offline/unknown/checking 枚举内），仪表盘统计将其误归入 unknown | P1 |
| B-N3 | `handleNetworkScan` 同样忽略解析错误 | `router.go:393`：失败时返回 `ok` + `data: null` | P2 |
| B-N4 | `handleUpdateDevice` 返回请求体而非重查行 | `router.go:196-208`：返回的 `d` 是客户端传来的对象，`id`/`created_at` 可能与路径 ID/真实行不一致 | P2 |
| B-N5 | 更新/删除不存在的设备或定时任务返回成功 | `repository.go:255-279`（Update/DeleteDevice）、`416-419`（DeleteSchedule）均不查 `RowsAffected`；对应 handler（router.go:196-216、425-432）照常返回成功 | P1 |
| B-N6 | 用户名枚举时序侧信道 | `auth.go:56-66`：用户不存在时直接返回，未做 dummy bcrypt；存在时走 `bcrypt.CompareHashAndPassword`，时序可区分用户名是否存在 | P2 |
| B-N7 | Agent Token 比对非恒定时间 | `router.go:133-134` 用 `!=` 比较 Token | P2 |
| B-N8 | 远程 Agent 可冒认内置 `local` Agent | `router.go:137-140` 接受任意 `X-Agent-ID`（含 `"local"`）；`repository.go:335-338` 的 Upsert 会覆盖种子行 `local` 的 name/subnet；`hub.go:130` 的 local 短路使该 session 永不被用于分发，但 Agents 列表会被投毒 | P2 |
| B-N9 | 调度器启动失败仅记日志，服务带病运行 | `cmd/server/main.go:36-39`：`sched.Start()` 出错只 `log.Printf` 警告，`cron.Start()` 未执行，所有定时唤醒静默失效且无重试 | P1 |
| B-N10 | Ping 检测参数注入与 locale 依赖 | `status.go:81` 将未校验的 `ip` 直接作 `ping` 参数（以 `-` 开头的输入可被解析为 ping 选项）；L90-93 仅匹配英文 `"operation not permitted"`/`"permission denied"`，非英文 locale 下权限不足会被误判为 Offline 而非 Unknown | P2 |
| B-N11 | `wol.Send` 广播地址未校验、端口静默钳制、睡眠不响应 ctx | `wol.go:68` 端口越界时静默改为 9 而非报错；`wol.go:86-103` 用 `fmt.Sprintf("%s:%d", BroadcastAddress, Port)` 直连任意地址（可为单播 IP）；`time.Sleep(opts.Interval)`（L103）不监听 `ctx.Done()` | P2 |
| B-N12 | `Dispatch` 查 session 与写之间存在 TOCTOU | `hub.go:134-137` 在 RLock 下取 session、解锁后 `sess.writeJSON`（L158）；期间若新连接注册替换了 session，写操作会发往已关闭的旧连接（仅表现为一次可重试的失败，但与第 1 项同源） | P2 |
| B-N13 | `GetDashboardStats` 全表加载后内存计数 | `repository.go:421-448`：`ListDevices("", "")` 拉出全部设备再循环计数，`UpcomingTasks` 为全部 schedules 无上限、无下次执行时间计算 | P2 |
| B-N14 | 用户名含 `|` 时登录后恒失败 | `auth.go:93-99`：`issueToken` 生成 `username|expiry`，`VerifyToken` 要求恰好 2 段；含 `|` 的用户名签发后每次校验都因段数≠2 而失败（用户名本身无字符校验） | P2 |
| B-N15 | `VerifyToken`/`withAuth` 把内部错误原文返回客户端 | `router.go:101-104`：`writeJSON(..., err.Error(), ...)` 直接外泄底层错误文本（含 base64 解码错误等） | P2 |

### 前端（8 项，F-N1～F-N8）

| # | 问题 | 证据 | 严重 |
|---|---|---|---|
| F-N1 | Token 存 `localStorage`，XSS 可窃取 | `App.vue:24` `localStorage.getItem('wakehub_token')`，`:107` 登录后 `setItem`；任意 XSS 即可读走长期有效的 Bearer Token；无 httpOnly Cookie、无过期处理（除 401 清除外，`:89-92`） | P1 |
| F-N2 | 登录页明文写出默认密码 | `App.vue:300` 标签文字就是“密码 (默认 wakehub123)”，且用户名预填 `admin`（`:25`） | P1 |
| F-N3 | PWA 离线能力是反的，manifest 不可安装 | `public/sw.js:3-10` 的 fetch 处理：非 `/api/` 请求全部 `fetch` 直通，失败时返回一段纯文本提示——**无任何 Cache API 缓存**，离线时连 HTML/JS/CSS 都拿不到；`manifest.json` 全文无 `icons` 字段（无 192/512 图标），不满足可安装标准；`main.ts:5-9` 注册 SW 时错误被静默 `catch(() => {})` | P1 |
| F-N4 | API 错误处理丢信息 | `apiFetch`（`App.vue:80-98`）先 `await resp.json()` 再判状态：后端返回非 JSON 错误页（如 502 网关页）时抛 `SyntaxError`，原始错误信息丢失；且成功与失败共用同一个蓝色 toast（`:285`），用户无法区分 | P2 |
| F-N5 | 无防重复提交 | 全部提交按钮（新建设备、扫描、唤醒、批量唤醒）在请求进行中不禁用、无 in-flight 标志，可重复点击造成重复提交/重复扫描 | P2 |
| F-N6 | 删除任务无确认，与删除设备不一致 | `deleteDevice` 有 `confirm`（`:199`），`deleteTask`（`:240-249`）直接发 DELETE | P2 |
| F-N7 | 搜索是死代码 | `filteredDevices`（`:275-276`）忽略 `searchQuery`，属无效 computed（`computed` 导入后仅用于此一处，`:2`） | P2 |
| F-N8 | API 地址 dev 写死、生产无配置 | `vite.config.ts:6-9` 开发代理写死 `http://127.0.0.1:8080`；生产构建的 `apiFetch` 用相对路径 `/api`，要求与服务端同源部署，但仓库中无生产反代/同源部署配置说明 | P2 |
| — | 已核查无问题（阴性结论） | 全文无 `v-html`/`innerHTML`，渲染全用 `{{ }}` 插值（Vue 默认转义），未发现前端 XSS 注入点；无 `eval`/`Function` 动态执行 | — |

### 部署与文档（5 项，D-N1～D-N5）

| # | 问题 | 证据 | 严重 |
|---|---|---|---|
| D-N1 | Server 容器内 `local` 节点 ping 检测默认即残废 | `internal/status/status.go:91` 在 ICMP 失败时提示“容器缺少 ICMP 权限，请改用 TCP 检测或赋予 CAP_NET_RAW”；但 `docker-compose.yml` 未给 server 加 `cap_add: [NET_RAW]`，`Dockerfile` 也未 `setcap`。`local` 内置节点在 server 进程内发包（`hub.go:128-132`），Docker 部署下所有 `local` 设备的 ping 检测默认失败，只能 TCP。README（`:41-42`）把责任推给用户手动加能力，compose 未默认解决 | P1 |
| D-N2 | 定时唤醒的 WOL 重发次数硬编码，忽略配置 | 手动/批量唤醒用 `WAKEHUB_WOL_RETRIES`（`router.go:234,266`），定时唤醒在 `scheduler.go:113` 写死 `Count: 3`。同一产品的同一参数两处行为不一致 | P2 |
| D-N3 | 默认网段与用户真实局域网不符 | 默认值 `192.168.1.0/24` 出现在 `docker-compose.yml:31`、`.env.example:12`、`001_init.sql:131`（种子 `local` Agent 行）三处；用户 LAN 是 `192.168.1.0/24`（VM 192.168.1.65）。v0.1.0 在 VM 上闭环验证过，说明当时有人手动改过；但默认开箱即用的新装会在错误网段扫描/发包。`install.sh` 会提问网段，但默认值误导 | P2 |
| D-N4 | README 与 install.sh 的提权口径冲突 | `README.md:58` 写 `sudo bash install.sh`，`install.sh` 头注释写 `bash install.sh`；`sudo` 运行会在 root 身份下生成 600 权限的 `.env`，后续非 sudo 的 `docker compose` 重跑可能读不到 `.env`。建议统一为“用户在 docker 组内直接跑，缺权限再 sudo” | P2 |
| D-N5 | `Makefile` 的 `docker-up` 与 install.sh 行为分叉 | `Makefile:15-16` `docker-up: docker compose up -d --build` 不经过 install.sh 的 `.env` 生成/端口检查/就绪门；直接 `make docker-up` 会以 compose 固定默认值（含 `admin/wakehub123`）启动。README 未提示此路径的风险 | P2 |

---

## 四、实施计划

### 原则

1. **契约先行**：M0 冻结 API 与数据模型契约，前后端按契约并行，禁止前端凭猜测调未定接口。
2. **串行文件**：`internal/api/router.go`、`internal/device/repository.go`、`internal/agent/hub.go`、`docker-compose.yml`、`web/src/App.vue` 是多模块共用文件，**同一时间只允许一个模块改**，见“串行文件交接表”。
3. **P0 先行**：M1–M2 解决三个 P0（重连竞态、全局 Token、默认凭证）后，再动 P1 功能；UI 重构（M6）不阻塞后端 P0/P1。
4. **不删功能**：已验证可用的 WOL 与扫描链路只修不推翻；真 ARP 若因特权/平台原因做不到，降级方案须写进报告，不得虚报。
5. **每个模块自带测试**：无测试不算完成（见 M8）。

### 模块划分（文件清单互不重叠，阶段之间串行）

#### M0 — 契约与迁移设计（文档，无代码；最先做，阻塞 M1–M6 的接口部分）

- **负责文件**：`docs/contract-v0.2.0.md`（新建）、`docs/migration-002-003.md`（新建）
- **覆盖任务书**：第 2、7、8、9、12、15、16、17、26、27、28、29 项的接口部分
- **内容**：
  - Agent 注册审批状态机（pending → approved → connected）、per-agent token 签发/吊销/轮换接口、WS 握手协议（token 位置、注册字段、错误码）
  - 设备校验错误结构（field + code + message）、404 规范、批量唤醒响应结构（batch_id、per-device results、success/failed/skipped、started_at/finished_at/duration_ms）
  - 扫描导入接口（`POST /api/v1/scan/import`：批量、去重、关联执行 Agent）、扫描任务 ID + 进度/取消
  - 定时任务 run 记录查询、next_run_at、pause/resume 接口
  - 日志筛选参数（时间/类型/设备/Agent/用户/结果）+ 分页参数
  - 认证接口：改密、退出/撤销、登录响应形态（HttpOnly Cookie 还是维持 Bearer，由 M1 定）
  - Dashboard stats 新增字段（server/db/uptime、upcoming_tasks 真实计算）
  - Migration 002（认证：密码修改时间、撤销 token 表）与 003（Agent：审批状态、per-agent token hash）的表结构设计
- **验收**：契约文档评审通过；前后端按此开工。

#### M1 — 认证与启动安全（P0-4、P0-3、P1-13，及 B-N6/B-N7/B-N15）

- **负责文件**：`internal/config/config.go`、`internal/auth/auth.go`、`cmd/server/main.go`、`internal/api/router.go`（本阶段唯一动 router.go 的模块：CheckOrigin、login handler、withAuth、Body 上限）、`migrations/002_auth.sql`（新建）、`.env.example`
- **覆盖**：第 3、4、13、16（部分：超时/Body 上限/错误外泄）项；B-N6、 B-N7、 B-N15
- **内容**：
  - 启动时拒绝默认密码与示例密钥（须同时覆盖三处互不一致的占位值：`change-me-in-production-wakehub-secret`、`replace-with-strong-secret-key`、`replace-with-random-secret-key`，见 D 报告 §1）；首次启动管理员初始化流程
  - 改密接口、密码重置（不删库）、Token 撤销（jti + 撤销表，002）、退出登录；bcrypt 保留（已评估，无需换 Argon2id）；用户名 `|` 校验（B-N14 顺带修，属 auth.go 内）
  - 限流键改为 `net.SplitHostPort` 的 host 部分 + 账号维度限流 + 内存上限与定期清理；反向代理信任须显式配置（默认不信任 `X-Forwarded-For`）
  - `CheckOrigin` allowlist（管理端同源 + 可配置）；`http.Server` 增加 ReadTimeout/WriteTimeout/IdleTimeout；请求 Body `MaxBytesReader`；`withAuth` 不再外泄内部错误原文
  - Token 比对改恒定时间比较（B-N7）
- **验收**：默认密码启动被拒绝；改密后旧 Token 失效；限流对同一 IP 生效（测试）；`go test ./internal/auth/...` 通过。

#### M2 — Agent 身份与连接（P0-1、P0-2，及 B-N8/B-N12）

- **负责文件**：`internal/agent/hub.go`、`internal/agent/client.go`、`internal/agent/store.go`（新建：Agent 相关 DB 操作从 repository.go 抽出）、`migrations/003_agent.sql`（新建）
- **覆盖**：第 1、2、5、14 项；B-N8、B-N12
- **内容**：
  - **竞态修复**：`RegisterConn` 的 defer 在删除前比对 `sessions[agentID] == sess`，只有匹配才删并置 offline；旧连接关闭不持有 hub 锁；`Dispatch` 的 TOCTOU 改为持读锁完成 session 引用 + 写失败重试一次（B-N12）
  - **per-agent token**：激活 `agent_tokens` 表（hash 存储、可单独吊销/重新签发/轮换）；注册审批 pending → approved → connected；Agent ID 与授权身份绑定，`X-Agent-ID` 不再是唯一身份依据；拒绝冒认 `local`（B-N8）；节点禁用/删除接口
  - **安全通信**：WSS/TLS 配置项（默认公网 WSS、验证证书、不跳过验证）；指数退避 + 抖动重连；WS Ping/Pong + 读超时 + 心跳超时清理僵死连接；`SetReadLimit`；任务并发上限；`sendChan` 发送监听 ctx、防永久阻塞、正确关闭通道；每连接独立子 ctx
  - **注意**：`hub.go` 与 M3 有交接——M2 先改，M3 在其后 rebase（见串行表）
- **验收**：并发重连压测（多次同时重连只留有效连接，`go test -race`）；一个 Agent 凭证吊销不影响其他 Agent；僵死连接被心跳超时清理。

#### M3 — 设备域与操作一致性（P1-7、8、12、15、6、10，及 B-N1～B-N5、B-N10、B-N11）

- **负责文件**：`internal/device/repository.go`、`internal/api/router.go`（续改，在 M1 之后）、`internal/wol/wol.go`、`internal/sleep/sleep.go`、`internal/status/status.go`
- **覆盖**：第 6、7、8、10、12、15 项；B-N1～B-N5、B-N10、B-N11
- **内容**：
  - 统一验证层（新增 `internal/device/validate.go` 或 repository 内同名函数）：名称/MAC/IP/广播地址/端口 1–65535/Agent 存在且已授权/Group/SleepEndpoint/类型/检测方式；`UpdateDevice` 补存在性检查（`RowsAffected`），不存在返回 404；更新返回重查行而非请求体（B-N4）
  - 统一操作校验（第 15 项）：单设备/批量/定时共用同一校验（存在、启用、Agent 存在/已授权/在线、设备属于该 Agent、操作允许）；禁用设备唤醒行为统一（不再一处允许一处跳过）
  - 设备 Agent 默认值统一（以后端唯一默认值 `agent-home-lan` 为准，或改为必填——由 M0 契约定）；取消静默回落 `local`；扫描导入关联执行 Agent（配合 M4 的导入接口）
  - 批量唤醒：per-device 结果、成功/失败/跳过统计、批量任务 ID、耗时、并发限制、取消；明确区分“魔术包发送成功”与“设备实际启动成功”（后者须回检，不虚报）
  - Sleep SSRF：Endpoint allowlist（主机/端口）、禁不安全重定向、签名认证、超时、HTTPS 优先；`handleSleepDevice`/`handleCheckDevice`/`handleNetworkScan` 的 JSON 解码错误不再忽略（B-N1、B-N3）；`handleCheckDevice` 解析失败不写脏状态（B-N2）
  - 状态检测：Ping 参数校验防注入（B-N10）、locale 无关的权限不足判定；TCP 端口关闭≠关机；ARP 主动化评估（见 M4）；`StateChecking` 真正返回；检测异常≠离线
  - `wol.Send`：端口越界报错而非静默钳制；广播地址校验；`time.Sleep` 响应 ctx（B-N11）
- **验收**：验证层单测；批量唤醒响应结构与契约一致；畸形 JSON 不再触发睡眠；更新不存在的设备返回 404。

#### M4 — 扫描与调度（P1-9、11，及 D-N2）

- **负责文件**：`internal/scanner/scanner.go`、`internal/scheduler/scheduler.go`
- **覆盖**：第 9、11 项；D-N2
- **内容**：
  - 调度器：`Action` 真正参与执行；未实现 Action 拒绝创建；第一阶段只开放已验证的 Wake；创建时校验设备/组；空目标/全禁用不再记 success；暂停/恢复；单次/每日/每周语义；重试上限且响应 ctx；执行超时；每次执行独立 Run ID、每设备独立结果；最近/下次执行时间；执行历史查询；`Count` 改用 `WAKEHUB_WOL_RETRIES`（D-N2）；启动失败改为致命错误而非仅记日志（B-N9 属 main.go，交 M1 顺带修——见串行表备注）
  - 扫描：评估真 ARP（raw socket / arping，需 `CAP_NET_RAW`，容器内须配能力）；至少做到：按 Agent 的网段白名单、扫描前验证、跳过 `.0`/`.255`、进度/取消/失败数、去重、已存在识别、导入接口（与 M3 对接）；OUI 表扩充或注明覆盖范围
- **验收**：调度器“空目标不再假成功”单测；扫描白名单外网段被拒绝；VM 实测扫描真实网段。

#### M5 — 数据库与审计（P1-17、18，及 B-N13）

- **负责文件**：`internal/device/repository.go`（续改，仅 InitDB/DSN 部分，在 M3 之后）、`internal/audit/audit.go`、`migrations/004_indexes_fk.sql`（新建）、`scripts/backup.sh`（新建）
- **覆盖**：第 17、18 项；B-N13
- **内容**：
  - 正式 Migration 版本机制（版本表 + 按序应用，002/003/004 均纳入）；DSN 加 `_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)`；连接池参数；补缺失索引（`wake_tasks.device_id`、`task_runs.schedule_id`、`schedules.target_id`、`audit_logs.actor`）；补缺失 FK（devices.agent_id/group_id、schedules.target_id）并处理既有孤儿数据；DB 文件权限收紧（0600）；备份/恢复脚本 + 升级前自动备份
  - 审计：`Record` 错误不再静默丢弃；补齐缺失事件（状态检测、Agent 注册/吊销/连接、定时执行、系统异常）；筛选（时间/设备/类型）+ 分页；日志清理/留存策略；结构化日志统一
  - `GetDashboardStats` 改 SQL 聚合计数，不再全表加载（B-N13）
- **验收**：旧库升级演练（v0.1.0 的库不删库升到 004）；备份/恢复往返测试。

#### M6 — 前端重构（第 7 项前端部分、20–30、§六、32，及 F-N1～F-N8）

- **负责文件**：`web/` 全部（`web/src/App.vue` 拆分、`web/src/main.ts`、`web/src/style.css`、`web/index.html`、`web/package.json`、`web/vite.config.ts`、`web/tailwind.config.js`、`web/postcss.config.js`、`web/public/manifest.json`、`web/public/sw.js`，及新建的 `api/`、`components/`、`layouts/`、`pages/`、`stores/`、`router/`、`composables/`、`types/`、`styles/`、`web/tsconfig.json`）
- **覆盖**：第 7、20、21、22、23、24、25、26、27、28、29、30 项；§六；第 32 项；F-N1～F-N8
- **内容**：
  - 按 M0 契约拆分：Vue Router + Pinia + 统一 API Client + Toast/Dialog/表单校验/状态徽标/分页公共组件；设备卡片组件化；`refreshAll` 改 `allSettled` + `AbortController` + 请求去重
  - 桌面端：左侧固定导航 + 顶部标题栏 + 右侧内容；导航 Dashboard/Devices/Network Scan/Agents/Schedules/Activity Logs/Settings
  - 移动端优先：底部固定导航（首页/设备/扫描/任务/更多）；按钮 ≥44px、输入框 46–50px/字体 ≥16px；iOS Safe Area；长 MAC/IP 截断防护；关机/删除二次确认统一；成功/失败 Toast 区分样式；防重复提交；复杂表单用底部抽屉/独立页面
  - 页面：Dashboard（Server/DB/运行时间、即将执行的任务真实渲染、常用设备）、Devices（编辑/详情/关机/睡眠、搜索修复、筛选、分页）、新增/编辑（分组表单、高级折叠、MAC 自动格式化、实时校验）、扫描向导（六步 + 批量导入）、Agents（审批/吊销/重新签发/禁用/删除）、Schedules（可视化创建 + 执行历史）、Logs（筛选 + 分页）、Settings（新建，含改密、备份、API Token）
  - 主题：深色/浅色/跟随系统/手动切换/保存偏好；Design Tokens 落地；状态徽标统一为颜色 + 文字 + 图标
  - 安全：Token 改 httpOnly Cookie（与 M1 契约一致）或至少缩短有效期 + 退出清理；登录页去掉默认密码明文（F-N2）；PWA：SW 预缓存 + manifest 补 icons，或如实降级宣称（F-N3）
  - 工程：补 `tsconfig.json`，`npm run build` 接入类型检查；补 Vitest 组件/表单/路由测试
- **前置依赖**：M0 契约；M1（改密/撤销/退出）、M2（Agent 审批/吊销）、M3（批量唤醒新响应、导入接口）、M4（扫描进度/取消、调度 run 历史）、M5（日志筛选/分页）的后端接口——M6 可先按契约做 UI 与 mock，后端接口落地后再联调
- **验收**：`npm run build` + `tsc --noEmit` 通过；实测断点 320/375/390/430/iPad/1366/1920（见验证矩阵）。

#### M7 — 部署与文档（第 19 项、§八，及 D-N1、D-N3、D-N4、D-N5）

- **负责文件**：`Dockerfile`、`.dockerignore`（新建）、`docker-compose.yml`（续改，在 M1 之后）、`install.sh`、`Makefile`、`README.md`、`CHANGELOG.md`（新建）、`docs/`（新建：`api.md`、`security.md`、`upgrade.md`、`deployment.md`）
- **覆盖**：第 19 项；§八交付物；D-N1、D-N3、D-N4、D-N5
- **内容**：
  - Dockerfile：`HEALTHCHECK`；`npm ci`（拷 `package-lock.json`）；`.dockerignore`（排除 `web/node_modules/`、`bin/`、`.git`）；非 root `USER` + 最小 capabilities；多平台构建配置（buildx）
  - compose：healthcheck + `depends_on: condition: service_healthy`；server 加 `cap_add: [NET_RAW]`（D-N1，local ping 默认可用）；默认值清理（与 M1 的启动拒绝配合）；`platform` 声明；`down -v` 删卷警示进 README
  - install.sh：密钥漏判补齐三处占位值；模式切换先停旧服务；预检 `curl`/`head`/`od`；端口检查无 `ss` 又无 `python3` 时告警而非静默跳过；就绪门改为调登录/健康接口而非 `GET /`；模式 3 不问管理员密码、WS URL 默认值与远端端口一致；`AGENT_SUBNET` 加 CIDR 校验；结束页不再明文打印密码；统一提权口径（D-N4）
  - 默认网段改为用户真实网段或安装时强制确认（D-N3）
  - `Makefile` 的 `docker-up` 改为走 install.sh 或明确标注风险（D-N5）
  - 文档：README 修正全部夸大宣称（HTTPS、限流、OUI、“全部审计”、删库改密）；CHANGELOG.md；API 文档；安全说明；升级文档（v0.1.0 → v0.2.0，不删库）；安装部署文档
- **验收**：`docker compose config` 通过；文档与实际构建逐项核对（审计复核）。

#### M8 — 测试与验收（第 31、32、33、34 项）

- **负责文件**：各包新增 `*_test.go`、`web/` 新增测试文件（归属 M6 的测试部分由 M6 实施，M8 负责验收门）、`tests/`（新建验收用例文档）
- **覆盖**：第 31、32、33、34 项
- **内容**：
  - Go：`go test ./...` + `go test -race ./...`；重点覆盖 WOL、MAC/IP 校验、广播地址、Ping/TCP/ARP、扫描、Agent 注册/重连/并发/断线、Token 吊销、登录限流、API 权限、定时任务、失败重试、数据库迁移、备份恢复、Sleep 安全
  - 前端：组件测试、表单校验、API 错误处理、路由、登录流程、移动端交互
  - Docker 验收与真实网络测试：见“验证矩阵”
- **验收**：测试报告如实区分“已修复 / 已测试通过 / 仅完成代码修改尚未实测 / 尚未实现 / 存在限制”（任务书 §九要求）。

### 串行文件交接表（同一文件，阶段串行，禁止并行）

| 文件 | 改动顺序 | 各阶段改动范围 |
|---|---|---|
| `internal/api/router.go` | M1 → M3 | M1：CheckOrigin、login handler、withAuth、Body 上限；M3：设备/批量/睡眠/检测/扫描 handlers、404 映射 |
| `internal/device/repository.go` | M2（抽出）→ M3 → M5 | M2：Agent 相关 DB 操作抽到 `internal/agent/store.go`；M3：设备验证层、RowsAffected；M5：仅 InitDB/DSN/连接池 |
| `internal/agent/hub.go` | M2 → M3（只读） | M2：竞态修复、授权检查、TOCTOU；M3 不动 hub.go，只调其接口 |
| `docker-compose.yml` | M1（默认值）→ M7 | M1：默认值清理（配合启动拒绝）；M7：healthcheck、capabilities、platform |
| `web/src/App.vue` | M6 独占 | 拆分时先冻结 v0.1.0 的 App.vue 为参照，拆完删除 |
| `cmd/server/main.go` | M1 → M4（备注） | M1：超时/TLS 配置；调度器启动失败改致命错误（B-N9）由 M1 顺带修，因属启动路径 |
| `internal/scheduler/scheduler.go` | M4 独占 | 与 M3 的接口以 M0 契约为准，不直接改对方文件 |

### 阶段顺序与并行度

```
M0 契约 ──► M1 安全地基 ──► M2 Agent 身份 ──► M3 设备域 ──► M4 扫描/调度 ──► M5 数据库/审计
              │                │                │                │
              └──────► M6 前端（按契约先行，接口落地后联调） ◄─────┘
M7 部署/文档（M1 之后可并行，终版文档在 M5 之后定稿）
M8 测试/验收（贯穿各模块完成时，最后汇总）
```

- **可并行**：M6（前端）与 M2–M5（后端）按契约并行；M7 文档与 M3–M5 并行。
- **不可并行**：串行表中的同一文件；M3 依赖 M1 的 router.go；M5 依赖 M2/M3 的 migration 002/003。
- **建议人力**：1 人按 M0→M8 顺序做约需数周；2 人可分“后端（M1–M5）+ 前端（M6）”并行，M0 与 M7/M8 共担。

---

## 五、API 契约变化点（前端重构依赖后端先定接口）

| 变化点 | 现状 | v0.2.0 契约（M0 定稿） | 影响模块 |
|---|---|---|---|
| Agent 注册 | 全局 Token + `X-Agent-ID` 自报身份；注册即授信 | per-agent token；pending → approved → connected；签发/吊销/轮换接口；WS 握手带 token 与注册状态 | M2、M6（Agents 页） |
| Agent 列表字段 | 仅 name/id/subnet/version/status | 新增 status（pending/approved/connected/disabled）、last_heartbeat、os/arch、ip、连接状态 | M2、M6 |
| 设备创建/更新 | 无 IP 校验、无 Agent 校验、更新不存在返回成功 | 统一验证层；结构化错误（field+code）；Agent 必填且须已授权在线；不存在返回 404；更新返回重查行 | M3、M6（设备表单） |
| 批量唤醒响应 | `{total, success}`，静默丢弃 | `{batch_id, total, success, failed, skipped, results[{device_id, status, reason}], started_at, finished_at, duration_ms}`；“发送成功”≠“启动成功” | M3、M6 |
| 扫描导入 | 无后端导入接口，前端“导入”只是填表单 | `POST /api/v1/scan/import`（批量、去重、关联执行 Agent）；扫描任务 ID + 进度/取消接口 | M3、M4、M6（扫描向导） |
| 定时任务 | Action 被忽略；run 记录只有一行且常假成功 | Action 校验；run 记录 per-schedule + per-device；`next_run_at`；pause/resume；`GET /api/v1/schedules/:id/runs` | M4、M6（Schedules 页） |
| 日志查询 | `GET /api/v1/logs` 固定最近 80 条，无筛选 | 筛选参数（时间/类型/设备/Agent/用户/结果）+ 分页；审计事件补齐（检测、Agent 注册/吊销、定时执行） | M5、M6（Logs 页） |
| 认证 | 无状态 HMAC、无撤销、无改密；Token 存 localStorage | 改密/退出/撤销接口；Token 形态由 M1 定（HttpOnly Cookie 优先）；登录不再返回长期 Bearer 给 localStorage | M1、M6（登录/设置） |
| Dashboard | stats 缺 server/db/uptime；`upcoming_tasks` 死数据 | stats 新增 server_status/db_status/uptime；`upcoming_tasks` 真实计算并渲染 | M5、M6（Dashboard） |
| 扫描执行 | 无白名单、无进度 | Agent 网段白名单校验；扫描任务进度/取消 | M4、M6 |

---

## 六、验证矩阵

### 本机可做（沙箱：Go/Node 可用，Docker 容器跑不起来）

| 验收项 | 方法 |
|---|---|
| Go 编译/ vet / 测试 / race | `go vet ./...`、`go test ./...`、`go test -race ./...`（M8） |
| 前端构建 / 类型检查 / 前端测试 | `npm run build`、`tsc --noEmit`、Vitest（M6/M8） |
| compose 配置校验 | `docker compose config`（纯客户端解析，不启动容器） |
| 后端二进制冒烟 | 本机跑 server 二进制：登录、CRUD、空网段扫描、无效输入（不依赖容器与真实 LAN） |
| 代码审计复核 | 本报告的核实表逐项复核（M8） |
| 契约一致性 | 前后端按 M0 契约联调（mock server 可在本机跑） |

### 必须到用户 VM / 真实局域网验证（本机做不了）

| 验收项 | 原因 | 备注 |
|---|---|---|
| `docker compose build` / `up -d` / `ps` | 需 runc 起容器，本机已知失败 | 用户 VM（192.168.1.65） |
| Healthcheck | 须先修代码补 healthcheck 定义，且只能在真实容器里验 | M7 之后，VM |
| 数据持久化（重建/重启不丢数据） | 需真实 `up/down/up` 循环 + 写库读回 | VM；验收用例须明确区分 `down` 与 `down -v`（后者删卷） |
| 真实 WOL 冷启动 | WOL 是二层广播，只在目标广播域生效；沙箱无目标网卡/待唤醒设备 | 真实局域网；目标机须 BIOS 开 WOL、关机后网口灯亮；**纯冷启动开机在 v0.1.0 尚未单独验证** |
| 多 Agent 跨网段 / 远程 Agent 连中心端 | 需第二台部署 Agent 的机器 + 跨网段 WS 注册、心跳、任务下发 | 真实多节点环境 |
| Agent 断网恢复 / Server 重启恢复 / Agent 重启恢复 | 需真实断网与重启操作 | VM + 真实局域网 |
| 设备离线检测 / 扫描真实网段 / 多设备批量唤醒 / 定时任务 | 需真实在线/离线设备与时间流逝 | 真实局域网 |
| 手机网络下远程唤醒 | 需手机 + 公网/远程接入路径 | 用户 iPhone 实测 |
| ARM64 构建 | 需 buildx + QEMU 或远程 ARM builder | VM 本机不可做；可用 Oracle ARM 实例或 buildx |
| 无权限用户无法执行危险操作 | 当前单管理员模型下“无权限用户”不存在 | 须等用户管理/权限模型落地后才能验收 |
| 首次初始化 / 改密 / 删库流程端到端 | “改密→重建库→数据丢失”验证需真实容器 | VM |

### 已验证（v0.1.0，不用重验）

- 同一局域网唤醒（2026-11-05，VM 上 LAN-29 一键唤醒发包成功，审计日志 device_wake）
- 扫描 192.168.1.0/24 发现 6 台以上设备并导入
- 两 Agent 在线、面板 1 设备在线、登录自检通过

---

## 七、最高风险的三件事

1. **默认凭证 + 不可撤销 Token + 删库改密（第 4、17 项，P0）**——公开部署即裸奔：任何人按 README 装好就是 `admin/wakehub123`，且改密码要删库；Token 是无状态 HMAC，泄露后 24 小时内无法作废。这是 v0.2.0 公开发布前必须先修的头号问题，也是 M1 的首要任务。
2. **全局 Agent Token + 身份冒认（第 2、3 项，P0）**——一个 Token 泄露等于全部 Agent 沦陷；持 Token 者可冒认任意 AgentID（含内置 `local`），且 `agent_tokens` 表建了却从未接线。M2 的 per-agent token 与审批流程是第二优先。
3. **重连竞态删有效连接（第 1 项，P0）**——旧连接的 defer 无条件删除新 session，多次重连后在线节点会被误删显示离线；这是核心功能正确性问题，修复成本最低（比对 session 身份再删），应与 M1 同批先做。

紧随其后的风险：登录限流名存实亡（第 13 项，5 行可修）、`handleSleepDevice` 畸形请求默认执行睡眠（B-N1）、Token 存 localStorage 可被 XSS 窃取（F-N1）、README 宣称 HTTPS 与有效限流但实际全是明文与失效防护（文档与实现双重失真）。

---

## 附录：审计覆盖文件清单

后端（16）：`cmd/server/main.go`、`cmd/agent/main.go`、`internal/agent/client.go`、`internal/agent/hub.go`、`internal/api/router.go`、`internal/audit/audit.go`、`internal/auth/auth.go`、`internal/config/config.go`、`internal/device/repository.go`、`internal/scanner/scanner.go`、`internal/scheduler/scheduler.go`、`internal/sleep/sleep.go`、`internal/status/status.go`、`internal/wol/wol.go`、`internal/wol/wol_test.go`、`go.mod`（另核 `migrations/001_init.sql`）。

前端（10）：`web/src/App.vue`、`web/src/main.ts`、`web/src/style.css`、`web/index.html`、`web/package.json`、`web/vite.config.ts`、`web/tailwind.config.js`、`web/postcss.config.js`、`web/public/manifest.json`、`web/public/sw.js`。

部署与文档（9）：`Dockerfile`、`docker-compose.yml`、`install.sh`、`Makefile`、`README.md`、`.env.example`、`migrations/001_init.sql`、`.gitignore`、`go.mod`。

*报告结束。本报告只读审计，未修改任何项目代码（除本文件本身）。*
