# WakeHub v0.3.3 审查结论（Review Verdict）

**审查对象：** GPT 对 v0.3.2 源码的审查意见（《WakeHub v0.3.2 全面整改与质量提升任务书》，19 项，目标 v0.3.3）
**基准代码：** `~/workspace/wakehub`，分支 `github-main`，提交 `09c4071`（v0.3.2，已发布）
**审查方法：** 证据门（proof gate，见 `~/workspace/custom-skills/review_suggestion_proof_gate/SKILL.md`）——每一项必须被证明为真实触发的缺陷（可运行的 stub 测试、追踪到错误结果的代码路径、日志或计数器变化）才采纳；已实现、不可行、超出范围或无缺陷支撑的重写一律书面拒绝；沙箱内无法触发的（如需真实硬件/容器）记为延期验证，而非拒绝。
**三方分工：** 前端（1、4、5、13–16）、后端（3、6、8–12）、部署/文档（2、7、17–19）分别独立取证；本结论由协调人汇总并对每项负责。

## 结论总表

| # | 项目 | 结论 | 一句话原因 |
|---|------|------|-----------|
| 1 | 定时任务编辑时 Cron 被覆盖 | **ADOPT** | 编辑时表单从不解析原 cron，默认草稿直接覆盖；取消编辑还会把编辑目标带进下一个新建表单 |
| 2 | Compose 三种部署模式依赖冲突 | **ADOPT** | 无 profiles；Agent 的 `depends_on: service_healthy` 使纯 Agent 模式自动拉起 Server；整文件 `:?` 插值使全新环境的单模式安装在配置加载阶段就失败 |
| 3 | 多网络缺少 Agent 与 Network 强制归属校验 | **ADOPT** | 设备校验只查 Network 是否存在，Agent A 可被分配 Agent B 独占网络的设备（测试已证实） |
| 4 | 单次任务选过去日期 | **ADOPT** | “单次”时间模式与 `run_once` 复选框是两个独立开关，均不校验日期；后端也不校验；单次任务会按年重触发（真机已证实，下次执行 2027/11/07） |
| 5 | 定时任务设备选择器只读第一页 | **ADOPT** | 表单只渲染设备 store 的当前页（默认 50 条第 1 页），第 51 台起不可选 |
| 6 | Network CIDR 缺少标准化与唯一约束 | **ADOPT** | 去重是字符串精确匹配，`192.168.1.1/24` 与 `192.168.1.0/24` 被当成两个网络；数据库无 `networks(cidr)` 唯一索引（测试已证实） |
| 7 | 单容器 Server/Agent 数据隔离不足 | **ADOPT（仅文档）** | 同一 uid、共享 `/app/data`，但 README 把单容器标为“推荐”、双容器标为“备选”，属于误导性标注；容器内真正隔离属于重设计且无已证实漏洞，不采纳 |
| 8 | 数据库迁移保护逻辑 | **ADOPT（收窄）** | 005 的 DELETE 静默删设备，且其整批豁免使同批其他删除迁移也放行（测试：3→1 台，`err=nil`）；备份已存在，该子项不成立；005 文件冻结不动 |
| 9 | Agent 网络范围校验查询失败放行 | **ADOPT（收窄）** | 作用域查询失败时错误被吞，返回空/部分作用域，与“未声明网络”的合法基线情形无法区分，退化为仅基础私网校验（测试已证实） |
| 10 | SSH sudo 关机模式完善 | **REJECT** | 认证/sudo/执行/超时四类失败已区分并有测试覆盖；无用户输入进入 shell；未触发任何误报成功或注入 |
| 11 | 任务修改/删除时的执行一致性 | **ADOPT（收窄）** | 删除任务级联删除全部执行历史（测试已证实）；快照、禁用停新执行、并发重叠均已实现 |
| 12 | 多设备定时关机超时与重试 | **REJECT** | 10s 单设备 / 60s 总超时 / 重试上限均已存在且受 ctx 约束，每设备结果独立记录；未触发记录丢失或无限阻塞 |
| 13 | 统一图标体系（Emoji → SVG） | **REJECT** | 仅约 10 处装饰性 Emoji，无功能缺陷；属外观重设计且仓库内无 SVG 图标集；按用户设计优先规则应先出设计稿，不属本次整改 |
| 14 | 移动端交互优化 | **REJECT** | 静态审查未发现具体缺陷；底部导航、抽屉、卡片列表、安全区、44px 触控目标均已实现 |
| 15 | 统一页面状态 | **ADOPT（收窄）** | 各页面 Loading/Empty/Error/Retry 与 401 跳转、Agent 离线均已正确；唯一缺陷是全局健康指示器在服务端中途宕机后仍冻结在上次正常的“服务正常” |
| 16 | 设备列表与网络信息完善 | **REJECT** | 桌面与移动端均显示 Agent、网络名与 CIDR，且有离线/不匹配警告与筛选；未证实误操作风险 |
| 17 | Docker 镜像构建可靠性 | **ADOPT（收窄）** | `workflow_dispatch` 可从未打 tag 的提交发布 `latest`；无 version==tag 校验（版本硬编码在三处）；其余（双架构同提交、标签、文件、入口、健康检查）均已正确；真实构建验证因沙箱无 Docker 而延期 |
| 18 | 安装器与部署脚本完善 | **ADOPT（收窄）** | 模式 2 可能写入空的 `WAKEHUB_AGENT_SUBNET`，导致从未启动的 Agent 服务使配置加载失败；其余（错误处理、幂等、down -v、备份、重启/健康检查）均已实现 |
| 19 | README 与文档统一 | **ADOPT** | README:354 仍写“定时任务只开放 wake”，与同文件 v0.3.2 sleep 功能矛盾；`docs/upgrade.md` 只覆盖 v0.1.0→v0.2.0；三种模式文档过薄 |

