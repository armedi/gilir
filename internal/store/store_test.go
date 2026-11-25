package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gilir.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	t.Cleanup(func() {
		_ = s.Close()
	})
	return s, path
}

func mustCreateProject(t *testing.T, s *Store, name string) Project {
	t.Helper()
	project, err := s.CreateProject(context.Background(), "alice", name)
	if err != nil {
		t.Fatalf("CreateProject(%q): %v", name, err)
	}
	return project
}

func newGitRepo(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(root, "sub", "deep"), 0o755); err != nil {
		t.Fatalf("create repository tree: %v", err)
	}
	command := exec.Command("git", "init", "-q", root)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	return root
}

func mustAddRepo(t *testing.T, s *Store, projectID int64, path string) Repo {
	t.Helper()
	repo, err := s.AddRepo(context.Background(), "alice", projectID, path)
	if err != nil {
		t.Fatalf("AddRepo(%q): %v", path, err)
	}
	return repo
}

func seedProjectRepo(t *testing.T, s *Store) (Project, Repo) {
	t.Helper()
	project := mustCreateProject(t, s, "project")
	repo := mustAddRepo(t, s, project.ID, newGitRepo(t))
	return project, repo
}

func mustCreateTask(t *testing.T, s *Store, in CreateTaskParams) Task {
	t.Helper()
	task, err := s.CreateTask(context.Background(), "alice", in)
	if err != nil {
		t.Fatalf("CreateTask(%+v): %v", in, err)
	}
	return task
}

func mustShow(t *testing.T, s *Store, taskID int64) TaskDetail {
	t.Helper()
	detail, err := s.Show(context.Background(), taskID)
	if err != nil {
		t.Fatalf("Show(%d): %v", taskID, err)
	}
	return detail
}

func mustReserve(t *testing.T, s *Store, url string, pid int64) *DispatchAttempt {
	t.Helper()
	attempt, err := s.Reserve(context.Background(), url, pid)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if attempt == nil {
		t.Fatal("Reserve returned no attempt")
	}
	return attempt
}

func countActivityType(activities []Activity, activityType string) int {
	count := 0
	for _, activity := range activities {
		if activity.Type == activityType {
			count++
		}
	}
	return count
}

func activityTypes(activities []Activity) []string {
	types := make([]string, 0, len(activities))
	for _, activity := range activities {
		types = append(types, activity.Type)
	}
	return types
}

func jsonObject(t *testing.T, value any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var object map[string]any
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatalf("json.Unmarshal(%s): %v", encoded, err)
	}
	return object
}

func TestOpenConfiguresSQLiteAndSchema(t *testing.T) {
	s, path := openTestStore(t)
	ctx := context.Background()

	var foreignKeys int
	if err := s.db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatalf("read foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d, want 1", foreignKeys)
	}
	var busyTimeout int
	if err := s.db.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		t.Fatalf("read busy_timeout: %v", err)
	}
	if busyTimeout != 5000 {
		t.Fatalf("busy_timeout = %d, want 5000", busyTimeout)
	}
	var journalMode string
	if err := s.db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if strings.ToLower(journalMode) != "wal" {
		t.Fatalf("journal_mode = %q, want wal", journalMode)
	}
	var version int
	if err := s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d, want %d", version, schemaVersion)
	}
	if stats := s.db.Stats(); stats.MaxOpenConnections != 1 {
		t.Fatalf("MaxOpenConnections = %d, want 1", stats.MaxOpenConnections)
	}

	settings, err := s.QueueSettings(ctx)
	if err != nil {
		t.Fatalf("QueueSettings: %v", err)
	}
	if settings.Paused || settings.MaxConcurrency != 1 {
		t.Fatalf("default queue settings = %+v, want paused=false max_concurrency=1", settings)
	}

	if _, err := Open(""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Open(empty) error = %v, want ErrInvalid", err)
	}

	memory, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	defer memory.Close()
	if _, err := memory.CreateProject(ctx, "alice", "memory project"); err != nil {
		t.Fatalf("memory CreateProject: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database file %q: %v", path, err)
	}
}

