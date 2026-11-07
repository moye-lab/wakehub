# WakeHub v0.2.9 升级与迁移说明

> 适用：从 v0.2.8（或更早版本）升级到 v0.2.9。
> 先读一遍再动手。全程不删除任何数据卷、不删除任何设备，
> 升级失败可按文末回滚。

## 0. 本次升级包含两类迁移

| 迁移 | 内容 | 是否自动 |
| --- | --- | --- |
| 数据库 008（`migrations/008_network_identity.sql`） | 新增 Network 实体：`networks` / `agent_networks` 两张表；devices 加 `network_id` 列；MAC 唯一范围从全局改为网络内 | 服务启动时自动应用（启动前自动备份，见 §2） |
| 数据卷分离（问题 1） | Local Agent 从 Server 的数据卷拆出，改挂独立卷 `wakehub_agent_data`，Agent 不再能读到 Server 的数据库 | **手动执行脚本** `scripts/migrate-agent-volume.sh`（见 §1） |

**升级顺序必须先 §1 再 §2**：先迁卷（Agent 拿到独立卷里的 token），
再 `docker compose up -d --build`（Server 启动时应用 008 迁移）。
顺序反了不会丢数据，但 Agent 会在新卷里找不到 token 而离线，
需重跑一次迁移脚本才能恢复。

---

## 1. Agent 数据卷迁移（手动，必做）

### 1.1 变化

| 服务 | 升级前 | 升级后 |
| --- | --- | --- |
| Server | `wakehub_data:/app/data` | 不变 |
| Local Agent | `wakehub_data:/app/data`（与 Server 共用，能读到整个库） | `wakehub_agent_data:/app/data`（独立卷，只含自己的 token） |

`WAKEHUB_AGENT_TOKEN_FILE=/app/data/agent-token` 的值不变，
只是这个路径现在落在独立卷里。

### 1.2 执行步骤

```bash
# 在 WakeHub 部署目录（docker-compose.yml 所在目录）执行
bash scripts/migrate-agent-volume.sh
```

脚本会依次做：

1. 停掉 Agent 容器（Server 不停，Web 界面不受影响）。
2. 调 `scripts/backup.sh` 备份 Server 数据卷。**注意**：VM 部署的数据在
   Docker 命名卷里、宿主机没有 `./data/wakehub.db`，`backup.sh` 已支持
   从命名卷备份（一次性容器拷出 db/-wal/-shm，再在宿主机做
   `integrity_check` + 一致备份；校验失败会大声报错并停止迁移）。
3. 创建独立卷 `wakehub_agent_data`（已存在则复用）。
4. 把旧卷里的 `agent-token` 复制到新卷（文件内容与 0600 权限原样保留；
   若新卷里已有内容一致的 token 则跳过；旧卷里没有 token 则跳过，
   全新安装走这条路）。
5. 校验：新卷 token 存在、权限 0600、属主 10001（容器内 wakehub 用户）、
   内容与旧卷一致、**新卷里没有 `wakehub.db`**。输出 `VERIFIED` 才算通过。
6. 打印下一步命令。

随后按提示执行：

```bash
docker compose up -d --build
```

### 1.3 迁移失败时怎么办

脚本中途失败退出时，会**自动把 Agent 恢复运行**（失败自恢复），
不需要手动处理。若你手动中断了脚本（Ctrl-C），或发现 Agent 离线，
用下面这条恢复——**注意不要用 `docker compose up -d`**：

```bash
docker compose start wakehub-agent-local
```

原因：此时磁盘上的 `docker-compose.yml` 已是新版（Agent 改挂新卷），
`up -d` 会按新卷重建 Agent 容器；新卷还是空的，Agent 会丢身份、
拿入网 token 重新注册。`start` 只启动已停掉的旧容器（仍挂旧卷、
token 还在），才是安全的恢复。

#### 已修复的失败：一次性容器未覆盖 ENTRYPOINT（2026-11-06 晚）

