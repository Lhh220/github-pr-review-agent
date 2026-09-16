package githubtools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckOutputBoundsMemoryWithoutShortWrite(t *testing.T) {
	output := &boundedCheckOutput{}
	chunk := strings.Repeat("x", maxStaticCheckOutputChars+100)
	for i := 0; i < 3; i++ {
		n, err := output.Write([]byte(chunk))
		if n != len(chunk) || err != nil {
			t.Fatalf("unexpected write: %d %v", n, err)
		}
	}
	if len(output.String()) != maxStaticCheckOutputChars || !output.truncated {
		t.Fatal("unbounded output or missing truncation flag")
	}
}

func TestStaticCheckEnvironmentBoundsCompilation(t *testing.T) {
	env := staticCheckEnvironment(t.TempDir(), "https://proxy.golang.org")
	found := map[string]bool{}
	for _, v := range env {
		found[v] = true
	}
	if !found["GOFLAGS=-mod=mod -p=1"] || !found["GOMAXPROCS=2"] {
		t.Fatalf("missing concurrency limits: %v", env)
	}
}

func TestStaticDiagnosticsDoNotExposeSecrets(t *testing.T) {
	got := staticCheckDiagnosticEnvironment([]string{"GOFLAGS=-mod=mod -p=1", "GOMAXPROCS=2", "DEEPSEEK_API_KEY=secret", "MYSQL_DSN=secret"})
	if len(got) != 2 || got["GOFLAGS"] != "-mod=mod -p=1" {
		t.Fatalf("unexpected environment: %v", got)
	}
}
func TestMemoryDiagnosticsUnavailableAndBounded(t *testing.T) {
	root := t.TempDir()
	if got := readStaticCheckResources(root); len(got) != 0 {
		t.Fatalf("missing metrics treated as available: %v", got)
	}
	if err := os.WriteFile(filepath.Join(root, "memory.max"), []byte("max\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "memory.events"), []byte(strings.Repeat("x", 5000)), 0600); err != nil {
		t.Fatal(err)
	}
	got := readStaticCheckResources(root)
	if got["memory.max"] != "max" || len(got["memory.events"]) != 4096 {
		t.Fatalf("unexpected metrics: %v", got)
	}
}
