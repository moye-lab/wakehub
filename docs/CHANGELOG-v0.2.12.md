# Changelog — v0.2.12

> 本文件记录 v0.2.12 的全部变更。v0.2.12 的目标只有一个：**SSH 关机
> 装即用**——飞牛这类“禁 root 登录 + 普通用户无权关机”的 NAS，旧版
> 只能让用户自己去目标机写 polkit 规则；v0.2.12 起勾选 sudo 即可，
> 目标机零配置。顺带修了旧版 nohup 后台执行的“发了 ≠ 成了”缺口。
> 升级方式与 v0.2.10/v0.2.11 相同：解压后直接
> `docker compose up -d --build`（010 迁移随 Server 启动自动应用，
> 老设备默认不启用 sudo，行为不变）。

## 背景（2026-11-06 晚，飞牛 SSH 关机实测）

盒子 WakeHub 上 Windows 唤醒 ✓ + SSH 关机 ✓；飞牛唤醒 ✓、SSH 关机 ✗——
点了 4 次睡眠，界面显示“关机指令已发送”，飞牛实际没关。手动复现
`ssh sg@10.0.0.3 'systemctl poweroff'` 返回
`Call to PowerOff failed: Interactive authentication required.`——飞牛
（systemd/logind + polkit）要求关机须交互确认，sg 无权关机；且 Agent
日志里没有任何 sleep 任务记录（命令是 nohup 后台跑的，失败了 Agent
也不知道）。当时用 polkit 规则放行后关机通过（规则写在飞牛系统里，
重启不丢，重装才需重写；只放行 sg，换账号须改规则）。

用户 22:29 定调：**“就这样尽量不让用户自己写规则到目标系统，我们的
项目装即用”**——v0.2.12 据此立项。

## 1. sudo 提权（新功能，目标机零配置）

**改动文件**：`migrations/010_ssh_use_sudo.sql`、
`internal/device/repository.go`、`internal/device/validate.go`（不动）、
`internal/agent/hub.go`、`internal/api/router.go`、
`web/src/api/types.ts`、`web/src/components/DeviceForm.vue`、
`docs/ssh-sleep.md`、`docs/api.md`、README。

- **新设备字段 `ssh_use_sudo`**（bool，默认 false；迁移 010，不改已
  发布的 001–009）：设备表单在 SSH 区、目标系统为 Linux 时出现复选框
  「使用 sudo 执行关机命令」+ 提示文案。勾选后 Agent 以单次会话
  `sudo -S sh -c '<关机脚本>'` 执行：密码经 stdin 一次喂给 sudo（不进
  命令行、不进 ps、不进日志），校验与断电在同一个 sudo 调用里完成，
  不依赖跨会话的时间戳（两段式在飞牛上实测失败，见下「盒子实测」）。
- **单次会话的前提**：无。目标机零配置——用户只需本身有 sudo 权限
  （NAS 管理员账号一般都有），且 SSH 密码与 sudo 密码相同（同一账号
  的登录密码）。
- **归零规则（三重保险）**：`sleep_type != ssh` 或 `ssh_os != linux`
  时 `ssh_use_sudo` 存为 false——仓库层 Create/Update 归一化 + 服务端
  离开 SSH 适配器时清空 + 前端提交时归一化。Windows 目标忽略该开关
  （其命令本就同步返回、失败可见）。`validate.go` 不加硬错误，避免
  用户切换 OS 时被陈旧标记挡住保存。
- **下发链路**：`SleepPayload` 加 `ssh_use_sudo`（omitempty）；
  `hub.go` 的 sleep 分支传 `UseSudo: p.SSHUseSudo`；服务端 sleep
  handler 的 ssh 分支加 `SSHUseSudo: d.SSHUseSudo`；更新合并 handler
  的 bool 分支新增 `"ssh_use_sudo"`（与 `"enabled"` 同组）。
- **老设备行为不变**：010 的 `DEFAULT 0`，升级后已有设备全部
  `ssh_use_sudo=0`；升级冒烟已验证 devices 3→3、ssh_use_sudo 全 0。

## 2. Linux 关机改前台执行，失败可见（修“发了 ≠ 成了”）

**改动文件**：`internal/sleep/ssh.go`、`internal/sleep/ssh_test.go`。

