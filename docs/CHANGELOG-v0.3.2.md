# Changelog — v0.3.2

> 本文件记录 v0.3.2 的全部变更。v0.3.2 只做一件事：**定时任务开放
> `sleep`（关机）动作**——用户要飞牛每天凌晨关机、早上 8:00 开机，
> 以前定时任务只能唤醒，排不了定时关机。升级方式：无新迁移，
> 已有部署直接换镜像重跑同一条命令（数据卷原地保留，老数据原地
> 可用）；已有 wake 任务不受影响。

## 用户场景

飞牛每天 00:00 关机、08:00 开机，延长设备寿命。用现有每日 cron UI
表达，不用特殊 cron 工作：

| 任务 | cron | 动作 | 目标 |
|---|---|---|---|
| 午夜关机 | `0 0 * * *` | `sleep` | 飞牛 NAS |
| 晨起唤醒 | `0 8 * * *` | `wake` | 飞牛 NAS |

## 1. 调度器：sleep 动作（`internal/scheduler/scheduler.go`）

**改动文件**：`internal/scheduler/scheduler.go`、`cmd/server/main.go`。

- **白名单放宽**：`ValidateSchedule` 的 action 白名单由仅 `wake`
  改为 `wake` / `sleep`；空仍默认 `wake`；其他值（如 `check`、
  `shutdown`）仍报 400 `unsupported_action`。
- **依赖接线**：Engine 新增 `secretKey` 字段 + `SetSecretKey`
  setter（与现有的 `SetAuditLogger` 同一模式）；`cmd/server/main.go`
  在 `SetAuditLogger` 后加 `sched.SetSecretKey(cfg.SecretKey)`。
  不改 Engine 构造签名，不动其他调用方。
- **执行分支**：`executeSchedule` 在 `ValidateOperation` 通过后按
  `s.Action` 分支——`sleep` 时先 `buildSleepPayload`、再
  `sleepDeviceWithRetry`；其余仍走原 wake 分支（未改动）。
- **`buildSleepPayload(dev)`**：与手动睡眠（`handleSleepDevice`）
  同一校验链，不新发明睡眠路径：
  - `sleep_type` 为空/`none` → 「未配置睡眠适配器」；
  - `http_sol`：校验端点 + 端点/IP 范围（执行 Agent 的管理网段）；
  - `ssh`：校验用户名/IP/范围，`secret.Decrypt` 解密码（密钥未注入
    则解密失败），端口默认 22；
  - payload 的 `Action` 固定为 `sleep.ActionShutdown`（与界面「睡
    眠」按钮发送的电源动作一致），`SSHUseSudo` / `SSHTargetOS` /
    主机指纹原样透传；未知适配器 → 「睡眠适配器类型无效」。
- **`sleepDeviceWithRetry`**：与 `wakeDeviceWithRetry` 同一重试模型
  （`retry_count + 1` 次、500ms backoff、响应 ctx 取消、
  `taskTimeout`）；成功后做 TOFU 主机指纹学习（SSH + 指纹为空 +
  结果含 `host_key_sha256` 时写入）。
- **配置错误不重试**：`sleepConfigError` 类型标记配置问题——
  `buildSleepPayload` 失败时该设备记 `skipped` 并写明原因，不
  dispatch、不重试（配置错了重试也没用）。
- **审计**：每设备成功写 `device_sleep`、失败写
  `device_sleep_failed`（actor `system`，与手动睡眠同一可观测性）；
  同时仍走原有的 `recordRun`（task_runs + 每设备行 + `schedule_run`
  审计）。sleep 成功行只设 `SentAt`，不设 `BootStatus`（关机没有
  “已开机”状态）。

## 2. 前端：动作选择器

**改动文件**：`web/src/components/TaskForm.vue`、
`web/src/views/TasksView.vue`、新增 `web/src/utils/schedule.ts`。

- 任务表单「目标」区新增「动作」下拉：唤醒（wake）/ 睡眠（关机）
  （sleep），默认 wake；编辑已有任务时按其 action 回填。
- 删掉「动作固定为唤醒（v0.2.0 第一阶段只开放 wake，契约 §8.1）」
  提示；选 sleep 时显示提示：按设备配置的关机适配器执行，未配置
  适配器的设备会跳过，不重试。
- 提交时发送所选 action（经 `buildScheduleBody` 组装，空值默认
  wake，与后端一致）；其余字段不动。
- 任务列表每张卡片加动作徽标（睡眠为 amber、唤醒为 blue）；空态
  提示改为「定时唤醒或关机」。
- `web/src/utils/schedule.ts` 抽出纯函数 `ACTION_OPTIONS` /
  `actionLabel` / `buildScheduleBody`，供组件与 vitest 共用。

## 3. 契约与文档

- 契约 `docs/contract-v0.2.0.md` §8.1：action 白名单改为
  `wake` / `sleep`（空默认 wake）；§8.4 增加 sleep 执行语义；
  §6 睡眠限制与第 33 行审计表同步更新。
- README：功能列表与 API 表格的「仅 `wake`」改为
  `wake` / `sleep`。
- `docs/api.md`：任务字段的 action 说明同步更新。
- `docs/ssh-sleep.md`：限制一节的「排不了定时关机」已过时，改为
  定时关机用法（每日 00:00 关机 + 08:00 唤醒）。

## 4. 版本号 0.3.1 → 0.3.2

按版本 bump 清单：`internal/api/router.go` 的 serverVersion、
`internal/agent/client.go` 的 agentVersion、m5/m7 测试断言、
`web/package.json` + `package-lock.json`（仅顶层两处）、
`web/src/views/SettingsView.vue`、`scripts/migrate-agent-volume.sh`
的 AGENT_IMAGE 回退、`install.sh` 头注释、`.github/workflows/
docker-publish.yml` 示例 tag、README、`docs/api.md`、
`docs/deployment.md`、`docs/security.md`、根 `CHANGELOG.md`（新增
v0.3.2 节）、`Dockerfile`、`docker-compose.yml`（两处 image）、
`docker-entrypoint.sh`。历史引用不动（如「v0.3.1 修权限」出处记录、
`package-lock.json` 里的依赖版本）。

## 5. 不改动

- 不改迁移 001–010（`schedules` 表已有 `action` 字符串列）。
- 不改手动睡眠路径（`handleSleepDevice`、Agent 的 sleep 执行器）。
- 不改 wake 分支、不改运行状态派生规则（跳过不降级状态：1 成功 +
  1 跳过 → `success`，与 wake 一致）。
- 不改任何安全校验（密码解密、端点/IP 范围、TOFU、审计均与手动
  睡眠一致）。

## 6. 升级

无新迁移。已有 v0.3.1 单容器部署：`docker rm -f wakehub` 后重跑
README 同一条命令，把镜像 tag 换成 `moyelab/wakehub:0.3.2`（数据卷
原地保留）；已有 compose 部署照旧 `docker compose up -d --build`。
已有 wake 任务升级后照常执行；新建 sleep 任务在任务页选「睡眠
（关机）」即可。注意：sleep 任务只对已配置睡眠适配器（SSH /
http_sol）的设备生效，未配置的设备会跳过——先在设备编辑页配好
SSH（飞牛记得勾「使用 sudo 执行关机命令」）。
