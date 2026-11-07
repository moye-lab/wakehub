# Changelog — v0.2.9（第一期快照）

> 本文件记录 v0.2.9 的全部变更。第一期快照（2026-11-06）只含
> 问题 1、2、10、12；第二、三期完成后在此追加。
> 升级步骤见 `docs/MIGRATION-v0.2.9.md`。

## 第一期：问题 1 / 2 / 10 / 12（2026-11-06）

### 问题 1（P1）：Server 与 Local Agent 数据卷分离

**改动文件**：`docker-compose.yml`、`scripts/migrate-agent-volume.sh`（新建）、
`docs/deployment.md`、`docs/MIGRATION-v0.2.9.md`（新建）。

- Agent 服务卷改为 `wakehub_agent_data:/app/data`；Server 仍为
  `wakehub_data:/app/data`；顶层 volumes 两卷并列。
- `WAKEHUB_AGENT_TOKEN_FILE=/app/data/agent-token` 不变，落在新卷里。
- 迁移脚本 `scripts/migrate-agent-volume.sh`：停 Agent（Server 不停）→
  跑 `scripts/backup.sh` 备份 → 建新卷 → 复制 token（内容与 0600 权限原样，
  属主改回容器内 wakehub 用户 10001，否则 Agent 读不了自己的 token）→
  校验段（token 存在、权限 0600、属主 10001、内容一致、新卷内无
  `wakehub.db`，输出 `VERIFIED` 才算通过）→ 打印下一步
  `docker compose up -d --build`。
- 旧卷原样保留可回滚；脚本从不删卷。禁止 `docker compose down -v`。
- **2026-11-06 VM 实测修复**：`backup.sh` 原先只认宿主机 `./data/wakehub.db`，
  VM 的命名卷部署下备份一步就失败，且旧版迁移脚本失败退出时没有把 Agent
  恢复运行。已修：`backup.sh` 支持从命名卷备份/恢复（一次性容器拷出
  db/-wal/-shm，宿主机 `integrity_check` + 一致备份，校验失败大声报错；
  恢复要求 Server 已停，写前把卷内当前库另存为
  `<备份包>.pre-restore-<时间戳>.db`；`restore` 也接受裸数据库文件，
  如 Server 启动前自动生成的 `.migrate-bak`）；迁移脚本加失败自恢复
  trap（中途退出自动 `docker compose start wakehub-agent-local`）。
  另修一个真 bug：备份拷出 payload 里 `for` 循环末尾无 `|| true`，
  -shm 文件不存在时整个 payload 返回 1（桩测试抓到）。修复后用假 docker
  桩完整验证：迁移成功（token 0600/10001/内容一致、新卷无 db、备份包
  含 3 行 WAL 库且 integrity ok）、强制复制失败时 trap 恢复 Agent、
  卷恢复往返、宿主机文件形态回归、裸库恢复均通过。

### 问题 2（P1）：Network 实体，MAC 唯一范围改为网络内

**改动文件**：`migrations/008_network_identity.sql`（新建）、
`internal/device/repository.go`、`internal/device/validate.go`、
`internal/api/router.go`、`internal/device/repository_test.go`、
`internal/device/validate_test.go`、`internal/db/migrate_test.go`、
`internal/api/*_test.go`（迁移清单加 008）、
前端 `web/src/api/{types,index}.ts`、`web/src/stores/devices.ts`、
`web/src/views/DevicesView.vue`、`web/src/views/AgentsView.vue`、
`web/src/components/DeviceForm.vue`、`web/src/stores/devices.test.ts`、
`docs/api.md`、`docs/MIGRATION-v0.2.9.md`。

- 迁移 008（不改写已发布的 005）：建 `networks` 表
  （id/name/description/cidr/gateway/created_at/updated_at）与
  `agent_networks` 表（Agent ↔ Network 多对多，复合主键，双 FK 级联删除）；
  种子默认网络 `default`；每个已有 Agent 的 `subnet` 非空时按 CIDR
  生成网络行（id 由 CIDR 派生，如 `192.168.1.0/24` → `net-192-168-1-0-24`）
  并写关联；`devices` 加 `network_id` 列（默认 `'default'`）；
  回填设备归属（有专属网络 → 该网络，否则 `default`）；
  删全局唯一索引，建 `UNIQUE(network_id, mac)`。
  全程无 `DELETE`，历史设备全部保留。
- 仓库层：`Network` 结构、`DefaultNetworkID = "default"`、
  哨兵错误（网络不存在 / CIDR 冲突 / 网络被引用 / 删默认网络 /
  名称必填 / CIDR 非法）；`GetDeviceByMACInNetwork`、
  `NetworkExists`、`ListNetworks`（default 首位）、`GetNetwork`、
  `GetNetworkByCIDR`、`CreateNetwork`、`UpdateNetwork`、
  `DeleteNetwork`、`ListAgentNetworks`、`SetAgentNetworks`（事务内
  先删后插，任一网络不存在整体报错）；Create/Update 的 MAC 冲突
  检查改走网络范围；`ListDevicesPage` 加 networkID 过滤。
