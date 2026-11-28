package store

import (
	"context"
	"errors"
	"testing"
)

// queuedTask creates a todo task with the given priority and title.
func queuedTask(t *testing.T, s *Store, project Project, repo Repo, title string, priority Priority) Task {
	t.Helper()
	return mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: title,
		Status: StatusTodo, Priority: priority,
	})
}

func statusPointer(status Status) *Status { return &status }

func TestBlockRequiresMessageAndAllowedStatuses(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	project, repo := seedProjectRepo(t, s)

	backlog := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: "backlog", Status: StatusBacklog,
	})
	review := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: "review", Status: StatusInReview,
	})
	done := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: "done", Status: StatusDone,
	})

	if _, err := s.Block(ctx, "alice", backlog.ID, "   "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank block message error = %v, want ErrInvalid", err)
	}
	for _, task := range []Task{review, done} {
		if _, err := s.Block(ctx, "alice", task.ID, "wait"); !errors.Is(err, ErrConflict) {
			t.Fatalf("Block(%s, %s) error = %v, want ErrConflict", task.ID, task.Status, err)
		}
		if detail := mustShow(t, s, task.ID); countActivityType(detail.Activity, ActivityTaskCommented) != 0 {
			t.Fatalf("refused block wrote a comment for %s: %v", task.ID, activityTypes(detail.Activity))
		}
	}

	blocked, err := s.Block(ctx, "alice", backlog.ID, "Waiting on the **ledger** migration.")
	if err != nil {
		t.Fatalf("Block backlog: %v", err)
	}
	if !hasMarking(blocked.Markings, MarkingBlocked) || blocked.Status != StatusBacklog {
		t.Fatalf("blocked backlog = %+v", blocked)
	}
	detail := mustShow(t, s, backlog.ID)
	if countActivityType(detail.Activity, ActivityTaskCommented) != 1 {
		t.Fatalf("block comments = %v", activityTypes(detail.Activity))
	}
	if _, err := s.Block(ctx, "alice", backlog.ID, "again"); !errors.Is(err, ErrConflict) {
		t.Fatalf("double block error = %v, want ErrConflict", err)
	}

	if _, err := s.SetMarking(ctx, "alice", backlog.ID, MarkingBlocked, false); !errors.Is(err, ErrInvalid) {
		t.Fatalf("SetMarking(blocked) error = %v, want ErrInvalid", err)
	}
}

