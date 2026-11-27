package watcher

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/armedi/gilir/internal/opencode"
	"github.com/armedi/gilir/internal/store"
)

// fakeOC is a stateful stand-in for the OpenCode V2 endpoints the watcher
// uses, speaking the same JSON shapes as the 2.4.1 contract.
type fakeOC struct {
	mu       sync.Mutex
	pid      int64
	clock    int64
	sessions map[string]*fakeSession
	active   map[string]bool
	badState bool // report an unknown active state

	creates []map[string]any
	prompts []map[string]any // every POST .../prompt body, retries included
	// promptFault, when set, is consumed by the next prompt: "drop" accepts
	// the message then hangs up, "503" and "400" refuse it.
	promptFault string
	onCreate    func(id string)
	onPrompt    func(sid string)
}

type fakeSession struct {
	id, parent, title, dir, outcome string
	idle                            int64
	inbox                           []string
	delivered                       map[string]int64
}

func newFakeOC() *fakeOC {
	return &fakeOC{pid: 100, clock: 1_700_000_000_000, sessions: map[string]*fakeSession{}, active: map[string]bool{}}
}

func (f *fakeOC) tick() int64 { f.clock += 10; return f.clock }

func (f *fakeOC) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/"), "/")
	reply := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	var after func()
	defer func() {
		f.mu.Unlock()
		if after != nil {
			after()
		}
	}()

	switch {
	case r.Method == "GET" && r.URL.Path == "/api/info":
		reply(200, map[string]any{"pid": f.pid})
	case r.Method == "GET" && r.URL.Path == "/api/session/active":
		data := map[string]any{}
		for id := range f.active {
			state := "running"
			if f.badState {
				state = "paused"
			}
			data[id] = map[string]any{"type": state}
		}
		reply(200, map[string]any{"data": data})
	case r.Method == "POST" && r.URL.Path == "/api/session":
		f.creates = append(f.creates, body)
		id := body["id"].(string)
		s := f.sessions[id]
		if s == nil {
			loc := body["location"].(map[string]any)
			s = &fakeSession{id: id, title: body["title"].(string), dir: loc["directory"].(string), delivered: map[string]int64{}}
			f.sessions[id] = s
		}
		reply(200, map[string]any{"data": f.info(s)})
		if f.onCreate != nil {
			after = func() { f.onCreate(id) }
		}
	case len(parts) >= 2 && parts[0] == "session":
		s := f.sessions[parts[1]]
		if s == nil {
			reply(404, map[string]any{"_tag": "SessionNotFoundError", "sessionID": parts[1], "message": "session not found"})
			return
		}
		switch {
		case r.Method == "GET" && len(parts) == 2:
			reply(200, map[string]any{"data": f.info(s)})
		case r.Method == "GET" && len(parts) == 3 && parts[2] == "inbox":
			items := []any{}
			for _, id := range s.inbox {
				items = append(items, map[string]any{"id": id, "type": "user"})
			}
			reply(200, map[string]any{"data": items})
		case r.Method == "GET" && len(parts) == 4 && parts[2] == "message":
			at, ok := s.delivered[parts[3]]
			if !ok {
				reply(404, map[string]any{"_tag": "MessageNotFoundError", "sessionID": s.id, "messageID": parts[3], "message": "message not found"})
				return
			}
			reply(200, map[string]any{"data": map[string]any{"id": parts[3], "type": "user", "time": map[string]any{"created": at}}})
		case r.Method == "POST" && len(parts) == 3 && parts[2] == "prompt":
			f.prompts = append(f.prompts, body)
			if f.onPrompt != nil {
				f.onPrompt(s.id)
			}
			fault := f.promptFault
			f.promptFault = ""
			switch fault {
			case "503":
				reply(503, map[string]any{"_tag": "Unavailable", "message": "busy"})
				return
			case "400":
				reply(400, map[string]any{"_tag": "InvalidRequestError", "message": "invalid"})
				return
			}
			mid := body["id"].(string)
			if _, done := s.delivered[mid]; !done && !slices.Contains(s.inbox, mid) {
				s.inbox = append(s.inbox, mid)
			}
			if fault == "drop" {
				conn, _, _ := w.(http.Hijacker).Hijack()
				conn.Close()
				return
			}
			reply(200, map[string]any{"data": map[string]any{"id": mid, "sessionID": s.id, "type": "user"}})
		default:
			reply(404, map[string]any{"_tag": "RouteNotFound"})
		}
	default:
		reply(404, map[string]any{"_tag": "RouteNotFound"})
	}
}

