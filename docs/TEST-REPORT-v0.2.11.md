# 测试报告 — v0.2.11

> 验收时间：2026-11-06 晚。结论：**全部通过**，可交付。
> 本机沙箱跑不起 Docker 容器（与代码无关），容器内验证仍须在 VM 做，
> 见 §6。

## 1. 后端

- `go vet ./...`：干净。
- `gofmt -l`：干净。
- `go test ./... -count=1`：**14 包全过**（api 20.9s、auth 16.0s）。
- `go test -race ./... -count=1`：**14 包全过、无数据竞争**
  （api 232.9s、auth 163.9s，race 下慢属正常）。

## 2. 前端

- `npm run typecheck`（vue-tsc）：通过。
- `npm run test`（Vitest）：**35/35**。
- `npm run build`（vite）：通过。

## 3. Agent 启动收紧实测（最终版二进制，真跑）

- 正向：0755 的 token 目录，启动后变 0700、无警告，随后正常进入
  连接重试。
- 反向：token 目录建不出来时（`/proc/nope`），启动打警告
  「token 目录 /proc/nope 收紧为 0700 失败」，继续重连、不拦截启动。
- 反向验证说明：收紧逻辑是 `EnsurePrivateDir`（已有单元测试覆盖），
  本轮只验证接线（启动时调一次、失败打警告不拦截）；失败分支的
  触发条件在上面两例里各跑了一次。

## 4. 迁移脚本严格桩测试（/tmp/stubfix，脚本以 uid 1000 跑）

- 旧脚本阴性对照：全流程 exit 0、token 600/10001 内容一致，但新卷
  目录留在 **770/1000**（复现 VM 的 bug）。
- 新脚本主流程：exit 0，新卷目录 **700/10001**、token 600/10001、
  内容一致；新增「设置新卷根目录属主与权限」步骤有日志。
- 新脚本全新安装分支（无旧卷）：exit 0，新卷目录 **700/10001**。
- 桩的教训：daemon 进程会死（本轮实测死过一次，桩的 docker run
  会在 resp FIFO 上无限等）；跑桩测试前先探活 daemon，死掉就用
  `setsid nohup` 重启；不要用 `pkill -f`（会匹配到自身命令行）。
  另：后拷进夹具的脚本文件是 root 属主 770，uid 1000 读不了，须
  先 chown 成 1000 属主再跑。

## 5. compose 校验

- python yaml 解析验证：两服务镜像 tag 均为 `wakehub:0.2.11`、
  build.args 齐全、server 端口映射
  `"${WAKEHUB_BIND_ADDRESS:-0.0.0.0}:${WAKEHUB_PORT:-8080}:8080"` 正确。

## 6. VM 验证清单（待用户侧）

- 部署：解压后直接 `docker compose up -d --build`（卷已在 v0.2.9
  分离，无需再跑迁移脚本；无新迁移）。
- 已部署卷的一次性修复已在 0.2.10 验证时跑通（目录 700 + wakehub
  属主、token 600），0.2.11 部署后 Agent 重启即走启动收紧，不会
  再回到 755。
- 核对：health 0.2.11、三台设备都在、网络列显示 `192.168.1.0/24`、
  Agent 在线；`docker exec wakehub-agent-local stat -c '%a %n'
  /app/data /app/data/agent-token` 应为 700 / 600。
