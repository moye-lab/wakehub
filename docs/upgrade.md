# WakeHub 升级文档：v0.1.0 → v0.2.0（不删库）

本文面向已部署 v0.1.0 的用户。升级路径：备份 → 处理 `.env` 占位值 →
处理全局 token → 拉新版 → 启动（迁移自动补齐 002–005）→ 重新登录 →
Agent 重新入网 → 验收。**全程不删库。**

## 升级前：备份

```bash
# 在部署机上执行（compose 部署；卷名以实际 docker-compose.yml 为准）
cd wakehub
bash scripts/backup.sh backup ./backup
ls -la backup/
```

备份包是 `wakehub-backup-<时间戳>.tar.gz`（0600），内含 `wakehub.db`
与 `.env`。备份须包含 `-wal` / `-shm` 若存在（WAL 模式下未 checkpoint
的数据在其中；备份脚本用 sqlite3 联机备份，已包含 WAL 内容）。

校验备份可用：`sqlite3 backup/wakehub.db.<时间戳> "PRAGMA integrity_check;"`
应返回 `ok`（部署机无 `sqlite3` 时，至少核对文件大小非零且能被新版
服务打开）。

回滚：恢复前先停服务，`bash scripts/backup.sh restore <备份包>` 会先
把当前库另存为 `.bak-<时间戳>` 再恢复。`.env` 默认不恢复（避免覆盖
现役密钥），须显式加 `--include-env`。

## 升级前：处理 .env 占位值（否则新版拒绝启动）

v0.2.0 启动时拒绝占位值（契约 §2.7），须在启动新版前处理：

```bash
# 1) 生成三个随机值（示例；任选其一工具生成）
SECRET=<redacted>
ENROLL=$(openssl rand -hex 16)

# 2) 写回 .env（只替换占位值；已有真实随机值不动）
#    WAKEHUB_SECRET_KEY=<SECRET>
#    WAKEHUB_AGENT_ENROLL_TOKEN=<redacted>
#    WAKEHUB_ADMIN_PASSWORD=<redacted>
```

注意：

- `WAKEHUB_ADMIN_PASSWORD` 只在 `users` 表为空时起种子作用。**已部署
  的老库表非空**，改 `.env` 里的密码不影响已存的管理员密码；老密码仍用
  `wakehub123` 的，升级后第一件事是经改密接口修改（网页右上角改密），
  不要指望改 `.env` 生效。
- install.sh 重跑时会自动生成缺失的随机值；但已存在的占位值**不会**
  被自动覆盖——须按上表手工替换。

## 全局 token 如何作废迁移

v0.1.0 的全局 token（`WAKEHUB_AGENT_TOKEN`，服务端与 Agent 共用同一值）
**整体作废**，不存在“迁移旧值继续用”的路径：

1. 服务端：v0.2.0 不再读取 `WAKEHUB_AGENT_TOKEN` 做 Agent 鉴权；该变量
   在服务端 `.env` 中失效（保留在文件里无害，但无任何作用）。
2. 入网 token 的来源（二选一，推荐前者）：
   - **沿用旧值**：把 `.env` 里旧的 `WAKEHUB_AGENT_TOKEN` 值复制为新增的
     `WAKEHUB_AGENT_ENROLL_TOKEN`。优点是已部署的 Agent 若走路径 B 重入网，
     无需改 Agent 侧配置；缺点是旧值若已泄露，等同于入网 token 泄露——
     泄露时直接换新值。
   - **换新值**：生成全新随机值作 `WAKEHUB_AGENT_ENROLL_TOKEN`，旧值彻底
     废弃。更干净；Agent 侧重入网时须用新值。
   - install.sh 重跑时会自动做这一步：旧 `.env` 有全局 token 且无入网
     token 时，沿用旧值作入网 token。
