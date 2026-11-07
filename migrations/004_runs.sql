-- 004: 定时任务运行记录（契约 §8 / 迁移文档 §7）
-- M4：task_runs 扩展 + 每设备执行结果表 task_run_devices + schedules.run_once。

-- task_runs 扩展：记录每次执行的开始/结束与耗时（执行历史查询用）。
ALTER TABLE task_runs ADD COLUMN started_at DATETIME;
ALTER TABLE task_runs ADD COLUMN finished_at DATETIME;
ALTER TABLE task_runs ADD COLUMN duration_ms INTEGER NOT NULL DEFAULT 0;

-- 每设备执行结果：一次 run 内每个目标设备的独立结果（契约 §8.3）。
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
CREATE INDEX IF NOT EXISTS idx_task_run_devices_run_id ON task_run_devices(run_id);

-- run_once：单次任务标记。首次执行后自动禁用（契约 §8.1/§8.2）。
ALTER TABLE schedules ADD COLUMN run_once INTEGER NOT NULL DEFAULT 0;