func TestProjectRepoLifecycleAndCanonicalization(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()

	if _, err := s.CreateProject(ctx, "alice", "   "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank project name error = %v, want ErrInvalid", err)
	}
	if _, err := s.CreateProject(ctx, "  ", "name"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank actor error = %v, want ErrInvalid", err)
	}

	first := mustCreateProject(t, s, "first")
	second := mustCreateProject(t, s, "second")
	projects, err := s.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 2 || projects[0].ID != first.ID || projects[1].ID != second.ID || first.ID >= second.ID {
		t.Fatalf("projects = %+v, expected FIFO IDs", projects)
	}

	root := newGitRepo(t)
	repo := mustAddRepo(t, s, first.ID, filepath.Join(root, "sub", "deep"))
	if repo.Path != root {
		t.Fatalf("repo path = %q, want canonical root %q", repo.Path, root)
	}
	link := filepath.Join(t.TempDir(), "repo-link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	if _, err := s.AddRepo(ctx, "alice", first.ID, filepath.Join(link, "sub")); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate canonical repo error = %v, want ErrConflict", err)
	}
	otherProjectRepo := mustAddRepo(t, s, second.ID, link)
	if otherProjectRepo.Path != root {
		t.Fatalf("second project repo path = %q, want %q", otherProjectRepo.Path, root)
	}
	if _, err := s.AddRepo(ctx, "alice", first.ID, t.TempDir()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("non-git repo error = %v, want ErrInvalid", err)
	}
	if _, err := s.AddRepo(ctx, "  ", first.ID, root); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank repo actor error = %v, want ErrInvalid", err)
	}

	repos, err := s.ListRepos(ctx, first.ID)
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(repos) != 1 || repos[0].ID != repo.ID {
		t.Fatalf("repos = %+v, want only %+v", repos, repo)
	}
	if empty, err := s.ListRepos(ctx, 99999); err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("ListRepos(missing) = %#v, %v; want empty non-nil", empty, err)
	}

	renamed, err := s.RenameProject(ctx, "bob", first.ID, "first-renamed")
	if err != nil {
		t.Fatalf("RenameProject: %v", err)
	}
	if renamed.Name != "first-renamed" {
		t.Fatalf("renamed project = %+v", renamed)
	}
	var version int
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM activity WHERE type = 'project.renamed' AND actor = 'bob'`,
	).Scan(&version); err != nil {
		t.Fatalf("count project.renamed: %v", err)
	}
	if version != 1 {
		t.Fatalf("project.renamed count = %d, want 1", version)
	}
	if _, err := s.RenameProject(ctx, "bob", first.ID, "first-renamed"); err != nil {
		t.Fatalf("no-op RenameProject: %v", err)
	}
	var renameEvents int
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM activity WHERE type = 'project.renamed'`,
	).Scan(&renameEvents); err != nil {
		t.Fatalf("count project.renamed: %v", err)
	}
	if renameEvents != 1 {
		t.Fatalf("no-op rename created an event: count = %d", renameEvents)
	}
	if _, err := s.RenameProject(ctx, "bob", 99999, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rename missing project error = %v, want ErrNotFound", err)
	}

	task := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: first.ID,
		RepoID:    repo.ID,
		Title:     "dependent task",
	})
	if err := s.RemoveRepo(ctx, "alice", repo.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("RemoveRepo with task error = %v, want ErrConflict", err)
	}
	if err := s.DeleteProject(ctx, "alice", first.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("DeleteProject with work error = %v, want ErrConflict", err)
	}
	if err := s.DeleteProject(ctx, "alice", second.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("DeleteProject with repo error = %v, want ErrConflict", err)
	}
	if err := s.RemoveRepo(ctx, "alice", otherProjectRepo.ID); err != nil {
		t.Fatalf("RemoveRepo empty: %v", err)
	}
	if err := s.DeleteProject(ctx, "alice", second.ID); err != nil {
		t.Fatalf("DeleteProject empty: %v", err)
	}

	if _, err := s.CreateTask(ctx, "alice", CreateTaskParams{
		ProjectID: first.ID, RepoID: repo.ID, Title: "bad status", Status: Status("blocked"),
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid status error = %v, want ErrInvalid", err)
	}
	if _, err := s.CreateTask(ctx, "alice", CreateTaskParams{
		ProjectID: first.ID, RepoID: repo.ID, Title: "bad priority", Priority: Priority("critical"),
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid priority error = %v, want ErrInvalid", err)
	}
	if task.ProjectID != first.ID || task.RepoID != repo.ID {
		t.Fatalf("task ownership = %+v", task)
	}

	var deleteAudit int
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM activity WHERE type = 'project.deleted' AND project_id = ?`, second.ID,
	).Scan(&deleteAudit); err != nil {
		t.Fatalf("count deleted audit: %v", err)
	}
	if deleteAudit != 1 {
		t.Fatalf("project.deleted audit rows = %d, want 1", deleteAudit)
	}

	replacement := mustCreateProject(t, s, "replacement")
	if replacement.ID <= second.ID {
		t.Fatalf("project ID reused after delete: old=%d new=%d", second.ID, replacement.ID)
	}
}

func TestCompositeOwnershipAndForeignKeys(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	projectA := mustCreateProject(t, s, "A")
	projectB := mustCreateProject(t, s, "B")
	repoA := mustAddRepo(t, s, projectA.ID, newGitRepo(t))
	repoB := mustAddRepo(t, s, projectB.ID, newGitRepo(t))

	if _, err := s.CreateTask(ctx, "alice", CreateTaskParams{
		ProjectID: projectA.ID, RepoID: repoB.ID, Title: "cross project",
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cross-project CreateTask error = %v, want ErrInvalid", err)
	}
	taskA := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: projectA.ID, RepoID: repoA.ID, Title: "A task",
	})
	taskB := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: projectB.ID, RepoID: repoB.ID, Title: "B task",
	})
	if _, err := s.UpdateTask(ctx, "alice", taskA.ID, TaskPatch{RepoID: &repoB.ID}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cross-project UpdateTask error = %v, want ErrInvalid", err)
	}
	if err := s.SetDependency(ctx, "alice", taskA.ID, taskB.ID, true); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cross-project dependency error = %v, want ErrInvalid", err)
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO tasks (project_id, repo_id, title, description, status, priority, created_at, updated_at)
		VALUES (?, ?, 'raw', '', 'backlog', 'medium', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		projectA.ID, repoB.ID,
	)
	if err == nil {
		t.Fatal("direct cross-project task insert succeeded; composite foreign key is not enforced")
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO task_dependencies (task_id, depends_on_id, project_id, created_at)
		VALUES (?, ?, ?, '2026-01-01T00:00:00Z')`,
		taskA.ID, taskB.ID, projectA.ID,
	)
	if err == nil {
		t.Fatal("direct cross-project dependency insert succeeded; composite foreign keys are not enforced")
	}
}