- 校验层：NetworkID 空 → 跟随 Agent 的首个管理网络（Agent 无网络时
  回落 `default`）；非空查存在性，不存在 → `network_id / not_found`；
  MAC 冲突文案保持旧文本「MAC 地址已被设备 X 占用」不变。
- API：新增 `GET/POST /api/v1/networks`、
  `GET/PUT/DELETE /api/v1/networks/{id}`、
  `GET/PUT /api/v1/agents/{id}/networks`；设备列表支持
  `?network_id=` 过滤；更新设备支持 `network_id` 键（迁网时在新网络
  重新查重）；扫描结果富化与导入全部按网络查重
  （`resolveScanNetwork`：同 CIDR 网络存在用它，否则 `default`）。
  原有 API 路径、方法、参数、响应格式不变。
- 前端：设备列表「网络」筛选下拉与「网络」列（桌面表格与手机卡片）；
  设备表单「所属网络」选择（默认「自动（跟随 Agent）」）；
  Agents 页每张卡片展示该 Agent 管理的网络（只读 chips）。
- 测试：`TestRepositoryNetworkScopedMAC`（跨网络同 MAC 可建、同网络
  冲突、按网络查、迁网冲突、按网络过滤）、
  `TestRepositoryNetworkCRUD`、`TestRepositoryNetworkAutoResolve`（新增，
  空 network_id 跟随 Agent 网络、同网络重复冲突、显式 default 仍允许、
  ValidateDevice 同样解析）、`TestMigrate008PreservesDevices`
  （v0.2.8 态库 → 应用 008：设备数不变、归属回填正确、同 MAC 跨网络
  可建、同网络被唯一索引拒）、`TestNetworksCRUDAndDeviceNetworkScope`
  （API 全链路）、`TestScanImportNetworkScoped`（扫描导入网络范围），
  前端 `devices.test.ts` 新增 networks 两例（loadNetworks /
  networkById、load 传 network_id）。

### 问题 10（P3）：JSON 请求体只解码一次

**改动文件**：`internal/api/router.go`、`internal/api/device_test.go`。

- 新增统一辅助 `decodeOneJSON(w, r, dst, allowEmpty)`：第一次 Decode
  成功后再 Decode 一次，非 `io.EOF`（第二个 JSON 值或垃圾）一律 400。
- `decodeJSONBody` 与 `decodeAgentBody` 改调它（allowEmpty=false）；
  登录、改密、创建分组三处直解码点改调它；唤醒端点用 allowEmpty=true
  （保留「空体 = verify=false」旧语义）。
- `handleCheckDevice` 的宽容解析（`if err == nil` 才取 auto）有意保持原样，
  文档化为宽容设计。
- 测试 `TestDecodeJSONBodyTrailingContent`：正常单 JSON 201、尾部第二 JSON
  400、尾部垃圾 400、尾部空白仍 201。

### 问题 12（P2）：迁移保护补强

**改动文件**：`internal/db/migrate.go`、`cmd/server/main.go`、
`internal/db/migrate_test.go`。

- `Migrate` 重构为走新 `PendingVersions(db, dir)`；
  新增 `TableCounts(db)`（devices/agents/schedules/task_runs/audit_logs/networks，
  表不存在则跳过）、`CountsReport(before, after)`；
  设备数量保护绊线：整轮结束时若 devices 减少且本轮未应用 005 →
  返回错误「迁移后设备数量减少（%d → %d），疑似数据丢失，已拒绝启动；
  请用迁移前备份恢复」。
- 新增 `BackupBeforeMigrate(db, dbPath, dir)`：dbPath 空 / `:memory:` /
  无待处理迁移 / 库文件不存在 → 返回 `""` 不备份；否则
  `VACUUM INTO '<dbPath>.migrate-bak-<时间戳>'` + chmod 0600，返回备份路径。
- `cmd/server/main.go` 主启动与 `runResetPassword` 两条路径均已接线：
  Migrate 前调备份（失败 fatal 拒绝启动，成功打印备份路径），
  Migrate 后用 TableCounts 前后对比打印「迁移后数量核对：…」日志。
- 修复：`AppliedVersions` 在版本表尚不存在时返回空列表而非报错
  （旧库首次走迁移运行器时，备份步骤不再因此拒绝启动）。
- 测试：`TestBackupBeforeMigrate`（001–007 态库 → 备份生成、0600、
  备份库可只读打开且设备数=1、应用 008 后再调返回 `""`、`:memory:`
  返回 `""`）、`TestMigrateDeviceLossRefused`（建 001–008 库 + 1 设备 →
  加删设备的 009 → Migrate 返回含「设备数量减少」的错误）。

