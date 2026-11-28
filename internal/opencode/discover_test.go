package opencode

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// fakeCLI stands in for the opencode executable. It records invocations and
// returns canned stdout/stderr/error per joined argument list.
type fakeCLI struct {
	calls  map[string]int
	stdout map[string]string
	stderr map[string]string
	err    map[string]error
}

func newFakeCLI() *fakeCLI {
	return &fakeCLI{
		calls:  map[string]int{},
		stdout: map[string]string{},
		stderr: map[string]string{},
		err:    map[string]error{},
	}
}

func (f *fakeCLI) run(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
	key := name + " " + strings.Join(args, " ")
	f.calls[key]++
	return []byte(f.stdout[key]), []byte(f.stderr[key]), f.err[key]
}

func (f *fakeCLI) count(args string) int { return f.calls["opencode "+args] }

func cliWith(f *fakeCLI) *CLI { return &CLI{run: f.run} }

// discovery is a scripted Discovery for connector tests.
type discovery struct {
	urlCalls      int
	passwordCalls int
	url           string
	password      string
	urlErr        error
	passwordErr   error
}

func (d *discovery) URL(context.Context) (string, error) {
	d.urlCalls++
	return d.url, d.urlErr
}

func (d *discovery) Password(context.Context) (string, error) {
	d.passwordCalls++
	return d.password, d.passwordErr
}

