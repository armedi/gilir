package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/armedi/gilir/internal/cli"
	"github.com/armedi/gilir/internal/opencode"
	"github.com/armedi/gilir/internal/store"
)

// TestMain doubles as the detached worker entry point. service.start re-execs
// this test binary with the worker marker set and no arguments, which lands
// here before the test framework starts.
func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == serviceWorkerCommand {
		if err := runServiceWorker(context.Background()); err != nil {
			fmt.Fprintf(os.Stderr, "gilir worker: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	// subprocessServeEnv makes this test binary a real `gilir` entry point so
	// a test can spawn it, signal it, and inspect its exact stdout and exit
	// code. The arguments travel in one JSON array.
	if raw := os.Getenv(subprocessServeEnvName); raw != "" {
		var argv []string
		if err := json.Unmarshal([]byte(raw), &argv); err != nil {
			fmt.Fprintf(os.Stderr, "gilir test entry: %v\n", err)
			os.Exit(1)
		}
		os.Exit(cli.Run(context.Background(), argv, os.Stdout, os.Stderr, "", execute))
	}
	os.Exit(m.Run())
}

const subprocessServeEnvName = "GILIR_TEST_SUBPROCESS_ARGS"

// isolateDataDir points dataDir() at a per-test directory so managed-service
// state, log, and lock files never leak between tests.
func isolateDataDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	t.Setenv("XDG_DATA_HOME", dir)
	t.Setenv("LOCALAPPDATA", dir)
	return dir
}

// dataServicePaths resolves the singleton service files inside the isolated
// data directory. Tests use it instead of resolving the current environment.
func dataServicePaths(t *testing.T) servicePaths {
	t.Helper()
	paths, err := resolveServicePaths()
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

func TestResolveListenAcceptsAddresses(t *testing.T) {
	tests := []struct {
		host string
		port string
		want string
	}{
		{host: "127.0.0.1", port: "4718", want: "127.0.0.1:4718"},
		{host: "[::1]", port: "4718", want: "[::1]:4718"},
		{host: "0.0.0.0", port: "4718", want: "0.0.0.0:4718"},
		{host: "[::]", port: "4718", want: "[::]:4718"},
		{host: "[::0]", port: "4718", want: "[::]:4718"},
		{host: "[0:0:0:0:0:0:0:0]", port: "4718", want: "[::]:4718"},
		{host: "[::ffff:0.0.0.0]", port: "4718", want: "0.0.0.0:4718"},
		{host: "192.168.1.2", port: "4718", want: "192.168.1.2:4718"},
		{host: "localhost", port: "4718", want: "localhost:4718"},
		{host: "127.0.0.1", port: "0", want: "127.0.0.1:0"},
		{host: "0.0.0.0", port: "44547", want: "0.0.0.0:44547"},
		{host: "127.0.0.1", port: "9000", want: "127.0.0.1:9000"},
		{host: "[::1]", port: "9000", want: "[::1]:9000"},
	}
	for _, test := range tests {
		got, err := resolveListen(test.host, test.port)
		if err != nil {
			t.Errorf("%q/%q rejected: %v", test.host, test.port, err)
			continue
		}
		if got.address() != test.want {
			t.Errorf("listen address = %q, want %q", got.address(), test.want)
		}
	}
}

func TestResolveListenRejectsBadAddresses(t *testing.T) {
	for _, test := range []struct {
		host string
		port string
	}{
		{host: "example.com", port: "4718"},
		{host: "not-an-ip", port: "4718"},
		{host: "127.0.0.1:4718"},
		{host: "127.0.0.1", port: "+80"},
		{host: "127.0.0.1", port: "080"},
		{host: "127.0.0.1", port: "65536"},
		{host: "127.0.0.1", port: "http"},
		{host: "", port: "65536"},
	} {
		if _, err := resolveListen(test.host, test.port); err == nil {
			t.Errorf("%q/%q accepted", test.host, test.port)
		}
	}
}

func TestResolveListenDefaultsTo44547(t *testing.T) {
	got, err := resolveListen("", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.address() != "127.0.0.1:44547" {
		t.Fatalf("listen address = %q, want 127.0.0.1:44547", got.address())
	}
}

func TestPrepareDatabaseCreatesPrivateFilesAndKeepsExistingMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "data", "gilir.sqlite")
	t.Setenv("GILIR_DB", path)
	got, err := prepareDatabase("")
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
	viaLink, err := prepareDatabase("")
	if err != nil {
		t.Fatal(err)
	}
	if viaLink != got {
		t.Fatalf("symlinked path resolved to %q, want %q", viaLink, got)
	}
	assertMode(t, path, 0o640)
}

func TestPrepareDatabaseFlagWinsOverEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GILIR_DB", filepath.Join(dir, "env.sqlite"))
	flag := filepath.Join(dir, "flag.sqlite")
	got, err := prepareDatabase(flag)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(flag)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("database path = %q, want the --db override %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "env.sqlite")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("--db did not win over GILIR_DB: %v", err)
	}
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

// serveTestEnv prepares an isolated foreground-service environment: a private
// database, an unreachable OpenCode, and no web bundle.
func serveTestEnv(t *testing.T) (dir, dbPath string) {
	dir = t.TempDir()
	isolateDataDir(t)
	dbPath = filepath.Join(dir, "gilir.sqlite")
	t.Setenv("GILIR_DB", dbPath)
	t.Setenv("GILIR_URL", "http://127.0.0.1:1") // reserved; nothing listens
	t.Setenv("GILIR_OPENCODE_URL", "http://127.0.0.1:1")
	t.Setenv("GILIR_WEB_DIR", filepath.Join(dir, "missing"))
	t.Setenv("OPENCODE_PASSWORD", "")
	canonical, err := prepareDatabase("")
	if err != nil {
		t.Fatal(err)
	}
	return dir, canonical
}

func TestServeServesStatusAndRejectsSecondInstance(t *testing.T) {
	dir, dbPath := serveTestEnv(t)
	hooksPath := filepath.Join(dir, "hooks.json")
	// The audit hook uses the default event set, which does not include the
	// project.created activity this test writes, so its command never runs.
	if err := os.WriteFile(hooksPath, []byte(`[{"name":"audit","command":["false"]}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GILIR_HOOKS", hooksPath)

	// Seed one activity as a fixture so startup cursor initialization has a
	// real latest ID to skip.
	seed, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.CreateProject(context.Background(), "fixture", "Before"); err != nil {
		seed.Close()
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	port := freePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, listenFor("127.0.0.1", port), dbPath, false)
	}()
	waitForServe(t, "127.0.0.1", port)
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Fatal("foreground service did not stop")
		}
	}()

	// A foreground serve publishes no managed state; it must not create
	// service.json, service.log, or service.lock.
	paths := dataServicePaths(t)
	for _, path := range []string{paths.State, paths.Log, paths.OpLock} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("foreground serve touched %s: %v", path, err)
		}
	}

	// Startup skips history: the cursor begins at the seeded event, before
	// any later activity exists for the hook to consume.
	if cursor := hookCursor(t, dbPath, "audit"); cursor != 1 {
		t.Fatalf("startup hook cursor = %d, want 1", cursor)
	}

	// Another serve on the same database is refused by the database lock.
	if err := serve(context.Background(), listenFor("127.0.0.1", "0"), dbPath, false); err == nil ||
		!strings.Contains(err.Error(), "already running") {
		t.Fatalf("second serve error = %v, want a lock conflict", err)
	}

	// The public API must not expose process shutdown even though it can
	// change queue state.
	if _, err := callService(context.Background(), "http://127.0.0.1:1", "service.stop", "", json.RawMessage(`{}`), time.Second); err == nil {
		t.Fatal("service.stop was accepted on the public API")
	}
}

func TestServeReleasesDatabaseLockOnExit(t *testing.T) {
	_, dbPath := serveTestEnv(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, listenFor("127.0.0.1", "0"), dbPath, false) }()

	deadline := time.Now().Add(10 * time.Second)
	for !serviceLockHeld(dbPath) {
		if time.Now().After(deadline) {
			t.Fatal("serve never acquired the database lock")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("serve exited with %v", err)
	}
	if serviceLockHeld(dbPath) {
		t.Fatal("serve left the database lock held on exit")
	}
}

func TestServeRejectsConcurrentUseOfTheManagedDatabase(t *testing.T) {
	_, dbPath := serveTestEnv(t)
	lock, err := lockFile(dbPath + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()

	// A `gilir serve` on a locked database fails fast instead of opening it.
	if err := serve(context.Background(), listenFor("127.0.0.1", "0"), dbPath, false); err == nil ||
		!strings.Contains(err.Error(), "already running") {
		t.Fatalf("serve error = %v, want a lock conflict", err)
	}
}

func hookCursor(t *testing.T, dbPath, name string) int64 {
	t.Helper()
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cursor, initialized, err := st.HookCursor(context.Background(), name)
	if err != nil || !initialized {
		t.Fatalf("hook cursor = %d %v %v, want an initialized cursor", cursor, initialized, err)
	}
	return cursor
}

func listenFor(host, port string) listenConfig {
	listen, err := resolveListen(host, port)
	if err != nil {
		panic(err)
	}
	return listen
}

func TestCustomPortServeHooksReceiveItsOwnURL(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /bin/sh for the hook")
	}
	for _, host := range []string{"127.0.0.1", "localhost", "0.0.0.0", "::1", "::0"} {
		t.Run(host, func(t *testing.T) {
			dir, dbPath := serveTestEnv(t)
			hooksPath := filepath.Join(dir, "hooks.json")
			capturePath := filepath.Join(dir, "hook-url")
			configs, err := json.Marshal([]map[string]any{{
				"name":   "capture",
				"events": []string{"project.created"},
				"command": []string{
					"/bin/sh", "-c", `printf '%s' "$GILIR_URL" > "$1"`, "sh", capturePath,
				},
			}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(hooksPath, configs, 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("GILIR_HOOKS", hooksPath)
			port := freePort(t)
			t.Setenv("GILIR_OPENCODE_URL", "http://127.0.0.1:"+freePort(t))
			// Service startup must ignore GILIR_URL; its hooks must still get
			// the URL it actually bound, not the inherited value.
			t.Setenv("GILIR_URL", "http://127.0.0.1:1")

			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- serve(ctx, listenFor(host, port), dbPath, false) }()
			t.Cleanup(func() {
				cancel()
				select {
				case <-done:
				case <-time.After(15 * time.Second):
				}
			})

			// A foreground serve publishes no state, so discover its dialable
			// loopback URL from the candidates the bind implies.
			target := waitForServe(t, host, port)
			endpoint, err := commandEndpoint(target)
			if err != nil {
				t.Fatalf("advertised URL %q: %v", target, err)
			}
			if host == "::0" && endpoint.Hostname() != "::1" {
				t.Fatalf("IPv6 wildcard advertised %s, want [::1]", target)
			}
			if _, err := sendCommand(ctx, endpoint, "project.create", "user", map[string]any{"name": "Hooked"}); err != nil {
				t.Fatal(err)
			}

			deadline := time.Now().Add(10 * time.Second)
			for {
				data, err := os.ReadFile(capturePath)
				if err == nil && string(data) == target {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("hook saw GILIR_URL %q, want %q (read error %v)", data, target, err)
				}
				time.Sleep(50 * time.Millisecond)
			}

			// The hook env must let gilir reach this service.
			t.Setenv("GILIR_URL", target)
			if _, err := execute(ctx, "project.create", "hook:capture", map[string]any{"name": "Routed"}); err != nil {
				t.Fatalf("ordinary command via hook URL: %v", err)
			}
		})
	}
}

// waitForServe returns the first loopback URL implied by a bind that answers
// service.status, which is the URL the service advertises to local clients.
func waitForServe(t *testing.T, host, port string) string {
	t.Helper()
	candidates := map[string][]string{
		"127.0.0.1": {"127.0.0.1"},
		"localhost": {"127.0.0.1", "::1"},
		"0.0.0.0":   {"127.0.0.1"},
		"::0":       {"::1"},
		"::1":       {"::1"},
	}[host]
	if len(candidates) == 0 {
		t.Fatalf("no dialable candidate for host %q", host)
	}
	deadline := time.Now().Add(10 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		for _, candidate := range candidates {
			target := "http://" + net.JoinHostPort(candidate, port)
			if _, err := serviceStatusAt(context.Background(), target); err == nil {
				return target
			} else {
				lastErr = err
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("service never answered on port %s: %v", port, lastErr)
	return ""
}

func TestServiceStartsDetachedAndStatusUsesState(t *testing.T) {
	dir, dbPath := serveTestEnv(t)
	t.Setenv("GILIR_OPENCODE_URL", "http://127.0.0.1:"+freePort(t))
	t.Setenv("GILIR_WEB_DIR", filepath.Join(dir, "missing"))
	t.Cleanup(func() { _, _ = stopService(context.Background()) })
	paths := dataServicePaths(t)

	started, err := startService(context.Background(), "127.0.0.1", "0", "")
	if err != nil {
		t.Fatalf("start service: %v", err)
	}
	status := started.(serviceStatus)
	if !status.Running || status.PID == 0 || status.Port == 0 || status.URL == "" {
		t.Fatalf("started status = %+v", status)
	}
	if status.DBPath != dbPath {
		t.Fatalf("status db_path = %q, want %q", status.DBPath, dbPath)
	}
	if status.URL == "http://127.0.0.1:1" {
		t.Fatalf("service bind address came from GILIR_URL: %+v", status)
	}
	if status.PID == os.Getpid() {
		t.Fatal("service runs inside the launcher instead of a detached worker")
	}
	if _, err := os.Stat(paths.State); err != nil {
		t.Fatalf("service state: %v", err)
	}
	if _, err := os.Stat(paths.Log); err != nil {
		t.Fatalf("service log: %v", err)
	}
	if state, ok := readServiceState(paths); !ok || state.DBPath != dbPath {
		t.Fatalf("service state db_path = %+v, want %q", state, dbPath)
	}
	// The managed files live in the data directory, never beside the database.
	for _, path := range []string{dbPath + ".service.json", dbPath + ".service.log", dbPath + ".service.lock"} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("legacy next-to-database file exists: %s", path)
		}
	}

	// A later invocation must find the service through local state even when
	// GILIR_URL points somewhere else; only ordinary commands use that URL.
	t.Setenv("GILIR_URL", "http://127.0.0.1:1")
	result, err := queryServiceStatus(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	queried := result.(serviceStatus)
	if !queried.Running || queried.URL != status.URL || queried.Port != status.Port ||
		queried.DBPath != dbPath {
		t.Fatalf("status from state = %+v, want %+v", queried, status)
	}

	// Starting again with the same requested address and database is
	// idempotent.
	again, err := startService(context.Background(), "127.0.0.1", "0", "")
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
	if _, err := os.Stat(paths.State); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("service state after stop: %v", err)
	}
}

func TestServiceStartRejectsMismatchedHostPortAndDatabase(t *testing.T) {
	dir, dbPath := serveTestEnv(t)
	t.Setenv("GILIR_OPENCODE_URL", "http://127.0.0.1:"+freePort(t))
	t.Setenv("GILIR_WEB_DIR", filepath.Join(dir, "missing"))
	t.Cleanup(func() { _, _ = stopService(context.Background()) })

	if _, err := startService(context.Background(), "127.0.0.1", "0", ""); err != nil {
		t.Fatalf("start service: %v", err)
	}
	for _, test := range []struct {
		name string
		host string
		port string
		db   string
	}{
		{name: "different port", host: "127.0.0.1", port: "9000", db: ""},
		{name: "different host", host: "0.0.0.0", port: "0", db: ""},
		{name: "different database", host: "127.0.0.1", port: "0", db: filepath.Join(dir, "other.sqlite")},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := startService(context.Background(), test.host, test.port, test.db)
			if err == nil || !strings.Contains(err.Error(), "gilir service stop") {
				t.Fatalf("mismatched start error = %v, want a stop-first error", err)
			}
		})
	}
	_ = dbPath
}

func TestServiceRestartPreservesAndOverridesDatabase(t *testing.T) {
	dir, _ := serveTestEnv(t)
	t.Setenv("GILIR_OPENCODE_URL", "http://127.0.0.1:"+freePort(t))
	t.Setenv("GILIR_WEB_DIR", filepath.Join(dir, "missing"))
	t.Cleanup(func() { _, _ = stopService(context.Background()) })

	// Start on a database that GILIR_DB does not name, so a restart that
	// ignored the stored record would be visible in db_path.
	firstDB, err := prepareDatabase(filepath.Join(dir, "first.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	started, err := startService(context.Background(), "127.0.0.1", "0", firstDB)
	if err != nil {
		t.Fatalf("start service: %v", err)
	}
	first := started.(serviceStatus)
	if first.DBPath != firstDB {
		t.Fatalf("start db_path = %q, want %q", first.DBPath, firstDB)
	}

	// Point GILIR_DB somewhere else, then restart without flags: it must keep
	// the running database and host, not fall back to the environment.
	t.Setenv("GILIR_DB", filepath.Join(dir, "env.sqlite"))
	restarted, err := restartService(context.Background(), "", "", "")
	if err != nil {
		t.Fatalf("restart service: %v", err)
	}
	second := restarted.(serviceStatus)
	if second.DBPath != firstDB || second.Host != first.Host || second.Port == first.Port {
		t.Fatalf("restart = %+v, want the same db_path and host on a new port", second)
	}

	// An explicit --db overrides the stored database.
	other, err := prepareDatabase(filepath.Join(dir, "other.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	restarted, err = restartService(context.Background(), "", "", other)
	if err != nil {
		t.Fatalf("restart with --db: %v", err)
	}
	if got := restarted.(serviceStatus).DBPath; got != other {
		t.Fatalf("restart --db path = %q, want %q", got, other)
	}

	// Once stopped, a restart with no flags uses GILIR_DB again.
	if stopped, err := stopService(context.Background()); err != nil || !stopped.(serviceStopped).Stopped {
		t.Fatalf("stop service = %+v, %v", stopped, err)
	}
	envDB, err := prepareDatabase("")
	if err != nil {
		t.Fatal(err)
	}
	restarted, err = restartService(context.Background(), "", "", "")
	if err != nil {
		t.Fatalf("restart while stopped: %v", err)
	}
	if got := restarted.(serviceStatus).DBPath; got != envDB {
		t.Fatalf("stopped restart db_path = %q, want GILIR_DB %q", got, envDB)
	}
}

func TestServiceStartWildcardRoutesCommandsViaGILIRURL(t *testing.T) {
	dir, _ := serveTestEnv(t)
	t.Setenv("GILIR_OPENCODE_URL", "http://127.0.0.1:"+freePort(t))
	t.Setenv("GILIR_WEB_DIR", filepath.Join(dir, "missing"))
	t.Cleanup(func() { _, _ = stopService(context.Background()) })

	started, err := startService(context.Background(), "0.0.0.0", "0", "")
	if err != nil {
		t.Fatalf("start service: %v", err)
	}
	status := started.(serviceStatus)
	if !status.Running || status.Host != "0.0.0.0" || status.URL == "http://127.0.0.1:1" {
		t.Fatalf("started status = %+v", status)
	}
	if !strings.HasPrefix(status.URL, "http://127.0.0.1:") {
		t.Fatalf("wildcard service local URL = %q, want loopback", status.URL)
	}

	// A wildcard bind answers through loopback, a supported GILIR_URL address.
	t.Setenv("GILIR_URL", status.URL)
	created, err := execute(context.Background(), "project.create", "user", map[string]any{"name": "Wildcard"})
	if err != nil {
		t.Fatalf("command via GILIR_URL: %v", err)
	}
	var project struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(created.(json.RawMessage), &project); err != nil {
		t.Fatal(err)
	}
	if project.ID != 1 {
		t.Fatalf("created project = %+v", project)
	}
	if _, err := execute(context.Background(), "service.update", "user", map[string]any{"paused": true}); err != nil {
		t.Fatalf("service.update wire command via GILIR_URL: %v", err)
	}
	statusResult, err := queryServiceStatus(context.Background())
	if err != nil {
		t.Fatalf("status after pause: %v", err)
	}
	if !statusResult.(serviceStatus).Paused {
		t.Fatal("paused queue control did not reach the service")
	}
	if _, err := stopService(context.Background()); err != nil {
		t.Fatalf("stop service: %v", err)
	}
}

func TestLoopbackServiceWorksBehindHTTPSProxy(t *testing.T) {
	dir := t.TempDir()
	isolateDataDir(t)
	t.Setenv("GILIR_DB", filepath.Join(dir, "gilir.sqlite"))
	t.Setenv("GILIR_OPENCODE_URL", "http://127.0.0.1:"+freePort(t))
	t.Setenv("GILIR_WEB_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("dashboard"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = stopService(context.Background()) })

	started, err := startService(context.Background(), "", "0", "")
	if err != nil {
		t.Fatalf("start service: %v", err)
	}
	status := started.(serviceStatus)
	const host = "gilir.example.ts.net:44547"
	client := &http.Client{Timeout: time.Second}
	for _, test := range []struct {
		name, method, path, origin, body string
	}{
		{name: "dashboard deep link", method: http.MethodGet, path: "/tasks/GILIR-42"},
		{name: "same-origin API", method: http.MethodPost, path: "/api/v1/command",
			origin: "https://" + host, body: `{"command":"service.status"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			req, err := http.NewRequest(test.method, status.URL+test.path, strings.NewReader(test.body))
			if err != nil {
				t.Fatal(err)
			}
			req.Host = host
			req.Header.Set("X-Forwarded-Proto", "https")
			if test.origin != "" {
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Origin", test.origin)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("HTTP %d, want %d", resp.StatusCode, http.StatusOK)
			}
			if test.method == http.MethodGet {
				body, err := io.ReadAll(resp.Body)
				if err != nil || string(body) != "dashboard" {
					t.Fatalf("dashboard response = %q, %v", body, err)
				}
			}
		})
	}
}

func TestServiceStatusRejectsForeignInstance(t *testing.T) {
	dir := t.TempDir()
	isolateDataDir(t)
	liveDB := filepath.Join(dir, "live.sqlite")
	t.Setenv("GILIR_DB", liveDB)
	t.Setenv("GILIR_OPENCODE_URL", "http://127.0.0.1:"+freePort(t))
	t.Setenv("GILIR_WEB_DIR", filepath.Join(dir, "missing"))
	t.Setenv("OPENCODE_PASSWORD", "")
	paths := dataServicePaths(t)

	if _, err := startService(context.Background(), "127.0.0.1", "0", ""); err != nil {
		t.Fatalf("start service: %v", err)
	}
	t.Cleanup(func() { _, _ = stopService(context.Background()) })

	// Simulate another service whose state file survived a crash and whose
	// port was then reused by the live service: the URL answers, but the
	// instance is not this service's.
	state, ok := readServiceState(paths)
	if !ok {
		t.Fatal("live service state missing")
	}
	state.Instance = "someone-else"
	if err := writeServiceState(paths, state); err != nil {
		t.Fatal(err)
	}
	result, err := queryServiceStatus(context.Background())
	if err != nil {
		t.Fatalf("foreign status: %v", err)
	}
	if result.(serviceStatus).Running {
		t.Fatalf("foreign state reported a running service: %+v", result)
	}
	if stopped, err := stopService(context.Background()); err != nil || !stopped.(serviceStopped).Stopped {
		t.Fatalf("stop live service = %+v, %v", stopped, err)
	}
}

func TestQueueControlsReadLocalStateAndIgnoreGILIRURL(t *testing.T) {
	dir, _ := serveTestEnv(t)
	t.Setenv("GILIR_OPENCODE_URL", "http://127.0.0.1:"+freePort(t))
	t.Setenv("GILIR_WEB_DIR", filepath.Join(dir, "missing"))
	t.Cleanup(func() { _, _ = stopService(context.Background()) })

	// A queue control before the service exists fails; it never falls back to
	// GILIR_URL.
	t.Setenv("GILIR_URL", "http://127.0.0.1:1")
	if _, err := execute(context.Background(), "service.pause", "agent:test", map[string]any{}); err == nil ||
		!strings.Contains(err.Error(), "not running") {
		t.Fatalf("pause while stopped = %v, want a not-running error", err)
	}

	started, err := startService(context.Background(), "127.0.0.1", "0", "")
	if err != nil {
		t.Fatalf("start service: %v", err)
	}
	status := started.(serviceStatus)

	// The host-local path must reach the running service even when GILIR_URL
	// points elsewhere.
	t.Setenv("GILIR_URL", "http://127.0.0.1:1")
	if _, err := execute(context.Background(), "service.pause", "agent:test", map[string]any{}); err != nil {
		t.Fatalf("pause: %v", err)
	}
	result, err := queryServiceStatus(context.Background())
	if err != nil || !result.(serviceStatus).Paused {
		t.Fatalf("status after pause = %+v, %v", result, err)
	}
	if _, err := execute(context.Background(), "service.resume", "agent:test", map[string]any{}); err != nil {
		t.Fatalf("resume: %v", err)
	}
	result, err = queryServiceStatus(context.Background())
	if err != nil || result.(serviceStatus).Paused {
		t.Fatalf("status after resume = %+v, %v", result, err)
	}
	if _, err := execute(context.Background(), "service.concurrency", "agent:test", map[string]any{"max_concurrency": 3}); err != nil {
		t.Fatalf("concurrency: %v", err)
	}
	result, err = queryServiceStatus(context.Background())
	if err != nil || result.(serviceStatus).MaxConcurrency != 3 {
		t.Fatalf("status after concurrency = %+v, %v", result, err)
	}
	if status.URL == "" {
		t.Fatal("service never advertised its URL")
	}
}

func TestQueueControlsRejectStaleState(t *testing.T) {
	dir, _ := serveTestEnv(t)
	t.Setenv("GILIR_OPENCODE_URL", "http://127.0.0.1:"+freePort(t))
	t.Setenv("GILIR_WEB_DIR", filepath.Join(dir, "missing"))
	paths := dataServicePaths(t)

	if _, err := startService(context.Background(), "127.0.0.1", "0", ""); err != nil {
		t.Fatalf("start service: %v", err)
	}
	t.Cleanup(func() { _, _ = stopService(context.Background()) })

	// Corrupt the recorded instance so the live service answers with a
	// different ID and the host-local path refuses to mutate it.
	state, ok := readServiceState(paths)
	if !ok {
		t.Fatal("service state missing")
	}
	state.Instance = "someone-else"
	if err := writeServiceState(paths, state); err != nil {
		t.Fatal(err)
	}
	if _, err := execute(context.Background(), "service.pause", "agent:test", map[string]any{}); err == nil ||
		!strings.Contains(err.Error(), "not the one recorded") {
		t.Fatalf("pause with stale state = %v, want an instance mismatch", err)
	}
}

// startServeSubprocess runs this test binary as `gilir serve` with args and
// returns the process plus its buffered output.
type serveSubprocess struct {
	cmd    *exec.Cmd
	stdout *strings.Builder
	stderr *strings.Builder
	done   chan error
}

func startServeSubprocess(t *testing.T, env []string, args ...string) *serveSubprocess {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), env...)
	cmd.Env = append(cmd.Env, subprocessServeEnvName+"="+string(raw))
	proc := &serveSubprocess{
		cmd:    cmd,
		stdout: &strings.Builder{},
		stderr: &strings.Builder{},
		done:   make(chan error, 1),
	}
	cmd.Stdout = proc.stdout
	cmd.Stderr = proc.stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { proc.done <- cmd.Wait() }()
	t.Cleanup(func() {
		if proc.cmd.ProcessState == nil {
			_ = proc.cmd.Process.Kill()
			select {
			case <-proc.done:
			case <-time.After(5 * time.Second):
			}
		}
	})
	return proc
}

