# WakeHub v0.2.9 测试报告

基线：v0.2.8（Gitea 私有仓 sg/wakehub，单根提交 973c1a5）。
任务书 20 项的逐项改动见 `docs/CHANGELOG-v0.2.9.md`；
安全结论见 `docs/SECURITY-REVIEW-v0.2.9.md`。

测试环境：本机沙箱（root、Go 1.22.12、python3、sqlite3 CLI；
**docker 跑不起容器**，容器内验证只能在用户 VM 做，见“未覆盖”）。
本机网卡：host0 `198.19.0.2/30`，默认路由经 198.19.0.1。

## 1. 后端

| 命令 | 结果 |
| --- | --- |
| `go vet ./...` | 通过，无输出 |
| `gofmt -l internal` | 干净（无输出） |
| `go test ./...` | **14 包全过**（见下） |
| `go test -race ./...` | **14 包全过，无数据竞争**（api 220.7s、auth 150.9s，race 下慢属正常） |

`go test ./...` 各包（2026-11-06 18:48，问题 14/15/16 之后）：

```
ok internal/agent      3.9s
ok internal/api        16.1s
ok internal/audit      (cached)
ok internal/auth       (cached)
ok internal/config     (cached)
ok internal/db         (cached)
ok internal/device     (cached)
ok internal/scanner    (cached)
ok internal/scheduler  2.1s
ok internal/secret     (cached)
ok internal/sleep      0.019s
ok internal/status     (cached)
ok internal/statuspoller (cached)
ok internal/wol        (cached)
```

### 新增测试（v0.2.9 各期）

- 问题 10：`TestDecodeJSONBodyTrailingContent`（单 JSON 201、尾部
  第二 JSON 400、尾部垃圾 400、尾部空白仍 201）。
- 问题 2：`TestRepositoryNetworkScopedMAC`、
  `TestRepositoryNetworkCRUD`、`TestRepositoryNetworkAutoResolve`、
  `TestMigrate008PreservesDevices`、
  `TestNetworksCRUDAndDeviceNetworkScope`、`TestScanImportNetworkScoped`。
- 问题 12：`TestBackupBeforeMigrate`、`TestMigrateDeviceLossRefused`。
- 问题 3：`TestValidateServerWSURL`（17 例）、
  `TestAgentConfigValidate` 新增 5 例。
- 问题 4：`TestRequestIsSecure`（11 例）、
  `TestLoginCookieSecureBehindTrustedProxy`。
- 问题 5：`TestDispatchCancelGoesToRetriedSession`（已做反向验证：
  改回旧行为时该测试 FAIL，恢复后 PASS）、
  `TestDispatchSuccessRecordsCompleted`。
- 问题 6：`TestTaskTimeoutEnv`（默认/显式/非法回落 + 服务端默认值
  大于 Agent 默认值）、`TestClientTaskTimeout`（零值/显式/负数回落）。
- 问题 7：`TestValidateEndpointInScope`（8 例）、
  `TestValidateIPInScope`（6 例）、
  `TestSleepEndpointOutsideAgentScope`（http_sol 与 SSH 越界均 400、
  网段内端点放行到真实执行）。
- 问题 8：`TestValidateSubnetOnNetworks`（9 例）、
  `TestLocalNetworks`（只断言不报错且全为 IPv4 非回环）。
- 问题 9：`TestMemoryFailureStore`（计数含过期丢弃/Delete/Prune/
  Len/容量上限不变量）。
- 问题 11：`TestEnsurePrivateDir`、`TestTightenDBFiles`。

### 已知抖动（未修，v0.2.5 起即如此）

`TestAgentManagementAPI` 全量并行跑偶发失败（吊销后状态仍
connected，断言未轮询等待会话清理），单独跑 3/3 通过、全量重跑
可通过。本轮 `go test ./...` 与 `-race` 各跑一次均直接通过，
未复现。

## 2. 前端

| 命令 | 结果 |
| --- | --- |
| `npm run typecheck`（vue-tsc） | 通过 |
| `npm run test`（Vitest） | **32/32**（4 文件；含问题 16 新增的 devices store 错误状态 2 例） |
| `npm run build`（vite） | 通过 |

## 3. 升级冒烟（最终版二进制，真跑 server）

构造 v0.2.8 态库：最终版二进制 + 仅 001–007 迁移文件建库，
sqlite3 直插 1 个 Agent（agent-home-lan，192.168.1.0/24）与 3 台
设备（NAS 192.168.1.3、路由器 192.168.1.11、Windows 192.168.1.29）。
再用最终版二进制 + 完整迁移目录重启：

- 启动自动备份生成（`.migrate-bak-*`，0600）；
- 日志「迁移后数量核对：devices 3→3，agents 1→1，…，networks 0→2」；
- health 报 `0.2.9`；
- 3 台旧设备全部回填 `net-192-168-1-0-24`；网络表含 `default` 与
  `net-192-168-1-0-24`。

API 实测（Cookie 登录后）：

- `GET /api/v1/devices`：3 台，network_id 均为 net-192-168-1-0-24；
  `?network_id=` 过滤正确；
- 同网络重复 MAC → **409**（旧文案「MAC 地址已被设备 NAS 占用」）；
- 同 MAC 在另一网络新建 → **201**（网络内唯一生效）；
- 尾部第二 JSON → **400**。

注：冒烟中途手工插的 Agent 行缺 `approval_status` 列值，曾导致
设备创建报 400「Agent 尚未获批」——这是冒烟构造问题（真实入网
流程会写该列），补列值后 409/201 均正常；另，网络创建接口忽略
客户端传的 id、改用服务端生成的 UUID（既有行为）。

## 4. 权限冒烟（最终版二进制，真跑 server）

