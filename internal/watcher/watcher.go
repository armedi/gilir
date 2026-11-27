// Package watcher is Gilir's passive queue scheduler. Each tick it observes
// one OpenCode server, reconciles open dispatch attempts against it, and then
// bootstraps at most one task with a short pointer prompt when capacity
// allows. It never steers or interrupts a session, and it fails closed: when
// OpenCode state cannot be read or understood, it dispatches nothing.
package watcher

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/armedi/gilir/internal/opencode"
	"github.com/armedi/gilir/internal/store"
)

// Interval is the pause between ticks in Run.
const Interval = time.Second

// Connection resolves the OpenCode client for one tick. Failed is called with
// a tick error: it reports that credentials or the endpoint were refreshed, so
// the tick should be retried immediately. Redact removes secrets from text.
// *opencode.Connector implements it.
type Connection interface {
	Client(ctx context.Context) (*opencode.Client, string, error)
	Failed(err error) bool
	Redact(text string) string
}

// Snapshot is the watcher's latest view for the daemon. ActiveParents is the
// number of root sessions observed running on the server, including ones Gilir
// did not start; it is nil when the last tick could not establish it. Error is
// the last tick's error, already redacted and phrased for humans.
type Snapshot struct {
	ActiveParents *int
	Error         string
}

// Watcher schedules queued tasks onto one OpenCode server. Run one instance
// per database; cross-process exclusion is the caller's job.
type Watcher struct {
	store *store.Store
	conn  Connection

	tickMu sync.Mutex
	// uncertain holds attempts whose prompt submission had no definitive
	// answer and has not been observed since. They keep their slot.
	uncertain map[string]bool

	mu   sync.Mutex
	snap Snapshot
}

// New returns a watcher that always dispatches to the server at serverURL
// through oc. It is the fixed-endpoint form of NewWithConnection.
func New(st *store.Store, oc *opencode.Client, serverURL string) *Watcher {
	return NewWithConnection(st, fixedConnection{oc: oc, serverURL: serverURL})
}

// NewWithConnection returns a watcher that resolves its client every tick,
// so it can follow a managed OpenCode service that stops, starts or rotates
// its password.
func NewWithConnection(st *store.Store, conn Connection) *Watcher {
	return &Watcher{
		store:     st,
		conn:      conn,
		uncertain: map[string]bool{},
		snap:      Snapshot{Error: "not yet polled"},
	}
}

type fixedConnection struct {
	oc        *opencode.Client
	serverURL string
}

func (f fixedConnection) Client(context.Context) (*opencode.Client, string, error) {
	return f.oc, f.serverURL, nil
}

func (f fixedConnection) Failed(error) bool      { return false }
func (f fixedConnection) Redact(s string) string { return s }

// Snapshot returns the result of the most recent tick.
func (w *Watcher) Snapshot() Snapshot {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.snap
	if s.ActiveParents != nil {
		n := *s.ActiveParents
		s.ActiveParents = &n
	}
	return s
}

