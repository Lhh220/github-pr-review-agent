package githubtools

import (
	"context"
	"io"
	"os"
	"path/filepath"
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

type downloadProbe struct {
	remaining, read int64
	closed          bool
}

func (p *downloadProbe) Read(b []byte) (int, error) {
	if p.remaining == 0 {
		return 0, io.EOF
	}
	n := int(min(int64(len(b)), p.remaining))
	clear(b[:n])
	p.remaining -= int64(n)
	p.read += int64(n)
	return n, nil
}
func (p *downloadProbe) Close() error { p.closed = true; return nil }

type downloadClient struct {
	*fakeClient
	probe *downloadProbe
}

func (c *downloadClient) GetRepositoryTarball(context.Context, string, string, string) (io.ReadCloser, error) {
	return c.probe, nil
}
func TestTarballDownloadLimitCleansUpAndAllowsRetry(t *testing.T) {
	dir := t.TempDir()
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(key, dir)
	}
	c := &downloadClient{fakeClient: newFakeClient(), probe: &downloadProbe{remaining: maxTarballDownloadBytes + 100}}
	toolkit := NewToolkit(c, "o", "r", 1, Options{})
	defer toolkit.Close()
	_, err := toolkit.cachedTarball(context.Background(), "sha")
	if err == nil || !strings.Contains(err.Error(), "download limit") || !c.probe.closed || c.probe.read != maxTarballDownloadBytes+1 {
		t.Fatalf("err=%v probe=%+v", err, c.probe)
	}
	paths, err := filepath.Glob(filepath.Join(dir, "pr-review-tarball-*.tgz"))
	if err != nil || len(paths) != 0 || toolkit.tarballPath != "" {
		t.Fatalf("leaked failed download: %v %v", paths, err)
	}
	// Exactly the limit is permitted and a failed download was not memoized.
	c.probe = &downloadProbe{remaining: maxTarballDownloadBytes}
	file, err := toolkit.cachedTarball(context.Background(), "sha")
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	if !c.probe.closed || c.probe.read != maxTarballDownloadBytes {
		t.Fatalf("retry probe=%+v", c.probe)
	}
}
