// Package db 是 WakeHub 的 SQLite 迁移运行器（M5，契约见
// docs/migration-002-003.md §1）：启动时按版本号顺序应用
// migrations/ 下的全部迁移文件，幂等重跑，失败即拒绝启动。
package db

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// migrationFileName 匹配迁移文件名：数字前缀 + 下划线 + 任意后缀。
// 例：001_init.sql、002_auth.sql、005_indexes.sql。
var migrationFileName = regexp.MustCompile(`^(\d+)_.*\.sql$`)

// Migrate 按版本号升序应用 dir 下全部未应用的迁移：
//
//   - 版本表 schema_migrations（version INTEGER PRIMARY KEY）记录已应用版本；
//   - 基线回填：库里无版本记录、但 users/devices/agents 三张核心表已存在
//     → 视为 001 已应用，先记版本 1 再按序应用 002+；全新空库按序应用 001+；
//   - 每个文件一个事务；任一语句失败 → 回滚该文件并返回错误（调用方须
//     fatal 退出，不得跳过失败的迁移继续启动）；
//   - 已记录的版本跳过，不重新执行；非迁移文件（不匹配命名规则）忽略。
//   - 设备数量保护（v0.2.9 问题 12，v0.3.3 问题 8 收窄）：整轮迁移结束
//     时设备数不得减少，唯一例外是 005 自身的去重删除（按 005 执行前后
//     的实际删除行数豁免，并记日志）；同批其他迁移导致设备减少一律
//     视为数据丢失，返回错误拒绝启动（调用方凭迁移前备份恢复，
//     见 BackupBeforeMigrate）。TableCounts 出错时 fail-closed，
//     直接返回错误拒绝启动，不忽略。
func Migrate(db *sql.DB, dir string) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return fmt.Errorf("创建版本表失败: %w", err)
	}

	// 基线回填（迁移文档 §1）：只有“无任何版本记录”时才判断。
	var recorded int
	if err := db.QueryRow(`SELECT COUNT(1) FROM schema_migrations`).Scan(&recorded); err != nil {
		return err
	}
	if recorded == 0 {
		var core int
		if err := db.QueryRow(`SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name IN ('users', 'devices', 'agents')`).Scan(&core); err != nil {
			return err
		}
		if core == 3 {
			if _, err := db.Exec(`INSERT INTO schema_migrations (version) VALUES (1)`); err != nil {
				return fmt.Errorf("基线回填版本 1 失败: %w", err)
			}
		}
	}

	pending, err := PendingVersions(db, dir)
	if err != nil {
		return err
	}
	// 数量核对失败即 fail-closed：数都数不出来时不得放行。
	before, err := TableCounts(db)
	if err != nil {
		return fmt.Errorf("迁移前数量核对失败，拒绝启动: %w", err)
	}
	// 005 自身的去重增量：只豁免 005 自己删的行（v0.3.3 问题 8）。
	// 旧逻辑 `!appliedSet[5]` 是整批豁免，同批其他删除迁移也被放行。
	deletedBy005 := 0
	for _, f := range pending {
		if f.version == 5 {
			before005, err := TableCounts(db)
			if err != nil {
				return fmt.Errorf("迁移 005 前数量核对失败，拒绝启动: %w", err)
			}
			if err := applyOne(db, f); err != nil {
				return err
			}
			after005, err := TableCounts(db)
			if err != nil {
				return fmt.Errorf("迁移 005 后数量核对失败，拒绝启动: %w", err)
			}
			deletedBy005 = before005["devices"] - after005["devices"]
			if deletedBy005 > 0 {
				log.Printf("[Migrate] 迁移 005 去重删除 %d 台重复设备", deletedBy005)
			}
			continue
		}
		if err := applyOne(db, f); err != nil {
			return err
		}
	}
	// 设备数量保护：005 自身的去重删除除外，其余迁移不得减少设备数。
	if len(pending) > 0 {
		after, err := TableCounts(db)
		if err != nil {
			return fmt.Errorf("迁移后数量核对失败，拒绝启动: %w", err)
		}
		if after["devices"] < before["devices"]-deletedBy005 {
			return fmt.Errorf("迁移后设备数量减少（%d → %d，其中 005 去重删除 %d 台），疑似数据丢失，已拒绝启动；请用迁移前备份恢复", before["devices"], after["devices"], deletedBy005)
		}
	}
	return nil
}

// PendingVersions 返回 dir 下尚未应用的迁移文件（按版本号升序）。
func PendingVersions(db *sql.DB, dir string) ([]migrationFile, error) {
	applied, err := AppliedVersions(db)
	if err != nil {
		return nil, err
	}
	done := make(map[int]bool, len(applied))
	for _, v := range applied {
		done[v] = true
	}
	files, err := listMigrationFiles(dir)
	if err != nil {
		return nil, err
	}
	var pending []migrationFile
	for _, f := range files {
		if done[f.version] {
			continue
		}
		pending = append(pending, f)
	}
	return pending, nil
}

