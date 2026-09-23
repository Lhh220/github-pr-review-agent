package review

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liaohonghui/github-pr-review-agent/internal/github"
)

type fakeContentClient struct {
	mu       sync.Mutex
	contents map[string]string
	errs     map[string]error
	calls    atomic.Int32
	inFlight atomic.Int32
	maxSeen  atomic.Int32
	started  chan struct{}
	release  chan struct{}
}

func (f *fakeContentClient) GetPullRequest(ctx context.Context, owner, repo string, number int) (*github.PullRequest, error) {
	return nil, nil
}

func (f *fakeContentClient) GetPullRequestFiles(ctx context.Context, owner, repo string, number int) ([]github.PullRequestFile, bool, error) {
	return nil, false, nil
}

func (f *fakeContentClient) CreatePullRequestReview(ctx context.Context, owner, repo string, number int, body string) error {
	return nil
}

func (f *fakeContentClient) ListPullRequestReviews(ctx context.Context, owner, repo string, number int) ([]github.PullRequestReview, error) {
	return nil, nil
}

func (f *fakeContentClient) CreatePullRequestReviewAtCommit(ctx context.Context, owner, repo string, number int, commitSHA, body string) (uint64, error) {
	return 0, nil
}

func (f *fakeContentClient) GetFileContent(ctx context.Context, owner, repo, path, ref string) (string, error) {
	current := f.inFlight.Add(1)
	for {
		seen := f.maxSeen.Load()
		if current <= seen || f.maxSeen.CompareAndSwap(seen, current) {
			break
		}
	}
	if f.started != nil {
		f.started <- struct{}{}
		select {
		case <-f.release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	f.inFlight.Add(-1)
	f.calls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, failed := f.errs[path]; failed {
		return "", err
	}
	return f.contents[path], nil
}

func contentsFor(paths ...string) []github.PullRequestFile {
	files := make([]github.PullRequestFile, 0, len(paths))
	for _, path := range paths {
		files = append(files, github.PullRequestFile{Filename: path})
	}
	return files
}

func TestFetchFileContentsRunsConcurrently(t *testing.T) {
	paths := []string{"a.go", "b.go", "c.go", "d.go"}
	client := &fakeContentClient{
		contents: map[string]string{
			"a.go": "package a", "b.go": "package b", "c.go": "package c", "d.go": "package d",
		},
		started: make(chan struct{}, 8),
		release: make(chan struct{}),
	}
	service := New(client, nil, nil, 0, 10, 20)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan []github.FileContent, 1)
	go func() { done <- service.fetchFileContents(ctx, "owner", "repo", "ref", contentsFor(paths...)) }()
	for i := 0; i < 4; i++ {
		select {
		case <-client.started:
		case <-ctx.Done():
			t.Fatal("four reads did not reach barrier")
		}
	}
	close(client.release)
	var got []github.FileContent
	select {
	case got = <-done:
	case <-ctx.Done():
		t.Fatal("reads did not finish")
	}

	if len(got) != 4 {
		t.Fatalf("fetched %d files, want 4", len(got))
	}
	for index, path := range paths {
		if got[index].Path != path {
			t.Fatalf("result order broken: index %d = %s, want %s", index, got[index].Path, path)
		}
	}
	if client.maxSeen.Load() < 2 {
		t.Fatalf("max concurrent fetches = %d, want >= 2 (fetches are still serial)", client.maxSeen.Load())
	}
	if client.maxSeen.Load() > 4 {
		t.Fatal("exceeded concurrency limit")
	}

}

func TestFetchFileContentsSkipsFailuresAndKeepsOrder(t *testing.T) {
	client := &fakeContentClient{
		contents: map[string]string{
			"a.go": "package a",
			"c.go": "   ",
			"e.go": "package e",
		},
		errs: map[string]error{"b.go": errors.New("boom"), "d.go": errors.New("boom")},
	}
	service := New(client, nil, nil, 0, 10, 20)

	got := service.fetchFileContents(context.Background(), "owner", "repo", "ref",
		contentsFor("a.go", "b.go", "c.go", "d.go", "e.go", "f.png"))

	if len(got) != 2 || got[0].Path != "a.go" || got[1].Path != "e.go" {
		t.Fatalf("unexpected results: %+v", got)
	}
	if got[0].Content != "package a" || got[1].Content != "package e" {
		t.Fatalf("unexpected contents: %+v", got)
	}
}

func TestFetchFileContentsCapsReadableFiles(t *testing.T) {
	client := &fakeContentClient{
		contents: map[string]string{
			"a.go": "1", "b.go": "2", "c.go": "3",
		},
	}
	service := New(client, nil, nil, 0, 2, 20)

	got := service.fetchFileContents(context.Background(), "owner", "repo", "ref", contentsFor("a.go", "b.go", "c.go"))
	if len(got) != 2 || got[0].Path != "a.go" || got[1].Path != "b.go" {
		t.Fatalf("cap not applied in order: %+v", got)
	}
	if got := client.calls.Load(); got != 2 {
		t.Fatalf("GetFileContent calls = %d, want 2", got)
	}
}

func TestFetchFileContentsZeroCapFetchesNothing(t *testing.T) {
	client := &fakeContentClient{contents: map[string]string{"a.go": "1"}}
	service := New(client, nil, nil, 0, 0, 20)

	if got := service.fetchFileContents(context.Background(), "owner", "repo", "ref", contentsFor("a.go")); len(got) != 0 {
		t.Fatalf("expected no fetches, got %+v", got)
	}
	if got := client.calls.Load(); got != 0 {
		t.Fatalf("GetFileContent calls = %d, want 0", got)
	}
}

func TestFetchFileContentsReplenishesFailedAndEmptyFiles(t *testing.T) {
	c := &fakeContentClient{contents: map[string]string{"b.go": " ", "c.go": "package c", "d.go": "package d", "e.go": "package e"}, errs: map[string]error{"a.go": errors.New("removed")}}
	s := New(c, nil, nil, 0, 2, 20)
	got := s.fetchFileContents(context.Background(), "o", "r", "head", contentsFor("a.go", "b.go", "c.go", "d.go", "e.go"))
	if len(got) != 2 || got[0].Path != "c.go" || got[1].Path != "d.go" || c.calls.Load() != 4 {
		t.Fatalf("got=%+v calls=%d", got, c.calls.Load())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.fetchFileContents(ctx, "o", "r", "head", contentsFor("e.go"))
	if c.calls.Load() != 4 {
		t.Fatal("read after cancellation")
	}
}
