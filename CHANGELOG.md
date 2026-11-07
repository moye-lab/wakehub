# Changelog

## v0.3.1 — 2026-11-07（修单容器 bind mount 权限 bug：root 启动 + 降权跑服务）

> 背景：v0.3.0 发布当天 VM 真机首跑即失败——root 账号下跑 README 的
> 单容器命令，容器无限重启。根因是宿主机 `./data` 属主 root，容器内
> 以 wakehub(10001) 跑，密钥文件写不进、数据库初始化失败。v0.3.1
> 改成入口以 root 启动（修属主、写密钥），再用 su-exec 降权到
> wakehub 跑 Server 与 Agent；README 那条命令在任何机器上都不用
> 再加 chown。另修 entrypoint 的误报 bug：写失败仍打印“已保存”。
> 逐项改动见 `docs/CHANGELOG-v0.3.1.md`；测试报告见
> `docs/TEST-REPORT-v0.3.1.md`。升级：已有 v0.3.0 单容器部署直接
> `docker rm -f wakehub` 后重跑同一条命令（数据卷原地保留）；已有
> compose 部署照旧 `docker compose up -d --build`（无新迁移）。

### 权限模型（单容器模式）

- 入口以 root 启动：先把 `/app/data` 属主改成 wakehub(10001)、权限
  0700（已是正确属主时跳过），再解析密钥并写文件（属主 wakehub、
  0600），最后用 su-exec 把 Server 与 Agent 降权到 wakehub 身份运行。
  长驻进程仍以非 root 运行，安全姿态与 v0.3.0 一致。
- 密钥写失败立即报错退出，绝不打印虚假的“已保存”（v0.3.0 的
  `set -e` 在命令替换里行为随 shell 而异，BusyBox ash 下写失败照常
  打印成功信息；v0.3.1 起 resolve_secret 改走全局变量 + 显式检查）。
- compose 模式兼容：server/agent 服务加 `user: "10001:10001"`，继续
  以 wakehub 身份跑（与 v0.3.0 行为一致，不经过入口的 root 阶段）。

## v0.3.0 — 2026-11-07（单容器：一条 docker run，不跑任何脚本）

> 目标：用户原话——“我真正需要的是减少步骤，一条命令不跑任何脚本
> 直接 docker run”。v0.3.0 起镜像默认是单容器：同一容器内同时跑
> Server 与本地 Agent，只填管理员密码即可部署。
> 逐项改动见 `docs/CHANGELOG-v0.3.0.md`；测试报告见
> `docs/TEST-REPORT-v0.3.0.md`。升级：已有 compose 部署照旧
> `docker compose up -d --build`（无新迁移）；新部署直接用单容器命令。

### 单容器模式（新部署方式，推荐）

- 镜像默认入口改为 `docker-entrypoint.sh`：同一容器内同时跑 Server
  与本地 Agent。部署就是一条命令（见 README“快速启动”），不跑任何脚本。
- 密钥自生成持久化：只填管理员密码即可；SECRET_KEY 与入网令牌不填时
  首次启动自动生成并存进数据卷，重启后复用；管理员密码不填时也自动
  生成 16 位随机值并打印在启动日志里。
- 本地 Agent 自动入网：回环 Agent 持有效入网令牌直接自动获批，无须
  手动审批；网段不配时自动检测本机首个非回环 IPv4 网段。
- 双进程监督：任一进程退出即整个容器退出，`--restart unless-stopped`
  重拉整个容器；信号转发给两个子进程。端口跟随
  `WAKEHUB_LISTEN_ADDR`（Agent WS 地址与健康检查都跟随）。
- compose 模式兼容：server 服务显式覆盖入口只跑 server；已有部署升级
  不受影响。

## v0.2.12 — 2026-11-06（SSH 关机装即用：sudo 提权 + 前台执行失败可见）

> 目标：飞牛这类“禁 root 登录 + 普通用户无权关机”的 NAS，旧版只能让
> 用户自己去目标机写 polkit 规则；v0.2.12 起设备表单勾选 sudo 即可，
> 目标机零配置。顺带修了旧版 nohup 后台执行的“发了 ≠ 成了”缺口。
> 逐项改动见 `docs/CHANGELOG-v0.2.12.md`；测试报告见
> `docs/TEST-REPORT-v0.2.12.md`。升级：解压后直接
> `docker compose up -d --build` 即可（010 迁移自动应用，老设备默认
> 不启用 sudo）。**盒子实测 sudo 时须先把飞牛的 polkit 规则临时移开**，
> 否则规则会掩盖 sudo 是否真生效。

