# WakeHub 迁移 002 / 003 设计（M0）

- **版本**：v0.2.0（M0 设计，随 M1 的 002、M2 的 003 实施）
- **基线**：`migrations/001_init.sql`（v0.1.0，冻结，不修改）
- **配套契约**：`docs/contract-v0.2.0.md`（认证 §2、Agent §3、设备 §4）
- **状态**：待评审。SQL 草案为实施起点；M1/M2 落地时如需调整列名或默认值，须同步更新本文档与契约。

## 0. 目标与原则

1. **不删库升级**：v0.1.0 的 `data/wakehub.db` 原地升级到 002/003；任何情况下不得要求用户删除数据库（任务书第 4、17 项硬性要求）。
2. **只加不减（除一处）**：002/003 以 `ADD COLUMN` 与新建表为主；唯一删除性操作是 003 删除种子 `local` Agent 行（契约 §3.6 的决定，见 §3.3 说明）。
3. **幂等与事务**：每个迁移文件在一个事务内执行；版本号表保证不重复应用；失败则整体回滚、服务拒绝启动（不得带病运行，见审计 B-N9 的同类要求）。
4. **001 冻结**：`001_init.sql` 一字不改。新装同样先应用 001（含种子 `local` 行），再由 003 删除该行——这个“先建后删”的小绕路是冻结基线的代价，本文档 §3.3 写明，不另改 001。
5. **旧数据零丢失**：设备、分组、定时任务、审计日志、已有 Agent 行全部保留；只有凭证类数据按 §4 规则作废或迁移（作废的是凭证的**有效性**，不是数据行）。

## 1. 迁移运行器契约（M5 实现，002/003 按此编写）

002/003 的 SQL 按以下运行器语义编写（运行器本身由 M5 实现；M1/M2 在运行器就绪前可用现有 `InitDB` 模式手动执行单文件做开发验证，正式路径以运行器为准）：

- **版本表**：

```sql
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

- **基线回填**：库里没有 `schema_migrations` 表、但 `users` / `devices` / `agents` 三张核心表已存在 → 视为“001 基线已应用”，先 `INSERT INTO schema_migrations (version) VALUES (1)`，再按序应用 002、003。全新空库 → 按序应用 001、002、003。
- **顺序与发现**：运行器扫描迁移目录，按文件名前缀数字升序应用（`001` < `002` < `003` < …）；已记录的版本跳过，不重新执行。
- **事务**：每个文件一个事务；任一语句失败 → 回滚该文件、记录失败、服务 fatal 退出（不得跳过失败的迁移继续启动）。
- **配置变化**：现状 `WAKEHUB_MIGRATIONS_PATH` 指向单个文件 `001_init.sql`；M5 改为按该路径所在的目录加载全部迁移文件（不再新增 `WAKEHUB_MIGRATIONS_DIR` 环境变量，行为等价）。M1/M2 开发验证期间可暂用单文件执行，契约行为不变。
- **文件命名**：`migrations/002_auth.sql`、`migrations/003_agent.sql`（与审计 M1/M2 的文件清单一致）。

---

## 2. 迁移 002：认证（M1）

对应契约 §2：密码修改时间（改密后旧会话失效）+ 会话撤销表（jti）。

### 2.1 SQL 草案

```sql
-- migrations/002_auth.sql
-- 目的：会话可撤销（jti）与改密后全部会话失效（password_changed_at）。
-- 原则：只加列、只建表；不改现有列、不删数据。

-- 密码修改时间：NULL = 从未修改（老用户初始态，不拒绝任何会话）；
-- 改密时置为当前时间；会话校验要求 token.iAT >= 该值（契约 §2.3）。
ALTER TABLE users ADD COLUMN password_changed_at DATETIME;

