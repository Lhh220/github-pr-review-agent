package githubtools

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// Linux deployments cancel the complete check process group, including tests.
func configureStaticProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