### sudo 提权（新功能）

- 新设备字段 `ssh_use_sudo`（迁移 010，默认 false）：SSH 区、Linux
  目标时出现「使用 sudo 执行关机命令」复选框。勾选后 Agent 以单次会话
  `sudo -S sh -c '<关机脚本>'` 执行关机命令；密码经 stdin 一次喂给 sudo，
  不进命令行、不进日志；校验与断电在同一个 sudo 调用里完成，不依赖
  跨会话的时间戳。
- 归零三重保险：非 SSH 或非 Linux 时标记存为 false（仓库层 + 服务端
  离开 SSH 时清空 + 前端提交归一化）；Windows 目标忽略该开关。

### Linux 关机改前台执行，失败可见

- sudo 与非 sudo 都去掉 nohup/`&`，统一前台执行；断电链去掉
  `2>/dev/null`，错误经 stderr 捕获回显。退出码 0 = 断电已发起；
  非 0 = 失败（带退出码 + stderr 首行）；已运行 ≥1 秒后连接被断电掐断
  = 成功；过早掐断 = 失败。
- race 抓到的真 bug 已修：stdout/stderr 分开两个 Buffer（ssh 库双协
  程并发写同一 Buffer 会竞争）。

## v0.2.11 — 2026-11-06（修 §5 核对抓到的 Agent 卷目录 0755）

> 修 v0.2.10 部署后 §5 容器内权限核对抓到的真问题：Agent 数据卷根
> 目录是 0755 而不是 0700（根因：`docker volume create` 建出的卷根
> 是 root 属主，Agent 以 10001 身份改不了它，且收紧只在入网时跑
> 一次）。逐项改动见 `docs/CHANGELOG-v0.2.11.md`；测试报告见
> `docs/TEST-REPORT-v0.2.11.md`。升级：解压后直接
> `docker compose up -d --build` 即可。**已部署的卷须先跑一次**
> 一次性修复命令（见 `docs/CHANGELOG-v0.2.11.md`，2026-11-06 晚已
> 在用户 VM 上跑通），代码修不了已存在的 root 属主目录。

### Agent 数据卷根目录 0755

- 迁移脚本新增 `fix_new_volume_perms`：建卷后立即把卷根改成
  `10001:10001` + `0700` 并校验，主流程与全新安装分支都调它。
- Agent 每次启动都收紧 token 所在目录，失败只警告不拦截启动；
  入网收紧失败也改打警告（之前静默吞掉）。

## v0.2.10 — 2026-11-06（v0.2.9 上线当晚的打磨版）

> 修 v0.2.9 部署后截图里看到的网络名显示重复（008 种子名 `'网络 ' ||
> subnet` 与前端标签/网段行三重重复），并顺手修掉 `TestAgentManagementAPI`
> 的已知抖动。逐项改动见 `docs/CHANGELOG-v0.2.10.md`；测试报告见
> `docs/TEST-REPORT-v0.2.10.md`。升级：解压后直接
> `docker compose up -d --build` 即可（卷已在 v0.2.9 分离，无需再跑
> 迁移脚本；009 迁移随 Server 启动自动应用）。

### 网络名显示重复

新迁移 **009**（不改已发布的 008）：只改 008 自动种子的行
（`name = '网络 ' || cidr`），改名后名字即 CIDR 本身；用户手工改过名
的网络与默认网络一律不动；幂等。前端新增共用显示 helper
`formatNetworkLabel(n)`，名字与网段相同时只显示名字，6 处显示点
（设备页下拉/桌面表/手机卡片、Agent 卡片 chips、设备表单下拉与
Agent 网络提示）统一走它。

### 测试抖动修复

`TestAgentManagementAPI` 吊销后立即 GET 断言状态回 `approved`，
吊销后的会话清理是异步的，偶发看到 `connected`。改为复用已有的轮询
helper `waitForAgentStatus(..., "approved")`。连跑 5 次 + 全量 api 包
均通过。

## v0.2.9 — 2026-11-06（任务书 20 项，问题 13 除外）

> 任务书 20 项中 19 项已做（问题 13 导航 Emoji 统一 SVG 按用户
> 设计图规矩暂缓）。升级前必读 `docs/MIGRATION-v0.2.9.md`：
> **先跑 `scripts/migrate-agent-volume.sh` 再
> `docker compose up -d --build`**，顺序不可反。逐项改动见
> `docs/CHANGELOG-v0.2.9.md`；安全评审见
> `docs/SECURITY-REVIEW-v0.2.9.md`；测试报告见
> `docs/TEST-REPORT-v0.2.9.md`。

