package review

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/liaohonghui/github-pr-review-agent/internal/github"
	"github.com/liaohonghui/github-pr-review-agent/internal/store"
)

type ReviewPublisher interface {
	GetPullRequest(context.Context, string, string, int) (*github.PullRequest, error)
	ListPullRequestReviews(context.Context, string, string, int) ([]github.PullRequestReview, error)
	CreatePullRequestReviewAtCommit(context.Context, string, string, int, string, string) (uint64, error)
}

// The worker holds the PR lock across generation, reconciliation and publishing.
func resumeReview(ctx context.Context, gh ReviewPublisher, db ResultStore, owner, repo string, number int, taskID uint64) (*github.PullRequest, bool, error) {
	result, err := db.GetReviewResultByTaskID(ctx, taskID)
	if err != nil && !errors.Is(err, store.ErrReviewResultNotFound) {
		return nil, false, err
	}
	if err == nil {
		delivery, err := db.GetReviewDelivery(ctx, taskID)
		if err != nil {
			return nil, false, fmt.Errorf("saved review has no readable delivery snapshot; manual reconciliation required: %w", err)
		}
		return nil, true, publishSavedReview(ctx, gh, db, owner, repo, number, result, delivery)
	}
	pr, err := gh.GetPullRequest(ctx, owner, repo, number)
	if err != nil {
		return nil, false, err
	}
	delivery, err := db.PrepareReviewDelivery(ctx, taskID, pr.Head.SHA)
	if err != nil {
		return nil, false, err
	}
	if delivery.CommitSHA != pr.Head.SHA {
		return nil, false, fmt.Errorf("PR head changed since task snapshot; refusing to mix commits")
	}
	return pr, false, nil
}

func finishReview(ctx context.Context, gh ReviewPublisher, db ResultStore, owner, repo string, number int, input store.NewReviewResult) error {
	delivery, err := db.GetReviewDelivery(ctx, input.TaskID)
	if err != nil {
		return err
	}
	// Use a fresh API read, not the tool cache, to detect a push during analysis.
	pr, err := gh.GetPullRequest(ctx, owner, repo, number)
	if err != nil {
		return err
	}
	if pr.Head.SHA != delivery.CommitSHA {
		return fmt.Errorf("PR head changed during review; result not published")
	}
	result, err := db.CreateReviewResult(ctx, input)
	if err != nil {
		return fmt.Errorf("persist review before publishing: %w", err)
	}
	return publishSavedReview(ctx, gh, db, owner, repo, number, result, delivery)
}

func publishSavedReview(ctx context.Context, gh ReviewPublisher, db ResultStore, owner, repo string, number int, result *store.ReviewResult, delivery *store.ReviewDelivery) error {
	if delivery.GitHubReviewID != 0 {
		return nil
	}
	if delivery.Marker == "" || delivery.CommitSHA == "" {
		return fmt.Errorf("invalid review delivery snapshot")
	}
	marker := "<!-- pr-review-agent:" + delivery.Marker + " -->"
	reviews, err := gh.ListPullRequestReviews(ctx, owner, repo, number)
	if err != nil {
		return fmt.Errorf("reconcile existing reviews: %w", err)
	}
	for _, review := range reviews {
		if review.ID != 0 && review.State == "COMMENTED" && review.CommitID == delivery.CommitSHA && strings.Contains(review.Body, marker) {
			return db.MarkReviewDelivered(ctx, result.TaskID, review.ID)
		}
	}
	body := buildReviewComment(*result, result.TaskID, delivery.CommitSHA) + "\n\n" + marker
	id, err := gh.CreatePullRequestReviewAtCommit(ctx, owner, repo, number, delivery.CommitSHA, body)
	if err != nil {
		return fmt.Errorf("publish review outcome may be unknown; reconcile on retry: %w", err)
	}
	return db.MarkReviewDelivered(ctx, result.TaskID, id)
}
