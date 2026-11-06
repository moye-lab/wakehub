# 测试报告 — v0.3.0

> 验收时间：2026-11-07 早。结论：**全部通过**，可交付。
> 本机沙箱跑不起 Docker 容器（与代码无关），容器内验证仍须在 VM/盒子做，
> 见 §6。`docker run` 真机验证须在用户 VM/盒子上做，见 §7。

## 1. 后端

- `go vet ./...`：干净。
- `gofmt -l`：干净。
- `go test ./... -count=1`：**14 包全过**（api 20.7s、auth 14.3s）。
- `go test -race ./internal/agent/ ./internal/api/ ./internal/config/ ./internal/sleep/`：**4 包全过、无数据竞争**（api 181.7s，race 下慢属正常）。
- 注意：全量跑时 api 包曾两次偶发失败，单独跑与重跑均通过，属已知抖动
  （`TestAgentManagementAPI` 的 5s 轮询在并行负载下超时）；最终
  `-count=1` 全量与 api 单独跑均通过。

## 2. 前端

- `npm run typecheck`（vue-tsc）：通过。
- `npm run test`（Vitest）：**38/38**（与 v0.2.12 相同，无新增前端测试）。
- `npm run build`（vite）：通过。

## 3. 新增与改动的测试

### 3.1 回环自动批准（`internal/agent/hub_test.go`）

- 新增 `TestLoopbackAutoApprove`：新 Agent 回环入网直接获批 +
  `AutoApproved` + token 非空；下发的 token 立即可用；非回环重入网
  仍回 pending；回环 pending Agent 自动批准；回环已获批 Agent 丢 token
  后重入网轮换 token、不降级；禁用的 Agent 回环也不放行；`::1` 也是回环。
- 反向验证：临时把回环分支条件改为 `false` 后，测试在首个断言
  （Approved+AutoApproved+Token 非空）即失败，恢复后通过——测试确实
  能抓住回归。
- 改动 `TestDispatchToPendingAgentRefused`：测试桩 `newTestServer` 写死
  IP 为 127.0.0.1，自动批准会改掉其 pending 语义；改为
  `newTestServerWithIP(t, hub, "192.168.1.65")`，用非回环 IP 测 pending
  路径。新增 `newTestServerWithIP` 桩，并让桩镜像真实 handler（自动批准
  时立即下发 `registration_approved`）。

### 3.2 API 层手动审批（`internal/api/agents_test.go`）

- `TestAgentManagementAPI` 第 8 步（自助注册 pending → 审批）受影响：
  测试服务器在回环地址上，入网连接会被自动批准。改为用
  `setupTestServerWithConfig` 把 127.0.0.1 配为可信代理、拨号时带
  `X-Forwarded-For: 192.168.1.65`，让该步走非回环 pending 路径（真实
  远端 Agent 的情形）；per-agent token 路径不看 IP，不受影响。
- 修改后 `TestAgentManagementAPI` 通过。

### 3.3 网段自动检测（`internal/config/config_test.go`）

- 新增 `TestDetectPrimarySubnet`：有网卡时返回首个非回环 IPv4 网段
  （与 `LocalNetworks()[0]` 一致且为合法 CIDR）；无网卡时返回 ""。
  沙箱有网卡（198.19.0.0/30），通过。

## 4. entrypoint 脚本验收（桩 + 真实二进制）

- `sh -n docker-entrypoint.sh`：语法通过。
- 桩测试（三路）：无 env 时三个密钥全生成（管理员密码打印、另两个不打印）、
  重启后从文件复用（值与首次一致）、env 覆盖管理员密码生效；目录 700、
  三个密钥文件 0600；Server 与 Agent 拿到的 SECRET/ENROLL 一致。
- 真实二进制 e2e：只给 `WAKEHUB_ADMIN_PASSWORD` + `WAKEHUB_LISTEN_ADDR`
  `:18098`，entrypoint 起双进程，Agent 连上 `ws://127.0.0.1:18098`，
  登录成功，`agent-local` 为 connected。
- **抓到的真 bug**：首版 entrypoint 里 Agent 的 WS 地址写死 8080，
  Server 改端口到 18098 后 Agent 连不上（`connection refused`）。
  已修：WS 端口跟随 `WAKEHUB_LISTEN_ADDR`（`${WAKEHUB_LISTEN_ADDR##*:}`，
  默认 8080）；修后 e2e 通过。健康检查同样跟随监听端口（Dockerfile）。

## 5. 单容器模式原生冒烟（`run.sh`，真实 Server+Agent 同起）

- 登录成功；健康接口 `version=0.3.0`。
- `agent-local` 自动获批并 connected（无须手动审批）。
- `agent-token` 已持久化（`wha_` 前缀）。
- 审计里有“自动获批”记录。
- 重启后 token 复用，`agent-local` 重新 connected（不再走入网）。

## 6. 未在本机验证（须真机）

- Docker 镜像构建（沙箱 runc 被禁，跑不起容器）。
- `docker run` 真机验证（VM/盒子）：一条命令拉起、打开页面、登录、
  Agent 自动入网、WOL 唤醒、SSH 关机。
- compose 模式回归（server 服务显式覆盖 entrypoint 后只跑 server）。

## 7. 待用户真机验证清单

1. VM 或盒子上跑 README“快速启动”的 `docker run` 命令，确认一条命令
   即部署、页面可打开、登录成功、Agent 自动入网。
2. 盒子上测飞牛 WOL 唤醒 + SSH 关机（sudo 路径沿用 v0.2.12，须先勾选）。
3. 改端口验证：`-e WAKEHUB_LISTEN_ADDR=:18081` 后页面与健康检查正常。
4. 重启容器后密钥复用（不重新生成、不丢 Agent）。
