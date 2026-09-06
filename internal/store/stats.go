package store

import (
	"context"
	"fmt"
	"time"
)

type StatsFilter struct {
	Repo string
}

type TaskStats struct {
	TotalTasks        int64          `json:"total_tasks"`
	StatusCounts      map[string]int `json:"status_counts"`
	CompletedTasks    int64          `json:"completed_tasks"`
	DoneTasks         int64          `json:"done_tasks"`
	FailedTasks       int64          `json:"failed_tasks"`
	DeadLetterTasks   int64          `json:"dead_letter_tasks"`
	SuccessRate       float64        `json:"success_rate"`
	RetryEvents       int64          `json:"retry_events"`
	AvgTaskDurationMS float64        `json:"avg_task_duration_ms"`
	ReviewResults     int64          `json:"review_results"`
	TotalFindings     int64          `json:"total_findings"`
	InputTokens       int64          `json:"input_tokens"`
	OutputTokens      int64          `json:"output_tokens"`
	TotalTokens       int64          `json:"total_tokens"`
	AvgLLMDurationMS  float64        `json:"avg_llm_duration_ms"`
	GeneratedAt       time.Time      `json:"generated_at"`
}

func (s *Store) GetTaskStats(ctx context.Context, filter StatsFilter) (*TaskStats, error) {
	stats := &TaskStats{
		StatusCounts: map[string]int{},
		GeneratedAt:  time.Now(),
	}

	taskWhere := "1 = 1"
	taskArgs := []any{}
	if filter.Repo != "" {
		taskWhere = "repo = ?"
		taskArgs = append(taskArgs, filter.Repo)
	}

	err := s.db.QueryRowContext(ctx, `
SELECT
    COUNT(*),
    COALESCE(SUM(status = 'done'), 0),
    COALESCE(SUM(status = 'failed'), 0),
    COALESCE(SUM(status = 'dead_letter'), 0),
    COALESCE(SUM(status IN ('done', 'failed', 'dead_letter')), 0),
    COALESCE(AVG(CASE WHEN status IN ('done', 'failed', 'dead_letter')
        THEN TIMESTAMPDIFF(MICROSECOND, created_at, updated_at) / 1000 END), 0)
FROM review_task
WHERE `+taskWhere, taskArgs...).Scan(
		&stats.TotalTasks,
		&stats.DoneTasks,
		&stats.FailedTasks,
		&stats.DeadLetterTasks,
		&stats.CompletedTasks,
		&stats.AvgTaskDurationMS,
	)
	if err != nil {
		return nil, fmt.Errorf("get task stats: %w", err)
	}
	if stats.CompletedTasks > 0 {
		stats.SuccessRate = float64(stats.DoneTasks) / float64(stats.CompletedTasks)
	}

	statusRows, err := s.db.QueryContext(ctx, `
SELECT status, COUNT(*)
FROM review_task
WHERE `+taskWhere+`
GROUP BY status`, taskArgs...)
	if err != nil {
		return nil, fmt.Errorf("get task status counts: %w", err)
	}
	defer statusRows.Close()
	for statusRows.Next() {
		var status string
		var count int
		if err := statusRows.Scan(&status, &count); err != nil {
			return nil, fmt.Errorf("scan task status count: %w", err)
		}
		stats.StatusCounts[status] = count
	}
	if err := statusRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate task status counts: %w", err)
	}

	auditWhere := "a.action = ? AND a.new_status = 'retrying'"
	auditArgs := []any{AuditActionTaskStatusChanged}
	if filter.Repo != "" {
		auditWhere += " AND t.repo = ?"
		auditArgs = append(auditArgs, filter.Repo)
	}
	err = s.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM audit_log a
JOIN review_task t ON t.id = a.task_id
WHERE `+auditWhere, auditArgs...).Scan(&stats.RetryEvents)
	if err != nil {
		return nil, fmt.Errorf("get retry event stats: %w", err)
	}

	resultWhere := "1 = 1"
	resultArgs := []any{}
	if filter.Repo != "" {
		resultWhere = "t.repo = ?"
		resultArgs = append(resultArgs, filter.Repo)
	}
	err = s.db.QueryRowContext(ctx, `
SELECT
    COUNT(*),
    COALESCE(SUM(JSON_LENGTH(r.payload_json)), 0),
    COALESCE(SUM(r.input_tokens), 0),
    COALESCE(SUM(r.output_tokens), 0),
    COALESCE(SUM(r.total_tokens), 0),
    COALESCE(AVG(r.llm_duration_ms), 0)
FROM review_result r
JOIN review_task t ON t.id = r.task_id
WHERE `+resultWhere, resultArgs...).Scan(
		&stats.ReviewResults,
		&stats.TotalFindings,
		&stats.InputTokens,
		&stats.OutputTokens,
		&stats.TotalTokens,
		&stats.AvgLLMDurationMS,
	)
	if err != nil {
		return nil, fmt.Errorf("get review result stats: %w", err)
	}
	return stats, nil
}
