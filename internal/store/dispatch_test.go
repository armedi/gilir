package store

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestReservePriorityOrderingWithUrgent(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	project, repo := seedProjectRepo(t, s)

	urgent := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: "urgent",
		Status: StatusTodo, Priority: PriorityUrgent,
	})
	highChanges := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: "high changes",
		Status: StatusTodo, Priority: PriorityHigh,
	})
	if _, err := s.SetMarking(ctx, "alice", highChanges.ID, MarkingChangesRequested, true); err != nil {
		t.Fatalf("mark high changes: %v", err)
	}
	mediumChanges := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: "medium changes",
		Status: StatusTodo, Priority: PriorityMedium,
	})
	if _, err := s.SetMarking(ctx, "alice", mediumChanges.ID, MarkingChangesRequested, true); err != nil {
		t.Fatalf("mark medium changes: %v", err)
	}
	medium := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: "medium",
		Status: StatusTodo, Priority: PriorityMedium,
	})
	low := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: "low",
		Status: StatusTodo, Priority: PriorityLow,
	})

	wantOrder := []int64{urgent.ID, highChanges.ID, mediumChanges.ID, medium.ID, low.ID}
	attemptIDs := map[string]bool{}
	for index, wantTaskID := range wantOrder {
		attempt := mustReserve(t, s, "http://127.0.0.1:4096", int64(1000+index))
		if attempt.TaskID != wantTaskID {
			t.Fatalf("reservation %d selected task %d, want %d", index, attempt.TaskID, wantTaskID)
		}
		if attempt.State != AttemptStateReserved {
			t.Fatalf("attempt state = %q, want reserved", attempt.State)
		}
		if attempt.RepoPath != repo.Path {
			t.Fatalf("attempt repo path = %q, want %q", attempt.RepoPath, repo.Path)
		}
		if attempt.ServerURL != "http://127.0.0.1:4096" || attempt.ServerPID != int64(1000+index) {
			t.Fatalf("attempt endpoint = %+v", attempt)
		}
		if attempt.SessionID != nil {
			t.Fatalf("new attempt session ID = %v, want nil", *attempt.SessionID)
		}
		if attemptIDs[attempt.ID] {
			t.Fatalf("duplicate attempt ID %q", attempt.ID)
		}
		attemptIDs[attempt.ID] = true
		decoded, err := hex.DecodeString(attempt.ID)
		if err != nil || len(decoded) != 16 {
			t.Fatalf("attempt ID %q is not 128-bit hex: %v", attempt.ID, err)
		}
		if err := s.FinishAttempt(ctx, attempt.ID, OutcomeSuccess, ""); err != nil {
			t.Fatalf("FinishAttempt(%s): %v", attempt.ID, err)
		}
	}

	open, err := s.ListOpenAttempts(ctx)
	if err != nil {
		t.Fatalf("ListOpenAttempts: %v", err)
	}
	if open == nil || len(open) != 0 {
		t.Fatalf("open attempts = %#v, want non-nil empty", open)
	}

	for _, wantTaskID := range wantOrder {
		task := mustShow(t, s, wantTaskID).Task
		if task.Status != StatusInReview {
			t.Fatalf("task %d status = %q, want in-review", task.ID, task.Status)
		}
	}
}

func TestReserveChangesRequestedBeforeTaskID(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	project, repo := seedProjectRepo(t, s)
	plain := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: "plain",
		Status: StatusTodo, Priority: PriorityMedium,
	})
	changes := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: "changes",
		Status: StatusTodo, Priority: PriorityMedium,
	})
	if _, err := s.SetMarking(ctx, "alice", changes.ID, MarkingChangesRequested, true); err != nil {
		t.Fatalf("mark changes: %v", err)
	}
	attempt := mustReserve(t, s, "http://127.0.0.1:4096", 100)
	if attempt.TaskID != changes.ID {
		t.Fatalf("selected task %d, want changes task %d ahead of older plain %d", attempt.TaskID, changes.ID, plain.ID)
	}
}