func (f *fakeOC) info(s *fakeSession) map[string]any {
	info := map[string]any{
		"id": s.id, "projectID": "prj_1", "title": s.title, "cost": 0, "tokens": map[string]any{},
		"time":     map[string]any{"created": 1, "updated": f.clock, "idle": s.idle},
		"location": map[string]any{"directory": s.dir},
	}
	if s.parent != "" {
		info["parentID"] = s.parent
	}
	if s.outcome != "" {
		info["outcome"] = s.outcome
	}
	return info
}

// external adds a session Gilir did not create.
func (f *fakeOC) external(id, parent string, running bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions[id] = &fakeSession{id: id, parent: parent, dir: "/elsewhere", delivered: map[string]int64{}}
	f.active[id] = running
	if !running {
		delete(f.active, id)
	}
}

// deliver moves queued messages into the transcript and starts the run.
func (f *fakeOC) deliver(sid string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.sessions[sid]
	for _, id := range s.inbox {
		s.delivered[id] = f.tick()
	}
	s.inbox = nil
	f.active[sid] = true
}

// settle ends the run with a durable outcome.
func (f *fakeOC) settle(sid, outcome string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.sessions[sid]
	delete(f.active, sid)
	s.outcome, s.idle = outcome, f.tick()
}

func (f *fakeOC) set(fn func(f *fakeOC)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeOC) promptBodies() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.prompts)
}

func (f *fakeOC) createCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.creates)
}

type env struct {
	ctx    context.Context
	st     *store.Store
	oc     *fakeOC
	client *opencode.Client
	url    string
	repo   string
	tasks  []string
}

