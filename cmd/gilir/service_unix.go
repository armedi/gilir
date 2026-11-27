//go:build unix

package main

import (
	"os/exec"
	"syscall"
)

// configureDetached puts the worker in its own session so it survives the
// launcher exiting and has no controlling terminal to receive signals from.
func configureDetached(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
