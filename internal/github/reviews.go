package github

import (
	"context"
	"fmt"
	"net/http"
)

type PullRequestReview struct {
	ID       uint64 `json:"id"`
	Body     string `json:"body"`
	CommitID string `json:"commit_id"`
	State    string `json:"state"`
}

// Fail closed if the bounded listing is incomplete: absence cannot be inferred.
func (c *Client) ListPullRequestReviews(ctx context.Context, owner, repo string, number int) ([]PullRequestReview, error) {
	var reviews []PullRequestReview
	for page := 1; page <= 100; page++ {
		var batch []PullRequestReview
		path := fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews?per_page=100&page=%d", owner, repo, number, page)
		if err := c.do(ctx, http.MethodGet, path, nil, &batch); err != nil {
			return nil, err
		}
		reviews = append(reviews, batch...)
		if len(batch) < 100 {
			return reviews, nil
		}
	}
	return nil, fmt.Errorf("review listing exceeded 100 pages; refusing to assume no existing review")
}

func (c *Client) CreatePullRequestReviewAtCommit(ctx context.Context, owner, repo string, number int, commitSHA, body string) (uint64, error) {
	if commitSHA == "" {
		return 0, fmt.Errorf("review commit SHA is required")
	}
	var review PullRequestReview
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews", owner, repo, number)
	err := c.do(ctx, http.MethodPost, path, map[string]string{"body": body, "event": "COMMENT", "commit_id": commitSHA}, &review)
	if err != nil {
		return 0, err
	}
	if review.ID == 0 {
		return 0, fmt.Errorf("GitHub returned no review ID; reconcile before retrying")
	}
	return review.ID, nil
}
