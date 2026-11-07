# Changelog — v0.3.3

> 本文件记录 v0.3.3 的全部变更。v0.3.3 是一次**审查整改版本**：GPT
> 对 v0.3.2 源码的全面审查给出 19 项任务书，按证明门槛逐条核验后
> 采纳 14 项、驳回 5 项、暂缓 0 项。只修被证明为真实触发缺陷的项，
> 不改产品设计、不删功能、不动已发布迁移 001–010、不关闭任何安全
> 检查。逐项裁决与证据见 `docs/REVIEW-VERDICT-v0.3.3.md`；测试报告
> 见 `docs/TEST-REPORT-v0.3.3.md`。升级方式：新增迁移 011、012，
> 已有部署直接换镜像重跑同一条命令（数据卷原地保留，启动时自动备
> 份并按序应用新迁移，老数据原地可用）。

## 裁决总览

| # | 任务书条目 | 裁决 | 一句话 |
|---|---|---|---|
| 1 | 编辑任务时 cron 被默认值覆盖 | 采纳 | 编辑表单不回读原 cron，一律回到默认每天 08:00；取消编辑还会把编辑目标泄漏进下次新建表单 |
| 2 | Compose 三模式互相干扰 | 采纳 | 无 profiles；agent `depends_on: service_healthy` 使纯 agent 模式自动拉起 server；整文件 `:?` 插值阻断全新单模式安装 |
| 3 | Agent 与网络归属无校验 | 采纳 | 设备 `network_id` 与 `agent_id` 无关联检查，跨 Agent 指派被接受（测试证实） |
| 4 | 单次任务日期可设为过去 | 采纳 | 「单次」时间模式与 `run_once` 复选框是两个独立开关；前后端均无过去日期校验；单次任务会每年重复触发（真机证实） |
| 5 | 设备选择器只读一页 | 采纳 | 表单只读 50 条一页，第 51 台起的设备选不到 |
| 6 | CIDR 未规范化 | 采纳 | 字符串去重，`192.168.1.1/24` 与 `192.168.1.0/24` 被当成两个网络；无唯一索引（测试证实） |
| 7 | 单容器隔离误标 | 采纳（仅文档） | 同 uid + 共享 `/app/data`，但 README 把单容器标「推荐」、compose 标「备选」——误标。容器内真实隔离是重设计，无已证 exploit，驳回 |
| 8 | 迁移保护不足 | 采纳（收窄） | 005 的 DELETE 静默 + 整批豁免会让其他迁移的删除漏网（3→1、`err=nil`，测试证实）。005 文件冻结不动 |
| 9 | Agent 作用域 fail-open | 采纳（收窄） | 作用域查询的数据库错误静默退化为基线检查（DROP TABLE 场景测试证实） |
| 10 | SSH sudo 区分 | 驳回 | 认证/sudo/执行/超时早已区分且有测试；无用户输入到达 shell |
| 11 | 任务编辑/删除一致性 | 采纳（收窄） | 删除任务级联删掉全部执行历史（测试证实）。快照/禁用/重叠早已处理 |
| 12 | 多设备睡眠超时 | 驳回 | 10s 单次 / 60s 总计 / 重试上限已存在、ctx 可感知、逐设备记结果；未触发丢失或无限阻塞 |
| 13 | Emoji 改 SVG 图标 | 驳回 | 约 10 处装饰性 emoji，无功能缺陷；纯样式重构，无现成 SVG 集；用户设计优先规则适用 |
| 14 | 移动端交互 | 驳回 | 未找到具体缺陷；移动端外壳（底部导航、抽屉、卡片、安全区、44px 触点）已实现 |
| 15 | 页面状态 | 采纳（收窄） | 各页状态 + 401 + 离线均正确；仅全局健康轮询在失败时冻结在上次正常的「服务正常」 |
| 16 | 设备列表/网络信息 | 驳回 | Agent + 网络（含 CIDR）在桌面与移动端均已展示，附离线/不匹配警告；未见误操作风险 |
| 17 | 镜像构建可靠性 | 采纳（收窄） | `workflow_dispatch` 可从未打标签的提交发布 `latest`；无 version==tag 校验（版本三处硬编码） |
| 18 | 安装器/脚本 | 采纳（收窄） | 模式 2 会写出空 `WAKEHUB_AGENT_SUBNET`，导致它从不起的服务配置加载失败。其余早已处理 |
| 19 | README 与文档统一 | 采纳 | README:354 仍写「定时任务只开放 wake」，与 v0.3.2 sleep 矛盾；`docs/upgrade.md` 只覆盖 v0.1.0→v0.2.0；三模式文档过薄 |

