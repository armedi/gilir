package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"
	"testing"
)

func TestCreateTaskDefaultsJSONAndDetailArrays(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	project, repo := seedProjectRepo(t, s)
	otherProject := mustCreateProject(t, s, "empty project")

	if _, err := s.CreateTask(ctx, "alice", CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: "  \t ",
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank title error = %v, want ErrInvalid", err)
	}
	if _, err := s.CreateTask(ctx, "alice", CreateTaskParams{
		ProjectID: 99999, RepoID: repo.ID, Title: "missing project",
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing project error = %v, want ErrNotFound", err)
	}

	task := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID,
		RepoID:    repo.ID,
		Title:     "  first task  ",
		Body:      "",
	})
	if task.Title != "first task" {
		t.Fatalf("title = %q, want trimmed title", task.Title)
	}
	if task.Status != StatusBacklog || task.Priority != PriorityMedium {
		t.Fatalf("defaults = status %q priority %q, want backlog/medium", task.Status, task.Priority)
	}
	if task.Markings == nil || len(task.Markings) != 0 {
		t.Fatalf("markings = %#v, want empty non-nil", task.Markings)
	}
	if task.ID != "GILIR-1" || task.CreatedAt.IsZero() || task.UpdatedAt.IsZero() {
		t.Fatalf("task = %+v", task)
	}

	encoded, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("marshal task: %v", err)
	}
	var object map[string]any
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatalf("unmarshal task: %v", err)
	}
	for _, key := range []string{
		"id", "project_id", "repo_id", "title", "body", "status",
		"priority", "created_at", "updated_at", "markings",
	} {
		if _, ok := object[key]; !ok {
			t.Fatalf("task JSON missing %q: %s", key, encoded)
		}
	}
	if len(object) != 10 {
		t.Fatalf("task JSON has extra keys: %s", encoded)
	}
	if markings, ok := object["markings"].([]any); !ok || len(markings) != 0 {
		t.Fatalf("markings JSON = %#v, want []", object["markings"])
	}
	if !strings.Contains(string(encoded), `"markings":[]`) {
		t.Fatalf("empty markings not encoded as array: %s", encoded)
	}
	created, ok := object["created_at"].(string)
	if !ok || !strings.HasSuffix(created, "Z") {
		t.Fatalf("created_at = %#v, want UTC string", object["created_at"])
	}

	detail := mustShow(t, s, task.ID)
	if detail.Blockers == nil || detail.Sessions == nil || detail.Activity == nil {
		t.Fatalf("detail arrays must be non-nil: %+v", detail)
	}
	if len(detail.Blockers) != 0 || len(detail.Sessions) != 0 || len(detail.Activity) == 0 {
		t.Fatalf("detail = %+v", detail)
	}
	detailJSON, err := json.Marshal(detail)
	if err != nil {
		t.Fatalf("marshal detail: %v", err)
	}
	if !strings.Contains(string(detailJSON), `"blockers":[]`) ||
		!strings.Contains(string(detailJSON), `"sessions":[]`) {
		t.Fatalf("detail empty arrays not encoded as arrays: %s", detailJSON)
	}

	empty, err := s.ListTasks(ctx, TaskFilter{ProjectID: otherProject.ID})
	if err != nil {
		t.Fatalf("ListTasks empty: %v", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Fatalf("empty ListTasks = %#v, want non-nil empty", empty)
	}

	second := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: "second task",
	})
	if second.ID != "GILIR-2" {
		t.Fatalf("task IDs not FIFO: %s then %s", task.ID, second.ID)
	}
}

// TestEventDataReservesBodyForKey pins the activity-data vocabulary: "body" names
// task.body and nothing else. Comment records store their prose under "message",
// the same name block and review take, because they all become comments.
// ActivityFeed identifies a comment thread by the presence of that key, so a
// task body under "body" would otherwise render created tasks as comments.
// Task body can also be arbitrarily long, so it stays on the task row rather
// than being duplicated into the append-only history and hook deliveries.
func TestEventDataReservesBodyForKey(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	project, repo := seedProjectRepo(t, s)
	task := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: "audited", Body: "long task body",
	})
	if _, err := s.Comment(ctx, "alice", task.ID, "please add a test"); err != nil {
		t.Fatalf("Comment: %v", err)
	}

	detail := mustShow(t, s, task.ID)
	var created, commented Activity
	for _, activity := range detail.Activity {
		switch activity.Type {
		case ActivityTaskCreated:
			created = activity
		case ActivityTaskCommented:
			commented = activity
		}
	}
	if created.Type != ActivityTaskCreated || commented.Type != ActivityTaskCommented {
		t.Fatalf("events missing: %v", activityTypes(detail.Activity))
	}
	if _, ok := created.Data["body"]; ok {
		t.Fatalf("task.created data carries body: %#v", created.Data)
	}
	// A stray message would render creation as a comment bubble, so pin the whole
	// payload rather than only checking for body.
	wantCreated := []string{"priority", "repo_id", "status", "title"}
	if got := sortedKeys(created.Data); !slices.Equal(got, wantCreated) {
		t.Fatalf("task.created keys = %v, want exactly %v", got, wantCreated)
	}
	if created.Data["title"] != "audited" {
		t.Fatalf("task.created title = %#v, want audited", created.Data["title"])
	}
	if commented.Data["message"] != "please add a test" {
		t.Fatalf("comment message = %#v, want it under the message key", commented.Data["message"])
	}
	if _, ok := commented.Data["body"]; ok {
		t.Fatalf("comment data carries body: %#v", commented.Data)
	}
	// Read the stored row, not the create response, which copies its input.
	if detail.Task.Body != "long task body" {
		t.Fatalf("stored task body = %q, want it preserved on the task row", detail.Task.Body)
	}
}

