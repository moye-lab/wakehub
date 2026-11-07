# Test Report — v0.3.3

> 本报告记录 v0.3.3 的全部验证结果。诚实区分：已跑过且通过的、
> 反向验证过的、以及未验证的（Docker 构建、真机 WOL/SSH/定时任务、
> 局域网相关）。沙箱不能运行 Docker（runc setns 被禁），也到不了
> 家里局域网，容器与真机部分一律标 UNVERIFIED，不宣称通过。

## 环境

- Go 1.22.12（`~/go1.22/go`），Node v24.22.1
- 基准：`~/workspace/wakehub`，分支 `github-main`，提交 `09c4071`
  （v0.3.2，已发布）；v0.3.3 改动未提交，留在工作区
- 沙箱：无 Docker、无局域网

## 1. 全量测试（升版后重跑，全部通过）

| 检查 | 结果 |
|---|---|
| `go vet ./...` | 通过，无输出 |
| `gofmt -l`（全部 Go 文件） | 干净，无输出 |
| `go test ./...`（14 个包） | 全部通过：agent、api、audit、auth、config、db、device、scanner、scheduler、secret、sleep、status、statuspoller、wol |
| `go test -race ./internal/scheduler/ ./internal/sleep/ ./internal/device/ ./internal/db/ ./internal/agent/` | 通过 |
| `go test -race ./internal/api/`（升版前单独跑） | 通过，178s |
| `npx vue-tsc --noEmit` | 通过 |
| Vitest | **67/67 通过**（7 个测试文件；v0.3.2 时 50 个，新增 17 个） |
| `npm run build`（vite） | 通过，3.7s |

说明：一次 5 包并行 `-race` 曾报 `internal/api FAIL`（203s，输出截
断、无 `DATA RACE` 字样），单独重跑通过（178s）。判定为并行
race 检测器负载下的超时抖动，非数据竞争——单独重跑已通过，且
该包的非 race 全量测试始终通过。

## 2. 反向验证（revert fix → 测试失败，恢复 → 通过）

每项采纳的修复都配了回归测试，且做了反向验证：

| 条目 | 反向验证方式 | 结果 |
|---|---|---|
| 1 cron 解析 | 还原 `cron.ts` | 11 个测试失败；恢复后通过 |
| 4 单次自动禁用 | 还原 `schedule.ts` | 3 个测试失败；恢复后通过 |
| 5 设备全量加载 | 还原 `devices.ts` | 4 个测试失败；恢复后通过 |
| 15 健康状态 | 删除 `serverHealth.ts` | 测试收集失败；恢复后通过 |
| 3 Agent–网络归属 | 还原校验 | 跨 Agent 指派测试失败；恢复后通过 |
| 6 CIDR 规范化 | 还原 `normalizeCIDR` | 等价 CIDR 去重测试失败；恢复后通过 |
| 8 迁移保护 | 还原守卫 | 非 005 迁移的删除漏网测试失败；恢复后通过 |
| 9 作用域 fail-closed | 还原基线回退 | DROP TABLE 场景测试失败；恢复后通过 |
| 11 保留执行历史 | 还原外键 | 删除任务后历史消失测试失败；恢复后通过 |
| 4 后端单次校验 | 还原 `ValidateSchedule` | 过去日期单次任务被接受测试失败；恢复后通过 |

例外（未做组件挂载反向验证）：问题 1 的「取消编辑后新建表单」
泄漏修复仅代码检查——仓库无组件挂载测试基建，无法以测试复现。

## 3. 升级冒烟（真二进制，v0.3.2 库 → v0.3.3）

用提交 `09c4071` 源码现场构建 v0.3.2 二进制，跑出 v0.3.2 状态库
（迁移 001–010，含 008 种子网络），手工置入测试数据，再用
v0.3.3 二进制对同一数据卷启动。**全部通过**：

- 启动前自动备份数据库（`wakehub.db.migrate-bak-*`）
- 迁移 011、012 按序应用（`schema_migrations` 1→12）
- 健康检查报 `"version":"0.3.3"`，`db: ok`
- 数量核对：devices 2→2，schedules 1→1，task_runs 1→1，
  task_run_devices 1→1，networks 4→3
- networks 4→3：等价 CIDR 对 `10.1.2.3/8` / `10.0.0.0/8` 去重为
  最早一行 `10.0.0.0/8`；未掩码种子行 `192.168.1.5/24` →
  `192.168.1.0/24`
- 设备关联重指向：原在重复网络上的设备重指向保留的网络行
- SSH 配置原地保留：`ssh_user=sg`、`ssh_use_sudo=1`、主机密钥
  指纹完整
- `task_runs.schedule_id` 外键现为 `ON DELETE SET NULL`
- 登录 + `/api/v1/devices` + `/api/v1/tasks` 升级后正常返回
- 第二次启动无新迁移（幂等）

冒烟库与二进制已删除（`/tmp` 临时目录）。

## 4. 工作流版本守卫模拟（问题 17）

- tag v0.3.2（版本未升时）→ 守卫 PASS（与当时版本一致）
- tag v0.3.3（版本未升时）→ 守卫 FAIL（符合预期：漏升版即被拦住）
- 升版后 tag v0.3.3 → 守卫应 PASS（比对逻辑与模拟一致；真实
  GitHub Actions 运行未触发，见第 6 节）

## 5. Compose / 安装器（桩化验证）

- `docker-compose.yml` YAML 解析通过；`bash -n install.sh` 通过
- 桩化模式 1 / 模式 2 跟踪通过（profiles 参数、Agent 变量跳过）
- `detect_lan_subnet` 空检测路径不再 `unbound variable` 退出

## 6. 未验证（UNVERIFIED）

以下因沙箱限制未跑，不宣称通过，留待真机：

- **Docker 镜像构建**：`docker buildx` 多架构构建、镜像推送、
  GitHub Actions 真实运行（workflow_dispatch 已删、version==tag
  守卫只在本地模拟过）
- **真实 `docker compose config` 分模式校验**：profiles 下
  server / agent 各模式的实际解析结果
- **真机安装**：`install.sh` 模式 1/2/3 在 VM 与盒子上的实跑
- **真机 WOL / SSH 关机 / 定时任务**：飞牛睡眠 + 唤醒、Windows
  唤醒、每日 00:00 / 08:00 任务的真实触发
- **局域网扫描导入**：扫描 Agent 不管理匹配网络时回落 `default`
  的真机行为
- **模式 2 局域网检测失败路径的真机实跑**（`detect_lan_subnet`
  无结果时）

## 7. 已知遗留（未修，留待未来）

- **冻结的 005 的级联风险**：005 的 `DROP TABLE devices` 在外键
  开启时（生产 DSN）会级联删 `wake_tasks` 行（`device_id
  REFERENCES devices(id) ON DELETE CASCADE`）。005 冻结不能改，
  本次未修复；真实 v0.3.2 升级路径中 005 早已应用，当前用户无法
  触发。
- **单次 cron 的 2 月 29 日**：非闰年时 `time.Date` 归一化为
  3 月 1 日做过去判断；底层 cron 届时永不触发——robfig 既有行为，
  未处理。
- **问题 1 的取消泄漏**：仅代码检查，无组件挂载测试（见第 2 节）。
