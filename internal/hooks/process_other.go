//go:build !unix

package hooks

import "os/exec"

// Process groups are Unix-only. Elsewhere cancellation kills only the direct
// child (the exec default) and WaitDelay still bounds Wait, but descendants
// of a hook command may outlive it.
func useProcessGroup(*exec.Cmd) {}

func killProcessGroup(*exec.Cmd) error { return nil }
