# Changelog — v0.3.0

> 本文件记录 v0.3.0 的全部变更。v0.3.0 的目标只有一个：**一条命令
> 部署，不跑任何脚本**——用户原话：“我真正需要的是减少步骤，一条命令
> 不跑任何脚本直接 docker run”。v0.3.0 起镜像默认是单容器：同一容器内
> 同时跑 Server 与本地 Agent，`docker run` 一条命令即部署。
> 升级方式：已有 compose 部署照旧（`docker compose up -d --build`，
> 无新迁移，老数据原地可用）；新部署直接用单容器命令（见 README
> “快速启动”）。

## 背景（2026-11-07 早，用户原话）

v0.2.12 发布后 Docker Hub 镜像已上架，用户问：“现在用
`docker pull moyelab/wakehub:0.2.12` 拉取部署就行了吗？不用跑脚本了吗？”
答：不行——`docker pull` 只下载镜像，部署还得要 compose 文件 + `.env`
（三个随机密钥）+ `docker compose up -d`，install.sh 还是绕不开。用户
追问：“那说到底它还不是依赖其它！这个项目有没有可能能直接一个镜像
就搞定，比如像 subhub 那样不依赖其他？”随后拍板：**就是要这条命令
直接能跑，不跑任何脚本**。

## 1. 单容器模式（新部署方式，推荐）

**改动文件**：`docker-entrypoint.sh`（新增）、`Dockerfile`、
`internal/agent/hub.go`、`internal/api/router.go`、
`internal/config/config.go`、`cmd/agent/main.go`、
`docker-compose.yml`、README、`docs/deployment.md`。

- **默认入口改为单容器**：镜像 `ENTRYPOINT` 从 `/app/wakehub-server`
  改为 `/app/docker-entrypoint.sh`，同一容器内同时跑 Server 与本地
  Agent。部署命令（见 README“快速启动”）：
  ```bash
  docker run -d --name=wakehub --restart unless-stopped --network host \
    --cap-add NET_RAW \
    -e WAKEHUB_ADMIN_PASSWORD=你的密码 \
    -v ./data:/app/data moyelab/wakehub:0.3.0
  ```
- **密钥自生成持久化**：entrypoint 解析三个密钥，规则是 env（非占位值）
  > 数据卷里的文件 > 随机生成并写文件（0600）；解析出的值一律写回文件，
  保证重启后与首次一致。只填管理员密码即可；SECRET_KEY 与入网令牌不填
  时首次启动自动生成（`.secret_key` / `.enroll_token`，不打印）；管理员
  密码不填时也自动生成 16 位随机值并打印在启动日志里。注意：管理员密码
  只在首次建账号时生效，已建账号后改密码走网页改密或 reset-password；
  SECRET_KEY 一经生成即持久化，换值会使已存 SSH 密码无法解密。
- **本地 Agent 自动入网**：Server 侧回环 Agent 持有效入网令牌直接自动
  获批（`AuthenticateAgent` 新增回环分支，见下 §2），无须管理员在界面
  上点审批；Agent 网段不配时自动检测本机首个非回环 IPv4 网段（见下 §3）。
- **双进程监督**：entrypoint 后台启动 Server 与 Agent，任一退出即杀掉
  另一个、整个容器退出（`--restart unless-stopped` 重拉整个容器）；
  SIGTERM/SIGINT 转发给两个子进程。
- **端口跟随**：Agent 的 WS 地址端口跟随 `WAKEHUB_LISTEN_ADDR`（默认
  :8080），改端口时不脱节；健康检查也跟随监听端口（Dockerfile
  HEALTHCHECK 用 `${WAKEHUB_LISTEN_ADDR##*:}` 取端口，默认 8080）。
- **数据卷合并为一个**：`./data:/app/data` 是唯一数据卷（数据库、审计、
  SSH 密文、Agent token、自动生成的密钥都在里面）。compose 模式仍用
  两个命名卷（`wakehub_data` / `wakehub_agent_data`），不受影响。

## 2. 回环自动批准（Server 侧）