**统计：ADOPT 14 项，REJECT 5 项，DEFER 0 项**（另有若干子项延期真实容器/硬件验证，见各条“延期”说明）。

---

## 逐项证据摘要

### 1. 定时任务编辑时 Cron 被覆盖 — ADOPT
- `web/src/components/TaskForm.vue`：时间草稿 `draft` 是写死字面量（`kind: 'daily', time: '08:00', weekdays: [1,2,3,4,5], cron: '0 8 * * 1-5'`），从不读取 `props.editing`；只有 `form` 字段（名称、时区、重试、动作、目标、启用、`run_once`）从 `props.editing` 回填。`web/src/utils/cron.ts` 全仓库无 `parseCron`，只有 `compileCron` 与仅用于展示的 `describeCron`，原 cron 无法还原为草稿。提交时 `compileCron(draft)` 直接覆盖原 cron。
- **纠正任务书一处说法：** 任务书称覆盖后变成 `0 8 * * 1-5`，实际草稿默认 `kind: 'daily'`，未改动时间字段时提交的是 `0 8 * * *`（每天 08:00）；缺陷相同，回归测试应断言后者。
- 同项第二缺陷（取消编辑不清状态）已证实：`TasksView.vue` 的 `@close` 只关表单不清空 `editingTask`，编辑 A → 取消 → 新建任务时表单仍以 A 为编辑对象，保存会覆盖 A。`DevicesView.vue` 存在同一模式，同批修复。
- 自定义 Cron 同理：原 `30 2 * * 0` 打开后草稿为 `0 8 * * 1-5`/`daily`，保存即被覆盖。

### 2. Compose 三种部署模式依赖冲突 — ADOPT
- 全仓库无 `profiles`（`docker-compose.yml`、`install.sh`、`README.md`、`docs/deployment.md` 均无）；服务只有 `wakehub-server` 与 `wakehub-agent-local`。Agent 含 `depends_on: wakehub-server: {condition: service_healthy}`（`docker-compose.yml:133-135`），Compose 会随 Agent 拉起依赖，故模式 3（仅 Agent）会启动 Server。
- `install.sh` 确实提供三种模式并按模式选服务（`:514-516`），但 compose 整文件插值先于服务选择：Server 的三个密钥变量与 Agent 的变量均为 `${…:?…}` 强制要求。模式 3 在全新机器上 `.env` 无 Server 密钥，`docker compose up wakehub-agent-local` 在配置加载阶段即因 `WAKEHUB_ADMIN_PASSWORD 未设置` 失败；模式 2 有镜像问题（见 18）。
- 实时 `docker compose config` / 真实 `up` 验证因沙箱无 Docker 而延期（Compose 依赖语义为公开文档行为，结论不依赖该验证）。