### 版本同步

版本号统一为 0.2.9 的位置：`internal/api/router.go` serverVersion、
`internal/agent/client.go` agentVersion、m5_test.go / m7_test.go 期望值、
`web/package.json` + `package-lock.json`、SettingsView.vue 文案、
`docker-compose.yml`（镜像 tag `wakehub:0.2.9` ×2 + 首行注释）。
已知遗漏（第三期问题 20 一并处理）：`Dockerfile` 首行注释仍写
「WakeHub v0.2.7 / wakehub:0.2.7」，历史 stale。

## 第二期：问题 3（2026-11-06，代码完成，待与第一期 VM 验证后打包）

### 问题 3（P2）：远程 Agent 明文 WS

**改动文件**：`internal/config/config.go`、`internal/config/config_test.go`、
`cmd/agent/main.go`、`docker-compose.yml`、`.env.example`、`install.sh`、
`docs/security.md`。

- 新增 `WAKEHUB_AGENT_ALLOW_INSECURE_WS`（默认 0）与
  `ValidateServerWSURL`（`internal/config`）：`wss://` 一律放行；
  `ws://` 仅回环（127.0.0.0/8、`::1`、`localhost`）放行；`ws://` 连
  非回环地址时须显式开关 = 1，否则 Agent 启动时拒绝（`AgentConfig.Validate`
  接入，`cmd/agent/main.go` 仍按原有方式 fatal）。开关与
  `WAKEHUB_AGENT_TLS_SKIP_VERIFY` 相互独立；开关启用且用明文 ws 时
  启动日志输出警告。
- `install.sh` 模式 3（远端 Agent）：提示示例改为 `wss://` 优先；
  输了非回环 `ws://` 时当场警告（token 明文传输），让用户确认是否
  放行（写入 `WAKEHUB_AGENT_ALLOW_INSECURE_WS=1`）或重输地址；
  回环 `ws://` 不打扰。`.env` 写入新增该变量。
- 注意：Server 本身不终止 TLS（见 security.md“已知不做”），`wss://`
  要求 Server 在 TLS 反向代理（或隧道）之后；纯局域网 HTTP 部署用
  `ws://` + 开关 = 1 即可。
- 测试：`TestValidateServerWSURL`（回环各形态 / wss 公网 / ws 局域网
  拒与放行 / 主机名拒 / http(s) 拒 / 空与无主机拒 / 垃圾拒）与
  `TestAgentConfigValidate` 新增 5 例；`install.sh` 模式 3 用假 docker
  桩跑三场景（ws LAN 拒后改 wss、ws LAN 接受放行、ws 回环直通），
  `.env` 内容均符合预期；`go test ./...` 14 包全过。

### 问题 4（P2）：反代下 Cookie Secure + 可信代理

**改动文件**：`internal/auth/auth.go`、`internal/auth/auth_test.go`、
`internal/api/router.go`、`internal/api/router_test.go`、
`docs/security.md`、`docs/deployment.md`、`.env.example`。

- 新增 `auth.Service.RequestIsSecure`（统一请求协议判断函数）：
  直连 TLS 即安全；否则仅当直连方在 `WAKEHUB_TRUSTED_PROXIES` 内
  且 `X-Forwarded-Proto` 为 `https`（大小写不敏感）时才算安全；
  非可信方送来的头一律不采信，多值按不安全处理（fail-closed）。
  与 `ClientIP` 的信任逻辑共用 `isTrustedProxy`。
- `internal/api/router.go` 新增 `cookieSecure`（两处下发点共用）：
  `RequestIsSecure || WAKEHUB_COOKIE_SECURE`；登录与清除 Cookie 均
  走它。`WAKEHUB_COOKIE_SECURE=1` 保留为显式强制开关；反代在信任
  列表内时 Secure 自动附加，不再需要手动设 1；纯 HTTP 局域网请求
  仍不附加 Secure，登录不受影响。
- 测试：`TestRequestIsSecure`（11 例）与
  `TestLoginCookieSecureBehindTrustedProxy`（可信代理 + https 带
  Secure / http 与无头不带 / 不可信方谎报 https 不带 / 直连 HTTP
  不带）；`go vet` 与 `internal/api`、`internal/auth` 测试通过。

### 问题 5（P2）：WS 重连后取消发往旧 Session + 统一任务状态

**改动文件**：`internal/agent/hub.go`、`internal/agent/hub_test.go`。

- `Dispatch` 新增 `carrier`：首次发送失败且会话已被替换时，重试
  发往新会话，并把后续 cancel 也发往新会话（旧实现 cancel 仍发往
  已断的旧会话，执行中的任务永远收不到取消）。