**协调人设计决定**（见裁决文档）：问题 3，`default` 网络任何
Agent 可用、无网络的 Agent 只能用 `default`，扫描导入执行同一规则；
问题 4，单次任务必须自动禁用——前端选 kind=once 自动勾选并禁用
`run_once` 复选框，后端强制 `RunOnce=true` 并拒绝过去日期；
问题 8，005 豁免只限 005 自身实测增量（记录删除行数），其余迁移
照常检查；问题 11，删除任务保留执行历史（新迁移改外键为
`ON DELETE SET NULL`）；问题 17，工作流只在 tag 事件触发，加
version==tag 校验步骤；问题 2，Compose 用 profiles 区分模式。

## 1. 编辑任务不再覆盖 cron（问题 1）

**改动文件**：`web/src/utils/cron.ts`（新）、`web/src/utils/cron.test.ts`（新）、`web/src/components/TaskForm.vue`、`web/src/views/TasksView.vue`、`web/src/views/DevicesView.vue`。

- **新增 `parseCron`**：把已存 cron 表达式反向解析为表单状态
  （daily / weekly / once / custom；周范围如 `1-5` 展开；单次日期取
  今年日期、若已过去则取明年——`compileCron` 忽略年份，cron 本身
  不变）。`TaskForm.vue` 在编辑时用 `parseCron(props.editing.cron_expr)`
  初始化 `draft`，不再用硬编码默认值覆盖。
- **取消泄漏修复**：`TasksView.vue` 与 `DevicesView.vue` 的关闭处理
  在取消时清空 `editingTask` / `editingDevice`，取消编辑不再把编辑
  目标带进下次新建表单。
- 注：`30 2 * * 0` 归类为 weekly（周日）但往返结果与原 cron 完全一
  致，满足「编辑后不变」的要求。取消后新建的验证仅代码检查（仓库
  无组件挂载测试基建）。

## 2. Compose 三模式隔离（问题 2）

**改动文件**：`docker-compose.yml`、`install.sh`。

- **profiles**：`wakehub-server` 归属 profile `server`，
  `wakehub-agent-local` 归属 profile `agent`；`install.sh` 按模式
  传 `--profile`，`up` / `stop` / `logs` 均带对应 profile，`stop`
  恒用双 profile。
- **agent 不再拉起 server**：删除 agent 服务的 `depends_on`
  （原 `service_healthy` 会在纯 agent 模式自动启动 server）。
- **`:?` → `:-`**：七处强制插值全部改为带空默认的 `:-`，全新单模
  式安装不再被整文件插值阻断；fail-closed 由应用层配置校验
  （`internal/config/config.go`）继续兜底。
- YAML 解析通过，`bash -n install.sh` 通过，桩化模式 1/2 跟踪通过。

## 3. Agent–网络归属校验（问题 3）

**改动文件**：`internal/device/repository.go`、`internal/device/validate.go`、`internal/api/router.go`、`internal/api/router_test.go`、`internal/api/device_test.go`。

- **新增 `AgentManagesNetwork(agentID, networkID) (bool, error)`**：
  `default` 网络任何 Agent 可用；数据库错误 fail-closed（返回错
  误，不放行）。新增 `ErrAgentNetworkMismatch`。
- **写入强制**：`CreateDevice` / `UpdateDevice` 与 `ValidateDevice`
  均校验设备 `network_id` 属于其 `agent_id`；无网络的 Agent 只能用
  `default`。
- **扫描导入同一规则**：`resolveScanNetwork` 在扫描 Agent 不管理
  匹配网络时回落到 `default`；查找失败返回 HTTP 500，整批导入拒绝。
- 4 个既有测试先把网络指派给 Agent 再断言（测试数据随规则收紧）。

## 4. 单次任务自动禁用与过去日期拒绝（问题 4）

