package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	parents := 3
	f := newFixture(t, func() Snapshot {
		return Snapshot{ActiveParents: &parents, WatcherError: "watcher crashed"}
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

func TestDaemonStatusUnknownHealthStaysNull(t *testing.T) {
	f := newFixture(t, func() Snapshot {
		return Snapshot{ActiveParents: nil, WatcherError: "watcher crashed"}
	})

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
		t.Fatalf("active_parents = %s, want null when watcher health is unknown", decoded["active_parents"])
	}
	if string(decoded["watcher_error"]) != `"watcher crashed"` {
		t.Fatalf("watcher_error = %s", decoded["watcher_error"])
	}
	if string(decoded["running"]) != "true" || string(decoded["paused"]) != "false" {
		t.Fatalf("status = %s", encoded)
	}
}

func TestDaemonStatusZeroParentsIsNotUnknown(t *testing.T) {
	parents := 0
	f := newFixture(t, func() Snapshot {
		return Snapshot{ActiveParents: &parents}
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
	if decoded["active_parents"] != float64(0) {
		t.Fatalf("active_parents = %v, want 0", decoded["active_parents"])
	}
	if _, present := decoded["watcher_error"]; present {
		t.Fatalf("watcher_error = %v, want omitted", decoded["watcher_error"])
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

func newGitRepo(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("create repo tree: %v", err)
	}
	if output, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	return root
}

func projectNames(t *testing.T, f *fixture) map[int64]string {
	t.Helper()
	listed, err := f.run(t, "project.list", map[string]any{})
	if err != nil {
		t.Fatalf("project.list: %v", err)
	}
	names := make(map[int64]string)
	for _, project := range listed.([]store.Project) {
		names[project.ID] = project.Name
	}
	return names
}

func requireProjectIDs(t *testing.T, repo store.Repo, want ...int64) {
	t.Helper()
	if len(repo.ProjectIDs) != len(want) {
		t.Fatalf("repo %d project_ids = %v, want %v", repo.ID, repo.ProjectIDs, want)
	}
	for i := range want {
		if repo.ProjectIDs[i] != want[i] {
			t.Fatalf("repo %d project_ids = %v, want %v", repo.ID, repo.ProjectIDs, want)
		}
	}
}

func TestRepoListGlobalAndProjectScoped(t *testing.T) {
	f := newFixture(t, nil)
	second := newGitRepo(t)
	added, err := f.run(t, "repo.add", map[string]any{"project_id": f.project, "path": second})
	if err != nil {
		t.Fatalf("add second repo: %v", err)
	}
	secondID := added.(store.Repo).ID

	global, err := f.run(t, "repo.list", map[string]any{})
	if err != nil {
		t.Fatalf("repo.list without project_id: %v", err)
	}
	repos := global.([]store.Repo)
	if len(repos) != 2 || repos[0].ID != f.repo || repos[1].ID != secondID {
		t.Fatalf("global repos = %+v, want [%d %d]", repos, f.repo, secondID)
	}
	requireProjectIDs(t, repos[0], f.project)
	requireProjectIDs(t, repos[1], f.project)

	scoped, err := f.run(t, "repo.list", map[string]any{"project_id": f.project})
	if err != nil {
		t.Fatalf("repo.list with project_id: %v", err)
	}
	if got := len(scoped.([]store.Repo)); got != 2 {
		t.Fatalf("project repos = %d, want 2", got)
	}

	empty, err := f.run(t, "project.create", map[string]any{"name": "Empty"})
	if err != nil {
		t.Fatalf("create empty project: %v", err)
	}
	emptyID := empty.(store.Project).ID
	scoped, err = f.run(t, "repo.list", map[string]any{"project_id": emptyID})
	if err != nil {
		t.Fatalf("repo.list for empty project: %v", err)
	}
	if got := len(scoped.([]store.Repo)); got != 0 {
		t.Fatalf("empty project repos = %d, want 0", got)
	}
}

func TestRepoAddWithoutProjectAutoCreatesProject(t *testing.T) {
	f := newFixture(t, nil)
	root := newGitRepo(t)

	added, err := f.run(t, "repo.add", map[string]any{"path": root})
	if err != nil {
		t.Fatalf("repo.add without project_id: %v", err)
	}
	repo := added.(store.Repo)
	if repo.Path != root {
		t.Fatalf("repo path = %q, want %q", repo.Path, root)
	}
	if len(repo.ProjectIDs) != 1 {
		t.Fatalf("auto repo project_ids = %v, want exactly one", repo.ProjectIDs)
	}
	projectID := repo.ProjectIDs[0]
	if projectID == f.project {
		t.Fatalf("auto repo reused the fixture project %d", f.project)
	}
	names := projectNames(t, f)
	if names[projectID] != filepath.Base(root) {
		t.Fatalf("auto project name = %q, want %q", names[projectID], filepath.Base(root))
	}

	scoped, err := f.run(t, "repo.list", map[string]any{"project_id": projectID})
	if err != nil {
		t.Fatalf("repo.list for auto project: %v", err)
	}
	repos := scoped.([]store.Repo)
	if len(repos) != 1 || repos[0].ID != repo.ID {
		t.Fatalf("auto project repos = %+v, want repo %d", repos, repo.ID)
	}
	if _, err := f.run(t, "repo.add", map[string]any{"path": root}); err == nil {
		t.Fatal("duplicate auto repo.add was accepted")
	} else if status, code := Classify(err); status != 409 || code != "conflict" {
		t.Fatalf("classify = (%d, %q), want (409, conflict)", status, code)
	}
}

func TestRepoLinkAndUnlinkRoundTrip(t *testing.T) {
	f := newFixture(t, nil)
	other, err := f.run(t, "project.create", map[string]any{"name": "Other"})
	if err != nil {
		t.Fatalf("create other project: %v", err)
	}
	otherID := other.(store.Project).ID

	linked, err := f.run(t, "repo.link", map[string]any{"repo_id": f.repo, "project_id": otherID})
	if err != nil {
		t.Fatalf("repo.link: %v", err)
	}
	repo := linked.(store.Repo)
	if repo.ID != f.repo {
		t.Fatalf("linked repo id = %d, want %d", repo.ID, f.repo)
	}
	requireProjectIDs(t, repo, f.project, otherID)

	for _, projectID := range []int64{f.project, otherID} {
		listed, err := f.run(t, "repo.list", map[string]any{"project_id": projectID})
		if err != nil {
			t.Fatalf("repo.list(%d): %v", projectID, err)
		}
		repos := listed.([]store.Repo)
		if len(repos) != 1 || repos[0].ID != f.repo {
			t.Fatalf("repos for project %d = %+v, want repo %d", projectID, repos, f.repo)
		}
		requireProjectIDs(t, repos[0], f.project, otherID)
	}

	unlinked, err := f.run(t, "repo.unlink", map[string]any{"repo_id": f.repo, "project_id": otherID})
	if err != nil {
		t.Fatalf("repo.unlink: %v", err)
	}
	requireProjectIDs(t, unlinked.(store.Repo), f.project)

	// Unlinking an absent link is an idempotent no-op.
	again, err := f.run(t, "repo.unlink", map[string]any{"repo_id": f.repo, "project_id": otherID})
	if err != nil {
		t.Fatalf("idempotent repo.unlink: %v", err)
	}
	requireProjectIDs(t, again.(store.Repo), f.project)

	if _, err := f.run(t, "repo.unlink", map[string]any{"repo_id": f.repo, "project_id": f.project}); err == nil {
		t.Fatal("unlink of a repo's last project was accepted")
	} else if status, code := Classify(err); status != 409 || code != "conflict" {
		t.Fatalf("classify = (%d, %q), want (409, conflict)", status, code)
	}
}

func TestRepoUnlinkRefusesProjectWithTasks(t *testing.T) {
	f := newFixture(t, nil)
	other, err := f.run(t, "project.create", map[string]any{"name": "Other"})
	if err != nil {
		t.Fatalf("create other project: %v", err)
	}
	otherID := other.(store.Project).ID
	if _, err := f.run(t, "repo.link", map[string]any{"repo_id": f.repo, "project_id": otherID}); err != nil {
		t.Fatalf("repo.link: %v", err)
	}

	// The fixture task targets f.repo inside f.project, so that link is in use.
	if _, err := f.run(t, "repo.unlink", map[string]any{"repo_id": f.repo, "project_id": f.project}); err == nil {
		t.Fatal("unlink of a repo used by a task was accepted")
	} else if status, code := Classify(err); status != 409 || code != "conflict" {
		t.Fatalf("classify = (%d, %q), want (409, conflict)", status, code)
	}

	listed, err := f.run(t, "repo.list", map[string]any{"project_id": f.project})
	if err != nil {
		t.Fatalf("repo.list after refused unlink: %v", err)
	}
	repos := listed.([]store.Repo)
	if len(repos) != 1 || repos[0].ID != f.repo {
		t.Fatalf("repos after refused unlink = %+v", repos)
	}
	requireProjectIDs(t, repos[0], f.project, otherID)

	unlinked, err := f.run(t, "repo.unlink", map[string]any{"repo_id": f.repo, "project_id": otherID})
	if err != nil {
		t.Fatalf("unlink unused project: %v", err)
	}
	requireProjectIDs(t, unlinked.(store.Repo), f.project)
}

func TestTaskListGlobalAndProjectScoped(t *testing.T) {
	f := newFixture(t, nil)

	global, err := f.run(t, "task.list", map[string]any{})
	if err != nil {
		t.Fatalf("task.list without project_id: %v", err)
	}
	tasks := global.([]store.Task)
	if len(tasks) != 1 || tasks[0].ID != f.task {
		t.Fatalf("global tasks = %+v, want task %d", tasks, f.task)
	}

	scoped, err := f.run(t, "task.list", map[string]any{"project_id": f.project})
	if err != nil {
		t.Fatalf("task.list with project_id: %v", err)
	}
	if got := len(scoped.([]store.Task)); got != 1 {
		t.Fatalf("project tasks = %d, want 1", got)
	}

	empty, err := f.run(t, "project.create", map[string]any{"name": "Empty"})
	if err != nil {
		t.Fatalf("create empty project: %v", err)
	}
	scoped, err = f.run(t, "task.list", map[string]any{"project_id": empty.(store.Project).ID})
	if err != nil {
		t.Fatalf("task.list for empty project: %v", err)
	}
	if got := len(scoped.([]store.Task)); got != 0 {
		t.Fatalf("empty project tasks = %d, want 0", got)
	}
}

func TestTaskCreateInfersProjectAndRejectsAmbiguity(t *testing.T) {
	f := newFixture(t, nil)

	inferred, err := f.run(t, "task.create", map[string]any{"repo_id": f.repo, "title": "Inferred"})
	if err != nil {
		t.Fatalf("task.create without project_id: %v", err)
	}
	task := inferred.(store.Task)
	if task.ProjectID != f.project {
		t.Fatalf("inferred project = %d, want %d", task.ProjectID, f.project)
	}
	if task.RepoID != f.repo {
		t.Fatalf("repo = %d, want %d", task.RepoID, f.repo)
	}

	other, err := f.run(t, "project.create", map[string]any{"name": "Other"})
	if err != nil {
		t.Fatalf("create other project: %v", err)
	}
	otherID := other.(store.Project).ID
	if _, err := f.run(t, "repo.link", map[string]any{"repo_id": f.repo, "project_id": otherID}); err != nil {
		t.Fatalf("repo.link: %v", err)
	}

	if _, err := f.run(t, "task.create", map[string]any{"repo_id": f.repo, "title": "Ambiguous"}); err == nil {
		t.Fatal("task.create inferred a project for a multi-project repo")
	} else {
		requireInvalid(t, err)
		if !strings.Contains(err.Error(), "project_id is required") {
			t.Fatalf("ambiguity error = %v, want it to name project_id", err)
		}
	}

	explicit, err := f.run(t, "task.create", map[string]any{
		"project_id": otherID,
		"repo_id":    f.repo,
		"title":      "Explicit other",
	})
	if err != nil {
		t.Fatalf("task.create with explicit project: %v", err)
	}
	if got := explicit.(store.Task).ProjectID; got != otherID {
		t.Fatalf("explicit project = %d, want %d", got, otherID)
	}
}

func TestCommentEditRemoveLifecycle(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()

	created, err := f.run(t, "task.comment", map[string]any{"id": f.task, "body": "first draft"})
	if err != nil {
		t.Fatalf("comment: %v", err)
	}
	comment := created.(store.Activity)
	commentKey := strconv.FormatInt(comment.ID, 10)

	detail, err := f.run(t, "task.show", map[string]any{"id": f.task})
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	var createdEventID int64
	for _, activity := range detail.(store.TaskDetail).Activity {
		if activity.Type == store.ActivityTaskCreated {
			createdEventID = activity.ID
		}
	}
	other, err := f.run(t, "task.create", map[string]any{
		"project_id": f.project, "repo_id": f.repo, "title": "Other task",
	})
	if err != nil {
		t.Fatalf("create other task: %v", err)
	}
	otherID := other.(store.Task).ID

	rawBefore, err := f.store.ActivityAfter(ctx, 0, 1000)
	if err != nil {
		t.Fatalf("activity before rejections: %v", err)
	}
	rejections := []struct {
		name, actor, command string
		args                 map[string]any
		want                 error
		status               int
	}{
		{"wrong actor", "bob", "task.comment.edit", map[string]any{"id": f.task, "comment_id": comment.ID, "body": "hijack"}, store.ErrNotFound, 404},
		{"wrong task", "alice", "task.comment.edit", map[string]any{"id": otherID, "comment_id": comment.ID, "body": "wrong task"}, store.ErrNotFound, 404},
		{"invalid target", "alice", "task.comment.edit", map[string]any{"id": f.task, "comment_id": createdEventID, "body": "wrong type"}, store.ErrNotFound, 404},
		{"missing comment", "alice", "task.comment.remove", map[string]any{"id": f.task, "comment_id": 999999}, store.ErrNotFound, 404},
		{"zero task", "alice", "task.comment.edit", map[string]any{"id": 0, "comment_id": comment.ID, "body": "x"}, store.ErrInvalid, 400},
		{"zero comment", "alice", "task.comment.remove", map[string]any{"id": f.task, "comment_id": 0}, store.ErrInvalid, 400},
		{"blank body", "alice", "task.comment.edit", map[string]any{"id": f.task, "comment_id": comment.ID, "body": "  "}, store.ErrInvalid, 400},
	}
	for _, test := range rejections {
		t.Run(test.name, func(t *testing.T) {
			_, err := f.service.Execute(ctx, test.command, test.actor, mustJSON(t, test.args))
			if err == nil {
				t.Fatalf("%s accepted %v", test.command, test.args)
			}
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if status, _ := Classify(err); status != test.status {
				t.Fatalf("status = %d, want %d", status, test.status)
			}
		})
	}
	rawAfterRejections, err := f.store.ActivityAfter(ctx, 0, 1000)
	if err != nil {
		t.Fatalf("activity after rejections: %v", err)
	}
	if len(rawAfterRejections) != len(rawBefore) {
		t.Fatalf("rejected mutations appended activity: %d -> %d", len(rawBefore), len(rawAfterRejections))
	}

	for _, body := range []string{"second draft", "final draft"} {
		if _, err := f.run(t, "task.comment.edit", map[string]any{"id": f.task, "comment_id": comment.ID, "body": body}); err != nil {
			t.Fatalf("edit %q: %v", body, err)
		}
	}
	detail, err = f.run(t, "task.show", map[string]any{"id": f.task})
	if err != nil {
		t.Fatalf("show after edits: %v", err)
	}
	projected := detail.(store.TaskDetail)
	var original *store.Activity
	for i := range projected.Activity {
		activity := &projected.Activity[i]
		if activity.Type == store.ActivityTaskCommentEdited || activity.Type == store.ActivityTaskCommentRemoved {
			t.Fatalf("mutation event exposed by Show: %+v", *activity)
		}
		if activity.ID == comment.ID {
			original = activity
		}
	}
	if original == nil {
		t.Fatal("edited comment missing from Show")
	}
	if original.Data["body"] != "final draft" || original.Data["edited_at"] == nil {
		t.Fatalf("projected comment = %+v, want latest body and edited_at", original.Data)
	}
	if !original.Timestamp.Equal(comment.Timestamp) || original.Actor != "alice" {
		t.Fatalf("edited comment identity changed: %+v", original)
	}

	raw, err := f.store.ActivityAfter(ctx, 0, 1000)
	if err != nil {
		t.Fatalf("raw activity after edits: %v", err)
	}
	edits := 0
	editBodies := map[string]bool{}
	for _, activity := range raw {
		if activity.ID == comment.ID && activity.Data["body"] != "first draft" {
			t.Fatalf("raw original comment changed: %#v", activity.Data["body"])
		}
		if activity.Type == store.ActivityTaskCommentEdited && activity.Data["comment_id"] == commentKey {
			edits++
			if body, ok := activity.Data["body"].(string); ok {
				editBodies[body] = true
			}
		}
	}
	if edits != 2 || !editBodies["second draft"] || !editBodies["final draft"] {
		t.Fatalf("raw edit events = %d, bodies = %v", edits, editBodies)
	}

	removed, err := f.run(t, "task.comment.remove", map[string]any{"id": f.task, "comment_id": comment.ID})
	if err != nil {
		t.Fatalf("remove comment: %v", err)
	}
	if removal := removed.(store.Activity); removal.Type != store.ActivityTaskCommentRemoved || removal.Data["comment_id"] != commentKey {
		t.Fatalf("removal activity = %+v", removal)
	}

	detail, err = f.run(t, "task.show", map[string]any{"id": f.task})
	if err != nil {
		t.Fatalf("show after removal: %v", err)
	}
	for _, activity := range detail.(store.TaskDetail).Activity {
		if activity.ID == comment.ID {
			t.Fatalf("removed comment still visible in Show: %+v", activity)
		}
	}

	raw, err = f.store.ActivityAfter(ctx, 0, 1000)
	if err != nil {
		t.Fatalf("raw activity after removal: %v", err)
	}
	var sawOriginal, sawRemoval bool
	for _, activity := range raw {
		if activity.ID == comment.ID {
			sawOriginal = true
		}
		if activity.Type == store.ActivityTaskCommentRemoved && activity.Data["comment_id"] == commentKey {
			sawRemoval = true
		}
	}
	if !sawOriginal || !sawRemoval {
		t.Fatalf("raw history incomplete: original=%v removal=%v", sawOriginal, sawRemoval)
	}

	for _, test := range []struct {
		name, command string
		args          map[string]any
	}{
		{"edit after removal", "task.comment.edit", map[string]any{"id": f.task, "comment_id": comment.ID, "body": "resurrect"}},
		{"remove after removal", "task.comment.remove", map[string]any{"id": f.task, "comment_id": comment.ID}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := f.run(t, test.command, test.args); err == nil {
				t.Fatalf("%s after removal was accepted", test.command)
			} else if !errors.Is(err, store.ErrConflict) {
				t.Fatalf("error = %v, want ErrConflict", err)
			}
		})
	}
}

func TestOptionalProjectIDRejectsExplicitZeroNegativeAndNull(t *testing.T) {
	f := newFixture(t, nil)
	path := newGitRepo(t)
	validPath, err := json.Marshal(path)
	if err != nil {
		t.Fatalf("encode path: %v", err)
	}

	tests := []struct {
		name    string
		command string
		args    string
	}{
		{"repo.list zero", "repo.list", `{"project_id":0}`},
		{"repo.list negative", "repo.list", `{"project_id":-1}`},
		{"repo.list null", "repo.list", `{"project_id":null}`},
		{"repo.add zero", "repo.add", `{"project_id":0,"path":` + string(validPath) + `}`},
		{"repo.add negative", "repo.add", `{"project_id":-1,"path":` + string(validPath) + `}`},
		{"repo.add null", "repo.add", `{"project_id":null,"path":` + string(validPath) + `}`},
		{"task.list zero", "task.list", `{"project_id":0}`},
		{"task.list negative", "task.list", `{"project_id":-1}`},
		{"task.list null", "task.list", `{"project_id":null}`},
		{"task.create zero", "task.create", `{"project_id":0,"repo_id":1,"title":"t"}`},
		{"task.create negative", "task.create", `{"project_id":-1,"repo_id":1,"title":"t"}`},
		{"task.create null", "task.create", `{"project_id":null,"repo_id":1,"title":"t"}`},
		{"repo.link zero repo", "repo.link", `{"repo_id":0,"project_id":1}`},
		{"repo.link zero project", "repo.link", `{"repo_id":1,"project_id":0}`},
		{"repo.link missing project", "repo.link", `{"repo_id":1}`},
		{"repo.unlink zero repo", "repo.unlink", `{"repo_id":0,"project_id":1}`},
		{"repo.unlink negative project", "repo.unlink", `{"repo_id":1,"project_id":-2}`},
		{"repo.unlink missing repo", "repo.unlink", `{"project_id":1}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := f.service.Execute(context.Background(), test.command, "alice", json.RawMessage(test.args))
			if err == nil {
				t.Fatalf("%s accepted %s", test.command, test.args)
			}
			requireInvalid(t, err)
		})
	}
}
