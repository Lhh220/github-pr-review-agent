package githubtools

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestStaticCancellationClosesDescendantPipes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	command := exec.CommandContext(ctx, "/bin/sh", "-c", "sleep 30 & wait")
	command.WaitDelay = 2 * time.Second
	configureStaticProcess(command)
	started := time.Now()
	_, err := command.CombinedOutput()
	if ctx.Err() == nil || err == nil {
		t.Fatalf("expected timeout cancellation, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > 1500*time.Millisecond {
		t.Fatalf("descendant retained output pipes after cancellation: %s", elapsed)
	}
}
