-- 012: 删除定时任务时保留执行历史（v0.3.3 问题 11）
--
-- 背景：001 建 task_runs 时 schedule_id 外键为 ON DELETE CASCADE，
-- 删除任务会级联删除全部执行历史（含每设备行），且删除前历史已不可达。
-- 本迁移把外键改为 ON DELETE SET NULL（并放宽 NOT NULL，删任务后
-- 历史行保留、schedule_id 置 NULL）；task_run_devices 不动，其 run_id
-- 外键仍为 CASCADE（run 行删时设备行随之删，语义不变）。
--
-- SQLite 不支持原地改外键，按官方流程重建 task_runs 表。注意：
-- DROP TABLE task_runs 时，若 task_run_devices 的外键仍在，SQLite 会
-- 把 DROP 当成整表 DELETE 并级联删除全部每设备行（实测）——因此先把
-- task_run_devices 的行暂存到临时表、删表，重建 task_runs 后再原样
-- 恢复（含自增 id）。索引随旧表一并删除，须重建。
-- 全程无执行历史丢失；幂等（重跑时结构已是目标形态，各语句均为
-- IF NOT EXISTS / 按目标定义重建后数据原样）。

-- 暂存每设备行（临时表，随连接消失；先删防上次失败残留）。
DROP TABLE IF EXISTS _trd_012;
CREATE TEMP TABLE _trd_012 AS SELECT * FROM task_run_devices;
DROP TABLE task_run_devices;

-- 重建 task_runs：schedule_id 改为可空 + ON DELETE SET NULL。
DROP TABLE IF EXISTS task_runs_new;
CREATE TABLE task_runs_new (
    id TEXT PRIMARY KEY,
    schedule_id TEXT,
    status TEXT NOT NULL,
    output TEXT NOT NULL DEFAULT '',
    executed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at DATETIME,
    finished_at DATETIME,
    duration_ms INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (schedule_id) REFERENCES schedules(id) ON DELETE SET NULL
);
INSERT INTO task_runs_new (id, schedule_id, status, output, executed_at, started_at, finished_at, duration_ms)
    SELECT id, schedule_id, status, output, executed_at, started_at, finished_at, duration_ms FROM task_runs;
DROP TABLE task_runs;
ALTER TABLE task_runs_new RENAME TO task_runs;

-- 恢复每设备行表（定义与 004 逐字一致）与索引。
CREATE TABLE IF NOT EXISTS task_run_devices (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id TEXT NOT NULL,
    device_id TEXT NOT NULL,
    device_name TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'failed',
    reason TEXT NOT NULL DEFAULT '',
    sent_at DATETIME,
    boot_status TEXT,
    FOREIGN KEY (run_id) REFERENCES task_runs(id) ON DELETE CASCADE
);
INSERT INTO task_run_devices (id, run_id, device_id, device_name, status, reason, sent_at, boot_status)
    SELECT id, run_id, device_id, device_name, status, reason, sent_at, boot_status FROM _trd_012;
DROP TABLE _trd_012;
CREATE INDEX IF NOT EXISTS idx_task_run_devices_run_id ON task_run_devices(run_id);

-- task_runs 上的 schedule_id 索引随旧表删除，重建（005 原定义）。
CREATE INDEX IF NOT EXISTS idx_task_runs_schedule_id ON task_runs(schedule_id);
