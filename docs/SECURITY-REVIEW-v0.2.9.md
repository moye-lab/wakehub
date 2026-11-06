# WakeHub v0.2.9 安全评审

基线：v0.2.8（Gitea 私有仓 sg/wakehub，单根提交 973c1a5）。
任务书：2026-11-06 用户发来的《WakeHub v0.2.8 全面代码整改与安全加固任务书》
20 项，逐项核查代码后执行；三处纠正见 `docs/CHANGELOG-v0.2.9.md`
（Issue 18 默认绑定地址、Issue 12 已有机制、Issue 9 已有机制）。

本文只写 v0.2.9 改了什么、改到什么程度、还剩什么。未改的沿用
`docs/security.md`（已同步到 v0.2.9）。

## 逐项结论

### 问题 1（P1）：Server 与 Agent 数据卷隔离

- 改动：`docker-compose.yml` 里 agent 服务改挂独立卷
  `wakehub_agent_data`（仅 `agent-token` 文件）；server 仍用
  `wakehub_data`。迁移脚本 `scripts/migrate-agent-volume.sh` 只复制
  token 文件（cmp 校验一致、0600、属主 10001），旧卷原样保留可回滚，
  脚本从不删卷。
- 结论：Agent 容器内已无数据库文件可读；即使 Agent 进程被攻破，也
  拿不到设备库、审计库与 SSH 密文。升级路径见
  `docs/MIGRATION-v0.2.9.md` §1。

### 问题 2（P1）：MAC 唯一改网络内 + Network 实体

- 改动：迁移 008 建 `networks` / `agent_networks` 表；设备加
  `network_id`；全局唯一索引改为 `UNIQUE(network_id, mac)`；历史
  设备全保留（按 Agent 网段回填，无 DELETE）。
- 结论：多网段下同一 MAC 可在不同网络各建一台（真实组网需求），同
  网络内仍 409 冲突。扫描导入与设备创建/更新统一走网络内判重。

### 问题 3（P2）：远程 Agent 明文 WS

- 改动：新增 `WAKEHUB_AGENT_ALLOW_INSECURE_WS`（默认 0）。
  `wss://` 放行；`ws://` 仅回环放行；非回环 `ws://` 须显式置 1，
  否则 Agent 拒绝启动；与 `WAKEHUB_AGENT_TLS_SKIP_VERIFY` 分开。
- 诚实说明：**Server 本身不终止 TLS**（全仓无 ListenAndServeTLS），
  `wss://` 只能靠反代/隧道终止 TLS。install.sh 与 `.env.example`
  已如实写明，不要误以为 Server 自带 HTTPS。

### 问题 4（P2）：反代下 Cookie Secure

- 改动：新增统一判断 `RequestIsSecure`：`r.TLS != nil` 或（RemoteAddr
  在 `WAKEHUB_TRUSTED_PROXIES` 内且 `X-Forwarded-Proto: https`）。
  未配信任列表时反代头一律不采信（fail-closed）。局域网 HTTP 部署
  行为不变（不强制 Secure）。
- 结论：反代后的 Cookie 不再因“Server 只看到 HTTP”而漏 Secure；
  未受信的代理无法用伪造头骗取 Secure（也骗不到别的，因为 Secure
  只影响 Cookie 是否随 HTTP 发送）。

### 问题 5（P2）：WS 重连后取消发往旧 Session

- 改动：Dispatch 记录实际成功发送的 Session（重试成功时更新），
  取消只发给承载任务的 Session；新增统一任务状态
  （pending/dispatched/completed/failed/cancelled/timeout）。
- 结论：重连后取消不再发往已失效的旧连接（旧行为是静默丢取消，
  任务可能在旧 Session 上继续跑完）。已做反向验证：改回旧行为时
  新增测试失败。

### 问题 6（P2）：两端任务超时分开配置

- 改动：`WAKEHUB_SERVER_TASK_TIMEOUT` 默认 15s、
  `WAKEHUB_AGENT_TASK_TIMEOUT` 默认 10s；服务端应比 Agent 长
  （建议至少 +5s）。
- 安全意义：服务端等待略长于 Agent 执行，避免 Agent 刚超时、服务端
  已先取消造成的“执行了但结果丢了”的不一致。

### 问题 7（P2）：Sleep 端点网络范围校验（最小一致版）

- 改动：保留 `ValidateEndpoint` 基线；新增 Agent 管理网段范围校验，
  在服务端下发时执行（http_sol 校验端点、SSH 校验目标 IP）；回环
  始终放行；Agent 未声明网段时只做基线校验，不拦截老 Agent。
- 刻意收窄：不另设设备级白名单字段——设备存的端点/IP 本身即白名单，
  睡眠请求 body 里没有端点字段，调用方无法把请求发往别的地址。
  用户 2026-11-06 已明确不希望为家用场景加过多限制，另设白名单
  字段与配置项暂缓。
- 仍未做：睡眠端点本身无签名认证（见“剩余风险”）。

### 问题 8（P2）：Agent 网段与实际网卡校验

