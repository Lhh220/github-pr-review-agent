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
	ID           uint64     `json:"id"`
	Repo         string     `json:"repo"`
	PRNumber     int        `json:"pr_number"`
	CommitSHA    string     `json:"commit_sha"`
	Action       string     `json:"action"`
	DeliveryID   string     `json:"delivery_id"`
	Status       string     `json:"status"`
	Error        string     `json:"error,omitempty"`
	AttemptCount int        `json:"attempt_count"`
	MaxAttempts  int        `json:"max_attempts"`
	NextRetryAt  *time.Time `json:"next_retry_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
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
	if err := Migrate(ctx, db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate mysql schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func upgradeLegacySchema(ctx context.Context, db *sql.DB) error {
	var tableCount int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM information_schema.tables
WHERE table_schema = DATABASE()
  AND table_name = 'review_task'`).Scan(&tableCount); err != nil {
		return fmt.Errorf("check legacy review_task table: %w", err)
	}
	if tableCount == 0 {
		return nil
	}
	if err := ensureRetryColumns(ctx, db); err != nil {
		return err
	}
	return ensureTimestampPrecision(ctx, db)
}

func ensureRetryColumns(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `
SELECT column_name
FROM information_schema.columns
WHERE table_schema = DATABASE()
  AND table_name = 'review_task'
  AND column_name IN ('attempt_count', 'max_attempts', 'next_retry_at')`)
	if err != nil {
		return fmt.Errorf("check review_task retry columns: %w", err)
	}
	defer rows.Close()

	existing := map[string]bool{}
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			return fmt.Errorf("scan review_task retry column: %w", err)
		}
		existing[column] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate review_task retry columns: %w", err)
	}

	alters := make([]string, 0, 3)
	if !existing["attempt_count"] {
		alters = append(alters, "ADD COLUMN attempt_count INT UNSIGNED NOT NULL DEFAULT 0")
	}
	if !existing["max_attempts"] {
		alters = append(alters, "ADD COLUMN max_attempts INT UNSIGNED NOT NULL DEFAULT 3")
	}
	if !existing["next_retry_at"] {
		alters = append(alters, "ADD COLUMN next_retry_at TIMESTAMP(3) NULL")
	}
	if len(alters) > 0 {
		if _, err := db.ExecContext(ctx, "ALTER TABLE review_task "+strings.Join(alters, ", ")); err != nil {
			return fmt.Errorf("upgrade review_task retry columns: %w", err)
		}
	}

	var retryIndex int
	err = db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM information_schema.statistics
WHERE table_schema = DATABASE()
  AND table_name = 'review_task'
  AND index_name = 'idx_review_task_retry'`).Scan(&retryIndex)
	if err != nil {
		return fmt.Errorf("check review_task retry index: %w", err)
	}
	if retryIndex == 0 {
		if _, err := db.ExecContext(ctx, "CREATE INDEX idx_review_task_retry ON review_task (status, next_retry_at)"); err != nil {
			return fmt.Errorf("create review_task retry index: %w", err)
		}
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

func (s *Store) ClaimTask(ctx context.Context, id uint64, attempt int, now time.Time) (bool, error) {
	result, err := s.db.ExecContext(ctx, `
UPDATE review_task
SET status = 'running',
    error = NULL,
    attempt_count = GREATEST(attempt_count, ?)
WHERE id = ?
  AND (
    (status IN ('queued', 'retrying') AND (next_retry_at IS NULL OR next_retry_at <= ?))
    OR (status = 'running' AND ? > attempt_count)
  )`,
		attempt,
		id,
		now,
		attempt,
	)
	if err != nil {
		return false, fmt.Errorf("claim review task: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("get claim rows affected: %w", err)
	}
	return affected == 1, nil
}

func (s *Store) MarkTaskRetry(ctx context.Context, id uint64, attempt, maxAttempts int, taskError string, nextRetryAt time.Time) error {
	result, err := s.db.ExecContext(ctx, `
UPDATE review_task
SET status = 'retrying',
    attempt_count = ?,
    max_attempts = ?,
    error = ?,
    next_retry_at = ?