func TestParseServiceStatus(t *testing.T) {
	valid := map[string]string{
		"http://127.0.0.1:49374\n":            "http://127.0.0.1:49374",
		"  https://[::1]:4096  \n":            "https://[::1]:4096",
		"advisory line\nhttp://127.0.0.1:9\n": "http://127.0.0.1:9",
		"http://localhost:49374\r\n":          "http://localhost:49374",
	}
	for raw, want := range valid {
		got, err := parseServiceStatus([]byte(raw))
		if err != nil || got != want {
			t.Errorf("parseServiceStatus(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}

	for _, raw := range []string{"stopped\n", "STOPPED", "not running\n", "  service is not running  \n"} {
		if _, err := parseServiceStatus([]byte(raw)); !errors.Is(err, ErrServiceStopped) {
			t.Errorf("parseServiceStatus(%q) error = %v, want ErrServiceStopped", raw, err)
		}
	}

	for _, raw := range []string{"", "\n\n", "something else\n", "127.0.0.1:49374\n", "ftp://127.0.0.1\n"} {
		if _, err := parseServiceStatus([]byte(raw)); err == nil || errors.Is(err, ErrServiceStopped) {
			t.Errorf("parseServiceStatus(%q) error = %v, want a parse error", raw, err)
		}
	}
}

func TestParseServicePassword(t *testing.T) {
	if got, err := parseServicePassword([]byte("s3cret\n")); err != nil || got != "s3cret" {
		t.Fatalf("password = %q, %v", got, err)
	}
	if got, err := parseServicePassword([]byte("  s3cret  \r\n\n")); err != nil || got != "s3cret" {
		t.Fatalf("padded password = %q, %v", got, err)
	}
	for _, raw := range []string{"", "\n\n", "first\nsecond\n"} {
		if _, err := parseServicePassword([]byte(raw)); err == nil {
			t.Errorf("parseServicePassword(%q) accepted unexpected output", raw)
		}
	}
}

func TestCLIURLAndPasswordUseExactSubcommands(t *testing.T) {
	f := newFakeCLI()
	f.stdout["opencode service status"] = "http://127.0.0.1:49374\n"
	f.stdout["opencode service get password"] = "pw123\n"
	c := cliWith(f)

	url, err := c.URL(context.Background())
	if err != nil || url != "http://127.0.0.1:49374" {
		t.Fatalf("URL = %q, %v", url, err)
	}
	password, err := c.Password(context.Background())
	if err != nil || password != "pw123" {
		t.Fatalf("Password = %q, %v", password, err)
	}
	if f.count("service status") != 1 || f.count("service get password") != 1 {
		t.Fatalf("calls = %v, want exactly one of each query", f.calls)
	}
}

func TestCLIStatusStoppedIsSentinel(t *testing.T) {
	f := newFakeCLI()
	f.stdout["opencode service status"] = "stopped\n"
	if _, err := cliWith(f).URL(context.Background()); !errors.Is(err, ErrServiceStopped) {
		t.Fatalf("stopped status error = %v, want ErrServiceStopped", err)
	}
}

// Gilir must never start OpenCode on its own, even when it is stopped.
func TestCLINeverStartsTheService(t *testing.T) {
	f := newFakeCLI()
	f.stdout["opencode service status"] = "stopped\n"
	f.stdout["opencode service get password"] = "pw\n"
	c := cliWith(f)
	_, _ = c.URL(context.Background())
	_, _ = c.Password(context.Background())
	for call := range f.calls {
		if strings.Contains(call, " service start") || strings.Contains(call, " service restart") {
			t.Fatalf("discovery started the service: %q", call)
		}
	}
	if len(f.calls) != 2 {
		t.Fatalf("discovery ran %d commands, want only status and get password: %v", len(f.calls), f.calls)
	}
}

func TestCLIMissingBinaryIsActionable(t *testing.T) {
	f := newFakeCLI()
	f.err["opencode service status"] = fmt.Errorf("start: %w", exec.ErrNotFound)
	_, err := cliWith(f).URL(context.Background())
	if err == nil || !strings.Contains(err.Error(), "opencode CLI is not installed") {
		t.Fatalf("missing binary error = %v", err)
	}
	if strings.Contains(err.Error(), "exec:") {
		t.Fatalf("missing binary error leaked exec detail: %v", err)
	}
}

func TestCLIFailureReportsFirstStderrLine(t *testing.T) {
	f := newFakeCLI()
	f.err["opencode service get password"] = errors.New("exit status 1")
	f.stderr["opencode service get password"] = "\nError: Unknown service config key: password\nmore detail\n"
	_, err := cliWith(f).Password(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Unknown service config key") {
		t.Fatalf("failure error = %v", err)
	}
	if strings.Contains(err.Error(), "more detail") {
		t.Fatalf("failure error included later stderr lines: %v", err)
	}
}

func TestCLITimeoutIsReported(t *testing.T) {
	f := newFakeCLI()
	f.err["opencode service status"] = context.DeadlineExceeded
	_, err := cliWith(f).URL(context.Background())
	if err == nil || !strings.Contains(err.Error(), "did not respond") {
		t.Fatalf("timeout error = %v", err)
	}
}

func TestConnectorUsesOverridesWithoutDiscovery(t *testing.T) {
	url, password := "http://127.0.0.1:4096", "explicit"
	conn, err := NewConnector(nil, Overrides{URL: &url, Password: &password})
	if err != nil {
		t.Fatal(err)
	}
	client, serverURL, err := conn.Client(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if serverURL != url {
		t.Fatalf("server URL = %q, want %q", serverURL, url)
	}
	if client.ServerURL() != url {
		t.Fatalf("client origin = %q", client.ServerURL())
	}
}

func TestConnectorRejectsInvalidOverrideAtConstruction(t *testing.T) {
	bad := "http://example.com:4096"
	if _, err := NewConnector(nil, Overrides{URL: &bad}); err == nil {
		t.Fatal("non-loopback override was accepted")
	}
	if _, err := NewConnector(nil, Overrides{}); err == nil {
		t.Fatal("connector without discovery or overrides was accepted")
	}
}

func TestConnectorDiscoversAndCaches(t *testing.T) {
	d := &discovery{url: "http://127.0.0.1:49374", password: "pw"}
	conn, err := NewConnector(d, Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, _, err := conn.Client(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if d.urlCalls != 1 || d.passwordCalls != 1 {
		t.Fatalf("discovery calls = %d/%d, want one each (cached client)", d.urlCalls, d.passwordCalls)
	}
}

func TestConnectorHonorsPartialOverrides(t *testing.T) {
	d := &discovery{url: "http://127.0.0.1:49999", password: "discovered"}
	url := "http://127.0.0.1:4096"
	conn, err := NewConnector(d, Overrides{URL: &url})
	if err != nil {
		t.Fatal(err)
	}
	if _, serverURL, err := conn.Client(context.Background()); err != nil || serverURL != url {
		t.Fatalf("server URL = %q, %v; want override %q", serverURL, err, url)
	}
	if d.urlCalls != 0 || d.passwordCalls != 1 {
		t.Fatalf("discovery calls = %d/%d, want only the password discovered", d.urlCalls, d.passwordCalls)
	}
}

func TestConnectorBacksOffWhileStopped(t *testing.T) {
	d := &discovery{urlErr: ErrServiceStopped}
	conn, err := NewConnector(d, Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	now := conn.now()
	conn.now = func() time.Time { return now }
	ctx := context.Background()

	for i := range 10 {
		if _, _, err := conn.Client(ctx); !errors.Is(err, ErrServiceStopped) {
			t.Fatalf("client %d error = %v, want ErrServiceStopped", i, err)
		}
	}
	if d.urlCalls != 1 {
		t.Fatalf("discovery calls while backing off = %d, want 1", d.urlCalls)
	}

	now = now.Add(initialRetryDelay)
	if _, _, err := conn.Client(ctx); !errors.Is(err, ErrServiceStopped) {
		t.Fatalf("client after first delay = %v", err)
	}
	if d.urlCalls != 2 {
		t.Fatalf("discovery calls after delay = %d, want 2", d.urlCalls)
	}

	// The next window doubles.
	now = now.Add(initialRetryDelay)
	if _, _, err := conn.Client(ctx); !errors.Is(err, ErrServiceStopped) {
		t.Fatalf("client inside second window = %v", err)
	}
	if d.urlCalls != 2 {
		t.Fatalf("discovery calls inside doubled window = %d, want still 2", d.urlCalls)
	}
	now = now.Add(initialRetryDelay)
	if _, _, err := conn.Client(ctx); !errors.Is(err, ErrServiceStopped) {
		t.Fatalf("client after doubled window = %v", err)
	}
	if d.urlCalls != 3 {
		t.Fatalf("discovery calls after doubled window = %d, want 3", d.urlCalls)
	}
}

func TestConnectorReconnectsOnceServiceStarts(t *testing.T) {
	d := &discovery{urlErr: ErrServiceStopped}
	conn, err := NewConnector(d, Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	now := conn.now()
	conn.now = func() time.Time { return now }

	if _, _, err := conn.Client(context.Background()); !errors.Is(err, ErrServiceStopped) {
		t.Fatalf("first client = %v", err)
	}
	d.url, d.urlErr, d.password = "http://127.0.0.1:49374", nil, "pw"
	now = now.Add(maxRetryDelay)
	client, serverURL, err := conn.Client(context.Background())
	if err != nil || serverURL != d.url || client == nil {
		t.Fatalf("client after start = %v, %q, %v", client, serverURL, err)
	}
}

func TestConnectorIgnores401WithExplicitPassword(t *testing.T) {
	_, _, url := fixture(t, "", map[string]reply{"GET /api/info": {http.StatusUnauthorized, `{}`}})
	d := &discovery{url: url}
	password := "explicit"
	conn, err := NewConnector(d, Overrides{Password: &password})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	client, _, err := conn.Client(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.ServerPID(ctx)
	if !IsUnauthorized(err) {
		t.Fatalf("ServerPID error = %v, want 401", err)
	}
	if conn.Failed(err) {
		t.Fatal("Failed(401) with an explicit password asked for discovery")
	}
	if _, _, err := conn.Client(ctx); err != nil {
		t.Fatal(err)
	}
	if d.urlCalls != 1 || d.passwordCalls != 0 {
		t.Fatalf("URL/password discoveries = %d/%d, want 1/0", d.urlCalls, d.passwordCalls)
	}
}

func TestConnectorRetriesAfterBackoffExpiresDespiteRepeatedAuthFailures(t *testing.T) {
	// Regression: while stale and out of a client, every tick reports the
	// cached error to Failed. Re-arming the backoff from those reports pushed
	// retryAt forward on every tick, so a rotated password was never picked up.
	d := &discovery{url: "http://127.0.0.1:49374", password: "old"}
	conn, err := NewConnector(d, Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	now := conn.now()
	conn.now = func() time.Time { return now }
	ctx := context.Background()

	if _, _, err := conn.Client(ctx); err != nil {
		t.Fatal(err)
	}
	unauthorized := &StatusError{Method: http.MethodGet, Path: "/api/info", StatusCode: http.StatusUnauthorized}

	// First 401: refresh credentials and retry at once.
	if !conn.Failed(unauthorized) {
		t.Fatal("first Failed(401) = false, want an immediate refresh")
	}
	// The refresh returns the same (now wrong) password, so the retry fails.
	if _, _, err := conn.Client(ctx); err != nil {
		t.Fatalf("client after refresh = %v", err)
	}
	// Second 401 with unchanged credentials: back off instead of looping.
	if conn.Failed(unauthorized) {
		t.Fatal("second Failed(401) = true, want a throttled refresh")
	}
	if d.passwordCalls != 2 {
		t.Fatalf("password discoveries = %d, want initial discovery and one refresh", d.passwordCalls)
	}
	deadline := now.Add(initialRetryDelay)
	if _, _, err := conn.Client(ctx); !errors.Is(err, unauthorized) {
		t.Fatalf("client during auth backoff = %v, want the cached auth error", err)
	}

	// Simulate one-second ticks up to the deadline, the way Run does. Each
	// tick retries the stale client and reports the cached error to Failed;
	// that must not push the deadline forward, or the window would never
	// expire while the service stays down.
	for now.Add(time.Second).Before(deadline) {
		now = now.Add(time.Second)
		_, _, err := conn.Client(ctx)
		if !errors.Is(err, unauthorized) {
			t.Fatalf("tick at %v returned %v, want the cached auth error", now, err)
		}
		if conn.Failed(err) {
			t.Fatalf("tick at %v asked for an immediate refresh inside the window", now)
		}
		if d.passwordCalls != 2 {
			t.Fatalf("tick at %v discovered passwords %d times during backoff, want 2", now, d.passwordCalls)
		}
	}

	// The service rotates its password; past the deadline it is picked up.
	d.password = "new"
	now = deadline.Add(time.Millisecond)
	if _, _, err := conn.Client(ctx); err != nil {
		t.Fatalf("client after backoff expired = %v, want recovery", err)
	}
	if d.passwordCalls != 3 {
		t.Fatalf("password discoveries = %d, want 3 after recovery", d.passwordCalls)
	}
	if got := conn.Redact("old and new"); got != "[redacted] and [redacted]" {
		t.Fatalf("Redact = %q", got)
	}
}

func TestConnectorRedactsExplicitAndDiscoveredPasswords(t *testing.T) {
	url, password := "http://127.0.0.1:4096", "explicit"
	conn, err := NewConnector(nil, Overrides{URL: &url, Password: &password})
	if err != nil {
		t.Fatal(err)
	}
	if got := conn.Redact("token explicit here"); got != "token [redacted] here" {
		t.Fatalf("Redact = %q", got)
	}
}

func TestDescribe(t *testing.T) {
	unauthorized := &StatusError{Method: http.MethodGet, Path: "/api/info", StatusCode: http.StatusUnauthorized}
	refused := &testNetError{}
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"stopped", ErrServiceStopped, `OpenCode is not running: start it with "opencode service start"`},
		{"unauthorized", unauthorized, "cannot authenticate to OpenCode: check OPENCODE_PASSWORD or the managed service password"},
		{"unreachable", fmt.Errorf("opencode: GET /api/info: %w", refused), "OpenCode is not reachable: the service may not be running"},
		{"other", errors.New("watcher: boom"), "watcher: boom"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Describe(tc.err); got != tc.want {
				t.Fatalf("Describe = %q, want %q", got, tc.want)
			}
		})
	}
}

// testNetError satisfies net.Error without opening a socket.
type testNetError struct{}

func (*testNetError) Error() string   { return "connection refused" }
func (*testNetError) Timeout() bool   { return false }
func (*testNetError) Temporary() bool { return false }

func TestConnectorUsesKeepAliveClientForRequests(t *testing.T) {
	d := &discovery{url: "", password: "pw"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"pid":42}`))
	}))
	defer srv.Close()
	d.url = srv.URL
	conn, err := NewConnector(d, Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	client, _, err := conn.Client(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pid, err := client.ServerPID(context.Background())
	if err != nil || pid != 42 {
		t.Fatalf("ServerPID = %d, %v", pid, err)
	}
}