func TestUnblockRequiresBlockedAndRequeuesOnlyIdleInProgress(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	project, repo := seedProjectRepo(t, s)

	never := queuedTask(t, s, project, repo, "never blocked", PriorityMedium)
	if _, err := s.Unblock(ctx, "alice", never.ID, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("Unblock unblocked error = %v, want ErrConflict", err)
	}
	if detail := mustShow(t, s, never.ID); detail.Attempt != nil {
		t.Fatalf("idle task attempt = %+v, want nil", detail.Attempt)
	}

	// backlog keeps its status, records the optional comment, and gets no boost.
	backlog := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: "backlog", Status: StatusBacklog,
	})
	if _, err := s.Block(ctx, "alice", backlog.ID, "hold"); err != nil {
		t.Fatalf("Block backlog: %v", err)
	}
	unblocked, err := s.Unblock(ctx, "alice", backlog.ID, "resumed")
	if err != nil {
		t.Fatalf("Unblock backlog: %v", err)
	}
	if unblocked.Status != StatusBacklog || hasMarking(unblocked.Markings, MarkingBlocked) {
		t.Fatalf("unblocked backlog = %+v, want unchanged status and no blocked marking", unblocked)
	}
	if hasMarking(unblocked.Markings, MarkingUnblocked) {
		t.Fatalf("backlog unblock gained the dispatch boost: %+v", unblocked.Markings)
	}
	if detail := mustShow(t, s, backlog.ID); countActivityType(detail.Activity, ActivityTaskCommented) != 2 {
		t.Fatalf("block and unblock comments = %v", activityTypes(detail.Activity))
	}

	// todo keeps todo and gets the boost. A blank message writes no comment.
	todo := queuedTask(t, s, project, repo, "todo", PriorityMedium)
	if _, err := s.Block(ctx, "alice", todo.ID, "hold"); err != nil {
		t.Fatalf("Block todo: %v", err)
	}
	unblocked, err = s.Unblock(ctx, "alice", todo.ID, "   ")
	if err != nil {
		t.Fatalf("Unblock todo: %v", err)
	}
	if unblocked.Status != StatusTodo || !hasMarking(unblocked.Markings, MarkingUnblocked) {
		t.Fatalf("unblocked todo = %+v, want todo with unblocked boost", unblocked)
	}
	if detail := mustShow(t, s, todo.ID); countActivityType(detail.Activity, ActivityTaskCommented) != 1 {
		t.Fatalf("blank unblock wrote a comment: %v", activityTypes(detail.Activity))
	}

	// idle in-progress re-queues; an actively reserved in-progress does not.
	idle := queuedTask(t, s, project, repo, "idle", PriorityMedium)
	if _, err := s.Block(ctx, "alice", idle.ID, "hold"); err != nil {
		t.Fatalf("Block idle: %v", err)
	}
	if _, err := s.UpdateTask(ctx, "alice", idle.ID, TaskPatch{Status: statusPointer(StatusInProgress)}); err != nil {
		t.Fatalf("move idle in progress: %v", err)
	}
	unblocked, err = s.Unblock(ctx, "alice", idle.ID, "")
	if err != nil {
		t.Fatalf("Unblock idle: %v", err)
	}
	if unblocked.Status != StatusTodo || !hasMarking(unblocked.Markings, MarkingUnblocked) {
		t.Fatalf("unblocked idle in-progress = %+v, want todo with boost", unblocked)
	}

	running := queuedTask(t, s, project, repo, "running", PriorityMedium)
	// An actively running in-progress task (reserved attempt) keeps its status
	// through the unblock. A blocked task is only in progress if it was claimed
	// before the block, so reserve first.
	active := mustReserveForTask(t, s, running.ID)
	if active == nil || active.TaskID != running.ID {
		t.Fatalf("could not reserve %s", running.ID)
	}
	if _, err := s.Block(ctx, "alice", running.ID, "hold"); err != nil {
		t.Fatalf("Block running: %v", err)
	}
	if after := mustShow(t, s, running.ID); after.Task.Status != StatusInProgress {
		t.Fatalf("status after blocking a claimed task = %s, want in-progress", after.Task.Status)
	}
	if after := mustShow(t, s, running.ID); after.Attempt == nil || after.Attempt.ID != active.ID {
		t.Fatalf("claimed task attempt = %+v, want %s", after.Attempt, active.ID)
	}
	unblocked, err = s.Unblock(ctx, "alice", running.ID, "")
	if err != nil {
		t.Fatalf("Unblock active in-progress: %v", err)
	}
	if unblocked.Status != StatusInProgress || hasMarking(unblocked.Markings, MarkingUnblocked) {
		t.Fatalf("unblocked active in-progress = %+v, want in-progress without boost", unblocked)
	}
	if err := s.FinishAttempt(ctx, active.ID, OutcomeAbandoned, "test"); err != nil {
		t.Fatalf("abandon running: %v", err)
	}
}

// mustReserveForTask drains the queue until taskID is reserved, abandoning the
// other claims.
func mustReserveForTask(t *testing.T, s *Store, taskID string) *DispatchAttempt {
	t.Helper()
	for range 32 {
		attempt, err := s.Reserve(context.Background(), "http://127.0.0.1:4096", 100)
		if err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		if attempt == nil {
			return nil
		}
		if attempt.TaskID == taskID {
			return attempt
		}
		if err := s.FinishAttempt(context.Background(), attempt.ID, OutcomeAbandoned, "drain"); err != nil {
			t.Fatalf("abandon %s: %v", attempt.TaskID, err)
		}
	}
	t.Fatalf("queue did not reach %s", taskID)
	return nil
}

