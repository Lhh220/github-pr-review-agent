package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
)

type ReviewDelivery struct {
	TaskID         uint64 `json:"task_id"`
	CommitSHA      string `json:"commit_sha"`
	Marker         string `json:"marker"`
	GitHubReviewID uint64 `json:"github_review_id"`
}

var ErrReviewDeliveryNotFound = errors.New("review delivery not found")

func (s *Store) GetReviewDelivery(ctx context.Context, taskID uint64) (*ReviewDelivery, error) {
	var d ReviewDelivery
	err := s.db.QueryRowContext(ctx, `SELECT task_id, commit_sha, marker, github_review_id FROM review_delivery WHERE task_id = ?`, taskID).
		Scan(&d.TaskID, &d.CommitSHA, &d.Marker, &d.GitHubReviewID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrReviewDeliveryNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get review delivery: %w", err)
	}
	return &d, nil
}

func (s *Store) PrepareReviewDelivery(ctx context.Context, taskID uint64, commitSHA string) (*ReviewDelivery, error) {
	if taskID == 0 || commitSHA == "" {
		return nil, errors.New("task and commit are required for review delivery")
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return nil, err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO review_delivery (task_id, commit_sha, marker) VALUES (?, ?, ?)
ON DUPLICATE KEY UPDATE task_id = task_id`, taskID, commitSHA, hex.EncodeToString(token[:]))
	if err != nil {
		return nil, fmt.Errorf("prepare review delivery: %w", err)
	}
	return s.GetReviewDelivery(ctx, taskID)
}

func (s *Store) MarkReviewDelivered(ctx context.Context, taskID, reviewID uint64) error {
	if reviewID == 0 {
		return errors.New("GitHub review ID is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM review_task WHERE id = ? FOR UPDATE`, taskID).Scan(&status); err != nil {
		return err
	}
	if status != "running" && status != "done" {
		return ErrTaskTransitionFailed
	}
	var storedID uint64
	if err := tx.QueryRowContext(ctx, `SELECT github_review_id FROM review_delivery WHERE task_id = ? FOR UPDATE`, taskID).Scan(&storedID); err != nil {
		return err
	}
	if storedID != 0 && storedID != reviewID {
		return errors.New("conflicting GitHub review ID")
	}
	if storedID == reviewID && status == "done" {
		return tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `UPDATE review_delivery SET github_review_id = ? WHERE task_id = ?`, reviewID, taskID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE review_task SET status = 'done', error = NULL, next_retry_at = NULL WHERE id = ?`, taskID); err != nil {
		return err
	}
	if err := insertAuditLog(ctx, tx, taskID, AuditActionTaskStatusChanged, status, "done", map[string]any{"github_review_id": reviewID}); err != nil {
		return err
	}
	return tx.Commit()
}
