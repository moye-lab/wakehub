-- migrations/002_auth.sql
-- 目的：会话可撤销（jti）与改密后全部会话失效（password_changed_at）。
-- 原则：只加列、只建表；不改现有列、不删数据。
-- 依据：docs/migration-002-003.md §2（M1 实施）。

-- 密码修改时间：NULL = 从未修改（老用户初始态，不拒绝任何会话）；
-- 改密时置为当前时间；会话校验要求 token.iAT >= 该值（契约 §2.3）。
ALTER TABLE users ADD COLUMN password_changed_at DATETIME;

-- 会话撤销表：logout 单会话撤销、审计定位用。
-- jti 为 token payload 中的会话唯一 ID（UUID v4）；
-- expires_at 用于定期清理（过期行由清理任务删除，与 token TTL 一致为 24h）。
CREATE TABLE IF NOT EXISTS revoked_tokens (
    jti        TEXT PRIMARY KEY,
    username   TEXT NOT NULL,
    revoked_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at DATETIME NOT NULL,
    reason     TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_revoked_tokens_expires_at
    ON revoked_tokens(expires_at);