func TestReserveSkipsBlockedAndBoostsResumedTasks(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	project, repo := seedProjectRepo(t, s)

	// A blocked urgent task must not outrank a fresh task at all.
	blockedUrgent := queuedTask(t, s, project, repo, "blocked urgent", PriorityUrgent)
	if _, err := s.Block(ctx, "alice", blockedUrgent.ID, "stop"); err != nil {
		t.Fatalf("Block urgent: %v", err)
	}
	plain := queuedTask(t, s, project, repo, "plain", PriorityMedium)
	if attempt := mustReserve(t, s, "http://127.0.0.1:4096", 100); attempt.TaskID != plain.ID {
		t.Fatalf("reserved %s, want %s (blocked urgent must be skipped)", attempt.TaskID, plain.ID)
	}
	if detail := mustShow(t, s, blockedUrgent.ID); !hasMarking(detail.Task.Markings, MarkingBlocked) {
		t.Fatal("Reserve cleared the blocked marking")
	}

	// Unblocking re-queues with a boost. The resumed task is created after an
	// older same-priority peer, so only the boost can put it first; without it
	// the older task would win on seq.
	older := queuedTask(t, s, project, repo, "older low", PriorityLow)
	resumed := queuedTask(t, s, project, repo, "resumed", PriorityLow)
	if _, err := s.Block(ctx, "alice", resumed.ID, "wait"); err != nil {
		t.Fatalf("Block resumed: %v", err)
	}
	fresher := queuedTask(t, s, project, repo, "fresher", PriorityLow)
	if older.ID == resumed.ID || fresher.ID == resumed.ID {
		t.Fatal("ids must differ")
	}
	unblocked, err := s.Unblock(ctx, "alice", resumed.ID, "")
	if err != nil {
		t.Fatalf("Unblock resumed: %v", err)
	}
	if !hasMarking(unblocked.Markings, MarkingUnblocked) {
		t.Fatalf("resumed task markings = %v, want unblocked", unblocked.Markings)
	}
	for range 32 {
		attempt, err := s.Reserve(ctx, "http://127.0.0.1:4096", 100)
		if err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		if attempt == nil {
			t.Fatalf("queue drained before %s", resumed.ID)
		}
		if attempt.TaskID == older.ID || attempt.TaskID == fresher.ID {
			t.Fatalf("reserved %s before the boosted %s", attempt.TaskID, resumed.ID)
		}
		if attempt.TaskID == resumed.ID {
			after := mustShow(t, s, resumed.ID)
			if hasMarking(after.Task.Markings, MarkingUnblocked) {
				t.Fatalf("Reserve left the boost marking on %s: %v", resumed.ID, after.Task.Markings)
			}
			break
		}
		if err := s.FinishAttempt(ctx, attempt.ID, OutcomeAbandoned, "drain"); err != nil {
			t.Fatalf("abandon drain: %v", err)
		}
	}
}