func TestReserveTieBreaksOnFewerInReviewBlockersThenTaskID(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	project, repo := seedProjectRepo(t, s)

	blocker := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: "blocker",
		Status: StatusInReview, Priority: PriorityLow,
	})
	blocked := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: "blocked but eligible",
		Status: StatusTodo, Priority: PriorityMedium,
	})
	if err := s.SetDependency(ctx, "alice", blocked.ID, blocker.ID, true); err != nil {
		t.Fatalf("SetDependency: %v", err)
	}
	plain := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: "plain",
		Status: StatusTodo, Priority: PriorityMedium,
	})
	first := mustReserve(t, s, "http://127.0.0.1:4096", 100)
	if first.TaskID != plain.ID {
		t.Fatalf("selected task %d, want plain %d (zero in-review blockers) over %d (one)", first.TaskID, plain.ID, blocked.ID)
	}
	if err := s.FinishAttempt(ctx, first.ID, OutcomeSuccess, ""); err != nil {
		t.Fatalf("finish plain: %v", err)
	}
	second := mustReserve(t, s, "http://127.0.0.1:4096", 100)
	if second.TaskID != blocked.ID {
		t.Fatalf("selected task %d, want blocked task %d", second.TaskID, blocked.ID)
	}
	if err := s.FinishAttempt(ctx, second.ID, OutcomeSuccess, ""); err != nil {
		t.Fatalf("finish blocked: %v", err)
	}

	older := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: "older",
		Status: StatusTodo, Priority: PriorityLow,
	})
	newer := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: "newer",
		Status: StatusTodo, Priority: PriorityLow,
	})
	third := mustReserve(t, s, "http://127.0.0.1:4096", 100)
	if third.TaskID != older.ID {
		t.Fatalf("selected task %d, want lower ID %d over %d", third.TaskID, older.ID, newer.ID)
	}
}

func TestReserveDependencyEligibilityAndReblock(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	project, repo := seedProjectRepo(t, s)

	blocker := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: "blocker",
		Status: StatusTodo, Priority: PriorityLow,
	})
	dependent := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: "high dependent",
		Status: StatusTodo, Priority: PriorityHigh,
	})
	normal := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: "normal",
		Status: StatusTodo, Priority: PriorityMedium,
	})
	if err := s.SetDependency(ctx, "alice", dependent.ID, blocker.ID, true); err != nil {
		t.Fatalf("add dependency: %v", err)
	}

	first := mustReserve(t, s, "http://127.0.0.1:4096", 100)
	if first.TaskID != normal.ID {
		t.Fatalf("blocked high task was reserved: selected %d, want normal %d", first.TaskID, normal.ID)
	}
	if err := s.FinishAttempt(ctx, first.ID, OutcomeSuccess, ""); err != nil {
		t.Fatalf("finish normal: %v", err)
	}

	inReview := StatusInReview
	if _, err := s.UpdateTask(ctx, "alice", blocker.ID, TaskPatch{Status: &inReview}); err != nil {
		t.Fatalf("mark blocker in-review: %v", err)
	}
	second := mustReserve(t, s, "http://127.0.0.1:4096", 100)
	if second.TaskID != dependent.ID {
		t.Fatalf("in-review blocker did not unblock high task: selected %d, want %d", second.TaskID, dependent.ID)
	}
	if err := s.FinishAttempt(ctx, second.ID, OutcomeSuccess, ""); err != nil {
		t.Fatalf("finish dependent: %v", err)
	}

	dependent2 := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: "second high dependent",
		Status: StatusTodo, Priority: PriorityHigh,
	})
	normal2 := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: "second normal",
		Status: StatusTodo, Priority: PriorityMedium,
	})
	if err := s.SetDependency(ctx, "alice", dependent2.ID, blocker.ID, true); err != nil {
		t.Fatalf("add second dependency: %v", err)
	}
	todo := StatusTodo
	blockerTask, err := s.UpdateTask(ctx, "alice", blocker.ID, TaskPatch{Status: &todo})
	if err != nil {
		t.Fatalf("return blocker to todo: %v", err)
	}
	if len(blockerTask.Markings) != 1 || blockerTask.Markings[0] != MarkingChangesRequested {
		t.Fatalf("returned blocker markings = %+v", blockerTask.Markings)
	}
	third := mustReserve(t, s, "http://127.0.0.1:4096", 100)
	if third.TaskID != normal2.ID {
		t.Fatalf("reblocked high task was reserved: selected %d, want normal2 %d", third.TaskID, normal2.ID)
	}
	if err := s.FinishAttempt(ctx, third.ID, OutcomeSuccess, ""); err != nil {
		t.Fatalf("finish normal2: %v", err)
	}
	fourth := mustReserve(t, s, "http://127.0.0.1:4096", 100)
	if fourth.TaskID != blocker.ID {
		t.Fatalf("selected %d, want changes-requested blocker %d", fourth.TaskID, blocker.ID)
	}
	if err := s.FinishAttempt(ctx, fourth.ID, OutcomeSuccess, ""); err != nil {
		t.Fatalf("finish blocker: %v", err)
	}
	fifth := mustReserve(t, s, "http://127.0.0.1:4096", 100)
	if fifth.TaskID != dependent2.ID {
		t.Fatalf("done blocker did not unblock second dependent: selected %d, want %d", fifth.TaskID, dependent2.ID)
	}
}