func sortedKeys(data map[string]any) []string {
	keys := make([]string, 0, len(data))
	for key := range data {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func TestListTasksFilters(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	projectA, repoA := seedProjectRepo(t, s)
	projectB := mustCreateProject(t, s, "B")
	repoB := mustAddRepo(t, s, projectB.ID, newGitRepo(t))

	backlog := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: projectA.ID, RepoID: repoA.ID, Title: "backlog low",
		Status: StatusBacklog, Priority: PriorityLow,
	})
	urgent := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: projectA.ID, RepoID: repoA.ID, Title: "urgent",
		Status: StatusTodo, Priority: PriorityUrgent,
	})
	progress := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: projectA.ID, RepoID: repoA.ID, Title: "progress",
		Status: StatusInProgress, Priority: PriorityMedium,
	})
	other := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: projectB.ID, RepoID: repoB.ID, Title: "other",
		Status: StatusTodo, Priority: PriorityHigh,
	})

	if _, err := s.SetMarking(ctx, "alice", backlog.ID, " docs ", true); err != nil {
		t.Fatalf("SetMarking: %v", err)
	}
	if _, err := s.SetMarking(ctx, "alice", other.ID, "docs", true); err != nil {
		t.Fatalf("SetMarking: %v", err)
	}

	cases := []struct {
		name   string
		filter TaskFilter
		want   []string
	}{
		{"project", TaskFilter{ProjectID: projectA.ID}, []string{backlog.ID, urgent.ID, progress.ID}},
		{"repo", TaskFilter{RepoID: repoB.ID}, []string{other.ID}},
		{"status", TaskFilter{Status: StatusTodo}, []string{urgent.ID, other.ID}},
		{"priority urgent", TaskFilter{Priority: PriorityUrgent}, []string{urgent.ID}},
		{"marking normalized", TaskFilter{Marking: " docs "}, []string{backlog.ID, other.ID}},
		{"combined", TaskFilter{ProjectID: projectA.ID, Status: StatusTodo, Marking: "docs"}, []string{}},
		{"project and priority", TaskFilter{ProjectID: projectA.ID, Priority: PriorityLow}, []string{backlog.ID}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			tasks, err := s.ListTasks(ctx, testCase.filter)
			if err != nil {
				t.Fatalf("ListTasks(%+v): %v", testCase.filter, err)
			}
			if tasks == nil {
				t.Fatal("ListTasks returned nil slice")
			}
			if len(tasks) != len(testCase.want) {
				t.Fatalf("got %d tasks, want %d: %+v", len(tasks), len(testCase.want), tasks)
			}
			for i, id := range testCase.want {
				if tasks[i].ID != id {
					t.Fatalf("task[%d].ID = %s, want %s (tasks=%+v)", i, tasks[i].ID, id, tasks)
				}
			}
		})
	}

	for _, filter := range []TaskFilter{
		{ProjectID: -1},
		{RepoID: -1},
		{Status: Status("blocked")},
		{Priority: Priority("critical")},
		{Marking: "   "},
	} {
		if _, err := s.ListTasks(ctx, filter); !errors.Is(err, ErrInvalid) {
			t.Fatalf("ListTasks(%+v) error = %v, want ErrInvalid", filter, err)
		}
	}
}