- 统一任务状态（`TaskStatus` / `TaskInfo`，状态机由 Dispatch 唯一
  推进）：pending → dispatched → completed/failed；等待中被取消时
  按 `ctx.Err()` 区分 cancelled（调用方主动取消，错误“任务已取消”）
  与 timeout（截止时间到，错误仍为“Agent 任务执行超时”）。
  `Hub.TaskStatus(id)` 可查最近 100 条记录的快照（供排障观测）。
- 测试：`TestDispatchCancelGoesToRetriedSession`（锁住旧会话写锁、
  在窗口内翻转 map，确定性复现 TOCTOU；已用旧实现反向验证——
  cancel 发往旧会话时该测试失败）与
  `TestDispatchSuccessRecordsCompleted`（完成分支记 completed）；
  另修测试里 defer 与显式解锁重复导致的 mutex panic；
  `internal/agent` 全包通过。

### 问题 6（P2）：Agent / Server 任务超时分开配置

**改动文件**：`internal/config/config.go`、
`internal/config/config_test.go`、`internal/agent/client.go`、
`internal/agent/client_test.go`、`docker-compose.yml`、
`.env.example`、`docs/contract-v0.2.0.md`。

- 新增 `parseDurationEnv`（Go 时长格式，未设置/非法/≤0 回落默认）：
  `WAKEHUB_SERVER_TASK_TIMEOUT`（默认 15s）与
  `WAKEHUB_AGENT_TASK_TIMEOUT`（默认 10s）。服务端等待默认比
  Agent 执行多 5s，给回包留余量；改动任一侧时保持“服务端 > Agent”。
- 服务端：`ServerConfig.TaskTimeout` 改走新变量，四个下发点
  （wake/check/sleep/探测）与 scheduler、statuspoller 均自动跟随，
  无需逐处改。Agent：`Client.taskTimeout()` 取配置，零值回落 10s
  默认（避免 `WithTimeout(0)` 立即过期）；`AgentConfig.Validate`
  拒绝 ≤0 的超时。
- 测试：`TestTaskTimeoutEnv`（两端默认值/显式值/非法回落/默认值
  的大小关系）、`TestAgentConfigValidate` 新增超时两例、
  `TestClientTaskTimeout`（零值回落与显式值）；
  `internal/config`、`internal/agent` 测试通过。

## 第三期（待施工，问题 7 已完成）

- 问题 7（Sleep 端点网络范围最小版）、8（Agent 网段与网卡校验）、
  9（登录限流加固）、11（文件权限）、14（字体）、15（网络列余项）、
  16（统一状态）、17（构建参数）、18（绑定地址选项，默认保持 0.0.0.0）、
  19（安装检测）、20（版本同步与文档总同步）。

### 问题 7（P2）：Sleep 端点网络范围校验（最小一致版）

**改动文件**：`internal/sleep/sleep.go`、`internal/sleep/sleep_test.go`、
`internal/api/router.go`、`internal/api/device_test.go`、
`docs/security.md`。

- 保留 `ValidateEndpoint` 基线不变；新增
  `ValidateEndpointInScope` / `ValidateIPInScope`：端点/IP 须落在
  执行 Agent 的管理网段内（Issue 2 的管理网络与 Agent 的 subnet
  字段取并集；Agent 未声明网段时只做基线校验，不拦截老 Agent）；
  回环 127/8 始终放行（Agent 本机地址）。
- 下发时校验（`handleSleepDevice`，在 Agent 在线检查之前）：
  http_sol 校验端点、SSH 校验目标 IP；越界返回 400 并说明管理网段。
- 设备级目标白名单不另设字段：设备存的端点/IP 即白名单——睡眠请求
  body 里没有端点字段，调用方无法把请求发往别的地址；Agent 侧
  `sleep.Execute` 仍会重跑基线校验。这是刻意收窄：用户 2026-11-06
  已明确不希望为家用场景加过多限制，另设白名单字段与配置项暂缓。
- 测试：`TestValidateEndpointInScope` / `TestValidateIPInScope`
  （网段内外 / 回环 / 无声明 / 基线仍拦公网与主机名）与
  `TestSleepEndpointOutsideAgentScope`（http_sol 与 SSH 越界均 400、
  网段内端点放行到真实执行）；`internal/sleep`、
  `internal/api` 测试通过。

### 问题 8（P2）：Agent 网段与实际网卡校验

**改动文件**：`internal/config/config.go`、
`internal/config/config_test.go`、`cmd/agent/main.go`、
`install.sh`、`.env.example`、`docs/MIGRATION-v0.2.9.md`。