### 3. 多网络缺少 Agent 与 Network 强制归属校验 — ADOPT
- `internal/device/validate.go:210-218`：网络检查仅为存在性（`NetworkExists`）；Agent 校验仅查批准/在线状态。`ValidateDevice`、`CreateDevice`、`UpdateDevice` 中无任何 `AgentOwnsNetwork` 式调用；全仓库 grep 无此函数，`ListAgentNetworks` 仅用于默认网络解析与 `agentScopeCIDRs`。
- **证明测试（已执行后删除）：** Agent A 与 B；网络 `192.168.2.0/24` 仅分配给 B；`ValidateDevice(AgentID=A, NetworkID=B 的网络)` 返回零错误，`CreateDevice` 落库成功（`agent=agent-A network=<B 的网络>` 已接受）。
- 扫描导入（`router.go:2784`、`:2821`）同样无归属校验，同批覆盖。
- **策略决定（协调人）：** `default` 网络允许任何 Agent 使用；无任何网络的 Agent 仅可使用 `default` 网络；扫描导入执行同一归属规则。

### 4. 单次任务选过去日期 — ADOPT
- 前端：`TaskForm.vue:47-50` 单次日期为裸 `<input type="date">`，无 `min`；`submit()` 只校验名称与目标；`compileCron` 直接生成 `0 8 5 11 *`。
- 后端：`ValidateSchedule`（`scheduler.go:195-260`）只校验名称、cron 语法、时区、目标、动作、重试范围，无未来时间校验，无 `run_once`/单次模式耦合。
- `run_once` 是独立复选框，默认 false（`TaskForm.vue:201`）；`finishRunOnce` 仅当 `runOnce==true` 才禁用（`scheduler.go:617-629`），而 `run_once` 本身工作正常（已有测试 `TestPauseResumeAndRunOnce` 证实勾选时首次执行后自动禁用并注销 cron）。
- 真机已证实机制：单次任务 `0 8 5 11 *` 的 `NextRunAt` 返回下一年 11 月 5 日，任务保持启用并显示下次执行 2027/11/07。
- **策略决定（协调人）：** 单次任务必须执行后自动禁用。前端：选 `kind=once` 时自动勾选并禁用 `run_once` 复选框（附说明“单次任务执行后自动禁用”）；后端：`ValidateSchedule` 检测单次型 cron（日、月均为具体值）时强制 `RunOnce=true`（防御纵深，使 API 客户端同样生效），并拒绝下次执行时间已过去的单次 cron。

### 5. 定时任务设备选择器只读第一页 — ADOPT
- `web/src/stores/devices.ts:12` `pageSize = ref(50)`；`load()` 一次取一页并替换 `items`。`TaskForm.vue:122-124` 的目标下拉只遍历 `devices.items`，无搜索、无分页、无 `loadAll`。`TasksView.vue:208` 挂载时只调用一次默认 `devices.load()`（第 1 页，50 条）。
- 追踪结果：51 台设备时，第 51 台的 id 永不出现在 `<option>` 中，无法被选中。附带问题：store 与 DevicesView 共享，若用户上次停在第 3 页或带筛选，任务表单会显示那一页/筛选而非全部设备。

### 6. Network CIDR 缺少标准化与唯一约束 — ADOPT
- `repository.go:451-466`（`CreateNetwork`）、`:478-495`（`UpdateNetwork`）：`net.ParseCIDR` 仅校验语法（结果丢弃），存入原始字符串；冲突检测为 `GetNetworkByCIDR` 的 `WHERE cidr = ?` 精确字符串匹配。
- `migrations/008_network_identity.sql:65-66`：网络相关索引只有 `devices(network_id, mac)`，全仓库 `migrations/*.sql` 无 `networks(cidr)` 唯一索引；检查后插入不在事务内，并发可双双通过检查。
- **证明测试（已执行后删除）：** 先建 `192.168.1.0/24` 成功，再建 `192.168.1.1/24` 返回 `err=<nil>`；`10.1.2.3/8` 与已有的 `10.0.0.0/8` 并存亦被接受。
- 下游影响：扫描导入用的 `GetNetworkByCIDR`（`router.go:1743`）只匹配一种写法，同一物理网段可被拆到两个网络行。
- 修复须含：建/改时用 `net.ParseCIDR` 规范化后存 `ipnet.String()`；新增迁移加 `UNIQUE INDEX ON networks(cidr)`（含对已有等价行的去重/回填；注意 008 种子行用的是 Agent 的原始 `subnet` 字符串，种子也要规范化）；检查+插入进事务。