func TestReservePausedQueueAndNoCandidate(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	project, repo := seedProjectRepo(t, s)

	attempt, err := s.Reserve(ctx, "http://127.0.0.1:4096", 100)
	if err != nil {
		t.Fatalf("Reserve empty: %v", err)
	}
	if attempt != nil {
		t.Fatalf("Reserve with no tasks = %+v, want nil", attempt)
	}

	mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: "backlog urgent",
		Status: StatusBacklog, Priority: PriorityUrgent,
	})
	attempt, err = s.Reserve(ctx, "http://127.0.0.1:4096", 100)
	if err != nil || attempt != nil {
		t.Fatalf("backlog task reserve = %+v, %v; want nil, nil", attempt, err)
	}

	task := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: "todo", Status: StatusTodo,
	})
	paused := true
	if _, err := s.UpdateQueueSettings(ctx, "alice", QueueSettingsPatch{Paused: &paused}); err != nil {
		t.Fatalf("pause: %v", err)
	}
	attempt, err = s.Reserve(ctx, "http://127.0.0.1:4096", 100)
	if err != nil {
		t.Fatalf("Reserve paused: %v", err)
	}
	if attempt != nil {
		t.Fatalf("paused queue reserved %+v", attempt)
	}
	if got := mustShow(t, s, task.ID).Task.Status; got != StatusTodo {
		t.Fatalf("paused reserve changed status to %q", got)
	}
	if open := mustListOpen(t, s); len(open) != 0 {
		t.Fatalf("paused reserve created attempts: %+v", open)
	}

	if _, err := s.Reserve(ctx, "", 100); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank URL error = %v, want ErrInvalid", err)
	}
	if _, err := s.Reserve(ctx, "http://127.0.0.1:4096", 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nonpositive PID error = %v, want ErrInvalid", err)
	}

	unpaused := false
	if _, err := s.UpdateQueueSettings(ctx, "bob", QueueSettingsPatch{Paused: &unpaused}); err != nil {
		t.Fatalf("unpause: %v", err)
	}
	attempt = mustReserve(t, s, "http://127.0.0.1:4096", 100)
	if attempt.TaskID != task.ID {
		t.Fatalf("resumed reserve task = %d, want %d", attempt.TaskID, task.ID)
	}
	var queueEvents int
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM activity WHERE type = 'queue.updated'`,
	).Scan(&queueEvents); err != nil {
		t.Fatalf("count queue events: %v", err)
	}
	if queueEvents != 2 {
		t.Fatalf("queue.updated events = %d, want 2", queueEvents)
	}
	activities, err := s.ActivityAfter(ctx, 0, 0)
	if err != nil {
		t.Fatalf("ActivityAfter queue events: %v", err)
	}
	var queueChanges []Activity
	for _, activity := range activities {
		if activity.Type == ActivityQueueUpdated {
			queueChanges = append(queueChanges, activity)
		}
	}
	if len(queueChanges) != 2 {
		t.Fatalf("queue event history = %v", activityTypes(activities))
	}
	pauseEvent := queueChanges[0]
	oldSettings, ok := pauseEvent.Data["old"].(map[string]any)
	if !ok {
		t.Fatalf("queue old data = %#v", pauseEvent.Data["old"])
	}
	newSettings, ok := pauseEvent.Data["new"].(map[string]any)
	if !ok {
		t.Fatalf("queue new data = %#v", pauseEvent.Data["new"])
	}
	if pauseEvent.Actor != "alice" || oldSettings["paused"] != false || newSettings["paused"] != true {
		t.Fatalf("pause event = %+v", pauseEvent)
	}
	if queueChanges[1].Actor != "bob" {
		t.Fatalf("unpause actor = %q, want bob", queueChanges[1].Actor)
	}
}

func mustListOpen(t *testing.T, s *Store) []DispatchAttempt {
	t.Helper()
	attempts, err := s.ListOpenAttempts(context.Background())
	if err != nil {
		t.Fatalf("ListOpenAttempts: %v", err)
	}
	if attempts == nil {
		t.Fatal("ListOpenAttempts returned nil")
	}
	return attempts
}

func TestReserveClaimDetailsAndRepoEditGuard(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	project, repo := seedProjectRepo(t, s)
	otherRepo := mustAddRepo(t, s, project.ID, newGitRepo(t))
	task := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: "claim me", Status: StatusTodo,
	})
	for _, marking := range []string{MarkingChangesRequested, MarkingError} {
		if _, err := s.SetMarking(ctx, "alice", task.ID, marking, true); err != nil {
			t.Fatalf("SetMarking(%s): %v", marking, err)
		}
	}

	attempt, err := s.Reserve(ctx, "http://127.0.0.1:4096", 4242)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if attempt == nil || attempt.TaskID != task.ID || attempt.ServerPID != 4242 {
		t.Fatalf("attempt = %+v", attempt)
	}
	detail := mustShow(t, s, task.ID)
	if detail.Task.Status != StatusInProgress {
		t.Fatalf("task status = %q, want in-progress", detail.Task.Status)
	}
	if len(detail.Task.Markings) != 0 {
		t.Fatalf("claim did not consume markings: %+v", detail.Task.Markings)
	}
	if countActivityType(detail.Activity, ActivityTaskClaimed) != 1 {
		t.Fatalf("task.claimed count = %d", countActivityType(detail.Activity, ActivityTaskClaimed))
	}
	if countActivityType(detail.Activity, ActivityTaskMarking) != 4 {
		t.Fatalf("marking events = %d, want 4 (2 add, 2 consume)", countActivityType(detail.Activity, ActivityTaskMarking))
	}
	var claimed Activity
	for _, activity := range detail.Activity {
		if activity.Type == ActivityTaskClaimed {
			claimed = activity
		}
	}
	if claimed.Actor != ActorSystem || claimed.Data["attempt_id"] != attempt.ID || claimed.Data["repo_path"] != repo.Path {
		t.Fatalf("claimed event = %+v", claimed)
	}

	if _, err := s.UpdateTask(ctx, "alice", task.ID, TaskPatch{RepoID: &otherRepo.ID}); !errors.Is(err, ErrConflict) {
		t.Fatalf("repo edit during attempt error = %v, want ErrConflict", err)
	}
	title := "edited while running"
	if _, err := s.UpdateTask(ctx, "alice", task.ID, TaskPatch{Title: &title}); err != nil {
		t.Fatalf("title edit during attempt: %v", err)
	}

	open := mustListOpen(t, s)
	if len(open) != 1 || open[0].ID != attempt.ID {
		t.Fatalf("open attempts = %+v", open)
	}
	if open[0].SessionID != nil {
		t.Fatalf("unlinked attempt session ID = %v, want nil", *open[0].SessionID)
	}
	encoded, err := json.Marshal(open[0])
	if err != nil {
		t.Fatalf("marshal attempt: %v", err)
	}
	var attemptJSON map[string]any
	if err := json.Unmarshal(encoded, &attemptJSON); err != nil {
		t.Fatalf("unmarshal attempt: %v", err)
	}
	if _, present := attemptJSON["session_id"]; present {
		t.Fatalf("unlinked attempt JSON includes session_id: %s", encoded)
	}

	if err := s.FinishAttempt(ctx, attempt.ID, OutcomeSuccess, "done"); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if _, err := s.UpdateTask(ctx, "alice", task.ID, TaskPatch{RepoID: &otherRepo.ID}); err != nil {
		t.Fatalf("repo edit after attempt: %v", err)
	}
}

func TestReservePreventsSecondUnresolvedAttemptEvenWhenTodo(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	project, repo := seedProjectRepo(t, s)
	task := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: "task", Status: StatusTodo,
	})

	first := mustReserve(t, s, "http://127.0.0.1:4096", 100)
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO dispatch_attempts
			(id, task_id, repo_path, server_url, server_pid, state, detail, created_at)
		VALUES ('manual-reserved', ?, ?, 'http://127.0.0.1:4096', 55, 'reserved', '', ?)`,
		task.ID, repo.Path, formatTime(nowUTC()),
	); err == nil {
		t.Fatal("second reserved attempt insert succeeded; partial unique index missing")
	}
	todo := StatusTodo
	if _, err := s.UpdateTask(ctx, "alice", task.ID, TaskPatch{Status: &todo}); err != nil {
		t.Fatalf("manually return to todo: %v", err)
	}
	second, err := s.Reserve(ctx, "http://127.0.0.1:4096", 100)
	if err != nil {
		t.Fatalf("Reserve with unresolved attempt: %v", err)
	}
	if second != nil {
		t.Fatalf("reserved task with unresolved attempt: %+v", second)
	}
	if open := mustListOpen(t, s); len(open) != 1 || open[0].ID != first.ID {
		t.Fatalf("open attempts = %+v", open)
	}

	if err := s.FinishAttempt(ctx, first.ID, OutcomeAbandoned, "manual transition"); err != nil {
		t.Fatalf("abandon: %v", err)
	}
	afterAbandon := mustShow(t, s, task.ID)
	if afterAbandon.Task.Status != StatusTodo {
		t.Fatalf("abandon changed task status to %q", afterAbandon.Task.Status)
	}
	if len(afterAbandon.Task.Markings) != 0 {
		t.Fatalf("abandon changed markings: %+v", afterAbandon.Task.Markings)
	}
	if countActivityType(afterAbandon.Activity, ActivitySessionOutcome) != 1 {
		t.Fatalf("abandon outcome events = %v", activityTypes(afterAbandon.Activity))
	}
	if open := mustListOpen(t, s); len(open) != 0 {
		t.Fatalf("abandoned attempt still open: %+v", open)
	}

	third := mustReserve(t, s, "http://127.0.0.1:4096", 101)
	if third.ID == first.ID {
		t.Fatalf("attempt ID reused: %s", third.ID)
	}
	if third.TaskID != task.ID {
		t.Fatalf("second reservation task = %d, want %d", third.TaskID, task.ID)
	}
}