// Run ticks immediately and then every Interval until ctx is done. Tick
// errors are reported through Snapshot.
func (w *Watcher) Run(ctx context.Context) {
	t := time.NewTicker(Interval)
	defer t.Stop()
	for {
		_ = w.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick runs one reconcile-and-dispatch pass and records its Snapshot. When a
// tick fails because OpenCode credentials or the endpoint changed, discovery
// is refreshed and the tick is retried once with the new client.
func (w *Watcher) Tick(ctx context.Context) error {
	w.tickMu.Lock()
	defer w.tickMu.Unlock()
	running, err := w.tick(ctx)
	if err != nil && w.conn.Failed(err) {
		running, err = w.tick(ctx)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err != nil {
		message := w.conn.Redact(opencode.Describe(err))
		w.snap = Snapshot{Error: message}
		return errors.New(message)
	}
	w.snap = Snapshot{ActiveParents: &running}
	return nil
}

type state int

const (
	finished   state = iota // attempt resolved this tick
	running                 // counted by ActiveParents
	pending                 // prompt accepted, not running or settled
	unprompted              // no prompt accepted; a bootstrap candidate
)

func (w *Watcher) tick(ctx context.Context) (int, error) {
	oc, serverURL, err := w.conn.Client(ctx)
	if err != nil {
		return 0, err
	}
	pid, err := oc.ServerPID(ctx)
	if err != nil {
		return 0, err
	}
	active, err := oc.ActiveParents(ctx)
	if err != nil {
		return 0, err
	}
	attempts, err := w.store.ListOpenAttempts(ctx)
	if err != nil {
		return 0, err
	}
	for _, a := range attempts {
		if a.ServerURL != serverURL {
			return 0, fmt.Errorf("watcher: attempt %s for task %s belongs to OpenCode server %s, not %s", a.ID, a.TaskID, a.ServerURL, serverURL)
		}
	}

	// Pass 1: observe every attempt and settle outcomes. Nothing is sent.
	occupied := len(active)
	var candidates []store.DispatchAttempt
	for _, a := range attempts {
		st, err := w.observe(ctx, oc, a, active, pid)
		if err != nil {
			return 0, err
		}
		if st == pending || st == unprompted && w.uncertain[a.ID] {
			occupied++
		}
		if st == unprompted {
			candidates = append(candidates, a)
		}
	}

	// Pass 2: with every slot counted, bootstrap the oldest candidate, or
	// reserve a fresh task when no candidate remains.
	settings, err := w.store.QueueSettings(ctx)
	if err != nil || settings.Paused {
		return len(active), err
	}
	// A candidate blocked by capacity is skipped, so a later uncertain one
	// that already owns its slot can still retry its message ID.
	blocked := false
	for _, a := range candidates {
		others := occupied
		if w.uncertain[a.ID] {
			others-- // its own slot is already counted
		}
		if others >= settings.MaxConcurrency {
			blocked = true
			continue
		}
		done, err := w.bootstrap(ctx, oc, a, others)
		if err != nil || !done {
			return len(active), err
		}
		occupied = others
	}
	if blocked || occupied >= settings.MaxConcurrency {
		return len(active), nil
	}
	a, err := w.store.Reserve(ctx, serverURL, pid)
	if err != nil || a == nil {
		return len(active), err
	}
	_, err = w.bootstrap(ctx, oc, *a, occupied)
	return len(active), err
}

// SessionID is the deterministic OpenCode session ID for an attempt.
func SessionID(attemptID string) string { return "ses_gilir_" + attemptID }

// MessageID is the deterministic bootstrap prompt ID for an attempt.
func MessageID(attemptID string) string { return "msg_gilir_" + attemptID }

// Prompt is the exact bootstrap text for a task. It carries no task content,
// only the self-describing task ID so the agent knows where the task lives.
func Prompt(taskID string) string {
	return fmt.Sprintf("Read task %s with `gilir show %s --json`.", taskID, taskID)
}

// observe classifies one open attempt and terminalizes it when OpenCode shows
// a durable outcome. Any read it cannot interpret is returned as an error.
func (w *Watcher) observe(ctx context.Context, oc *opencode.Client, a store.DispatchAttempt, active map[string]struct{}, pid int64) (state, error) {
	if a.SessionID == nil {
		// Bootstrap order is create, link, prompt: unlinked means unprompted.
		return w.unprompted(ctx, a, pid)
	}
	sid, mid := SessionID(a.ID), MessageID(a.ID)
	if *a.SessionID != sid {
		return 0, fmt.Errorf("watcher: attempt %s is linked to unexpected session %s", a.ID, *a.SessionID)
	}

	missing := func() (state, error) {
		return finished, w.finish(ctx, a, store.OutcomeFailure, "OpenCode session "+sid+" no longer exists")
	}
	s, err := oc.Get(ctx, sid)
	if opencode.IsSessionNotFound(err) {
		return missing()
	}
	if err != nil {
		return 0, err
	}
	inbox, err := oc.Inbox(ctx, sid)
	if opencode.IsSessionNotFound(err) {
		return missing()
	}
	if err != nil {
		return 0, err
	}
	queued := false
	for _, id := range inbox {
		queued = queued || id == mid
	}
	var deliveredAt int64
	delivered := false
	if !queued {
		deliveredAt, err = oc.MessageCreatedAt(ctx, sid, mid)
		switch {
		case err == nil:
			delivered = true
		case opencode.IsSessionNotFound(err):
			return missing()
		case !opencode.IsMessageNotFound(err):
			return 0, err
		}
	}
	accepted := queued || delivered
	if accepted {
		delete(w.uncertain, a.ID)
	}
	if _, ok := active[sid]; ok {
		return running, nil
	}

	switch {
	case delivered && s.Outcome != "" && s.IdleAt >= deliveredAt && len(inbox) == 0:
		if s.Outcome == "succeeded" {
			return finished, w.finish(ctx, a, store.OutcomeSuccess, "OpenCode session succeeded")
		}
		return finished, w.finish(ctx, a, store.OutcomeFailure, "OpenCode session "+s.Outcome)
	case !delivered && (s.Outcome == "failed" || s.Outcome == "interrupted"):
		// A run that ended before the bootstrap was delivered can only fail.
		return finished, w.finish(ctx, a, store.OutcomeFailure,
			"OpenCode session "+s.Outcome+" before the task prompt was delivered")
	case accepted && a.ServerPID != pid:
		return finished, w.finish(ctx, a, store.OutcomeFailure,
			fmt.Sprintf("OpenCode server restarted (pid %d -> %d) and the session is not running", a.ServerPID, pid))
	case accepted:
		return pending, nil
	}
	return w.unprompted(ctx, a, pid)
}

// unprompted keeps an attempt with no accepted prompt as a candidate, unless
// it was reserved against an earlier server process. That is settled now,
// even while paused or full: abandoned if the task was moved away, otherwise
// a visible failure the user can retry by moving the task back to todo.
func (w *Watcher) unprompted(ctx context.Context, a store.DispatchAttempt, pid int64) (state, error) {
	if a.ServerPID == pid {
		return unprompted, nil
	}
	if stale, err := w.abandonIfMoved(ctx, a); stale || err != nil {
		return finished, err
	}
	return finished, w.finish(ctx, a, store.OutcomeFailure,
		fmt.Sprintf("OpenCode server restarted (pid %d -> %d) before the task prompt was sent; move the task to todo to retry", a.ServerPID, pid))
}

// bootstrap creates, links and prompts an attempt with no accepted prompt,
// given the slots occupied by everything else. It is idempotent because the
// session and message IDs are deterministic. done reports that the attempt
// was resolved without sending, so the caller may move on to the next one.
func (w *Watcher) bootstrap(ctx context.Context, oc *opencode.Client, a store.DispatchAttempt, others int) (done bool, err error) {
	sid, mid := SessionID(a.ID), MessageID(a.ID)
	if proceed, done, err := w.ready(ctx, a, others); !proceed {
		return done, err
	}
	if _, err := oc.Create(ctx, sid, fmt.Sprintf("Gilir task %s", a.TaskID), a.RepoPath); err != nil {
		return w.rejected(ctx, a, "create session", err)
	}
	if err := w.store.LinkSession(ctx, a.ID, sid, oc.Link(sid)); err != nil {
		return false, err
	}
	if proceed, done, err := w.ready(ctx, a, others); !proceed {
		return done, err
	}
	if err := oc.Prompt(ctx, sid, mid, Prompt(a.TaskID)); err != nil {
		done, err := w.rejected(ctx, a, "send prompt", err)
		if !done {
			// No definitive answer: hold the slot until the same message ID
			// is observed or resent.
			w.uncertain[a.ID] = true
		}
		return done, err
	}
	delete(w.uncertain, a.ID)
	return false, nil
}

// ready rereads state just before unprompted work. A task moved away from
// in-progress is abandoned; a paused or full queue holds without progress.
func (w *Watcher) ready(ctx context.Context, a store.DispatchAttempt, others int) (proceed, done bool, err error) {
	if stale, err := w.abandonIfMoved(ctx, a); stale || err != nil {
		return false, stale, err
	}
	settings, err := w.store.QueueSettings(ctx)
	if err != nil {
		return false, false, err
	}
	return !settings.Paused && others < settings.MaxConcurrency, false, nil
}

// abandonIfMoved finishes an unprompted attempt as abandoned when its task
// is no longer in progress.
func (w *Watcher) abandonIfMoved(ctx context.Context, a store.DispatchAttempt) (bool, error) {
	d, err := w.store.Show(ctx, a.TaskID)
	if err != nil || d.Task.Status == store.StatusInProgress {
		return false, err
	}
	return true, w.finish(ctx, a, store.OutcomeAbandoned,
		fmt.Sprintf("task moved to %s before the prompt was sent", d.Task.Status))
}

// rejected fails the attempt when OpenCode definitively refused a request
// with a 4xx response. Timeouts stay uncertain and auth or rate-limit
// refusals fail closed; anything else is left pending.
func (w *Watcher) rejected(ctx context.Context, a store.DispatchAttempt, op string, err error) (done bool, _ error) {
	var se *opencode.StatusError
	if errors.As(err, &se) && definitive(se.StatusCode) {
		return true, w.finish(ctx, a, store.OutcomeFailure, "OpenCode rejected "+op+": "+se.Error())
	}
	return false, err
}

func definitive(code int) bool {
	switch code {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusRequestTimeout, http.StatusTooManyRequests:
		return false
	}
	return code >= http.StatusBadRequest && code < http.StatusInternalServerError
}

func (w *Watcher) finish(ctx context.Context, a store.DispatchAttempt, outcome store.AttemptOutcome, detail string) error {
	delete(w.uncertain, a.ID)
	return w.store.FinishAttempt(ctx, a.ID, outcome, detail)
}
