-- 005: 索引与外键（契约附录 B / 迁移文档 §7，M5）
--
-- 补索引：wake_tasks.device_id、task_runs.schedule_id、schedules.target_id、
-- audit_logs.actor、devices.mac 唯一索引（MAC 唯一约束落在此处，M3 先在
-- 验证层强制；遗留重复 MAC 在此去重，保留最早创建的一行）。
--
-- 补外键：仅 devices.group_id → device_groups(id)（SQLite 无法 ALTER TABLE
-- 加外键，须按官方 12 步重建表；本文件在同一事务内完成重建）。
-- 以下两处外键**有意不建**，原因写明，避免后人误以为遗漏：
--   1. devices.agent_id → agents(id)：契约 §4.6 规定遗留设备保留
--      agent_id='local'/''（003 只删种子行、不改写设备行），这些行必然
--      违反外键；Agent 存在性由 M3 的统一验证层在写入时强制。
--   2. schedules.target_id：多态目标（target_type='device' 时指向 devices，
--      ='group' 时指向 device_groups），单个外键无法表达；目标存在性由
--      scheduler.ValidateSchedule 在创建/更新时强制。
-- 既有孤儿数据处理：group_id 不在 device_groups 中的行先归入 'default'
-- 分组（分组无删除接口，正常不应出现；防御性处理）。

-- 遗留重复 MAC 去重：每组 MAC 只保留 rowid 最小（最早创建）的一行。
DELETE FROM devices WHERE rowid NOT IN (
    SELECT MIN(rowid) FROM devices GROUP BY mac
);

-- 保证默认分组存在，再清理孤儿 group_id。
INSERT OR IGNORE INTO device_groups (id, name, description)
    VALUES ('default', '默认分组', '系统默认设备分组');
UPDATE devices SET group_id = 'default'
    WHERE group_id NOT IN (SELECT id FROM device_groups);

-- 重建 devices 表，补 group_id 外键（其余列定义与 001 逐字一致）。
CREATE TABLE devices_new (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    mac TEXT NOT NULL,
    ip TEXT NOT NULL DEFAULT '',
    agent_id TEXT NOT NULL DEFAULT 'local',
    broadcast_address TEXT NOT NULL DEFAULT '255.255.255.255',
    wol_port INTEGER NOT NULL DEFAULT 9,
    device_type TEXT NOT NULL DEFAULT 'pc',
    status_method TEXT NOT NULL DEFAULT 'ping',
    status_port INTEGER NOT NULL DEFAULT 22,
    status TEXT NOT NULL DEFAULT 'unknown',
    last_checked_at DATETIME,
    last_status_error TEXT NOT NULL DEFAULT '',
    group_id TEXT NOT NULL DEFAULT 'default',
    enabled INTEGER NOT NULL DEFAULT 1,
    sleep_type TEXT NOT NULL DEFAULT 'none',
    sleep_endpoint TEXT NOT NULL DEFAULT '',
    remark TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (group_id) REFERENCES device_groups(id)
);
INSERT INTO devices_new (
    id, name, mac, ip, agent_id, broadcast_address, wol_port, device_type,
    status_method, status_port, status, last_checked_at, last_status_error,
    group_id, enabled, sleep_type, sleep_endpoint, remark, created_at, updated_at
) SELECT
    id, name, mac, ip, agent_id, broadcast_address, wol_port, device_type,
    status_method, status_port, status, last_checked_at, last_status_error,
    group_id, enabled, sleep_type, sleep_endpoint, remark, created_at, updated_at
FROM devices;
DROP TABLE devices;
ALTER TABLE devices_new RENAME TO devices;

-- 重建后索引须重建（随旧表一并删除）；另加 MAC 唯一索引。
CREATE INDEX IF NOT EXISTS idx_devices_agent_id ON devices(agent_id);
CREATE INDEX IF NOT EXISTS idx_devices_group_id ON devices(group_id);
CREATE INDEX IF NOT EXISTS idx_devices_mac ON devices(mac);
CREATE UNIQUE INDEX IF NOT EXISTS idx_devices_mac_unique ON devices(mac);

-- 其余索引。
CREATE INDEX IF NOT EXISTS idx_wake_tasks_device_id ON wake_tasks(device_id);
CREATE INDEX IF NOT EXISTS idx_task_runs_schedule_id ON task_runs(schedule_id);
CREATE INDEX IF NOT EXISTS idx_schedules_target_id ON schedules(target_id);
CREATE INDEX IF NOT EXISTS idx_audit_logs_actor ON audit_logs(actor);
