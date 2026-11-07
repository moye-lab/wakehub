-- migrations/008_network_identity.sql
-- 目的：设备 MAC 唯一范围从全局收窄到“网络内唯一”（v0.2.9 问题 2）。
-- 原则：历史设备一个不少；已发布的 005 不动，本文件只做加法。
--
-- networks：id / name / cidr / description / created_at / updated_at。
-- agent_networks：agent_id ↔ network_id（一个 Agent 可管理多个网络）。
--
-- 回填规则：
--   * 默认网络 'default'（cidr 为空），全部历史设备初始归属默认网络；
--   * 每个已有 Agent 的注册网段（agents.subnet 非空）建一个网络行，
--     id 由 CIDR 派生（net-192-168-1-0-24），并关联到该 Agent；
--   * 设备归属：agent 有专属网络的 → 该网络；其余 → 'default'。
--   * 全程无 DELETE，不删除任何设备 / Agent。
--
-- 迁移后验证（见 docs/MIGRATION-v0.2.9.md）：
--   SELECT COUNT(*) FROM devices;  -- 迁移前后必须相等
--   SELECT network_id, COUNT(*) FROM devices GROUP BY network_id;
--   SELECT mac, COUNT(*) FROM devices GROUP BY network_id, mac HAVING COUNT(*) > 1;
--   -- 最后一句必须返回 0 行（同一网络内无重复 MAC）。

CREATE TABLE IF NOT EXISTS networks (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    cidr        TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS agent_networks (
    agent_id   TEXT NOT NULL,
    network_id TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (agent_id, network_id),
    FOREIGN KEY (agent_id) REFERENCES agents(id) ON DELETE CASCADE,
    FOREIGN KEY (network_id) REFERENCES networks(id) ON DELETE CASCADE
);

-- 默认网络：全部历史设备初始归属。
INSERT OR IGNORE INTO networks (id, name, cidr, description)
    VALUES ('default', '默认网络', '', '迁移保留：全部历史设备初始归属');

-- 每个已有 Agent 的注册网段建一个网络行（id 由 CIDR 派生，确定性命名）。
INSERT OR IGNORE INTO networks (id, name, cidr, description)
    SELECT 'net-' || replace(replace(subnet, '.', '-'), '/', '-'),
           '网络 ' || subnet, subnet, '由 Agent 注册网段自动建立'
    FROM agents WHERE subnet <> '';

-- Agent 与其网段网络的归属关系。
INSERT OR IGNORE INTO agent_networks (agent_id, network_id)
    SELECT id, 'net-' || replace(replace(subnet, '.', '-'), '/', '-')
    FROM agents WHERE subnet <> '';

-- devices 加 network_id 列（默认 'default'）。
ALTER TABLE devices ADD COLUMN network_id TEXT NOT NULL DEFAULT 'default';

-- 回填：agent 有专属网络的设备归属该网络；其余留在默认网络。
UPDATE devices SET network_id = COALESCE(
    (SELECT 'net-' || replace(replace(a.subnet, '.', '-'), '/', '-')
     FROM agents a WHERE a.id = devices.agent_id AND a.subnet <> ''),
    'default');

-- 全局 MAC 唯一索引改为网络内唯一；旧索引删除后重建。
DROP INDEX IF EXISTS idx_devices_mac_unique;
CREATE UNIQUE INDEX IF NOT EXISTS idx_devices_network_mac_unique ON devices(network_id, mac);
CREATE INDEX IF NOT EXISTS idx_devices_network_id ON devices(network_id);
