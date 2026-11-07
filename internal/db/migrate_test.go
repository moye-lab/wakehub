package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

const migrationsDir = "../../migrations"

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "test.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func readMigration(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(migrationsDir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

func TestMigrateFreshDB(t *testing.T) {
	db := openTestDB(t)
	if err := Migrate(db, migrationsDir); err != nil {
		t.Fatalf("Migrate 全新库失败: %v", err)
	}
	versions, err := AppliedVersions(db)
	if err != nil {
		t.Fatalf("AppliedVersions: %v", err)
	}
	want := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	if len(versions) != len(want) {
		t.Fatalf("版本应为 %v，实际 %v", want, versions)
	}
	for i, v := range want {
		if versions[i] != v {
			t.Fatalf("版本应为 %v，实际 %v", want, versions)
		}
	}
	// 各迁移的关键产物须存在。
	checks := []struct{ q, what string }{
		{`SELECT COUNT(1) FROM sqlite_master WHERE type='table' AND name='revoked_tokens'`, "002 revoked_tokens 表"},
		{`SELECT COUNT(1) FROM pragma_table_info('agents') WHERE name='approval_status'`, "003 approval_status 列"},
		{`SELECT COUNT(1) FROM sqlite_master WHERE type='table' AND name='agent_allowed_subnets'`, "003 agent_allowed_subnets 表"},
		{`SELECT COUNT(1) FROM sqlite_master WHERE type='table' AND name='task_run_devices'`, "004 task_run_devices 表"},
		{`SELECT COUNT(1) FROM pragma_table_info('schedules') WHERE name='run_once'`, "004 run_once 列"},
		{`SELECT COUNT(1) FROM sqlite_master WHERE type='index' AND name='idx_wake_tasks_device_id'`, "005 wake_tasks 索引"},
		{`SELECT COUNT(1) FROM sqlite_master WHERE type='index' AND name='idx_audit_logs_actor'`, "005 audit_logs 索引"},
		{`SELECT COUNT(1) FROM pragma_table_info('devices') WHERE name='ssh_user'`, "006 ssh_user 列"},
		{`SELECT COUNT(1) FROM pragma_table_info('devices') WHERE name='ssh_port'`, "006 ssh_port 列"},
		{`SELECT COUNT(1) FROM pragma_table_info('devices') WHERE name='ssh_password_enc'`, "006 ssh_password_enc 列"},
		{`SELECT COUNT(1) FROM pragma_table_info('devices') WHERE name='ssh_host_key_sha256'`, "006 ssh_host_key_sha256 列"},
		{`SELECT COUNT(1) FROM pragma_table_info('devices') WHERE name='ssh_os'`, "007 ssh_os 列"},
		{`SELECT COUNT(1) FROM sqlite_master WHERE type='table' AND name='networks'`, "008 networks 表"},
		{`SELECT COUNT(1) FROM sqlite_master WHERE type='table' AND name='agent_networks'`, "008 agent_networks 表"},
		{`SELECT COUNT(1) FROM pragma_table_info('devices') WHERE name='network_id'`, "008 network_id 列"},
		{`SELECT COUNT(1) FROM sqlite_master WHERE type='index' AND name='idx_devices_network_mac_unique'`, "008 网络内 MAC 唯一索引"},
		{`SELECT COUNT(1) FROM pragma_table_info('devices') WHERE name='ssh_use_sudo'`, "010 ssh_use_sudo 列"},
		{`SELECT COUNT(1) FROM sqlite_master WHERE type='index' AND name='idx_networks_cidr_unique'`, "011 networks(cidr) 唯一索引"},
		{`SELECT COUNT(1) FROM sqlite_master WHERE type='index' AND name='idx_task_runs_schedule_id'`, "012 重建 task_runs 后 schedule_id 索引"},
		{`SELECT COUNT(1) FROM sqlite_master WHERE type='index' AND name='idx_task_run_devices_run_id'`, "012 重建 task_run_devices 后 run_id 索引"},
	}
	for _, c := range checks {
		var n int
		if err := db.QueryRow(c.q).Scan(&n); err != nil || n == 0 {
			t.Fatalf("%s 缺失（%v）", c.what, err)
		}
	}
	// 008 应删除 005 的全局 MAC 唯一索引（被网络内唯一索引取代）。
	var nIdx int
	if err := db.QueryRow(`SELECT COUNT(1) FROM sqlite_master WHERE type='index' AND name='idx_devices_mac_unique'`).Scan(&nIdx); err != nil || nIdx != 0 {
		t.Fatalf("005 全局 MAC 唯一索引应被 008 删除，实际 %d（%v）", nIdx, err)
	}
	// 003 应删除种子 local 行；001 的默认分组须保留。
	var n int
	if err := db.QueryRow(`SELECT COUNT(1) FROM agents WHERE id='local'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("种子 local 行应被 003 删除，实际 %d（%v）", n, err)
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM device_groups WHERE id='default'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("默认分组应保留，实际 %d（%v）", n, err)
	}
	// 外键完整性：迁移后不得有违反项。
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatalf("foreign_key_check: %v", err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("迁移后存在外键违反")
	}
}

func TestMigrateLegacy001DB(t *testing.T) {
	db := openTestDB(t)
	// 模拟 v0.1.0 旧库：只应用 001。
	if _, err := db.Exec(readMigration(t, "001_init.sql")); err != nil {
		t.Fatalf("应用 001 失败: %v", err)
	}
	// 遗留数据：管理员、agent_id='local' 的设备（全字段 distinctive 值，
	// 校验 005 重建表不丢数据）、孤儿 group_id 的设备、重复 MAC 的两行。
	if _, err := db.Exec(`INSERT INTO users (id, username, password_hash) VALUES ('u1', 'admin', 'x')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	full := `INSERT INTO devices (id, name, mac, ip, agent_id, broadcast_address, wol_port, device_type, status_method, status_port, status, last_checked_at, last_status_error, group_id, enabled, sleep_type, sleep_endpoint, remark, created_at, updated_at)
		VALUES ('dev-full', 'LAN-29', 'AA:BB:CC:DD:EE:01', '192.168.1.29', 'local', '192.168.1.255', 9, 'pc', 'ping', 22, 'online', '2026-11-05 22:45:22', '', 'default', 1, 'none', '', '备注', '2026-11-05 22:00:00', '2026-11-05 22:00:00')`
	if _, err := db.Exec(full); err != nil {
		t.Fatalf("seed full device: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO devices (id, name, mac, agent_id, group_id) VALUES ('dev-orphan', '孤儿分组设备', 'AA:BB:CC:DD:EE:03', 'local', 'ghost-group')`); err != nil {
		t.Fatalf("seed orphan device: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO devices (id, name, mac, agent_id) VALUES ('dev-dup-1', '重复MAC-先', 'AA:BB:CC:DD:EE:02', 'local')`); err != nil {
		t.Fatalf("seed dup1: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO devices (id, name, mac, agent_id) VALUES ('dev-dup-2', '重复MAC-后', 'AA:BB:CC:DD:EE:02', 'local')`); err != nil {
		t.Fatalf("seed dup2: %v", err)
	}

	if err := Migrate(db, migrationsDir); err != nil {
		t.Fatalf("Migrate 旧库升级失败: %v", err)
	}
	versions, _ := AppliedVersions(db)
	if len(versions) != 12 {
		t.Fatalf("升级后版本应为 1-12，实际 %v", versions)
	}
	// 旧数据零丢失：管理员与 local 设备行保留。
	var n int
	if err := db.QueryRow(`SELECT COUNT(1) FROM users WHERE username='admin'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("管理员应保留，实际 %d（%v）", n, err)
	}
	var name, mac, ip, agentID, bc, status, groupID, remark string
	var wolPort, statusPort, enabled int
	if err := db.QueryRow(`SELECT name, mac, ip, agent_id, broadcast_address, status, group_id, remark, wol_port, status_port, enabled FROM devices WHERE id='dev-full'`).Scan(
		&name, &mac, &ip, &agentID, &bc, &status, &groupID, &remark, &wolPort, &statusPort, &enabled); err != nil {
		t.Fatalf("全字段设备应保留: %v", err)
	}
	if name != "LAN-29" || mac != "AA:BB:CC:DD:EE:01" || ip != "192.168.1.29" || agentID != "local" || bc != "192.168.1.255" || status != "online" || groupID != "default" || remark != "备注" || wolPort != 9 || statusPort != 22 || enabled != 1 {
		t.Fatalf("005 重建表丢了数据：%q %q %q %q %q %q %q %q %d %d %d", name, mac, ip, agentID, bc, status, groupID, remark, wolPort, statusPort, enabled)
	}
	// 孤儿 group_id 归入 default；重复 MAC 只剩最早一行。
	if err := db.QueryRow(`SELECT group_id FROM devices WHERE id='dev-orphan'`).Scan(&groupID); err != nil || groupID != "default" {
		t.Fatalf("孤儿 group_id 应归入 default，实际 %q（%v）", groupID, err)
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM devices WHERE mac='AA:BB:CC:DD:EE:02'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("重复 MAC 应去重为 1 行，实际 %d（%v）", n, err)
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM devices WHERE id='dev-dup-1'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("重复 MAC 应保留最早一行 dev-dup-1，实际 %d（%v）", n, err)
	}
	// 种子 local Agent 行已删，但设备行的 agent_id 原样保留（契约 §4.6）。
	if err := db.QueryRow(`SELECT COUNT(1) FROM agents WHERE id='local'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("种子 local 行应删除，实际 %d（%v）", n, err)
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM devices WHERE agent_id='local'`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("遗留设备的 agent_id 应原样保留 3 行，实际 %d（%v）", n, err)
	}
	// 外键完整性与 group_id 外键生效。
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatalf("foreign_key_check: %v", err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("升级后存在外键违反")
	}
	if _, err := db.Exec(`INSERT INTO devices (id, name, mac, group_id) VALUES ('dev-bad-fk', '坏外键', 'AA:BB:CC:DD:EE:04', 'no-such-group')`); err == nil {
		t.Fatal("group_id 外键应拒绝不存在的分组")
	} else if !strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Fatalf("应为外键约束错误，实际：%v", err)
	}
}

func TestMigrateIdempotent(t *testing.T) {
	db := openTestDB(t)
	if err := Migrate(db, migrationsDir); err != nil {
		t.Fatalf("首次 Migrate: %v", err)
	}
	if err := Migrate(db, migrationsDir); err != nil {
		t.Fatalf("二次 Migrate 应幂等无错: %v", err)
	}
	versions, _ := AppliedVersions(db)
	if len(versions) != 12 {
		t.Fatalf("幂等重跑后版本应仍为 1-12，实际 %v", versions)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(1) FROM devices`).Scan(&n); err != nil {
		t.Fatalf("count devices: %v", err)
	}
}

func TestMigrateBadMigrationFails(t *testing.T) {
	// 复制 001–008 到临时目录，再加一个坏的 009（坏文件若与已有版本
	// 同号，执行顺序不确定，见旧版注释）。
	dir := t.TempDir()
	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(migrationsDir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(dir, e.Name()), b, 0o644); err != nil {
			t.Fatalf("write %s: %v", e.Name(), err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "009_broken.sql"), []byte("CREATE TABL broken (id TEXT);"), 0o644); err != nil {
		t.Fatalf("write 009: %v", err)
	}
	db := openTestDB(t)
	err = Migrate(db, dir)
	if err == nil {
		t.Fatal("坏迁移应返回错误")
	}
	if !strings.Contains(err.Error(), "009") {
		t.Fatalf("错误应指明失败的迁移 009，实际：%v", err)
	}
	versions, _ := AppliedVersions(db)
	if len(versions) != 8 {
		t.Fatalf("失败前 001–008 应已应用，实际 %v", versions)
	}
	for _, v := range versions {
		if v == 9 {
			t.Fatal("失败的 009 不得记入版本表")
		}
	}
}

func TestMigrateMissingDirFails(t *testing.T) {
	db := openTestDB(t)
	if err := Migrate(db, filepath.Join(t.TempDir(), "no-such-dir")); err == nil {
		t.Fatal("迁移目录缺失应返回错误（调用方 fatal）")
	}
}

func TestMigrate008PreservesDevices(t *testing.T) {
	// 先建一个 v0.2.8 状态的库：只应用 001–007。
	dir := t.TempDir()
	for _, f := range []string{"001_init.sql", "002_auth.sql", "003_agent.sql", "004_runs.sql", "005_indexes.sql", "006_ssh_sleep.sql", "007_ssh_windows.sql"} {
		b, err := os.ReadFile(filepath.Join(migrationsDir, f))
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), b, 0o644); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
	db := openTestDB(t)
	if err := Migrate(db, dir); err != nil {
		t.Fatalf("应用 001–007 失败: %v", err)
	}
	// 造数据：两个 Agent（不同网段）+ 三台设备。
	if _, err := db.Exec(`INSERT INTO agents (id, name, subnet, approval_status, status, authorized)
		VALUES ('agent-a', 'A', '192.168.1.0/24', 'approved', 'offline', 1),
		       ('agent-b', 'B', '192.168.2.0/24', 'approved', 'offline', 1),
		       ('agent-c', 'C', '', 'approved', 'offline', 1)`); err != nil {
		t.Fatalf("插入 Agent 失败: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO devices (id, name, mac, ip, agent_id)
		VALUES ('dev-1', 'A-11', '00:11:22:33:44:01', '192.168.1.11', 'agent-a'),
		       ('dev-2', 'B-12', '00:11:22:33:44:02', '192.168.2.12', 'agent-b'),
		       ('dev-3', 'A-13', '00:11:22:33:44:03', '192.168.1.13', 'agent-a'),
		       ('dev-4', 'C-14', '00:11:22:33:44:04', '192.168.1.14', 'agent-c')`); err != nil {
		t.Fatalf("插入设备失败: %v", err)
	}
	var before int
	if err := db.QueryRow(`SELECT COUNT(*) FROM devices`).Scan(&before); err != nil || before != 4 {
		t.Fatalf("迁移前设备应为 4 台，got %d, %v", before, err)
	}
	// 再应用 008。
	b, err := os.ReadFile(filepath.Join(migrationsDir, "008_network_identity.sql"))
	if err != nil {
		t.Fatalf("read 008: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "008_network_identity.sql"), b, 0o644); err != nil {
		t.Fatalf("write 008: %v", err)
	}
	if err := Migrate(db, dir); err != nil {
		t.Fatalf("应用 008 失败: %v", err)
	}
	// 设备一台不少。
	var after int
	if err := db.QueryRow(`SELECT COUNT(*) FROM devices`).Scan(&after); err != nil || after != before {
		t.Fatalf("迁移后设备数必须不变，迁移前 %d、迁移后 %d, %v", before, after, err)
	}
	// 回填归属：agent-a 的设备 → net-192-168-1-0-24；agent-b → net-192-168-2-0-24；
	// 无网段 agent-c 的设备 → default。
	checks := map[string]string{
		"dev-1": "net-192-168-1-0-24",
		"dev-3": "net-192-168-1-0-24",
		"dev-2": "net-192-168-2-0-24",
		"dev-4": "default",
	}
	for id, want := range checks {
		var got string
		if err := db.QueryRow(`SELECT network_id FROM devices WHERE id = ?`, id).Scan(&got); err != nil || got != want {
			t.Fatalf("设备 %s 应归属 %s，got %q, %v", id, want, got, err)
		}
	}
	// networks：default + 2 个网段网络；agent_networks：2 行。
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM networks`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("networks 应为 3 行，got %d, %v", n, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM agent_networks`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("agent_networks 应为 2 行，got %d, %v", n, err)
	}
	// 同 MAC 跨网络可插；同网络同 MAC 拒绝。
	if _, err := db.Exec(`INSERT INTO devices (id, name, mac, ip, agent_id, network_id)
		VALUES ('dev-5', 'B-11', '00:11:22:33:44:01', '192.168.2.11', 'agent-b', 'net-192-168-2-0-24')`); err != nil {
		t.Fatalf("同 MAC 跨网络插入应成功，got %v", err)
	}
	if _, err := db.Exec(`INSERT INTO devices (id, name, mac, ip, agent_id, network_id)
		VALUES ('dev-6', 'A-11-dup', '00:11:22:33:44:01', '192.168.1.15', 'agent-a', 'net-192-168-1-0-24')`); err == nil {
		t.Fatal("同网络同 MAC 插入应被唯一索引拒绝")
	}
}

func TestBackupBeforeMigrate(t *testing.T) {
	// 只复制 001–007，模拟 v0.2.8 状态库。
	dir := t.TempDir()
	for _, f := range []string{"001_init.sql", "002_auth.sql", "003_agent.sql", "004_runs.sql", "005_indexes.sql", "006_ssh_sleep.sql", "007_ssh_windows.sql"} {
		b, err := os.ReadFile(filepath.Join(migrationsDir, f))
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), b, 0o644); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
	dbPath := filepath.Join(t.TempDir(), "wakehub.db")
	db, err := sql.Open("sqlite", dbPath+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(db, dir); err != nil {
		t.Fatalf("应用 001–007 失败: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO devices (id, name, mac, ip, agent_id) VALUES ('dev-1', 'A', '00:11:22:33:44:01', '192.168.1.11', 'agent-a')`); err != nil {
		t.Fatalf("插入设备失败: %v", err)
	}
	// 待处理迁移为 008 → 应备份。
	bak, err := BackupBeforeMigrate(db, dbPath, migrationsDir)
	if err != nil || bak == "" {
		t.Fatalf("有待处理迁移时应备份，got %q, %v", bak, err)
	}
	info, err := os.Stat(bak)
	if err != nil {
		t.Fatalf("备份文件应存在: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("备份文件权限应为 0600，got %o", info.Mode().Perm())
	}
	bakDB, err := sql.Open("sqlite", bak+"?mode=ro")
	if err != nil {
		t.Fatalf("打开备份库失败: %v", err)
	}
	defer bakDB.Close()
	var n int
	if err := bakDB.QueryRow(`SELECT COUNT(*) FROM devices`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("备份库设备数应为 1，got %d, %v", n, err)
	}
	// 应用 008 后再备份 → 无待处理迁移，返回 ""。
	if err := Migrate(db, migrationsDir); err != nil {
		t.Fatalf("应用 008 失败: %v", err)
	}
	if bak2, err := BackupBeforeMigrate(db, dbPath, migrationsDir); err != nil || bak2 != "" {
		t.Fatalf("无待处理迁移时不应备份，got %q, %v", bak2, err)
	}
	// 内存库 → ""。
	memDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open memory: %v", err)
	}
	defer memDB.Close()
	if bak3, err := BackupBeforeMigrate(memDB, ":memory:", migrationsDir); err != nil || bak3 != "" {
		t.Fatalf("内存库不应备份，got %q, %v", bak3, err)
	}
}

func TestMigrateDeviceLossRefused(t *testing.T) {
	// 复制 001–008，先建库并造一台设备。
	dir := t.TempDir()
	for _, f := range []string{"001_init.sql", "002_auth.sql", "003_agent.sql", "004_runs.sql", "005_indexes.sql", "006_ssh_sleep.sql", "007_ssh_windows.sql", "008_network_identity.sql"} {
		b, err := os.ReadFile(filepath.Join(migrationsDir, f))
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), b, 0o644); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
	db := openTestDB(t)
	if err := Migrate(db, dir); err != nil {
		t.Fatalf("应用 001–008 失败: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO devices (id, name, mac, ip, agent_id) VALUES ('dev-1', 'A', '00:11:22:33:44:01', '192.168.1.11', 'agent-a')`); err != nil {
		t.Fatalf("插入设备失败: %v", err)
	}
	// 再加一个删设备的 009：迁移应返回设备减少错误，拒绝启动。
	if err := os.WriteFile(filepath.Join(dir, "009_delete_device.sql"), []byte("DELETE FROM devices WHERE id = 'dev-1';"), 0o644); err != nil {
		t.Fatalf("write 009: %v", err)
	}
	err := Migrate(db, dir)
	if err == nil {
		t.Fatal("删设备的迁移应返回错误")
	}
	if !strings.Contains(err.Error(), "设备数量减少") {
		t.Fatalf("错误应指明设备数量减少，实际：%v", err)
	}
}

func TestMigrate009RenamesSeededNetworks(t *testing.T) {
	// 先建一个 v0.2.9 状态的库：应用 001–008。
	dir := t.TempDir()
	for _, f := range []string{"001_init.sql", "002_auth.sql", "003_agent.sql", "004_runs.sql", "005_indexes.sql", "006_ssh_sleep.sql", "007_ssh_windows.sql", "008_network_identity.sql"} {
		b, err := os.ReadFile(filepath.Join(migrationsDir, f))
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), b, 0o644); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
	db := openTestDB(t)
	if err := Migrate(db, dir); err != nil {
		t.Fatalf("应用 001–008 失败: %v", err)
	}
	// 造一个 Agent 网段（008 会种子一个 '网络 192.168.1.0/24' 行）与一个
	// 用户手工改过名的网络；默认网络由 008 种子。
	if _, err := db.Exec(`INSERT INTO agents (id, name, subnet, approval_status, status, authorized)
		VALUES ('agent-a', 'A', '192.168.1.0/24', 'approved', 'offline', 1)`); err != nil {
		t.Fatalf("插入 Agent 失败: %v", err)
	}
	// 注意：008 在 Migrate 时已跑过，此时再插 Agent 不会触发 008 的种子；
	// 直接按 008 的种子规则补一行，模拟 v0.2.9 线上库的状态。
	if _, err := db.Exec(`INSERT INTO networks (id, name, cidr, description)
		VALUES ('net-192-168-2-0-24', '网络 192.168.2.0/24', '192.168.2.0/24', '由 Agent 注册网段自动建立'),
		       ('net-192-168-1-0-24', '办公室', '192.168.1.0/24', '用户手工改名')`); err != nil {
		t.Fatalf("种子 networks 失败: %v", err)
	}
	// 再应用 009。
	b, err := os.ReadFile(filepath.Join(migrationsDir, "009_rename_seeded_networks.sql"))
	if err != nil {
		t.Fatalf("read 009: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "009_rename_seeded_networks.sql"), b, 0o644); err != nil {
		t.Fatalf("write 009: %v", err)
	}
	if err := Migrate(db, dir); err != nil {
		t.Fatalf("应用 009 失败: %v", err)
	}
	// 种子行改名：名字即 CIDR。
	var got string
	if err := db.QueryRow(`SELECT name FROM networks WHERE id = 'net-192-168-2-0-24'`).Scan(&got); err != nil || got != "192.168.2.0/24" {
		t.Fatalf("种子网络应改名为 CIDR 本身，got %q, %v", got, err)
	}
	// 默认网络与手工改名的一律不动。
	if err := db.QueryRow(`SELECT name FROM networks WHERE id = 'default'`).Scan(&got); err != nil || got != "默认网络" {
		t.Fatalf("默认网络名字不应动，got %q, %v", got, err)
	}
	if err := db.QueryRow(`SELECT name FROM networks WHERE id = 'net-192-168-1-0-24'`).Scan(&got); err != nil || got != "办公室" {
		t.Fatalf("手工改名的网络不应动，got %q, %v", got, err)
	}
	// 幂等：再跑一次 Migrate 不报错、名字不变。
	if err := Migrate(db, dir); err != nil {
		t.Fatalf("重复应用 009 应成功: %v", err)
	}
	if err := db.QueryRow(`SELECT name FROM networks WHERE id = 'net-192-168-2-0-24'`).Scan(&got); err != nil || got != "192.168.2.0/24" {
		t.Fatalf("重复应用后种子网络名应仍为 CIDR，got %q, %v", got, err)
	}
}

// TestMigrate010SSHUseSudo 覆盖 v0.2.12 迁移 010：
// 旧库升级后 devices 多 ssh_use_sudo 列（NOT NULL，默认 0），
// 已有设备行默认不启用 sudo，老行为不变。
func TestMigrate010SSHUseSudo(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.Exec(readMigration(t, "001_init.sql")); err != nil {
		t.Fatalf("应用 001 失败: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO devices (id, name, mac, agent_id) VALUES ('dev-old', '老设备', 'AA:BB:CC:DD:EE:09', 'agent-a')`); err != nil {
		t.Fatalf("seed 老设备: %v", err)
	}
	if err := Migrate(db, migrationsDir); err != nil {
		t.Fatalf("Migrate 旧库升级失败: %v", err)
	}
	var def string
	if err := db.QueryRow(`SELECT dflt_value FROM pragma_table_info('devices') WHERE name='ssh_use_sudo'`).Scan(&def); err != nil {
		t.Fatalf("ssh_use_sudo 列缺失: %v", err)
	}
	if def != "0" {
		t.Fatalf("ssh_use_sudo 默认值应为 0，实际 %q", def)
	}
	var sudo int
	if err := db.QueryRow(`SELECT ssh_use_sudo FROM devices WHERE id='dev-old'`).Scan(&sudo); err != nil {
		t.Fatalf("读老设备 ssh_use_sudo: %v", err)
	}
	if sudo != 0 {
		t.Fatalf("老设备升级后 ssh_use_sudo 应为 0（不启用），实际 %d", sudo)
	}
}

// copyMigrations 把 migrationsDir 下指定文件复制到 dir。
func copyMigrations(t *testing.T, dir string, files ...string) {
	t.Helper()
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(migrationsDir, f))
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), b, 0o644); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
}

// TestMigrate011NetworkCIDRUnique 覆盖 v0.3.3 问题 6 的迁移部分：
// 011 在全新库与已有等价行库上均能干净应用，去重只保留最早一行。
func TestMigrate011NetworkCIDRUnique(t *testing.T) {
	// A. 全新库：唯一索引存在；同一规范化 CIDR 直接插库即被拒（等价
	// 写法由仓库层规范化后命中同一索引值，见 device 包测试）；
	// 空 CIDR 允许多行（部分索引）。
	db := openTestDB(t)
	if err := Migrate(db, migrationsDir); err != nil {
		t.Fatalf("Migrate 全新库失败: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO networks (id, name, cidr) VALUES ('n1', '办公', '192.168.1.0/24')`); err != nil {
		t.Fatalf("插入网络失败: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO networks (id, name, cidr) VALUES ('n2', '重复', '192.168.1.0/24')`); err == nil {
		t.Fatal("同一 CIDR 应被唯一索引拒绝")
	} else if !strings.Contains(err.Error(), "UNIQUE constraint") {
		t.Fatalf("应为唯一约束错误，实际：%v", err)
	}
	if _, err := db.Exec(`INSERT INTO networks (id, name, cidr) VALUES ('n3', '未指定1', ''), ('n4', '未指定2', '')`); err != nil {
		t.Fatalf("空 CIDR 应允许多行，got %v", err)
	}

	// B. 已有等价行：011 去重只保留最早一行，引用改指，设备一台不少。
	dir := t.TempDir()
	copyMigrations(t, dir, "001_init.sql", "002_auth.sql", "003_agent.sql", "004_runs.sql", "005_indexes.sql", "006_ssh_sleep.sql", "007_ssh_windows.sql", "008_network_identity.sql", "009_rename_seeded_networks.sql", "010_ssh_use_sudo.sql")
	db2 := openTestDB(t)
	if err := Migrate(db2, dir); err != nil {
		t.Fatalf("应用 001–010 失败: %v", err)
	}
	if _, err := db2.Exec(`INSERT INTO agents (id, name, subnet, approval_status, status, authorized) VALUES ('agent-a', 'A', '', 'approved', 'offline', 1)`); err != nil {
		t.Fatalf("插入 Agent 失败: %v", err)
	}
	if _, err := db2.Exec(`INSERT INTO networks (id, name, cidr, created_at) VALUES
		('net-old', '旧写法', '192.168.1.1/24', '2026-01-01 00:00:00'),
		('net-new', '新写法', '192.168.1.0/24', '2026-01-02 00:00:00')`); err != nil {
		t.Fatalf("插入等价网络行失败: %v", err)
	}
	if _, err := db2.Exec(`INSERT INTO agent_networks (agent_id, network_id) VALUES ('agent-a', 'net-old'), ('agent-a', 'net-new')`); err != nil {
		t.Fatalf("插入 Agent 网络关联失败: %v", err)
	}
	if _, err := db2.Exec(`INSERT INTO devices (id, name, mac, agent_id, network_id) VALUES
		('dev-1', 'A', '00:11:22:33:44:01', 'agent-a', 'net-old'),
		('dev-2', 'B', '00:11:22:33:44:02', 'agent-a', 'net-new')`); err != nil {
		t.Fatalf("插入设备失败: %v", err)
	}
	copyMigrations(t, dir, "011_network_cidr_unique.sql")
	if err := Migrate(db2, dir); err != nil {
		t.Fatalf("应用 011 失败: %v", err)
	}
	var n int
	if err := db2.QueryRow(`SELECT COUNT(*) FROM networks WHERE cidr = '192.168.1.0/24'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("去重后 192.168.1.0/24 应只剩 1 行，got %d, %v", n, err)
	}
	if err := db2.QueryRow(`SELECT COUNT(*) FROM networks WHERE id = 'net-new'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("重复行 net-new 应被删除，got %d, %v", n, err)
	}
	if err := db2.QueryRow(`SELECT COUNT(*) FROM devices`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("去重不得删除设备，got %d, %v", n, err)
	}
	if err := db2.QueryRow(`SELECT COUNT(*) FROM devices WHERE network_id = 'net-old'`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("设备引用应全部改指保留行 net-old，got %d, %v", n, err)
	}
	if err := db2.QueryRow(`SELECT COUNT(*) FROM agent_networks WHERE agent_id = 'agent-a'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("Agent 关联去重后应只剩 1 行，got %d, %v", n, err)
	}
	if err := Migrate(db2, dir); err != nil {
		t.Fatalf("重复应用 011 应成功: %v", err)
	}

	// C. 008 种子行用 Agent 原始 subnet 字符串：011 须同样规范化。
	dir3 := t.TempDir()
	copyMigrations(t, dir3, "001_init.sql", "002_auth.sql", "003_agent.sql", "004_runs.sql", "005_indexes.sql", "006_ssh_sleep.sql", "007_ssh_windows.sql")
	db3 := openTestDB(t)
	if err := Migrate(db3, dir3); err != nil {
		t.Fatalf("应用 001–007 失败: %v", err)
	}
	if _, err := db3.Exec(`INSERT INTO agents (id, name, subnet, approval_status, status, authorized) VALUES ('agent-a', 'A', '192.168.1.5/24', 'approved', 'offline', 1)`); err != nil {
		t.Fatalf("插入 Agent 失败: %v", err)
	}
	copyMigrations(t, dir3, "008_network_identity.sql")
	if err := Migrate(db3, dir3); err != nil {
		t.Fatalf("应用 008 失败: %v", err)
	}
	var cidr string
	if err := db3.QueryRow(`SELECT cidr FROM networks WHERE id = 'net-192-168-1-5-24'`).Scan(&cidr); err != nil || cidr != "192.168.1.5/24" {
		t.Fatalf("008 应按原始 subnet 种子，got %q, %v", cidr, err)
	}
	copyMigrations(t, dir3, "009_rename_seeded_networks.sql", "010_ssh_use_sudo.sql", "011_network_cidr_unique.sql")
	if err := Migrate(db3, dir3); err != nil {
		t.Fatalf("应用 009–011 失败: %v", err)
	}
	if err := db3.QueryRow(`SELECT cidr FROM networks WHERE id = 'net-192-168-1-5-24'`).Scan(&cidr); err != nil || cidr != "192.168.1.0/24" {
		t.Fatalf("011 应把种子行规范化为 192.168.1.0/24，got %q, %v", cidr, err)
	}
}

func seedTaskRun(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO schedules (id, name, cron_expr, timezone, target_type, target_id, action, retry_count, enabled) VALUES ('sched-1', '晨起', '0 8 * * *', 'UTC', 'device', 'dev-1', 'wake', 0, 1)`); err != nil {
		t.Fatalf("插入任务失败: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO task_runs (id, schedule_id, status, output, executed_at, started_at, finished_at, duration_ms) VALUES ('run-1', 'sched-1', 'success', '成功 1 / 失败 0 / 跳过 0', '2026-11-07 08:00:00', '2026-11-07 08:00:00', '2026-11-07 08:00:01', 1000)`); err != nil {
		t.Fatalf("插入执行记录失败: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO task_run_devices (run_id, device_id, device_name, status, reason) VALUES ('run-1', 'dev-1', 'PC-01', 'success', '')`); err != nil {
		t.Fatalf("插入每设备行失败: %v", err)
	}
}

func assertTaskRunSurvives(t *testing.T, db *sql.DB, when string) {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM task_runs WHERE id = 'run-1'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("%s：run 行应保留，got %d, %v", when, n, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM task_run_devices WHERE run_id = 'run-1'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("%s：每设备行应保留，got %d, %v", when, n, err)
	}
}

// TestMigrate012KeepsTaskRunsOnScheduleDelete 覆盖 v0.3.3 问题 11：
// 012 把 task_runs.schedule_id 外键改为 ON DELETE SET NULL；
// 升级过程与删任务后执行历史均保留。
func TestMigrate012KeepsTaskRunsOnScheduleDelete(t *testing.T) {
	// A. 升级路径：010 状态库已有执行历史，应用 011+012 后历史一台不少。
	dir := t.TempDir()
	copyMigrations(t, dir, "001_init.sql", "002_auth.sql", "003_agent.sql", "004_runs.sql", "005_indexes.sql", "006_ssh_sleep.sql", "007_ssh_windows.sql", "008_network_identity.sql", "009_rename_seeded_networks.sql", "010_ssh_use_sudo.sql")
	db := openTestDB(t)
	if err := Migrate(db, dir); err != nil {
		t.Fatalf("应用 001–010 失败: %v", err)
	}
	seedTaskRun(t, db)
	copyMigrations(t, dir, "011_network_cidr_unique.sql", "012_task_runs_keep_history.sql")
	if err := Migrate(db, dir); err != nil {
		t.Fatalf("应用 011–012 失败: %v", err)
	}
	assertTaskRunSurvives(t, db, "升级后")
	// 外键已改为 SET NULL：表定义须含该子句，且 schedule_id 可空。
	var sqlDef string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='task_runs'`).Scan(&sqlDef); err != nil || !strings.Contains(sqlDef, "ON DELETE SET NULL") {
		t.Fatalf("task_runs 外键应为 ON DELETE SET NULL，got %q, %v", sqlDef, err)
	}
	var notNull int
	if err := db.QueryRow(`SELECT "notnull" FROM pragma_table_info('task_runs') WHERE name='schedule_id'`).Scan(&notNull); err != nil || notNull != 0 {
		t.Fatalf("task_runs.schedule_id 应可空，got notnull=%d, %v", notNull, err)
	}

	// B. 删除行为：删任务后 run 行与每设备行仍在，schedule_id 置 NULL。
	if _, err := db.Exec(`DELETE FROM schedules WHERE id = 'sched-1'`); err != nil {
		t.Fatalf("删除任务失败: %v", err)
	}
	assertTaskRunSurvives(t, db, "删任务后")
	var schedID sql.NullString
	if err := db.QueryRow(`SELECT schedule_id FROM task_runs WHERE id = 'run-1'`).Scan(&schedID); err != nil || schedID.Valid {
		t.Fatalf("删任务后 run 行 schedule_id 应为 NULL，got %+v, %v", schedID, err)
	}
	if err := Migrate(db, dir); err != nil {
		t.Fatalf("重复应用 011–012 应成功: %v", err)
	}
}

// TestMigrate005PlusDeletingMigrationRefused 覆盖 v0.3.3 问题 8：
// 005 的豁免只限其自身去重增量；同批另一个删设备的迁移不得被放行。
// （复现审查证明：001–004 旧库 3 台设备、两台同 MAC；待执行 = 真实
// 005 + 合成 009 删设备；旧逻辑返回 nil、设备 3→1。）
func TestMigrate005PlusDeletingMigrationRefused(t *testing.T) {
	dir := t.TempDir()
	copyMigrations(t, dir, "001_init.sql", "002_auth.sql", "003_agent.sql", "004_runs.sql")
	db := openTestDB(t)
	if err := Migrate(db, dir); err != nil {
		t.Fatalf("应用 001–004 失败: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO devices (id, name, mac, agent_id) VALUES
		('dev-1', 'A', 'AA:BB:CC:DD:EE:01', 'agent-a'),
		('dev-2', 'A-重复MAC', 'AA:BB:CC:DD:EE:01', 'agent-a'),
		('dev-3', 'B', 'AA:BB:CC:DD:EE:03', 'agent-a')`); err != nil {
		t.Fatalf("插入设备失败: %v", err)
	}
	copyMigrations(t, dir, "005_indexes.sql")
	if err := os.WriteFile(filepath.Join(dir, "009_delete_device.sql"), []byte("DELETE FROM devices WHERE id = 'dev-3';"), 0o644); err != nil {
		t.Fatalf("write 009: %v", err)
	}
	err := Migrate(db, dir)
	if err == nil {
		t.Fatal("005 + 删设备迁移同批应返回错误")
	}
	if !strings.Contains(err.Error(), "设备数量减少") {
		t.Fatalf("错误应指明设备数量减少，实际：%v", err)
	}
	// 005 自身去重 1 台是被豁免的：错误须说明其中 005 删除了 1 台，
	// 而总数 3→1 超出豁免。
	if !strings.Contains(err.Error(), "005 去重删除 1 台") {
		t.Fatalf("错误应说明 005 自身去重删除 1 台，实际：%v", err)
	}
}

// TestMigrateTableCountsFailureAborts 覆盖 v0.3.3 问题 8：
// TableCounts 出错时 fail-closed，迁移拒绝启动。
// 模拟手段：临时视图遮蔽同名真实表（同一连接内生效）——
// TableCounts 先在 sqlite_master 中确认表存在，再 COUNT(*) 时命中
// 临时视图而报错，模拟“表在但数不出来”的损坏库。
func TestMigrateTableCountsFailureAborts(t *testing.T) {
	// A. 迁移前核对失败。
	dir := t.TempDir()
	copyMigrations(t, dir, "001_init.sql", "002_auth.sql", "003_agent.sql", "004_runs.sql")
	db := openTestDB(t)
	if err := Migrate(db, dir); err != nil {
		t.Fatalf("应用 001–004 失败: %v", err)
	}
	if _, err := db.Exec(`CREATE TEMP VIEW devices AS SELECT * FROM missing_table`); err != nil {
		t.Fatalf("建临时遮蔽视图: %v", err)
	}
	copyMigrations(t, dir, "005_indexes.sql")
	err := Migrate(db, dir)
	if err == nil {
		t.Fatal("迁移前数量核对失败时应返回错误")
	}
	if !strings.Contains(err.Error(), "数量核对失败") {
		t.Fatalf("错误应指明数量核对失败，实际：%v", err)
	}
	versions, _ := AppliedVersions(db)
	for _, v := range versions {
		if v == 5 {
			t.Fatal("核对失败时 005 不得被应用")
		}
	}

	// B. 迁移后核对失败：某迁移把 audit_logs 遮蔽成坏视图。
	dir2 := t.TempDir()
	copyMigrations(t, dir2, "001_init.sql", "002_auth.sql", "003_agent.sql", "004_runs.sql", "005_indexes.sql", "006_ssh_sleep.sql", "007_ssh_windows.sql", "008_network_identity.sql")
	db2 := openTestDB(t)
	if err := Migrate(db2, dir2); err != nil {
		t.Fatalf("应用 001–008 失败: %v", err)
	}
	if _, err := db2.Exec(`INSERT INTO devices (id, name, mac, agent_id) VALUES ('dev-1', 'A', '00:11:22:33:44:01', 'agent-a')`); err != nil {
		t.Fatalf("插入设备失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir2, "009_break_audit.sql"), []byte("CREATE TEMP VIEW audit_logs AS SELECT * FROM missing_table;"), 0o644); err != nil {
		t.Fatalf("write 009: %v", err)
	}
	err = Migrate(db2, dir2)
	if err == nil {
		t.Fatal("迁移后数量核对失败时应返回错误")
	}
	if !strings.Contains(err.Error(), "数量核对失败") {
		t.Fatalf("错误应指明数量核对失败，实际：%v", err)
	}
	var n int
	if err := db2.QueryRow(`SELECT COUNT(*) FROM devices`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("设备应原样保留 1 台，got %d, %v", n, err)
	}
}
