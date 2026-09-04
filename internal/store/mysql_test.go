package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestMySQLTaskStore(t *testing.T) {
	dsn := os.Getenv("MYSQL_DSN")
	if dsn == "" {
		t.Skip("MYSQL_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	var createdPrecision, updatedPrecision int
	err = s.db.QueryRowContext(ctx, `
SELECT
    MAX(CASE WHEN column_name = 'created_at' THEN datetime_precision END),
    MAX(CASE WHEN column_name = 'updated_at' THEN datetime_precision END)
FROM information_schema.columns
WHERE table_schema = DATABASE()
  AND table_name = 'review_task'`).Scan(&createdPrecision, &updatedPrecision)
	if err != nil {
		t.Fatalf("check timestamp precision: %v", err)
	}
	if createdPrecision < 3 || updatedPrecision < 3 {
		t.Fatalf("timestamp precision = created:%d updated:%d, want at least 3", createdPrecision, updatedPrecision)
	}

	deliveryID := fmt.Sprintf("test-%d", time.Now().UnixNano())
	task, created, err := s.CreateTask(ctx, NewTask{
		Repo:       "Lhh220/github-pr-review-agent-test",
		PRNumber:   1,
		CommitSHA:  "0123456789abcdef0123456789abcdef01234567",
		Action:     "opened",
		DeliveryID: deliveryID,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if !created || task.Status != "received" {
		t.Fatalf("unexpected task: created=%v status=%s", created, task.Status)
	}
	defer func() {
		_, _ = s.db.Exec("DELETE FROM review_task WHERE id = ?", task.ID)
	}()

	existing, duplicate, err := s.CreateTask(ctx, NewTask{
		Repo:       "Lhh220/github-pr-review-agent-test",
		PRNumber:   1,
		CommitSHA:  "0123456789abcdef0123456789abcdef01234567",
		Action:     "opened",
		DeliveryID: deliveryID,
	})
	if err != nil {
		t.Fatalf("create duplicate task: %v", err)
	}
	if duplicate || existing.ID != task.ID {
		t.Fatalf("unexpected duplicate result: duplicate=%v existing=%d original=%d", duplicate, existing.ID, task.ID)
	}

	if err := s.UpdateTaskStatus(ctx, task.ID, "running", ""); err != nil {
		t.Fatalf("update running: %v", err)
	}
	got, err := s.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.Status != "running" {
		t.Fatalf("expected running, got %s", got.Status)
	}

	if err := s.UpdateTaskStatus(ctx, task.ID, "failed", "test failure"); err != nil {
		t.Fatalf("update failed: %v", err)
	}
	got, err = s.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("get failed task: %v", err)
	}
	if got.Status != "failed" || got.Error != "test failure" {
		t.Fatalf("unexpected failed task: %+v", got)
	}

	tasks, err := s.ListTasks(ctx, ListFilter{Repo: "Lhh220/github-pr-review-agent-test", Limit: 10})
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) == 0 {
		t.Fatal("expected at least one task")
	}
}
