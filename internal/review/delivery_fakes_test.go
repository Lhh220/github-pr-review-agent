package review

import (
	"context"
	"github.com/liaohonghui/github-pr-review-agent/internal/github"
	"github.com/liaohonghui/github-pr-review-agent/internal/store"
)

func (f *fakeResultStore) GetReviewResultByTaskID(_ context.Context, _ uint64) (*store.ReviewResult, error) {
	if f.input.TaskID == 0 {
		return nil, store.ErrReviewResultNotFound
	}
	return &store.ReviewResult{TaskID: f.input.TaskID, Summary: f.input.Summary, Findings: f.input.Findings}, nil
}
func (f *fakeResultStore) GetReviewDelivery(context.Context, uint64) (*store.ReviewDelivery, error) {
	if f.delivery == nil {
		return nil, store.ErrReviewDeliveryNotFound
	}
	return f.delivery, nil
}
func (f *fakeResultStore) PrepareReviewDelivery(_ context.Context, id uint64, sha string) (*store.ReviewDelivery, error) {
	if f.delivery == nil {
		f.delivery = &store.ReviewDelivery{TaskID: id, CommitSHA: sha, Marker: "test-marker"}
	}
	return f.delivery, nil
}
func (f *fakeResultStore) MarkReviewDelivered(_ context.Context, _ uint64, id uint64) error {
	f.delivery.GitHubReviewID = id
	return nil
}

func (f *fakeAgentStore) GetReviewResultByTaskID(context.Context, uint64) (*store.ReviewResult, error) {
	if f.result.TaskID == 0 {
		return nil, store.ErrReviewResultNotFound
	}
	return &store.ReviewResult{TaskID: f.result.TaskID, Summary: f.result.Summary, Findings: f.result.Findings}, nil
}
func (f *fakeAgentStore) GetReviewDelivery(context.Context, uint64) (*store.ReviewDelivery, error) {
	if f.delivery == nil {
		return nil, store.ErrReviewDeliveryNotFound
	}
	return f.delivery, nil
}
func (f *fakeAgentStore) PrepareReviewDelivery(_ context.Context, id uint64, sha string) (*store.ReviewDelivery, error) {
	if f.delivery == nil {
		f.delivery = &store.ReviewDelivery{TaskID: id, CommitSHA: sha, Marker: "test-marker"}
	}
	return f.delivery, nil
}
func (f *fakeAgentStore) MarkReviewDelivered(_ context.Context, _ uint64, id uint64) error {
	f.delivery.GitHubReviewID = id
	return nil
}

func (f *fakeGitHubClient) ListPullRequestReviews(context.Context, string, string, int) ([]github.PullRequestReview, error) {
	return nil, nil
}
func (f *fakeGitHubClient) CreatePullRequestReviewAtCommit(ctx context.Context, o, r string, n int, sha, body string) (uint64, error) {
	return 1, f.CreatePullRequestReview(ctx, o, r, n, body)
}
func (f *fakeAgentGitHubClient) ListPullRequestReviews(context.Context, string, string, int) ([]github.PullRequestReview, error) {
	return nil, nil
}
func (f *fakeAgentGitHubClient) CreatePullRequestReviewAtCommit(ctx context.Context, o, r string, n int, sha, body string) (uint64, error) {
	return 1, f.CreatePullRequestReview(ctx, o, r, n, body)
}
