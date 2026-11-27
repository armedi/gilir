package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// TestMain doubles as the detached worker entry point. service.start re-execs
// this test binary with the worker marker set and no arguments, which lands
// here before the test framework starts.
func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == serviceWorkerCommand {
		if _, err := runDaemon(context.Background(), "", ""); err != nil {
			fmt.Fprintf(os.Stderr, "gilir worker: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestResolveListenAcceptsAddresses(t *testing.T) {
	tests := []struct {
		env  string
		host string
		port string
		want string
	}{
		{env: "127.0.0.1:4718", want: "127.0.0.1:4718"},
		{env: "[::1]:4718", want: "[::1]:4718"},
		{env: "0.0.0.0:4718", want: "0.0.0.0:4718"},
		{env: "[::]:4718", want: "[::]:4718"},
		{env: "192.168.1.2:4718", want: "192.168.1.2:4718"},
		{env: "localhost:4718", want: "localhost:4718"},
		{env: "127.0.0.1:0", want: "127.0.0.1:0"},
		{env: "127.0.0.1:44547", host: "0.0.0.0", want: "0.0.0.0:44547"},
		{env: "127.0.0.1:44547", port: "9000", want: "127.0.0.1:9000"},
		{env: "127.0.0.1:44547", host: "[::1]", port: "9000", want: "[::1]:9000"},
	}
	for _, test := range tests {
		t.Setenv("GILIR_LISTEN", test.env)
		got, err := resolveListen(test.host, test.port)
		if err != nil {
			t.Errorf("%q/%q/%q rejected: %v", test.env, test.host, test.port, err)
			continue
		}
		if got.address() != test.want {
			t.Errorf("listen address = %q, want %q", got.address(), test.want)
		}
	}
}

func TestResolveListenRejectsBadAddresses(t *testing.T) {
	for _, test := range []struct {
		env  string
		host string
	}{
		{env: "example.com:4718"},
		{env: ":4718"},
		{env: "127.0.0.1"},
		{env: "127.0.0.1:+80"},
		{env: "127.0.0.1:080"},
		{env: "127.0.0.1:65536"},
		{env: "127.0.0.1:http"},
		{env: "127.0.0.1:4718", host: "example.com"},
	} {
		t.Setenv("GILIR_LISTEN", test.env)
		if _, err := resolveListen(test.host, ""); err == nil {
			t.Errorf("%q/%q accepted", test.env, test.host)
		}
	}
}

func TestResolveListenDefaultsTo44547(t *testing.T) {
	t.Setenv("GILIR_LISTEN", "")
	got, err := resolveListen("", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.address() != "127.0.0.1:44547" {
		t.Fatalf("listen address = %q, want 127.0.0.1:44547", got.address())
	}
}

func TestServiceURLMapsWildcardToLoopback(t *testing.T) {
	tests := []struct {
		host string
		want string
	}{
		{host: "0.0.0.0", want: "http://127.0.0.1:9000"},
		{host: "::", want: "http://[::1]:9000"},
		{host: "192.168.1.5", want: "http://192.168.1.5:9000"},
	}
	for _, test := range tests {
		got := listenConfig{host: test.host, port: "9000"}.urlFor("9000")
		if got != test.want {
			t.Errorf("%q url = %q, want %q", test.host, got, test.want)
		}
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
	t.Setenv("GILIR_LISTEN", "127.0.0.1:0")
	t.Setenv("GILIR_OPENCODE_URL", "http://127.0.0.1:"+freePort(t)) // nothing listens
	t.Setenv("GILIR_WEB_DIR", filepath.Join(dir, "missing"))
	t.Setenv("GILIR_HOOKS", hooksPath)
	t.Setenv("OPENCODE_PASSWORD", "")

	if _, err := execute(context.Background(), "project.create", "user", map[string]any{"name": "Before"}); err != nil {
		t.Fatal(err)
	}
	offline, err := queryServiceStatus(context.Background())
	if err != nil {
		t.Fatalf("offline status: %v", err)
	}
	if offline.(serviceStatus).Running {
		t.Fatal("offline status reported a running service")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := runDaemon(ctx, "", "")
		done <- err
	}()

	var status serviceStatus
	deadline := time.Now().Add(10 * time.Second)
	for {
		result, err := queryServiceStatus(context.Background())
		if err == nil {
			status = result.(serviceStatus)
			if status.Running && status.WatcherError != "not yet polled" {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("service status unavailable: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !status.Running || status.ActiveParents != nil || status.WatcherError == "" || status.Port == 0 {
		t.Fatalf("status with unreachable OpenCode = %+v", status)
	}

	// The public API must not expose process shutdown, even though it can
	// change queue state.
	if _, err := callDaemon(context.Background(), status.URL, "daemon.stop", json.RawMessage(`{}`), time.Second); err == nil {
		t.Fatal("daemon.stop was accepted on the public API")
	}
	if result, err := queryServiceStatus(context.Background()); err != nil || !result.(serviceStatus).Running {
		t.Fatalf("service stopped after a rejected daemon.stop: %+v %v", result, err)
	}

	if _, err := runDaemon(context.Background(), "", ""); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second daemon error = %v", err)
	}

	stopped, err := stopService(context.Background())
	if err != nil {
		t.Fatalf("stop service: %v", err)
	}
	if !stopped.(serviceStopped).Stopped {
		t.Fatal("stop reported that the service was not stopped")
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
	if _, err := os.Stat(serviceStatePath(dbPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("service state after stop: %v", err)
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

func TestServiceStartsDetachedAndStatusUsesState(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "gilir.sqlite")
	t.Setenv("GILIR_DB", dbPath)
	t.Setenv("GILIR_LISTEN", "127.0.0.1:0")
	t.Setenv("GILIR_OPENCODE_URL", "http://127.0.0.1:"+freePort(t))
	t.Setenv("GILIR_WEB_DIR", filepath.Join(dir, "missing"))
	t.Setenv("OPENCODE_PASSWORD", "")
	t.Cleanup(func() { _, _ = stopService(context.Background()) })

	started, err := startService(context.Background(), "", "")
	if err != nil {
		t.Fatalf("start service: %v", err)
	}
	status := started.(serviceStatus)
	if !status.Running || status.PID == 0 || status.Port == 0 || status.URL == "" {
		t.Fatalf("started status = %+v", status)
	}
	if status.PID == os.Getpid() {
		t.Fatal("service runs inside the launcher instead of a detached worker")
	}
	if _, err := os.Stat(serviceStatePath(dbPath)); err != nil {
		t.Fatalf("service state: %v", err)
	}

	// A later invocation must find the service through the state file even
	// when GILIR_LISTEN points somewhere else.
	t.Setenv("GILIR_LISTEN", "127.0.0.1:1")
	result, err := queryServiceStatus(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	queried := result.(serviceStatus)
	if !queried.Running || queried.URL != status.URL || queried.Port != status.Port {
		t.Fatalf("status from state = %+v, want %+v", queried, status)
	}

	// Starting again with the same requested address is idempotent.
	again, err := startService(context.Background(), "127.0.0.1", "0")
	if err != nil {
		t.Fatalf("idempotent start: %v", err)
	}
	if got := again.(serviceStatus); got.PID != status.PID {
		t.Fatalf("idempotent start pid = %d, want %d", got.PID, status.PID)
	}

	stopped, err := stopService(context.Background())
	if err != nil {
		t.Fatalf("stop service: %v", err)
	}
	if !stopped.(serviceStopped).Stopped {
		t.Fatal("stop reported that the service was not stopped")
	}
	if _, err := queryServiceStatus(context.Background()); err != nil {
		t.Fatalf("status after stop: %v", err)
	}
	if _, err := os.Stat(serviceStatePath(dbPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("service state after stop: %v", err)
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