func TestLinkSessionAndFinishOutcome(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	project, repo := seedProjectRepo(t, s)
	task := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: "session task", Status: StatusTodo,
	})
	attempt := mustReserve(t, s, "http://127.0.0.1:4096", 777)

	for name, call := range map[string]func() error{
		"blank attempt": func() error { return s.LinkSession(ctx, "", "session", "http://session") },
		"blank session": func() error { return s.LinkSession(ctx, attempt.ID, " ", "http://session") },
		"blank URL":     func() error { return s.LinkSession(ctx, attempt.ID, "session", " ") },
		"unknown":       func() error { return s.LinkSession(ctx, "missing", "session", "http://session") },
	} {
		if err := call(); !errors.Is(err, ErrInvalid) && !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s error = %v, want ErrInvalid or ErrNotFound", name, err)
		}
	}

	sessionOne := "ses_gilir_" + attempt.ID
	sessionTwo := "ses_gilir_alt_" + attempt.ID
	if err := s.LinkSession(ctx, attempt.ID, sessionOne, "http://127.0.0.1:4096/session/one"); err != nil {
		t.Fatalf("link session one: %v", err)
	}
	if err := s.LinkSession(ctx, attempt.ID, sessionOne, "http://127.0.0.1:4096/session/one"); err != nil {
		t.Fatalf("idempotent link: %v", err)
	}
	if err := s.LinkSession(ctx, attempt.ID, sessionOne, "http://127.0.0.1:4096/session/other"); !errors.Is(err, ErrConflict) {
		t.Fatalf("rewrite session URL error = %v, want ErrConflict", err)
	}
	if err := s.LinkSession(ctx, attempt.ID, sessionTwo, "http://127.0.0.1:4096/session/two"); err != nil {
		t.Fatalf("link session two: %v", err)
	}

	detail := mustShow(t, s, task.ID)
	if countActivityType(detail.Activity, ActivitySessionLinked) != 2 {
		t.Fatalf("session.linked events = %v", activityTypes(detail.Activity))
	}
	if len(detail.Sessions) != 2 || detail.Sessions[0].ID != sessionOne || detail.Sessions[1].ID != sessionTwo {
		t.Fatalf("sessions = %+v, want ordered one then two", detail.Sessions)
	}
	for _, session := range detail.Sessions {
		if session.State != "linked" || session.AttemptID != attempt.ID || session.CreatedAt.Location() != time.UTC {
			t.Fatalf("session = %+v", session)
		}
	}

	open := mustListOpen(t, s)
	if len(open) != 1 || open[0].SessionID == nil || *open[0].SessionID != sessionOne {
		t.Fatalf("open attempt with session = %+v", open)
	}
	encoded, err := json.Marshal(open[0])
	if err != nil {
		t.Fatalf("marshal linked attempt: %v", err)
	}
	var attemptJSON map[string]any
	if err := json.Unmarshal(encoded, &attemptJSON); err != nil {
		t.Fatalf("unmarshal linked attempt: %v", err)
	}
	if attemptJSON["session_id"] != sessionOne {
		t.Fatalf("linked attempt JSON = %s", encoded)
	}

	if err := s.FinishAttempt(ctx, attempt.ID, OutcomeSuccess, "all good"); err != nil {
		t.Fatalf("finish success: %v", err)
	}
	finished := mustShow(t, s, task.ID)
	if finished.Task.Status != StatusInReview {
		t.Fatalf("task status after success = %q, want in-review", finished.Task.Status)
	}
	for _, session := range finished.Sessions {
		if session.State != "success" {
			t.Fatalf("finished session state = %q, want success", session.State)
		}
	}
	if countActivityType(finished.Activity, ActivitySessionOutcome) != 1 {
		t.Fatalf("session.outcome events = %v", activityTypes(finished.Activity))
	}
	var outcome Activity
	for _, activity := range finished.Activity {
		if activity.Type == ActivitySessionOutcome {
			outcome = activity
		}
	}
	if outcome.Actor != ActorSystem || outcome.Data["outcome"] != "success" ||
		outcome.Data["detail"] != "all good" || outcome.Data["attempt_id"] != attempt.ID {
		t.Fatalf("outcome event = %+v", outcome)
	}
	sessionIDs, ok := outcome.Data["session_ids"].([]any)
	if !ok || len(sessionIDs) != 2 {
		t.Fatalf("outcome session_ids = %#v", outcome.Data["session_ids"])
	}

	if err := s.FinishAttempt(ctx, attempt.ID, OutcomeSuccess, "again"); !errors.Is(err, ErrConflict) {
		t.Fatalf("second finish error = %v, want ErrConflict", err)
	}
	if after := mustShow(t, s, task.ID); countActivityType(after.Activity, ActivitySessionOutcome) != 1 {
		t.Fatalf("second finish wrote another outcome: %v", activityTypes(after.Activity))
	}
	if err := s.LinkSession(ctx, attempt.ID, "late", "http://late"); !errors.Is(err, ErrConflict) {
		t.Fatalf("late session link error = %v, want ErrConflict", err)
	}
	if err := s.LinkSession(ctx, attempt.ID, sessionOne, "http://127.0.0.1:4096/session/one"); err != nil {
		t.Fatalf("idempotent retry of existing link after finish: %v", err)
	}

	if err := s.FinishAttempt(ctx, "missing", OutcomeSuccess, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing attempt error = %v, want ErrNotFound", err)
	}
	if err := s.FinishAttempt(ctx, attempt.ID, AttemptOutcome("cancelled"), ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid outcome error = %v, want ErrInvalid", err)
	}
}

