package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/armedi/gilir/internal/store"
)

type fixture struct {
	service *Service
	store   *store.Store
	project int64
	repo    int64
	task    int64
}

func newFixture(t *testing.T, status func() Snapshot) *fixture {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "gilir.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	root := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("create repo tree: %v", err)
	}
	if output, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}

	service := New(db, status)
	project, err := service.Execute(ctx, "project.create", "alice", mustJSON(t, map[string]any{"name": "Gilir"}))
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	repo, err := service.Execute(ctx, "repo.add", "alice", mustJSON(t, map[string]any{
		"project_id": project.(store.Project).ID,
		"path":       root,
	}))
	if err != nil {
		t.Fatalf("add repo: %v", err)
	}
	task, err := service.Execute(ctx, "task.create", "alice", mustJSON(t, map[string]any{
		"project_id": project.(store.Project).ID,
		"repo_id":    repo.(store.Repo).ID,
		"title":      "Ship commands",
	}))
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	return &fixture{
		service: service,
		store:   db,
		project: project.(store.Project).ID,
		repo:    repo.(store.Repo).ID,
		task:    task.(store.Task).ID,
	}
}

func (f *fixture) run(t *testing.T, command string, args map[string]any) (any, error) {
	t.Helper()
	return f.service.Execute(context.Background(), command, "alice", mustJSON(t, args))
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode args: %v", err)
	}
	return encoded
}

func requireInvalid(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
	if status, code := Classify(err); status != 400 || code != "invalid" {
		t.Fatalf("classify = (%d, %q), want (400, invalid)", status, code)
	}
}

func TestOmittedBooleansDoNotMeanFalse(t *testing.T) {
	f := newFixture(t, nil)

	if _, err := f.run(t, "task.mark", map[string]any{"id": f.task, "marking": "triage"}); err == nil {
		t.Fatal("task.mark without present was accepted")
	} else {
		requireInvalid(t, err)
	}
	if _, err := f.run(t, "task.depend", map[string]any{"id": f.task, "blocker_id": f.task}); err == nil {
		t.Fatal("task.depend without present was accepted")
	} else {
		requireInvalid(t, err)
	}
	if _, err := f.run(t, "task.review", map[string]any{"id": f.task}); err == nil {
		t.Fatal("task.review without approve was accepted")
	} else {
		requireInvalid(t, err)
	}

	marked, err := f.run(t, "task.mark", map[string]any{"id": f.task, "marking": "triage", "present": false})
	if err != nil {
		t.Fatalf("mark present false: %v", err)
	}
	if len(marked.(store.Task).Markings) != 0 {
		t.Fatalf("markings = %v, want none", marked.(store.Task).Markings)
	}
	marked, err = f.run(t, "task.mark", map[string]any{"id": f.task, "marking": "triage", "present": true})
	if err != nil {
		t.Fatalf("mark present true: %v", err)
	}
	if got := marked.(store.Task).Markings; len(got) != 1 || got[0] != "triage" {
		t.Fatalf("markings = %v, want [triage]", got)
	}
}

func TestTaskListRepoFilterMustBePositiveWhenPresent(t *testing.T) {
	f := newFixture(t, nil)

	listed, err := f.run(t, "task.list", map[string]any{"project_id": f.project})
	if err != nil {
		t.Fatalf("list without repo: %v", err)
	}
	if got := len(listed.([]store.Task)); got != 1 {
		t.Fatalf("tasks = %d, want 1", got)
	}
	listed, err = f.run(t, "task.list", map[string]any{"project_id": f.project, "repo_id": f.repo})
	if err != nil {
		t.Fatalf("list with repo: %v", err)
	}
	if got := len(listed.([]store.Task)); got != 1 {
		t.Fatalf("tasks = %d, want 1", got)
	}
	if _, err := f.run(t, "task.list", map[string]any{"project_id": f.project, "repo_id": 0}); err == nil {
		t.Fatal("explicit zero repo_id was treated as omitted")
	} else {
		requireInvalid(t, err)
	}
}

func TestReviewFalseRequestsChanges(t *testing.T) {
	f := newFixture(t, nil)

	if _, err := f.run(t, "task.update", map[string]any{"id": f.task, "status": "in-review"}); err != nil {
		t.Fatalf("move to in-review: %v", err)
	}
	reviewed, err := f.run(t, "task.review", map[string]any{"id": f.task, "approve": false})
	if err != nil {
		t.Fatalf("request changes: %v", err)
	}
	task := reviewed.(store.Task)
	if task.Status != store.StatusTodo {
		t.Fatalf("status = %q, want todo", task.Status)
	}
	found := false
	for _, marking := range task.Markings {
		if marking == store.MarkingChangesRequested {
			found = true
		}
	}
	if !found {
		t.Fatalf("markings = %v, want changes-requested", task.Markings)
	}
}

