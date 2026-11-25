//go:build unix

package hooks

import (
	"os/exec"
	"syscall"
)

// useProcessGroup starts the command as leader of a new process group and
// makes context cancellation kill the whole group, not just the leader.
func useProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killProcessGroup(cmd) }
}

// killProcessGroup kills every process left in the command's group. A group
// with no remaining members is not an error.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		return err
	}
	return nil
}