func TestDeleteTaskPreservesAuditAndRemovesTerminalHistory(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	project, repo := seedProjectRepo(t, s)
	task := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: "delete me", Status: StatusTodo,
	})
	blocker := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: "blocker", Status: StatusInReview,
	})
	if err := s.SetDependency(ctx, "alice", task.ID, blocker.ID, true); err != nil {
		t.Fatalf("SetDependency: %v", err)
	}
	if _, err := s.SetMarking(ctx, "alice", task.ID, "docs", true); err != nil {
		t.Fatalf("SetMarking: %v", err)
	}
	if _, err := s.Comment(ctx, "alice", task.ID, "prior audit"); err != nil {
		t.Fatalf("Comment: %v", err)
	}

	attempt := mustReserve(t, s, "http://127.0.0.1:4096", 4242)
	sessionOne := "ses_gilir_" + attempt.ID
	sessionTwo := "ses_gilir_alt_" + attempt.ID
	worktree := mustRecordWorktree(t, s, attempt.ID)
	if err := s.LinkSession(ctx, attempt.ID, sessionOne, "http://127.0.0.1:4096/session/one", worktree); err != nil {
		t.Fatalf("LinkSession one: %v", err)
	}
	if err := s.LinkSession(ctx, attempt.ID, sessionTwo, "http://127.0.0.1:4096/session/two", worktree); err != nil {
		t.Fatalf("LinkSession two: %v", err)
	}
	if err := s.FinishAttempt(ctx, attempt.ID, OutcomeSuccess, "complete"); err != nil {
		t.Fatalf("FinishAttempt: %v", err)
	}

	before, err := s.ActivityAfter(ctx, 0, 1000)
	if err != nil {
		t.Fatalf("ActivityAfter before delete: %v", err)
	}

	if err := s.DeleteTask(ctx, "deleter", task.ID); err != nil {
		t.Fatalf("DeleteTask: %v", err)
	}
	if _, err := s.Show(ctx, task.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Show deleted task error = %v, want ErrNotFound", err)
	}
	if _, err := s.Show(ctx, blocker.ID); err != nil {
		t.Fatalf("Show blocker after delete: %v", err)
	}

	var attempts, sessions, markings, dependencies int
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM dispatch_attempts WHERE task_id = ?`, task.ID,
	).Scan(&attempts); err != nil {
		t.Fatalf("count task attempts: %v", err)
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM sessions WHERE id IN (?, ?)`, sessionOne, sessionTwo,
	).Scan(&sessions); err != nil {
		t.Fatalf("count task sessions: %v", err)
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM task_markings WHERE task_id = ?`, task.ID,
	).Scan(&markings); err != nil {
		t.Fatalf("count task markings: %v", err)
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM task_dependencies WHERE task_id = ?`, task.ID,
	).Scan(&dependencies); err != nil {
		t.Fatalf("count task dependencies: %v", err)
	}
	if attempts != 0 || sessions != 0 || markings != 0 || dependencies != 0 {
		t.Fatalf("delete left attempts=%d sessions=%d markings=%d dependencies=%d, want all zero",
			attempts, sessions, markings, dependencies)
	}

	after, err := s.ActivityAfter(ctx, 0, 1000)
	if err != nil {
		t.Fatalf("ActivityAfter after delete: %v", err)
	}
	if len(after) != len(before)+1 {
		t.Fatalf("activity length = %d, want %d after one task.deleted event", len(after), len(before)+1)
	}
	for i := range before {
		if after[i].ID != before[i].ID || after[i].Type != before[i].Type {
			t.Fatalf("history changed at %d: %+v -> %+v", i, before[i], after[i])
		}
	}
	deleted := after[len(after)-1]
	if deleted.Type != ActivityTaskDeleted || deleted.Actor != "deleter" ||
		deleted.TaskID == nil || *deleted.TaskID != task.ID ||
		deleted.ProjectID == nil || *deleted.ProjectID != project.ID {
		t.Fatalf("task.deleted event = %+v", deleted)
	}
	sawPriorComment := false
	for _, activity := range after {
		if activity.Type == ActivityTaskCommented && activity.Data["message"] == "prior audit" {
			sawPriorComment = true
		}
	}
	if !sawPriorComment {
		t.Fatalf("prior audit missing after delete: %v", activityTypes(after))
	}

	if err := s.DeleteTask(ctx, "again", task.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second DeleteTask error = %v, want ErrNotFound", err)
	}
	if err := s.DeleteTask(ctx, "again", "42"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unprefixed task DeleteTask error = %v, want ErrInvalid", err)
	}
	repeated, err := s.ActivityAfter(ctx, 0, 1000)
	if err != nil {
		t.Fatalf("ActivityAfter after repeat: %v", err)
	}
	if len(repeated) != len(after) || countActivityType(repeated, ActivityTaskDeleted) != 1 {
		t.Fatalf("repeat delete wrote activity: before=%d after=%d types=%v",
			len(after), len(repeated), activityTypes(repeated))
	}
}

func TestDeleteTaskConflictsPreserveData(t *testing.T) {
	t.Run("reserved attempt", func(t *testing.T) {
		s, _ := openTestStore(t)
		ctx := context.Background()
		project, repo := seedProjectRepo(t, s)
		task := mustCreateTask(t, s, CreateTaskParams{
			ProjectID: project.ID, RepoID: repo.ID, Title: "running", Status: StatusTodo,
		})
		attempt := mustReserve(t, s, "http://127.0.0.1:4096", 4242)
		sessionID := "ses_gilir_" + attempt.ID
		worktree := mustRecordWorktree(t, s, attempt.ID)
		if err := s.LinkSession(ctx, attempt.ID, sessionID, "http://127.0.0.1:4096/session/running", worktree); err != nil {
			t.Fatalf("LinkSession: %v", err)
		}
		before, err := s.ActivityAfter(ctx, 0, 1000)
		if err != nil {
			t.Fatalf("ActivityAfter before delete: %v", err)
		}

		err = s.DeleteTask(ctx, "deleter", task.ID)
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("DeleteTask with reserved attempt error = %v, want ErrConflict", err)
		}
		if !strings.Contains(err.Error(), attempt.ID) || !strings.Contains(err.Error(), "reserved") {
			t.Fatalf("reserved conflict error = %v, want the attempt ID and reserved state", err)
		}

		detail := mustShow(t, s, task.ID)
		if len(detail.Sessions) != 1 || detail.Sessions[0].ID != sessionID {
			t.Fatalf("sessions after refused delete = %+v", detail.Sessions)
		}
		if open := mustListOpen(t, s); len(open) != 1 || open[0].ID != attempt.ID {
			t.Fatalf("open attempts after refused delete = %+v", open)
		}
		after, err := s.ActivityAfter(ctx, 0, 1000)
		if err != nil {
			t.Fatalf("ActivityAfter after refused delete: %v", err)
		}
		if len(after) != len(before) || countActivityType(after, ActivityTaskDeleted) != 0 {
			t.Fatalf("refused delete wrote activity: before=%d after=%d types=%v",
				len(before), len(after), activityTypes(after))
		}
	})

	t.Run("incoming dependency", func(t *testing.T) {
		s, _ := openTestStore(t)
		ctx := context.Background()
		project, repo := seedProjectRepo(t, s)
		blocker := mustCreateTask(t, s, CreateTaskParams{
			ProjectID: project.ID, RepoID: repo.ID, Title: "blocker",
		})
		dependent := mustCreateTask(t, s, CreateTaskParams{
			ProjectID: project.ID, RepoID: repo.ID, Title: "dependent",
		})
		if err := s.SetDependency(ctx, "alice", dependent.ID, blocker.ID, true); err != nil {
			t.Fatalf("SetDependency: %v", err)
		}
		before, err := s.ActivityAfter(ctx, 0, 1000)
		if err != nil {
			t.Fatalf("ActivityAfter before delete: %v", err)
		}

		err = s.DeleteTask(ctx, "deleter", blocker.ID)
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("DeleteTask with incoming dependency error = %v, want ErrConflict", err)
		}
		if !strings.Contains(err.Error(), "incoming dependencies") ||
			!strings.Contains(err.Error(), dependent.ID) {
			t.Fatalf("incoming conflict error = %v, want the dependent task ID", err)
		}

		mustShow(t, s, blocker.ID)
		dependentDetail := mustShow(t, s, dependent.ID)
		if len(dependentDetail.Blockers) != 1 || dependentDetail.Blockers[0] != blocker.ID {
			t.Fatalf("blockers after refused delete = %+v, want [%s]",
				dependentDetail.Blockers, blocker.ID)
		}
		after, err := s.ActivityAfter(ctx, 0, 1000)
		if err != nil {
			t.Fatalf("ActivityAfter after refused delete: %v", err)
		}
		if len(after) != len(before) || countActivityType(after, ActivityTaskDeleted) != 0 {
			t.Fatalf("refused delete wrote activity: before=%d after=%d types=%v",
				len(before), len(after), activityTypes(after))
		}
	})
}

