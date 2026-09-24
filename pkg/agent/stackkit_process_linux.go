package agent

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func configureStackKitProcess(cmd *exec.Cmd) {
	// The CLI and its ordinary children share a private process group. Killing
	// only the CLI can leave a child mutating and keeping stdout/stderr open,
	// preventing the agent from returning its bounded failure result.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	// Bound inherited-pipe completion inside the existing result grace even
	// when a descendant has deliberately left the process group.
	cmd.WaitDelay = time.Second
}