### 7. 单容器 Server/Agent 数据隔离不足 — ADOPT（仅文档）
- 事实已证实：`docker-entrypoint.sh:209` `RUN_AS="su-exec wakehub:wakehub"`，`:218` 与 `:220` 以同一用户跑 Server 与 Agent；`DATA_DIR=/app/data`（`:52`），数据库、Agent token、`.admin_password`/`.secret_key`/`.enroll_token` 同目录；同一 uid 10001 + 0700 目录 + 0600 文件，Agent 进程可读 SQLite 库、库内 SSH 密文与密钥。`scripts/test-entrypoint.sh` 沙箱执行：Server 与 Agent 桩均以 uid 10001 运行且拿到同一密钥。
- 双容器确有隔离：compose 用 `wakehub_data`（Server）与 `wakehub_agent_data`（仅 Agent token）分离，`docs/deployment.md` 亦注明“v0.2.9 起两卷分离，Agent 触不到数据库文件”。
- 具体错误表述（属标注问题而非事实造假）：`README.md:75`“快速启动（单容器，一条命令，推荐）”对 `README.md:115`“一键安装（Docker Compose，备选）”；`docs/deployment.md:5`“推荐：单容器”对 `:41`“备选：install.sh”。两处均未说明隔离差异或推荐双容器作隔离方案。共享本身已有事实披露（README 快速启动注明数据卷内容），故无须纠正事实，只须改标注并加一段说明。
- **拒绝部分：** 容器内真正隔离（Server 与 Agent 不同 uid/卷）属重设计，且无已证实漏洞——Agent 是同镜像的第一方代码、无对外服务。不采纳。

### 8. 数据库迁移保护逻辑 — ADOPT（收窄）
- (a) `before, _ :=` / `after, _ :=` 忽略错误属实（`migrate.go:63,73`；`cmd/server/main.go:62,66` 亦然），但影响有限：`TableCounts` 仅在 `sqlite_master`/`COUNT(*)` 查询失败时出错，即数据库已根本损坏；健康库无静默绕过路径。并入修复作 fail-closed 加固，非主缺陷。
- (b) 005 的 DELETE 存在且会删真实设备，已证实：`migrations/005_indexes.sql:20-22` `DELETE FROM devices WHERE rowid NOT IN (SELECT MIN(rowid) FROM devices GROUP BY mac)`。证明测试：001–004 旧库 3 台设备（两台同 MAC）→ 应用 005 后重复项被删，无错误、无日志、无删除计数。
- (c) 005 整批豁免可绕过设备数量保护，已证实：`migrate.go:72` `if len(pending) > 0 && !appliedSet[5]` 是整批豁免而非仅限 005 自身增量。证明测试：同旧库，待执行 = 真实 `005_indexes.sql` + 合成 `009_delete_device.sql`（`DELETE FROM devices WHERE id='dev-3'`）→ `Migrate` 返回 `nil`，设备 3→1 台（005 去重 + 009 删除）。对照：不含 005 的同删除迁移被 `TestMigrateDeviceLossRefused` 正确拒绝。
- (d) “迁移前无备份”不成立：`BackupBeforeMigrate`（`migrate.go:143`，`VACUUM INTO`，0600）已在 `cmd/server/main.go:56`（及重置密码路径 `:150`）于 `Migrate` 前调用，计数亦有日志。问题在保护被绕过，不在无备份。
- 范围说明：真实 v0.3.2 升级路径中 005 早已应用，当前用户无法触发；新库应用 005 时设备数为零。缺陷潜伏于 001–004 旧库及未来与 005 同批的迁移。修复范围相应收窄：仅豁免 005 自身增量（并记录其删除行数），其余照常检查；005 文件冻结不动，任何改动走新迁移。

### 9. Agent 网络范围校验查询失败放行 — ADOPT（收窄）
- `internal/api/router.go:644-661` 与 `internal/scheduler/scheduler.go:146-164`（两处重复逻辑）：`if networks, err := …ListAgentNetworks(agentID); err == nil { … }` 与 `GetAgent` 同理——错误被丢弃，无日志、无传播；部分结果亦可能（网络读失败但 Agent `subnet` 读成功时仅强制 subnet）。
- `internal/sleep/sleep.go:95`（`ValidateEndpointInScope`）、`:122`（`validateIPInScope`）：`if len(cidrs) == 0 { return nil }`——空作用域即“仅基线”。该回退对“Agent 确实未声明网络”的旧 Agent 是有意且有测试覆盖的（`sleep_test.go:126`），正因如此查询失败与合法情形无法区分，才构成危险。
- **证明测试（已执行后删除，scheduler 包）：** Agent `agent-9`，无 subnet，仅通过 `agent_networks` 管理 `192.168.1.0/24`。健康时作用域拒绝 `http://192.168.2.50:8009`（len=1）；`DROP TABLE agent_networks` 模拟该读失败后，`agentScopeCIDRs` 返回 len=0，`ValidateEndpointInScope("http://192.168.2.50:8009", scope)` 返回 `nil`——越权私网 IP 被放行。
- 修复须区分“查询失败”（fail-closed，拒绝本次关机操作并记录）与“Agent 确实未声明网络”（保留已有基线回退）；两处重复的 `agentScopeCIDRs` 均须修或去重。