func TestFinishFailureAndReworkHistory(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	project, repo := seedProjectRepo(t, s)
	task := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: "rework", Status: StatusTodo,
	})
	if _, err := s.SetMarking(ctx, "alice", task.ID, MarkingChangesRequested, true); err != nil {
		t.Fatalf("mark changes requested: %v", err)
	}

	first := mustReserve(t, s, "http://127.0.0.1:4096", 100)
	if err := s.FinishAttempt(ctx, first.ID, OutcomeFailure, "tests failed"); err != nil {
		t.Fatalf("finish failure: %v", err)
	}
	failed := mustShow(t, s, task.ID)
	if failed.Task.Status != StatusInProgress {
		t.Fatalf("failure status = %q, want in-progress", failed.Task.Status)
	}
	if len(failed.Task.Markings) != 1 || failed.Task.Markings[0] != MarkingError {
		t.Fatalf("failure markings = %+v, want error", failed.Task.Markings)
	}
	if countActivityType(failed.Activity, ActivitySessionOutcome) != 1 {
		t.Fatalf("failure outcome events = %v", activityTypes(failed.Activity))
	}
	if countActivityType(failed.Activity, ActivityTaskMarking) != 3 {
		t.Fatalf("failure marking events = %v, want add changes-requested, consume, and add error", activityTypes(failed.Activity))
	}
	if err := s.FinishAttempt(ctx, first.ID, OutcomeFailure, "again"); !errors.Is(err, ErrConflict) {
		t.Fatalf("second failure error = %v, want ErrConflict", err)
	}

	todo := StatusTodo
	if _, err := s.UpdateTask(ctx, "alice", task.ID, TaskPatch{Status: &todo}); err != nil {
		t.Fatalf("return failed task to todo: %v", err)
	}
	second := mustReserve(t, s, "http://127.0.0.1:4096", 100)
	if second.TaskID != task.ID {
		t.Fatalf("retry task = %d, want %d", second.TaskID, task.ID)
	}
	retried := mustShow(t, s, task.ID)
	if len(retried.Task.Markings) != 0 {
		t.Fatalf("retry did not clear error marking: %+v", retried.Task.Markings)
	}
	if countActivityType(retried.Activity, ActivityTaskMarking) != 4 {
		t.Fatalf("retry marking events = %v, want error removal as fourth", activityTypes(retried.Activity))
	}
	if err := s.FinishAttempt(ctx, second.ID, OutcomeSuccess, "fixed"); err != nil {
		t.Fatalf("finish retry success: %v", err)
	}

	changes, err := s.Review(ctx, "carol", task.ID, ReviewRequestChanges, "more coverage")
	if err != nil {
		t.Fatalf("request changes: %v", err)
	}
	if changes.Status != StatusTodo {
		t.Fatalf("request changes status = %q", changes.Status)
	}
	third := mustReserve(t, s, "http://127.0.0.1:4096", 100)
	if third.TaskID != task.ID {
		t.Fatalf("rework reservation task = %d, want %d", third.TaskID, task.ID)
	}
	if err := s.FinishAttempt(ctx, third.ID, OutcomeSuccess, "addressed"); err != nil {
		t.Fatalf("finish rework: %v", err)
	}
	done, err := s.Review(ctx, "carol", task.ID, ReviewApprove, "ship it")
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if done.Status != StatusDone {
		t.Fatalf("approved status = %q", done.Status)
	}

	final := mustShow(t, s, task.ID)
	if countActivityType(final.Activity, ActivityTaskClaimed) != 3 {
		t.Fatalf("claims = %d, want 3", countActivityType(final.Activity, ActivityTaskClaimed))
	}
	if countActivityType(final.Activity, ActivitySessionOutcome) != 3 {
		t.Fatalf("outcomes = %d, want 3", countActivityType(final.Activity, ActivitySessionOutcome))
	}
	outcomes := map[string]bool{}
	for _, activity := range final.Activity {
		if activity.Type == ActivitySessionOutcome {
			outcomes[activity.Data["outcome"].(string)] = true
		}
	}
	if !outcomes["failure"] || !outcomes["success"] {
		t.Fatalf("outcome history = %+v, want failure and success", outcomes)
	}
}