- 改动：Agent 启动时枚举本机网卡，配置网段须被本机某网段包含
  （子网可接受、超网拒绝）；不符则拒绝启动并提示本机实际网段；
  `WAKEHUB_AGENT_SKIP_SUBNET_CHECK=1` 可跳过（特殊组网逃生口）。
  install.sh 自动检测默认路由网卡的直连网段做默认值，多网卡时
  编号选（容器网桥等虚拟接口不参与）。
- 结论：`192.168.1.0/24` 写死默认值用在 `192.168.1.0/24` 局域网这类
  静默错配，在启动期就被拦住，不再等到“唤醒不到设备”才发现。

### 问题 9（P2）：登录限流加固

- 改动：失败记录抽为 `failureStore` 接口（内存实现，语义与旧代码
  等价；需要跨重启/多实例持久化时可换 SQLite/Redis 实现，不为
  单机引入 Redis）；IP + 账号双维度、窗口 5 分钟、阈值 8、容量
  上限 10000、超限淘汰最旧、5 分钟清理 ticker（以上均已存在，
  本次只补存储扩展点与文档）。
- 明确写进文档：失败记录只存内存，重启后清空；成功登录清除记录；
  未配信任列表时反代后共享代理 IP 限流（须配
  `WAKEHUB_TRUSTED_PROXIES` 才按真实 IP 限流）。

### 问题 10（P3）：JSON 请求体只解码一次

- 改动：统一 `decodeOneJSON`，首次 Decode 后再 Decode 一次，非
  io.EOF 一律 400；空体按调用方语义处理（唤醒端点允许空体）。
- 结论：`{"a":1} {"b":2}` 这类尾部拼接体不再被静默接受前半段。

### 问题 11（P3）：SQLite 数据目录权限

- 改动：数据目录 / 备份目录 / Agent token 目录 0700（已存在的旧
  目录在启动时也被显式收紧——`MkdirAll` 本身不改已存在目录）；
  库文件及 -wal/-shm 伴生文件 0600；迁移备份 0600；backup.sh
  的备份目录与临时拷贝目录 0700。
- 容器内核对：本机沙箱跑不起容器，容器内实测只能在 VM 做，
  命令见 `docs/MIGRATION-v0.2.9.md` §5，已列入 VM 验证清单。

### 问题 12（P2）：迁移保护增强

- 改动：迁移前自动备份（`VACUUM INTO` + 0600）；整轮结束时设备
  数量绊线（devices 减少且本轮未应用 005 时拒启）；迁移后打印
  数量核对；修了 `AppliedVersions` 在迁移表不存在时报错的真 bug。
- 结论：迁移失败停启动、不丢设备；备份可恢复（恢复步骤见迁移文档）。

### 问题 17（P3）：构建源改 Build Args

- 改动：Dockerfile 用 `ARG NPM_REGISTRY` / `ARG GOPROXY`
  （默认国内源），compose build.args 透传，`.env` 可覆盖，
  install.sh 重跑保留。
- 安全意义：构建源可被覆盖，默认源不可达时不必改 Dockerfile 就能
  换源；默认仍为国内源（与此前写死的值一致，无行为变化）。

### 问题 18（P2）：Server 绑定地址选项

- 改动：新增 `WAKEHUB_BIND_ADDRESS`，compose 端口映射改为
  `"${WAKEHUB_BIND_ADDRESS:-0.0.0.0}:${WAKEHUB_PORT:-8080}:8080"`。
- **纠正任务书**：默认保持 `0.0.0.0`，不改 `127.0.0.1`——Cloudflare
  隧道从另一台机器连服务器 IP，绑回环会直接断连。只在“仅本机访问”时才改 `127.0.0.1`，文档与注释已写明。

### 问题 19（P3）：install.sh 安装检测（多网卡）

- 改动：多网卡时编号选负责网段（默认路由网卡排第一）；容器网桥
  （docker0/veth/br-* 等）与隧道接口不参与；无保存值且单网段时
  沿用老行为（只打印一行检测提示）。
- 安全意义：安装期就把 Agent 网段钉在真实局域网，避免误选容器
  网段导致 WOL 发不出去。

### 问题 13：暂缓

前端导航 Emoji 图标统一为 SVG——按用户「先给设计图、选定再实现」
的规矩暂缓，不进 v0.2.9。

## 剩余风险（未改，用户已知）

1. **Server 本身无 TLS**：浏览器与 Agent 到 Server 均为明文 HTTP；
   公网访问必须走反代/隧道做 TLS 终止，不要直接暴露 8080。
2. **睡眠端点无签名认证**：http_sol 端点无鉴权，仅限可信局域网内使用。
3. **SSH 凭据集中**：存的是 Windows/NAS登录密码本身（AES-256-GCM
   加密，密钥由 SECRET_KEY 派生），库与 SECRET_KEY 同在 VM 上；
   VM 被拿下等于该密码被拿下。TOFU 首次连接不设防。
4. **无 2FA**：管理员登录被盗可关所有机器（但偷不出 SSH 密码明文）。
5. **登录限流仅内存**：重启后清空（见问题 9）。

用户 2026-11-06 17:16 已明确：不希望为此加过多限制，怕影响使用；
家用现状已够用，专用账号/指纹显示/2FA 均暂缓，按需再做。
