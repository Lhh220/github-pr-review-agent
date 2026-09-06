package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	AuditActionTaskCreated         = "task_created"
	AuditActionTaskStatusChanged   = "task_status_changed"
	AuditActionReviewResultCreated = "review_result_created"
)

type AuditLog struct {
	ID        uint64         `json:"id"`
	TaskID    uint64         `json:"task_id"`
	Repo      string         `json:"repo"`
	PRNumber  int            `json:"pr_number"`
	Action    string         `json:"action"`
	OldStatus string         `json:"old_status,omitempty"`
	NewStatus string         `json:"new_status,omitempty"`
	Detail    map[string]any `json:"detail"`
	CreatedAt time.Time      `json:"created_at"`
}

type AuditFilter struct {
	TaskID uint64
	Action string
	Limit  int
}

type auditExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func insertAuditLog(
	ctx context.Context,
	db auditExecer,
	taskID uint64,
	action, oldStatus, newStatus string,
	detail any,
) error {
	if taskID == 0 {
		return errors.New("audit task id is required")
	}
	if strings.TrimSpace(action) == "" {
		return errors.New("audit action is required")
	}

	payload, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("marshal audit detail: %w", err)
	}
	if string(payload) == "null" {
		payload = []byte(`{}`)
	}

	_, err = db.ExecContext(ctx, `
INSERT INTO audit_log (task_id, action, old_status, new_status, detail_json)
VALUES (?, ?, ?, ?, ?)`,
		taskID,
		action,
		nullableString(oldStatus),
		nullableString(newStatus),
		payload,
	)
	if err != nil {
		return fmt.Errorf("insert audit log: %w", err)
	}
	return nil
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func (s *Store) ListAuditLogs(ctx context.Context, filter AuditFilter) ([]AuditLog, error) {
	where := []string{"1 = 1"}
	args := []any{}
	if filter.TaskID > 0 {
		where = append(where, "a.task_id = ?")
		args = append(args, filter.TaskID)
	}
	if filter.Action != "" {
		where = append(where, "a.action = ?")
		args = append(args, filter.Action)
	}
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 50
	}
	args = append(args, filter.Limit)

	rows, err := s.db.QueryContext(ctx, `
SELECT a.id, a.task_id, t.repo, t.pr_number, a.action,
       COALESCE(a.old_status, ''), COALESCE(a.new_status, ''), a.detail_json, a.created_at
FROM audit_log a
JOIN review_task t ON t.id = a.task_id
WHERE `+strings.Join(where, " AND ")+`
ORDER BY a.id DESC
LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("list audit logs: %w", err)
	}
	defer rows.Close()

	logs := make([]AuditLog, 0)
	for rows.Next() {
		log, err := scanAuditLog(rows.Scan)
		if err != nil {
			return nil, err
		}
		logs = append(logs, *log)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate audit logs: %w", err)
	}
	return logs, nil
}

func scanAuditLog(scan func(dest ...any) error) (*AuditLog, error) {
	var log AuditLog
	var payload []byte
	err := scan(
		&log.ID,
		&log.TaskID,
		&log.Repo,
		&log.PRNumber,
		&log.Action,
		&log.OldStatus,
		&log.NewStatus,
		&payload,
		&log.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("audit log not found")
	}
	if err != nil {
		return nil, fmt.Errorf("scan audit log: %w", err)
	}
	if err := json.Unmarshal(payload, &log.Detail); err != nil {
		return nil, fmt.Errorf("unmarshal audit detail: %w", err)
	}
	if log.Detail == nil {
		log.Detail = map[string]any{}
	}
	return &log, nil
}
