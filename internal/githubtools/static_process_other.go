//go:build !linux

package githubtools

import "os/exec"

// Non-Linux development retains CommandContext cancellation and WaitDelay.
// Production process-group cancellation is currently supported on Linux only.
func configureStaticProcess(command *exec.Cmd) {}
