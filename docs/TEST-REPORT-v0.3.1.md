# 测试报告 — v0.3.1

> 验收时间：2026-11-07 凌晨。结论：**全部通过**，可交付。
> 本次改动只动 entrypoint 权限模型 + 版本号，Go/前端无功能改动。
> 本机沙箱跑不起 Docker 容器（与代码无关），容器内验证仍须在 VM/盒子做，
> 见 §6。`docker run` 真机验证须在用户 VM/盒子上做，见 §7。

## 1. 后端

- `go vet ./...`：干净。
- `gofmt -l`：干净。
- `go test ./... -count=1`：**14 包全过**（api 21.5s、auth 15.7s、agent 5.1s）。
- `go test -race ./internal/agent/ ./internal/api/ ./internal/config/ ./internal/sleep/`：**4 包全过、无数据竞争**（api 180.4s，race 下慢属正常）。
- 版本号只改真实引用：`router.go` 的 serverVersion、`client.go` 的
  agentVersion、m5/m7 测试里的版本号与错误信息串、前端与文档；历史引用
  未动（如 `hub.go` 里的“v0.3.0 单容器”注释、`hub_test.go` 里传给
  `AuthenticateAgent` 的 `"0.3.0"` 夹具值）。

## 2. 前端

- `npm run typecheck`（vue-tsc）：通过。
- `npm run test`（Vitest）：**38/38**（与 v0.3.0 相同，无新增前端测试）。
- `npm run build`（vite）：通过。

## 3. 新增测试：entrypoint 桩测（`scripts/test-entrypoint.sh`）

新增三路 + 两项反向验证，**37/37 全过**：

- **场景 1（root 属主目录）**：entrypoint 以 root 启动，先 `chown -R
  wakehub:wakehub` + `chmod 700` 数据目录；三个密钥文件落盘属主 wakehub、
  权限 0600；`.admin_password` 内容为 env 值；su-exec 对 Server 与 Agent
  均以 `wakehub:wakehub` 调用；两桩进程 uid 均为 10001；两进程拿到的
  SECRET/ENROLL 一致且与文件一致。
- **场景 2（已正确属主的目录）**：不重复 chown 也能跑；密钥从文件复用，
  文件未被改写。
- **场景 3（密钥写失败）**：`.secret_key` 为悬空符号链接时 entrypoint
  exit 1、报错“无法写入”、无“已随机生成并保存”字样、两桩进程均未启动。
- **反向验证 R1**：旧版 entrypoint 在场景 1 夹具下目录保持 root 属主、
  未调 su-exec、桩进程 uid 0——旧版确实有 bug，测试能抓住。
- **反向验证 R2**：去掉 `set -eu` 后（模拟 BusyBox ash 的宽松行为），
  旧版打印虚假“已随机生成并保存”并继续启动进程；新版仍 exit 1 且无该
  字样——误报 bug 已修，测试能抓住。
- 桩实现：桩掉 su-exec（用 `su -p` 真实降权到 wakehub）与 chown（记调用、
  再调真实 chown），其余命令真实；桩进程以 wakehub 写 marker。
  沙箱教训：本机 umask 0007，新建文件/目录默认仅属主+组可读写，桩文件
  与 entrypoint 副本须显式 `chmod 755`，否则 wakehub 读不到。

## 4. 原生 e2e + 升级冒烟（`/tmp/run-e2e-031.sh`，真实二进制）

**31/31 全过**。三阶段：

- **阶段 A（v0.3.0 态数据）**：旧版 entrypoint（`git show HEAD`）+ 新版
  二进制跑起来（DB schema 与 v0.3.0 相同，无新迁移）；登录成功；
  `agent-local` 自动获批 connected；创建 1 台设备成功；数量
  devices/agents/networks = **1/1/1**；旧版未改目录属主（仍为 root）。
- **阶段 B（v0.3.1 升级同一数据目录）**：新版 entrypoint 升级；health
  `version=0.3.1`；数据目录属主 wakehub、权限 0700；三个密钥文件属主
  wakehub、权限 0600；su-exec 以 wakehub 调用；Server 与 Agent 进程 uid
  均为 10001；登录成功；`agent-local` 复用旧 token 自动获批 connected；
  数量仍 **1/1/1**（设备/网络不变）；审计有自动获批记录；API 设备列表
  含 e2e-pc。
- **阶段 C（重启后密钥复用）**：再重启一次 entrypoint；登录成功（密码
  复用）；`agent-local` 重新 connected（token 复用）；数量仍 **1/1/1**。

e2e 排障记录（均为测试脚本的隔离问题，非产品 bug）：

- 旧版 entrypoint 把 Agent token 文件默认写死在 `/app/data/agent-token`
  （容器内正确），而 e2e 数据目录在 `/tmp/e2e-031/data`；第一轮 Agent
  把 wha_ 令牌存到 `/app/data/agent-token`，后续轮次 Agent 优先读它、
  拿旧令牌连全新数据库 → 握手 401。修法：e2e 里
  `WAKEHUB_AGENT_TOKEN_FILE=$E2E/data/agent-token`，并删掉沙箱残留的
  `/app/data`。产品本身不受影响（容器内 token 文件本就在数据目录里）。
- `pgrep -f` 会同时命中 su 包装进程（其命令行也含二进制路径），改用
  `pgrep -x` 按进程名定位真进程；`/proc/<pid>/exe` 在本沙箱读不到
  （连 root 自己的进程都读不到），不能用它定位。
- `check` 的 eval 字符串里 `$(...)` 须写成 `\$(...)`（与桩测同款），
  否则在调用时展开、引号错乱。

## 5. 未在本机验证（须真机）

- Docker 镜像构建（沙箱 runc 被禁，跑不起容器）。
- `docker run` 真机验证（VM/盒子）：一条命令拉起、打开页面、登录、
  Agent 自动入网、WOL 唤醒、SSH 关机；**重点是本次修的 bug**：宿主机
  数据目录为 root 属主时，容器须能正常启动（不再 Restarting 循环）。
- compose 模式回归（server/agent 服务已显式加 `user: "10001:10001"`，
  应继续以 wakehub 身份跑，不经过入口的 root 阶段）。

## 6. 待用户真机验证清单

1. VM 上跑 README“快速启动”的 `docker run` 命令（**不用先 chown**），
   确认一条命令即部署、页面可打开、登录成功、Agent 自动入网。
2. 换端口验证：`-e WAKEHUB_LISTEN_ADDR=:18081` 后页面与健康检查正常
   （v0.3.0 未验，顺带补）。
3. 重启容器后密钥复用（不重新生成、不丢 Agent）。
4. 盒子上测飞牛 WOL 唤醒 + SSH 关机（sudo 路径沿用 v0.2.12，须先勾选）。
