package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestMySQLReviewDeliveryAtomicCompletion(t *testing.T) {
	dsn := os.Getenv("MYSQL_DSN")
	if dsn == "" {
		t.Skip("MYSQL_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	task, _, err := s.CreateTask(ctx, NewTask{Repo: "test/delivery", PRNumber: 1, CommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Action: "opened", DeliveryID: fmt.Sprintf("delivery-test-%d", time.Now().UnixNano())})
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Exec("DELETE FROM review_task WHERE id = ?", task.ID)
	first, err := s.PrepareReviewDelivery(ctx, task.ID, task.CommitSHA)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.PrepareReviewDelivery(ctx, task.ID, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if err != nil {
		t.Fatal(err)
	}
	if first.Marker != again.Marker || again.CommitSHA != task.CommitSHA || len(first.Marker) != 32 {
		t.Fatalf("snapshot changed: %+v %+v", first, again)
	}
	if err := s.MarkReviewDelivered(ctx, task.ID, 123); err == nil {
		t.Fatal("completed an unclaimed task")
	}
	unchanged, err := s.GetReviewDelivery(ctx, task.ID)
	if err != nil || unchanged.GitHubReviewID != 0 {
		t.Fatalf("partial write: %+v %v", unchanged, err)
	}
	if err := s.UpdateTaskStatus(ctx, task.ID, "running", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkReviewDelivered(ctx, task.ID, 123); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkReviewDelivered(ctx, task.ID, 123); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateTaskStatus(ctx, task.ID, "done", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkReviewDelivered(ctx, task.ID, 456); err == nil {
		t.Fatal("overwrote existing review ID")
	}
	final, err := s.GetTask(ctx, task.ID)
	if err != nil || final.Status != "done" {
		t.Fatalf("task=%+v err=%v", final, err)
	}
	delivery, err := s.GetReviewDelivery(ctx, task.ID)
	if err != nil || delivery.GitHubReviewID != 123 {
		t.Fatalf("delivery=%+v err=%v", delivery, err)
	}
	var events int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log WHERE task_id = ? AND new_status = 'done'`, task.ID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("done audit count=%d want=1", events)
	}
}
