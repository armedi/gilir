package watcher

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/armedi/gilir/internal/opencode"
	"github.com/armedi/gilir/internal/store"
)

// authOC wraps a fakeOC with Basic auth so password rotation is observable.
type authOC struct {
	*fakeOC
	mu       sync.Mutex
	password string
	requests int
}

func (a *authOC) setPassword(password string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.password = password
}

func (a *authOC) requestCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.requests
}

func (a *authOC) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	password := a.password
	a.requests++
	a.mu.Unlock()
	if password != "" {
		want := "Basic " + base64.StdEncoding.EncodeToString([]byte("opencode:"+password))
		if r.Header.Get("Authorization") != want {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"_tag":"UnauthorizedError","message":"Authentication required"}`))
			return
		}
	}
	a.fakeOC.ServeHTTP(w, r)
}

// scriptedDiscovery simulates the managed OpenCode service state. It counts
// calls so tests can prove the CLI is not run on every tick.
type scriptedDiscovery struct {
	mu        sync.Mutex
	url       string
	password  string
	stopped   bool
	urlCalls  int
	passCalls int
}

func (d *scriptedDiscovery) set(stopped bool, url, password string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stopped, d.url, d.password = stopped, url, password
}

func (d *scriptedDiscovery) URL(context.Context) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.urlCalls++
	if d.stopped {
		return "", opencode.ErrServiceStopped
	}
	return d.url, nil
}

func (d *scriptedDiscovery) Password(context.Context) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.passCalls++
	return d.password, nil
}

func (d *scriptedDiscovery) counts() (urls, passwords int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.urlCalls, d.passCalls
}

// discoverySetup builds a task queue wired to an authenticated fake server and
// a scripted discovery source. It never contacts a real OpenCode service.
func discoverySetup(t *testing.T, tasks, maxConcurrency int, password string) (*env, *authOC, *scriptedDiscovery) {
	t.Helper()
	e := setup(t, tasks, maxConcurrency)
	auth := &authOC{fakeOC: e.oc, password: password}
	srv := httptest.NewServer(auth)
	t.Cleanup(srv.Close)
	e.url = srv.URL
	return e, auth, &scriptedDiscovery{url: srv.URL, password: password}
}

func (e *env) discoveryWatcher(t *testing.T, d *scriptedDiscovery) *Watcher {
	t.Helper()
	conn, err := opencode.NewConnector(d, opencode.Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	return NewWithConnection(e.st, conn)
}

func TestWatcherStaysFailClosedWhileOpenCodeIsStopped(t *testing.T) {
	e, auth, d := discoverySetup(t, 1, 1, "pw")
	d.set(true, e.url, "pw")
	w := e.discoveryWatcher(t, d)

	if err := w.Tick(e.ctx); err == nil {
		t.Fatal("Tick while stopped succeeded, want an error")
	}
	snap := w.Snapshot()
	if snap.ActiveParents != nil {
		t.Fatalf("active parents while stopped = %v, want nil", snap.ActiveParents)
	}
	if !strings.Contains(snap.Error, "OpenCode is not running") {
		t.Fatalf("snapshot error = %q, want an OpenCode-not-running warning", snap.Error)
	}
	if got := e.status(t, 0); got != store.StatusTodo {
		t.Fatalf("task status while stopped = %s, want todo (fail closed)", got)
	}
	if n := auth.requestCount(); n != 0 {
		t.Fatalf("HTTP requests while stopped = %d, want none", n)
	}
}

func TestWatcherRecoversFromPasswordRotation(t *testing.T) {
	e, auth, d := discoverySetup(t, 1, 1, "old")
	w := e.discoveryWatcher(t, d)
	e.tick(t, w)
	wantActive(t, w, 0)
	if got := e.status(t, 0); got != store.StatusInProgress {
		t.Fatalf("initial dispatch status = %s", got)
	}
	e.tick(t, w)
	if urls, passwords := d.counts(); urls != 1 || passwords != 1 {
		t.Fatalf("discovery calls after two healthy ticks = %d/%d, want one each", urls, passwords)
	}

	// The service rotated its password: the cached client now gets a 401,
	// discovery re-reads the password, and the same tick succeeds.
	auth.setPassword("new")
	d.set(false, e.url, "new")
	if err := w.Tick(e.ctx); err != nil {
		t.Fatalf("Tick across a password rotation = %v, want a clean retry", err)
	}
	wantActive(t, w, 0)
	if snap := w.Snapshot(); snap.Error != "" {
		t.Fatalf("snapshot error after rotation = %q, want none", snap.Error)
	}
	if _, after := d.counts(); after != 2 {
		t.Fatalf("password discoveries after rotation = %d, want initial discovery and one refresh", after)
	}
}

func TestWatcherReportsUnauthorizedWithoutLeakingPassword(t *testing.T) {
	e, auth, d := discoverySetup(t, 1, 1, "expected")
	w := e.discoveryWatcher(t, d)
	e.tick(t, w)

	// The server refuses every password, including the re-discovered one.
	auth.setPassword("different")
	if err := w.Tick(e.ctx); err == nil {
		t.Fatal("Tick with a rejected password succeeded, want an error")
	}
	snap := w.Snapshot()
	if snap.ActiveParents != nil {
		t.Fatalf("active parents = %v, want nil after a failed tick", *snap.ActiveParents)
	}
	if !strings.Contains(snap.Error, "cannot authenticate to OpenCode") {
		t.Fatalf("snapshot error = %q, want a clear authentication message", snap.Error)
	}
	for _, secret := range []string{"expected", "different"} {
		if strings.Contains(snap.Error, secret) {
			t.Fatalf("snapshot error leaked password %q: %q", secret, snap.Error)
		}
	}
}