### 问题 1：Server 与 Local Agent 数据卷分离

Agent 服务改挂独立卷 `wakehub_agent_data:/app/data`，
Server 仍为 `wakehub_data:/app/data`；顶层 volumes 增加
`wakehub_agent_data`。Agent 的 token 文件路径不变
（`/app/data/agent-token`），只是现在落在独立卷里，
Agent 容器内再也看不到 Server 的 `wakehub.db`。

新增 `scripts/migrate-agent-volume.sh`：停 Agent → 备份 Server 卷 →
建新卷 → 复制旧 token（内容与 0600 权限原样，属主改回容器内
wakehub 用户 10001）→ 校验（token 存在、权限 0600、属主 10001、
内容一致、新卷内无 `wakehub.db`，输出 `VERIFIED` 才算通过）。
旧卷原样保留可回滚；脚本从不删卷，禁止 `docker compose down -v`。

### 问题 2：Network 实体，MAC 唯一范围改为网络内

- 新增迁移 008（`migrations/008_network_identity.sql`，不改写已发布的 005）：
  新建 `networks` 与 `agent_networks`（Agent ↔ Network 多对多）两张表；
  devices 加 `network_id` 列；删全局唯一索引
  `idx_devices_mac_unique`，改建网络内唯一索引
  `idx_devices_network_mac_unique (network_id, mac)`。
  全程无 `DELETE`，历史设备全部保留：每个已有 Agent 的 `subnet`
  非空时按 CIDR 生成网络行（id 由 CIDR 派生，如 `net-192-168-1-0-24`），
  其设备回填到对应网络；无网段 Agent 的设备进 `default`。
- 后端：`internal/device` 新增 Network 结构与网络 CRUD、
  Agent 网络分配（`SetAgentNetworks` / `ListAgentNetworks`）、
  网络内 MAC 冲突检查（创建、更新、扫描导入、批量导入、设备迁网
  全部按网络查重）；新增 API `GET/POST /api/v1/networks`、
  `GET/PUT/DELETE /api/v1/networks/{id}`、
  `GET/PUT /api/v1/agents/{id}/networks`（错误映射：CIDR 冲突 409、
  名称空/CIDR 非法 400、删默认网络 400、被引用 409、不存在 404）。
- 新建设备不填网络时自动跟随其 Agent 的首个管理网络
  （Agent 无网络时回落默认网络）；显式指定仍优先。
  设备表单网络下拉默认「自动（跟随 Agent）」。
- 前端：设备列表加「网络」筛选下拉与「网络」列（桌面表格与手机卡片）；
  设备表单加「所属网络」选择；Agents 页每张卡片展示该 Agent 管理的网络。

### 问题 10：JSON 请求体只解码一次

新增统一辅助 `decodeOneJSON`：第一次 Decode 成功后再 Decode 一次，
非 `io.EOF`（第二个 JSON 值或垃圾）一律 400。登录、改密、创建分组
等直解码点与 `decodeJSONBody` / `decodeAgentBody` 全部改调它；
唤醒端点保留「空体 = verify=false」旧语义（allowEmpty=true）。
`handleCheckDevice` 的宽容解析（`{"auto": true}` 可选）有意保持原样。

### 问题 12：迁移保护补强

- Server 启动与改密重置两条路径：应用迁移前先调 `BackupBeforeMigrate`
  做 `VACUUM INTO` 备份（`<库文件>.migrate-bak-<时间戳>`，0600），
  备份失败拒绝启动；迁移后打印各表行数前后对比日志。
- 设备数量保护绊线：整轮迁移结束时若 devices 减少且本轮未应用 005，
  返回错误并拒绝启动（删除发生在坏迁移已提交的事务内，绊线只能检测
  与拒启，恢复靠备份文件，见 `docs/MIGRATION-v0.2.9.md` §2.5）。
- 修复：`AppliedVersions` 在版本表尚不存在时返回空列表而非报错
  （旧库首次走迁移运行器时，备份步骤不再因此拒绝启动）。

### 第二期（问题 3 / 4 / 5 / 6）

- 问题 3：`WAKEHUB_AGENT_ALLOW_INSECURE_WS`（默认 0），`ws://` 仅
  回环放行，非回环须显式放行；Server 本身不终止 TLS，`wss://`
  须走反代/隧道。
