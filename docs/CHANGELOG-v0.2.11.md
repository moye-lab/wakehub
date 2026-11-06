# Changelog — v0.2.11

> 本文件记录 v0.2.11 的全部变更。v0.2.11 修 v0.2.10 部署后 §5 容器内
> 权限核对抓到的真问题：Agent 数据卷根目录是 0755 而不是 0700。
> 升级方式与 v0.2.10 相同：解压后直接 `docker compose up -d --build`。

## Agent 数据卷根目录 0755（2026-11-06 晚，VM 实测发现）

**背景**：v0.2.10 的 §5 核对里，Agent 卷内 `/app/data` 是 0755
（期望 0700），token 文件本身 0600 正常、无密码暴露。根因：
`docker volume create` 建出的卷根是 root 属主 0755，Agent 以
wakehub(10001) 身份既改不了属主也改不了权限；且收紧逻辑只在入网时
（`onRegistrationApproved`）跑一次、错误还被 `_ =` 吞掉，重启时不
重跑，目录一直留在 0755。Server 卷不受影响（compose 首挂继承镜像
属主 10001，`InitDB` 每次启动都收紧）。

**改动文件**：`scripts/migrate-agent-volume.sh`、
`cmd/agent/main.go`、`internal/agent/client.go`。

- **迁移脚本**：新增 `fix_new_volume_perms`——建卷后立即用
  `--user 0` 一次性容器把卷根改成 `10001:10001` + `0700`，并用
  stat 校验（`PERMS_OK` 才算通过）；主流程与全新安装分支都调它；
  `AGENT_IMAGE` 回落值 `wakehub:0.2.8` → `wakehub:0.2.11`。
  注意：payload 里路径须带尾斜杠（`/new/`），假 docker 桩按
  「容器路径/」改写挂载点，不带斜杠的 `/new` 不会被改写。
- **Agent 启动收紧**：`cmd/agent/main.go` 在配置校验后、连接前，
  每次启动都对 token 所在目录调 `EnsurePrivateDir`；失败只打警告
  不拦截启动（目录 0755 不影响连接；属主不对时靠部署侧一次性
  chown 修复）。
- **入网收紧打警告**：`client.go` 的 `onRegistrationApproved`
  里收紧失败改打警告（之前用 `_` 吞掉，755 目录静默留存）。

**已部署卷的修复**（代码修不了已存在的 root 属主目录，须一次性
处理；2026-11-06 晚已在用户 VM 上跑通）：

```bash
docker run --rm --entrypoint sh --user 0 -v wakehub-v020_wakehub_agent_data:/app/data wakehub:0.2.10 -c 'chown 10001:10001 /app/data && chmod 700 /app/data && stat -c "%a %U %n" /app/data'
```

## 测试

- **Agent 二进制实测**：0755 的 token 目录启动后变 0700、无警告；
  token 目录建不出来时（`/proc/nope`）打警告、继续重连、不拦截启动。
- **迁移脚本严格桩测试**（`/tmp/stubfix`，脚本以 uid 1000 跑、载荷由
  root daemon 代跑）：旧脚本阴性对照——全流程 exit 0 但新卷目录留在
  770/1000（复现 VM 的 bug）；新脚本主流程——exit 0，新卷目录
  700/10001、token 600/10001、内容一致；新脚本全新安装分支——
  exit 0，新卷目录 700/10001。
- **桩的教训**：daemon 进程会死（本轮实测死过一次），桩的 docker
  run 会在 resp FIFO 上无限等；跑桩测试前先探活 daemon，死掉就用
  `setsid nohup` 重启（不要用 `pkill -f`，会匹配到自身命令行）。

## 版本同步

- 版本号统一 0.2.11：`internal/api/router.go`（serverVersion）、
  `internal/agent/client.go`（agentVersion）、m5/m7 测试期望、
  `web/package.json` + `package-lock.json`、SettingsView 文案、
  `docker-compose.yml`（镜像 tag ×2 + 首行注释）、Dockerfile、
  `install.sh`、README、api/deployment/security 文档标题与示例。
  API 总数仍为 50（无新增端点；无新迁移）。