-- 会话撤销表：logout 单会话撤销、审计定位用。
-- jti 为 token payload 中的会话唯一 ID（UUID v4）；
-- expires_at 用于定期清理（过期行由清理任务删除，与 token TTL 一致为 24h）。
CREATE TABLE IF NOT EXISTS revoked_tokens (
    jti        TEXT PRIMARY KEY,
    username   TEXT NOT NULL,
    revoked_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at DATETIME NOT NULL,
    reason     TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_revoked_tokens_expires_at
    ON revoked_tokens(expires_at);
```

### 2.2 逐项说明

| 变更 | 说明 |
|---|---|
| `users.password_changed_at` | 可空。改密接口（契约 §2.4）更新它；`VerifyToken` 增加 `iat >= password_changed_at` 判断（NULL 时跳过）。**不回填历史值**——老用户首次改密前，`password_changed_at` 为 NULL，全部历史会话按原规则校验（24h 过期），不做追溯失效。 |
| `revoked_tokens` | 只增不改。`logout` 写入一行；`VerifyToken` 查 `jti` 是否存在。清理任务（M1 实现，每小时一次 + 启动时一次）删除 `expires_at < 当前时间` 的行。 |
| 未包含的内容 | 用户名不做 SQL 层约束（字符集校验在代码层，契约 §2.3）；密码哈希算法不变（bcrypt，契约 §2.3）；不建 `api_tokens` 相关表（个人 API Token 顺延，契约 §13）。 |

### 2.3 对旧会话的影响

- v0.1.0 的会话 token 是无 jti 的旧格式（`username|expiry`），v0.2.0 的 `VerifyToken` 直接拒收（契约 §2.3）。升级后**全部旧会话失效**，用户须重新登录一次。这是格式变更的必然结果，升级文档须写明，不设兼容期。
- `revoked_tokens` 初始为空；不存在需要迁移的旧撤销数据（v0.1.0 无撤销机制）。

---

## 3. 迁移 003：Agent（M2）

对应契约 §3：审批状态、per-agent token 哈希、扫描白名单；以及种子 `local` 行的退役。

### 3.1 SQL 草案

```sql
-- migrations/003_agent.sql
-- 目的：Agent 审批状态机、per-agent token 扩展、扫描白名单、种子 local 行退役。
-- 原则：只加列、只建表；唯一删除性操作是末尾的种子行删除（见 §3.3）。

-- 审批状态：pending | approved | disabled（契约 §3.2）。
-- 新增列默认 'pending'，随后按既有 authorized 回填，再删除种子行。
ALTER TABLE agents ADD COLUMN approval_status TEXT NOT NULL DEFAULT 'pending'
    CHECK (approval_status IN ('pending', 'approved', 'disabled'));

-- 握手上报的节点信息（契约 §3.4/§3.5）。
ALTER TABLE agents ADD COLUMN ip   TEXT NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN os   TEXT NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN arch TEXT NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN last_heartbeat_at DATETIME;

-- 回填：既有 authorized=1 的行视为已获批（v0.1.0 注册即授信，全部既有行均为 authorized=1，
-- 故实际效果是全部既有 Agent 回填为 approved；authorized=0 的行（若存在）回填为 pending）。
UPDATE agents
    SET approval_status = CASE WHEN authorized = 1 THEN 'approved' ELSE 'pending' END;

-- 种子 local 行退役（契约 §3.6）：删除后其 agent_tokens 行由 FK 级联删除。
-- 注意：001 新装也会先建出该行，再由本语句删除（001 冻结的代价，见 §0 原则 4）。
DELETE FROM agents WHERE id = 'local';

-- per-agent token 扩展（契约 §3.3）。agent_tokens 表本身不动结构（id/agent_id/token_hash/
-- revoked/created_at 保持），只加管理列；UNIQUE(agent_id) 保持不变（每 Agent 同时一个有效 token）。
ALTER TABLE agent_tokens ADD COLUMN label       TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_tokens ADD COLUMN last_used_at DATETIME;
ALTER TABLE agent_tokens ADD COLUMN expires_at   DATETIME;
ALTER TABLE agent_tokens ADD COLUMN rotated_at   DATETIME;

-- 扫描白名单（契约 §7.2）：每 Agent 允许扫描的网段。
CREATE TABLE IF NOT EXISTS agent_allowed_subnets (
    agent_id   TEXT NOT NULL,
    cidr       TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (agent_id, cidr),
    FOREIGN KEY (agent_id) REFERENCES agents(id) ON DELETE CASCADE
);

-- 回填：把各 Agent 注册时上报的 subnet 写入白名单（种子 local 行已删，不会进入）。
INSERT OR IGNORE INTO agent_allowed_subnets (agent_id, cidr)
    SELECT id, subnet FROM agents WHERE subnet <> '';
```

### 3.2 逐项说明

| 变更 | 说明 |
|---|---|
| `agents.approval_status` | 新状态机存储列。`CHECK` 约束防非法值写入。`authorized` 列**保留**，改为代码维护的派生镜像（`authorized = (approval_status = 'approved')`，每次写 Agent 行时同步更新；标记废弃，未来大版本可删）。不删除该列的原因：SQLite 删列需重建表，风险大于收益。 |
| `agents.ip/os/arch/last_heartbeat_at` | 握手上报字段（契约 §3.5）。老行初始为空，由 Agent 下次连接时上报回填。`version` 列不动（默认 `'0.1.0'`，新上报覆盖）。 |
| `agent_tokens` 扩展列 | `label`：token 备注（如 `主用`，预留，v0.2.0 不强制使用）；`last_used_at`：每次 WS 握手成功更新（供 UI 展示“最近使用”）；`expires_at`：预留（v0.2.0 token 不过期，恒为 NULL）；`rotated_at`：重新签发时更新。 |
| `agent_allowed_subnets` | 独立表而非 `agents` 的逗号分隔列：支持多网段、FK 级联删除、按 `(agent_id, cidr)` 去重。`INSERT OR IGNORE` 保证回填幂等。 |
| `devices.agent_id` **不动** | 003 不改写任何设备行（见 §4.4）。设备与 Agent 的外键约束由迁移 005 补（附录 B 预告）。 |

### 3.3 种子 `local` 行：为什么删、删什么、不删什么

- **删什么**：`agents` 表中 `id = 'local'` 的一行（001 种子：`Local Agent (内置)`、`192.168.1.0/24`、`authorized=1`）。其 `agent_tokens` 行（若有）由 FK 级联删除；其白名单行不会产生（删除发生在白名单回填之前，见 SQL 顺序）。
- **为什么删**：契约 §3.6。`local` 同时是种子行 ID、设备默认 `agent_id`、hub 进程内执行短路的三重含义，且可被远程 Agent 冒认（B-N8）。v0.2.0 全部任务经注册 Agent 执行，种子行没有保留价值。
- **不删什么**：不删任何设备行、不改任何设备的 `agent_id`（见 §4.4）；不删审计日志中与 `local` 有关的历史记录。
- **新装影响**：001 新装先建出种子行，003 随即删除，最终状态与升级库一致。安装器（M7）不得再依赖种子行存在。

### 3.4 `agent_tokens` 死表如何激活

现状：`agent_tokens` 表在 001 中已存在（`id, agent_id UNIQUE, token_hash, revoked, created_at`，FK 级联），但**全仓零 Go 代码引用**（审计第 2 项）——表是死的，里面正常情况下是空表。

激活方式（M2 实现，003 只负责结构）：

1. 003 只给表加管理列（§3.1），不写任何 token 行。**不存在“旧 token 数据迁移”**——全局 token 从未存进这张表，无行可迁。
2. M2 的签发路径（契约 §3.4 `POST /agents`、`approve`、`reissue-token`）生成明文 token → 存 SHA-256 哈希 → 返回明文一次。校验路径（WS 握手）查哈希、恒定时间比较、查 `revoked`。
3. 若某库的 `agent_tokens` 里已有手工插入的行（正常部署不会有），003 保留它们；其 `token_hash` 若与任何签发记录无关，则永远校验失败（无对应明文），不产生安全风险。M2 可在首次签发时按 `agent_id` 覆盖（`INSERT ... ON CONFLICT(agent_id) DO UPDATE`）。
4. `revoked` 列语义不变（`1` = 已吊销）；吊销接口只改该列，不删行（保留审计线索）。

---

## 4. 旧库升级步骤（v0.1.0 → v0.2.0，不删库）

以下步骤面向已部署 v0.1.0 的用户（以 Docker Compose 部署为例；二进制部署类推）。升级文档（M7）须按此成文。

### 4.1 升级前：备份

```bash
# 在部署机上执行（示例为 compose 部署；卷名以实际 docker-compose.yml 为准）
docker compose stop wakehub-server wakehub-agent-local
docker run --rm -v wakehub_data:/data -v "$PWD/backup":/backup alpine \
  sh -c 'cp /data/wakehub.db /backup/wakehub.db.$(date +%Y%m%d%H%M%S) && cp /data/wakehub.db-wal /backup/ 2>/dev/null; cp /data/wakehub.db-shm /backup/ 2>/dev/null; true'
ls -la backup/
```

- 备份须包含 `wakehub.db`（主库）；`-wal` / `-shm` 若存在一并备份（WAL 模式下未 checkpoint 的数据在其中）。
- 校验备份可用：`sqlite3 backup/wakehub.db.<时间戳> "PRAGMA integrity_check;"` 应返回 `ok`（部署机无 `sqlite3` 时，至少核对文件大小非零且能被新版服务打开；M5 的备份脚本会把这一步自动化）。

### 4.2 升级前：处理 `.env` 占位值（否则新版拒绝启动）

v0.1.0 的 `.env` 里通常有占位值；v0.2.0 启动时拒绝占位值（契约 §2.7），须在启动新版前处理：

```bash
# 1) 生成三个随机值（示例；任选其一工具生成）
SECRET=$(openssl rand -hex 32)
ENROLL=$(openssl rand -hex 16)

# 2) 写回 .env（只替换占位值；已有真实随机值不动）
#    WAKEHUB_SECRET_KEY=<SECRET>
#    WAKEHUB_AGENT_ENROLL_TOKEN=<ENROLL>      # 新增行；或从旧 WAKEHUB_AGENT_TOKEN 复制，见 §4.3
#    WAKEHUB_ADMIN_PASSWORD=<随机强密码>       # 若当前仍是 wakehub123，须改掉（改后用新密码登录）
```

- 注意：`WAKEHUB_ADMIN_PASSWORD` 只在 `users` 表为空时起种子作用。**已部署的老库表非空**，改 `.env` 里的密码不影响已存的管理员密码；老密码仍用 `wakehub123` 的，升级后第一件事是经改密接口修改（契约 §2.4），不要指望改 `.env` 生效。
- install.sh（M7）重跑时会自动生成缺失的随机值；但已存在的占位值**不会**被自动覆盖——升级文档须明确要求手工替换（或提供 `install.sh --rotate-secrets` 之类的显式动作，由 M7 定）。

### 4.3 全局 token 如何作废迁移

v0.1.0 的全局 token（`WAKEHUB_AGENT_TOKEN`，服务端与 Agent 共用同一值）**整体作废**，不存在“迁移旧值继续用”的路径：

1. 服务端：v0.2.0 不再读取 `WAKEHUB_AGENT_TOKEN` 做 Agent 鉴权；该变量在服务端 `.env` 中失效（保留在文件里无害，但无任何作用）。
2. 入网 token 的来源（二选一，推荐前者）：
   - **沿用旧值**：把 `.env` 里旧的 `WAKEHUB_AGENT_TOKEN` 值复制为新增的 `WAKEHUB_AGENT_ENROLL_TOKEN`。优点是已部署的 Agent 若走路径 B 重入网，无需改 Agent 侧配置；缺点是旧值若已泄露，等同于入网 token 泄露——泄露时直接换新值。
   - **换新值**：生成全新随机值作 `WAKEHUB_AGENT_ENROLL_TOKEN`，旧值彻底废弃。更干净；Agent 侧重入网时须用新值。
3. Agent 重新入网（二选一，见契约 §3.1）：
   - **路径 A（推荐）**：升级后管理员在 UI/API 对每个既有 Agent 执行 `reissue-token`（既有行已由 003 回填为 `approved`，可直接签发），把返回的一次性 token 写进对应 Agent 的 `WAKEHUB_AGENT_TOKEN`，重启 Agent 容器/进程。
   - **路径 B**：Agent 保持旧配置重启，持入网 token 连接 → 进入 `pending` → 管理员在 UI 审批 → token 经 WS 自动下发、Agent 自动持久化并重连。
4. 验收：全部 Agent 在 UI 显示 `connected`；任一 Agent 吊销测试不影响其他 Agent（M2 验收）。
5. **旧全局 token 残留处理**：升级完成后，任何写有旧全局 token 的地方（旧 `.env` 备份、聊天记录、文档草稿）都应视为已作废凭证处理——服务端已不再接受它，不存在“泄露后被利用”的问题，但也不应继续把它当作有效凭证传播。

### 4.4 种子 `local` Agent 行与遗留设备

- 003 执行时删除种子行（§3.3）。删除前无需备份该行（它是 001 的固定种子，可随时重建；且数据库全量备份已在 §4.1 完成）。
- 设备行：`agent_id = 'local'` 或 `''` 的设备**原样保留**。升级后这些设备在 UI 显示“未分配 Agent”，唤醒/检测/睡眠返回 502 `agent_not_found`（契约 §4.6）；管理员在设备编辑页重选 Agent 即可恢复，无需删库、无需手工改 SQL。
- 若部署中不存在此类设备（v0.1.0 实测的 VM 上只有 `agent-home-lan` 的设备），则此条无影响。
- 例外：若某部署的**全部**设备都是 `local` 归属且只有一个真实 Agent，管理员逐台重选即可；不提供自动重分配（自动猜测归属是错误的数据修复方式，见契约 §4.6 决策）。

### 4.5 升级执行顺序

```
1. 备份数据库（§4.1）
2. 停 server 与 agent 容器
3. 处理 .env 占位值（§4.2）：SECRET_KEY、ENROLL_TOKEN、ADMIN_PASSWORD
4. 拉取/构建 v0.2.0 镜像，启动 server
   → 运行器按序应用 002、003（日志应显示 applied 002、003）
   → 启动拒绝检查（占位值未处理会在此 fatal，见 §4.2）
5. 验证（§6 清单）
6. Agent 重新入网（§4.3），逐个确认 connected
7. 用户重新登录（旧会话全部失效，§2.3）；检查 Dashboard 数据无丢失
```

- 升级窗口：应用 002/003 是毫秒级 DDL；主要耗时在镜像构建与 Agent 逐个重入网。
- 升级失败（任一步报错）：停在当场，按 §5 回滚，不得“先跳过迁移启动旧服务将就用”。

---

## 5. 回滚

- **迁移本身是单向的**：不提供 down 迁移。回滚 = 恢复备份的数据库文件 + 启回旧版镜像/二进制 + 恢复旧 `.env`（若改过）。
- 步骤：

```bash
docker compose stop wakehub-server wakehub-agent-local
docker run --rm -v wakehub_data:/data -v "$PWD/backup":/backup alpine \
  sh -c 'cp /backup/wakehub.db.<时间戳> /data/wakehub.db && rm -f /data/wakehub.db-wal /data/wakehub.db-shm'
# 启回 v0.1.0 镜像/代码，恢复旧 .env（如有改动），启动服务
```

- 回滚后：002/003 的结构变更随旧库文件一起消失（旧库文件不含这些变更）；已签发的 per-agent token 失效（旧版不认），Agent 须改回旧全局 token 配置。
- **回滚窗口**：只在升级后、尚未产生大量新数据前回滚才干净；一旦新版写入大量审计/任务数据后再回滚，那部分数据随旧库恢复而丢失——这是备份恢复的固有语义，升级文档须写明。

---

## 6. 验证清单（升级后必查）

在部署机上执行（`sqlite3` 可用时直接查库；无 `sqlite3` 时用新版服务的 `/api/v1/agents` 与 Dashboard 核对）：

```sql
-- 1) 版本表：应为 1、2、3 三行
SELECT version FROM schema_migrations ORDER BY version;

