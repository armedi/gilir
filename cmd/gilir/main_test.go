package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/armedi/gilir/internal/opencode"
	"github.com/armedi/gilir/internal/store"
)

func TestListenAddressAcceptsOnlyLiteralLoopback(t *testing.T) {
	for _, value := range []string{"127.0.0.1:4718", "[::1]:4718", "127.0.0.1:0"} {
		t.Setenv("GILIR_LISTEN", value)
		if _, err := listenAddress(""); err != nil {
			t.Errorf("%q rejected: %v", value, err)
		}
	}
	for _, value := range []string{"0.0.0.0:4718", "localhost:4718", "192.168.1.2:4718", "127.0.0.2:4718",
		"127.0.0.1", "127.0.0.1:+80", "127.0.0.1:080", "127.0.0.1:65536", "127.0.0.1:http"} {
		t.Setenv("GILIR_LISTEN", value)
		if _, err := listenAddress(""); err == nil {
			t.Errorf("%q accepted", value)
		}
	}
}

func TestListenAddressDefaultsTo44547(t *testing.T) {
	t.Setenv("GILIR_LISTEN", "")
	got, err := listenAddress("")
	if err != nil {
		t.Fatal(err)
	}
	if got != "127.0.0.1:44547" {
		t.Fatalf("listen address = %q, want 127.0.0.1:44547", got)
	}
}

func TestListenAddressPortOverridesEnvPort(t *testing.T) {
	t.Setenv("GILIR_LISTEN", "127.0.0.1:44547")
	got, err := listenAddress("9000")
	if err != nil {
		t.Fatal(err)
	}
	if got != "127.0.0.1:9000" {
		t.Fatalf("listen address = %q, want 127.0.0.1:9000", got)
	}

	t.Setenv("GILIR_LISTEN", "[::1]:44547")
	got, err = listenAddress("9000")
	if err != nil {
		t.Fatal(err)
	}
	if got != "[::1]:9000" {
		t.Fatalf("listen address = %q, want [::1]:9000", got)
	}
}

func TestPrepareDatabaseCreatesPrivateFilesAndKeepsExistingMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "data", "gilir.sqlite")
	t.Setenv("GILIR_DB", path)
	got, err := prepareDatabase()
	if err != nil {
		t.Fatal(err)
	}
	assertMode(t, filepath.Dir(path), 0o700)
	assertMode(t, path, 0o600)

	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.sqlite")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GILIR_DB", link)
	viaLink, err := prepareDatabase()
	if err != nil {
		t.Fatal(err)
	}
	if viaLink != got {
		t.Fatalf("symlinked path resolved to %q, want %q", viaLink, got)
	}
	assertMode(t, path, 0o640)
}

func TestLockFileIsExclusiveAndReleasedOnClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gilir.sqlite.lock")
	first, err := lockFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockFile(path); !errors.Is(err, errLocked) {
		t.Fatalf("second lock: %v, want errLocked", err)
	}
	first.Close()
	again, err := lockFile(path)
	if err != nil {
		t.Fatalf("relock after close: %v", err)
	}
	again.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("lock file removed: %v", err)
	}
}

func TestDaemonServesStatusAndRejectsSecondInstance(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "gilir.sqlite")
	hooksPath := filepath.Join(dir, "hooks.json")
	// The hook is never delivered: no activity is written after startup.
	if err := os.WriteFile(hooksPath, []byte(`[{"name":"audit","command":["false"]}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GILIR_DB", dbPath)
	t.Setenv("GILIR_LISTEN", "127.0.0.1:"+freePort(t))
	t.Setenv("GILIR_OPENCODE_URL", "http://127.0.0.1:"+freePort(t)) // nothing listens
	t.Setenv("GILIR_WEB_DIR", filepath.Join(dir, "missing"))
	t.Setenv("GILIR_HOOKS", hooksPath)
	t.Setenv("OPENCODE_PASSWORD", "")

	if _, err := execute(context.Background(), "project.create", "user", map[string]any{"name": "Before"}); err != nil {
		t.Fatal(err)
	}
	if _, err := daemonStatus(context.Background()); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("offline status error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := runDaemon(ctx, "")
		done <- err
	}()

	var status map[string]any
	deadline := time.Now().Add(10 * time.Second)
	for {
		result, err := daemonStatus(context.Background())
		if err == nil {
			if err := json.Unmarshal(result.(json.RawMessage), &status); err != nil {
				t.Fatal(err)
			}
			if status["watcher_error"] != "not yet polled" {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon status unavailable: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if status["running"] != true || status["active_parents"] != nil || status["watcher_error"] == "" {
		t.Fatalf("status with unreachable OpenCode = %v", status)
	}

	if _, err := runDaemon(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second daemon error = %v", err)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("daemon did not stop")
	}

	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	latest, err := st.LatestActivityID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cursor, initialized, err := st.HookCursor(context.Background(), "audit")
	if err != nil || !initialized || cursor != latest || latest == 0 {
		t.Fatalf("hook cursor = %d %v %v, want initialized at %d", cursor, initialized, err, latest)
	}
}

func TestOpencodeOverridesDistinguishesUnsetFromEmpty(t *testing.T) {
	t.Setenv("GILIR_OPENCODE_URL", "")
	t.Setenv("OPENCODE_PASSWORD", "")
	overrides := opencodeOverrides()
	if overrides.URL == nil || *overrides.URL != "" {
		t.Fatalf("URL override = %v, want an explicit empty value", overrides.URL)
	}
	if overrides.Password == nil || *overrides.Password != "" {
		t.Fatalf("password override = %v, want an explicit empty value", overrides.Password)
	}
}

func TestOpencodeOverridesLeavesUnsetFieldsForDiscovery(t *testing.T) {
	t.Setenv("GILIR_OPENCODE_URL", "http://127.0.0.1:4096")
	os.Unsetenv("OPENCODE_PASSWORD")
	overrides := opencodeOverrides()
	if overrides.URL == nil || *overrides.URL != "http://127.0.0.1:4096" {
		t.Fatalf("URL override = %v", overrides.URL)
	}
	if overrides.Password != nil {
		t.Fatalf("password override = %q, want nil so it is discovered", *overrides.Password)
	}
}

func TestOpencodeEndpointNeverPrintsASecret(t *testing.T) {
	url := "http://127.0.0.1:4096"
	if got := opencodeEndpoint(opencode.Overrides{URL: &url}); got != url {
		t.Fatalf("endpoint = %q, want %q", got, url)
	}
	if got := opencodeEndpoint(opencode.Overrides{}); !strings.Contains(got, "auto-discovered") {
		t.Fatalf("endpoint without a URL = %q", got)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s mode = %o, want %o", path, got, want)
	}
}

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	return port
}
