package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

type Task struct {
	ID         uint64    `json:"id"`
	Repo       string    `json:"repo"`
	PRNumber   int       `json:"pr_number"`
	CommitSHA  string    `json:"commit_sha"`
	Action     string    `json:"action"`
	DeliveryID string    `json:"delivery_id"`
	Status     string    `json:"status"`
	Error      string    `json:"error,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type Finding struct {
	Category   string `json:"category"`
	File       string `json:"file"`
	Line       int    `json:"line"`
	Severity   string `json:"severity"`
	Comment    string `json:"comment"`
	Suggestion string `json:"suggestion,omitempty"`
	Confidence string `json:"confidence"`
}

type ReviewResult struct {
	ID            uint64    `json:"id"`
	TaskID        uint64    `json:"task_id"`
	Summary       string    `json:"summary"`
	Findings      []Finding `json:"findings"`
	RawResponse   string    `json:"raw_response"`
	Model         string    `json:"model"`
	InputTokens   int       `json:"input_tokens"`
	OutputTokens  int       `json:"output_tokens"`
	TotalTokens   int       `json:"total_tokens"`
	LLMDurationMS int64     `json:"llm_duration_ms"`
	CreatedAt     time.Time `json:"created_at"`
}

type NewReviewResult struct {
	TaskID        uint64
	Summary       string
	Findings      []Finding
	RawResponse   string
	Model         string
	InputTokens   int
	OutputTokens  int
	TotalTokens   int
	LLMDurationMS int64
}

type NewTask struct {
	Repo       string
	PRNumber   int
	CommitSHA  string
	Action     string
	DeliveryID string
}

type ListFilter struct {
	Repo     string
	Status   string
	PRNumber int
	Limit    int
}

type Store struct {
	db *sql.DB
}

func Open(ctx context.Context, dsn string) (*Store, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping mysql: %w", err)
	}
	if err := ensureSchema(ctx, db); err != nil {
		db.Close()
		return nil, fmt.Errorf("ensure mysql schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func ensureSchema(ctx context.Context, db *sql.DB) error {
	query := `
CREATE TABLE IF NOT EXISTS review_task (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    repo VARCHAR(255) NOT NULL,
    pr_number INT UNSIGNED NOT NULL,
    commit_sha VARCHAR(40) NOT NULL,
    action VARCHAR(32) NOT NULL,
    delivery_id VARCHAR(64) NOT NULL,
    status VARCHAR(32) NOT NULL,
    error TEXT NULL,
    created_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    UNIQUE KEY uq_review_task_delivery (delivery_id),
    KEY idx_review_task_repo_pr (repo, pr_number),
    KEY idx_review_task_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`
	if _, err := db.ExecContext(ctx, query); err != nil {
		return err
	}
	if err := ensureTimestampPrecision(ctx, db); err != nil {
		return err
	}
	resultQuery := `
CREATE TABLE IF NOT EXISTS review_result (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    task_id BIGINT UNSIGNED NOT NULL,
    summary VARCHAR(2048) NOT NULL,
    payload_json JSON NOT NULL,
    raw_response MEDIUMTEXT NOT NULL,
    model VARCHAR(128) NOT NULL,
    input_tokens INT UNSIGNED NOT NULL DEFAULT 0,
    output_tokens INT UNSIGNED NOT NULL DEFAULT 0,
    total_tokens INT UNSIGNED NOT NULL DEFAULT 0,
    llm_duration_ms BIGINT UNSIGNED NOT NULL DEFAULT 0,
    created_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    UNIQUE KEY uq_review_result_task (task_id),
    CONSTRAINT fk_review_result_task
        FOREIGN KEY (task_id) REFERENCES review_task (id)
        ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`
	if _, err := db.ExecContext(ctx, resultQuery); err != nil {
		return err
	}
	return nil
}

func ensureTimestampPrecision(ctx context.Context, db *sql.DB) error {
	var impreciseColumns int
	err := db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM information_schema.columns
WHERE table_schema = DATABASE()
  AND table_name = 'review_task'
  AND column_name IN ('created_at', 'updated_at')
  AND datetime_precision < 3`).Scan(&impreciseColumns)
	if err != nil {
		return fmt.Errorf("check review_task timestamp precision: %w", err)
	}
	if impreciseColumns == 0 {
		return nil
	}
	_, err = db.ExecContext(ctx, `
ALTER TABLE review_task
    MODIFY created_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    MODIFY updated_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3)`)
	if err != nil {
		return fmt.Errorf("upgrade review_task timestamp precision: %w", err)
	}
	return nil
}

func (s *Store) CreateTask(ctx context.Context, input NewTask) (*Task, bool, error) {
	if input.DeliveryID == "" {
		input.DeliveryID = randomID()
	}
	result, err := s.db.ExecContext(ctx, `
INSERT INTO review_task
    (repo, pr_number, commit_sha, action, delivery_id, status)
VALUES (?, ?, ?, ?, ?, 'received')
ON DUPLICATE KEY UPDATE id = LAST_INSERT_ID(id)`,
		input.Repo,
		input.PRNumber,
		input.CommitSHA,
		input.Action,
		input.DeliveryID,
	)
	if err != nil {
		return nil, false, fmt.Errorf("insert review task: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, false, fmt.Errorf("get review task id: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, false, fmt.Errorf("get review task rows affected: %w", err)
	}
	task, err := s.GetTask(ctx, uint64(id))
	if err != nil {
		return nil, false, err
	}
	return task, affected == 1, nil
}

func (s *Store) UpdateTaskStatus(ctx context.Context, id uint64, status, taskError string) error {
	var errorMessage any
	if strings.TrimSpace(taskError) != "" {
		errorMessage = taskError
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE review_task
SET status = ?, error = ?
WHERE id = ?`,
		status,
		errorMessage,
		id,
	)
	if err != nil {
		return fmt.Errorf("update review task status: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("get update rows affected: %w", err)
	}
	if affected == 0 {
		return ErrTaskNotFound
	}
	return nil
}

func (s *Store) GetTask(ctx context.Context, id uint64) (*Task, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, repo, pr_number, commit_sha, action, delivery_id, status,
       COALESCE(error, ''), created_at, updated_at
FROM review_task
WHERE id = ?`, id)
	return scanTask(row.Scan)
}

func (s *Store) ListTasks(ctx context.Context, filter ListFilter) ([]Task, error) {
	where := []string{"1 = 1"}
	args := []any{}
	if filter.Repo != "" {
		where = append(where, "repo = ?")
		args = append(args, filter.Repo)
	}
	if filter.Status != "" {
		where = append(where, "status = ?")
		args = append(args, filter.Status)
	}
	if filter.PRNumber > 0 {
		where = append(where, "pr_number = ?")
		args = append(args, filter.PRNumber)
	}
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 50
	}
	args = append(args, filter.Limit)

	rows, err := s.db.QueryContext(ctx, `
SELECT id, repo, pr_number, commit_sha, action, delivery_id, status,
       COALESCE(error, ''), created_at, updated_at
FROM review_task
WHERE `+strings.Join(where, " AND ")+`
ORDER BY id DESC
LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("list review tasks: %w", err)
	}
	defer rows.Close()

	tasks := make([]Task, 0)
	for rows.Next() {
		task, err := scanTask(rows.Scan)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, *task)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate review tasks: %w", err)
	}
	return tasks, nil
}

func (s *Store) CreateReviewResult(ctx context.Context, input NewReviewResult) (*ReviewResult, error) {
	payload, err := json.Marshal(input.Findings)
	if err != nil {
		return nil, fmt.Errorf("marshal review findings: %w", err)
	}
	result, err := s.db.ExecContext(ctx, `
INSERT INTO review_result
    (task_id, summary, payload_json, raw_response, model,
     input_tokens, output_tokens, total_tokens, llm_duration_ms)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		input.TaskID,
		input.Summary,
		payload,
		input.RawResponse,
		input.Model,
		input.InputTokens,
		input.OutputTokens,
		input.TotalTokens,
		input.LLMDurationMS,
	)
	if err != nil {
		return nil, fmt.Errorf("insert review result: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("get review result id: %w", err)
	}
	return s.GetReviewResult(ctx, uint64(id))
}

func (s *Store) GetReviewResult(ctx context.Context, id uint64) (*ReviewResult, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, task_id, summary, payload_json, raw_response, model,
       input_tokens, output_tokens, total_tokens, llm_duration_ms, created_at
FROM review_result
WHERE id = ?`, id)
	return scanReviewResult(row.Scan)
}

func (s *Store) GetReviewResultByTaskID(ctx context.Context, taskID uint64) (*ReviewResult, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, task_id, summary, payload_json, raw_response, model,
       input_tokens, output_tokens, total_tokens, llm_duration_ms, created_at
FROM review_result
WHERE task_id = ?`, taskID)
	return scanReviewResult(row.Scan)
}

var ErrReviewResultNotFound = errors.New("review result not found")

func scanReviewResult(scan func(dest ...any) error) (*ReviewResult, error) {
	var result ReviewResult
	var payload []byte
	err := scan(
		&result.ID,
		&result.TaskID,
		&result.Summary,
		&payload,
		&result.RawResponse,
		&result.Model,
		&result.InputTokens,
		&result.OutputTokens,
		&result.TotalTokens,
		&result.LLMDurationMS,
		&result.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrReviewResultNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan review result: %w", err)
	}
	if err := json.Unmarshal(payload, &result.Findings); err != nil {
		return nil, fmt.Errorf("unmarshal review findings: %w", err)
	}
	if result.Findings == nil {
		result.Findings = []Finding{}
	}
	return &result, nil
}

var ErrTaskNotFound = errors.New("review task not found")

func scanTask(scan func(dest ...any) error) (*Task, error) {
	var task Task
	err := scan(
		&task.ID,
		&task.Repo,
		&task.PRNumber,
		&task.CommitSHA,
		&task.Action,
		&task.DeliveryID,
		&task.Status,
		&task.Error,
		&task.CreatedAt,
		&task.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTaskNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan review task: %w", err)
	}
	return &task, nil
}

func randomID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return hex.EncodeToString(buf)
}
