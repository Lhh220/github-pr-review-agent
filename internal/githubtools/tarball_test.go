package githubtools

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestSearchReferencesReusesCachedTarball(t *testing.T) {
	client := newFakeClient()
	client.tarball = gzipTarballForTest(t, map[string]string{
		"internal/auth/token.go": "package auth\n\nfunc Token() string { return \"t\" }\n",
	})
	toolkit := NewToolkit(client, "owner", "repo", 12, Options{})
	defer toolkit.Close()

	tool := toolByName(t, toolkit, "search_references")
	for invocation := 0; invocation < 3; invocation++ {
		output, err := tool.Execute(context.Background(), map[string]any{"symbol": "Token"})
		if err != nil {
			t.Fatalf("search invocation %d failed: %v", invocation, err)
		}
		if !strings.Contains(output, "internal/auth/token.go") {
			t.Fatalf("search invocation %d missed expected match: %s", invocation, output)
		}
	}
	if client.tarballCalls != 1 {
		t.Fatalf("tarball downloads = %d, want 1", client.tarballCalls)
	}
}

func TestToolkitCloseRemovesCachedTarball(t *testing.T) {
	client := newFakeClient()
	client.tarball = gzipTarballForTest(t, map[string]string{
		"internal/auth/token.go": "package auth\n",
	})
	toolkit := NewToolkit(client, "owner", "repo", 12, Options{})

	tool := toolByName(t, toolkit, "search_references")
	if _, err := tool.Execute(context.Background(), map[string]any{"symbol": "Token"}); err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if client.tarballCalls != 1 {
		t.Fatalf("tarball downloads = %d, want 1", client.tarballCalls)
	}

	toolkit.mu.Lock()
	path := toolkit.tarballPath
	toolkit.mu.Unlock()
	if path == "" {
		t.Fatal("tarball cache path is empty before close")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("cached tarball missing before close: %v", err)
	}

	toolkit.Close()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("cached tarball still present after close: %v", err)
	}
}