- Agent 启动时校验 `WAKEHUB_AGENT_SUBNET` 是否在本机网卡上：
  `LocalNetworks()` 枚举已启用网卡的 IPv4 网段（回环除外，host
  网络容器看到的就是宿主机网卡），`ValidateSubnetOnNetworks`
  要求本机网段包含配置网段（配置可以是本机网段的子网，反过来不行）。
  不符时拒绝启动并提示本机实际网段；读不到网卡时只警告不拦截；
  特殊组网可用 `WAKEHUB_AGENT_SKIP_SUBNET_CHECK=1` 显式跳过。
  校验放在 `cmd/agent/main.go` 的 `Validate()` 之后（不在
  `Validate()` 里，避免单元测试的假网段被本机网卡误杀）。
- install.sh：`detect_lan_subnet` 取默认路由网卡的直连网段做默认值，
  不再写死 192.168.1.0/24；检测不到时提问改为必填（不擅自给默认值）；
  已保存的值优先于检测结果。两个网段提问循环抽成共用 `ask_subnet`。
- 测试：`TestValidateSubnetOnNetworks`（精确匹配 / 子网可接受 /
  超网拒绝 / 其他网段拒绝 / 无网段 / 非法 CIDR / IPv6）、
  `TestLocalNetworks`；真二进制三场景验证（错配立即 fatal、匹配通过、
  错配+跳过开关通过）；install.sh 桩测试（回车接受检测值 198.19.0.0/30
  入 .env、手填 192.168.1.0/24 覆盖检测值）。模式 1 的 Server 健康轮询在
  桩环境无真 Server 时 90 秒超时，属桩局限，与本次改动无关。

### 问题 9（P2）：登录限流加固

**改动文件**：`internal/auth/ratelimit.go`（新建）、
`internal/auth/auth.go`、`internal/auth/auth_test.go`、
`internal/auth/ratelimit_test.go`（新建）、`docs/security.md`。

- 现状核查：双维度（IP + 账号）、容量上限 10000 + 淘汰最旧、
  5 分钟清理 ticker 本就已有；登录入口已走 `ClientIP`
  （可信代理规则，见 security.md），本次不改行为。
- 存储抽成 `failureStore` 接口（Count/Append/Delete/Prune/Len），
  内存实现 `memoryFailureStore` 为唯一实现；需要跨重启/多实例持久化
  时可替换为 SQLite/Redis 实现，接口与限流语义不变——不为单机部署
  引入 Redis。Service 的限流方法（isRateLimited/recordFailure/
  recordSuccess/pruneFailures）改走接口，锁仍由 Service 持。
- 测试：新增 `TestMemoryFailureStore`（计数含过期丢弃 / 删除 /
  清理 / 容量上限不变量）；既有 `TestRateLimitIPAndAccount` 与
  `TestRateLimitKeysCapped`（后者改走 `Len()`）继续覆盖全路径；
  `internal/auth` 全包通过。

### 问题 11（P3）：数据目录与文件权限

**改动文件**：`internal/db/permissions.go`（新建）、
`internal/db/permissions_test.go`（新建）、
`internal/device/repository.go`、`internal/agent/client.go`、
`cmd/server/main.go`、`scripts/backup.sh`、`docs/MIGRATION-v0.2.9.md`。

- 新增 `db.EnsurePrivateDir`（建目录 0700，已存在的旧目录也显式
  Chmod 收紧——`MkdirAll` 本身不改已存在目录）与
  `db.TightenDBFiles`（库文件及 -wal/-shm 伴生文件收紧为 0600，
  伴生文件缺失时跳过；SQLite 建伴生文件时会沿用库文件权限）。
- 接线：`InitDB` 建数据目录走新函数（主启动与 reset-password 两条
  路径都受益）；主启动与 reset-password 在迁移后都调
  `TightenDBFiles`；Agent 的 token 目录（`onRegistrationApproved`）
  同样 0700（token 文件本身早已是 0600）。
- backup.sh：备份目录 `./backups` 与临时拷贝目录、`restore` 的数据
  目录全部 `chmod 700`；备份包与库文件早已是 0600。
- 测试：`TestEnsurePrivateDir` / `TestTightenDBFiles`；
  真跑 server 二进制冒烟（预建 0755 数据目录 → 启动后变 700，
  库与迁移备份均为 600）；backup.sh 实跑（备份目录 700、包 600）。
- Docker 命名卷权限：镜像里 `/app/data` 由 wakehub 用户拥有，
  容器内权限由服务端启动时的收紧逻辑负责；**容器内实测只能在 VM
  做**（本机沙箱跑不起容器），已写入迁移文档 §5 的 VM 核对命令。

### 问题 14（P3）：前端字号下限

**改动文件**：`web/src/style.css`、`web/src/components/StatusBadge.vue`、
`web/src/views/DevicesView.vue`、`web/src/views/AgentsView.vue`、
`web/src/views/LogsView.vue`。

