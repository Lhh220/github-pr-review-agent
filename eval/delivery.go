package eval

import (
	"context"
	"github.com/liaohonghui/github-pr-review-agent/internal/github"
	"github.com/liaohonghui/github-pr-review-agent/internal/store"
)

func (s *memoryStore) GetReviewResultByTaskID(context.Context, uint64) (*store.ReviewResult, error) {
	if s.result == nil {
		return nil, store.ErrReviewResultNotFound
	}
	return s.result, nil
}
func (s *memoryStore) GetReviewDelivery(context.Context, uint64) (*store.ReviewDelivery, error) {
	if s.delivery == nil {
		return nil, store.ErrReviewDeliveryNotFound
	}
	return s.delivery, nil
}
func (s *memoryStore) PrepareReviewDelivery(_ context.Context, id uint64, sha string) (*store.ReviewDelivery, error) {
	if s.delivery == nil {
		s.delivery = &store.ReviewDelivery{TaskID: id, CommitSHA: sha, Marker: "offline-fixture"}
	}
	return s.delivery, nil
}
func (s *memoryStore) MarkReviewDelivered(_ context.Context, _ uint64, id uint64) error {
	s.delivery.GitHubReviewID = id
	return nil
}

func (c *fixtureGitHubClient) ListPullRequestReviews(context.Context, string, string, int) ([]github.PullRequestReview, error) {
	return nil, nil
}
func (c *fixtureGitHubClient) CreatePullRequestReviewAtCommit(context.Context, string, string, int, string, string) (uint64, error) {
	return 1, nil
}