func TestFinishAttemptBlockedSuccessStaysInProgress(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	project, repo := seedProjectRepo(t, s)

	// Block a task while its run is active: the successful run must stay in
	// progress so dependents remain held, and clear any earlier error.
	task := queuedTask(t, s, project, repo, "blocked run", PriorityMedium)
	first := mustReserveForTask(t, s, task.ID)
	if first == nil {
		t.Fatalf("could not reserve %s", task.ID)
	}
	if _, err := s.Block(ctx, "alice", task.ID, "stop after this run"); err != nil {
		t.Fatalf("Block: %v", err)
	}
	if err := s.FinishAttempt(ctx, first.ID, OutcomeFailure, "tests failed"); err != nil {
		t.Fatalf("FinishAttempt failure: %v", err)
	}
	failed := mustShow(t, s, task.ID)
	if failed.Task.Status != StatusInProgress || !hasMarking(failed.Task.Markings, MarkingError) ||
		!hasMarking(failed.Task.Markings, MarkingBlocked) {
		t.Fatalf("blocked failure = %+v, want in-progress with error and blocked", failed.Task)
	}

	// Unblock re-queues the idle in-progress task, and the retry can succeed.
	if _, err := s.Unblock(ctx, "alice", task.ID, "fixed"); err != nil {
		t.Fatalf("Unblock: %v", err)
	}
	second := mustReserveForTask(t, s, task.ID)
	if second == nil {
		t.Fatalf("blocked task did not re-queue after unblock")
	}
	if err := s.FinishAttempt(ctx, second.ID, OutcomeSuccess, "all green"); err != nil {
		t.Fatalf("FinishAttempt success: %v", err)
	}
	done := mustShow(t, s, task.ID)
	if done.Task.Status != StatusInReview || hasMarking(done.Task.Markings, MarkingError) {
		t.Fatalf("success after unblock = %+v, want in-review without error", done.Task)
	}
}

func TestFinishAttemptBlockedSuccessKeepsInProgress(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	project, repo := seedProjectRepo(t, s)

	task := queuedTask(t, s, project, repo, "blocked active", PriorityMedium)
	attempt := mustReserveForTask(t, s, task.ID)
	if attempt == nil {
		t.Fatalf("could not reserve %s", task.ID)
	}
	// Seed a stale error after reservation so the test proves blocked success
	// clears it, not that Reserve once did.
	if _, err := s.SetMarking(ctx, "alice", task.ID, MarkingError, true); err != nil {
		t.Fatalf("SetMarking(error): %v", err)
	}
	if _, err := s.Block(ctx, "alice", task.ID, "hold"); err != nil {
		t.Fatalf("Block: %v", err)
	}
	if err := s.FinishAttempt(ctx, attempt.ID, OutcomeSuccess, "success"); err != nil {
		t.Fatalf("FinishAttempt: %v", err)
	}
	after := mustShow(t, s, task.ID)
	if after.Task.Status != StatusInProgress || hasMarking(after.Task.Markings, MarkingError) ||
		!hasMarking(after.Task.Markings, MarkingBlocked) {
		t.Fatalf("blocked success = %+v, want in-progress, blocked, no error", after.Task)
	}
	if countActivityType(after.Activity, ActivitySessionOutcome) != 1 {
		t.Fatalf("blocked success outcome audit = %v, want exactly one", activityTypes(after.Activity))
	}
	for _, activity := range after.Activity {
		if activity.Type == ActivitySessionOutcome && activity.Data["outcome"] != "success" {
			t.Fatalf("blocked success outcome = %+v, want success", activity)
		}
		if activity.Type == ActivityTaskStatusChanged && activity.Data["new"] == string(StatusInReview) {
			t.Fatalf("blocked success wrote an in-review transition: %+v", activity)
		}
	}
	if second, err := s.Reserve(ctx, "http://127.0.0.1:4096", 100); err != nil || second != nil {
		t.Fatalf("Reserve while blocked = (%+v, %v), want nil", second, err)
	}
}

func TestBlockedTaskCannotReachReviewOrDone(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	project, repo := seedProjectRepo(t, s)

	task := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: "guard", Status: StatusTodo,
	})
	if _, err := s.Block(ctx, "alice", task.ID, "hold"); err != nil {
		t.Fatalf("Block: %v", err)
	}
	for _, status := range []Status{StatusInReview, StatusDone} {
		if _, err := s.UpdateTask(ctx, "alice", task.ID, TaskPatch{Status: statusPointer(status)}); !errors.Is(err, ErrConflict) {
			t.Fatalf("move blocked task to %s error = %v, want ErrConflict", status, err)
		}
	}
	// Allowed moves inside the blockable set still work.
	if _, err := s.UpdateTask(ctx, "alice", task.ID, TaskPatch{Status: statusPointer(StatusBacklog)}); err != nil {
		t.Fatalf("move blocked task to backlog: %v", err)
	}
}
