package store

import (
	"context"
	"encoding/json"
	"errors"
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
		ProjectID:   project.ID,
		RepoID:      repo.ID,
		Title:       "  first task  ",
		Description: "",
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
	if task.ID <= 0 || task.CreatedAt.IsZero() || task.UpdatedAt.IsZero() {
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
		"id", "project_id", "repo_id", "title", "description", "status",
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
	if second.ID <= task.ID {
		t.Fatalf("task IDs not FIFO: %d then %d", task.ID, second.ID)
	}
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
		want   []int64
	}{
		{"project", TaskFilter{ProjectID: projectA.ID}, []int64{backlog.ID, urgent.ID, progress.ID}},
		{"repo", TaskFilter{RepoID: repoB.ID}, []int64{other.ID}},
		{"status", TaskFilter{Status: StatusTodo}, []int64{urgent.ID, other.ID}},
		{"priority urgent", TaskFilter{Priority: PriorityUrgent}, []int64{urgent.ID}},
		{"marking normalized", TaskFilter{Marking: " docs "}, []int64{backlog.ID, other.ID}},
		{"combined", TaskFilter{ProjectID: projectA.ID, Status: StatusTodo, Marking: "docs"}, []int64{}},
		{"project and priority", TaskFilter{ProjectID: projectA.ID, Priority: PriorityLow}, []int64{backlog.ID}},
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
					t.Fatalf("task[%d].ID = %d, want %d (tasks=%+v)", i, tasks[i].ID, id, tasks)
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

func TestUpdateTaskPatchNoOpAndAudit(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	project, repo := seedProjectRepo(t, s)
	otherRepo := mustAddRepo(t, s, project.ID, newGitRepo(t))
	task := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: "original", Description: "body",
	})
	originalDetail := mustShow(t, s, task.ID)
	originalUpdatedAt := task.UpdatedAt

	sameTitle := task.Title
	sameDescription := task.Description
	samePriority := task.Priority
	sameStatus := task.Status
	sameRepo := task.RepoID
	unchanged, err := s.UpdateTask(ctx, "bob", task.ID, TaskPatch{
		Title:       &sameTitle,
		Description: &sameDescription,
		Priority:    &samePriority,
		Status:      &sameStatus,
		RepoID:      &sameRepo,
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
	emptyDescription := ""
	priority := PriorityUrgent
	updated, err := s.UpdateTask(ctx, "bob", task.ID, TaskPatch{
		Title:       &newTitle,
		Description: &emptyDescription,
		Priority:    &priority,
		RepoID:      &otherRepo.ID,
	})
	if err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}
	if updated.Title != "renamed task" || updated.Description != "" ||
		updated.Priority != PriorityUrgent || updated.RepoID != otherRepo.ID {
		t.Fatalf("updated task = %+v", updated)
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
	if oldFields["description"] != "body" || newFields["description"] != "" {
		t.Fatalf("description audit old/new = %#v / %#v", oldFields["description"], newFields["description"])
	}
	if oldFields["priority"] != "medium" || newFields["priority"] != "urgent" {
		t.Fatalf("priority audit old/new = %#v / %#v", oldFields["priority"], newFields["priority"])
	}
	if oldFields["repo_id"] != float64(repo.ID) || newFields["repo_id"] != float64(otherRepo.ID) {
		t.Fatalf("repo audit old/new = %#v / %#v", oldFields["repo_id"], newFields["repo_id"])
	}

	beforeLength := len(detail.Activity)
	if _, err := s.UpdateTask(ctx, "bob", task.ID, TaskPatch{Description: &emptyDescription}); err != nil {
		t.Fatalf("no-op empty description: %v", err)
	}
	if after := mustShow(t, s, task.ID); len(after.Activity) != beforeLength {
		t.Fatalf("explicit empty no-op wrote activity: %v", activityTypes(after.Activity))
	}

	blank := "  "
	if _, err := s.UpdateTask(ctx, "bob", task.ID, TaskPatch{Title: &blank}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank title error = %v, want ErrInvalid", err)
	}
	badStatus := Status("blocked")
	if _, err := s.UpdateTask(ctx, "bob", task.ID, TaskPatch{Status: &badStatus}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad status error = %v, want ErrInvalid", err)
	}
	badPriority := Priority("critical")
	if _, err := s.UpdateTask(ctx, "bob", task.ID, TaskPatch{Priority: &badPriority}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad priority error = %v, want ErrInvalid", err)
	}
	if _, err := s.UpdateTask(ctx, "bob", 99999, TaskPatch{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing task error = %v, want ErrNotFound", err)
	}

	inReview := StatusInReview
	task, err = s.UpdateTask(ctx, "bob", task.ID, TaskPatch{Status: &inReview})
	if err != nil {
		t.Fatalf("set in-review: %v", err)
	}
	todo := StatusTodo
	task, err = s.UpdateTask(ctx, "bob", task.ID, TaskPatch{Status: &todo})
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
	if _, err := s.Review(ctx, "carol", 99999, ReviewApprove, ""); !errors.Is(err, ErrNotFound) {
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
	var sawReason bool
	for _, activity := range changesDetail.Activity {
		if activity.Type == ActivityTaskStatusChanged && activity.Data["reason"] == "changes_requested" && activity.Data["detail"] == "add tests" {
			sawReason = true
		}
	}
	if !sawReason {
		t.Fatalf("request-changes audit missing reason/detail: %+v", changesDetail.Activity)
	}

	auto := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: "auto changes", Status: StatusInReview,
	})
	todo := StatusTodo
	auto, err = s.UpdateTask(ctx, "carol", auto.ID, TaskPatch{Status: &todo})
	if err != nil {
		t.Fatalf("UpdateTask in-review -> todo: %v", err)
	}
	if len(auto.Markings) != 1 || auto.Markings[0] != MarkingChangesRequested {
		t.Fatalf("auto markings = %+v, want changes-requested", auto.Markings)
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
	if _, err := s.SetMarking(ctx, "alice", 99999, "docs", true); !errors.Is(err, ErrNotFound) {
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
	if _, err := s.Comment(ctx, "alice", 99999, "hello"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing comment task error = %v, want ErrNotFound", err)
	}
	comment, err := s.Comment(ctx, "dave", taskA.ID, "please add a test")
	if err != nil {
		t.Fatalf("Comment: %v", err)
	}
	if comment.Type != ActivityTaskCommented || comment.Actor != "dave" ||
		comment.Data["body"] != "please add a test" || comment.TaskID == nil || *comment.TaskID != taskA.ID {
		t.Fatalf("comment activity = %+v", comment)
	}

	if err := s.SetDependency(ctx, "alice", taskA.ID, taskA.ID, true); !errors.Is(err, ErrInvalid) {
		t.Fatalf("self dependency error = %v, want ErrInvalid", err)
	}
	if err := s.SetDependency(ctx, "alice", 99999, taskB.ID, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing dependent error = %v, want ErrNotFound", err)
	}
	if err := s.SetDependency(ctx, "alice", taskA.ID, 99999, true); !errors.Is(err, ErrNotFound) {
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
		t.Fatalf("blockers = %+v, want [%d]", aShow.Blockers, taskB.ID)
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
		{"SetMarking", func() error { _, err := s.SetMarking(ctx, " ", task.ID, "x", true); return err }},
		{"SetDependency", func() error { return s.SetDependency(ctx, " ", task.ID, task.ID+1, true) }},
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

func TestPriorityEnumIncludesUrgent(t *testing.T) {
	for _, priority := range []Priority{PriorityLow, PriorityMedium, PriorityHigh, PriorityUrgent} {
		if !priority.Valid() {
			t.Fatalf("%q should be valid", priority)
		}
	}
	if Priority("critical").Valid() {
		t.Fatal("critical should not be a valid priority")
	}
	encoded, err := json.Marshal(PriorityUrgent)
	if err != nil || string(encoded) != `"urgent"` {
		t.Fatalf("urgent JSON = %s, %v", encoded, err)
	}
	if _, ok := jsonObject(t, Task{Priority: PriorityUrgent})["priority"]; !ok {
		t.Fatal("task JSON missing priority")
	}
}
