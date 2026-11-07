# 测试报告 — v0.2.10

> 验收时间：2026-11-06 晚。结论：**全部通过**，可交付。
> 本机沙箱跑不起 Docker 容器（与代码无关），容器内验证仍须在 VM 做，
> 见 §7。

## 1. 后端

- `go vet ./...`：干净。
- `gofmt -l`：干净。
- `go test ./... -count=1`：**14 包全过**（api 20.8s、auth 15.6s）。
- `go test -race ./... -count=1`：**14 包全过、无数据竞争**
  （api 224.5s、auth 156.4s，race 下慢属正常）。

## 2. 前端

- `npm run typecheck`（vue-tsc）：通过。
- `npm run test`（Vitest）：**35/35**（新增 `src/api/types.test.ts` 3 例，
  其余 32 例不变）。
- `npm run build`（vite）：通过。

## 3. 新增测试

- `TestMigrate009RenamesSeededNetworks`：种子行改名、默认网络与手工
  改名不动、幂等重跑无错。
- `formatNetworkLabel` 3 例：名段相同只显示名字、不同显示
  `名字（网段）`、无网段只显示名字。
- `TestMigrateFreshDB` 与 `TestMigrateIdempotent` 期望版本 1–8 → 1–9。

## 4. 抖动修复验证

- `TestAgentManagementAPI`：吊销后断言改轮询等待后，**连跑 5 次全过**，
  全量 api 包 1 次通过。本轮普通与 race 各跑一次均直接通过，未复现
  旧抖动。旧代码是时序抖动（吊销后会话清理异步），无法做确定性反向
  验证；机制上与第 3 步连接后等 `connected` 的轮询是同一 helper，
  且轮询不到 `approved` 时 5 秒后会明确失败而非静默通过。

## 5. 升级冒烟（最终版二进制，真跑）

- 造 v0.2.9 态库：仅 001–008 迁移建库，插 approved Agent（网段
  192.168.1.0/24）+ 3 台设备 + 种子网络行 `网络 192.168.1.0/24` + 手工改名
  网络「办公室」。
- 用完整迁移目录重启：迁移前备份生成（`.migrate-bak-*`）、日志
  「迁移后数量核对：devices 3→3，agents 1→1，…，networks 3→3」、
  health 报 **0.2.10**、版本表 1–9。
- 009 效果：`net-192-168-1-0-24` 改名为 `192.168.1.0/24`；「办公室」与
  「默认网络」不动；设备 3 台不变。
- 冒烟中发现的工具坑（与代码无关）：`WAKEHUB_MIGRATIONS_PATH` 要给
  **文件路径**（如 `.../001_init.sql`），目录由它推导；直接传目录会
  导致迁移目录算错、迁移不跑、`users` 表缺失。已记录在案，不改代码。

## 6. compose 校验

- python yaml 解析验证：两服务镜像 tag 均为 `wakehub:0.2.10`、
  build.args 齐全、server 端口映射
  `"${WAKEHUB_BIND_ADDRESS:-0.0.0.0}:${WAKEHUB_PORT:-8080}:8080"` 正确。

## 7. VM 验证清单（待用户侧）

- 部署：解压后直接 `docker compose up -d --build`（卷已在 v0.2.9 分离，
  无需再跑迁移脚本；009 随 Server 启动自动应用）。
- 核对：health 0.2.10、三台设备都在、网络列显示 `192.168.1.0/24`
  （不再是「网络 网络 192.168.1.0/24」）、Agent 在线。
- MIGRATION §5 容器内权限核对（v0.2.9 遗留，仍待做）：容器内 data
  目录 0700、DB 0600、token 目录/文件权限。
