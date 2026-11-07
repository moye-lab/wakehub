-- 011: 网络 CIDR 标准化与唯一约束（v0.3.3 问题 6）
--
-- 背景：CreateNetwork 只做字符串精确匹配去重，192.168.1.1/24 与
-- 192.168.1.0/24 会被当成两个网络；库里也无 networks(cidr) 唯一索引。
-- 本迁移：
--   1. 把已有非空 CIDR 全部规范化为掩码后的标准写法（008 种子行用的是
--      Agent 的原始 subnet 字符串，同样在此规范化）；
--   2. 对规范化后等价的重复行去重：只保留最早创建的一行（created_at
--      相同取 rowid 最小者），devices 与 agent_networks 的引用改指保留行；
--   3. 建部分唯一索引（只约束非空 CIDR；默认网络与未指定网段的网络
--      cidr 为空，允许多行）。
-- 全程无设备删除；幂等（重跑时规范化与去重均为 no-op，索引 IF NOT EXISTS）。
--
-- SQLite 没有 CIDR 函数，掩码用整数位运算算出（经 sqlite3 实测：
-- /0、/8、/12、/24、/25、/32 均正确）。只有形如数字点分十进制/前缀
-- 的行才参与规范化，其余非法遗留行保持原值（写路径已校验 CIDR 合法，
-- 正常不应出现）。

-- 1. 规范化：先算出每行的标准写法，再写回。
CREATE TEMP TABLE IF NOT EXISTS net_norm (id TEXT PRIMARY KEY, cidr TEXT NOT NULL);
DELETE FROM net_norm;
INSERT INTO net_norm (id, cidr)
WITH
split AS (
    SELECT id,
        substr(cidr, 1, instr(cidr, '/') - 1) AS ip,
        CAST(substr(cidr, instr(cidr, '/') + 1) AS INTEGER) AS p
    FROM networks
    WHERE cidr <> ''
      AND cidr GLOB '[0-9]*.[0-9]*.[0-9]*.[0-9]*/*'
      AND CAST(substr(cidr, instr(cidr, '/') + 1) AS INTEGER) BETWEEN 0 AND 32
),
o1 AS (
    SELECT id, ip, p,
        CAST(substr(ip, 1, instr(ip, '.') - 1) AS INTEGER) AS a,
        substr(ip, instr(ip, '.') + 1) AS rest1
    FROM split
),
o2 AS (
    SELECT id, p, a,
        CAST(substr(rest1, 1, instr(rest1, '.') - 1) AS INTEGER) AS b,
        substr(rest1, instr(rest1, '.') + 1) AS rest2
    FROM o1
),
o3 AS (
    SELECT id, p, a, b,
        CAST(substr(rest2, 1, instr(rest2, '.') - 1) AS INTEGER) AS c,
        CAST(substr(rest2, instr(rest2, '.') + 1) AS INTEGER) AS d
    FROM o2
),
norm AS (
    SELECT id, p,
        ((((a * 256 + b) * 256 + c) * 256 + d) >> (32 - p)) << (32 - p) AS masked
    FROM o3
)
SELECT id,
    ((masked >> 24) & 255) || '.' || ((masked >> 16) & 255) || '.' ||
    ((masked >> 8) & 255) || '.' || (masked & 255) || '/' || p
FROM norm;
UPDATE networks SET cidr = (SELECT cidr FROM net_norm WHERE net_norm.id = networks.id)
WHERE id IN (SELECT id FROM net_norm);
DROP TABLE net_norm;

-- 2. 去重：规范化后等价的行只保留最早创建的一行。
CREATE TEMP TABLE IF NOT EXISTS net_dupes (dup_id TEXT PRIMARY KEY, keep_id TEXT NOT NULL);
DELETE FROM net_dupes;
INSERT INTO net_dupes (dup_id, keep_id)
SELECT n.id,
       (SELECT k.id FROM networks k WHERE k.cidr = n.cidr
         ORDER BY k.created_at ASC, k.rowid ASC LIMIT 1)
FROM networks n
WHERE n.cidr <> ''
  AND n.id <> (SELECT k.id FROM networks k WHERE k.cidr = n.cidr
                ORDER BY k.created_at ASC, k.rowid ASC LIMIT 1);
-- 先删已管理保留行的重复关联（否则改指会撞 agent_networks 主键）。
DELETE FROM agent_networks
WHERE network_id IN (SELECT dup_id FROM net_dupes)
  AND EXISTS (
    SELECT 1 FROM agent_networks a2
    WHERE a2.agent_id = agent_networks.agent_id
      AND a2.network_id = (SELECT keep_id FROM net_dupes WHERE dup_id = agent_networks.network_id)
  );
-- 其余引用改指保留行；同一 Agent 管理两个重复行时，UPDATE OR IGNORE
-- 只改一行，另一行由下一步清理。
UPDATE OR IGNORE agent_networks SET network_id =
    (SELECT keep_id FROM net_dupes WHERE dup_id = agent_networks.network_id)
WHERE network_id IN (SELECT dup_id FROM net_dupes);
DELETE FROM agent_networks WHERE network_id IN (SELECT dup_id FROM net_dupes);
UPDATE devices SET network_id =
    (SELECT keep_id FROM net_dupes WHERE dup_id = devices.network_id)
WHERE network_id IN (SELECT dup_id FROM net_dupes);
DELETE FROM networks WHERE id IN (SELECT dup_id FROM net_dupes);
DROP TABLE net_dupes;

-- 3. 部分唯一索引：只约束非空 CIDR。
CREATE UNIQUE INDEX IF NOT EXISTS idx_networks_cidr_unique ON networks(cidr) WHERE cidr <> '';
