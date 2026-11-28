package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
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

func TestResolveWorkerListenUsesOnlyThePrivateAddress(t *testing.T) {
	t.Setenv(serviceListenEnvName, "[::0]:4718")
	t.Setenv("GILIR_URL", "http://127.0.0.1:1")
	got, err := resolveWorkerListen("", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.address() != "[::]:4718" {
		t.Fatalf("worker listen address = %q, want [::]:4718", got.address())
	}
	got, err = resolveWorkerListen("127.0.0.1", "9000")
	if err != nil {
		t.Fatal(err)
	}
	if got.address() != "127.0.0.1:9000" {
		t.Fatalf("explicit listen address = %q, want 127.0.0.1:9000", got.address())
	}
}

func TestServiceURLMapsWildcardToLoopback(t *testing.T) {
	tests := []struct {
		host string
		want string
	}{
		{host: "0.0.0.0", want: "http://127.0.0.1:9000"},
		{host: "::", want: "http://[::1]:9000"},
		{host: "::0", want: "http://[::1]:9000"},
		{host: "localhost", want: "http://127.0.0.1:9000"},
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
	// The audit hook uses the default event set, which does not include the
	// project.created activity this test writes, so its command never runs.
	if err := os.WriteFile(hooksPath, []byte(`[{"name":"audit","command":["false"]}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GILIR_DB", dbPath)
	t.Setenv("GILIR_URL", "http://127.0.0.1:1") // reserved; nothing listens
	t.Setenv("GILIR_OPENCODE_URL", "http://127.0.0.1:1")
	t.Setenv("GILIR_WEB_DIR", filepath.Join(dir, "missing"))
	t.Setenv("GILIR_HOOKS", hooksPath)
	t.Setenv("OPENCODE_PASSWORD", "")

	// A stateful command before the service starts must fail at the target
	// rather than create or open the database itself.
	target := os.Getenv("GILIR_URL")
	if _, err := execute(context.Background(), "project.create", "user", map[string]any{"name": "Before"}); err == nil ||
		!strings.Contains(err.Error(), target) || !strings.Contains(err.Error(), "gilir service start") {
		t.Fatalf("offline command error = %v", err)
	}
	if _, err := os.Stat(dbPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("client created the database: %v", err)
	}
	offline, err := queryServiceStatus(context.Background())
	if err != nil {
		t.Fatalf("offline status: %v", err)
	}
	if offline.(serviceStatus).Running {
		t.Fatal("offline status reported a running service")
	}

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

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := runDaemon(ctx, "127.0.0.1", "0")
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

	// Startup skips history: the cursor begins at the seeded event, before
	// any later activity exists for the hook to consume.
	if cursor := hookCursor(t, dbPath, "audit"); cursor != 1 {
		t.Fatalf("startup hook cursor = %d, want 1", cursor)
	}

	// Ordinary commands follow GILIR_URL to the running service rather than
	// opening the database or inferring the address from service state.
	t.Setenv("GILIR_URL", status.URL)
	created, err := execute(context.Background(), "project.create", "agent:test", map[string]any{"name": "Central"})
	if err != nil {
		t.Fatal(err)
	}
	var project struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(created.(json.RawMessage), &project); err != nil {
		t.Fatal(err)
	}
	if project.ID != 2 || project.Name != "Central" {
		t.Fatalf("created project = %+v", project)
	}

	// The public API must not expose process shutdown, even though it can
	// change queue state.
	if _, err := callDaemon(context.Background(), status.URL, "daemon.stop", json.RawMessage(`{}`), time.Second); err == nil {
		t.Fatal("daemon.stop was accepted on the public API")
	}
	if result, err := queryServiceStatus(context.Background()); err != nil || !result.(serviceStatus).Running {
		t.Fatalf("service stopped after a rejected daemon.stop: %+v %v", result, err)
	}

	if _, err := runDaemon(context.Background(), "127.0.0.1", "0"); err == nil || !strings.Contains(err.Error(), "already running") {
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
	// The seeded event was the latest at startup and remains the recorded
	// cursor baseline; the later project.created event was not replayed.
	if cursor := hookCursor(t, dbPath, "audit"); cursor != 1 || latest < 2 {
		t.Fatalf("hook cursor = %d with latest %d, want 1 with later activity", cursor, latest)
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

func TestCustomPortDaemonHooksReceiveItsOwnURL(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /bin/sh for the hook")
	}
	for _, host := range []string{"127.0.0.1", "localhost", "0.0.0.0", "::1", "::0"} {
		t.Run(host, func(t *testing.T) {
			dir := t.TempDir()
			dbPath := filepath.Join(dir, "gilir.sqlite")
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

			t.Setenv("GILIR_DB", dbPath)
			// Service startup must ignore GILIR_URL; its hooks must still get
			// the URL it actually bound, not the inherited value.
			t.Setenv("GILIR_URL", "http://127.0.0.1:1")
			t.Setenv("GILIR_OPENCODE_URL", "http://127.0.0.1:"+freePort(t))
			t.Setenv("GILIR_WEB_DIR", filepath.Join(dir, "missing"))
			t.Setenv("GILIR_HOOKS", hooksPath)
			t.Setenv("OPENCODE_PASSWORD", "")

			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() {
				_, err := runDaemon(ctx, host, "0")
				done <- err
			}()
			t.Cleanup(func() {
				cancel()
				select {
				case <-done:
				case <-time.After(15 * time.Second):
				}
			})

			// Wait for the daemon to publish its ephemeral address, then use
			// exactly the URL it advertises for local clients.
			deadline := time.Now().Add(10 * time.Second)
			var target string
			for target == "" {
				if state, ok := readServiceState(dbPath); ok {
					target = state.URL
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("daemon did not publish its address")
				}
				time.Sleep(50 * time.Millisecond)
			}
			endpoint, err := commandEndpoint(target)
			if err != nil {
				t.Fatalf("advertised URL %q: %v", target, err)
			}
			for {
				if _, err := sendCommand(ctx, endpoint, "daemon.status", "user", map[string]any{}); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("daemon at %s unavailable: %v", target, err)
				}
				time.Sleep(50 * time.Millisecond)
			}
			if _, err := sendCommand(ctx, endpoint, "project.create", "user", map[string]any{"name": "Hooked"}); err != nil {
				t.Fatal(err)
			}

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

			// The hook env must let gilir reach this service. Executing the
			// same command through the client proves the advertised URL passes
			// the Host guard for this bind.
			t.Setenv("GILIR_URL", target)
			if _, err := execute(ctx, "project.create", "hook:capture", map[string]any{"name": "Routed"}); err != nil {
				t.Fatalf("ordinary command via hook URL: %v", err)
			}
		})
	}
}

func TestServiceStartsDetachedAndStatusUsesState(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "gilir.sqlite")
	t.Setenv("GILIR_DB", dbPath)
	// The client destination must never choose the service bind address.
	t.Setenv("GILIR_URL", "http://127.0.0.1:1")
	t.Setenv("GILIR_OPENCODE_URL", "http://127.0.0.1:"+freePort(t))
	t.Setenv("GILIR_WEB_DIR", filepath.Join(dir, "missing"))
	t.Setenv("OPENCODE_PASSWORD", "")
	t.Cleanup(func() { _, _ = stopService(context.Background()) })

	started, err := startService(context.Background(), "127.0.0.1", "0")
	if err != nil {
		t.Fatalf("start service: %v", err)
	}
	status := started.(serviceStatus)
	if !status.Running || status.PID == 0 || status.Port == 0 || status.URL == "" {
		t.Fatalf("started status = %+v", status)
	}
	if status.URL == "http://127.0.0.1:1" {
		t.Fatalf("service bind address came from GILIR_URL: %+v", status)
	}
	if status.PID == os.Getpid() {
		t.Fatal("service runs inside the launcher instead of a detached worker")
	}
	if _, err := os.Stat(serviceStatePath(dbPath)); err != nil {
		t.Fatalf("service state: %v", err)
	}

	// A later invocation must find the service through local state even when
	// GILIR_URL points somewhere else; only ordinary commands use that URL.
	t.Setenv("GILIR_URL", "http://127.0.0.1:1")
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

func TestServiceStartWildcardRoutesCommandsViaGILIRURL(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "gilir.sqlite")
	t.Setenv("GILIR_DB", dbPath)
	// A launcher's client destination must not choose the worker bind.
	t.Setenv("GILIR_URL", "http://127.0.0.1:1")
	t.Setenv("GILIR_OPENCODE_URL", "http://127.0.0.1:"+freePort(t))
	t.Setenv("GILIR_WEB_DIR", filepath.Join(dir, "missing"))
	t.Setenv("OPENCODE_PASSWORD", "")
	t.Cleanup(func() { _, _ = stopService(context.Background()) })

	started, err := startService(context.Background(), "0.0.0.0", "0")
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
	if _, err := execute(context.Background(), "daemon.update", "user", map[string]any{"paused": true}); err != nil {
		t.Fatalf("queue control via GILIR_URL: %v", err)
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

func TestServiceAllowsDNSHostnamesWhenEnabled(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GILIR_DB", filepath.Join(dir, "gilir.sqlite"))
	t.Setenv("GILIR_OPENCODE_URL", "http://127.0.0.1:"+freePort(t))
	t.Setenv("GILIR_WEB_DIR", dir)
	t.Setenv("GILIR_ALLOW_ANY_HOST", "1")
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("dashboard"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = stopService(context.Background()) })

	started, err := startService(context.Background(), "0.0.0.0", "0")
	if err != nil {
		t.Fatalf("start service: %v", err)
	}
	status := started.(serviceStatus)
	port := fmt.Sprint(status.Port)
	client := &http.Client{Timeout: time.Second}
	for _, test := range []struct {
		name, host, origin, command string
		want                        int
	}{
		{name: "short name", host: "agentic:" + port, want: http.StatusOK},
		{name: "MagicDNS", host: "agentic.example.ts.net:" + port, want: http.StatusOK},
		{name: "arbitrary domain", host: "gilir.example.org:" + port, want: http.StatusOK},
		{name: "invalid hostname", host: "-invalid.example.org:" + port, want: http.StatusForbidden},
		{name: "wrong port", host: "agentic:0", want: http.StatusForbidden},
		{name: "same-origin API", host: "agentic.example.ts.net:" + port, origin: "http://agentic.example.ts.net:" + port, command: `{"command":"daemon.status"}`, want: http.StatusOK},
		{name: "cross-origin API", host: "agentic:" + port, origin: "http://other.example.ts.net:" + port, command: `{"command":"daemon.status"}`, want: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			method, path := http.MethodGet, "/"
			if test.command != "" {
				method, path = http.MethodPost, "/api/v1/command"
			}
			req, err := http.NewRequest(method, status.URL+path, strings.NewReader(test.command))
			if err != nil {
				t.Fatal(err)
			}
			req.Host = test.host
			if test.command != "" {
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Origin", test.origin)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != test.want {
				t.Fatalf("HTTP %d, want %d", resp.StatusCode, test.want)
			}
		})
	}
}

func TestServiceStatusRejectsForeignInstance(t *testing.T) {
	dir := t.TempDir()
	liveDB := filepath.Join(dir, "live.sqlite")
	foreignDB := filepath.Join(dir, "foreign.sqlite")
	t.Setenv("GILIR_DB", liveDB)
	t.Setenv("GILIR_OPENCODE_URL", "http://127.0.0.1:"+freePort(t))
	t.Setenv("GILIR_WEB_DIR", filepath.Join(dir, "missing"))
	t.Setenv("OPENCODE_PASSWORD", "")

	if _, err := startService(context.Background(), "127.0.0.1", "0"); err != nil {
		t.Fatalf("start service: %v", err)
	}
	t.Cleanup(func() { _, _ = stopService(context.Background()) })

	// Simulate another database whose state file survived a crash and whose
	// port was then reused by the live service: the URL answers, but the
	// instance is not this database's.
	state, ok := readServiceState(liveDB)
	if !ok {
		t.Fatal("live service state missing")
	}
	state.Instance = "someone-else"
	if err := writeServiceState(foreignDB, state); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GILIR_DB", foreignDB)
	result, err := queryServiceStatus(context.Background())
	if err != nil {
		t.Fatalf("foreign status: %v", err)
	}
	if result.(serviceStatus).Running {
		t.Fatalf("foreign state reported a running service: %+v", result)
	}
	t.Setenv("GILIR_DB", liveDB)
	if stopped, err := stopService(context.Background()); err != nil || !stopped.(serviceStopped).Stopped {
		t.Fatalf("stop live service = %+v, %v", stopped, err)
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