// TableCounts 返回关键表行数（表不存在时跳过，不报错）：devices、agents、
// schedules、task_runs、audit_logs、networks。供迁移前后核对用。
func TableCounts(db *sql.DB) (map[string]int, error) {
	out := map[string]int{}
	for _, table := range []string{"devices", "agents", "schedules", "task_runs", "audit_logs", "networks"} {
		var exists int
		if err := db.QueryRow(`SELECT COUNT(1) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&exists); err != nil {
			return nil, err
		}
		if exists == 0 {
			continue
		}
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			return nil, err
		}
		out[table] = n
	}
	return out, nil
}

// CountsReport 把迁移前后数量核对结果拼成一行日志文本。
func CountsReport(before, after map[string]int) string {
	var parts []string
	for _, table := range []string{"devices", "agents", "schedules", "task_runs", "audit_logs", "networks"} {
		b, okB := before[table]
		a, okA := after[table]
		if !okB && !okA {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s %d→%d", table, b, a))
	}
	if len(parts) == 0 {
		return "无可核对表"
	}
	return strings.Join(parts, "，")
}

// BackupBeforeMigrate 在应用待处理迁移前备份数据库（v0.2.9 问题 12）：
// 无待处理迁移、内存库或库文件尚不存在时不备份，返回 ""；否则用
// VACUUM INTO 生成一致快照（含 WAL 内容），文件权限 0600，返回备份路径。
func BackupBeforeMigrate(db *sql.DB, dbPath, dir string) (string, error) {
	if dbPath == "" || dbPath == ":memory:" {
		return "", nil
	}
	pending, err := PendingVersions(db, dir)
	if err != nil {
		return "", err
	}
	if len(pending) == 0 {
		return "", nil
	}
	if _, err := os.Stat(dbPath); err != nil {
		return "", nil
	}
	stamp := time.Now().Format("20060102-150405")
	backupPath := fmt.Sprintf("%s.migrate-bak-%s", dbPath, stamp)
	// VACUUM INTO 不接受绑定参数；路径来自服务端配置，单引号转义后拼接。
	escaped := strings.ReplaceAll(backupPath, "'", "''")
	if _, err := db.Exec("VACUUM INTO '" + escaped + "'"); err != nil {
		return "", fmt.Errorf("迁移前备份失败: %w", err)
	}
	if err := os.Chmod(backupPath, 0o600); err != nil {
		return "", fmt.Errorf("备份文件权限收紧失败: %w", err)
	}
	return backupPath, nil
}

// AppliedVersions 返回已记录的迁移版本号（升序）。
// 版本表尚不存在时（旧库首次走迁移运行器）返回空列表，不报错；
// Migrate 会先建表再做基线回填。
func AppliedVersions(db *sql.DB) ([]int, error) {
	var exists int
	if err := db.QueryRow(`SELECT COUNT(1) FROM sqlite_master WHERE type='table' AND name='schema_migrations'`).Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return []int{}, nil
	}
	rows, err := db.Query(`SELECT version FROM schema_migrations ORDER BY version ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if out == nil {
		out = []int{}
	}
	return out, nil
}

type migrationFile struct {
	version int
	path    string
}

// listMigrationFiles 扫描目录，按版本号升序返回迁移文件。
func listMigrationFiles(dir string) ([]migrationFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("读取迁移目录 %s 失败: %w", dir, err)
	}
	var out []migrationFile
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := migrationFileName.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		v, err := strconv.Atoi(m[1])
		if err != nil {
			return nil, fmt.Errorf("迁移文件名 %s 的版本号无法解析: %w", e.Name(), err)
		}
		out = append(out, migrationFile{version: v, path: filepath.Join(dir, e.Name())})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

// applyOne 在一个事务内应用单个迁移文件并记录版本。
// 例外：PRAGMA 语句（如 001 的 journal_mode=WAL）不能在事务内执行，
// 须先在事务外单独执行（单行一条，迁移文件中的 PRAGMA 须写成单行）。
func applyOne(db *sql.DB, f migrationFile) error {
	sqlBytes, err := os.ReadFile(f.path)
	if err != nil {
		return fmt.Errorf("读取迁移文件 %s 失败: %w", f.path, err)
	}
	if strings.TrimSpace(string(sqlBytes)) == "" {
		return fmt.Errorf("迁移文件 %s 为空", f.path)
	}
	pragmas, rest := splitPragmas(string(sqlBytes))
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			return fmt.Errorf("迁移 %03d 执行 %s 失败: %w", f.version, p, err)
		}
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("迁移 %03d 开启事务失败: %w", f.version, err)
	}
	defer tx.Rollback()
	if strings.TrimSpace(rest) != "" {
		if _, err := tx.Exec(rest); err != nil {
			return fmt.Errorf("应用迁移 %03d（%s）失败: %w", f.version, filepath.Base(f.path), err)
		}
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations (version) VALUES (?)`, f.version); err != nil {
		return fmt.Errorf("记录迁移版本 %03d 失败: %w", f.version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交迁移 %03d 失败: %w", f.version, err)
	}
	return nil
}

// splitPragmas 把单行 PRAGMA 语句从迁移 SQL 中分离出来，返回
// （pragma 语句列表，其余 SQL）。其余部分保持原文不动。
func splitPragmas(sqlText string) (pragmas []string, rest string) {
	var restLines []string
	for _, line := range strings.Split(sqlText, "\n") {
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(line)), "PRAGMA") {
			pragmas = append(pragmas, strings.TrimSpace(line))
			continue
		}
		restLines = append(restLines, line)
	}
	return pragmas, strings.Join(restLines, "\n")
}