- 规范：正文 ≥12px、重要信息 ≥14px、关键数值约 20px、手机端输入
  ≥16px、状态须颜色+文字并存；装饰性小标签（品牌小字/eyebrow/角标/
  页脚/底部导航）下限 11px。`style.css` 共 37 处替换（`.dashboard-stat
  strong` 14→20px 为关键数值；`.nav-link`、`.section-heading h2`、
  `.drawer-link` 等 13→14px；内容类 10/11→12px）；Vue 文件的
  `text-[11px]` 全部改为 `text-xs`（12px）。改后全仓无 8/9/10px 字号。
- 手机端输入 16px 早已达标（style.css 全局规则，≥768px 才 14px）；
  StatusBadge 原本就是圆点+文字+色调（颜色+文字并存），仅字号 11px
  改为 12px。

### 问题 15（P3）：设备列表明确网络归属（余项：表单关系提示）

**改动文件**：`web/src/components/DeviceForm.vue`。

- 表格列与手机卡片网络行、网络筛选已在第一期完成。本次补表单关系
  提示：① Agent select 下方显示所选 Agent 的管理网络（名称+CIDR，
  调 `listAgentNetworks` 拉取，编辑挂载与切换 Agent 时都拉）与上报
  网段（Agent 的 subnet 字段）；拉取失败只留空、不编造。② Network
  select 下方：当显式选了某网络且该网络不在所选 Agent 的管理网络
  内时，显示 amber 警告（睡眠可能被问题 7 的范围校验拦截，建议选
  自动跟随 Agent）；在管理范围内则不打扰。

### 问题 16（P3）：统一加载 / 空 / 错误状态

**改动文件**：新建 `web/src/components/ErrorState.vue`；
`web/src/stores/devices.ts`、`agents.ts`、`tasks.ts`、`logs.ts`、
`scan.ts`；`web/src/views/DashboardView.vue`、`DevicesView.vue`、
`AgentsView.vue`、`TasksView.vue`、`LogsView.vue`、`ScanView.vue`；
`web/src/stores/devices.test.ts`。

- 新建 `ErrorState` 组件：完整版（居中 ⚠️ + 标题 + 文案 +「重试」）
  与 compact 版（横排横条，有旧数据时置顶提示、不遮旧数据）。
- 五个 store 的 `load()` 不再向外抛错：失败时把可读文案写入
  `error` ref（tasks 另有 `runsError`、scan 有 `error`），旧数据
  保留；devices 的辅助下拉（分组/网络）失败只记控制台警告、不拦
  主列表。`scan.loadRecent` 失败不再导致 ScanView 的自动选首个
  Agent 不跑（顺带修掉的真问题）。
- 各页统一规则：error 且列表为空 → ErrorState 占位替代 EmptyState
  （不再误显示「暂无设备」）；error 且有旧数据 → 列表照常显示 +
  顶部 compact 横条；重试保留筛选/页码。Dashboard 的内联错误横幅
  改为 ErrorState，统计数值在未加载时显示 `—` 而非 0（失败时不
  显示假数据），并补「加载中…」。
- 测试：devices store 新增两例（失败时 error 设置、旧列表保留、
  load() 不抛；随后成功加载后 error 清空）；前端 Vitest 32/32、
  vue-tsc、vite build 均通过。

### 问题 17（P3）：Dockerfile 构建源改 Build Args

**改动文件**：`Dockerfile`、`docker-compose.yml`、`install.sh`、
`.env.example`、`docs/deployment.md`。

- Dockerfile 用 `ARG NPM_REGISTRY`（默认
  `https://registry.npmmirror.com`）与 `ARG GOPROXY`（默认
  `https://goproxy.cn,direct`），不再把源写死在 RUN/ENV 里；
  两个构建阶段各声明一次 ARG（全局 ARG 须在阶段内重声明才可见）。
- compose 两个服务的 build 段都加 `args` 透传（`.env` 可覆盖，
  不写时用 Dockerfile 默认值）；install.sh 写 `.env` 时写入两项，
  重跑保留已有值；`.env.example` 与部署文档补覆盖方式
  （`.env` 改值或单次 `--build-arg`）。

### 问题 18（P2）：Server 绑定地址选项

**改动文件**：`docker-compose.yml`、`install.sh`、`.env.example`、
`docs/deployment.md`、`docs/security.md`（评审文档）。

- 新增 `WAKEHUB_BIND_ADDRESS`，compose 端口映射改为
  `"${WAKEHUB_BIND_ADDRESS:-0.0.0.0}:${WAKEHUB_PORT:-8080}:8080"`。
- **纠正任务书**：默认保持 `0.0.0.0`，不改 `127.0.0.1`——Cloudflare
  隧道从另一台机器连服务器 IP，绑回环会直接断连。只在“仅本机访问”时才改 `127.0.0.1`，compose 注释、
  `.env.example`、部署文档三处都写明了这一条。
