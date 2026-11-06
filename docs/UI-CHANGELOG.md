# UI Changelog — v0.2.9（第一期）

- 设备列表加「网络」筛选下拉（在 Agent 筛选后）与「网络」列（桌面表格 Agent 列后、手机卡片 Agent/检测行后），显示网络名 + 网段小字。
- 设备表单「执行 Agent」后加「所属网络」选择，默认「自动（跟随 Agent）」；不填时服务端按 Agent 的首个管理网络归属，显式选「默认网络」仍可手动指定。
- Agents 页每张卡片加「管理网络」一行（只读 chips：网络名 + 网段），未分配时显示「未分配」。
- 其余继承 v0.2.8。

# UI Changelog — v0.2.8

- 仪表盘快捷唤醒按钮文案按状态分流：真在唤醒时“发送中…”，只是在刷新状态（切页自动检测）时“刷新中…”，平时“⚡ 唤醒”；刷新期间唤醒按钮仍可点。根因是检测与唤醒曾共用 `busyIds`。
- 设备页“检测”按钮禁用条件同步加上 `checkingIds`（检测进行中变灰，防重复点）。
- 其余继承 v0.2.7。

# UI Changelog — v0.2.7

- 设备表单关机适配器下拉“SSH 关机（Linux）”改名为“SSH 关机（Linux / Windows）”。
- 其余继承 v0.2.6。

# UI Changelog — v0.2.6

- 设备表单 SSH 一节加“目标系统”选择（Linux / Windows）；选 Windows 时提示改为“目标须开启 OpenSSH Server，用 Windows 登录密码”。
- 其余继承 v0.2.5。

# UI Changelog — v0.2.5

- 设备表单“远程关机 / 睡眠”适配器下拉增加“SSH 关机（Linux）”；选 SSH 时显示 SSH 用户 *、SSH 密码（编辑留空表示保持原密码）、SSH 端口，附提示“密码只存加密后的密文；改密码后需重新信任主机；目标须为 Linux 且 SSH 已开启”。
- 设备页“睡眠”按钮（桌面表格与手机 ••• 菜单）对 `ssh` 适配器同样出现。
- 其余继承 v0.2.4。

# UI Changelog — v0.2.4

- 移动端顶栏页标题改副标题样式（13px / 500 / 次要色），不再与 WakeHub 同字同粗；根因是 v0.2.1-ui 合并时丢了 v0.2.0 的移动端标题隐藏规则。
- 移动端顶栏去掉运行时间（只留“服务正常”）；PC 端顶栏仍保留运行时间。
- 其余继承 v0.2.3。

# UI Changelog — v0.2.3

- 移动端顶栏填空：手机上图标旁恢复显示 "WakeHub" 字标，状态旁追加运行时间（"服务正常 · 21 分钟"）；桌面端顶栏不变。
- 其余继承 v0.2.2（方案 A 深色 Slate Pro）。

# UI Changelog — v0.2.2

- 方案 A（Slate Pro 深色运维风）：深底色 `#0b1120` + 蓝色主按钮 + 在线呼吸灯；默认主题改为深色。
- 仪表盘统计改为紧凑胶囊（桌面一行 6 项、手机 3 列 × 2 行），顶部不再用大卡片。
- 移除仪表盘“欢迎回来”大标题与手动刷新按钮（恢复 V-N7：自动检测已覆盖手动刷新）。
- 版本号统一为 0.2.2（服务端与 Agent 此前仍报 0.2.0）。
- 其余继承 v0.2.1：固定桌面侧栏、移动端底部导航、移动端设备卡片、浅色/深色/跟随系统三档主题；后端、API、数据库、认证契约无变化。

# UI Changelog — v0.2.1

- Reworked the authenticated application shell with a compact professional SaaS layout, fixed desktop sidebar, status-aware top bar, and mobile bottom navigation.
- Added explicit light, dark, and system theme modes while preserving local-only theme preference storage.
- Introduced shared CSS design tokens for surfaces, text, borders, shadows, buttons, and form controls.
- Reorganized the dashboard into concise status metrics, quick wake cards, upcoming tasks, and recent activity using existing API/store data.
- Added a dedicated mobile device-card presentation while retaining the desktop inventory table and existing device actions.
- Added a three-way theme selector to Settings.
- No backend, API, database, or authentication contract changes were intended.

## Validation note

Build/typecheck/test execution must be completed in an environment where the locked npm dependencies are available. This working environment could not complete `npm ci` because package registry access timed out; do not treat frontend build or test status as passed until rerun.