func TestActivityIsAppendOnly(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	project := mustCreateProject(t, s, "audit")
	activities, err := s.ActivityAfter(ctx, 0, 10)
	if err != nil {
		t.Fatalf("ActivityAfter: %v", err)
	}
	if len(activities) != 1 || activities[0].ProjectID == nil || *activities[0].ProjectID != project.ID {
		t.Fatalf("activities = %+v", activities)
	}
	if activities[0].Actor != "alice" {
		t.Fatalf("actor = %q, want alice", activities[0].Actor)
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE activity SET actor = 'mallory' WHERE id = ?`, activities[0].ID,
	); err == nil {
		t.Fatal("updating activity succeeded; append-only trigger missing")
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM activity WHERE id = ?`, activities[0].ID,
	); err == nil {
		t.Fatal("deleting activity succeeded; append-only trigger missing")
	}
}

func TestTimestampsAreUTC(t *testing.T) {
	s, _ := openTestStore(t)
	project, repo := seedProjectRepo(t, s)
	task := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: "time",
	})
	if task.CreatedAt.Location() != time.UTC || task.UpdatedAt.Location() != time.UTC {
		t.Fatalf("task timestamps are not UTC: %+v", task)
	}
	detail := mustShow(t, s, task.ID)
	if len(detail.Activity) == 0 || detail.Activity[0].Timestamp.Location() != time.UTC {
		t.Fatalf("activity timestamp is not UTC: %+v", detail.Activity)
	}
}