func TestUpdateTaskPatchNoOpAndAudit(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	project, repo := seedProjectRepo(t, s)
	otherRepo := mustAddRepo(t, s, project.ID, newGitRepo(t))
	task := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: "original", Body: "body",
	})
	originalDetail := mustShow(t, s, task.ID)
	originalUpdatedAt := task.UpdatedAt

	sameTitle := task.Title
	sameBody := task.Body
	samePriority := task.Priority
	sameStatus := task.Status
	sameRepo := task.RepoID
	unchanged, err := s.UpdateTask(ctx, "bob", task.ID, TaskPatch{
		Title:    &sameTitle,
		Body:     &sameBody,
		Priority: &samePriority,
		Status:   &sameStatus,
		RepoID:   &sameRepo,
	})
	if err != nil {
		t.Fatalf("no-op UpdateTask: %v", err)
	}
	if !unchanged.UpdatedAt.Equal(originalUpdatedAt) {
		t.Fatalf("no-op update changed updated_at: %s -> %s", originalUpdatedAt, unchanged.UpdatedAt)
	}
	noOpDetail := mustShow(t, s, task.ID)
	if len(noOpDetail.Activity) != len(originalDetail.Activity) {
		t.Fatalf("no-op update wrote activity: before=%v after=%v",
			activityTypes(originalDetail.Activity), activityTypes(noOpDetail.Activity))
	}

	newTitle := "  renamed task  "
	emptyBody := ""
	priority := PriorityUrgent
	updated, err := s.UpdateTask(ctx, "bob", task.ID, TaskPatch{
		Title:    &newTitle,
		Body:     &emptyBody,
		Priority: &priority,
		RepoID:   &otherRepo.ID,
	})
	if err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}
	if updated.Title != "renamed task" || updated.Body != "" ||
		updated.Priority != PriorityUrgent || updated.RepoID != otherRepo.ID {
		t.Fatalf("updated task = %+v", updated)
	}
	encoded, err := json.Marshal(updated)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Priority string `json:"priority"`
	}
	if err := json.Unmarshal(encoded, &wire); err != nil || wire.Priority != "urgent" {
		t.Fatalf("updated task JSON priority = %q, %v; want urgent", wire.Priority, err)
	}
	if !updated.UpdatedAt.After(originalUpdatedAt) {
		t.Fatalf("updated_at did not advance: %s -> %s", originalUpdatedAt, updated.UpdatedAt)
	}

	detail := mustShow(t, s, task.ID)
	updates := 0
	var updateEvent Activity
	for _, activity := range detail.Activity {
		if activity.Type == ActivityTaskUpdated {
			updates++
			updateEvent = activity
		}
	}
	if updates != 1 {
		t.Fatalf("task.updated count = %d, want 1: %v", updates, activityTypes(detail.Activity))
	}
	if updateEvent.Actor != "bob" {
		t.Fatalf("update actor = %q, want bob", updateEvent.Actor)
	}
	oldFields, ok := updateEvent.Data["old"].(map[string]any)
	if !ok {
		t.Fatalf("task.updated old data = %#v", updateEvent.Data["old"])
	}
	newFields, ok := updateEvent.Data["new"].(map[string]any)
	if !ok {
		t.Fatalf("task.updated new data = %#v", updateEvent.Data["new"])
	}
	if oldFields["title"] != "original" || newFields["title"] != "renamed task" {
		t.Fatalf("title audit old/new = %#v / %#v", oldFields["title"], newFields["title"])
	}
	if oldFields["body"] != "body" || newFields["body"] != "" {
		t.Fatalf("body audit old/new = %#v / %#v", oldFields["body"], newFields["body"])
	}
	if oldFields["priority"] != "medium" || newFields["priority"] != "urgent" {
		t.Fatalf("priority audit old/new = %#v / %#v", oldFields["priority"], newFields["priority"])
	}
	if oldFields["repo_id"] != float64(repo.ID) || newFields["repo_id"] != float64(otherRepo.ID) {
		t.Fatalf("repo audit old/new = %#v / %#v", oldFields["repo_id"], newFields["repo_id"])
	}

	beforeLength := len(detail.Activity)
	if _, err := s.UpdateTask(ctx, "bob", task.ID, TaskPatch{Body: &emptyBody}); err != nil {
		t.Fatalf("no-op empty body: %v", err)
	}
	if after := mustShow(t, s, task.ID); len(after.Activity) != beforeLength {
		t.Fatalf("explicit empty no-op wrote activity: %v", activityTypes(after.Activity))
	}

	blank := "  "
	if _, err := s.UpdateTask(ctx, "bob", task.ID, TaskPatch{Title: &blank}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank title error = %v, want ErrInvalid", err)
	}
	strayMessage := "note"
	if _, err := s.UpdateTask(ctx, "bob", task.ID, TaskPatch{Message: &strayMessage}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("message without request-changes error = %v, want ErrInvalid", err)
	}
	badStatus := Status("blocked")
	if _, err := s.UpdateTask(ctx, "bob", task.ID, TaskPatch{Status: &badStatus}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad status error = %v, want ErrInvalid", err)
	}
	badPriority := Priority("critical")
	if _, err := s.UpdateTask(ctx, "bob", task.ID, TaskPatch{Priority: &badPriority}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad priority error = %v, want ErrInvalid", err)
	}
	if _, err := s.UpdateTask(ctx, "bob", "GILIR-99999", TaskPatch{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing task error = %v, want ErrNotFound", err)
	}

	inReview := StatusInReview
	task, err = s.UpdateTask(ctx, "bob", task.ID, TaskPatch{Status: &inReview})
	if err != nil {
		t.Fatalf("set in-review: %v", err)
	}
	todo := StatusTodo
	if _, err := s.UpdateTask(ctx, "bob", task.ID, TaskPatch{Status: &todo}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("in-review -> todo without message error = %v, want ErrInvalid", err)
	}
	reviewMessage := "please add tests"
	task, err = s.UpdateTask(ctx, "bob", task.ID, TaskPatch{Status: &todo, Message: &reviewMessage})
	if err != nil {
		t.Fatalf("return to todo: %v", err)
	}
	if task.Status != StatusTodo {
		t.Fatalf("status = %q, want todo", task.Status)
	}
	foundMarking := false
	for _, marking := range task.Markings {
		if marking == MarkingChangesRequested {
			foundMarking = true
		}
	}
	if !foundMarking {
		t.Fatalf("in-review -> todo did not apply changes-requested: %+v", task.Markings)
	}
	detail = mustShow(t, s, task.ID)
	var statusOld, statusNew any
	for _, activity := range detail.Activity {
		if activity.Type == ActivityTaskStatusChanged && activity.Data["new"] == "todo" {
			statusOld = activity.Data["old"]
			statusNew = activity.Data["new"]
		}
	}
	if statusOld != "in-review" || statusNew != "todo" {
		t.Fatalf("status audit old/new = %#v / %#v", statusOld, statusNew)
	}
	if countActivityType(detail.Activity, ActivityTaskMarking) != 1 {
		t.Fatalf("marking events = %v, want exactly the auto-applied one", activityTypes(detail.Activity))
	}
	if countActivityType(detail.Activity, ActivityTaskCommented) != 1 {
		t.Fatalf("review comment events = %v, want one", activityTypes(detail.Activity))
	}
	assertCommentMessages(t, detail.Activity, "please add tests")
}