- 问题 4：Cookie Secure 按可信代理的 `X-Forwarded-Proto` 自动附加
  （`RequestIsSecure`），未配信任列表时不采信反代头。
- 问题 5：Hub Dispatch 取消只发给实际承载任务的 Session；新增统一
  任务状态（pending/dispatched/completed/failed/cancelled/timeout）。
- 问题 6：任务超时分开配置，服务端默认 15s、Agent 默认 10s。

### 第三期（问题 7 / 8 / 9 / 11 / 14 / 15 / 16 / 17 / 18 / 19 / 20）

- 问题 7：睡眠下发时目标须落在执行 Agent 的管理网段内（最小一致版，
  不另设设备级白名单字段）；问题 8：Agent 启动时校验网段是否在本机
  网卡上，不符拒绝启动；问题 9：登录限流失败记录抽为 `failureStore`
  接口（内存实现，重启清空已写明）；问题 11：数据目录/备份目录/
  token 目录 0700，库文件与 -wal/-shm 0600。
- 问题 14：前端字号下限（正文 ≥12px、重要信息 ≥14px、关键数值
  20px）；问题 15：设备表单显示 Agent 管理网络与上报网段，选了
  Agent 不管理的网络时警告；问题 16：统一加载/空/错误状态
  （`ErrorState` 组件，失败不再误显示“暂无设备”，仪表盘失败时
  不显示全 0 假数据）。
- 问题 17：构建源改 `ARG NPM_REGISTRY` / `ARG GOPROXY`；问题 18：
  新增 `WAKEHUB_BIND_ADDRESS`，**默认保持 0.0.0.0**（任务书的
  127.0.0.1 会断 Cloudflare 隧道 隧道）；问题 19：install.sh 多网卡
  编号选负责网段；问题 20：版本号总同步（API 总数 43 → 50）、
  README 与安全/部署/API 文档一致性、四份交付文档齐全。

### 已知未做

- 问题 13（导航 Emoji 统一 SVG）按用户设计图规矩暂缓，不进本版。
- Docker 容器内验收（卷隔离的容器内验证、备份往返）未在沙箱执行，
  需在 VM 上按 `docs/MIGRATION-v0.2.9.md` §1.3 / §2.4 验证。
  第一期 VM 验证仍待用户侧（见 `docs/TEST-REPORT-v0.2.9.md` §7）。

## v0.2.8 — 2026-11-06

根因：仪表盘每次切页，快捷唤醒三张卡片都会短暂显示“发送中…”，
其实什么都没发——那是 V-N2 的页面刷新即最新在逐台自动检测状态。
旧实现里 `check()` 与 `wake()`/`sleep()` 共用同一个 `busyIds`，
按钮文案只认 `busyIds`，检测也被说成发送。

修法（前端）：store 里把检测拆到独立的 `checkingIds`；仪表盘快捷
唤醒按钮按优先级显示——真在唤醒时“发送中…”，只是在刷新状态时
“刷新中…”，平时“⚡ 唤醒”；刷新期间唤醒按钮仍可点（检测是只读
的，不冲突）。设备页“检测”按钮的禁用条件同步加上 `checkingIds`，
避免改完后它在检测进行中不再变灰、可重复点。

## v0.2.7 — 2026-11-06

小修：设备表单关机适配器“SSH 关机（Linux）”改名为
“SSH 关机（Linux / Windows）”。适配器本身只有一个 SSH，目标系统在
下面的“目标系统”里选；单独加一项会变成两个入口管同一个东西，改名
最干净。后端、API、数据库、契约无变化。

## v0.2.6 — 2026-11-06

SSH 关机适配器支持 Windows 目标：Windows 自带 OpenSSH Server，打开后
不用再装 Sleep-on-LAN exe、不用开机任务、没有黑窗口（详见
`docs/ssh-sleep.md` 与契约 §6.3）。

### 新增

- 设备字段 `ssh_os`（`linux` 默认 | `windows`）：SSH 适配器按目标系统
  下发命令。Windows（OpenSSH 默认 shell 为 cmd.exe）：`shutdown` →
  `shutdown /s /t 1`；`sleep` → `start "" /b rundll32.exe powrprof.dll,SetSuspendState 0,1,0`；
  `hibernate` → `start "" /b shutdown /h`（前台先 `where` 检查工具存在）。
