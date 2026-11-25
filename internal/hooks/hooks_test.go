package hooks

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	cases := []struct {
		name string
		file string
		want string // error substring; empty means valid
	}{
		{"valid", `[{"name":"notify","events":["task.commented"],"url":"https://example.test/hook"},{"name":"local.sh","command":["/bin/echo","$HOME"]}]`, ""},
		{"empty array", `[]`, ""},
		{"not array", `{"name":"a","url":"http://x"}`, "JSON array"},
		{"null", `null`, "JSON array"},
		{"trailing data", `[] []`, "after JSON array"},
		{"unknown field", `[{"name":"a","url":"http://x","shell":"echo"}]`, "unknown field"},
		{"no destination", `[{"name":"a"}]`, "exactly one"},
		{"both destinations", `[{"name":"a","url":"http://x","command":["true"]}]`, "exactly one"},
		{"empty command", `[{"name":"a","command":[]}]`, "non-empty argv"},
		{"empty program", `[{"name":"a","command":[""]}]`, "non-empty argv"},
		{"bad name", `[{"name":"a b","url":"http://x"}]`, "invalid name"},
		{"missing name", `[{"url":"http://x"}]`, "invalid name"},
		{"duplicate name", `[{"name":"a","url":"http://x"},{"name":"a","command":["true"]}]`, "duplicate name"},
		{"credentials", `[{"name":"a","url":"https://user:secret@example.test/"}]`, "credentials"},
		{"scheme", `[{"name":"a","url":"file:///etc/passwd"}]`, "http or https"},
		{"no host", `[{"name":"a","url":"http:///path"}]`, "host"},
		{"port without host", `[{"name":"a","url":"http://:8080/path"}]`, "host"},
		{"empty port", `[{"name":"a","url":"http://example.test:/path"}]`, "port must not be empty"},
		{"malformed path", `[{"name":"a","url":"https://example.test/secret%zz"}]`, "not a valid URL"},
		{"malformed port", `[{"name":"a","url":"https://example.test:secret/"}]`, "not a valid URL"},
		{"empty event", `[{"name":"a","url":"http://x","events":[""]}]`, "empty event"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "hooks.json")
			if err := os.WriteFile(path, []byte(tc.file), 0o600); err != nil {
				t.Fatal(err)
			}
			configs, err := Load(path)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("Load: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load = %v, %v; want error containing %q", configs, err, tc.want)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatalf("error leaks credentials: %v", err)
			}
		})
	}

	path := filepath.Join(t.TempDir(), "hooks.json")
	os.WriteFile(path, []byte(cases[0].file), 0o600)
	configs, err := Load(path)
	want := []Config{
		{Name: "notify", Events: []string{"task.commented"}, URL: "https://example.test/hook"},
		{Name: "local.sh", Command: []string{"/bin/echo", "$HOME"}},
	}
	if err != nil || !reflect.DeepEqual(configs, want) {
		t.Fatalf("Load = %#v, %v; want %#v", configs, err, want)
	}
}

// memorySource is an in-memory Source recording every persisted cursor.
type memorySource struct {
	mu      sync.Mutex
	events  []Event
	cursors map[string]int64
	history []int64
}

func newMemorySource(events ...Event) *memorySource {
	return &memorySource{events: events, cursors: map[string]int64{}}
}

func (s *memorySource) After(_ context.Context, afterID int64, limit int) ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Event
	for _, event := range s.events {
		if event.ID > afterID && len(out) < limit {
			out = append(out, event)
		}
	}
	return out, nil
}

func (s *memorySource) Cursor(_ context.Context, name string) (int64, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.cursors[name]
	return id, ok, nil
}

func (s *memorySource) SetCursor(_ context.Context, name string, lastID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cursors[name] = lastID
	s.history = append(s.history, lastID)
	return nil
}

func (s *memorySource) Latest(context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.events) == 0 {
		return 0, nil
	}
	return s.events[len(s.events)-1].ID, nil
}

func (s *memorySource) append(event Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
}

func (s *memorySource) cursor(name string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cursors[name]
}

