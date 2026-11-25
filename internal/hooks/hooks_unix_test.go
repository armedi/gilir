//go:build unix

package hooks

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// processGone reports whether pid has exited. A zombie (exited but not yet
// reaped by a non-reaping init) counts as gone.
func processGone(pid int) bool {
	if syscall.Kill(pid, 0) == syscall.ESRCH {
		return true
	}
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	return err == nil && bytes.Contains(stat, []byte(") Z "))
}

func TestCancelledCommandKillsDescendants(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	// The backgrounded sleep inherits the output pipe and would outlive a
	// kill of the shell alone.
	script := `sleep 60 & echo $! > "$1"; wait`
	source := newMemorySource(event(1, "task.created", "user"))
	source.cursors["local"] = 0
	var log errorLog
	stop := start(t, []Config{{Name: "local", Command: []string{"/bin/sh", "-c", script, "sh", pidFile}}}, source, &log)

	var pid int
	eventually(t, "descendant to start", func() bool {
		data, err := os.ReadFile(pidFile)
		pid, _ = strconv.Atoi(string(bytes.TrimSpace(data)))
		return err == nil && pid > 0
	})
	began := time.Now()
	stop()
	if elapsed := time.Since(began); elapsed >= waitDelay {
		t.Fatalf("Run took %s to stop; descendants held it until WaitDelay", elapsed)
	}
	eventually(t, "descendant to exit", func() bool { return processGone(pid) })
	if source.cursor("local") != 0 {
		t.Fatalf("cursor advanced for an interrupted delivery")
	}
}
