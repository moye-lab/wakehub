# 测试报告 — v0.2.12

> 验收时间：2026-11-06 晚。结论：**全部通过**，可交付。
> 本机沙箱跑不起 Docker 容器（与代码无关），容器内验证仍须在 VM/盒子做，
> 见 §6。sudo 对真实目标（飞牛）的验证须在盒子实测时做，见 §7。

## 1. 后端

- `go vet ./...`：干净。
- `gofmt -l`：干净（`internal/sleep`、`internal/device`、`internal/agent`、
  `internal/api`、`internal/db` 均已格式化）。
- `go test ./... -count=1`：**14 包全过**（api 21.4s、auth 15.7s）。
- `go test -race ./... -count=1`：**14 包全过、无数据竞争**（api 232.1s、
  auth 157.5s，race 下慢属正常）。

## 2. 前端

- `npm run typecheck`（vue-tsc）：通过。
- `npm run test`（Vitest）：**38/38**（v0.2.11 为 35/35；新增
  `sshSudoApplies` 3 例）。
- `npm run build`（vite）：通过。

## 3. 新增与改动的测试

### 3.1 sleep 包（sudo 单次会话 + 前台执行）

- `TestSSHPowerScript`：3 参调用；新增断言 Linux 脚本不含 `nohup`、
  sudo 脚本以 `sudo -S sh -c '` 开头、Windows 在 `useSudo=true` 时与
  false 逐字相同。
- `TestExecuteSSHShutdownCommand`：原断言含 `nohup` 改为含
  `systemctl poweroff`、`sleep 1` 且不含 `nohup`。
- 新增 6 个测试，全部通过：
  - `TestExecuteSSHSudoSuccess`：单次会话只发 1 条命令
    `sudo -S sh -c '...systemctl poweroff'`、stdin 为 `testpass\n`、
    密码不出现在命令行里、内层断电脚本实际执行（`powerRan`）。
  - `TestExecuteSSHSudoWrongPassword`：SSH 密码 ≠ sudo 密码时，错误含
    「sudo 校验失败」+「Sorry」，且内层断电脚本**没有**执行（`powerRan==false`）。
  - `TestExecuteSSHPoweroffFailureVisible`：powerExit=1 + stderr
    `Call to PowerOff failed: Interactive authentication required.` 时，
    错误含「退出码 1」+「Interactive authentication required」。
    **反向验证**：旧版 nohup 恒回 0，本测试在旧代码下会失败（新行为
    的证明）。另开 `chdirWarning`（模拟飞牛 sg 无家目录时 sshd 的
    chdir 警告）：错误回显须跳过该噪音行、显示真正的断电错误；撤掉
    过滤时本测试必失败（反向验证已跑）。
  - `TestExecuteSSHDropAfterDelayIsSuccess`：dropAfter=1500ms → 成功
    （目标正在断电，掐线是预期结果）。
  - `TestExecuteSSHEarlyDropIsFailure`：dropAfter=100ms → 失败且错误
    含「中断」。
  - `TestExecuteSSHWindowsIgnoresSudo`：windows + UseSudo → 只发 1 条
    cmd 命令、无 sudo 字样。

### 3.2 两个测试自身的 bug（已修，均在测试代码，不改产品）

1. **`TestSSHPowerScript` 的 `&` 断言过宽**：助手加了「Linux 脚本不应
   含 `&`」，但脚本里 `2>&1` 本就含 `&`。修法：只禁 `nohup` 与后台
   操作符形态 `" &"`。
2. **`TestExecuteSSHSudoWrongPassword` 抖动（约 1/5）**：错误为「sudo
   校验失败…: （无输出）」（stderr 丢了）。隔离调试（临时文件
   `zz_debug_test.go`，已删除）证明同一场景下 stderr 能抓到——根因是
   测试服务器发完 stderr + exit-status 后立刻 return、`defer
   channel.Close()` 关通道，客户端 copy 协程偶发还没把已缓冲的 stderr
   读走就被 EOF 截断（真实 sshd 不会立刻关通道，故仅测试服务器的问
   题，非产品代码）。修复：服务器在两处发 stderr 后（sudo 密码错分支、
   powerStderr 分支）加 `channel.CloseWrite()` + `Sleep(100ms)` 再
   return。复跑 `go test -count=10 -run 'TestExecuteSSH|TestSSHPowerScript'`
   全过；随后 sleep 包 `-count=1` 全过。

### 3.3 race 抓到的真 bug（产品代码，已修）

