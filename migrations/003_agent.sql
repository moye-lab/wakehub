-- migrations/003_agent.sql
-- 目的：Agent 审批状态机、per-agent token 扩展、扫描白名单、种子 local 行退役。
-- 原则：只加列、只建表；唯一删除性操作是末尾的种子行删除（见 §3.3）。
-- 对应契约 §3、迁移文档 docs/migration-002-003.md §3。

-- 审批状态：pending | approved | disabled（契约 §3.2）。
-- 新增列默认 'pending'，随后按既有 authorized 回填，再删除种子行。
ALTER TABLE agents ADD COLUMN approval_status TEXT NOT NULL DEFAULT 'pending'
    CHECK (approval_status IN ('pending', 'approved', 'disabled'));

-- 握手上报的节点信息（契约 §3.4/§3.5）。
ALTER TABLE agents ADD COLUMN ip   TEXT NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN os   TEXT NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN arch TEXT NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN last_heartbeat_at DATETIME;

-- 回填：既有 authorized=1 的行视为已获批（v0.1.0 注册即授信，全部既有行均为 authorized=1，
-- 故实际效果是全部既有 Agent 回填为 approved；authorized=0 的行（若存在）回填为 pending）。
UPDATE agents
    SET approval_status = CASE WHEN authorized = 1 THEN 'approved' ELSE 'pending' END;

-- 种子 local 行退役（契约 §3.6）：删除后其 agent_tokens 行由 FK 级联删除。
-- 注意：001 新装也会先建出该行，再由本语句删除（001 冻结的代价）。
DELETE FROM agents WHERE id = 'local';

-- per-agent token 扩展（契约 §3.3）。agent_tokens 表本身不动结构（id/agent_id/token_hash/
-- revoked/created_at 保持），只加管理列；UNIQUE(agent_id) 保持不变（每 Agent 同时一个有效 token）。
ALTER TABLE agent_tokens ADD COLUMN label       TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_tokens ADD COLUMN last_used_at DATETIME;
ALTER TABLE agent_tokens ADD COLUMN expires_at   DATETIME;
ALTER TABLE agent_tokens ADD COLUMN rotated_at   DATETIME;

-- 扫描白名单（契约 §7.2）：每 Agent 允许扫描的网段。
CREATE TABLE IF NOT EXISTS agent_allowed_subnets (
    agent_id   TEXT NOT NULL,
    cidr       TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (agent_id, cidr),
    FOREIGN KEY (agent_id) REFERENCES agents(id) ON DELETE CASCADE
);

-- 回填：把各 Agent 注册时上报的 subnet 写入白名单（种子 local 行已删，不会进入）。
INSERT OR IGNORE INTO agent_allowed_subnets (agent_id, cidr)
    SELECT id, subnet FROM agents WHERE subnet <> '';
