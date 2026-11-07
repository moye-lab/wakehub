-- 007: SSH 关机适配器支持 Windows 目标（v0.2.6）。
-- ssh_os 区分目标系统：linux（默认）| windows；Windows 目标走
-- OpenSSH Server + cmd 关机命令（shutdown /s /t 1 等），不再依赖
-- Sleep-on-LAN exe。旧设备行默认 linux，与 v0.2.5 行为一致。
ALTER TABLE devices ADD COLUMN ssh_os TEXT NOT NULL DEFAULT 'linux';