如果你在备份一步看到 server 的占位值拒绝启动（`WAKEHUB_ADMIN_PASSWORD
仍为占位值 "wakehub123"…`），**不是你的 `.env` 或密码有问题**：
镜像的 `ENTRYPOINT` 是 server 二进制，旧版 `backup.sh` /
`migrate-agent-volume.sh` 的一次性容器写的是
`docker run … 镜像 sh -c '…'`，没加 `--entrypoint`，真实 docker
跑起来的实际是 `/app/wakehub-server sh -c '…'`——server 启动即做
占位值校验（一次性容器没传环境变量，取的是默认占位值）而拒绝启动，
拷贝随之失败。已修：五处一次性 `docker run` 全部加
`--entrypoint sh`（backup.sh 三处：拷出、写回、恢复前预存；
migrate 脚本两处：复制 token、校验）。旧包不要再用，请用修复后的
包重跑（你的 `.env` 不用改、密码不用改）。

#### 已修复的失败：拷出的库宿主机读不到（2026-11-06 晚，第三轮）

如果你在备份一步看到 Python 的 `sqlite3.OperationalError: unable to
open database file`，随后 `从数据卷拷出的数据库校验失败（可能正在
写入导致复制撕裂）`，**也不是数据库损坏、更不是正在写入**：一次性
容器以 root 拷文件，拷到宿主机临时目录里的就是 root 属主 0600，
而校验是你自己的账号在跑，打不开那个文件。已修：两处向宿主机拷
出的 payload（备份拷出、恢复前预存）拷完后把文件改回运行脚本的
用户属主（`chown $HOST_UID:$HOST_GID`，uid/gid 由 `-e` 传进容器）；
校验前先确认文件可读，读不到时直接报权限问题，不再误报成 WAL 撕裂。

#### 已修复的失败：一次性容器权限不足（2026-11-06 晚，第二轮）

如果你在备份一步看到 `cp: can't stat '/out/wakehub.db': Permission
denied`，随后 `数据卷内没有 wakehub.db`，**也不是你的数据坏了**：
镜像以非 root 用户 `wakehub`（uid 10001）运行，一次性容器默认也
是这个用户，而备份的临时目录是宿主机上你自己的 0700 目录，
10001 连进都进不去，拷贝写不进去。已修：五处一次性 `docker run`
全部加 `--user 0`（以 root 跑一次性容器；写回卷内的文件仍由脚本
显式 `chmod 600` + `chown 10001:10001`，权限与原来一致）。

### 1.4 升级后验证（逐项核对）

```bash
# 1. 两个容器都在跑
docker compose ps

# 2. Agent 容器内看不到 Server 的数据库（应报错 No such file）
docker compose exec agent sh -c 'ls /app/data/wakehub.db; echo exit=$?'
# 期望：ls: /app/data/wakehub.db: No such file or directory，exit=1

# 3. Agent 容器内只有自己的 token
docker compose exec agent sh -c 'ls -la /app/data'
# 期望：只有 agent-token 一个文件，权限 -rw-------，属主 wakehub

# 4. Agent 在线：打开 Web 界面 → Agents 页，Local Agent 状态为「在线」
```

### 1.5 回滚（卷迁移）

旧卷 `wakehub_data` 在整个过程中**原样保留、从未被删除**，
回滚就是把它重新挂回 Agent：

```bash
# 1. 停掉 Agent
docker compose stop agent

# 2. 把 docker-compose.yml 里 agent 服务的卷临时改回
#    wakehub_agent_data:/app/data → wakehub_data:/app/data
#    （只改这一行；或直接用 v0.2.8 的 docker-compose.yml 覆盖）

# 3. 重启 Agent
docker compose up -d agent
```

**禁止执行 `docker compose down -v`**：它会删掉所有命名卷，
包括 Server 的数据库卷，数据无法恢复。

---