func (s *memorySource) cursorHistory() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int64(nil), s.history...)
}

func event(id int64, typ, actor string) Event {
	payload, _ := json.Marshal(map[string]any{"id": id, "type": typ, "actor": actor})
	return Event{ID: id, Type: typ, Actor: actor, Payload: payload}
}

type request struct {
	eventID, contentType, body string
}

// recorder is an httptest handler that records requests and answers with the
// next queued status (200 once the queue is empty).
type recorder struct {
	mu       sync.Mutex
	requests []request
	statuses []int
}

func (r *recorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	r.requests = append(r.requests, request{req.Header.Get("X-Gilir-Event-ID"), req.Header.Get("Content-Type"), string(body)})
	status := http.StatusOK
	if len(r.statuses) > 0 {
		status, r.statuses = r.statuses[0], r.statuses[1:]
	}
	r.mu.Unlock()
	if status == http.StatusFound {
		http.Redirect(w, req, "/elsewhere", status)
		return
	}
	w.WriteHeader(status)
	if status != http.StatusOK {
		io.WriteString(w, "reflected secret")
	}
}

func (r *recorder) seen() []request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]request(nil), r.requests...)
}

type errorLog struct {
	mu     sync.Mutex
	errors []string
}

func (l *errorLog) report(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.errors = append(l.errors, err.Error())
}

func (l *errorLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.errors...)
}

// start runs the hooks in the background; the returned stop cancels Run and
// waits for it to return.
func start(t *testing.T, configs []Config, source Source, log *errorLog) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Run(ctx, configs, source, log.report)
		close(done)
	}()
	stop = func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("Run did not return after cancellation")
		}
	}
	t.Cleanup(stop)
	return stop
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestWebhookDeliversPayloadSkippingSelfAndUnlistedEvents(t *testing.T) {
	rec := &recorder{}
	server := httptest.NewServer(rec)
	defer server.Close()

	source := newMemorySource(
		event(1, "task.commented", "hook:notify"), // own write: skipped
		event(2, "task.commented", "agent:example"),
		event(3, "task.updated", "user"), // not in filter
		event(4, "task.created", "user"), // not in filter
		event(5, "task.commented", "hook:other"),
	)
	source.cursors["notify"] = 0
	var log errorLog
	start(t, []Config{{Name: "notify", Events: []string{"task.commented"}, URL: server.URL}}, source, &log)

	eventually(t, "cursor to reach 5", func() bool { return source.cursor("notify") == 5 })
	want := []request{
		{"2", "application/json", string(source.events[1].Payload)},
		{"5", "application/json", string(source.events[4].Payload)},
	}
	if got := rec.seen(); !reflect.DeepEqual(got, want) {
		t.Fatalf("requests = %#v; want %#v", got, want)
	}
	if errs := log.all(); len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
}

func TestFailedDeliveryRetriesBeforeLaterEventsAndHoldsCursor(t *testing.T) {
	rec := &recorder{statuses: []int{http.StatusFound, http.StatusInternalServerError}}
	server := httptest.NewServer(rec)
	defer server.Close()

	source := newMemorySource(event(1, "task.created", "user"), event(2, "task.claimed", "user"))
	source.cursors["notify"] = 0
	var log errorLog
	start(t, []Config{{Name: "notify", URL: server.URL}}, source, &log)

	eventually(t, "cursor to reach 2", func() bool { return source.cursor("notify") == 2 })
	var ids []string
	for _, r := range rec.seen() {
		ids = append(ids, r.eventID)
	}
	// The redirect and the 500 are failures, the redirect is not followed,
	// and event 1 is redelivered before event 2; the cursor never moves past
	// an undelivered event.
	if want := []string{"1", "1", "1", "2"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("delivered event IDs = %v; want %v", ids, want)
	}
	if got, want := source.cursorHistory(), []int64{1, 2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("cursor history = %v; want %v", got, want)
	}
	errs := log.all()
	if len(errs) != 2 || !strings.Contains(errs[0], "status 302") || !strings.Contains(errs[1], "status 500") {
		t.Fatalf("reported errors = %v; want a 302 then a 500 failure", errs)
	}
	// Response bodies may echo the URL's token, so they are never reported.
	for _, err := range errs {
		if strings.Contains(err, "secret") || strings.Contains(err, "elsewhere") {
			t.Fatalf("error includes response body: %v", err)
		}
	}
}

