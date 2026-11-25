package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestStateSurvivesCloseAndReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "persistent.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Close()
	})

	project := mustCreateProject(t, s, "persistent")
	repo := mustAddRepo(t, s, project.ID, newGitRepo(t))
	task := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: "persisted task", Description: "body",
		Status: StatusTodo, Priority: PriorityUrgent,
	})
	if _, err := s.SetMarking(ctx, "alice", task.ID, "docs", true); err != nil {
		t.Fatalf("SetMarking: %v", err)
	}
	blocker := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: "blocker", Status: StatusInReview,
	})
	if err := s.SetDependency(ctx, "alice", task.ID, blocker.ID, true); err != nil {
		t.Fatalf("SetDependency: %v", err)
	}

	attempt := mustReserve(t, s, "http://127.0.0.1:4096", 9876)
	if attempt == nil || attempt.TaskID != task.ID {
		t.Fatalf("attempt = %+v", attempt)
	}
	sessionID := "ses_gilir_" + attempt.ID
	if err := s.LinkSession(ctx, attempt.ID, sessionID, "http://127.0.0.1:4096/session/persisted"); err != nil {
		t.Fatalf("LinkSession: %v", err)
	}
	paused := true
	maxConcurrency := 3
	if _, err := s.UpdateQueueSettings(ctx, "alice", QueueSettingsPatch{
		Paused: &paused, MaxConcurrency: &maxConcurrency,
	}); err != nil {
		t.Fatalf("UpdateQueueSettings: %v", err)
	}
	latestBefore, err := s.LatestActivityID(ctx)
	if err != nil {
		t.Fatalf("LatestActivityID before close: %v", err)
	}
	if err := s.SaveHookCursor(ctx, "deploy", latestBefore); err != nil {
		t.Fatalf("SaveHookCursor: %v", err)
	}
	activitiesBefore, err := s.ActivityAfter(ctx, 0, 0)
	if err != nil {
		t.Fatalf("ActivityAfter before close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	s = reopened

	projects, err := s.ListProjects(ctx)
	if err != nil || len(projects) != 1 || projects[0].Name != "persistent" {
		t.Fatalf("projects after reopen = %+v, %v", projects, err)
	}
	repos, err := s.ListRepos(ctx, project.ID)
	if err != nil || len(repos) != 1 || repos[0].Path != repo.Path {
		t.Fatalf("repos after reopen = %+v, %v", repos, err)
	}
	detail := mustShow(t, s, task.ID)
	if detail.Task.Title != "persisted task" || detail.Task.Description != "body" ||
		detail.Task.Status != StatusInProgress || detail.Task.Priority != PriorityUrgent {
		t.Fatalf("task after reopen = %+v", detail.Task)
	}
	if len(detail.Task.Markings) != 1 || detail.Task.Markings[0] != "docs" {
		t.Fatalf("markings after reopen = %+v", detail.Task.Markings)
	}
	if len(detail.Blockers) != 1 || detail.Blockers[0] != blocker.ID {
		t.Fatalf("blockers after reopen = %+v", detail.Blockers)
	}
	if len(detail.Sessions) != 1 || detail.Sessions[0].ID != sessionID || detail.Sessions[0].State != "linked" {
		t.Fatalf("sessions after reopen = %+v", detail.Sessions)
	}

	openAttempts := mustListOpen(t, s)
	if len(openAttempts) != 1 {
		t.Fatalf("open attempts after reopen = %+v", openAttempts)
	}
	restored := openAttempts[0]
	if restored.ID != attempt.ID || restored.TaskID != task.ID || restored.RepoPath != repo.Path ||
		restored.ServerURL != "http://127.0.0.1:4096" || restored.ServerPID != 9876 {
		t.Fatalf("attempt after reopen = %+v", restored)
	}
	if restored.SessionID == nil || *restored.SessionID != sessionID {
		t.Fatalf("attempt session after reopen = %+v", restored.SessionID)
	}

	settings, err := s.QueueSettings(ctx)
	if err != nil || !settings.Paused || settings.MaxConcurrency != 3 {
		t.Fatalf("settings after reopen = %+v, %v", settings, err)
	}
	if latest, err := s.LatestActivityID(ctx); err != nil || latest != latestBefore {
		t.Fatalf("latest activity after reopen = %d, %v; want %d", latest, err, latestBefore)
	}
	if cursor, initialized, err := s.HookCursor(ctx, "deploy"); err != nil || !initialized || cursor != latestBefore {
		t.Fatalf("hook cursor after reopen = (%d,%v,%v), want (%d,true,nil)", cursor, initialized, err, latestBefore)
	}
	activitiesAfter, err := s.ActivityAfter(ctx, 0, 0)
	if err != nil || len(activitiesAfter) != len(activitiesBefore) {
		t.Fatalf("activity after reopen = %d, %v; want %d", len(activitiesAfter), err, len(activitiesBefore))
	}

	pausedAttempt, err := s.Reserve(ctx, "http://127.0.0.1:4096", 9876)
	if err != nil {
		t.Fatalf("Reserve paused after reopen: %v", err)
	}
	if pausedAttempt != nil {
		t.Fatalf("paused queue reserved after reopen: %+v", pausedAttempt)
	}

	unpaused := false
	if _, err := s.UpdateQueueSettings(ctx, "bob", QueueSettingsPatch{Paused: &unpaused}); err != nil {
		t.Fatalf("unpause after reopen: %v", err)
	}
	if err := s.FinishAttempt(ctx, restored.ID, OutcomeSuccess, "resumed"); err != nil {
		t.Fatalf("FinishAttempt after reopen: %v", err)
	}
	finished := mustShow(t, s, task.ID)
	if finished.Task.Status != StatusInReview {
		t.Fatalf("task after finish = %+v", finished.Task)
	}
	if len(finished.Sessions) != 1 || finished.Sessions[0].State != "success" {
		t.Fatalf("session after finish = %+v", finished.Sessions)
	}

	attemptState, outcome, detailText, finishedAt := readAttemptRow(t, s, attempt.ID)
	if attemptState != string(OutcomeSuccess) || outcome != string(OutcomeSuccess) ||
		detailText != "resumed" || !finishedAt.Valid {
		t.Fatalf("attempt row after finish = state %q outcome %q detail %q finished %+v",
			attemptState, outcome, detailText, finishedAt)
	}

	paused = false
	if _, err := s.UpdateQueueSettings(ctx, "bob", QueueSettingsPatch{Paused: &paused}); err != nil {
		t.Fatalf("set queue for second restart: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	again, err := Open(path)
	if err != nil {
		t.Fatalf("second reopen: %v", err)
	}
	s = again
	final := mustShow(t, s, task.ID)
	if final.Task.Status != StatusInReview || len(final.Sessions) != 1 || final.Sessions[0].State != "success" {
		t.Fatalf("state after second reopen = %+v", final)
	}
	if open := mustListOpen(t, s); len(open) != 0 {
		t.Fatalf("terminal attempt reopened as open: %+v", open)
	}
	if activities, err := s.ActivityAfter(ctx, 0, 0); err != nil || len(activities) <= len(activitiesAfter) {
		t.Fatalf("activity after second reopen = %d, %v", len(activities), err)
	}
}

func readAttemptRow(t *testing.T, s *Store, attemptID string) (state, outcome, detail string, finishedAt sql.NullString) {
	t.Helper()
	err := s.db.QueryRow(`
		SELECT state, outcome, detail, finished_at FROM dispatch_attempts WHERE id = ?`, attemptID,
	).Scan(&state, &outcome, &detail, &finishedAt)
	if err != nil {
		t.Fatalf("read attempt row: %v", err)
	}
	return state, outcome, detail, finishedAt
}