func TestReviewAndReworkTransitions(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	project, repo := seedProjectRepo(t, s)
	reviewTask := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: "review me", Status: StatusInReview,
	})
	backlog := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: "backlog",
	})

	if _, err := s.Review(ctx, "carol", backlog.ID, ReviewApprove, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("review backlog error = %v, want ErrConflict", err)
	}
	if _, err := s.Review(ctx, "carol", reviewTask.ID, ReviewDecision("ship"), ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid decision error = %v, want ErrInvalid", err)
	}
	if _, err := s.Review(ctx, "  ", reviewTask.ID, ReviewApprove, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank reviewer error = %v, want ErrInvalid", err)
	}
	if _, err := s.Review(ctx, "carol", "GILIR-99999", ReviewApprove, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing task error = %v, want ErrNotFound", err)
	}

	approved, err := s.Review(ctx, "carol", reviewTask.ID, ReviewApprove, "LGTM")
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if approved.Status != StatusDone {
		t.Fatalf("approved status = %q, want done", approved.Status)
	}
	approvedDetail := mustShow(t, s, reviewTask.ID)
	if countActivityType(approvedDetail.Activity, ActivityTaskStatusChanged) != 1 {
		t.Fatalf("approve events = %v", activityTypes(approvedDetail.Activity))
	}
	if _, err := s.Review(ctx, "carol", reviewTask.ID, ReviewApprove, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("second review error = %v, want ErrConflict", err)
	}
	if after := mustShow(t, s, reviewTask.ID); len(after.Activity) != len(approvedDetail.Activity) {
		t.Fatalf("failed second review wrote activity: %v", activityTypes(after.Activity))
	}

	changesTask := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: "needs work", Status: StatusInReview,
	})
	if _, err := s.Review(ctx, "carol", changesTask.ID, ReviewRequestChanges, "  "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank changes message error = %v, want ErrInvalid", err)
	}
	changes, err := s.Review(ctx, "carol", changesTask.ID, ReviewRequestChanges, "add tests")
	if err != nil {
		t.Fatalf("request changes: %v", err)
	}
	if changes.Status != StatusTodo {
		t.Fatalf("changes status = %q, want todo", changes.Status)
	}
	found := false
	for _, marking := range changes.Markings {
		if marking == MarkingChangesRequested {
			found = true
		}
	}
	if !found {
		t.Fatalf("changes-requested missing: %+v", changes.Markings)
	}
	changesDetail := mustShow(t, s, changesTask.ID)
	var sawReason, sawComment bool
	for _, activity := range changesDetail.Activity {
		if activity.Type == ActivityTaskStatusChanged && activity.Data["reason"] == "changes_requested" {
			sawReason = true
		}
		if activity.Type == ActivityTaskCommented && activity.Data["message"] == "add tests" {
			sawComment = true
		}
	}
	if !sawReason {
		t.Fatalf("request-changes audit missing reason: %+v", changesDetail.Activity)
	}
	if !sawComment {
		t.Fatalf("request-changes did not record a comment: %+v", changesDetail.Activity)
	}
}