- install.sh 写 `.env` 时保留已有绑定地址（模式 1/2 写默认值，
  模式 3 保留旧值），重跑不丢。

### 问题 19（P3）：install.sh 多网卡选择

**改动文件**：`install.sh`。

- 新增 `detect_lan_subnets`：枚举本机所有“真实”局域网网段（一行
  一个 CIDR，默认路由网卡排第一，其余去重）；只取非虚拟网卡上的
  全局 IPv4 地址算出的网段，跳过 docker0/veth*/br-*/flannel/cni/
  tun/tap/wg/tailscale 等容器与隧道接口——容器网桥（如 docker0
  的 172.17.0.0/16）不是局域网，不能当 Agent 负责网段。
- 主流程：无保存值且本模式要问网段时，多网段（≥2 个）编号选
  （回车默认 1、0 手动输入、非数字或超 3 位重问）；单网段时沿用
  老行为（只打印一行检测提示）。模式 2（仅 server）不问网段，
  不弹菜单。
- 桩测试三场景全过（假 `ip`/`docker`/`curl`，全流程 exit 0）：
  ① 双网卡选 2 → `.env` 为 192.168.5.0/24；② 选 0 → 手动填
  192.168.1.0/24；③ 单网卡 → 不弹菜单、默认 192.168.1.0/24；三场景
  菜单里均未出现 docker0 的 172.17.0.0/16。

### 问题 20（P3）：版本同步与文档

**改动文件**：`Dockerfile`、`install.sh`、`README.md`、`docs/api.md`、
`docs/deployment.md`、`docs/security.md`、新建
`docs/SECURITY-REVIEW-v0.2.9.md`、`docs/TEST-REPORT-v0.2.9.md`。

- 版本号总检查：Dockerfile 首行注释与构建示例的 stale
  「v0.2.7 / wakehub:0.2.7」已修正为 0.2.9；install.sh 头注释
  （v0.2.0）同步为 v0.2.9；其余版本号位置（router.go、client.go、
  package.json、SettingsView 文案、compose 镜像 tag）均为 0.2.9，
  全仓 grep 无残留。
- README 与功能一致性：标题 v0.2.9；设备管理补“MAC 冲突 409（网络
  内唯一）”；关机 bullet 补 SSH 适配器与管理网段范围校验；Server
  表补 `WAKEHUB_BIND_ADDRESS` / `WAKEHUB_SERVER_TASK_TIMEOUT` /
  `WAKEHUB_STATUS_CHECK_INTERVAL`；Agent 表修正
  `WAKEHUB_AGENT_SUBNET` 默认值（无、必填、install.sh 自动检测）
  并补 `WAKEHUB_AGENT_TASK_TIMEOUT` /
  `WAKEHUB_AGENT_ALLOW_INSECURE_WS` /
  `WAKEHUB_AGENT_SKIP_SUBNET_CHECK`；API 总数 43 → **50**（实数，
  路由表 grep 51 行减 `/` 静态兜底 1 行），表内补 7 个网络端点；
  目录结构迁移 001–005 → 001–008；已知限制标题同步 v0.2.9。
- security.md：标题与引言同步 v0.2.9；睡眠 bullet 的旧文案
  「适配器仅 http_sol」改为两种适配器；“已知不做”删掉已实现的
  “Linux/Windows 本地关机适配器”（SSH 适配器自 v0.2.5 已有），
  剩余项版本号同步 v0.2.9；补数据目录/备份目录/token 目录 0700。
- api.md：接口总数 43 → 50，标题与示例 version 同步 0.2.9。
- deployment.md：标题同步 v0.2.9；构建示例 tag 0.2.9；服务端口表
  补绑定地址；新增构建源覆盖方式；反代节补信任列表与 Cookie
  Secure 的关系（问题 4）。
- 问题 13（导航 Emoji 统一 SVG）按用户设计图规矩暂缓，不进本版。

### 修复记录：一次性容器 ENTRYPOINT（2026-11-06 晚，VM 实测）

**改动文件**：`scripts/backup.sh`、`scripts/migrate-agent-volume.sh`、
`docs/MIGRATION-v0.2.9.md`（§1.3）。

- 现象：用户在 VM 上跑修复后的第一期包，迁移脚本在备份一步 die，
  报错是 server 的占位值拒绝启动（`WAKEHUB_ADMIN_PASSWORD 仍为
  占位值 "wakehub123"…`）。**用户的 `.env` 与密码没有问题**：
  报错里的占位值是一次性容器没传环境变量时 server 取的默认值。