-- 2) 002 落地
SELECT name FROM pragma_table_info('users') WHERE name = 'password_changed_at';
SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'revoked_tokens';

-- 3) 003 落地
SELECT name FROM pragma_table_info('agents')
  WHERE name IN ('approval_status', 'ip', 'os', 'arch', 'last_heartbeat_at');
SELECT name FROM pragma_table_info('agent_tokens')
  WHERE name IN ('label', 'last_used_at', 'expires_at', 'rotated_at');
SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'agent_allowed_subnets';

-- 4) 种子 local 行已删；既有 Agent 回填为 approved
SELECT id, approval_status, authorized FROM agents;
-- 期望：无 id='local' 的行；agent-home-lan 等既有行 approval_status='approved'

-- 5) 白名单回填
SELECT agent_id, cidr FROM agent_allowed_subnets;

-- 6) 遗留设备（须手工重选 Agent 的清单；为 0 行最理想）
SELECT id, name, agent_id FROM devices WHERE agent_id IN ('', 'local');

-- 7) 数据零丢失抽查：升级前后对比
SELECT (SELECT COUNT(*) FROM devices) AS devices,
       (SELECT COUNT(*) FROM schedules) AS schedules,
       (SELECT COUNT(*) FROM audit_logs) AS audit_logs;