- 设备表单 SSH 一节加“目标系统”选择（Linux / Windows），提示文案随
  目标系统切换。
- 迁移 007：devices 表加 `ssh_os` 列（默认 `linux`）；旧库升级自动应用，
  无破坏性变更。

### 说明

- Windows 目标须开启 OpenSSH Server（管理员 PowerShell 一次操作，
  安装时自动放行防火墙、服务开机自启）；SSH 用 Windows 登录账户的
  密码（不是 PIN）。
- 真实 Windows SSH 关机尚未实测：命令字符串对进程内真实 SSH 协议测试
  服务器验证通过；首次真机验证前请先确认电脑上 22 端口已开。

## v0.2.5 — 2026-11-06

新增 SSH 关机适配器（Linux 目标零安装远程关机；兑现契约 §6.1 里
“Linux 本地关机适配器 v0.2.0 不新增”的延期项，详见 `docs/ssh-sleep.md`
与契约 §6.3）。

### 新增

- 设备 `sleep_type` 增加 `ssh`：Agent 用 SSH 直连目标执行关机命令，
  目标上不用装任何监听服务。适用 Linux（NAS、迷你主机、虚拟机、路由器等），
  Windows 目标不在本适配器范围内。
- 设备新增字段：`ssh_user`、`ssh_port`（默认 22）、`ssh_password`
  （写时字段，编辑留空表示保持原密码）、`ssh_password_set`（只读）、
  `ssh_host_key_sha256`（TOFU 指纹，只读）。
- 迁移 006：devices 表加 `ssh_user` / `ssh_port` / `ssh_password_enc` /
  `ssh_host_key_sha256` 四列；旧库升级自动应用，无破坏性变更。

### 安全

- SSH 密码 AES-256-GCM 加密存储（密钥由 `WAKEHUB_SECRET_KEY` 派生，
  `internal/secret`），明文永不落库、不进响应、不进日志；`SECRET_KEY`
  更换后旧密文无法解密，接口提示重新填写保存。
- 主机密钥 TOFU：首次连接记录指纹，之后每次校验，不符即拒绝；
  改密码即清空指纹重新信任；离开 SSH 适配器即清空已存凭据与指纹。
- SSH 目标仅限私有 IPv4 / 回环地址，禁主机名、公网、169.254 元数据段；
  动作仍为 `shutdown | sleep | hibernate` 白名单；关机命令先在前台
  检查工具存在，再以后台方式延迟 1 秒执行（避免会话在断电瞬间被掐断
  导致成功与失败无法区分）。

### 不变

- 定时任务仍只允许 `wake`；http_sol 路径行为不变；设备页“睡眠”按钮
  仍实际发 `shutdown`（既有行为）。
- 真实NAS SSH 关机尚未实测（进程内真实 SSH 协议测试服务器已通过），
  待用户在 VM 上验证。

全面代码审查 + 安全加固 + 前端重构（任务书 34 项；审计核实 21 属实 /
14 部分属实 / 0 整项不实，另新发现 28 项）。破坏性变更见
`docs/contract-v0.2.0.md` §0.1，升级步骤见 `docs/upgrade.md`。

### 破坏性变更

- 登录改 HttpOnly Cookie 会话（`wakehub_session`，SameSite=Lax），不再
  返回 token、不再接受 `Authorization: Bearer`；旧 token 全部作废，升级
  后须重新登录一次。
- 全局 Agent token（`WAKEHUB_AGENT_TOKEN` 服务端）废止，改为每 Agent 独立
  token（`wha_`，只存 SHA-256 哈希）+ 入网 token + 管理员审批；全部 Agent
  升级后须重新入网（路径 A 预签发或路径 B 自助注册待审批）。
- 内置 `local` 节点退役：种子行由迁移 003 删除，全部任务经注册 Agent
  执行；单机单网段场景也须跑 local agent 容器（标准安装已包含）。
- 设备 `agent_id` 必填，无默认值；`local` 不再是合法值。
- 设备列表、分组列表、任务列表改为分页信封
  `{items, total, page, page_size}`；批量唤醒改为完整响应结构
  （`batch_id` + per-device 结果）；扫描改为异步任务
  （202 + `scan_id` 轮询）。
- 占位密钥拒绝启动：`WAKEHUB_ADMIN_PASSWORD` / `WAKEHUB_SECRET_KEY` /
  `WAKEHUB_AGENT_ENROLL_TOKEN` 任一仍为占位值即 fatal（老库不豁免）。
