# 测试报告 — v0.3.2

> 验收时间：2026-11-07 上午。结论：**全部通过**，可交付。
> 本次改动：调度器开放 sleep 动作 + 前端动作选择器 + 文档与版本号。
> 本机沙箱跑不起 Docker 容器（与代码无关），容器内验证仍须在 VM/盒子做，
> 见 §6。定时 sleep 的真机执行（飞牛关机 + 唤醒闭环）须在盒子上测，见 §6。

## 1. 后端

- `go vet ./...`：干净。
- `gofmt -l internal/ cmd/ web/src/`：干净。
- `go test ./... -count=1`：**14 包全过**（最终轮：api 16.3s、auth 13.9s、
  agent 5.1s、scheduler 3.7s；另见 §5 的一次 api 抖动）。
- `go test -race ./internal/scheduler/ ./internal/sleep/ -count=1`：**全过、
  无数据竞争**（scheduler 11.3s）。
- `go test -race ./internal/api/ -count=1`：**全过、无数据竞争**（175.7s，
  race 下慢属正常）。
- 版本号只改真实引用：`router.go` 的 serverVersion、`client.go` 的
  agentVersion、m5/m7 测试里的版本号、前端与文档；历史引用未动（如
  Dockerfile 里的“v0.3.1 修权限”、`package-lock.json` 里的依赖版本）。

## 2. 前端

- `npm run typecheck`（vue-tsc）：通过。
- `npm run test`（Vitest）：**44/44**（v0.3.1 为 38/38；新增 6 个
  `schedule.test.ts`，见 §3）。
- `npm run build`（vite）：通过。

## 3. 新增测试与反向验证

### Go（`internal/scheduler/scheduler_sleep_test.go`，新增 5 个测试）

- **TestBuildSleepPayload**（9 个子例）：http_sol 回环端点放行、超出
  Agent 管理网段拒绝、公网端点拒绝、ssh 透传 sudo 与解密密码、ssh
  超出网段拒绝、ssh 用户名为空、ssh 密码无法解密、未配置睡眠适配器
  （`none` 与空）、适配器类型无效。
- **TestExecuteScheduleSleepWithLiveAgent**：真 Agent + http_sol 受控端，
  断言端点收到 1 次 `POST /shutdown`、run success、设备行 success 且
  无 BootStatus、`device_sleep` 审计（actor system）与 `schedule_run`
  审计在位。
- **TestExecuteScheduleSleepAdapterMissingSkipped**：未配置适配器的设备
  记 skipped（不重试、不 dispatch、不写 device_sleep 审计）、run
  状态 skipped。
- **TestExecuteScheduleSleepGroupMixed**：分组 1 成功 + 1 跳过 → run
  success、输出“成功 1 / 失败 0 / 跳过 1”（跳过不降级状态，与 wake
  一致）。
- **TestExecuteScheduleSleepRetryLimit**：受控端不可达（127.0.0.1:1），
  retry_count=2 → 耗时 ≥ 900ms（2×500ms backoff，实测约 1.08s）、
  run failed、设备行原因含“受控执行端”、`device_sleep_failed` 审计在位。
- **既有测试更新**：`TestValidateSchedule` 增加“action 为 sleep 合法”，
  “Action 非 wake 被拒”改为 `check`；`TestTasksCRUD`（m4）增加 sleep
  任务创建（201 且 action=sleep）与列表含两任务的断言。

反向验证（逐项临时回退生产代码，确认测试失败，再恢复；恢复后全过）：

- **R1 校验白名单回退为仅 wake**：`TestValidateSchedule` 的“action 为
  sleep 合法”失败（`unsupported_action`）。
- **R2 sleep 执行分支永不进入（回落 wake）**：live-agent 测试收不到
  `/shutdown`、retry 测试 1.25ms 即“成功”（走了 WOL），双双失败——
  证明 sleep 走的是独立分支而非 wake。
- **R3 sleep 重试循环改为只发一次**：retry 测试耗时断言失败（实测
  1.7ms，期望 ≥ 900ms）。

### 前端（`web/src/utils/schedule.test.ts`，新增 6 个测试）

