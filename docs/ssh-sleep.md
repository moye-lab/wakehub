# SSH 关机适配器（v0.2.5；Windows 目标支持 v0.2.6 加入；sudo 提权 v0.2.12 加入）

目标上不装任何东西的远程关机：Agent 用 SSH 直连目标执行关机命令。
兑现契约 §6.1 里“Linux 本地关机适配器 v0.2.0 不新增”的延期项；
v0.2.6 把另一半（Windows）也兑现了。

## 为什么是 SSH

- 唤醒（WOL）有通用硬件包，什么都不用装；关机没有——必须让目标系统
  自己执行，系统得有个“听话”的入口。
- Linux 目标本来就有 SSH 这个入口（NAS、迷你主机、虚拟机、路由器等），不用
  再装监听服务；Windows 目标自带 OpenSSH Server（可选功能），打开后
  同样不用装第三方软件（Sleep-on-LAN exe 不再是 Windows 的必选项）。

## 数据模型（迁移 006 + 007 + 010）

devices 表新增六列：

| 列 | 含义 |
| --- | --- |
| `ssh_user` | SSH 用户名 |
| `ssh_port` | SSH 端口，默认 22 |
| `ssh_password_enc` | AES-256-GCM 密文（base64），明文永不落库 |
| `ssh_host_key_sha256` | TOFU 学到的主机密钥指纹（小写 hex） |
| `ssh_os` | 目标系统：`linux`（默认）\| `windows`（迁移 007） |
| `ssh_use_sudo` | 是否用 sudo 提权关机（迁移 010，v0.2.12；Linux 专用，默认 0） |

API 上的设备对象另有 `ssh_password`（写时字段：新建必填；编辑留空
表示保持原密码）与 `ssh_password_set`（只读：是否已存密码）。密文列
永不序列化返回（`json:"-"`）。

## 凭据加密（internal/secret）

- 算法：AES-256-GCM；密钥 = SHA-256(`"wakehub-ssh-v1" + 0x00 + SECRET_KEY`)，
  与会话签名等其他用途隔离；每次加密用随机 12 字节 nonce，
  密文格式为 base64(nonce || ciphertext)。
- 明文只在三个地方出现：用户在表单里输入的一瞬间、服务端内存里解密后
  下发给 Agent 的 payload、Agent 内存里建立 SSH 连接时。三个环节都不
  落盘、不进日志。
- `SECRET_KEY` 一旦更换，旧密文即无法解密——sleep 接口返回 400
  “SSH 密码无法解密，请重新填写并保存”，不得静默失败。

## TOFU 主机密钥

- 首次连接：Agent 记录观测到的主机密钥 SHA-256 指纹，随成功结果回传，
  服务端持久化到 `ssh_host_key_sha256`。
- 之后每次：Agent 校验观测指纹与已存指纹是否一致，不符即拒绝连接
  （防局域网中间人偷密码），错误提示用户重新保存密码。
- 改密码即清空指纹（重新信任主机）；离开 SSH 适配器即清空已存凭据与
  指纹（只保留用户名/端口，下次切回只需重填密码）。

## 执行链路

```
POST /api/v1/devices/{id}/sleep {"action": "shutdown"}
  → 校验（动作白名单 → sleep_type/ssh_user/IP/密码解密 → §4.4 操作链）
  → Hub.Dispatch("sleep", SleepPayload{adapter_type:"ssh", ssh_host, ssh_port,
      ssh_user, ssh_password, ssh_known_host_key, ssh_target_os, ssh_use_sudo, action})
  → Agent: sleep.ExecuteSSH
      1. 校验目标为私有 IPv4/回环（禁主机名、公网、169.254）
      2. SSH 握手：密码 + 键盘交互认证，TOFU 校验主机密钥
      3. 勾选 sudo（Linux 专用）时以单次会话 `sudo -S sh -c '<关机脚本>'`
         执行：密码经 stdin 一次喂给 sudo，校验与断电在同一个 sudo
         调用里完成，不依赖跨会话的时间戳；鉴权失败立即返回真实
         stderr、不发起断电
      4. 前台执行关机脚本（见下）：退出码 0 = 断电已发起；非 0 = 失败，
         错误带退出码与 stderr 首行回显；连接被掐断且已运行 ≥1 秒 = 成功
         （目标正在断电，sshd 随系统下线是预期结果）
  → 成功：Agent 回传观测指纹，服务端首次成功时持久化；审计 device_sleep
  → 失败：502，审计 device_sleep_failed
```

## 命令映射

### Linux 目标（v0.2.12 起前台执行）

脚本先在前台检查关机工具是否存在（缺失即刻以 exit 3 失败，不静默），
再**前台**执行关机命令。v0.2.11 及之前是 nohup 后台执行：目标机上
执行失败（如权限被拒）时 Agent 不知道、照样上报成功，界面显示
“关机指令已发送”但目标没关——“发了 ≠ 成了”。v0.2.12 改前台后，
失败可见：非零退出码带 stderr 首行回显；断电瞬间掐断 SSH 连接是
预期结果——命令已运行 ≥1 秒（脚本内 `sleep 1`）才被掐断时判成功，
过早掐断判失败。

