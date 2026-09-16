package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReviewPaginationAndPinnedCreate(t *testing.T) {
	pages := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["commit_id"] != "head" || body["event"] != "COMMENT" || body["body"] != "review" {
				t.Errorf("payload=%v", body)
			}
			json.NewEncoder(w).Encode(PullRequestReview{ID: 123})
			return
		}
		pages++
		if r.URL.Query().Get("per_page") != "100" {
			t.Error("page size missing")
		}
		if r.URL.Query().Get("page") == "1" {
			json.NewEncoder(w).Encode(make([]PullRequestReview, 100))
			return
		}
		json.NewEncoder(w).Encode([]PullRequestReview{{ID: 99, Body: "marker", CommitID: "head", State: "COMMENTED"}})
	}))
	defer server.Close()
	c := NewClient("token")
	c.baseURL = server.URL
	reviews, err := c.ListPullRequestReviews(context.Background(), "o", "r", 1)
	if err != nil || len(reviews) != 101 || pages != 2 || reviews[100].ID != 99 {
		t.Fatalf("reviews=%v pages=%d err=%v", len(reviews), pages, err)
	}
	id, err := c.CreatePullRequestReviewAtCommit(context.Background(), "o", "r", 1, "head", "review")
	if err != nil || id != 123 {
		t.Fatalf("id=%d err=%v", id, err)
	}
}

func TestReviewListingFailsClosed(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusOK} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				if status == http.StatusOK {
					json.NewEncoder(w).Encode(make([]PullRequestReview, 100))
				}
			}))
			defer server.Close()
			c := NewClient("token")
			c.baseURL = server.URL
			if _, err := c.ListPullRequestReviews(context.Background(), "o", "r", 1); err == nil {
				t.Fatal("incomplete listing accepted")
			}
		})
	}
}