func (p *serveSubprocess) wait(t *testing.T, timeout time.Duration) error {
	t.Helper()
	select {
	case err := <-p.done:
		p.done <- err // keep it observable for the cleanup
		return err
	case <-time.After(timeout):
		t.Fatalf("subprocess did not exit within %s (stderr %q)", timeout, p.stderr.String())
		return nil
	}
}

func TestServeCommandExitsCleanlyOnSignalWithNoStdout(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	dbPath := filepath.Join(dir, "gilir.sqlite")
	port := freePort(t)

	proc := startServeSubprocess(t, []string{
		"XDG_DATA_HOME=" + dataDir,
		"LOCALAPPDATA=" + dataDir,
		"GILIR_OPENCODE_URL=http://127.0.0.1:1",
		"GILIR_WEB_DIR=" + filepath.Join(dir, "missing"),
		"OPENCODE_PASSWORD=",
	}, "serve", "--host", "127.0.0.1", "--port", port, "--db", dbPath)

	target := "http://127.0.0.1:" + port
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := serviceStatusAt(context.Background(), target); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("serve never answered at %s (stderr %q)", target, proc.stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}

	for _, name := range []string{"service.json", "service.log", "service.lock"} {
		if _, err := os.Stat(filepath.Join(dataDir, "gilir", name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("foreground serve wrote %s: %v", name, err)
		}
	}

	if err := proc.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := proc.wait(t, 15*time.Second); err != nil {
		t.Fatalf("serve exited with %v (stderr %q)", err, proc.stderr.String())
	}
	if got := proc.stdout.String(); got != "" {
		t.Fatalf("clean serve wrote to stdout: %q", got)
	}
	if !strings.Contains(proc.stderr.String(), "listening") {
		t.Fatalf("serve did not log startup to stderr: %q", proc.stderr.String())
	}
	canonical, err := filepath.EvalSymlinks(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if serviceLockHeld(canonical) {
		t.Fatal("serve left the database lock held")
	}
}

func TestServeStopsBeforeBindingOnACancelledContext(t *testing.T) {
	dir, dbPath := serveTestEnv(t)
	t.Setenv("GILIR_OPENCODE_URL", "http://127.0.0.1:"+freePort(t))
	t.Setenv("GILIR_WEB_DIR", filepath.Join(dir, "missing"))

	// A signal that arrives during startup must end the process cleanly, not
	// leave it bound or holding the database. A cancelled parent context is
	// the same condition without a racy subprocess signal.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := serve(ctx, listenFor("127.0.0.1", freePort(t)), dbPath, false); err != nil {
		t.Fatalf("serve on a cancelled context = %v, want a clean nil exit", err)
	}
	if serviceLockHeld(dbPath) {
		t.Fatal("serve left the database lock held after an early stop")
	}
}

func TestServeCommandRejectsJSON(t *testing.T) {
	dir := t.TempDir()
	proc := startServeSubprocess(t, []string{
		"XDG_DATA_HOME=" + filepath.Join(dir, "data"),
		"LOCALAPPDATA=" + filepath.Join(dir, "data"),
	}, "serve", "--json", "--db", filepath.Join(dir, "gilir.sqlite"), "--port", freePort(t))
	if err := proc.wait(t, 10*time.Second); err == nil {
		t.Fatal("serve --json exited 0")
	}
	var exit *exec.ExitError
	if !errors.As(proc.wait(t, time.Second), &exit) || exit.ExitCode() != 2 {
		t.Fatalf("serve --json exit = %v, want 2", exit)
	}
	if got := proc.stdout.String(); got != "" {
		t.Fatalf("serve --json wrote to stdout: %q", got)
	}
	if !strings.Contains(proc.stderr.String(), "flag --json is not valid") {
		t.Fatalf("serve --json stderr = %q", proc.stderr.String())
	}
}

func TestServeCommandRejectsDatabaseOwnedByManagedService(t *testing.T) {
	dir, dbPath := serveTestEnv(t)
	t.Setenv("GILIR_OPENCODE_URL", "http://127.0.0.1:"+freePort(t))
	t.Setenv("GILIR_WEB_DIR", filepath.Join(dir, "missing"))
	t.Cleanup(func() { _, _ = stopService(context.Background()) })
	if _, err := startService(context.Background(), "127.0.0.1", "0", ""); err != nil {
		t.Fatalf("start service: %v", err)
	}
	dataDir := os.Getenv("XDG_DATA_HOME")
	proc := startServeSubprocess(t, []string{
		"XDG_DATA_HOME=" + dataDir,
		"LOCALAPPDATA=" + dataDir,
		"GILIR_OPENCODE_URL=http://127.0.0.1:1",
		"GILIR_WEB_DIR=" + filepath.Join(dir, "missing"),
	}, "serve", "--host", "127.0.0.1", "--port", freePort(t), "--db", dbPath)
	err := proc.wait(t, 10*time.Second)
	if err == nil {
		t.Fatal("serve succeeded on a database owned by the managed service")
	}
	if !strings.Contains(proc.stderr.String(), "already running") {
		t.Fatalf("serve lock-conflict stderr = %q", proc.stderr.String())
	}
}

func TestQueueControlsForwardActor(t *testing.T) {
	dir := t.TempDir()
	isolateDataDir(t)
	paths := dataServicePaths(t)
	dbPath := filepath.Join(dir, "gilir.sqlite")

	type captured struct {
		Command string                     `json:"command"`
		Actor   string                     `json:"actor"`
		Args    map[string]json.RawMessage `json:"args"`
	}
	var mu sync.Mutex
	var requests []captured
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request captured
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		requests = append(requests, request)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if request.Command == "service.status" {
			io.WriteString(w, `{"data":{"running":true,"paused":false,"max_concurrency":1,"instance":"inst-1"}}`)
			return
		}
		io.WriteString(w, `{"data":{"paused":true,"max_concurrency":1}}`)
	}))
	defer server.Close()

	dbLock, err := lockFile(dbPath + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	defer dbLock.Close()
	if err := writeServiceState(paths, serviceState{
		Version:       serviceVersion,
		Instance:      "inst-1",
		PID:           os.Getpid(),
		RequestedHost: "127.0.0.1",
		RequestedPort: 44547,
		Host:          "127.0.0.1",
		Port:          44547,
		URL:           server.URL,
		ControlPort:   1,
		Token:         "token",
		DBPath:        dbPath,
		LogPath:       paths.Log,
		StartedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := execute(context.Background(), "service.pause", "agent:flag", map[string]any{}); err != nil {
		t.Fatalf("pause: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	update := requests[len(requests)-1]
	if update.Command != "service.update" {
		t.Fatalf("command = %q, want service.update", update.Command)
	}
	if update.Actor != "agent:flag" {
		t.Fatalf("actor = %q, want agent:flag", update.Actor)
	}
	if string(update.Args["paused"]) != "true" {
		t.Fatalf("paused arg = %s, want true", update.Args["paused"])
	}
}

func TestStopWaitsForDatabaseLockRelease(t *testing.T) {
	dir := t.TempDir()
	isolateDataDir(t)
	paths := dataServicePaths(t)
	dbPath := filepath.Join(dir, "gilir.sqlite")

	dbLock, err := lockFile(dbPath + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	defer dbLock.Close()
	// A dead control port makes requestStop fail. A live worker of ours still
	// answers service.status, so stop must wait for the database lock rather
	// than trust the failed control request.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":{"running":true,"paused":false,"max_concurrency":1,"instance":"inst-1","active_parents":0}}`)
	}))
	defer server.Close()
	if err := writeServiceState(paths, serviceState{
		Version:       serviceVersion,
		Instance:      "inst-1",
		PID:           os.Getpid(),
		RequestedHost: "127.0.0.1",
		RequestedPort: 44547,
		Host:          "127.0.0.1",
		Port:          44547,
		URL:           server.URL,
		ControlPort:   1,
		Token:         "token",
		DBPath:        dbPath,
		LogPath:       paths.Log,
		StartedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	released := make(chan struct{})
	go func() {
		time.Sleep(250 * time.Millisecond)
		dbLock.Close()
		close(released)
	}()

	start := time.Now()
	stopped, err := stopService(context.Background())
	if err != nil {
		t.Fatalf("stop service: %v", err)
	}
	<-released
	if !stopped.(serviceStopped).Stopped {
		t.Fatal("stop reported that the service was not stopped")
	}
	if elapsed := time.Since(start); elapsed < 200*time.Millisecond {
		t.Fatalf("stop returned after %s, before the lock was released", elapsed)
	}
	if _, err := os.Stat(paths.State); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("service state after stop: %v", err)
	}
}

func TestStopClearsStaleStateWithoutWaiting(t *testing.T) {
	dir := t.TempDir()
	isolateDataDir(t)
	paths := dataServicePaths(t)
	dbPath := filepath.Join(dir, "gilir.sqlite")

	// A foreground `gilir serve` (or any process) holds the database, while a
	// killed worker's service.json still names a dead URL. Stop must not wait
	// the full timeout on a process it does not own.
	dbLock, err := lockFile(dbPath + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	defer dbLock.Close()
	if err := writeServiceState(paths, serviceState{
		Version:       serviceVersion,
		Instance:      "dead-worker",
		PID:           1,
		RequestedHost: "127.0.0.1",
		RequestedPort: 44547,
		Host:          "127.0.0.1",
		Port:          44547,
		URL:           "http://127.0.0.1:1",
		ControlPort:   1,
		Token:         "token",
		DBPath:        dbPath,
		LogPath:       paths.Log,
		StartedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	stopped, err := stopService(context.Background())
	if err != nil {
		t.Fatalf("stop service: %v", err)
	}
	if stopped.(serviceStopped).Stopped {
		t.Fatal("stop reported a stale record as stopped")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("stop waited %s on a stale record", elapsed)
	}
	if _, err := os.Stat(paths.State); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale service state survived: %v", err)
	}

	// The same record must not block a start on that database either.
	if _, err := startService(context.Background(), "127.0.0.1", "0", dbPath); err == nil {
		t.Fatal("start accepted a database held by another process")
	} else if !strings.Contains(err.Error(), "not the managed service") {
		t.Fatalf("start error = %v, want an unrelated-holder message", err)
	}
}

func TestRemoveServiceStateIsInstanceAware(t *testing.T) {
	isolateDataDir(t)
	paths := dataServicePaths(t)
	write := func(instance string) {
		if err := writeServiceState(paths, serviceState{
			Version:     serviceVersion,
			Instance:    instance,
			URL:         "http://127.0.0.1:44547",
			Token:       "token",
			DBPath:      "/tmp/gilir.sqlite",
			LogPath:     paths.Log,
			Port:        44547,
			ControlPort: 44548,
		}); err != nil {
			t.Fatal(err)
		}
	}

	// A worker that lost its record must not delete the replacement another
	// worker published.
	write("worker-b")
	removeServiceStateIf(paths, "worker-a")
	if state, ok := readServiceState(paths); !ok || state.Instance != "worker-b" {
		t.Fatalf("state after foreign cleanup = %+v, %v", state, ok)
	}
	removeServiceStateIf(paths, "worker-b")
	if _, ok := readServiceState(paths); ok {
		t.Fatal("own record survived cleanup")
	}
}

func TestServiceStartRefusesDatabaseHeldByForegroundServe(t *testing.T) {
	dir, dbPath := serveTestEnv(t)
	t.Setenv("GILIR_OPENCODE_URL", "http://127.0.0.1:"+freePort(t))
	t.Setenv("GILIR_WEB_DIR", filepath.Join(dir, "missing"))
	lock, err := lockFile(dbPath + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()

	if _, err := startService(context.Background(), "127.0.0.1", "0", ""); err == nil ||
		!strings.Contains(err.Error(), "in use by another gilir process") {
		t.Fatalf("start service on a served database = %v, want a lock conflict", err)
	}
	if _, err := os.Stat(dataServicePaths(t).State); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused start published service state: %v", err)
	}
}

func TestServeRunsAlongsideManagedServiceOnAnotherDatabase(t *testing.T) {
	dir, managedDB := serveTestEnv(t)
	t.Setenv("GILIR_OPENCODE_URL", "http://127.0.0.1:"+freePort(t))
	t.Setenv("GILIR_WEB_DIR", filepath.Join(dir, "missing"))
	t.Cleanup(func() { _, _ = stopService(context.Background()) })
	managed, err := startService(context.Background(), "127.0.0.1", "0", "")
	if err != nil {
		t.Fatalf("start service: %v", err)
	}

	dataDir := os.Getenv("XDG_DATA_HOME")
	foregroundDB := filepath.Join(dir, "foreground.sqlite")
	port := freePort(t)
	proc := startServeSubprocess(t, []string{
		"XDG_DATA_HOME=" + dataDir,
		"LOCALAPPDATA=" + dataDir,
		"GILIR_OPENCODE_URL=http://127.0.0.1:" + freePort(t),
		"GILIR_WEB_DIR=" + filepath.Join(dir, "missing"),
	}, "serve", "--host", "127.0.0.1", "--port", port, "--db", foregroundDB)
	target := "http://127.0.0.1:" + port
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := serviceStatusAt(context.Background(), target); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("foreground serve never answered (stderr %q)", proc.stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Both instances serve independently: the managed service's state still
	// points at its own database and stays reachable.
	status, err := queryServiceStatus(context.Background())
	if err != nil {
		t.Fatalf("managed status: %v", err)
	}
	if got := status.(serviceStatus); !got.Running || got.DBPath != managedDB {
		t.Fatalf("managed status = %+v, want the managed database %q", got, managedDB)
	}

	if err := proc.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := proc.wait(t, 15*time.Second); err != nil {
		t.Fatalf("foreground serve exited with %v", err)
	}
	if got := proc.stdout.String(); got != "" {
		t.Fatalf("foreground serve wrote to stdout: %q", got)
	}
	if _, ok := managed.(serviceStatus); !ok {
		t.Fatalf("start service result = %T, want serviceStatus", managed)
	}
	if stopped, err := stopService(context.Background()); err != nil || !stopped.(serviceStopped).Stopped {
		t.Fatalf("stop managed service = %+v, %v", stopped, err)
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
