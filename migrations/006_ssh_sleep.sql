-- 006: SSH 关机适配器（契约 §6.3，v0.2.5）。
-- Linux 目标不用装监听服务，Agent 用 SSH 直连执行关机命令。
-- ssh_password_enc 存 AES-256-GCM 密文（密钥由服务端 SECRET_KEY 派生，
-- 见 internal/secret），明文永不落库；ssh_host_key_sha256 存 TOFU
-- 学到的主机密钥指纹（小写 hex），改密码即清空重新信任。
ALTER TABLE devices ADD COLUMN ssh_user TEXT NOT NULL DEFAULT '';
ALTER TABLE devices ADD COLUMN ssh_port INTEGER NOT NULL DEFAULT 22;
ALTER TABLE devices ADD COLUMN ssh_password_enc TEXT NOT NULL DEFAULT '';
ALTER TABLE devices ADD COLUMN ssh_host_key_sha256 TEXT NOT NULL DEFAULT '';