func TestMarkingCommentDependencyIdempotency(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	project, repo := seedProjectRepo(t, s)
	taskA := mustCreateTask(t, s, CreateTaskParams{ProjectID: project.ID, RepoID: repo.ID, Title: "A"})
	taskB := mustCreateTask(t, s, CreateTaskParams{ProjectID: project.ID, RepoID: repo.ID, Title: "B"})
	taskC := mustCreateTask(t, s, CreateTaskParams{ProjectID: project.ID, RepoID: repo.ID, Title: "C"})
	taskD := mustCreateTask(t, s, CreateTaskParams{ProjectID: project.ID, RepoID: repo.ID, Title: "D"})

	if _, err := s.SetMarking(ctx, "alice", taskA.ID, "  ", true); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank marking error = %v, want ErrInvalid", err)
	}
	if _, err := s.SetMarking(ctx, "alice", "GILIR-99999", "docs", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing marking task error = %v, want ErrNotFound", err)
	}
	marked, err := s.SetMarking(ctx, "alice", taskA.ID, "docs", true)
	if err != nil {
		t.Fatalf("add marking: %v", err)
	}
	markedAt := marked.UpdatedAt
	again, err := s.SetMarking(ctx, "alice", taskA.ID, "docs", true)
	if err != nil {
		t.Fatalf("duplicate marking: %v", err)
	}
	if !again.UpdatedAt.Equal(markedAt) {
		t.Fatalf("duplicate marking changed updated_at")
	}
	detail := mustShow(t, s, taskA.ID)
	if countActivityType(detail.Activity, ActivityTaskMarking) != 1 {
		t.Fatalf("duplicate marking events = %v", activityTypes(detail.Activity))
	}
	var markingEvent Activity
	for _, activity := range detail.Activity {
		if activity.Type == ActivityTaskMarking {
			markingEvent = activity
		}
	}
	if markingEvent.Data["old"] != false || markingEvent.Data["new"] != true || markingEvent.Data["marking"] != "docs" {
		t.Fatalf("marking event data = %+v", markingEvent.Data)
	}
	if _, err := s.SetMarking(ctx, "alice", taskA.ID, "docs", false); err != nil {
		t.Fatalf("remove marking: %v", err)
	}
	afterRemove := mustShow(t, s, taskA.ID)
	if _, err := s.SetMarking(ctx, "alice", taskA.ID, "docs", false); err != nil {
		t.Fatalf("duplicate remove marking: %v", err)
	}
	afterDuplicateRemove := mustShow(t, s, taskA.ID)
	if len(afterRemove.Activity) != len(afterDuplicateRemove.Activity) {
		t.Fatalf("duplicate marking removal wrote activity: %v", activityTypes(afterDuplicateRemove.Activity))
	}
	if countActivityType(afterRemove.Activity, ActivityTaskMarking) != 2 {
		t.Fatalf("marking add/remove events = %v", activityTypes(afterRemove.Activity))
	}

	if _, err := s.Comment(ctx, "alice", taskA.ID, "  "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank comment error = %v, want ErrInvalid", err)
	}
	if _, err := s.Comment(ctx, "alice", "GILIR-99999", "hello"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing comment task error = %v, want ErrNotFound", err)
	}
	comment, err := s.Comment(ctx, "dave", taskA.ID, "please add a test")
	if err != nil {
		t.Fatalf("Comment: %v", err)
	}
	if comment.Type != ActivityTaskCommented || comment.Actor != "dave" ||
		comment.Data["message"] != "please add a test" || comment.TaskID == nil || *comment.TaskID != taskA.ID {
		t.Fatalf("comment activity = %+v", comment)
	}

	if err := s.SetDependency(ctx, "alice", taskA.ID, taskA.ID, true); !errors.Is(err, ErrInvalid) {
		t.Fatalf("self dependency error = %v, want ErrInvalid", err)
	}
	if err := s.SetDependency(ctx, "alice", "GILIR-99999", taskB.ID, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing dependent error = %v, want ErrNotFound", err)
	}
	if err := s.SetDependency(ctx, "alice", taskA.ID, "GILIR-99999", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing blocker error = %v, want ErrNotFound", err)
	}

	if err := s.SetDependency(ctx, "alice", taskA.ID, taskB.ID, true); err != nil {
		t.Fatalf("add A->B: %v", err)
	}
	aShow := mustShow(t, s, taskA.ID)
	aUpdatedAt := aShow.Task.UpdatedAt
	if err := s.SetDependency(ctx, "alice", taskA.ID, taskB.ID, true); err != nil {
		t.Fatalf("duplicate A->B: %v", err)
	}
	duplicateShow := mustShow(t, s, taskA.ID)
	if !duplicateShow.Task.UpdatedAt.Equal(aUpdatedAt) {
		t.Fatal("duplicate dependency add changed updated_at")
	}
	if countActivityType(duplicateShow.Activity, ActivityDependencyAdded) != 1 {
		t.Fatalf("duplicate dependency events = %v", activityTypes(duplicateShow.Activity))
	}
	if len(aShow.Blockers) != 1 || aShow.Blockers[0] != taskB.ID {
		t.Fatalf("blockers = %+v, want [%s]", aShow.Blockers, taskB.ID)
	}

	if err := s.SetDependency(ctx, "alice", taskB.ID, taskC.ID, true); err != nil {
		t.Fatalf("add B->C: %v", err)
	}
	if err := s.SetDependency(ctx, "alice", taskC.ID, taskD.ID, true); err != nil {
		t.Fatalf("add C->D: %v", err)
	}
	if err := s.SetDependency(ctx, "alice", taskD.ID, taskA.ID, true); !errors.Is(err, ErrConflict) {
		t.Fatalf("cycle error = %v, want ErrConflict", err)
	}
	graph := mustShow(t, s, taskD.ID)
	if len(graph.Blockers) != 0 {
		t.Fatalf("failed cycle add mutated graph: blockers = %+v", graph.Blockers)
	}
	if countActivityType(graph.Activity, ActivityDependencyAdded) != 0 {
		t.Fatalf("failed cycle add wrote activity: %v", activityTypes(graph.Activity))
	}

	if err := s.SetDependency(ctx, "alice", taskA.ID, taskB.ID, false); err != nil {
		t.Fatalf("remove A->B: %v", err)
	}
	removed := mustShow(t, s, taskA.ID)
	if err := s.SetDependency(ctx, "alice", taskA.ID, taskB.ID, false); err != nil {
		t.Fatalf("duplicate remove A->B: %v", err)
	}
	removedAgain := mustShow(t, s, taskA.ID)
	if len(removed.Activity) != len(removedAgain.Activity) {
		t.Fatal("duplicate dependency remove wrote activity")
	}
	if countActivityType(removed.Activity, ActivityDependencyRemoved) != 1 {
		t.Fatalf("dependency remove events = %v", activityTypes(removed.Activity))
	}
	if len(removed.Blockers) != 0 {
		t.Fatalf("blockers after remove = %+v", removed.Blockers)
	}
}