func TestTaskUpdateOmitsUnspecifiedFields(t *testing.T) {
	f := newFixture(t, nil)

	updated, err := f.run(t, "task.update", map[string]any{"id": f.task, "title": "Renamed"})
	if err != nil {
		t.Fatalf("update title: %v", err)
	}
	task := updated.(store.Task)
	if task.Title != "Renamed" {
		t.Fatalf("title = %q, want Renamed", task.Title)
	}
	if task.Status != store.StatusBacklog || task.Priority != store.PriorityMedium {
		t.Fatalf("status/priority = %q/%q, want backlog/medium", task.Status, task.Priority)
	}
	if task.RepoID != f.repo {
		t.Fatalf("repo_id = %d, want %d", task.RepoID, f.repo)
	}
}

func TestStrictArgsRejectUnknownAndMissingFields(t *testing.T) {
	f := newFixture(t, nil)

	if _, err := f.run(t, "project.create", map[string]any{"name": "Other", "extra": true}); err == nil {
		t.Fatal("unknown field was accepted")
	} else {
		requireInvalid(t, err)
	}
	if _, err := f.run(t, "task.list", map[string]any{"project_id": 0}); err == nil {
		t.Fatal("nonpositive project_id was accepted")
	} else {
		requireInvalid(t, err)
	}
	if _, err := f.run(t, "task.mark", map[string]any{"id": f.task, "marking": "triage", "present": false, "extra": 1}); err == nil {
		t.Fatal("unknown field on task.mark was accepted")
	} else {
		requireInvalid(t, err)
	}
	if _, err := f.service.Execute(context.Background(), "daemon.run", "alice", mustJSON(t, map[string]any{})); err == nil {
		t.Fatal("daemon.run was accepted")
	} else {
		requireInvalid(t, err)
	}
}

