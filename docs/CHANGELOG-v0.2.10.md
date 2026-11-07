# Changelog — v0.2.10

> 本文件记录 v0.2.10 的全部变更。v0.2.10 是 v0.2.9 上线当晚的打磨版：
> 修 v0.2.9 部署后用户截图里看到的网络名显示重复，并顺手修掉一个
> 已知测试抖动。升级方式与 v0.2.9 相同（卷已分离，无需再跑迁移脚本）：
> 解压后直接 `docker compose up -d --build`；009 迁移随 Server 启动
> 自动应用。

## 网络名显示重复（2026-11-06 晚）

**背景**：008 迁移把 Agent 网段网络的名字写成 `'网络 ' || subnet`
（如 `网络 192.168.1.0/24`），前端在名字前又加「网络」标签、后面又并列
网段，设备页显示成「网络 网络 192.168.1.0/24 / 网段 192.168.1.0/24」三重重复。

**改动文件**：`migrations/009_rename_seeded_networks.sql`（新建）、
`internal/db/migrate_test.go`、`web/src/api/types.ts`、
`web/src/api/types.test.ts`（新建）、`web/src/views/DevicesView.vue`、
`web/src/views/AgentsView.vue`、`web/src/components/DeviceForm.vue`。

- **新迁移 009**（不改已发布的 008）：
  `UPDATE networks SET name = cidr WHERE id LIKE 'net-%' AND cidr <> ''
  AND name = '网络 ' || cidr`——只改 008 自动种子的行，改名后名字即
  CIDR 本身（如 `192.168.1.0/24`）；用户手工改过名的网络（如「办公室」）
  与默认网络一律不动；幂等，重复执行不改变任何行。
- **前端共用显示 helper** `formatNetworkLabel(n)`（`web/src/api/types.ts`）：
  名字与网段相同时只显示名字，不同才显示 `名字（网段）`，无网段只显示
  名字。6 处显示点统一走它：设备页网络筛选下拉、桌面表网络列、手机卡片
  网络行、Agent 卡片管理网络 chips、设备表单所属网络下拉、设备表单
  Agent 网络归属提示。桌面表与手机卡片的网段行/span 另加
  `cidr !== name` 条件，不再与名字重复。
- **测试**：`TestMigrate009RenamesSeededNetworks`（种子行改名、默认网络
  与手工改名不动、幂等）；`formatNetworkLabel` 3 例；`TestMigrateFreshDB`
  与 `TestMigrateIdempotent` 期望版本 1–8 → 1–9。

## 测试抖动修复（2026-11-06 晚）

**改动文件**：`internal/api/agents_test.go`。

- `TestAgentManagementAPI` 全量并行跑偶发失败（v0.2.5 起即如此）：
  第 5 步吊销 token 后立即 GET 断言状态回 `approved`，吊销后的会话清理
  是异步的，紧接着的 GET 还可能看到 `connected`。改为复用已有的轮询
  helper `waitForAgentStatus(..., "approved")`（第 3 步连接后等
  `connected` 用的是同一 helper）。连跑 5 次 + 全量 api 包均通过。

## 版本同步

- 版本号统一 0.2.10：`internal/api/router.go`（serverVersion）、
  `internal/agent/client.go`（agentVersion）、m5/m7 测试期望、
  `web/package.json` + `package-lock.json`、SettingsView 文案、
  `docker-compose.yml`（镜像 tag ×2 + 首行注释）、Dockerfile、
  `install.sh`、README、api/deployment/security 文档标题与示例。
  迁移目录 001–009。API 总数仍为 50（009 不新增端点）。