- 更新/删除不存在的对象返回 404（此前返回成功）。
- 定时任务 `action` 第一阶段只开放 `wake`，其余 400。

### 安全（M1–M2）

- 会话 token 改 JSON payload + `jti` + `iat`/`exp`，恒定时间比较签名，
  撤销表可吊销；改密后旧会话全部失效；新增退出、改密、me 接口与
  `wakehub-server reset-password` CLI（不删库重置）。
- 登录限流改 IP + 账号双维度（8 次/5 分钟），限流键取 host 部分（修复
  随机源端口致限流永不触发）；map 上限 + 定期清理；默认不信任
  `X-Forwarded-For`（须显式配 `WAKEHUB_TRUSTED_PROXIES`）。
- WS `CheckOrigin` 改 allowlist（`WAKEHUB_WS_ALLOWED_ORIGINS`）；状态
  变更请求加 Origin/Referer CSRF 校验；Body 上限 1 MiB。
- 重连竞态修复：新连接替换旧连接时锁外关闭旧连接，旧连接 defer 比对
  会话引用才删；Dispatch 写失败重取会话引用重试一次。
- Agent 身份：token 与 `agent_id` 绑定，冒认 `local` 被拒；未获批 Agent
  不得执行任务；WSS 配置、指数退避 + 抖动、Ping/Pong + 心跳超时清理、
  任务并发上限、每连接独立子 ctx、任务超时与取消传播。
- 用户名正则校验；用户不存在时走 dummy bcrypt，错误文案统一。

### 设备与操作（M3）

- 统一验证层：名称/MAC/IP/广播/端口/枚举/Agent 状态/MAC 唯一；
  创建与更新共用；更新返回重查行；更新/删除不存在返回 404。
- 批量唤醒：per-device 结果、成功/失败/跳过统计、`batch_id`、起止时间
  与耗时、并发上限 10、整体 120s、客户端断开取消；`sent` 与 `booted`
  严格区分（启动成功须回检，不虚报）。
- Sleep SSRF 防护：端点 allowlist（私有 IPv4 + 回环 127/8，禁公网/
  主机名/userinfo/169.254.0.0/16）、禁跟随重定向、超时 5s、HTTPS 优先；
  畸形 JSON 不再默认执行睡眠；检测解析失败不写脏状态。
- 状态检测：Ping 参数校验防注入、locale 无关的权限不足判定、TCP 端口
  关闭不再判关机、ARP 缓存未命中标 `unknown` 并注明边界。
- WOL 端口越界报错（不再静默钳制为 9）；广播地址校验；重发间隔响应
  ctx 取消。

### 扫描与调度（M4）

- 扫描异步化：`POST /scan` 返回 202 + `scan_id`，进度查询、取消；
  每 Agent 仅 1 个进行中扫描；按 Agent 白名单校验网段；跳过 `.0`/`.255`；
  去重与已存在识别；导入接口逐条 best-effort、按 MAC 去重、自动关联
  执行 Agent；OUI 表由 13 条扩至 27 条。
- 真主动 ARP 评估为降级：沙箱内 root 也打不开 raw socket，保留
  TCP-touch + 读 ARP 缓存并写明能力边界，不虚报。
- 调度器：Action 真正参与执行（仅 `wake`）；创建时校验目标存在性；
  空目标/全禁用记 `skipped`；暂停/恢复；单次任务执行后自动禁用；
  防重叠执行；每次执行独立 Run ID 与每设备结果；重试上限（500ms 间隔
  响应 ctx）；60s 执行超时；WOL 重发次数改用 `WAKEHUB_WOL_RETRIES`。
- `failed` 语义：只计探测任务本身失败，单台主机无响应是正常阴性结果。

### 数据库与审计（M5）

- 迁移运行器正式化：启动时按序应用 001–005（`schema_migrations` 记
  版本，幂等重跑，失败即拒绝启动）；旧库原地升级，不删库。
- 索引与外键（005）：`devices.mac` 唯一索引等；DSN 加 `busy_timeout`；
  新建 DB 文件权限 0600；Dashboard 改 SQL 聚合。
- 审计补齐（`device_check`、`schedule_run`、`agent_update` 等）与筛选 +
  分页；审计写入失败不再静默丢弃；保留 90 天定期清理。
- 通用 API 限流：每会话 600 次/分钟，超限 429 + `Retry-After`。
- 新增 `scripts/backup.sh`（备份/恢复，实测往返）。