- **旧形态（v0.2.11 及之前）**：Linux 关机命令是 nohup 后台跑的，
  目标机上执行失败（如权限被拒）时 Agent 不知道、照样上报成功，
  界面显示“关机指令已发送”、审计照记 device_sleep。
- **新形态**：sudo 与非 sudo 都去掉 nohup/`&`，统一前台执行；断电链
  的 `2>/dev/null` 去掉，错误文本经 session stderr 捕获回显。结果
  解读：退出码 0 = 成功（断电已发起）；`*ssh.ExitError` = 失败
  （错误带退出码 + stderr 首行）；连接被掐断且已运行 ≥
  `poweroffDropGrace`（1 秒，与脚本内 `sleep 1` 对齐）= 成功
  （目标正在断电，sshd 随系统下线掐断连接是预期结果）；过早掐断 =
  失败；ctx 超时 = 失败。
- **stdout/stderr 分开捕获**：ssh 库用两个协程分别拷 stdout 与
  stderr，同一 `bytes.Buffer` 会被并发写——race 实测命中（见测试
  报告 §5）。改为两个 Buffer，合并时 stderr 在前（调用方只在失败时
  用首行做回显，错误文本都在 stderr）。
- **Windows 不动**：其命令本就同步返回、失败可见。

## 3. 前端

- `types.ts` 的 Device 加 `ssh_use_sudo: boolean`；新增 helper
  `sshSudoApplies(sleepType, sshOs)`（只对 SSH 的 Linux 目标返回
  true），`types.test.ts` 加 3 例。
- `DeviceForm.vue`：SSH 区加复选框（仅 `ssh_os === 'linux'` 时显示）
  + 提示文案；form 初始值加 `ssh_use_sudo`；提交时先归一化（不适用
  时置 false）。仓库无 `@vue/test-utils`，组件渲染测试无现成基建，
  故测规则函数 + vue-tsc/build 把关，与既有前端测试风格一致。
- `devices.test.ts` 的 Device 构造 helper 补 `ssh_use_sudo: false`
  （否则 vue-tsc 报错）。

## 4. 版本号统一 0.2.12

照 v0.2.8 清单：`serverVersion`、`agentVersion`、m5/m7 测试期望值、
`web/package.json` + `package-lock.json`（顶层两处；tinyglobby 依赖
的 `^0.2.11` 不动）、SettingsView 文案、docker-compose（两镜像 tag +
首行注释）、Dockerfile、install.sh、README（标题 + 已知限制 + 镜像
pull 命令改为 0.2.12 + “首个镜像”过渡句改为通用表述）、docs/api.md、
docs/deployment.md、docs/security.md、迁移脚本 `AGENT_IMAGE` 回落值、
GitHub Actions 注释示例。历史注释（「v0.2.11：…」）一律不动。

## 5. 文档

- 新建本文档与 `docs/TEST-REPORT-v0.2.12.md`；根 CHANGELOG.md 顶部加
  v0.2.12 节。
- `docs/ssh-sleep.md`：标题加 sudo；数据模型加 010 与 `ssh_use_sudo`
  列；执行链路加 sudo 单次会话与前台语义；Linux 章节改前台执行 + 新增
  sudo 提权小节；限制一节修正两处过时表述——飞牛与 Windows SSH 关机
  均已于 2026-11-06 真机验证通过（飞牛当时靠 polkit 规则；v0.2.12
  的 sudo 是该规则的替代品，盒子实测时须先移开规则验证）。
- `docs/api.md`：设备字段加 `ssh_use_sudo` 说明（端点数仍 50 不变）。
- README 特性行加 sudo 提权与前台执行说明。

## 升级

- 解压后直接 `docker compose up -d --build` 即可（010 随 Server 启动
  自动应用，迁移前自动备份，数量核对 devices 不变才继续）。
- 盒子实例同样：解压新包后 `docker compose up -d --build`（盒子包
  另行出，本包为 VM/通用版）。
- 升级后：飞牛设备在表单勾选「使用 sudo 执行关机命令」即可；已有的
  polkit 规则可留着不用，或按需删除——**盒子实测 sudo 时须先把规则
  临时移开**（`sudo mv /etc/polkit-1/rules.d/49-wakehub-poweroff.rules
  /tmp/`），通过后再决定是否移回，否则规则会掩盖 sudo 是否真生效。
- 本版不改 Agent 注册/审批/Token 机制，不改 API 路径/方法/参数/响应
  格式（只新增可选字段），不删现有功能。