**改动文件**：`internal/agent/hub.go`、`internal/api/router.go`、
`internal/agent/store.go`（复用已有方法，无改动）、
`internal/agent/hub_test.go`、`internal/agent/testhelper_test.go`、
`internal/api/agents_test.go`。

- **判定条件**：入网令牌有效 **且** 连接来自回环地址（127.0.0.0/8、
  ::1），两者缺一不可。回环地址本身即证明它是本机容器内进程，入网
  令牌又是服务端同容器生成的随机值。
- **三种分流**：不存在 → `CreateAgentApprovedWithToken` 新建已获批行 +
  首个 token；已是 pending → `ApproveAgent` 审批签发 token；已是
  approved（token 文件丢失的重入网）→ `IssueToken` 轮换 token、不降级。
  disabled 仍拒绝（回环也不放行）。
- **token 下发**：`AgentAuth` 新增 `Token` 字段；WS 握手后服务端立即经
  `registration_approved` 下发新 token（仅此一次），Agent 持久化到
  token 文件后重连；此后走 per-agent token 路径，不再用入网令牌。
- **审计**：自动批准记 `agent_register`，文案为“本地 Agent 自动获批入网”
  /“本地 Agent 自动重新入网”，与手动审批的“等待审批”区分。
- **非回环不变**：远端 Agent（另一台机器、另一网段）持入网令牌仍进
  pending，须管理员手动审批——原流程与安全边界未动。

## 3. Agent 网段自动检测

**改动文件**：`internal/config/config.go`、`cmd/agent/main.go`、
`internal/config/config_test.go`。

- 新增 `DetectPrimarySubnet()`：返回本机首个非回环 IPv4 网段（host 网络
  下看到的就是宿主机网卡）。`WAKEHUB_AGENT_SUBNET` 未配置时自动推导并
  打日志；推导失败时留空，由 `Validate()` 报错，提示显式配置。
- 多网卡时取第一条（通常就是主局域网）；不合用时显式配
  `WAKEHUB_AGENT_SUBNET` 覆盖。原有的网段-网卡校验（v0.2.9 问题 8）
  仍在，自动检测出的网段必然通过校验。

## 4. compose 模式兼容

**改动文件**：`docker-compose.yml`。

- 镜像默认入口已是单容器 entrypoint，故 server 服务须显式覆盖
  `entrypoint: ["/app/wakehub-server"]`（只跑 server）；agent 服务
  原先就覆盖为 `["/app/wakehub-agent"]`，不变。
- 两处 `image:` 升为 `wakehub:0.3.0`。compose 的 `.env` 流程、命名卷、
  多 Agent 部署均不受影响；已有部署升级仍是
  `docker compose up -d --build`（无新迁移）。

## 5. 版本号统一

- `serverVersion` / `agentVersion` / `web/package.json` /
  `web/package-lock.json`（仅顶层两处）/ `SettingsView.vue` /
  `scripts/migrate-agent-volume.sh` 的 `AGENT_IMAGE` 回退 /
  `install.sh` 头注释 / `.github/workflows/docker-publish.yml` 示例 tag /
  README / `docs/api.md` / `docs/deployment.md` / `docs/security.md`
  均升为 0.3.0。历史引用（ssh.go、ssh-sleep.md、migration 010 相关测试
  注释里的“v0.2.12”）未动——它们记录的是该功能出自哪个版本。

## 6. 已知限制（v0.3.0）

- 单容器用 `--network host`，端口不能再用 `-p` 映射改，须用
  `-e WAKEHUB_LISTEN_ADDR=:端口` 改；健康检查跟随该端口。
- 单容器只覆盖“一个局域网”的场景；多网段仍需额外再跑 Agent（compose
  模式 3 或单独的 agent 容器）。
- 管理员密码只在首次建账号时生效；已建账号后改 `WAKEHUB_ADMIN_PASSWORD`
  不会改库里密码，须走网页改密或 `reset-password`。
- 容器内验证沙箱做不了（runc 被禁），镜像构建与 `docker run` 真机验证
  须在用户 VM/盒子上做，见 `docs/TEST-REPORT-v0.3.0.md` §6。