### 10. SSH sudo 关机模式完善 — REJECT
- 四类失败均已区分并有测试：握手/认证失败（`ssh.go:211`）、sudo 认证失败（`:285-286`，`isSudoAuthFailure` 匹配 `Sorry, try again` / `a password is required`）、执行失败（`:288`，含退出码与首行 stderr，并过滤 sshd chdir 噪音）、超时（`:259`）、连接中断按 `poweroffDropGrace`（1s）区分（延迟断开=成功，提前断开=失败）。测试覆盖：`TestExecuteSSHSudoWrongPassword`（sudo 失败且内部关机脚本未执行）、`TestExecuteSSHPoweroffFailureVisible`（polkit 式 exit 1 显式回归测试）、`TestExecuteSSHDropAfterDelayIsSuccess` / `TestExecuteSSHEarlyDropIsFailure`、`TestExecuteSSHSudoSuccess`（密码仅走 stdin，断言不出现在命令行）。
- 无任意 shell：关机脚本由固定字符串按白名单 `Action` 枚举拼接（`sleep.go` 的 `ValidateAction`）；主机/用户/密码永不插值（密码仅经 `sudo -S` 的 stdin；主机须为私有 IPv4）。
- 残留观察（不采纳，仅措辞问题）：sudo 失败检测依赖英文 sudo 输出，非英文 locale 会落入通用执行失败错误；仍会大声失败，永不误报成功。

### 11. 任务修改/删除时的执行一致性 — ADOPT（收窄）
- **删除丢失历史，已证实：** `migrations/001_init.sql:99` `task_runs.schedule_id REFERENCES schedules(id) ON DELETE CASCADE`，`task_run_devices.run_id REFERENCES task_runs(id) ON DELETE CASCADE`（`004_runs.sql`）；生产启用 FK（`InitDB` 的 `_pragma=foreign_keys(1)`）。证明测试（FK 开启）：任务 + 1 条执行 + 1 条设备行 → `DeleteSchedule` 后 `task_runs=0`、`task_run_devices=0`。且删除前历史已不可达（`handleListTaskRuns` 对缺失任务返回 404），数据不可恢复。
- 快照已实现：`executeSchedule(s device.ScheduleItem, …)` 按值接收任务；`Reload` 将每项复制进 cron 闭包；运行中编辑不影响本次运行的 cron/动作/重试/目标类型。
- 禁用停新执行已实现：`Reload` 跳过 `!item.Enabled`；暂停/恢复/删除/创建/更新均调 `Reload`；已有 `TestPauseResumeAndRunOnce`（暂停→0 条 cron，恢复→1 条）与 `TestExecuteScheduleOverlap`（并发→1 成功 + 1 `skipped`“上一次执行尚未结束”）。
- 运行中任务不因禁用/删除被取消——运行中的 goroutine 会完成并记录，属标准语义，非缺陷。
- 未采纳观察：`executeSchedule` 在运行开始时加载目标设备行，但每轮用 `ValidateOperation` 重新校验，派发却用旧 `dev` 副本——运行中编辑设备理论上可致“按新值校验、按旧值派发”；沙箱内无法确定性触发，需附失败案例才采纳。
- **策略决定（协调人）：** 删除任务时保留执行历史。实现方式由后端实施者选最不破坏的方案（新迁移将 `task_runs.schedule_id` 的 FK 由 `ON DELETE CASCADE` 改为 `ON DELETE SET NULL`，或删除前先归档），须有回归测试证明删除后执行行仍存在；001 号迁移冻结不动。

### 12. 多设备定时关机超时与重试 — REJECT
- 超时均已定义且分层：`scheduler.go:32-35,95-96` 单次派发 `taskTimeout = 10s`、总运行 `runTimeout = 60s`、重试退避 `500ms`、尝试次数 `retry_count+1`（上限 5→6 次）。`hub.Dispatch` 在 ctx 到期时返回“Agent 任务执行超时”并向 Agent 传播取消，故 60s 后剩余设备快速失败而非挂起，且每台仍有 `failed`/`skipped` 记录。`recordRun` 在成功/部分/失败/跳过/空目标/重叠全部路径均可达；每设备 `RecordTaskRunDevice` 错误仅记日志，不致整条执行行丢失。
- 每设备独立性已有测试：`TestExecuteScheduleSleepGroupMixed`（一次运行 1 成功 + 1 跳过，`success` 且输出“成功 1 / 失败 0 / 跳过 1”）、`TestExecuteScheduleRetryLimit`、`TestExecuteScheduleSleepRetryLimit`。
- 幂等性：WOL 重发只是重复魔术包；关机重试重发关机，SSH 路径按断开后成功处理（grace 规则），不会对已完成的关机重复重试。
- 设计说明（非缺陷）：`runTimeout` 为固定 60s 共享预算，与分组大小无关；大分组 + 高重试时尾部会超时，但尾部记为带超时原因的 `failed`，不丢失不静默。改预算/并发/按规模缩放属设计决策，非已证实缺陷的修复。