**改动文件**：`internal/scheduler/scheduler.go`、`internal/api/router.go`、`web/src/components/TaskForm.vue`、`web/src/utils/schedule.ts`、`web/src/utils/schedule.test.ts`。

- **前端**：单次日期输入 `:min` 为今天；提交时拒绝非未来的单次
  日期时间；kind=once 时自动勾选并禁用 `run_once` 复选框（提示
  「单次任务执行后自动禁用」），切回其他模式时经 `schedule.ts`
  的 `nextRunOnce()` 恢复上次非单次值。
- **后端**：`ValidateSchedule` 经 `parseOnceCronFields` 识别单次型
  cron，强制 `RunOnce=true`，并在任务时区的当年出现时间已过去时
  拒绝（错误信息写明过去的时间）。`handleCreateTask` /
  `handleUpdateTask` 把强制后的 `item.RunOnce` 或进传给
  `CreateSchedule` / `UpdateSchedule` 的 `runOnce`。
- 边界：2 月 29 日在非闰年经 `time.Date` 归一化为 3 月 1 日做过去
  判断；底层 cron 届时永不触发——robfig 既有行为，未处理。

## 5. 设备选择器全量加载（问题 5）

**改动文件**：`web/src/stores/devices.ts`、`web/src/stores/devices.test.ts`、`web/src/components/TaskForm.vue`。

- **新增 `loadAll()` / `loadForSelector()`**：分页遍历 `listDevices`
  写入独立的 `allItems` ref，不污染列表页的分页状态。
- 任务表单的目标下拉改用 `allItems` 渲染，带名称/MAC 搜索；已选
  设备置顶。第 51 台起的设备可选。

## 6. CIDR 规范化与唯一索引（问题 6）

**改动文件**：`internal/device/repository.go`、新迁移 `migrations/011_network_cidr_unique.sql`、`internal/db/migrate_test.go`。

- **新增 `normalizeCIDR`**：`CreateNetwork` / `UpdateNetwork` /
  `GetNetworkByCIDR` 统一规范化；`CreateNetwork` 的检查+插入在
  同一事务内，唯一索引作后盾。
- **迁移 011**：纯 SQL 整数位移规范化已存非空 CIDR（验证 /0、/8、
  /12、/24、/25、/32），等价行去重保留最早一行（设备与
  `agent_networks` 关联重指向），再建部分唯一索引
  `ON networks(cidr) WHERE cidr <> ''`。
- 升级实测：5 个网络 → 3 个（3 路 CIDR 冲突去重为最早一行；008
  种子行 `192.168.2.5/24` → `192.168.2.0/24`）。

## 7. 单容器隔离说明纠正（问题 7，仅文档）

**改动文件**：`README.md`、`docs/deployment.md`、`docs/security.md`。

- 单容器由「推荐」改为「便捷部署」，compose 在隔离重要时为推荐，
  并写明具体差别：单容器同 uid 10001 共享 `/app/data`，compose
  用分离的 `wakehub_data` / `wakehub_agent_data`。
- 容器内真实隔离是重设计、无已证 exploit，驳回，未做代码改动。

## 8. 迁移保护收窄（问题 8）

**改动文件**：`internal/db/migrate.go`。

- 005 的豁免只限 005 自身实测增量，并记录删除行数（日志
  `[Migrate] 迁移 005 去重删除 N 台重复设备`）；设备数守卫对其余
  批内迁移照常生效；全部 `TableCounts` 调用 fail-closed。
- 005 文件冻结不动。已注：冻结的 005 其 `DROP TABLE devices` 在
  外键开启时同样会级联删 `wake_tasks` 行——生产 DSN 下的潜伏机制，
  未修复，留待未来（见测试报告）。

## 9. Agent 作用域 fail-closed（问题 9）

**改动文件**：新文件 `internal/agent/scope.go`、`internal/scheduler/scheduler.go`、`internal/api/router.go`。

- **去重为单一实现**：`AgentScopeCIDRs(repo, store, agentID)` 供调度
  器与路由共用，旧的两份拷贝已删除。
- 任何查找失败返回错误，调用方 fail-closed：调度器记日志并以
  `sleepConfigError` 拒绝睡眠分发、设备记 skipped；路由返回
  HTTP 500。确实无网络的 Agent 仍走空作用域基线回退。