```

功能验证（与 §6 的库检查同属升级验收）：

- 管理员用**新密码**（或旧密码，若未改）登录成功；改密后旧会话失效、新会话可用。
- `logout` 后 Cookie 失效，再请求需登录的接口返回 401。
- 全部 Agent 显示 `connected`；逐个吊销其中一个的 token 后其余 Agent 不受影响。
- 扫描、导入、唤醒、定时任务各跑一次，与升级前行为一致（除契约规定的响应结构变化外）。

---

## 7. 编号说明：004 / 005 预告

- **004（M4，任务运行记录）**：`task_runs` 加列（`started_at` / `finished_at` / `duration_ms`）+ 新建 `task_run_devices` 表 + `schedules.run_once` 列。编号理由见契约附录 B：运行记录表是 M4 的功能依赖，004 归 M4；审计原计划的 004（索引/FK）顺延为 005。
- **005（M5，索引与外键）**：补索引（`wake_tasks.device_id`、`task_runs.schedule_id`、`schedules.target_id`、`audit_logs.actor`、`devices.mac` 唯一索引）与外键（`devices.agent_id/group_id`、`schedules.target_id`），并处理既有孤儿数据。
- 002/003 的实施者**不得**提前写 004/005 的内容；M4/M5 按契约附录 B 与审计报告实施，写正式 SQL 时另起文档或在本文档后追加章节（须经评审）。

---

*文档结束。M1（002）、M2（003）实施时如需调整列名、默认值或回填逻辑，须同步更新本文档与 `docs/contract-v0.2.0.md`，并注明修改日期。*