func TestFinishAfterManualEditsAndAbandoned(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	project, repo := seedProjectRepo(t, s)
	task := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: "manual edits", Status: StatusTodo,
	})
	todo := StatusTodo

	first := mustReserve(t, s, "http://127.0.0.1:4096", 100)
	if _, err := s.UpdateTask(ctx, "alice", task.ID, TaskPatch{Status: &todo}); err != nil {
		t.Fatalf("manual return to todo: %v", err)
	}
	if err := s.FinishAttempt(ctx, first.ID, OutcomeSuccess, "finished after edit"); err != nil {
		t.Fatalf("finish after edit: %v", err)
	}
	edited := mustShow(t, s, task.ID)
	if edited.Task.Status != StatusTodo {
		t.Fatalf("finish after manual edit changed status to %q", edited.Task.Status)
	}
	if countActivityType(edited.Activity, ActivitySessionOutcome) != 1 {
		t.Fatalf("outcome not logged after manual edit: %v", activityTypes(edited.Activity))
	}

	second := mustReserve(t, s, "http://127.0.0.1:4096", 101)
	if second.TaskID != task.ID {
		t.Fatalf("task not reservable after terminal attempt: %d", second.TaskID)
	}
	if _, err := s.SetMarking(ctx, "alice", task.ID, "keep", true); err != nil {
		t.Fatalf("set keep marking: %v", err)
	}
	if _, err := s.UpdateTask(ctx, "alice", task.ID, TaskPatch{Status: &todo}); err != nil {
		t.Fatalf("manual return again: %v", err)
	}
	if err := s.FinishAttempt(ctx, second.ID, OutcomeAbandoned, "moved away before prompt"); err != nil {
		t.Fatalf("finish abandoned: %v", err)
	}
	abandoned := mustShow(t, s, task.ID)
	if abandoned.Task.Status != StatusTodo {
		t.Fatalf("abandoned changed status to %q", abandoned.Task.Status)
	}
	if len(abandoned.Task.Markings) != 1 || abandoned.Task.Markings[0] != "keep" {
		t.Fatalf("abandoned changed markings: %+v", abandoned.Task.Markings)
	}
	outcomeFound := false
	for _, activity := range abandoned.Activity {
		if activity.Type == ActivitySessionOutcome && activity.Data["outcome"] == "abandoned" &&
			activity.Data["detail"] == "moved away before prompt" {
			outcomeFound = true
		}
	}
	if !outcomeFound {
		t.Fatalf("abandoned outcome missing: %+v", abandoned.Activity)
	}
	if open := mustListOpen(t, s); len(open) != 0 {
		t.Fatalf("abandoned attempt open: %+v", open)
	}

	third := mustReserve(t, s, "http://127.0.0.1:4096", 102)
	if third.TaskID != task.ID {
		t.Fatalf("reserve after abandoned = %d", third.TaskID)
	}
	if _, err := s.UpdateTask(ctx, "alice", task.ID, TaskPatch{Status: &todo}); err != nil {
		t.Fatalf("manual return before failed outcome: %v", err)
	}
	if err := s.FinishAttempt(ctx, third.ID, OutcomeFailure, "late failure"); err != nil {
		t.Fatalf("finish failure after edit: %v", err)
	}
	failedAfterEdit := mustShow(t, s, task.ID)
	if failedAfterEdit.Task.Status != StatusTodo {
		t.Fatalf("late failure changed status to %q", failedAfterEdit.Task.Status)
	}
	for _, marking := range failedAfterEdit.Task.Markings {
		if marking == MarkingError {
			t.Fatalf("late failure applied error marking: %+v", failedAfterEdit.Task.Markings)
		}
	}
}