### 13. 统一图标体系（Emoji → SVG） — REJECT
- 全仓库扫描：宽泛符号正则 49 处，但多为文本符号（`✕` 关闭、`→`、`✓`、`☀/☾` 主题切换、`⚡`、`↗`）；真正象形 Emoji 图标约 10 处（EmptyState 的 `⏰`/`🖥️`/`📡`/`📋`、路由导航 `🏠 📋 📡 🔍 🖥️ ⚙️`、`ErrorState.vue` 的 `⚠️`）。全仓库无任何 `<svg>` 元素，所谓“统一为 SVG”须新写一套图标，而非替换现有图标。
- 无功能缺陷：无图标渲染失败、无因图标导致的布局破坏、无可归因于 Emoji 的无障碍失败（装饰性图标带 `aria-hidden`）。用户反馈历史中亦无图标投诉。
- 按用户设计优先规则，此类外观重设计应先出设计稿由用户选定，不属本次整改。

### 14. 移动端交互优化 — REJECT
- 静态审查未发现具体缺陷：`web/index.html:6` viewport + safe-area；`App.vue` 移动底部导航、“更多”抽屉、顶部栏折叠、≤767px 隐藏侧栏；`style.css:152-200` 移动媒体查询（侧栏隐藏、底部导航安全区、设备表改卡片列表、MAC/IP 任意换行、页脚隐藏）；输入框移动端强制 16px（防 iOS 聚焦缩放）；各视图按钮均 `min-h-[44px]`；表格用 `overflow-x-auto` 而非整页横向溢出；仪表盘栅格 6→3→2 列折叠。
- 诚实说明：本子代理无真实浏览器，未做真机点按测试；结论为“静态审查未发现具体缺陷”。如需真机验证，属新的验证任务，非修复项。

### 15. 统一页面状态 — ADOPT（收窄）
- 各页面 Loading/Empty/Error/Retry 与 401 跳转、Agent 离线均已正确：Devices/Tasks/Agents/Logs/Scan/Dashboard 均渲染 loading 文本、`ErrorState`（带 `@retry`）与 `EmptyState`；store 捕获错误而非抛错（`devices.ts:30-35` 注明 v0.2.9“无静默空列表”修复；agents/tasks/logs 同模式）；未授权：`api/client.ts:9-11` + `App.vue:127`（401 回调跳登录），有测试覆盖；Agent 离线：`AgentsView.vue:156`“已批准（离线）”、侧栏“Agent 在线 X / Y”、`DeviceForm.vue` 的 `agentOfflineWarning` / `networkMismatchWarning`。
- **唯一采纳缺陷——全局健康指示器过期：** `App.vue:107-108` `loadServerInfo()` 每 60s 轮询 `api.dashboard()`，但 `catch { /* 保留上次状态 */ }` 丢弃失败并保留 `serverStatus`/`dbStatus`/`uptimeSeconds` 的上次正常值。追踪结果：服务端在一次成功轮询后宕机，`healthOk` 保持 `true`，侧栏与顶栏继续显示绿点与“服务正常”，页脚（`App.vue:58`）无限期打印“服务运行正常”——外壳主动把已死的服务端谎报为健康。同理“Agent 在线 X / Y”计数仅挂载时加载一次。
- `SettingsView.vue:154-156` 吞掉仪表盘统计加载失败，但仅为非阻塞信息面板，页面仍可用，不采纳。

### 16. 设备列表与网络信息完善 — REJECT
- 桌面表有 Agent 与网络专列（`DevicesView.vue:74-79`），网络格显示名称与 CIDR；未分配 Agent 显示琥珀色“未分配 Agent”；移动卡片同样显示 Agent、网络名、CIDR、最后检查时间与 `last_status_error`（`:133-136`）；`DeviceForm.vue:59,74` 在编辑时提示离线/不匹配两种真正风险状态；列表有 Agent 与网络筛选及搜索框，归属可查而非仅可见。
- 设备唤醒/关机/批量唤醒/删除均在同一行/卡片可见 Agent 与网络，未发现无归属信息的操作路径。如用户有具体误操作场景（如同名设备跨网络），应作为触发案例补充，现状无已证实缺陷。