## 11. 删除任务保留执行历史（问题 11）

**改动文件**：新迁移 `migrations/012_task_runs_keep_history.sql`、`internal/db/migrate_test.go`。

- 迁移 012 重建 `task_runs`：`schedule_id` 可空 + 外键
  `ON DELETE SET NULL`，两个被删索引重建。
- 关键发现：外键开启时 `DROP TABLE task_runs` 会级联删
  `task_run_devices` 行，故 012 先把它们暂存到临时表再恢复。
- 删除任务不再删掉全部执行历史。快照/禁用/重叠早已处理，未动。

## 15. 全局健康状态不再冻结（问题 15）

**改动文件**：新文件 `web/src/utils/serverHealth.ts`、`web/src/utils/serverHealth.test.ts`、`web/src/App.vue`。

- **新增 `createServerHealth()`**：`App.vue` 改用它管理健康轮询。
  轮询失败时 `serverStatus='error'`、清空 `uptimeSeconds`、显示红
  点 + 「服务异常 · 上次正常 HH:mm」；恢复后重新轮询 Agents。
- 各页状态 + 401 + 离线早已正确，未动。

## 17. 镜像构建版本守卫（问题 17）

**改动文件**：`.github/workflows/docker-publish.yml`。

- 删除裸 `workflow_dispatch` 触发（只在 `v*` tag 推送时触发）。
- 新增「Check version matches tag」步骤：tag 与 `serverVersion`、
  `agentVersion`、`web/package.json` 三处比对，不一致即失败。
- 模拟验证：tag v0.3.2 → PASS；tag v0.3.3 在版本未升时 → FAIL
  （符合预期，升版后通过）。

## 18. 安装器模式 2 不写 Agent 变量（问题 18）

**改动文件**：`install.sh`。

- 模式 2 跳过全部 Agent 侧 `.env` 写入，不再写出空
  `WAKEHUB_AGENT_SUBNET`。
- 附带修同一路径的既有 bug：`detect_lan_subnet` 的 locals
  （`iface`、`cidr`、`ip`）在 `set -u` 下未初始化，检测无结果时以
  `cidr: unbound variable` 退出——现初始化为空字符串。

## 19. README 与文档统一（问题 19）

**改动文件**：`README.md`、`docs/upgrade.md`、`docs/deployment.md`、`docs/security.md`。

- 删除 README 已知限制里过期的 wake 分句（「定时任务只开放
  wake」；分组分句仍属实，保留）。
- `docs/upgrade.md` 开头新增「当前升级路径：v0.3.x」一节（备份、
  v0.3.2→v0.3.3 路径、迁移 001–012、健康版本检查、备份恢复回滚）；
  旧 v0.1.0→v0.2.0 内容保留在「历史」标题下。迁移表补 011、012
  两行，更新「v0.3.0–v0.3.2 均无新迁移；v0.3.3 新增 011、012」。
- 三模式文档扩充（compose profiles、单容器与 compose 的隔离差别）。

## 版本号

- `internal/api/router.go`：`serverVersion` 0.3.2 → **0.3.3**
- `internal/agent/client.go`：`agentVersion` 0.3.2 → **0.3.3**
- `web/package.json` / `web/package-lock.json`（根包版本）：0.3.2 → **0.3.3**
- README / docker-compose.yml（两处 image）/ Dockerfile /
  `docs/api.md` / `docs/deployment.md` / `docs/security.md` /
  `docker-entrypoint.sh` / `install.sh` /
  `scripts/migrate-agent-volume.sh`：版本标识与镜像标签 0.3.2 → **0.3.3**
- 历史引用不动：各处「v0.3.2 起开放 sleep」注释、契约文档、旧
  changelog 与测试报告、package-lock 内的依赖版本号。

## 升级

已有部署直接换镜像重跑同一条命令（数据卷原地保留）。启动时自动
备份当前数据库（`VACUUM INTO`，0600），按序应用迁移 011、012；
设备、任务、SSH 配置与执行历史原地保留。回滚：用启动前自动生成的
备份文件恢复数据库后换回 v0.3.2 镜像。