func TestConcurrentReserveSerializesWriters(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	project, repo := seedProjectRepo(t, s)
	const taskCount = 3
	for i := 0; i < taskCount; i++ {
		mustCreateTask(t, s, CreateTaskParams{
			ProjectID: project.ID, RepoID: repo.ID, Title: "concurrent", Status: StatusTodo,
		})
	}

	const workers = 8
	attempts := make(chan *DispatchAttempt, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			attempt, err := s.Reserve(ctx, "http://127.0.0.1:4096", int64(2000+worker))
			if err != nil {
				errs <- err
				return
			}
			attempts <- attempt
		}(i)
	}
	wg.Wait()
	close(attempts)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent Reserve: %v", err)
	}

	nonNil := 0
	taskIDs := map[int64]bool{}
	attemptIDs := map[string]bool{}
	for attempt := range attempts {
		if attempt == nil {
			continue
		}
		nonNil++
		if taskIDs[attempt.TaskID] {
			t.Fatalf("task %d reserved twice", attempt.TaskID)
		}
		taskIDs[attempt.TaskID] = true
		if attemptIDs[attempt.ID] {
			t.Fatalf("attempt ID %s issued twice", attempt.ID)
		}
		attemptIDs[attempt.ID] = true
	}
	if nonNil != taskCount {
		t.Fatalf("reserved %d tasks, want %d", nonNil, taskCount)
	}
	if open := mustListOpen(t, s); len(open) != taskCount {
		t.Fatalf("open attempts = %d, want %d", len(open), taskCount)
	}
}
