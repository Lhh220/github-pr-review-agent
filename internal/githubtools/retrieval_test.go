package githubtools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestRetrievalRankEvidenceCacheAndBounds(t *testing.T) {
	client := newFakeClient()
	client.tarball = gzipTarballForTest(t, map[string]string{
		"a.go":                    "package example\n// session only\n",
		"session/cache.go":        "package session\nvar cache = make(map[string]string)\n",
		"vendor/session/cache.go": "session cache\n",
		"binary.go":               "session cache\x00",
		"eval/report-live.json":   "session cache",
	})
	toolkit := NewToolkit(client, "owner", "repo", 1, Options{EnableRetrieval: true})
	defer toolkit.Close()
	tool := toolByName(t, toolkit, "retrieve_code_context")
	for i := 0; i < 2; i++ {
		raw, err := tool.Execute(context.Background(), map[string]any{"query": "session cache", "top_k": 1})
		if err != nil {
			t.Fatal(err)
		}
		var got retrievalOutput
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Chunks) != 1 || got.Chunks[0].Path != "session/cache.go" || !got.ResultsTruncated || got.ScanTruncated {
			t.Fatalf("unexpected ranking: %s", raw)
		}
		if got.Ref != "head-sha" || got.Matches[1].Line != 2 || got.Matches[1].Snippet != "var cache = make(map[string]string)" {
			t.Fatalf("bad evidence: %s", raw)
		}
	}
	if client.tarballCalls != 1 || client.tarballRef != "head-sha" {
		t.Fatal("archive not cached at head")
	}
	for _, input := range []map[string]any{{"query": ""}, {"query": strings.Repeat("a", 257)}, {"query": "a b c d e f g h i"}, {"query": "a", "top_k": 0}, {"query": "a", "top_k": 1.5}} {
		if _, err := tool.Execute(context.Background(), input); err == nil {
			t.Fatalf("accepted %v", input)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := tool.Execute(ctx, map[string]any{"query": "session"}); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestRetrievalOutputBudgetAndExactLines(t *testing.T) {
	client := newFakeClient()
	client.tarball = gzipTarballForTest(t, map[string]string{"a.go": strings.Repeat("// keyword "+strings.Repeat("x", 200)+"\n", 100), "huge.go": "keyword " + strings.Repeat("x", 20000)})
	toolkit := NewToolkit(client, "o", "r", 1, Options{EnableRetrieval: true})
	defer toolkit.Close()
	raw, err := toolByName(t, toolkit, "retrieve_code_context").Execute(context.Background(), map[string]any{"query": "keyword", "top_k": 5})
	if err != nil {
		t.Fatal(err)
	}
	var got retrievalOutput
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if len(raw) > retrievalOutputBytes || !got.ResultsTruncated || len(got.Matches) == 0 {
		t.Fatalf("bad bounded output: %d bytes", len(raw))
	}
	for _, m := range got.Matches {
		if m.Path != "a.go" || m.Snippet != "// keyword "+strings.Repeat("x", 200) {
			t.Fatal("partial or incorrect source evidence")
		}
	}
}

func TestRetrievalCorruptArchiveFails(t *testing.T) {
	client := newFakeClient()
	client.tarball = strings.NewReader("not gzip")
	toolkit := NewToolkit(client, "o", "r", 1, Options{EnableRetrieval: true})
	defer toolkit.Close()
	if _, err := toolByName(t, toolkit, "retrieve_code_context").Execute(context.Background(), map[string]any{"query": "cache"}); err == nil {
		t.Fatal("accepted corrupt archive")
	}
}

func TestRetrievalScanLimitsAndSHAIsolation(t *testing.T) {
	for _, largeFile := range []bool{false, true} {
		files := map[string]string{}
		if largeFile {
			files["large.go"] = strings.Repeat("x", maxSearchFileBytes+1)
		} else {
			for i := 0; i < maxSearchFiles+1; i++ {
				files[fmt.Sprintf("file%04d.go", i)] = "package example"
			}
		}
		client := newFakeClient()
		client.pr.Head.SHA = "other-head"
		client.tarball = gzipTarballForTest(t, files)
		toolkit := NewToolkit(client, "o", "r", 1, Options{EnableRetrieval: true})
		raw, err := toolByName(t, toolkit, "retrieve_code_context").Execute(context.Background(), map[string]any{"query": "missing"})
		toolkit.Close()
		if err != nil {
			t.Fatal(err)
		}
		var got retrievalOutput
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatal(err)
		}
		if !got.ScanTruncated || len(got.Matches) != 0 || got.Ref != "other-head" || client.tarballRef != "other-head" {
			t.Fatalf("lost scan coverage: %s", raw)
		}
	}
}