### 前端（M6）

- Vue Router（7 页：Dashboard、Devices、Scan、Schedules、Agents、Logs、
  Settings）+ Pinia 按域拆 store；瘦 App 外壳；移动端底部导航与 Safe Area。
- Cookie 会话：删掉 `localStorage` 存 token 的全部代码；登录页去掉默认
  密码明文提示；401 后跳登录。
- 扫描向导按异步流程（202 + 轮询 + 取消 + 导入逐条结果）；Schedules 页
  接 pause/resume 与 runs 历史；Agents 页接审批/吊销/轮换/白名单；
  Logs 页接筛选 + 分页；亮/暗主题；危险操作统一确认弹窗；加载/空数据/
  错误三种状态；表单即时校验；时间显示本地时区。
- 前端测试 22 个（utils + API client）；`vue-tsc` 类型检查干净。

### 部署与文档（M7）

- Dockerfile：`npm ci`（锁文件可复现）、Go 构建缓存友好、多架构
  （buildx `--platform linux/amd64,linux/arm64`）、非 root `USER`、
  `HEALTHCHECK` 调 `/api/v1/health`、`.dockerignore`。
- 新增 `GET /api/v1/health`（公开，`{status, version, db, uptime_seconds}`）；
  `GET /api/v1/groups` 改为分页信封（契约 §11）。
- docker-compose：healthcheck + `depends_on: condition: service_healthy`；
  密钥类变量无默认值（未设置直接报错，不以占位值启动）；agent 加
  `cap_add: [NET_RAW]`（ping 在 Agent 侧执行）；server/agent 均
  `cap_drop: [ALL]` + 只读根文件系统；server 另有 `no-new-privileges`
  （agent 的已在 VM 验证后删除，见下）；Agent token 文件挂数据卷持久化。
- install.sh：三处占位值补齐（缺失或占位即随机生成）；模式 3 不问管理
  员密码；WS URL 默认值不再误导为 127.0.0.1；网段加 CIDR 校验；就绪门
  改调健康接口 + 登录自检；模式 1 自动审批本机 local agent；结束页
  不再明文打印密码；sudo 运行时 `.env` 属主改回原用户；模式切换先停
  旧服务；无 ss 又无 python3 时端口检查告警而非静默跳过。
- `WAKEHUB_AGENT_TOKEN` 服务端配置已删除（死代码）：该变量名现仅为
  Agent 侧 token（首次注册填入网 token，获批后改走 token 文件）。
- README 修正 4 处失实宣称：不宣称 HTTPS（纯 HTTP，公网走反代/Tunnel
  做 TLS）、限流按真实实现写、OUI 27 条写清覆盖范围、审计覆盖范围如实写。
- 新增 `docs/api.md`、`docs/security.md`、`docs/upgrade.md`、
  `docs/deployment.md`。

### VM 验证后追加（V-N1、V-N2、V-N3、V-N4、V-N5、V-N6、V-N7，2026-11-06）

- **V-N1：Agent 容器内 ping 权限（两层修复）。** 仅 `cap_add: [NET_RAW]`
  不够：容器以非 root 运行，cap_add 只进 bounding set，非 root 进程
  exec 普通二进制拿不到能力；补 `setcap cap_net_raw+ep /bin/ping`
  （Dockerfile 加 `libcap`）后仍不够——agent 服务的
  `no-new-privileges:true` 会让内核在 exec 时忽略文件能力，遂从 agent
  服务上删除该选项（server 不发 ICMP，保留）。另：`docker exec` 不套用
  `no-new-privileges`，拿它测 ping 会误判为"已修好"。
- **V-N2：自动状态检测（契约 §14）。** 服务端周期检测：默认每 5 分钟
  检测全部已启用设备（`WAKEHUB_STATUS_CHECK_INTERVAL` 秒，`0` 禁用；
  启动即跑一轮；Agent 离线跳过、不写脏状态；并发上限 5；不写审计）。
  页面刷新即最新：设备页、仪表盘加载后自动检测当前页已启用设备
  （并发上限 5，逐行更新；单台失败不阻断其余）。自动检测经
  `POST /api/v1/devices/{id}/check` 的可选 body `{"auto": true}` 标识，
  后端仅跳过审计写入，检测逻辑与手动完全一致。