### 17. Docker 镜像构建可靠性 — ADOPT（收窄）
- 已正确（拒绝为缺陷）：同一提交双架构——`.github/workflows/docker-publish.yml:27-53` 单任务、单 `actions/checkout@v4`、`platforms: linux/amd64,linux/arm64` 单步构建；标签——`:41-46` metadata-action `type=semver,pattern={{version}}`、`{{major}}.{{minor}}`、`type=raw,value=latest`，tag `v0.3.3` 得 `0.3.3`、`0.3`、`latest`；文件/入口/健康检查——Dockerfile 阶段 1 `npm ci` + `npm run build`、`COPY --from=web-builder …/dist`、`COPY migrations/`（001–010 齐全）、`COPY docker-entrypoint.sh`、`HEALTHCHECK … /api/v1/health`、`ENTRYPOINT ["/app/docker-entrypoint.sh"]`；`.dockerignore` 排除 `web/dist/` 但构建阶段内重建，正确。
- 版本一致性现状：`router.go:66` `serverVersion = "0.3.2"`、`client.go:26` `agentVersion = "0.3.2"`、`web/package.json:27` `"version": "0.3.2"`、README/CHANGELOG/deployment.md 均为 `0.3.2`，grep 验证无不一致。
- **采纳的两处窄缺口：**
  - (a) `docker-publish.yml:18-22` 触发器含裸 `workflow_dispatch`。手动触发时无 semver tag ref，两条 `type=semver` 不产生标签，但 `type=raw,value=latest` 仍产生——本次运行会从当前 main HEAD 构建并推送 `moyelab/wakehub:latest`。文件头注释还鼓励手动触发。属配置可确定性导致的错误结果：未发布提交成为 `latest`。
  - (b) 全流程无任何步骤校验 version==tag。版本仅为三处硬编码常量；打 `v0.3.3` 而漏改任一处，即发出标签 `0.3.3` 但 `/health` 报 `0.3.2` 的镜像。尚未触发（当前三处均为 `0.3.2`），属同批加入的预防性守卫。
- **延期：** 真实构建验证（双架构、启动、健康检查）——沙箱无 Docker 守护进程，不作声称。

### 18. 安装器与部署脚本完善 — ADOPT（收窄）
- 已实现（拒绝为缺陷）：入口错误处理——`bash scripts/test-entrypoint.sh` 沙箱执行，当前入口 32 PASS（三种场景：root 属主目录→chown+降权、正确属主→不重复 chown+密钥复用、写失败→非零退出且无虚假“已保存”、两进程均未启动）；5 FAIL 为旧入口反向验证的预期失败。无 `down -v` 作升级步骤——全仓库 grep 仅见明确标注“删除全部数据”的指引（`install.sh:654` 附备份警告；`docs/deployment.md:117,197`）与禁令（`docs/upgrade.md:99` “`docker compose down` # 不加 -v”）；升级路径为 `install.sh:519` `docker compose stop` + `:534` `docker compose up -d --build`（3 次重试），从不删卷。幂等重跑——`install.sh` 读已有 `.env` 作默认、原子写（临时文件 + `mv`，`chmod 600`）、重跑保留值，启动后 90s 健康轮询加真实登录自检。重启/健康检查/启动等待——compose 两服务 `restart: unless-stopped`、Server 健康检查、Agent `depends_on: service_healthy`。破坏性步骤前备份——`scripts/backup.sh` 恢复时先把当前库存为 `.bak-<timestamp>`；install.sh 无破坏性步骤。
- **唯一采纳缺陷：** `install.sh:301` 从保存值或探测得 `AGENT_SUBNET`；`:302` 模式 2 时跳过子网提示/选择块；`ask_subnet` 仅在 `:397`（模式 3）与 `:402`（模式 1）调用。但 `:493` 无条件写 `WAKEHUB_AGENT_SUBNET=$AGENT_SUBNET`。若子网探测失败（无 `ip` 命令且 `hostname -I` 为空）且无保存值，模式 2 会写空行，而 `docker-compose.yml:106` 的 `${WAKEHUB_AGENT_SUBNET:?…}`（冒号形式视空为未设置）使 `up -d wakehub-server` 在配置加载阶段失败，尽管模式 2 从不启动 Agent。条件触发，全链路可追踪。安装器侧修复：模式 2 不写/不要求 Agent 变量（compose 侧根因为第 2 项的修复）。模式 3 缺 Server 密钥的情况属第 2 项缺陷，不重复计算。
- **延期：** 真实容器上的重跑/升级行为（无守护进程）。