func TestDaemonStatusShape(t *testing.T) {
	f := newFixture(t, func() Snapshot {
		return Snapshot{ActiveParents: 3, WatcherError: "watcher crashed"}
	})

	result, err := f.run(t, "daemon.status", map[string]any{})
	if err != nil {
		t.Fatalf("daemon.status: %v", err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("encode status: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if decoded["running"] != true {
		t.Fatalf("running = %v, want true", decoded["running"])
	}
	if decoded["paused"] != false {
		t.Fatalf("paused = %v, want false", decoded["paused"])
	}
	if decoded["max_concurrency"] != float64(1) {
		t.Fatalf("max_concurrency = %v, want 1", decoded["max_concurrency"])
	}
	if decoded["active_parents"] != float64(3) {
		t.Fatalf("active_parents = %v, want 3", decoded["active_parents"])
	}
	if decoded["watcher_error"] != "watcher crashed" {
		t.Fatalf("watcher_error = %v", decoded["watcher_error"])
	}

	_, err = f.run(t, "daemon.update", map[string]any{"paused": true, "max_concurrency": 2})
	if err != nil {
		t.Fatalf("daemon.update: %v", err)
	}
	result, err = f.run(t, "daemon.status", map[string]any{})
	if err != nil {
		t.Fatalf("daemon.status after update: %v", err)
	}
	encoded, err = json.Marshal(result)
	if err != nil {
		t.Fatalf("encode status: %v", err)
	}
	decoded = nil
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if decoded["paused"] != true || decoded["max_concurrency"] != float64(2) {
		t.Fatalf("status after update = %v", decoded)
	}
}

func TestDaemonStatusWithoutProviderReportsNullParents(t *testing.T) {
	f := newFixture(t, nil)

	result, err := f.run(t, "daemon.status", map[string]any{})
	if err != nil {
		t.Fatalf("daemon.status: %v", err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("encode status: %v", err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if string(decoded["active_parents"]) != "null" {
		t.Fatalf("active_parents = %s, want null", decoded["active_parents"])
	}
	if _, present := decoded["watcher_error"]; present {
		t.Fatalf("watcher_error = %s, want omitted", decoded["watcher_error"])
	}
}

func TestPausedFalseIsApplied(t *testing.T) {
	f := newFixture(t, nil)

	result, err := f.run(t, "daemon.update", map[string]any{"paused": true})
	if err != nil {
		t.Fatalf("pause: %v", err)
	}
	if !result.(store.QueueSettings).Paused {
		t.Fatal("paused = false after pause request")
	}
	result, err = f.run(t, "daemon.update", map[string]any{"paused": false})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if result.(store.QueueSettings).Paused {
		t.Fatal("paused = true after resume request")
	}
	if _, err := f.run(t, "daemon.update", map[string]any{}); err != nil {
		t.Fatalf("empty update: %v", err)
	}
}

func TestVoidMutationsReturnOK(t *testing.T) {
	f := newFixture(t, nil)

	other, err := f.run(t, "task.create", map[string]any{
		"project_id": f.project,
		"repo_id":    f.repo,
		"title":      "Blocker",
	})
	if err != nil {
		t.Fatalf("create blocker: %v", err)
	}
	otherID := other.(store.Task).ID

	result, err := f.run(t, "task.depend", map[string]any{"id": f.task, "blocker_id": otherID, "present": true})
	if err != nil {
		t.Fatalf("depend: %v", err)
	}
	if result.(okResult).OK != true {
		t.Fatalf("depend result = %#v, want ok true", result)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("encode result: %v", err)
	}
	if string(encoded) != `{"ok":true}` {
		t.Fatalf("dependency result JSON = %s, want {\"ok\":true}", encoded)
	}

	detail, err := f.run(t, "task.show", map[string]any{"id": f.task})
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if got := detail.(store.TaskDetail).Blockers; len(got) != 1 || got[0] != otherID {
		t.Fatalf("blockers = %v, want [%d]", got, otherID)
	}

	comment, err := f.run(t, "task.comment", map[string]any{"id": f.task, "body": "Progress: **done**"})
	if err != nil {
		t.Fatalf("comment: %v", err)
	}
	if comment.(store.Activity).Type != store.ActivityTaskCommented {
		t.Fatalf("comment type = %q", comment.(store.Activity).Type)
	}

	removed, err := f.run(t, "repo.remove", map[string]any{"id": f.repo})
	if err == nil {
		t.Fatalf("repo.remove with dependent task returned %v, want conflict", removed)
	} else if status, code := Classify(err); status != 409 || code != "conflict" {
		t.Fatalf("classify = (%d, %q), want (409, conflict)", status, code)
	}

	if _, err := f.run(t, "project.remove", map[string]any{"id": f.project}); err == nil {
		t.Fatal("project.remove with dependent work was accepted")
	}

	empty, err := f.run(t, "project.create", map[string]any{"name": "Empty"})
	if err != nil {
		t.Fatalf("create empty project: %v", err)
	}
	result, err = f.run(t, "project.remove", map[string]any{"id": empty.(store.Project).ID})
	if err != nil {
		t.Fatalf("remove empty project: %v", err)
	}
	encoded, err = json.Marshal(result)
	if err != nil {
		t.Fatalf("encode result: %v", err)
	}
	if string(encoded) != `{"ok":true}` {
		t.Fatalf("project.remove result JSON = %s, want {\"ok\":true}", encoded)
	}
}

func TestUnknownCommandAndMissingRecordClassification(t *testing.T) {
	f := newFixture(t, nil)

	if _, err := f.run(t, "task.show", map[string]any{"id": 999}); err == nil {
		t.Fatal("missing task was accepted")
	} else if status, code := Classify(err); status != 404 || code != "not_found" {
		t.Fatalf("classify = (%d, %q), want (404, not_found)", status, code)
	}

	if _, err := f.run(t, "task.create", map[string]any{
		"project_id": f.project,
		"repo_id":    f.repo,
		"title":      "   ",
	}); err == nil {
		t.Fatal("blank title was accepted")
	} else {
		requireInvalid(t, err)
	}

	if _, err := f.run(t, "task.list", map[string]any{"project_id": f.project, "status": "bogus"}); err == nil {
		t.Fatal("bogus status filter was accepted")
	} else {
		requireInvalid(t, err)
	}

	if _, err := f.service.Execute(context.Background(), "bogus.command", "alice", json.RawMessage(`{}`)); err == nil {
		t.Fatal("unknown command was accepted")
	} else if !strings.Contains(err.Error(), "bogus.command") {
		t.Fatalf("unknown command error = %v", err)
	}
}
