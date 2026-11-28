package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// Event is one row of the audit activity feed. Payload is the serialized
// activity row and is delivered to hooks unchanged.
type Event struct {
	ID      int64
	Type    string
	Actor   string
	Payload json.RawMessage
}

// Source is the audit event store and per-hook cursor persistence.
type Source interface {
	// After returns up to limit events with ID greater than afterID in
	// ascending ID order.
	After(ctx context.Context, afterID int64, limit int) ([]Event, error)
	// Cursor returns the last consumed event ID for the named hook and
	// whether a cursor has been stored for it.
	Cursor(ctx context.Context, name string) (lastID int64, initialized bool, err error)
	// SetCursor persists the last consumed event ID for the named hook.
	SetCursor(ctx context.Context, name string, lastID int64) error
	// Latest returns the newest event ID, or 0 when there are no events.
	Latest(ctx context.Context) (int64, error)
}

const (
	batchSize       = 100
	pollInterval    = 500 * time.Millisecond
	retryBase       = 500 * time.Millisecond
	retryMax        = 30 * time.Second
	deliveryTimeout = 30 * time.Second
)

// defaultEvents are delivered when a hook has no events filter.
var defaultEvents = []string{
	"task.created",
	"task.updated",
	"task.deleted",
	"task.status_changed",
	"task.marking_changed",
	"task.commented",
	"task.claimed",
	"dependency.added",
	"dependency.removed",
	"session.linked",
	"session.outcome",
}

// Run starts one worker per hook and blocks until ctx is cancelled and every
// worker has stopped. Errors are passed to report, which may be nil; calls to
// report are serialized. Invalid configs are reported and nothing runs.
//
// Run does not configure GILIR_URL for command hooks; any inherited value is
// filtered out. The daemon uses RunWithURL so hooks invoke the daemon that
// delivered the event.
func Run(ctx context.Context, configs []Config, source Source, report func(error)) {
	run(ctx, configs, source, "", report)
}

// RunWithURL is Run with the daemon's own HTTP origin, for example
// http://127.0.0.1:44547. Command hooks receive it as GILIR_URL, replacing any
// inherited value, so a hook that invokes gilir reaches the daemon that
// delivered the event even when it listens on a custom port.
func RunWithURL(ctx context.Context, configs []Config, source Source, daemonURL string, report func(error)) {
	run(ctx, configs, source, daemonURL, report)
}

func run(ctx context.Context, configs []Config, source Source, daemonURL string, report func(error)) {
	var mu sync.Mutex
	reportErr := func(err error) {
		if report == nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		report(err)
	}
	if err := validate(configs); err != nil {
		reportErr(err)
		return
	}
	var wg sync.WaitGroup
	for _, config := range configs {
		w := &worker{config: config, source: source, report: reportErr, events: eventSet(config.Events), daemonURL: daemonURL}
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.run(ctx)
		}()
	}
	wg.Wait()
}

func eventSet(events []string) map[string]bool {
	if len(events) == 0 {
		events = defaultEvents
	}
	set := make(map[string]bool, len(events))
	for _, event := range events {
		set[event] = true
	}
	return set
}

type worker struct {
	config    Config
	source    Source
	report    func(error)
	events    map[string]bool
	daemonURL string
}

func (w *worker) run(ctx context.Context) {
	cursor, ok := w.initialCursor(ctx)
	if !ok {
		return
	}
	for ctx.Err() == nil {
		var events []Event
		if !w.retry(ctx, "read events", func(ctx context.Context) (err error) {
			events, err = w.source.After(ctx, cursor, batchSize)
			return err
		}) {
			return
		}
		if len(events) == 0 {
			if !sleep(ctx, pollInterval) {
				return
			}
			continue
		}
		for _, event := range events {
			if event.ID <= cursor {
				w.report(fmt.Errorf("hook %q: ignoring out-of-order event %d after %d", w.config.Name, event.ID, cursor))
				continue
			}
			if w.wants(event) && !w.retry(ctx, fmt.Sprintf("deliver event %d", event.ID), func(ctx context.Context) error {
				return w.deliver(ctx, event)
			}) {
				return
			}
			if !w.retry(ctx, "save cursor", func(ctx context.Context) error {
				return w.source.SetCursor(ctx, w.config.Name, event.ID)
			}) {
				return
			}
			cursor = event.ID
		}
	}
}

// initialCursor loads the stored cursor, starting a newly configured hook at
// the latest event so it does not replay history.
func (w *worker) initialCursor(ctx context.Context) (int64, bool) {
	var cursor int64
	var initialized bool
	if !w.retry(ctx, "load cursor", func(ctx context.Context) (err error) {
		cursor, initialized, err = w.source.Cursor(ctx, w.config.Name)
		return err
	}) {
		return 0, false
	}
	if initialized {
		return cursor, true
	}
	ok := w.retry(ctx, "initialize cursor", func(ctx context.Context) error {
		latest, err := w.source.Latest(ctx)
		if err != nil {
			return err
		}
		if err := w.source.SetCursor(ctx, w.config.Name, latest); err != nil {
			return err
		}
		cursor = latest
		return nil
	})
	return cursor, ok
}

func (w *worker) wants(event Event) bool {
	return event.Actor != "hook:"+w.config.Name && w.events[event.Type]
}

func (w *worker) deliver(ctx context.Context, event Event) error {
	ctx, cancel := context.WithTimeout(ctx, deliveryTimeout)
	defer cancel()
	if w.config.URL != "" {
		return postEvent(ctx, w.config.URL, event)
	}
	return runCommand(ctx, w.config, w.daemonURL, event)
}

// retry runs fn until it succeeds, reporting each failure and backing off up
// to retryMax. It returns false only when ctx is cancelled.
func (w *worker) retry(ctx context.Context, action string, fn func(context.Context) error) bool {
	delay := retryBase
	for {
		err := fn(ctx)
		if err == nil {
			return true
		}
		if ctx.Err() != nil {
			return false
		}
		w.report(fmt.Errorf("hook %q: %s: %w (retrying in %s)", w.config.Name, action, err, delay))
		if !sleep(ctx, delay) {
			return false
		}
		delay = min(delay*2, retryMax)
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
