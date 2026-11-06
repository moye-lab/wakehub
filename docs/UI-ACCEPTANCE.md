# WakeHub UI v0.2.1 Acceptance Checklist

Mark each item only after testing the packaged build in a browser or deployment environment.

- [ ] Desktop 1366px / 1440px / 1920px: sidebar, header, content width, and tables render correctly.
- [ ] Tablet 768px–1100px: navigation and content remain usable.
- [ ] Mobile 320px / 375px / 390px / 430px: no page-level horizontal overflow.
- [ ] Mobile bottom navigation respects iOS safe-area insets.
- [ ] Light, dark, and system theme modes work and persist after reload.
- [ ] OS theme changes update the UI while system mode is selected.
- [ ] Mobile device list uses cards; desktop retains the table.
- [ ] Device selection, pagination, wake, check, sleep, edit, delete, and batch wake remain functional.
- [ ] Dashboard statistics and lists are populated only by existing API/store data.
- [ ] Login, session restore, unauthorized redirect, and logout remain functional.
- [ ] Agent approval and token operations remain functional.
- [ ] Scan, cancel, result import, task scheduling, logs, and settings remain functional.
- [ ] Frontend typecheck passes: `npm run typecheck`.
- [ ] Frontend tests pass: `npm run test`.
- [ ] Production frontend build passes: `npm run build`.
- [ ] Docker image builds and starts with existing persistent data.

## Execution status in the authoring environment

Not fully verified. `npm ci` could not complete because the package registry was unreachable/timed out, so typecheck, Vitest, and production build were not run. Do not mark those checks as passed until they are run on a machine with registry access.

## v0.2.2（方案 A）执行记录（2026-11-06，Muse 代验）

- `vue-tsc --noEmit` 通过；Vitest 27/27 通过；`vite build` 通过；`go build` + `go vet` 通过。
- 本地实测跑通（server + 新前端 + Agent 原生运行）：登录、仪表盘、Agent 注册审批、设备创建、唤醒（sent/pending）、TCP 检测在线全通；Ping 检测在沙箱报缺 ICMP 权限（沙箱限制）。
- 本包实测报告版本 0.2.2。浏览器真机目视与 VM 真机唤醒仍待用户在 VM 上验证。

## v0.2.3 执行记录（2026-11-06，Muse 代验）

- `vue-tsc --noEmit` 通过；Vitest 27/27 通过；`vite build` 通过；`go build` + `go vet` 通过。
- 本地实测跑通（server 原生运行）：health 报告版本 0.2.3，登录与 dashboard 接口正常；构建产物 CSS/JS 含 mobile-brand-name 与 health-uptime，移动端媒体查询内字标显示、运行时间以 "· 21 分钟" 内联在状态旁。
- 浏览器真机目视仍待用户在 VM 上验证。

## v0.2.9 第一期执行记录（2026-11-06，Muse 代验）

- 问题 1：compose 里 agent 服务改挂独立卷 `wakehub_agent_data`；新增 `scripts/migrate-agent-volume.sh`（bash -n 语法通过；容器内验证只能在 VM 做，见 `docs/MIGRATION-v0.2.9.md` §1.3）。
- 问题 2：迁移 008 + Network 实体 + networks API + 前端网络列/筛选/表单选择/Agent 卡片网络 chips；本地冒烟（server 原生运行，v0.2.8 态库）：启动自动备份、数量核对 devices 3→3 / networks 0→2、health 报 0.2.9、3 台旧设备回填到 `net-192-168-1-0-24`、不填网络的新设备自动进 Agent 网络、同网络重复 MAC 409、跨网络同 MAC 可建、尾部 JSON 400。
- 问题 10：`decodeOneJSON` 尾部校验 + 测试；问题 12：迁移前备份 + 数量核对 + 设备数绊线 + 测试；`AppliedVersions` 缺版本表时不再报错（冒烟中实测发现并修复）。
- `vue-tsc --noEmit` 通过；Vitest 30/30 通过（新增 networks 用例 2 个）；`vite build` 通过，构建产物含「自动（跟随 Agent）」；`go build ./...` + `go vet ./...` 通过；`go test ./...` 全部 14 个包通过。
- 浏览器真机目视、容器内卷隔离验证与 VM 升级仍待用户在 VM 上验证。

## v0.2.8 执行记录（2026-11-06，Muse 代验）

- 根因：仪表盘切页时快捷唤醒按钮短暂显示“发送中…”，实为 V-N2 自动检测在刷新状态；`check()` 与 `wake()`/`sleep()` 共用 `busyIds`。修法：store 加独立 `checkingIds`，检测走它；按钮文案按优先级显示（唤醒“发送中…”＞刷新“刷新中…”＞平时“⚡ 唤醒”）；设备页“检测”按钮禁用条件同步加 `checkingIds`。
- `vue-tsc --noEmit` 通过；Vitest 28/28 通过（新增 checkingIds 用例 1 个）；`vite build` 通过，构建产物 DashboardView 含“刷新中”；`go build ./...` + `go vet ./...` + `gofmt` 通过；`go test ./...` 全部 14 个包通过（全量第一跑 internal/api 偶发失败，系已知 TestAgentManagementAPI 时序抖动，单包与重跑全过）。
- 本地冒烟（server 原生运行，端口 18089，临时数据）：health 报 version 0.2.8、db ok；未登录调 dashboard 返回 401。
- 浏览器真机目视仍待用户在 VM 上验证。

