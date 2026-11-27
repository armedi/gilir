//go:build !unix && !windows

package main

import "os/exec"

// configureDetached is a no-op on platforms without process-detachment
// primitives; the worker still runs, but it may keep the launcher's session.
func configureDetached(*exec.Cmd) {}