预建 0755 数据目录 → 启动后变 0700，库与迁移备份均为 0600；
backup.sh 实跑（宿主机文件形态）：`./backups` → 700、备份包 → 600。
-wal/-shm 在干净关闭后被 SQLite 回收删除，冒烟里没出现，其 chmod
由单元测试覆盖。**容器内权限核对只能在 VM 做**（见 §7）。

## 5. 脚本桩测试（假 docker / ip / curl）

### install.sh 多网卡（v0.2.9 问题 19，三场景，全流程 exit 0）

假 `ip`：eth0 192.168.1.65/24、eth1 192.168.5.10/24、docker0 与
veth（172.17.0.0/16，必须被过滤）。

- 选 2 → `.env` 为 `WAKEHUB_AGENT_SUBNET=192.168.5.0/24`；
- 选 0 → 手动填 192.168.1.0/24；
- 单网卡 → 不弹菜单、默认 192.168.1.0/24；
- 三场景菜单里均未出现 docker0 的 172.17.0.0/16；
- `.env` 同时含 `WAKEHUB_BIND_ADDRESS=0.0.0.0`、
  `NPM_REGISTRY`、`GOPROXY`（问题 17/18）。

### backup.sh 与 migrate-agent-volume.sh（第一期，已验证）

- migrate 全流程成功（VERIFIED、token 0600/10001、备份可
  integrity_check、行数 3）；
- **2026-11-06 晚 ENTRYPOINT 回归（严格桩）**：旧脚本 + 严格桩
  （无 `--entrypoint` 的 `docker run` 按真实行为打印 server 占位值
  拒绝并 exit 1）逐字复现 VM 失败；新脚本全流程 exit 0（备份
  tar.gz integrity_check 通过、3 台设备；token 复制 VERIFIED、
  0600、属主 10001、新卷无库）；restore 到卷往返通过（库 0600/
  10001）。第一期桩没模拟 entrypoint 行为是本次漏网的直接原因。
- **2026-11-06 晚容器用户回归（严格桩升级）**：桩按 `--user` 模拟
  容器用户（默认 10001，`--user 0` 时 root；夹具卷内文件 10001/
  0600）。阴性对照：上一版脚本复现 VM 的 Permission denied（源可
  读、目标 0700 临时目录被拦）；阳性对照：新脚本全流程 exit 0
  （备份 integrity 通过、3 台设备；token VERIFIED 0600/10001；
  restore 到卷往返通过，库 0600/10001）。
- **2026-11-06 晚宿主机属主回归（daemon 桩）**：桩改走 root daemon
  代跑载荷，脚本以 uid 1000 跑（夹具卷内文件 10001/0600）。阴性
  对照：上一版脚本复现 VM 的「校验失败」；阳性对照：新脚本全流程
  exit 0（备份 integrity 通过、3 台设备，包内库属主 1000；token
  VERIFIED 0600/10001；restore 到卷往返通过，库 0600/10001；第二
  轮 restore 预存文件属主 1000）。
- 注入 copy 失败 → trap 恢复 agent（须用 `docker compose start`，
  不能用 `up -d`）；
- restore 往返、宿主机形态回归、裸 .db restore 到卷；
- 桩测试抓到的真 bug：payload 里 `for` 循环末尾缺 `|| true`
  致 -shm 不存在时返回 1，已修两处。

### install.sh 其他（问题 3、问题 8，沿用此前桩测试）

- 问题 3：ws LAN 答 n → 重问改 wss ALLOW=0；答 y → ALLOW=1；
  ws 回环 → ALLOW=0；均 exit 0。
- 问题 8：真二进制三场景（错配 192.168.1.0/24 → 立即 fatal 且文案含
  本机网段 198.19.0.0/30；匹配 → 过校验后进入拨号重试；错配 +
  跳过开关 → 过校验）；install.sh 全回车 → `.env` 为检测值
  198.19.0.0/30（退出码 1 是桩环境无真 Server 的 90 秒健康轮询
  超时，属桩局限）。

## 6. 配置验证

- `bash -n install.sh` 通过；`bash -n` scripts/backup.sh 通过。
- `docker-compose.yml` 用 YAML 解析验证：两服务 build.args 齐全，
  server 端口映射为
  `${WAKEHUB_BIND_ADDRESS:-0.0.0.0}:${WAKEHUB_PORT:-8080}:8080`，
  镜像 tag 均为 `wakehub:0.2.9`。
- 路由总数：`grep mux.HandleFunc` 51 行，减 `/` 静态兜底 1 行，
  **API 50 个**（v0.2.0 的 43 + 问题 2 的 7 个网络端点），与
  README / api.md 一致。
- 版本号 grep：Dockerfile、compose、install.sh 头注释、router.go、
  client.go、package.json、SettingsView 文案均为 0.2.9，无残留。

## 7. 未覆盖（须在 VM / 真机验证，如实标注）

- **容器内验证**：本机沙箱跑不起容器。`docker compose config` /
  `build` / `up`、容器内权限核对（迁移文档 §5）、命名卷备份
  restore 往返、Agent 容器内 ping（V-N1 的能力位）均须在 VM 跑。
- **第一期 VM 验证仍待用户侧**：新包 phase1 的 migrate 脚本重跑
  （VERIFIED）+ `up` + 核对清单（health 0.2.9、3 台设备、网络列、
  Agent 在线、Agent 容器内看不到 wakehub.db）。
- **第二期快照包**：代码已齐，未打包；等第一期 VM 验证通过后补打。
- **真机行为**：多 Agent 跨网段、ARM64、PWA 真机安装、NAS SSH
  关机实测（NAS不当试验品，须挑能接受NAS下线的时机）。
- **问题 13**（导航 Emoji 统一 SVG）按用户设计图规矩暂缓，不进本版。