## 2. 数据库 008 迁移（自动，随 Server 启动）

### 2.1 变化

- 新建 `networks` 表（id / name / description / cidr / gateway / created_at / updated_at）
  与 `agent_networks` 表（Agent ↔ Network 多对多，复合主键，双外键级联删除）。
- `devices` 表加 `network_id` 列（默认 `'default'`）。
- 删掉 005 的全局唯一索引 `idx_devices_mac_unique`，
  改为网络内唯一索引 `idx_devices_network_mac_unique (network_id, mac)`。
- **历史设备全部保留**：008 全程无 `DELETE`。
  每个已有 Agent 的 `subnet` 非空时，迁移按 CIDR 生成一个网络行
  （id 由 CIDR 派生，如 `192.168.1.0/24` → `net-192-168-1-0-24`），
  并把该 Agent 的设备回填到对应网络；无网段的 Agent 的设备进 `default`。
- 已发布的 005 迁移文件**没有被改写**（005 里那条按 mac 只留 `MIN(rowid)` 的
  静默删除是历史行为，008 不重写历史，只保证此后不再发生）。

### 2.2 启动前自动备份

Server 启动时，若检测到有待应用的迁移，先把数据库文件复制为：

```
<数据库文件>.migrate-bak-<YYYYMMDD-HHMMSS>
```

（例如 `wakehub.db.migrate-bak-20261106-175756`），权限 0600，
成功后才应用迁移，并在日志里打印：

```
[WakeHub] 检测到待应用迁移，已备份数据库到 <路径>
[WakeHub] 迁移后数量核对：devices 3→3，agents 1→1，…，networks 0→2
```

若备份失败，Server **拒绝启动**（日志写明原因），请先检查磁盘空间与目录权限，
不要绕过备份强行启动。

### 2.3 迁移后数量核对（绊线）

整轮迁移结束时，Server 对比迁移前后的表行数并打印到日志。
若 `devices` 数量减少（且本轮未应用 005——005 本身含删重复逻辑），
Server 返回错误并**拒绝启动**：

```
迁移后设备数量减少（N → M），疑似数据丢失，已拒绝启动；请用迁移前备份恢复
```

注意：这条绊线只能检测并拒启，删除本身发生在坏迁移已提交的事务内，
绊线拦不住已发生的删除。恢复办法见 §2.5。

### 2.4 升级后验证（数据库）

```bash
# 进 Server 容器（或在宿主机上对数据库文件）执行 sqlite3
docker compose exec server sqlite3 /app/data/wakehub.db <<'SQL'
-- 设备总数与升级前一致（本机 3 台为例）
SELECT COUNT(*) FROM devices;
-- 按网络分组：应看到 net-192-168-1-0-24（或你的网段）与 default
SELECT network_id, COUNT(*) FROM devices GROUP BY network_id;
-- 网络内重复 MAC 应为 0 行
SELECT network_id, mac, COUNT(*) c FROM devices GROUP BY network_id, mac HAVING c > 1;
-- 网络表：default + 每个有网段的 Agent 各一行
SELECT id, name, cidr FROM networks;
-- Agent 与网络的关联
SELECT agent_id, network_id FROM agent_networks;
-- 迁移版本应含 8
SELECT version FROM schema_migrations ORDER BY version;
SQL
```

Web 界面核对：设备页应出现「网络」列与筛选下拉；每台旧设备的网络
显示为其 Agent 的网段（单网段部署下即 `网络 192.168.1.0/24` 这类名称）；
新建设备不填网络时自动跟随其 Agent 的网络（表单里显示为
「自动（跟随 Agent）」），显式选「默认网络」仍可手动指定。

### 2.5 回滚（数据库 008）

008 只做加表、加列、换索引，没有删除或改写业务数据，
回滚即用启动前自动生成的备份文件恢复。备份文件在 Server 容器内
（`/app/data/wakehub.db.migrate-bak-<时间戳>`），先拷出来，
再用 `backup.sh restore` 恢复（它接受裸数据库文件，也支持写回命名卷）：

