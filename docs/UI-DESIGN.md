# WakeHub UI Design — v0.2.9（第一期）

## v0.2.9 方向（2026-11-06 网络归属）

- 设备列表新增「网络」列与筛选：MAC 唯一范围改为网络内，列表必须让人一眼看出每台设备属于哪个网络；网络列放 Agent 列后（设备—Agent—网络的归属链），手机卡片同样在 Agent 行后加一行。
- 设备表单「所属网络」默认「自动（跟随 Agent）」：单网段部署下用户不填就是对的，显式选网络只在多网络时才需要；表单不替用户猜网络，服务端按 Agent 归属。
- Agents 页每张卡片展示该 Agent 管理的网络（只读 chips）：分配走 API，卡片只展示，不在卡片上放编辑入口。
- 其余继承 v0.2.8。

# WakeHub UI Design — v0.2.8

## v0.2.8 方向（2026-11-06 切页“发送中”误会）

- 按钮文案必须说真话：检测只是读状态，不能写成“发送中”。唤醒进行中才叫“发送中…”，状态刷新进行中叫“刷新中…”，两者优先级唤醒高于刷新。
- 刷新期间不锁唤醒按钮：检测是只读的，用户切页后想立刻唤醒不该被拦住。
- 其余继承 v0.2.7。

# WakeHub UI Design — v0.2.7

## v0.2.7 方向（2026-11-06 适配器标签）

- 设备表单关机适配器“SSH 关机（Linux）”改名为“SSH 关机（Linux / Windows）”：适配器本身只有一个 SSH，系统是在下面的“目标系统”里选的，单独加一项会变成两个入口管同一个东西；改名最干净。
- 其余继承 v0.2.6。

# WakeHub UI Design — v0.2.5

## v0.2.5 方向（2026-11-06 SSH 关机适配器）

- 设备表单关机适配器新增“SSH 关机（Linux）”选项；SSH 配置组沿用现有
  fieldset 样式（用户 *、密码、端口三栏），不另开页面。
- “睡眠”按钮对 `ssh` 适配器同样出现（桌面表格与手机 ••• 菜单），行为
  与 http_sol 一致（点后确认框，实际发 shutdown）。
- 其余继承 v0.2.4。

# WakeHub UI Design — v0.2.4

## v0.2.4 方向（2026-11-06 用户反馈）

- 移动端顶栏页标题为副标题样式（13px / 500 / 次要色），与 WakeHub 主标题拉开层级；桌面端顶栏不变。
- 移动端顶栏不显示运行时间（只留“服务正常”）；PC 端顶栏仍保留运行时间。
- 其余继承 v0.2.3。

# WakeHub UI Design — v0.2.3

## v0.2.3 方向（2026-11-06 用户反馈）

- 移动端顶栏填空：手机上图标旁显示 "WakeHub" 字标，状态旁追加运行时间（"服务正常 · 21 分钟"）；桌面端顶栏不变。
- 其余继承 v0.2.2（方案 A）。

# WakeHub UI Design — v0.2.2（方案 A）

## v0.2.2 方向（2026-11-06 用户定）

- Visual direction: 方案 A Slate Pro 深空运维风，深色打底（`#0b1120`），蓝色主按钮 `#2563eb`，在线状态呼吸灯。
- 默认主题：深色（浅色/跟随系统仍可切换，偏好只存本地 `wakehub_theme`）。
- 仪表盘统计：紧凑胶囊，桌面一行 6 项、手机 3 列 × 2 行；不再用大卡片（v0.2.1 用户反馈顶部卡片占位过大）。
- 仪表盘无欢迎大标题、无手动刷新按钮（V-N7：自动检测已覆盖）。
- 其余继承 v0.2.1 的壳结构（桌面侧栏、移动端底部导航、移动端设备卡片）与功能约束（不改 API/认证/数据库、不造假数据、sent ≠ booted）。

# WakeHub UI Design — v0.2.1

## Direction

- Visual direction: professional blue/white SaaS console (方案 B).
- Density: compact, with readable typography and touch targets preserved.
- Theme modes: light, dark, and follow system. Preference is stored locally as `wakehub_theme`; authentication credentials are not stored there.
- Desktop: fixed navigation rail, sticky top bar, constrained content canvas.
- Mobile: dedicated bottom navigation with safe-area padding; device inventory switches from a wide table to cards.

## Design tokens

Light: background `#f4f7fb`, surface `#ffffff`, text `#172033`, muted `#66758b`, border `#e4eaf2`, primary `#2563eb`.

Dark: background `#0b1220`, surface `#111c2e`, text `#e7edf7`, muted `#a0aec2`, border `#26364d`, primary `#4b8bff`.

Shared components consume CSS variables in `src/style.css`. Keep status colors semantic: green=online/success, red=offline/error, amber=unknown/warning. Status must always include text, not color alone.

## Responsive targets

- Mobile: 320, 375, 390, 430 CSS px.
- Tablet: 768–1100 CSS px.
- Desktop: 1366, 1440, 1920 CSS px.
- Mobile touch targets: minimum 44px where practical; form controls remain at least 16px to avoid iOS focus zoom.
- Avoid page-level horizontal scrolling. Tables may remain desktop-only where a mobile card representation exists.

## Functional constraints

This UI update must not change server API routes, HTTP methods, request/response schemas, authentication semantics, database schema, or existing business operations. Never replace live data with mock values. Wake packet delivery and device boot verification remain distinct states.