3. Agent 重新入网（二选一）：
   - **路径 A（推荐）**：升级后管理员在 UI/API 对每个既有 Agent 执行
     `reissue-token`（既有行已由 003 回填为 `approved`，可直接签发），
     把返回的一次性 token 写进对应 Agent 的 `WAKEHUB_AGENT_TOKEN`，
     重启 Agent 容器/进程。
   - **路径 B**：Agent 保持旧配置重启，持入网 token 连接 → 进入
     `pending` → 管理员在 UI 审批 → token 经 WS 自动下发、Agent 自动
     持久化并重连。
4. 验收：全部 Agent 在 UI 显示 `connected`；任一 Agent 吊销测试不影响
   其他 Agent。
5. **旧全局 token 残留处理**：升级完成后，任何写有旧全局 token 的地方
   （旧 `.env` 备份、聊天记录、文档草稿）都应视为已作废凭证处理——
   服务端已不再接受它，不存在“泄露后被利用”的问题，但也不应继续把它
   当作有效凭证传播。

## 种子 local Agent 行与遗留设备

- 003 执行时删除种子 `local` 行。删除前无需备份该行（它是 001 的固定
  种子，可随时重建；且数据库全量备份已完成）。
- 设备行：`agent_id = 'local'` 或 `''` 的设备**原样保留**。升级后这些
  设备在 UI 显示“未分配 Agent”，唤醒/检测/睡眠返回 502
  `agent_not_found`；管理员在设备编辑页重选 Agent 即可恢复，无需删库、
  无需手工改 SQL。
- 不提供自动重分配（自动猜测归属是错误的数据修复方式）。

## 升级执行顺序

```bash
1. 备份数据库（见上）
2. 处理 .env 占位值（见上）
3. 处理全局 token（见上）
4. 停旧服务：docker compose down        # 不加 -v（-v 会删卷删库）
5. 拉新版代码：git pull                  # 或重新解压新版包
6. 重跑安装器：bash install.sh          # 配置保留、服务重建；占位值已手工处理过
7. 启动后验收（见下）
```

启动时迁移运行器按序应用 002–005（`schema_migrations` 记版本，幂等
重跑，失败即拒绝启动）：

- 002：`users.password_changed_at` + `revoked_tokens` 表。**旧会话
  升级后全部失效，须重新登录一次**（旧格式 token 直接拒收）。
- 003：`agents.approval_status` 等列 + `agent_tokens` 扩展列 + 扫描
  白名单表；删除种子 `local` 行；既有 Agent 行回填为 `approved`。
- 004：任务运行记录（`task_runs` 扩展列 + `task_run_devices` 表；
  `schedules.run_once`）。
- 005：索引与外键（`devices.mac` 唯一索引等；`devices.agent_id` 与
  `schedules.target_id` 两处外键有意不建——前者违反契约 §4.6 遗留
  `local` 设备，后者是多态目标，由验证层在写入时强制）。

## 升级后验收

1. 健康检查：`curl http://127.0.0.1:8080/api/v1/health` 返回
   `{"code":0,"data":{"status":"ok","version":"0.2.0","db":"ok",...}}`。
2. 重新登录（旧会话已失效）；登录页不得再显示默认密码明文。
3. 全部 Agent 在 UI 显示 `connected`（路径 A/B 任一完成后）。
4. 任一设备一键唤醒成功（`sent`）；`verify` 回检能区分 `sent` 与 `booted`。
5. 任一 Agent 吊销测试不影响其他 Agent。
6. 遗留 `local` 设备在 UI 显示“未分配 Agent”，重选 Agent 后恢复。
7. 审计日志能查到升级前的旧记录（保留 90 天）。

## 忘记密码（升级后也适用）

不要删库改密。用 CLI 重置（须能执行容器命令）：

```bash
docker compose exec wakehub-server /app/wakehub-server reset-password
```

交互式读取两次新密码（终端不回显），校验强度同改密（≥ 8 位），
bcrypt 入库并更新 `password_changed_at`（全部会话失效）。
