# Changelog — v0.3.1

> 本文件记录 v0.3.1 的全部变更。v0.3.1 只修一件事：**v0.3.0 单容器
> 版在真机上暴露的 bind mount 权限 bug**——README 那条命令在 root
> 账号下跑不起来。修完后同一条命令在任何机器上都不用再加 chown。
> 升级方式：已有 v0.3.0 单容器部署直接 `docker rm -f wakehub` 后重
> 跑同一条命令（数据卷原地保留，老数据原地可用）；已有 compose
> 部署照旧 `docker compose up -d --build`（无新迁移）。

## 背景（2026-11-07 上午，VM 真机实测）

v0.3.0 发布后用户在 VM 上跑 README 的单容器命令：

```bash
docker run -d --name=wakehub --restart unless-stopped --network host \
  --cap-add NET_RAW -e WAKEHUB_ADMIN_PASSWORD=admin123 \
  -v ./data:/app/data moyelab/wakehub:0.3.0
```

镜像拉取成功（digest 与盯盘一致），容器起来后陷入 Restarting 循环。
日志：

```
[entrypoint] 警告：数据目录 /app/data 收紧为 0700 失败
/app/docker-entrypoint.sh: line 66: can't create /app/data/.admin_password: Permission denied
[entrypoint] 未设置 WAKEHUB_SECRET_KEY，已随机生成并保存到数据卷（不打印）
2026/11/07 00:44:10 [WakeHub] 数据库初始化失败: chmod /app/data: operation not permitted
```

根因：用户在 VM 上是 root 账号，`-v ./data:/app/data` 落到宿主机是
`/root/data`，属主 root；但 v0.3.0 的镜像以 `USER wakehub`（10001）
运行，entrypoint 写 `.admin_password` / `.secret_key` /
`.enroll_token` 全是 Permission denied，Server 初始化数据库时
`chmod /app/data` 直接失败退出，entrypoint 杀掉 Agent、容器退出、
`--restart unless-stopped` 再拉起来，无限循环。沙箱跑不起容器
（runc setns 被禁），这一步之前没测到。

当时即时修法是 `chown -R 10001:10001 /root/data` 后重跑，容器随即
Up (healthy)，Agent 自动获批、health 返回 0.3.0。但“先 chown 再跑”
多了一个步骤，违背 v0.3.0 “一条命令”的初衷，所以要做 v0.3.1 彻底修。

## 1. 权限模型：root 启动 + su-exec 降权

**改动文件**：`docker-entrypoint.sh`、`Dockerfile`、
`docker-compose.yml`、`docs/deployment.md`、README。

- **入口以 root 启动**：`Dockerfile` 去掉 `USER wakehub`（镜像默认
  root）。entrypoint 先确保 `/app/data` 存在，属主改成
  wakehub(10001)、权限 0700（已是正确属主时跳过 chown，递归改属主
  时顺带修卷内已有的 root 属主文件）；chmod/chown 失败直接报错退出，
  不继续跑——继续跑只会重蹈 v0.3.0 的重启循环。
- **密钥写文件后属主 wakehub、0600**：`resolve_secret` 三个分支
  （env 写回、文件复用、新生成）写完都经 `secure_secret_file`
  收紧（属主 wakehub、权限 0600）。
- **长驻进程仍以 wakehub 运行**：密钥解析完后，entrypoint 用
  `su-exec wakehub:wakehub` 启动 Server 与 Agent。su-exec 是
  setuid/setgid 后直接 exec 目标的微型工具（Alpine 主仓库有包，
  `apk add su-exec`），无中间包装进程，PID 与信号行为与直接启动
  一致，环境变量原样透传；Agent 的 ping 仍靠 `/bin/ping` 的文件
  能力 cap_net_raw+ep（不受影响）。入口启动前校验 su-exec 与
  wakehub 用户存在，缺失直接报错退出。
- **非 root 运行降级兼容**：入口被 `--user 10001` 之类方式以非
  root 启动时，跳过属主修正与降权（警告后直接启动进程）；密钥写
  失败仍由 resolve_secret 报错退出，不静默循环。
- **compose 模式不变**：server/agent 两个服务加
  `user: "10001:10001"`，继续以 wakehub 身份跑（与 v0.3.0 的行为
  一致；命名卷本就是 wakehub 属主，不经过入口的 root 阶段）。
  `scripts/migrate-agent-volume.sh` 与 `scripts/backup.sh` 不受
  影响（它们本来就用 `--entrypoint sh --user 0` 的一次性容器）。

## 2. entrypoint 误报 bug：写失败不再打印“已保存”

**改动文件**：`docker-entrypoint.sh`。

- v0.3.0 的 `resolve_secret` 用 `$(...)` 命令替换接返回值；写失败
  时 `set -e` 在命令替换里的行为随 shell 而异——沙箱的 dash 会直接
  退出，Alpine 的 BusyBox ash 却继续执行，于是“已随机生成并保存到
  数据卷（不打印）”照常打印，掩盖了真正的问题（VM 日志为证）。
- v0.3.1 起 `resolve_secret` 不再用命令替换返回值，改走全局变量
  `RESOLVED_VALUE` + 每一步显式检查：`printf > 文件` 失败、
  `chown`/`chmod` 失败、读取失败、随机值生成为空，一律向 stderr
  报错并 `exit 1`；“已保存”只在写成功后打印。

## 3. 版本号 0.3.0 → 0.3.1

**改动文件**：`internal/api/router.go`（serverVersion）、
`internal/agent/client.go`（agentVersion）、`internal/api/m5_test.go`、
`internal/api/m7_test.go`、`web/package.json`、
`web/package-lock.json`（仅顶层两处）、`web/src/views/SettingsView.vue`、
`scripts/migrate-agent-volume.sh`（AGENT_IMAGE 回退）、`install.sh`
头注释、`.github/workflows/docker-publish.yml` 示例 tag、README、
`docs/api.md`、`docs/deployment.md`、`docs/security.md`、根
`CHANGELOG.md`（新增 v0.3.1 节）、`Dockerfile`、
`docker-compose.yml`（两处 image）、`docker-entrypoint.sh`。

历史引用未动（如 `internal/agent/hub.go` 里“v0.3.0 单容器”的功能
出处记录、`docs/CHANGELOG-v0.3.0.md`）。

## 4. 文档

- `docs/deployment.md`：单容器一节加一行——权限不用手动处理，入口
  以 root 启动、先修数据卷属主再降权，宿主机目录是 root 属主时也不
  用加 chown。README 的 `-v ./data` 说明里同样加了一句。
- `docs/TEST-REPORT-v0.3.1.md`：测试报告（含 entrypoint 桩测三路 +
  反向验证、Go/前端、原生 e2e、升级冒烟）。
- `scripts/test-entrypoint.sh`（新增）：entrypoint 桩测脚本，三路
  场景 + 反向验证，可重复跑。

## 5. 未在本机验证（须真机）

- Docker 镜像构建（沙箱 runc 被禁，跑不起容器；`apk add su-exec`
  是否顺利、盒子 ARM 构建是否顺利，都须真机验证）。
- `docker run` 真机验证（VM/盒子）：root 账号下直接跑 README 的
  单容器命令，不加 chown，确认容器一次起来、页面可打开、登录成功、
  Agent 自动入网。
- compose 模式回归（server/agent 服务加 `user: "10001:10001"` 后，
  以 wakehub 身份跑、命名卷读写正常）。
