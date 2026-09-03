package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
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
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uq_review_task_delivery (delivery_id),
    KEY idx_review_task_repo_pr (repo, pr_number),
    KEY idx_review_task_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`
	_, err := db.ExecContext(ctx, query)
	return err
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