## v0.2.7 执行记录（2026-11-06，Muse 代验）

- 仅改设备表单适配器下拉文案“SSH 关机（Linux）”→“SSH 关机（Linux / Windows）”（`DeviceForm.vue` 一行）+ 文档与版本号。
- `vue-tsc --noEmit` 通过；Vitest 27/27 通过；`vite build` 通过（4.51s），构建产物含新标签；`go build ./...` + `go vet ./...` + `gofmt` 通过；`go test ./...` 全部包通过。
- 本地冒烟（server 原生运行，端口 18088，临时数据）：health 报 version 0.2.7、db ok。
- 浏览器真机目视仍待用户在 VM 上验证。

## v0.2.6 执行记录（2026-11-06，Muse 代验）

- `vue-tsc --noEmit` 通过；Vitest 27/27 通过；`vite build` 通过（5.09s）；`go build ./...` + `go vet ./...` + `gofmt` 通过。
- `go test ./...`：全部包通过（agent、api、audit、auth、config、db、device、scanner、scheduler、secret、sleep、status、statuspoller、wol）；新增 `TestExecuteSSHWindowsTarget`（TargetOS=windows 时下发 `shutdown /s /t 1`）与 ssh_os 校验用例通过。
- 本地冒烟（server + Agent 原生运行，端口 18087，临时数据）：health 报 version 0.2.6、db ok；Agent 注册→审批→connected。
- Windows SSH 设备全链路（目标 IP 为 127.0.0.1，无 SSH 服务监听，故只验证到下发与报错回传）：创建 SSH 设备（ssh_os=windows）响应 `ssh_password_set: true`，响应体不含明文密码与密文字段；非法 ssh_os（macos）返回 400 且错误字段为 ssh_os；`POST /sleep {"action":"shutdown"}` 返回 502“连接 SSH 目标失败: dial tcp 127.0.0.1:22: connect: connection refused”，证明服务端解密→Hub 下发（ssh_target_os=windows）→Agent 执行 SSH 拨号→错误回传链路通。
- **未实测**：真实 Windows SSH 关机（目标 192.168.1.29，需电脑先开 OpenSSH Server，用户在 VM 上验证）；真实NAS SSH 关机（同 v0.2.5，未实测）；浏览器真机目视。

## v0.2.5 执行记录（2026-11-06，Muse 代验）

- `vue-tsc --noEmit` 通过；Vitest 27/27 通过；`vite build` 通过（3.74s），构建产物含“SSH 关机”文案；`go build ./...` + `go vet ./...` 通过。
- `go test ./...`：最终一轮全部包通过（agent、api、audit、auth、config、db、device、scanner、scheduler、secret、sleep、status、statuspoller、wol）。注：`TestAgentManagementAPI` 在全量并行跑中 4 轮里出现 2 次吊销后状态仍为 connected 的失败，单独跑 3/3 通过、单包跑 2/2 通过，v0.2.0 基线 worktree 单包跑 3/3 通过——根因是吊销接口返回 200 时 Hub 读循环的清理 defer 尚未执行，用例未像连接步骤那样轮询等待，属既有用例时序抖动，与本次改动无关（本次未动 Agent 会话/吊销路径）；建议后续给吊销后断言加轮询等待。
- 本地冒烟（server + Agent 原生运行，端口 18086，临时数据目录）：health 报 version 0.2.5、db ok；登录、仪表盘接口、前端首页 200 正常；Agent 注册→审批→connected（报 0.2.5，token 文件 0600）。
- SSH 设备全链路（目标 IP 为 127.0.0.1，无 SSH 服务监听，故只验证到下发与报错回传）：创建 SSH 设备响应 `ssh_password_set: true`，响应体不含明文密码与密文字段；库内 `ssh_password_enc` 为 52 字符 base64（12 字节 nonce + 密文 + 16 字节 GCM tag），不等于明文；空密码更新后 `ssh_password_set` 仍为 true（保持原密码）；`POST /sleep {"action":"shutdown"}` 返回 502“连接 SSH 目标失败: dial tcp 127.0.0.1:22: connect: connection refused”，证明服务端解密→Hub 下发→Agent 执行 SSH 拨号→错误回传链路通。
- SSH 执行本体以 `internal/sleep` 进程内真实 SSH 协议测试服务器为准：关机命令下发、TOFU 学习/匹配/不符拒绝、密码错误、sleep/hibernate 命令、主机 allowlist 均通过。
- **未实测**：真实NAS SSH 关机（目标 192.168.1.3，需用户在 VM 上验证）；浏览器真机目视。

## v0.2.4 执行记录（2026-11-06，Muse 代验）

- `vue-tsc --noEmit` 通过；Vitest 27/27 通过；`vite build` 通过；`go build` + `go vet` 通过。
- 构建产物复查：dist CSS 移动端媒体查询内含 `.topbar-heading h1` 副标题规则（13px / 500 / 次要色），且无移动端运行时间显示规则（health-uptime 仅基础隐藏）。
- 本地实测跑通（server 原生运行）：health 报告版本 0.2.4，登录与 dashboard 接口正常。
- 浏览器真机目视仍待用户在 VM 上验证。