func setup(t *testing.T, tasks, maxConcurrency int) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "gilir.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	repoDir := filepath.Join(t.TempDir(), "repo")
	if out, err := exec.Command("git", "init", "-q", repoDir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	project, err := st.CreateProject(ctx, "alice", "p")
	if err != nil {
		t.Fatal(err)
	}
	repo, err := st.AddRepo(ctx, "alice", project.ID, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{ctx: ctx, st: st, oc: newFakeOC(), repo: repo.Path}
	for i := range tasks {
		task, err := st.CreateTask(ctx, "alice", store.CreateTaskParams{
			ProjectID: project.ID, RepoID: repo.ID, Title: fmt.Sprintf("secret title %d", i),
			Description: "secret description", Status: store.StatusTodo, Priority: store.PriorityMedium,
		})
		if err != nil {
			t.Fatal(err)
		}
		e.tasks = append(e.tasks, task.ID)
	}
	if _, err := st.UpdateQueueSettings(ctx, "alice", store.QueueSettingsPatch{MaxConcurrency: &maxConcurrency}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(e.oc)
	t.Cleanup(srv.Close)
	e.url = srv.URL
	if e.client, err = opencode.New(srv.URL, "pw"); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) watcher() *Watcher { return New(e.st, e.client, e.url) }

func (e *env) tick(t *testing.T, w *Watcher) {
	t.Helper()
	if err := w.Tick(e.ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}
}

func (e *env) task(t *testing.T, i int) store.TaskDetail {
	t.Helper()
	d, err := e.st.Show(e.ctx, e.tasks[i])
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func (e *env) status(t *testing.T, i int) store.Status { t.Helper(); return e.task(t, i).Task.Status }

// sessionOf returns the deterministic session ID linked to task i.
func (e *env) sessionOf(t *testing.T, i int) string {
	t.Helper()
	d := e.task(t, i)
	if len(d.Sessions) != 1 {
		t.Fatalf("task %s sessions = %+v, want one", e.tasks[i], d.Sessions)
	}
	return d.Sessions[0].ID
}

func (e *env) setPaused(t *testing.T, paused bool) {
	t.Helper()
	if _, err := e.st.UpdateQueueSettings(e.ctx, "alice", store.QueueSettingsPatch{Paused: &paused}); err != nil {
		t.Fatal(err)
	}
}

func wantActive(t *testing.T, w *Watcher, want int) {
	t.Helper()
	s := w.Snapshot()
	if s.Error != "" || s.ActiveParents == nil || *s.ActiveParents != want {
		t.Fatalf("snapshot = %+v (active %v), want %d running parents", s, s.ActiveParents, want)
	}
}

func TestCapCountsExternalParentsIgnoresChildrenAndLinksBeforePrompt(t *testing.T) {
	e := setup(t, 2, 1)
	w := e.watcher()
	e.oc.external("ses_ext", "", true)

	e.oc.set(func(f *fakeOC) { f.badState = true })
	if err := w.Tick(e.ctx); err == nil {
		t.Fatal("Tick with unknown active state succeeded, want fail closed")
	}
	if s := w.Snapshot(); s.ActiveParents != nil || s.Error == "" {
		t.Fatalf("snapshot after unknown state = %+v, want nil count and error", s)
	}
	e.oc.set(func(f *fakeOC) { f.badState = false })

	e.tick(t, w)
	wantActive(t, w, 1)
	if got := e.status(t, 0); got != store.StatusTodo {
		t.Fatalf("task dispatched past a full cap: status %s", got)
	}

	// Only a child of the external parent keeps running; it does not count.
	e.oc.external("ses_ext", "", false)
	e.oc.external("ses_ext_child", "ses_ext", true)
	var linkedAtPrompt []store.Session
	e.oc.onPrompt = func(string) { linkedAtPrompt = e.task(t, 0).Sessions }
	e.tick(t, w)

	sid := e.sessionOf(t, 0)
	prompts := e.oc.promptBodies()
	if len(prompts) != 1 {
		t.Fatalf("prompts = %v, want one", prompts)
	}
	attemptID := strings.TrimPrefix(sid, "ses_gilir_")
	if want := map[string]any{"id": "msg_gilir_" + attemptID, "text": fmt.Sprintf("Read task %s with `gilir show %s --json`.", e.tasks[0], e.tasks[0])}; fmt.Sprint(prompts[0]) != fmt.Sprint(want) {
		t.Fatalf("prompt = %v, want %v", prompts[0], want)
	}
	var create map[string]any
	e.oc.set(func(f *fakeOC) { create = f.creates[0] })
	if create["title"] != fmt.Sprintf("Gilir task %s", e.tasks[0]) || create["location"].(map[string]any)["directory"] != e.repo {
		t.Fatalf("create = %v", create)
	}
	if len(linkedAtPrompt) != 1 || linkedAtPrompt[0].ID != sid || linkedAtPrompt[0].URL != e.client.Link(sid) {
		t.Fatalf("sessions linked when prompt arrived = %+v, want %s at %s", linkedAtPrompt, sid, e.client.Link(sid))
	}

	// The bootstrap is queued but not running: it still fills the cap.
	e.tick(t, w)
	wantActive(t, w, 0)
	if got := e.status(t, 1); got != store.StatusTodo {
		t.Fatalf("second task dispatched while bootstrap queued: status %s", got)
	}
	if n := len(e.oc.promptBodies()); n != 1 {
		t.Fatalf("prompts = %d, want 1", n)
	}
}

func TestRecoveryWaitsForCapacityBehindRunningAndQueuedWork(t *testing.T) {
	e := setup(t, 2, 2)
	w := e.watcher()
	e.tick(t, w) // task 0 bootstrapped; its prompt stays queued

	// Simulate a crash right after reserving task 1, then an external run.
	stranded, err := e.st.Reserve(e.ctx, e.url, 100)
	if err != nil || stranded == nil || stranded.TaskID != e.tasks[1] {
		t.Fatalf("Reserve = %+v, %v", stranded, err)
	}
	e.oc.external("ses_ext", "", true)

	w = e.watcher()
	e.tick(t, w)
	wantActive(t, w, 1)
	if n := e.oc.createCount(); n != 1 {
		t.Fatalf("creates = %d, want stranded attempt held back by full cap", n)
	}

	e.oc.external("ses_ext", "", false)
	e.tick(t, w)
	if sid := e.sessionOf(t, 1); sid != SessionID(stranded.ID) {
		t.Fatalf("recovered session = %s, want %s", sid, SessionID(stranded.ID))
	}
	if n := len(e.oc.promptBodies()); n != 2 {
		t.Fatalf("prompts = %d, want 2", n)
	}
}

func TestRecreatedWatcherRestoresRunAndSettlesWhilePaused(t *testing.T) {
	e := setup(t, 2, 1)
	e.tick(t, e.watcher())
	sid := e.sessionOf(t, 0)
	e.oc.deliver(sid)

	w := e.watcher()
	e.setPaused(t, true)
	e.tick(t, w)
	wantActive(t, w, 1)
	if got := e.status(t, 0); got != store.StatusInProgress {
		t.Fatalf("running task status = %s", got)
	}

	e.oc.settle(sid, "succeeded")
	e.tick(t, w)
	wantActive(t, w, 0)
	if got, next := e.status(t, 0), e.status(t, 1); got != store.StatusInReview || next != store.StatusTodo {
		t.Fatalf("after success while paused: task 0 %s, task 1 %s; want in-review, todo", got, next)
	}

	e.setPaused(t, false)
	e.tick(t, w)
	if got := e.status(t, 1); got != store.StatusInProgress {
		t.Fatalf("freed slot not reused: task 1 %s", got)
	}
}

func TestTerminalFailuresMarkErrorAndFreeSlot(t *testing.T) {
	for _, tc := range []struct {
		name  string
		after func(f *fakeOC, sid string)
	}{
		{"failed after delivery", func(f *fakeOC, sid string) { f.deliver(sid); f.settle(sid, "failed") }},
		{"interrupted before delivery", func(f *fakeOC, sid string) {
			f.set(func(f *fakeOC) { f.sessions[sid].inbox = nil })
			f.settle(sid, "interrupted")
		}},
		{"linked session missing", func(f *fakeOC, sid string) { f.set(func(f *fakeOC) { delete(f.sessions, sid) }) }},
		{"server restarted with prompt queued", func(f *fakeOC, sid string) { f.set(func(f *fakeOC) { f.pid = 200 }) }},
		{"prompt rejected", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := setup(t, 2, 1)
			w := e.watcher()
			if tc.after == nil {
				e.oc.set(func(f *fakeOC) { f.promptFault = "400" })
			}
			e.tick(t, w)
			if tc.after != nil {
				tc.after(e.oc, e.sessionOf(t, 0))
				e.tick(t, w)
			}
			d := e.task(t, 0)
			if d.Task.Status != store.StatusInProgress || !slices.Contains(d.Task.Markings, store.MarkingError) {
				t.Fatalf("task 0 = %s %v, want in-progress with error", d.Task.Status, d.Task.Markings)
			}
			e.tick(t, w)
			if got := e.status(t, 1); got != store.StatusInProgress {
				t.Fatalf("slot not freed: task 1 %s", got)
			}
		})
	}
}

func TestManualStatusChangeBeforePromptAbandons(t *testing.T) {
	e := setup(t, 1, 1)
	e.oc.onCreate = func(string) {
		backlog := store.StatusBacklog
		if _, err := e.st.UpdateTask(e.ctx, "alice", e.tasks[0], store.TaskPatch{Status: &backlog}); err != nil {
			t.Error(err)
		}
	}
	e.tick(t, e.watcher())
	if n := len(e.oc.promptBodies()); n != 0 {
		t.Fatalf("prompts = %d, want none for a task moved away", n)
	}
	d := e.task(t, 0)
	if d.Task.Status != store.StatusBacklog || slices.Contains(d.Task.Markings, store.MarkingError) {
		t.Fatalf("task = %s %v, want untouched backlog", d.Task.Status, d.Task.Markings)
	}
	if open, err := e.st.ListOpenAttempts(e.ctx); err != nil || len(open) != 0 {
		t.Fatalf("open attempts = %v, %v; want abandoned", open, err)
	}
}

func TestUncertainPromptRetryReusesMessageID(t *testing.T) {
	for _, tc := range []struct {
		name, fault string
		otherRun    bool // an unrelated run settles before the retry
		wantPrompts int
	}{
		{"accepted then hung up", "drop", false, 1}, // found in the inbox, not resent
		{"refused", "503", false, 2},                // resent with the same ID
		// A succeeded session whose bootstrap was never delivered is not
		// a task success.
		{"refused then other run succeeded", "503", true, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := setup(t, 2, 1)
			w := e.watcher()
			e.oc.set(func(f *fakeOC) { f.promptFault = tc.fault })
			if err := w.Tick(e.ctx); err == nil {
				t.Fatal("Tick after failed prompt succeeded, want error")
			}
			if tc.otherRun {
				e.oc.settle(e.sessionOf(t, 0), "succeeded")
			}
			e.tick(t, w)
			e.tick(t, w)

			prompts := e.oc.promptBodies()
			if len(prompts) != tc.wantPrompts || prompts[len(prompts)-1]["id"] != prompts[0]["id"] {
				t.Fatalf("prompts = %v, want %d with one ID", prompts, tc.wantPrompts)
			}
			sid := e.sessionOf(t, 0)
			e.oc.set(func(f *fakeOC) {
				if got := f.sessions[sid].inbox; len(got) != 1 {
					t.Errorf("inbox = %v, want exactly one message", got)
				}
			})
			if first, second := e.status(t, 0), e.status(t, 1); first != store.StatusInProgress || second != store.StatusTodo {
				t.Fatalf("tasks = %s, %s; want in-progress, todo", first, second)
			}
		})
	}
}

func TestServerRestartSettlesUnpromptedReservationWhilePaused(t *testing.T) {
	for _, tc := range []struct {
		name    string
		moved   bool
		want    store.Status
		wantErr bool
	}{
		{"still in progress", false, store.StatusInProgress, true},
		{"moved before prompt", true, store.StatusBacklog, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := setup(t, 1, 1)
			if a, err := e.st.Reserve(e.ctx, e.url, 99); err != nil || a == nil {
				t.Fatalf("Reserve = %+v, %v", a, err)
			}
			if tc.moved {
				backlog := store.StatusBacklog
				if _, err := e.st.UpdateTask(e.ctx, "alice", e.tasks[0], store.TaskPatch{Status: &backlog}); err != nil {
					t.Fatal(err)
				}
			}
			e.setPaused(t, true)
			e.tick(t, e.watcher()) // the fake server reports pid 100

			if open, err := e.st.ListOpenAttempts(e.ctx); err != nil || len(open) != 0 {
				t.Fatalf("open attempts = %v, %v; want settled while paused", open, err)
			}
			d := e.task(t, 0)
			if d.Task.Status != tc.want || slices.Contains(d.Task.Markings, store.MarkingError) != tc.wantErr {
				t.Fatalf("task = %s %v, want %s with error=%v", d.Task.Status, d.Task.Markings, tc.want, tc.wantErr)
			}
			if n := e.oc.createCount(); n != 0 {
				t.Fatalf("creates = %d, want none", n)
			}
		})
	}
}