```bash
# 1. 停掉 Server 与 Agent
docker compose stop wakehub-server wakehub-agent-local

# 2. 查看自动备份文件（在 Server 容器内）
docker compose exec wakehub-server ls /app/data/ | grep migrate-bak

# 3. 把备份拷出来（把 <时间戳> 换成实际文件名）
docker cp wakehub-server:/app/data/wakehub.db.migrate-bak-<时间戳> /tmp/rollback.db

# 4. 恢复到数据卷（恢复前会把卷内当前库另存为 /tmp/rollback.db.pre-restore-<时间戳>.db）
bash scripts/backup.sh restore /tmp/rollback.db --force

# 5. 用 v0.2.8 的 docker-compose.yml / 镜像重新拉起
docker compose up -d --build
```

回滚后数据库回到 008 之前的状态（网络表不存在，MAC 全局唯一恢复），
设备与 Agent 数据与升级前一致。回滚不影响 §1 的卷迁移：
卷迁移后 Agent 的 token 在独立卷里，回滚到 v0.2.8 的 compose 时
Agent 会重新挂回 `wakehub_data`，token 仍在旧卷里，不会丢失身份。

---

## 3. 升级检查清单（一次性过一遍）

- [ ] 已执行 `scripts/migrate-agent-volume.sh`，输出 `VERIFIED`
- [ ] `docker compose up -d --build` 后两个容器都在跑
- [ ] 启动日志里有迁移前备份路径与「迁移后数量核对」
- [ ] Web 健康接口报 `version: 0.2.9`（`GET /api/v1/health`）
- [ ] 设备数与升级前一致，网络列显示正常
- [ ] Agents 页 Local Agent 在线
- [ ] Agent 容器内看不到 `wakehub.db`（§1.4 第 2 步）
- [ ] 备份文件已确认存在（§2.2 的路径），升级后一周内不要删除

## 4. 新增的启动校验（v0.2.9 问题 8，Agent 侧）

升级后 Agent 启动时会校验 `.env` 里的 `WAKEHUB_AGENT_SUBNET` 是否在
本机网卡上。**网段正确时无任何变化**（本机部署的网段本来就与网卡一致）。
如果 Agent 容器起不来、日志报 `WAKEHUB_AGENT_SUBNET ... 与本机网卡不符`，
按日志里给出的本机实际网段改 `.env` 里的值，再 `docker compose up -d`：

```bash
# 示例：日志说本机网段是 192.168.1.0/24，而 .env 里写的是 192.168.1.0/24
sed -i 's/WAKEHUB_AGENT_SUBNET=.*/WAKEHUB_AGENT_SUBNET=192.168.1.0\/24/' .env
docker compose up -d
```

特殊组网（Agent 管理的网段靠路由到达、本机没有直连网卡）可在 `.env`
里加 `WAKEHUB_AGENT_SKIP_SUBNET_CHECK=1` 跳过本检查；正常部署不要开。

## 5. 容器内权限核对（v0.2.9 问题 11，VM 上跑）

升级并启动后，在 VM 的部署目录执行（本机沙箱跑不起容器，无法代测）：

```bash
# 期望：数据目录 700、库文件与伴生文件 600、token 目录 700、token 文件 600
docker exec wakehub-server stat -c '%a %n' /app/data /app/data/wakehub.db*
docker exec wakehub-agent-local stat -c '%a %n' /app/data /app/data/agent-token
# 宿主机备份目录也应为 700、备份包 600（跑一次备份即验证）
bash scripts/backup.sh backup
stat -c '%a %n' ./backups ./backups/*.tar.gz | tail -2
```

任一项不符时不要手动 `chmod` 容器外卷目录（属主是容器内 wakehub 用户，
宿主机 root 改权限可能反把属主弄乱），先把输出贴出来再处理。
