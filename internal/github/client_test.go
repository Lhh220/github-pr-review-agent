package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetPullRequestFilesPaginates(t *testing.T) {
	requestedPages := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/owner/repo/pulls/12/files" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		page := r.URL.Query().Get("page")
		requestedPages <- page

		fileCount := 2
		if page == "1" {
			fileCount = 100
		}
		files := make([]PullRequestFile, fileCount)
		for i := range files {
			files[i].Filename = "file.go"
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(files); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	defer server.Close()

	client := NewClient("test-token")
	client.baseURL = server.URL

	files, err := client.GetPullRequestFiles(context.Background(), "owner", "repo", 12)
	if err != nil {
		t.Fatalf("GetPullRequestFiles() error = %v", err)
	}
	if len(files) != 102 {
		t.Fatalf("file count = %d, want 102", len(files))
	}
	if got := <-requestedPages; got != "1" {
		t.Fatalf("first page = %s, want 1", got)
	}
	if got := <-requestedPages; got != "2" {
		t.Fatalf("second page = %s, want 2", got)
	}
}
