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

// Snapshot is the watcher's latest view for the daemon. ActiveParents is the
// number of root sessions observed running on the server, including ones Gilir
// did not start; it is nil when the last tick could not establish it. Error is
// the last tick's error.
type Snapshot struct {
	ActiveParents *int
	Error         string
}

// Watcher schedules queued tasks onto one OpenCode server. Run one instance
// per database; cross-process exclusion is the caller's job.
type Watcher struct {
	store     *store.Store
	oc        *opencode.Client
	serverURL string

	tickMu sync.Mutex
	// uncertain holds attempts whose prompt submission had no definitive
	// answer and has not been observed since. They keep their slot.
	uncertain map[string]bool

	mu   sync.Mutex
	snap Snapshot
}

// New returns a watcher that dispatches to the server at serverURL through oc.
func New(st *store.Store, oc *opencode.Client, serverURL string) *Watcher {
	return &Watcher{
		store: st, oc: oc, serverURL: serverURL,
		uncertain: map[string]bool{},
		snap:      Snapshot{Error: "not yet polled"},
	}
}

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

// Tick runs one reconcile-and-dispatch pass and records its Snapshot.
func (w *Watcher) Tick(ctx context.Context) error {
	w.tickMu.Lock()
	defer w.tickMu.Unlock()
	running, err := w.tick(ctx)
	w.mu.Lock()
	defer w.mu.Unlock()
	if err != nil {
		w.snap = Snapshot{Error: err.Error()}
		return err
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
	pid, err := w.oc.ServerPID(ctx)
	if err != nil {
		return 0, err
	}
	active, err := w.oc.ActiveParents(ctx)
	if err != nil {
		return 0, err
	}
	attempts, err := w.store.ListOpenAttempts(ctx)
	if err != nil {
		return 0, err
	}
	for _, a := range attempts {
		if a.ServerURL != w.serverURL {
			return 0, fmt.Errorf("watcher: attempt %s for task %d belongs to OpenCode server %s, not %s", a.ID, a.TaskID, a.ServerURL, w.serverURL)
		}
	}

	// Pass 1: observe every attempt and settle outcomes. Nothing is sent.
	occupied := len(active)
	var candidates []store.DispatchAttempt
	for _, a := range attempts {
		st, err := w.observe(ctx, a, active, pid)
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
	for _, a := range candidates {
		others := occupied
		if w.uncertain[a.ID] {
			others-- // its own slot is already counted
		}
		if others >= settings.MaxConcurrency {
			return len(active), nil
		}
		done, err := w.bootstrap(ctx, a, pid, others)
		if err != nil || !done {
			return len(active), err
		}
		occupied = others
	}
	if occupied >= settings.MaxConcurrency {
		return len(active), nil
	}
	a, err := w.store.Reserve(ctx, w.serverURL, pid)
	if err != nil || a == nil {
		return len(active), err
	}
	_, err = w.bootstrap(ctx, *a, pid, occupied)
	return len(active), err
}

// SessionID is the deterministic OpenCode session ID for an attempt.
func SessionID(attemptID string) string { return "ses_gilir_" + attemptID }

// MessageID is the deterministic bootstrap prompt ID for an attempt.
func MessageID(attemptID string) string { return "msg_gilir_" + attemptID }

// Prompt is the exact bootstrap text for a task. It carries no task content.
func Prompt(taskID int64) string {
	return fmt.Sprintf("Read task %d with gilir show %d --json.", taskID, taskID)
}

// observe classifies one open attempt and terminalizes it when OpenCode shows
// a durable outcome. Any read it cannot interpret is returned as an error.
func (w *Watcher) observe(ctx context.Context, a store.DispatchAttempt, active map[string]struct{}, pid int64) (state, error) {
	if a.SessionID == nil {
		// Bootstrap order is create, link, prompt: unlinked means unprompted.
		return unprompted, nil
	}
	sid, mid := SessionID(a.ID), MessageID(a.ID)
	if *a.SessionID != sid {
		return 0, fmt.Errorf("watcher: attempt %s is linked to unexpected session %s", a.ID, *a.SessionID)
	}

	s, err := w.oc.Get(ctx, sid)
	if opencode.IsNotFound(err) {
		return finished, w.finish(ctx, a, store.OutcomeFailure, "OpenCode session "+sid+" no longer exists")
	}
	if err != nil {
		return 0, err
	}
	inbox, err := w.oc.Inbox(ctx, sid)
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
		deliveredAt, err = w.oc.MessageCreatedAt(ctx, sid, mid)
		switch {
		case err == nil:
			delivered = true
		case !opencode.IsNotFound(err):
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
	return unprompted, nil
}

// bootstrap creates, links and prompts an attempt with no accepted prompt,
// given the slots occupied by everything else. It is idempotent because the
// session and message IDs are deterministic. done reports that the attempt
// was resolved without sending, so the caller may move on to the next one.
func (w *Watcher) bootstrap(ctx context.Context, a store.DispatchAttempt, pid int64, others int) (done bool, err error) {
	sid, mid := SessionID(a.ID), MessageID(a.ID)
	if proceed, done, err := w.ready(ctx, a, others); !proceed {
		return done, err
	}
	if a.ServerPID != pid {
		return true, w.finish(ctx, a, store.OutcomeFailure,
			fmt.Sprintf("OpenCode server restarted (pid %d -> %d) before the task prompt was sent; move the task to todo to retry", a.ServerPID, pid))
	}
	if _, err := w.oc.Create(ctx, sid, fmt.Sprintf("Gilir task %d", a.TaskID), a.RepoPath); err != nil {
		return w.rejected(ctx, a, "create session", err)
	}
	if err := w.store.LinkSession(ctx, a.ID, sid, w.oc.Link(sid)); err != nil {
		return false, err
	}
	if proceed, done, err := w.ready(ctx, a, others); !proceed {
		return done, err
	}
	if err := w.oc.Prompt(ctx, sid, mid, Prompt(a.TaskID)); err != nil {
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
	d, err := w.store.Show(ctx, a.TaskID)
	if err != nil {
		return false, false, err
	}
	if d.Task.Status != store.StatusInProgress {
		return false, true, w.finish(ctx, a, store.OutcomeAbandoned,
			fmt.Sprintf("task moved to %s before the prompt was sent", d.Task.Status))
	}
	settings, err := w.store.QueueSettings(ctx)
	if err != nil {
		return false, false, err
	}
	return !settings.Paused && others < settings.MaxConcurrency, false, nil
}

// rejected fails the attempt when OpenCode definitively refused a request
// with a 4xx response; anything else is uncertain and left pending.
func (w *Watcher) rejected(ctx context.Context, a store.DispatchAttempt, op string, err error) (done bool, _ error) {
	var se *opencode.StatusError
	if errors.As(err, &se) && se.StatusCode >= http.StatusBadRequest && se.StatusCode < http.StatusInternalServerError {
		return true, w.finish(ctx, a, store.OutcomeFailure, "OpenCode rejected "+op+": "+se.Error())
	}
	return false, err
}

func (w *Watcher) finish(ctx context.Context, a store.DispatchAttempt, outcome store.AttemptOutcome, detail string) error {
	delete(w.uncertain, a.ID)
	return w.store.FinishAttempt(ctx, a.ID, outcome, detail)
}