func TestUncertainCandidateRetriesBehindCapacityBlockedOlderOne(t *testing.T) {
	e := setup(t, 2, 2)
	w := e.watcher()
	e.tick(t, w) // task 0 prompted and queued
	e.oc.set(func(f *fakeOC) { f.promptFault = "503" })
	if err := w.Tick(e.ctx); err == nil {
		t.Fatal("Tick with refused prompt succeeded, want error")
	}
	older, newer := e.sessionOf(t, 0), e.sessionOf(t, 1)

	// The server drops task 0's queued prompt, so the older attempt is
	// unprompted again, and an external run takes the remaining slot.
	e.oc.set(func(f *fakeOC) { f.sessions[older].inbox = nil })
	e.oc.external("ses_ext", "", true)
	e.tick(t, w)

	var olderPrompts, newerPrompts []any
	for _, p := range e.oc.promptBodies() {
		switch p["id"] {
		case MessageID(strings.TrimPrefix(older, "ses_gilir_")):
			olderPrompts = append(olderPrompts, p)
		case MessageID(strings.TrimPrefix(newer, "ses_gilir_")):
			newerPrompts = append(newerPrompts, p)
		}
	}
	if len(olderPrompts) != 1 || len(newerPrompts) != 2 {
		t.Fatalf("prompts older=%d newer=%d, want 1 and a same-ID retry (2)", len(olderPrompts), len(newerPrompts))
	}
}
