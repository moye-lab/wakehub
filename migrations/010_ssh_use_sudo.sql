-- v0.2.12：SSH 关机支持 sudo 提权。
--
-- 背景：飞牛等 NAS 默认禁 root SSH，且普通用户无权关机（systemd/logind
-- 要求交互确认），旧版 SSH 适配器直跑 systemctl poweroff 会静默失败，
-- 界面还显示“已发送”。v0.2.12 起设备可勾选“使用 sudo 关机”：Agent 先
-- 用 sudo -S -v 以已存 SSH 密码校验并缓存 sudo 时间戳，再 sudo -n 前台
-- 执行关机命令，目标机无需预先写 polkit 规则或改 sudoers，装即用。
-- 本迁移只加一列，默认 0（不启用），老设备行为不变。
-- 幂等：列已存在时重复执行由迁移框架的版本表挡住，不会重复加列。
ALTER TABLE devices ADD COLUMN ssh_use_sudo INTEGER NOT NULL DEFAULT 0;