### 19. README 与文档统一 — ADOPT
- (a) `README.md:354`（已知限制）：“定时任务第一阶段只开放 `wake` 动作；分组暂无更新/删除接口。”与同文件 `README.md:56-57`（开放 `wake` 与 `sleep`）、`README.md:283`（`POST /api/v1/tasks` 新建定时任务（`wake` / `sleep`））、`CHANGELOG.md:3-11`（v0.3.2 开放 sleep）及 `internal/scheduler/scheduler.go:263`（v0.3.2 起开放 sleep）直接矛盾，自 v0.3.2 起过期。第二分句仍属实——`router.go:463-464` 分组仅有 GET/POST——故修复只删 wake 分句。
- (b) `docs/upgrade.md:1` 标题“WakeHub 升级文档：v0.1.0 → v0.2.0”，步骤仅覆盖迁移 002–005（`:99-105`），验收检查期望 `{"version":"0.2.0"}`（`:131`），而仓库已是 v0.3.2、迁移至 010。README“升级、恢复与回滚流程见 `docs/upgrade.md`”把 v0.3.2 用户指向这份过期文档。
- (c) 三种模式仅有一行括号说明（`README.md:123-124`）与一小段（`docs/deployment.md:47-52`）；模式专属要求（模式 3 需远程 WS 地址与入网 token；模式 2 需另外部署 Agent）仅存于 `install.sh` 提示中。属轻微缺口，同批修复。单双容器安全指引缺失——交叉引用第 7 项。
- 已正确（拒绝为缺陷）：版本号全仓库统一 `0.3.2`；`CHANGELOG.md:342,384` 的“只开放 `wake`”在历史 v0.2.x 章节中，属历史记录而非缺陷；SSH 关机（README 功能条目 + `docs/ssh-sleep.md`）、数据库备份（README 备份节 + `docs/deployment.md`）、多网络（架构节）、Agent 安装/授权/网络（`docs/deployment.md`“Agent 入网两种路径”）、架构支持（README Docker Hub 节 amd64+arm64）均已文档化。

---

## 延期验证（不属于拒绝）

以下内容沙箱内无法验证，须在真实硬件/容器上补测，见 `docs/TEST-REPORT-v0.3.3.md`：
- 第 2 项：每种模式的 `docker compose config` 与真实安装/启动/健康检查/重启。
- 第 17 项：真实双架构构建、镜像启动、健康检查、标签发布。
- 第 18 项：子网探测失败机器上的 install.sh 模式 2 真实运行。
- 第 4、11 项的部分行为：真实定时触发与删除后历史在真机上的表现（沙箱内用 Go 测试覆盖逻辑，真机触发仍待验证）。
- 全部硬件相关行为：真实 WOL 唤醒、SSH 关机、定时任务真实触发。

## 协调人设计决定

1. **第 3 项：** `default` 网络允许任何 Agent 使用；无任何网络的 Agent 仅可使用 `default` 网络；扫描导入执行同一归属规则。
2. **第 4 项：** 单次任务必须执行后自动禁用。前端：选 `kind=once` 时自动勾选并禁用 `run_once` 复选框（附说明）；后端：`ValidateSchedule` 检测单次型 cron（日、月均为具体值）时强制 `RunOnce=true`，并拒绝下次执行时间已过去的单次 cron（防御纵深）。
3. **第 8 项：** 仅豁免 005 自身增量（记录其删除行数），其余照常检查；`TableCounts` 出错时 fail-closed；005 文件冻结不动。
4. **第 11 项：** 删除任务时保留执行历史。实现方式由后端实施者选最不破坏的方案（新迁移将 `task_runs.schedule_id` 的 FK 由 `ON DELETE CASCADE` 改为 `ON DELETE SET NULL`，或删除前归档），须有回归测试证明删除后执行行仍存在；001 号迁移冻结不动。
5. **第 17 项：** 工作流只允许 tag 事件触发构建与 `latest` 发布（或手动触发时要求输入 tag）；增加 version==tag 校验步骤。
6. **第 2 项：** Compose 用 profiles 区分三种模式，并移除/弱化 Agent 的 `depends_on`（Agent 已有重连逻辑，启动顺序非硬性要求）；install.sh 按模式只写/只要求对应变量。