WHERE id = ? AND status = 'running'`,
		attempt,
		maxAttempts,
		taskError,
		nextRetryAt,
		id,
	)
	if err != nil {
		return fmt.Errorf("mark review task retrying: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("get retry rows affected: %w", err)
	}
	if affected == 0 {
		return ErrTaskTransitionFailed
	}
	return nil
}

func (s *Store) MarkTaskDeadLetter(ctx context.Context, id uint64, attempt, maxAttempts int, taskError string) error {
	result, err := s.db.ExecContext(ctx, `
UPDATE review_task
SET status = 'dead_letter',
    attempt_count = ?,
    max_attempts = ?,
    error = ?,
    next_retry_at = NULL
WHERE id = ? AND status = 'running'`,
		attempt,
		maxAttempts,
		taskError,
		id,
	)
	if err != nil {
		return fmt.Errorf("mark review task dead letter: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("get dead letter rows affected: %w", err)
	}
	if affected == 0 {
		return ErrTaskTransitionFailed
	}
	return nil
}

func (s *Store) RequeueTask(ctx context.Context, id uint64) error {
	result, err := s.db.ExecContext(ctx, `
UPDATE review_task
SET status = 'queued',
    attempt_count = 0,
    next_retry_at = NULL
WHERE id = ? AND status = 'dead_letter'`,
		id,
	)
	if err != nil {
		return fmt.Errorf("requeue dead letter task: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("get requeue rows affected: %w", err)
	}
	if affected == 0 {
		if _, err := s.GetTask(ctx, id); err != nil {
			return err
		}
		return ErrTaskTransitionFailed
	}
	return nil
}

func (s *Store) TouchQueuedTask(ctx context.Context, id uint64, updatedAt time.Time) error {
	result, err := s.db.ExecContext(ctx, `
UPDATE review_task
SET updated_at = ?
WHERE id = ? AND status = 'queued'`,
		updatedAt,
		id,
	)
	if err != nil {
		return fmt.Errorf("touch queued review task: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("get queued touch rows affected: %w", err)
	}
	if affected == 0 {
		return ErrTaskTransitionFailed
	}
	return nil
}

func (s *Store) GetTask(ctx context.Context, id uint64) (*Task, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, repo, pr_number, commit_sha, action, delivery_id, status,
       COALESCE(error, ''), attempt_count, max_attempts, next_retry_at, created_at, updated_at
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
       COALESCE(error, ''), attempt_count, max_attempts, next_retry_at, created_at, updated_at
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

func (s *Store) ListRecoverableTasks(ctx context.Context, staleBefore time.Time, limit int) ([]Task, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, repo, pr_number, commit_sha, action, delivery_id, status,
       COALESCE(error, ''), attempt_count, max_attempts, next_retry_at, created_at, updated_at
FROM review_task
WHERE status = 'running' AND updated_at < ?
ORDER BY updated_at ASC
LIMIT ?`, staleBefore, limit)
	if err != nil {
		return nil, fmt.Errorf("list recoverable review tasks: %w", err)
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
		return nil, fmt.Errorf("iterate recoverable review tasks: %w", err)
	}
	return tasks, nil
}

func (s *Store) ListStaleQueuedTasks(ctx context.Context, staleBefore time.Time, limit int) ([]Task, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, repo, pr_number, commit_sha, action, delivery_id, status,
       COALESCE(error, ''), attempt_count, max_attempts, next_retry_at, created_at, updated_at
FROM review_task
WHERE status = 'queued' AND updated_at < ?
ORDER BY updated_at ASC
LIMIT ?`, staleBefore, limit)
	if err != nil {
		return nil, fmt.Errorf("list stale queued review tasks: %w", err)
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
		return nil, fmt.Errorf("iterate stale queued review tasks: %w", err)
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

var (
	ErrTaskNotFound         = errors.New("review task not found")
	ErrTaskTransitionFailed = errors.New("review task transition failed")
)

func scanTask(scan func(dest ...any) error) (*Task, error) {
	var task Task
	var nextRetryAt sql.NullTime
	err := scan(
		&task.ID,
		&task.Repo,
		&task.PRNumber,
		&task.CommitSHA,
		&task.Action,
		&task.DeliveryID,
		&task.Status,
		&task.Error,
		&task.AttemptCount,
		&task.MaxAttempts,
		&nextRetryAt,
		&task.CreatedAt,
		&task.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTaskNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan review task: %w", err)
	}
	if nextRetryAt.Valid {
		next := nextRetryAt.Time
		task.NextRetryAt = &next
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