- **V-N3：仪表盘手机布局。** 6 张统计大卡改成一排小数字条；快捷唤醒
  移到「即将执行的任务」上面，改成一排横向滑动的小卡片（状态圆点 +
  设备名 + IP + ⚡唤醒按钮，最多 6 台）。统计条后据用户反馈再改：
  手机上 3 列 × 2 行、桌面上一行 6 项，文字完整不截断，细线分隔；
  用户看 4 套设计图后选定方案三（彩色胶囊），已按方案三实现。
- **V-N4：仪表盘顶部。** 大标题 + 刷新按钮 + 4 颗状态徽章太占地方，
  改为一行小字状态条（服务正常 · 版本 · 已运行时间；服务/数据库
  异常时整行变红；待审批 Agent 数并进同一行）+ 右端圆形刷新图标
  按钮；大标题与徽章全删。
- **V-N5：仪表盘顶部再收紧。** 设计与文案保持原样，只收间距：
  区块间距 24px → 16px、刷新按钮 36px → 32px、胶囊内边距与间距
  各减 2px，顶部整体约省 26px 高度。
- **V-N6：运行时间进顶栏、版本号放底部。** 状态行只剩健康状态；
  "已运行 X"收到顶栏（登录后拉一次、每 60 秒轮询一次，所有页面
  共用）；版本号放仪表盘页面最底部小字；移动端顶栏的页面名让位
  给运行时间。
- **V-N7：状态也进顶栏，状态行整行删掉。** 顶栏右边改为"● 服务
  正常 · 已运行 X"（异常时整段变红）；手动刷新按钮随状态行一并
  去掉（打开页面自动检测 + 每 5 分钟周期检测已覆盖）。

### 未实现 / 顺延（诚实说明）

- 多用户 / RBAC、个人 API Token、服务端直出 TLS、Sleep 签名认证、
  Linux/Windows 本地关机适配器、真 ARP 主动扫描、批量唤醒历史查询、
  分组更新/删除：均不在 v0.2.0，见契约 §13。
- Docker 镜像未在本机沙箱实测构建/运行（runc 被禁），须在 VM/真机验证，
  见 `docs/deployment.md` 的验证清单。
- PWA 可安装性与移动端渲染未在真机目视验证。

## v0.1.0 — 2026-11-05

- 首版：Go Server + 多 Agent + Vue 3 前端；WOL、Ping/TCP/ARP 检测、
  局域网扫描、定时唤醒、审计日志；一键安装；私有仓发布。

## [0.2.1] - UI refresh

### Added
- Professional blue/white SaaS visual system with light, dark, and system theme modes.
- Responsive authenticated shell with desktop sidebar and mobile bottom navigation.
- Mobile device cards and a denser dashboard hierarchy.

### Changed
- Updated dashboard and settings appearance while retaining existing live data sources and business actions.

### Compatibility
- No backend API or database migration changes are included in this UI update.

## [0.2.4] - 移动端顶栏标题层级

### Fixed
- 移动端顶栏页标题（“设备管理”等）不再与 WakeHub 同字同粗：改为副标题样式（13px / 500 / 次要色），与品牌主标题拉开层级。根因：v0.2.1-ui 合并时丢了 v0.2.0 的移动端标题隐藏规则，标题一直以 16px 粗体原样显示。
- 移动端顶栏去掉运行时间（只留“服务正常”）；PC 端顶栏仍保留运行时间（“运行 X”独立标签）。

### Compatibility
- No backend API or database migration changes are included in this UI update.

## [0.2.3] - 移动端顶栏填空

### Fixed
- 移动端顶栏中间大面积留白：手机上恢复显示 "WakeHub" 字标（图标旁），状态旁追加运行时间（"服务正常 · 21 分钟"）；桌面端顶栏不变。

### Compatibility
- No backend API or database migration changes are included in this UI update.

## [0.2.2] - 方案 A 深色界面（Slate Pro）

### Added
- 深色 Slate Pro 视觉：深底色 + 蓝色主按钮 + 在线状态呼吸灯。
- 仪表盘统计改为紧凑胶囊（桌面一行 6 项、手机 3 列 × 2 行），解决顶部卡片占位过大问题。

### Changed
- 默认主题由“跟随系统”改为深色（浅色/跟随系统仍可在顶栏与设置页切换，偏好只存本地）。
- 仪表盘“欢迎回来”大标题与手动刷新按钮移除（恢复 V-N7：自动检测已覆盖手动刷新）。

### Fixed
- 版本号统一为 0.2.2（服务端与 Agent 此前仍报 0.2.0）。

### Compatibility
- No backend API or database migration changes are included in this UI update.
