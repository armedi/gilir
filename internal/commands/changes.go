package commands

import (
	"context"
	"encoding/json"
	"time"
)

// Leave room for the browser's deadline and the HTTP server's write timeout.
const webPollWait = 25 * time.Second

type webRevision struct {
	StartedAt string `json:"started_at"`
	Activity  uint64 `json:"activity"`
	Watcher   uint64 `json:"watcher"`
}

// StopPolling releases outstanding long polls during HTTP shutdown.
func (s *Service) StopPolling() {
	close(s.pollingDone)
}

func (s *Service) waitForChanges(ctx context.Context, args json.RawMessage) (webRevision, error) {
	var in struct {
		After *webRevision `json:"after"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return webRevision{}, err
	}

	var watcher Snapshot
	if s.status != nil {
		watcher = s.status()
	}
	activity, changed := s.store.ActivityChanges()
	current := webRevision{StartedAt: s.startedAt, Activity: activity, Watcher: watcher.Revision}
	if in.After == nil || *in.After != current {
		return current, nil
	}

	timer := time.NewTimer(webPollWait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return webRevision{}, ctx.Err()
	case <-s.pollingDone:
		return current, nil
	case <-timer.C:
	case <-changed:
	case <-watcher.Changed:
	}
	if s.status != nil {
		current.Watcher = s.status().Revision
	}
	current.Activity, _ = s.store.ActivityChanges()
	return current, nil
}