动作选项含唤醒/睡眠（关机）、sleep 原样透传、wake 原样透传、action
为空默认 wake、其余字段原样透传且名称去空格、动作徽标文案。

反向验证：把 `buildScheduleBody` 的 action 改成写死 `'wake'`，1 个
测试失败（“sleep 原样透传到请求体”），其余 5 个通过；恢复后 6/6。

### 命名教训

新测试文件一开始叫 `scheduler_test_sleep.go`——不以 `_test.go`
结尾，Go 把它当普通源码编进包，`go test -list` 里看不到新测试。
已改名 `scheduler_sleep_test.go`。

## 4. 升级冒烟（`/tmp/run-e2e-032.sh`，真实二进制，**24/24 全过**）

三阶段：

- **阶段 A（v0.3.1 态数据）**：v0.3.1 二进制（git worktree，HEAD=
  f244c4f）+ 真实 Agent（回环自动获批）；health `version=0.3.1`；
  登录成功；`agent-local` 自动获批 connected；创建 1 台设备（SSH
  睡眠配置，sudo 开）；创建 wake 任务成功；列表 total=1、action=wake；
  DB devices/schedules = **1/1**。
- **阶段 B（同一数据目录换 v0.3.2 二进制）**：health `version=0.3.2`；
  登录成功（密码复用）；`agent-local` 复用 token connected；升级后
  wake 任务仍在（total=1、action 仍为 wake）；新建 sleep 任务成功
  （201，action=sleep）；列表 total=2；非法 action `check` → 400
  `unsupported_action`；DB devices/schedules = **1/2**。
- **阶段 C（再重启一次 v0.3.2）**：health `version=0.3.2`；登录成功；
  重启后任务仍为 2 条，动作分别为 sleep、wake。

冒烟脚本排障记录（均为测试脚本的隔离问题，非产品 bug）：

- `(cd … && 二进制 > 日志 2>&1 &)` 的子 shell 写法会卡死：子 shell 里
  后台的 server 进程会被脚本收养，脚本卡在 `do_wait` 等它，阶段 A
  的 API 调用一次都没发出（DB 全 0、Agent 日志文件不存在），6 分钟
  无进展。修法：直接以后台作业启动（不用子 shell、不用 cd，路径
  全绝对），stdin 接 `/dev/null`，再 `disown`；改完 24/24 一次通过。
  另：`pkill -f` 会匹配自身命令行而自杀，清理进程时不要用它。
- git worktree 用 `rm -rf` 删目录后注册信息还在，须 `git worktree
  remove --force` 或 `git worktree prune` 才能重建同路径 worktree。
- 本版无新增前端 e2e：冒烟只走 API；前端动作选择器由 vitest 覆盖。

## 5. 一次 api 抖动（已确认非回归）

最终轮 `go test ./...` 里 `internal/api` 失败 1 次（24.5s），伴随
日志 `[Audit] 写入审计日志失败 agent_disconnect: sql: database is
closed`——疑似某测试的 Agent 断开发生在测试库关闭之后（teardown
竞态）。同一份代码不做任何改动重跑：api 连续通过 3 次（16.3s、
16.5s、16.2s），且此前 pre-bump 全量与 race（175.7s）也均通过。
结论：抖动，非本次改动引入；未深挖（须复现抓测试名，留待后续）。

## 6. 未在本机验证（须真机）

- Docker 镜像构建与 `docker run` 真机行为（沙箱跑不起容器）：v0.3.2
  镜像须等 tag 触发 Actions 构建后在 VM/盒子拉取验证。
- 定时 sleep 的真机执行：冒烟只验证了 sleep 任务的创建与列出，未
  真正触发执行（执行须等 cron 到点或把任务改成近时的单次任务）。
  盒子上须测：飞牛 sleep 任务到点关机（审计 `device_sleep`）、
  08:00 wake 任务唤醒回来；未配置适配器的设备跳过不报错。
- 定时任务的 cron 实际触发：本机未等到 00:00/08:00；建议用户先建
  一个 5 分钟后的单次 sleep 任务验证执行与审计，再排每日任务。
