package audit

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
)

type LogEntry struct {
	ID         string    `json:"id"`
	Actor      string    `json:"actor"`
	Action     string    `json:"action"`
	Result     string    `json:"result"` // 响应期派生：action 以 _failed 结尾 → failed，其余 → success
	TargetType string    `json:"target_type"`
	TargetID   string    `json:"target_id"`
	Detail     string    `json:"detail"`
	IPAddress  string    `json:"ip_address"`
	CreatedAt  time.Time `json:"created_at"`
}

// ResultOf 按契约 §9.1 从事件名派生结果：_failed 结尾 → failed，否则 success。
func ResultOf(action string) string {
	if strings.HasSuffix(action, "_failed") {
		return "failed"
	}
	return "success"
}

type Logger struct {
	db *sql.DB
}

func NewLogger(db *sql.DB) *Logger {
	return &Logger{db: db}
}

// Record 写一条审计日志。写入失败返回错误，调用方须记系统日志，
// 不得静默丢弃（契约 §9.1）。
func (l *Logger) Record(actor, action, targetType, targetID, detail, ip string) error {
	_, err := l.db.Exec(
		`INSERT INTO audit_logs (id, actor, action, target_type, target_id, detail, ip_address, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		uuid.NewString(), actor, action, targetType, targetID, detail, ip, time.Now().UTC(),
	)
	return err
}

// Filter 是 GET /api/v1/logs 的筛选条件（契约 §9.1），全部可选。
type Filter struct {
	From       *time.Time // 含端点
	To         *time.Time // 含端点
	Actions    []string   // 精确匹配，多值 OR
	TargetType string
	TargetID   string
	Actor      string
	Result     string // success | failed（按 action 后缀派生）
}

// ListFiltered 按筛选条件分页查询审计日志（created_at DESC），
// 返回本页条目与过滤后的总数。page 从 1 开始；pageSize 超出 1–200
// 时钳制为 50 / 200（契约 §1.4）。
func (l *Logger) ListFiltered(f Filter, page, pageSize int) ([]LogEntry, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 200 {
		pageSize = 200
	}
	where, args := buildFilterWhere(f)
	var total int
	if err := l.db.QueryRow(`SELECT COUNT(*) FROM audit_logs`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := l.db.Query(
		`SELECT id, actor, action, target_type, target_id, detail, ip_address, created_at
		 FROM audit_logs`+where+` ORDER BY created_at DESC LIMIT ? OFFSET ?`,
		append(args, pageSize, (page-1)*pageSize)...,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	logs := []LogEntry{}
	for rows.Next() {
		var item LogEntry
		if err := rows.Scan(&item.ID, &item.Actor, &item.Action, &item.TargetType, &item.TargetID, &item.Detail, &item.IPAddress, &item.CreatedAt); err != nil {
			return nil, 0, err
		}
		item.Result = ResultOf(item.Action)
		logs = append(logs, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return logs, total, nil
}

// buildFilterWhere 把 Filter 转成 WHERE 子句与参数。
func buildFilterWhere(f Filter) (string, []any) {
	var conds []string
	var args []any
	if f.From != nil {
		conds = append(conds, `created_at >= ?`)
		args = append(args, f.From.UTC())
	}
	if f.To != nil {
		conds = append(conds, `created_at <= ?`)
		args = append(args, f.To.UTC())
	}
	if len(f.Actions) > 0 {
		placeholders := strings.Repeat("?,", len(f.Actions))
		placeholders = strings.TrimSuffix(placeholders, ",")
		conds = append(conds, `action IN (`+placeholders+`)`)
		for _, a := range f.Actions {
			args = append(args, a)
		}
	}
	if f.TargetType != "" {
		conds = append(conds, `target_type = ?`)
		args = append(args, f.TargetType)
	}
	if f.TargetID != "" {
		conds = append(conds, `target_id = ?`)
		args = append(args, f.TargetID)
	}
	if f.Actor != "" {
		conds = append(conds, `actor = ?`)
		args = append(args, f.Actor)
	}
	switch f.Result {
	case "failed":
		// LIKE 中 _ 是通配符，须转义为字面下划线。
		conds = append(conds, `action LIKE '%\_failed' ESCAPE '\'`)
	case "success":
		conds = append(conds, `action NOT LIKE '%\_failed' ESCAPE '\'`)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

func (l *Logger) ListRecent(limit int) ([]LogEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	logs, _, err := l.ListFiltered(Filter{}, 1, limit)
	return logs, err
}

// CleanupOlderThan 删除 retentionDays 天前的审计日志，返回删除条数。
// 清理只按时间（契约 §9.2：v0.2.0 不提供按条件删除日志的 API）。
func (l *Logger) CleanupOlderThan(retentionDays int) (int64, error) {
	if retentionDays <= 0 {
		retentionDays = 90
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays)
	res, err := l.db.Exec(`DELETE FROM audit_logs WHERE created_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// StartCleanup 启动定期清理（契约 §9.2，默认保留 90 天）：启动时
// 清理一次，之后每 interval 清理一次；实际删掉过期行时写一条
// audit_cleanup 系统审计（actor 为 system）。ctx 取消即停止。
func (l *Logger) StartCleanup(ctx context.Context, interval time.Duration, retentionDays int) {
	cleanupOnce := func() {
		n, err := l.CleanupOlderThan(retentionDays)
		if err != nil {
			log.Printf("[Audit] 清理过期审计日志失败: %v", err)
			return
		}
		if n > 0 {
			if err := l.Record("system", "audit_cleanup", "audit", "",
				fmt.Sprintf("清理过期审计日志 %d 条（保留 %d 天）", n, retentionDays), ""); err != nil {
				log.Printf("[Audit] 写入 audit_cleanup 审计失败: %v", err)
			}
		}
	}
	cleanupOnce()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cleanupOnce()
		}
	}
}