`go test -race` 在 `TestExecuteSSHPoweroffFailureVisible` 命中数据竞争：
`runSessionCmd` 把同一个 `bytes.Buffer` 同时挂给 `Stdout` 与 `Stderr`，
ssh 库用两个协程分别拷两路，并发写同一 Buffer。修法：stdout/stderr
分开两个 Buffer，合并时 stderr 在前（调用方只在失败时用首行做回显，
错误文本都在 stderr）。修后 sleep 包 `go test -race -count=3` 通过，
全量 race 14 包全过。

### 3.4 设备仓库与迁移

- `TestRepositorySSHUseSudoRoundTrip`（新增）：Linux + SSH 勾选 sudo
  创建后读回 true；切 windows 归零；切回 linux 但改 sleep_type=none
  归零；创建时 sleep_type=none 却带标记，创建即归零。
- `TestMigrateFreshDB`：版本期望 1–9 → 1–10，加 010 的
  `ssh_use_sudo` 列存在检查；`TestMigrateLegacy001DB` 与
  `TestMigrateIdempotent` 的版本期望同步改 1–10。
- `TestMigrate010SSHUseSudo`（新增）：001 旧库 + 一台老设备 → 迁移后
  列存在、默认值 0、老设备 `ssh_use_sudo=0`（老行为不变）。
- 三处测试 DB 搭建的迁移列表补 009 + 010（`internal/api/router_test.go`、
  `internal/api/m4_test.go`、`internal/scheduler/scheduler_test.go`；
  `internal/db/migrate_test.go` 里两个刻意造 v0.2.9 态的测试——
  `TestMigrateDeviceLossRefused` 与 `TestMigrate009RenamesSeededNetworks`——
  不动）。

### 3.5 API 端到端

- `TestSleepSSHPayloadCarriesUseSudo`（新增）：fake Agent 用自定义
  reply 捕获睡眠 payload 并直接回成功（不真连目标 SSH）。勾选 sudo
  的 Linux 设备下发 payload 含 `ssh_use_sudo: true`；未勾选为 false；
  部分更新 PUT `ssh_use_sudo: true` 后回显 true；再把目标系统切到
  windows，标记被归零。
- `internal/api/device_test.go` 的 `setupM3Server` 迁移列表补 009 + 010。

### 3.6 前端 helper

- `sshSudoApplies` 3 例：SSH+Linux 为 true；Windows 为 false；
  http_sol/none 为 false。

## 4. 升级冒烟（最终版二进制，真跑）

- 态库：v0.2.11 态（迁移 001–009 + 已获批 Agent subnet 10.0.0.0/24 +
  3 台设备：飞牛 SSH Linux、OpenWrt、Windows SSH）→ 最终版 server
  二进制启动。
- 迁移前备份生成（`.migrate-bak-*`）；日志「迁移后数量核对：
  devices 3→3，agents 1→1，networks 2→2」；版本表 1–10；
  老设备 `ssh_use_sudo` 全为 0；网络行保留（009 改名后的
  `10.0.0.0/24`）；health 报 **0.2.12**。
- 冒烟工具坑（与代码无关）：`WAKEHUB_MIGRATIONS_PATH` 要给**文件路径**
  （如 `.../001_init.sql`），目录由 `filepath.Dir` 推导；直接传目录会
  使迁移目录算错、迁移不跑（v0.2.10 报告 §5 已记）。

## 5. 未实测（须如实说明）

- **sudo 对真实飞牛的验证（2026-11-06 23:25 盒子实测，首轮失败，已修）**：
  盒子 0.2.12（两段式版）+ 规则已移开，点飞牛睡眠报「关机命令执行失败
  （退出码 1）: Could not chdir to home directory /home/sg: No such file
  or directory」。根因两点：① 两段式设计缺陷——`sudo -S -v` 通过后，
  另开会话的 `sudo -n` 认不到 sudo 时间戳（无 TTY 时时间戳不跨会话；
  也可能是目标 `timestamp_timeout=0`）；② chdir 警告（sg 无家目录，
  sshd 的会话噪音）盖住了真正的错误行。修法：改单次会话 `sudo -S`
  （校验与断电同一 sudo 调用，不依赖时间戳）+ `firstLine` 跳过 chdir
  噪音行（见 §3.1 与 CHANGELOG §1）。**单次会话版尚未在飞牛上重测**，
  待新包交付后重测通过再推仓库。
- **容器内验证**：沙箱跑不起 Docker（`runc setns: operation not
  permitted`），`docker compose config/build/up` 与容器内行为须在
  VM/盒子做。
- **Linux sleep/hibernate 的 sudo 路径**：只测了 shutdown 的协议级
  行为；sleep/hibernate 走同一条 `runPowerScript`，未单独真机测。