- 根因：镜像 `ENTRYPOINT ["/app/wakehub-server"]`，五处一次性
  `docker run` 写的是 `docker run … 镜像 sh -c '…'`，没加
  `--entrypoint`——真实 docker 跑起来的实际是
  `/app/wakehub-server sh -c '…'`，server 启动即做占位值校验而
  拒绝启动，拷贝随之失败。第一期的假 docker 桩对任何 `docker run`
  都直接返回成功，没模拟 entrypoint 行为，所以本地没测出来。
- 修复：五处全部加 `--entrypoint sh`（backup.sh：拷出、写回、
  恢复前预存；migrate 脚本：复制 token、校验）。
- 回归测试：重建严格桩（无 `--entrypoint` 的 `docker run` 按真实
  行为打印 server 占位值拒绝并 exit 1；有则按 `-v` 映射真实执行
  payload）。阴性对照：旧脚本 + 严格桩逐字复现 VM 失败（备份 die、
  trap 恢复 Agent）；阳性对照：新脚本全流程 exit 0（备份 tar.gz
  integrity_check 通过、3 台设备；token 复制 VERIFIED、0600、
  属主 10001、新卷无库；restore 到卷往返通过，库 0600/10001）。
  桩本身修过两个 bug（卷名须解析到假卷目录；路径改写须占位符两步
  走，宿主机路径含 `/new/` 会级联误改）。

### 修复记录：一次性容器以 root 运行（2026-11-06 晚，VM 实测，第二轮）

**改动文件**：`scripts/backup.sh`、`scripts/migrate-agent-volume.sh`、
`docs/MIGRATION-v0.2.9.md`（§1.3）。

- 现象：entrypoint 修复包在 VM 上跑，备份一步新报错
  `cp: can't stat '/out/wakehub.db': Permission denied`，随后
  `数据卷内没有 wakehub.db`。**数据没有坏**：源库可读，是写目标
  被拦。
- 根因：镜像 `USER wakehub`（uid 10001），一次性容器默认也是
  10001；而备份的临时目录是宿主机上运行脚本的用户（sg）的 0700
  目录，10001 无法进入，cp 写不进 /out。同流程里 token 复制的
  `chown 10001:10001` 非 root 也做不了，是同一根因的第二个潜伏点。
- 修复：五处一次性 `docker run` 全部加 `--user 0`。写回卷内的
  文件仍由脚本显式 chmod 600 + chown 10001:10001，权限与原来一致。
- 回归测试：严格桩升级为模拟容器用户（默认以 uid 10001 跑 payload，
  `--user 0` 时以 root 跑；夹具卷内文件按 VM 真实情况设 10001/
  0600，夹具目录 755——此前桩目录因 umask 007 是 770，10001 连卷
  目录都进不去，挡在了真问题前面，已修正）。阴性对照：上一版脚本
  + 新桩复现 VM 的 Permission denied（源可读、目标被拦）；阳性
  对照：新脚本全流程 exit 0（备份 integrity_check 通过、3 台设备；
  token VERIFIED 0600/10001；restore 到卷往返通过，库 0600/10001）。

### 修复记录：拷出的库改回宿主机属主（2026-11-06 晚，VM 实测，第三轮）

**改动文件**：`scripts/backup.sh`、`docs/MIGRATION-v0.2.9.md`（§1.3）。

- 现象：`--user 0` 修复包在 VM 上跑，拷贝本身成功了，死在校验：
  Python `sqlite3.OperationalError: unable to open database file`，
  随后误报成「校验失败（可能正在写入导致复制撕裂）」。**数据库没有
  损坏**：源库可读，是拷出的副本打不开。
- 根因：一次性容器以 root 拷文件，落到宿主机临时目录里的副本就是
  root 属主 0600；校验是以运行脚本的用户（sg）在宿主机跑的，打不开
  root 属主 0600 的文件。`--user 0` 治好了容器内的权限，宿主机一侧
  的属主问题是同一链条的下一环。
- 修复：两处向宿主机拷出的 payload（备份拷出、恢复前预存）拷完后
  `chown $HOST_UID:$HOST_GID` 改回运行脚本的用户属主（uid/gid 由
  `-e` 传进容器）；`backup_db_from_volume` 校验前先确认文件可读，
  读不到时直接报权限问题，不再误报成 WAL 撕裂。
- 回归测试：严格桩升级为 root daemon 代跑载荷（桩本身以 uid 1000
  被调用时提权被 no-new-privileges 拦住），脚本以 uid 1000 跑，
  夹具卷内文件 10001/0600。阴性对照：上一版脚本复现 VM 失败（同
  一句「校验失败」）；阳性对照：新脚本全流程 exit 0（备份
  integrity 通过、3 台设备，包内库属主 1000；token VERIFIED
  0600/10001；restore 到卷往返通过，库 0600/10001；第二轮 restore
  预存文件属主 1000）。