func TestNewHookStartsAtLatestEvent(t *testing.T) {
	rec := &recorder{}
	server := httptest.NewServer(rec)
	defer server.Close()

	source := newMemorySource(event(1, "task.created", "user"), event(2, "task.created", "user"))
	var log errorLog
	start(t, []Config{{Name: "fresh", URL: server.URL}}, source, &log)

	eventually(t, "cursor initialization", func() bool { return len(source.cursorHistory()) > 0 })
	source.append(event(3, "task.created", "user"))
	eventually(t, "cursor to reach 3", func() bool { return source.cursor("fresh") == 3 })
	if got := rec.seen(); len(got) != 1 || got[0].eventID != "3" {
		t.Fatalf("requests = %#v; want only event 3", got)
	}
}

func TestCommandReceivesArgvStdinAndHookEnvironment(t *testing.T) {
	t.Setenv("GILIR_ACTOR", "user")
	t.Setenv("GILIR_EVENT_ID", "999")
	t.Setenv("OPENCODE_PASSWORD", "secret")
	out := filepath.Join(t.TempDir(), "out")
	script := `printf '%s|%s|%s|%s|' "$1" "$GILIR_ACTOR" "$GILIR_EVENT_ID" "${OPENCODE_PASSWORD-unset}" > "$2"; cat >> "$2"`

	source := newMemorySource(event(7, "session.outcome", "agent:x"))
	source.cursors["local"] = 6
	var log errorLog
	start(t, []Config{{Name: "local", Command: []string{"/bin/sh", "-c", script, "sh", "$HOME;x", out}}}, source, &log)

	eventually(t, "cursor to reach 7", func() bool { return source.cursor("local") == 7 })
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	want := "$HOME;x|hook:local|7|unset|" + string(source.events[0].Payload)
	if string(got) != want {
		t.Fatalf("command saw %q; want %q", got, want)
	}
}

func TestCommandFailureIsReportedAndRetried(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "marker")
	// Fails the first time (creating the marker), succeeds afterwards.
	script := `if [ -e "$1" ]; then exit 0; fi; : > "$1"; echo boom >&2; exit 3`
	source := newMemorySource(event(1, "task.created", "user"))
	source.cursors["local"] = 0
	var log errorLog
	start(t, []Config{{Name: "local", Command: []string{"/bin/sh", "-c", script, "sh", marker}}}, source, &log)

	eventually(t, "cursor to reach 1", func() bool { return source.cursor("local") == 1 })
	if errs := log.all(); len(errs) != 1 || !strings.Contains(errs[0], "exit status 3") || !strings.Contains(errs[0], "boom") {
		t.Fatalf("reported errors = %v; want one exit status 3 failure with output", errs)
	}
}

func TestCappedBufferMarksTruncationOnlyPastLimit(t *testing.T) {
	cases := []struct {
		writes []string
		want   string
	}{
		{[]string{"abcd"}, "abcd"},
		{[]string{"ab", "cd"}, "abcd"},
		{[]string{"abcde"}, "abcd...(truncated)"},
		{[]string{"abcd", "e"}, "abcd...(truncated)"},
		{[]string{"abc", "de", "f"}, "abcd...(truncated)"},
	}
	for _, tc := range cases {
		b := &cappedBuffer{limit: 4}
		for _, w := range tc.writes {
			if n, err := b.Write([]byte(w)); n != len(w) || err != nil {
				t.Fatalf("Write(%q) = %d, %v; want full write", w, n, err)
			}
		}
		if got := b.String(); got != tc.want {
			t.Errorf("writes %q: String() = %q; want %q", tc.writes, got, tc.want)
		}
	}
}