func TestBlankActorsRejectedEverywhere(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	project, repo := seedProjectRepo(t, s)
	task := mustCreateTask(t, s, CreateTaskParams{ProjectID: project.ID, RepoID: repo.ID, Title: "task"})
	paused := true
	title := "title"

	checks := []struct {
		name string
		call func() error
	}{
		{"CreateProject", func() error { _, err := s.CreateProject(ctx, " ", "x"); return err }},
		{"RenameProject", func() error { _, err := s.RenameProject(ctx, " ", project.ID, "x"); return err }},
		{"DeleteProject", func() error { return s.DeleteProject(ctx, " ", project.ID) }},
		{"AddRepo", func() error { _, err := s.AddRepo(ctx, " ", project.ID, repo.Path); return err }},
		{"RemoveRepo", func() error { return s.RemoveRepo(ctx, " ", repo.ID) }},
		{"CreateTask", func() error {
			_, err := s.CreateTask(ctx, " ", CreateTaskParams{ProjectID: project.ID, RepoID: repo.ID, Title: "x"})
			return err
		}},
		{"UpdateTask", func() error {
			_, err := s.UpdateTask(ctx, " ", task.ID, TaskPatch{Title: &title})
			return err
		}},
		{"DeleteTask", func() error { return s.DeleteTask(ctx, " ", task.ID) }},
		{"SetMarking", func() error { _, err := s.SetMarking(ctx, " ", task.ID, "x", true); return err }},
		{"SetDependency", func() error { return s.SetDependency(ctx, " ", task.ID, "GILIR-99999", true) }},
		{"Comment", func() error { _, err := s.Comment(ctx, " ", task.ID, "x"); return err }},
		{"EditComment", func() error { _, err := s.EditComment(ctx, " ", task.ID, 1, "x"); return err }},
		{"RemoveComment", func() error { _, err := s.RemoveComment(ctx, " ", task.ID, 1); return err }},
		{"Review", func() error { _, err := s.Review(ctx, " ", task.ID, ReviewApprove, ""); return err }},
		{"UpdateQueueSettings", func() error {
			_, err := s.UpdateQueueSettings(ctx, " ", QueueSettingsPatch{Paused: &paused})
			return err
		}},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.call(); !errors.Is(err, ErrInvalid) {
				t.Fatalf("error = %v, want ErrInvalid", err)
			}
		})
	}

	if _, err := s.QueueSettings(ctx); err != nil {
		t.Fatalf("QueueSettings: %v", err)
	}
	settings, err := s.UpdateQueueSettings(ctx, "alice", QueueSettingsPatch{Paused: &paused})
	if err != nil {
		t.Fatalf("pause queue: %v", err)
	}
	if !settings.Paused {
		t.Fatalf("settings = %+v", settings)
	}
	zero := 0
	if _, err := s.UpdateQueueSettings(ctx, "alice", QueueSettingsPatch{MaxConcurrency: &zero}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero concurrency error = %v, want ErrInvalid", err)
	}
	negative := -3
	if _, err := s.UpdateQueueSettings(ctx, "alice", QueueSettingsPatch{MaxConcurrency: &negative}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("negative concurrency error = %v, want ErrInvalid", err)
	}
	same, err := s.UpdateQueueSettings(ctx, "alice", QueueSettingsPatch{Paused: &paused})
	if err != nil {
		t.Fatalf("no-op queue update: %v", err)
	}
	if !same.UpdatedAt.Equal(settings.UpdatedAt) {
		t.Fatal("no-op queue update changed updated_at")
	}
	var queueEvents int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM activity WHERE type = 'queue.updated'`).Scan(&queueEvents); err != nil {
		t.Fatalf("count queue events: %v", err)
	}
	if queueEvents != 1 {
		t.Fatalf("queue.updated events = %d, want 1", queueEvents)
	}
}

func TestTaskIDsArePrefixedAndOrderedBySequence(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	project, repo := seedProjectRepo(t, s)

	var ids []string
	for i := 0; i < 12; i++ {
		task := mustCreateTask(t, s, CreateTaskParams{
			ProjectID: project.ID, RepoID: repo.ID, Title: "task",
		})
		ids = append(ids, task.ID)
	}
	if ids[0] != "GILIR-1" || ids[9] != "GILIR-10" || ids[11] != "GILIR-12" {
		t.Fatalf("task IDs = %v, want GILIR-1 .. GILIR-12", ids)
	}

	listed, err := s.ListTasks(ctx, TaskFilter{ProjectID: project.ID})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(listed) != len(ids) {
		t.Fatalf("listed %d tasks, want %d", len(listed), len(ids))
	}
	for i := range listed {
		if listed[i].ID != ids[i] {
			t.Fatalf("ListTasks[%d].ID = %s, want %s (creation order, not lexicographic)", i, listed[i].ID, ids[i])
		}
	}

	// A deleted ID is never reused, so old references stay unambiguous.
	if err := s.DeleteTask(ctx, "alice", "GILIR-12"); err != nil {
		t.Fatalf("DeleteTask: %v", err)
	}
	next := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: "after delete",
	})
	if next.ID != "GILIR-13" {
		t.Fatalf("task ID after delete = %s, want GILIR-13", next.ID)
	}

	// Blockers and activity carry the same prefixed text ID.
	dependent := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: "dependent",
	})
	if err := s.SetDependency(ctx, "alice", dependent.ID, next.ID, true); err != nil {
		t.Fatalf("SetDependency: %v", err)
	}
	detail := mustShow(t, s, dependent.ID)
	if len(detail.Blockers) != 1 || detail.Blockers[0] != next.ID {
		t.Fatalf("blockers = %v, want [%s]", detail.Blockers, next.ID)
	}
	for _, activity := range detail.Activity {
		if activity.TaskID == nil || *activity.TaskID != dependent.ID {
			t.Fatalf("activity task_id = %v, want %s", activity.TaskID, dependent.ID)
		}
	}

	var stored string
	if err := s.db.QueryRowContext(ctx,
		`SELECT id FROM tasks WHERE seq = 1`,
	).Scan(&stored); err != nil {
		t.Fatalf("read stored task id: %v", err)
	}
	if stored != "GILIR-1" {
		t.Fatalf("stored task id = %q, want GILIR-1 (prefix must be persisted)", stored)
	}
}

func TestTaskIDLookupsAreCaseInsensitiveAndRejectBareNumbers(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	project, repo := seedProjectRepo(t, s)
	task := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: "case",
	})

	detail, err := s.Show(ctx, "gilir-1")
	if err != nil {
		t.Fatalf("Show lower-case: %v", err)
	}
	if detail.Task.ID != task.ID {
		t.Fatalf("lower-case Show returned %s, want %s", detail.Task.ID, task.ID)
	}
	if _, err := s.Show(ctx, "1"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bare numeric Show error = %v, want ErrInvalid", err)
	}
	if _, err := s.Show(ctx, "GILIR-01"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero-padded Show error = %v, want ErrInvalid", err)
	}
	if err := s.SetDependency(ctx, "alice", "gilir-1", "gilir-1", true); !errors.Is(err, ErrInvalid) {
		t.Fatalf("lower-case self dependency error = %v, want ErrInvalid", err)
	}
}

// assertCommentMessages checks that the comment records carry the given prose in
// order under the message key. Block, unblock and review all funnel their prose
// into comment records, so this is where their wording is pinned.
func assertCommentMessages(t *testing.T, activities []Activity, want ...string) {
	t.Helper()
	got := []string{}
	for _, activity := range activities {
		if activity.Type != ActivityTaskCommented {
			continue
		}
		if _, ok := activity.Data["body"]; ok {
			t.Fatalf("comment data carries body: %#v", activity.Data)
		}
		message, _ := activity.Data["message"].(string)
		got = append(got, message)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("comment messages = %v, want %v", got, want)
	}
}