| action | 前台检查 | 前台执行（延迟 1 秒后断电） |
| --- | --- | --- |
| `shutdown` | `systemctl` / `poweroff` / `shutdown` 任一存在 | `systemctl poweroff \|\| poweroff \|\| shutdown -h now` |
| `sleep` | `systemctl` 存在 | `systemctl suspend` |
| `hibernate` | `systemctl` 存在 | `systemctl hibernate` |

命令字符串由 `internal/sleep` 固定写死，不拼接任何外部输入。

#### sudo 提权（v0.2.12，设备表单“使用 sudo 执行关机命令”）

**什么时候勾选**：目标是飞牛这类 NAS——root 不让登录、普通用户又没
权限关机，直接点“睡眠”会报 `Interactive authentication required.`。
勾上这个开关就不用去目标机上写任何规则。

**怎么用**：编辑设备 → 关机适配器选 SSH、目标系统选 Linux → 勾选
「使用 sudo 执行关机命令」→ 保存。WakeHub 关机时用你已经存在设备里
的 SSH 密码去提权，目标机零配置。

**两个前提**（缺一不可）：
- SSH 用户本身有 sudo 权限（NAS 管理员账号一般都有）；
- SSH 密码与 sudo 密码是同一个（同一账号的登录密码）。

**失败会怎样**：密码错或没有 sudo 权限时，界面直接显示真实原因
（比如「sudo 校验失败」），不会假装成功；关机命令本身失败时也会
把目标机的真实错误显示出来。

**密码安全**：密码只通过 stdin 一次喂给 sudo，不出现在命令行、不进
进程列表、不进日志。只对 Linux 目标生效：Windows 目标忽略该开关；
切到 Windows 或离开 SSH 适配器时，标记会自动归零。

> 实现说明：关机命令是单次会话 `sudo -S sh -c '<关机脚本>'`，校验与
> 断电在同一个 sudo 调用里完成，不依赖跨会话的 sudo 时间戳（两段式
> 在飞牛上实测失败，见 `docs/TEST-REPORT-v0.2.12.md` §5）。错误回显
> 会跳过 sshd 的会话噪音行 "Could not chdir to home directory
> /home/<user>: ..."（用户无家目录时 sshd 先打，飞牛 sg 即如此），
> 只显示真正的错误行。

### Windows 目标（v0.2.6）

Windows OpenSSH 默认 shell 是 cmd.exe，脚本按 cmd 语法写。`shutdown
/s /t 1` 本身立即返回、1 秒后关机；`sleep`/`hibernate` 用 `start /b`
后台起、不等它完成就返回（同 Linux 一样的道理：不等断电就返回）。

| action | 前台检查 | 执行 |
| --- | --- | --- |
| `shutdown` | `where shutdown` 存在 | `shutdown /s /t 1` |
| `sleep` | `where rundll32` 存在 | `start "" /b rundll32.exe powrprof.dll,SetSuspendState 0,1,0` |
| `hibernate` | `where shutdown` 存在 | `start "" /b shutdown /h` |

Windows 目标开启方式（电脑上一次操作，管理员 PowerShell）：

```powershell
Add-WindowsCapability -Online -Name OpenSSH.Server~~~~0.0.1.0; Start-Service sshd; Set-Service -Name sshd -StartupType 'Automatic'
```

安装时自动放行防火墙；服务开机自启、无黑窗口。SSH 用 Windows 登录
账户的密码（不是 PIN——平时用 PIN 登录的，要用账户密码）。

## 限制（须如实告知用户）

- 仅手动关机：定时任务 `action` 仍只允许 `wake`，排不了定时关机。
- 目标系统：Linux 与 Windows（v0.2.6 起）；`ssh_os` 为其他值时校验
  拒绝。Windows 目标须开启 OpenSSH Server（见上），未开启时 Agent
  拨号失败回 502，不会静默“成功”。
- 密码经 server→Agent 的 WS 通道下发：同机/同局域网部署时该通道在
  可信网络内；跨不可信网络部署 Agent 时须用 `wss://`。
- 返回成功表示“关机命令已被目标接受并开始执行”（前台退出码 0，或
  已运行 ≥1 秒后连接被断电掐断），不等同于目标已彻底断电；仪表盘上
  设备随后会变“未知”而非“离线”（TCP 端口关闭 ≠ 关机）。v0.2.12
  之前是 nohup 后台执行，失败也报成功；v0.2.12 起失败可见（见上）。
- 飞牛 NAS SSH 关机已真机验证（2026-11-06）：盒子 WakeHub 点睡眠
  成功关掉飞牛。注意当时是靠 polkit 规则放行（`/etc/polkit-1/rules.d/
  49-wakehub-poweroff.rules`，只放行 sg）；v0.2.12 的 sudo 模式是
  该规则的替代品——盒子实测时应先把规则临时移开，确认 sudo 真能
  替代后再决定是否保留规则。
- Windows SSH 关机已真机验证（2026-11-06 16:20）：点「睡眠」→ 电脑
  关机 → 再点唤醒 → 电脑回来。
