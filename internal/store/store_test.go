package store

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

func requireProjectIDs(t *testing.T, repo Repo, want ...int64) {
	t.Helper()
	if repo.ProjectIDs == nil {
		t.Fatalf("repo %d ProjectIDs is nil, want %v", repo.ID, want)
	}
	if !slices.Equal(repo.ProjectIDs, want) {
		t.Fatalf("repo %d ProjectIDs = %v, want %v", repo.ID, repo.ProjectIDs, want)
	}
}

func mustProject(t *testing.T, s *Store, projectID int64) Project {
	t.Helper()
	projects, err := s.ListProjects(context.Background())
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	for _, project := range projects {
		if project.ID == projectID {
			return project
		}
	}
	t.Fatalf("project %d not found in %+v", projectID, projects)
	return Project{}
}

func mustListRepos(t *testing.T, s *Store, projectID int64) []Repo {
	t.Helper()
	repos, err := s.ListRepos(context.Background(), projectID)
	if err != nil {
		t.Fatalf("ListRepos(%d): %v", projectID, err)
	}
	if repos == nil {
		t.Fatalf("ListRepos(%d) returned nil", projectID)
	}
	return repos
}

func mustActivityCount(t *testing.T, s *Store, activityType string) int {
	t.Helper()
	activities, err := s.ActivityAfter(context.Background(), 0, 1000)
	if err != nil {
		t.Fatalf("ActivityAfter: %v", err)
	}
	return countActivityType(activities, activityType)
}

func mustCreateTask(t *testing.T, s *Store, in CreateTaskParams) Task {
	t.Helper()
	task, err := s.CreateTask(context.Background(), "alice", in)
	if err != nil {
		t.Fatalf("CreateTask(%+v): %v", in, err)
	}
	return task
}

func mustShow(t *testing.T, s *Store, taskID string) TaskDetail {
	t.Helper()
	detail, err := s.Show(context.Background(), taskID)
	if err != nil {
		t.Fatalf("Show(%s): %v", taskID, err)
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
	if _, err := s.CreateProject(ctx, "alice", "  first  "); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate trimmed project name error = %v, want ErrConflict", err)
	}
	spaced := mustCreateProject(t, s, "  spaced  ")
	if spaced.Name != "spaced" {
		t.Fatalf("trimmed project name = %q, want %q", spaced.Name, "spaced")
	}
	projects, err := s.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 3 || projects[0].ID != first.ID || projects[1].ID != second.ID || first.ID >= second.ID {
		t.Fatalf("projects = %+v, expected FIFO IDs", projects)
	}

	root := newGitRepo(t)
	repo := mustAddRepo(t, s, first.ID, filepath.Join(root, "sub", "deep"))
	if repo.Path != root {
		t.Fatalf("repo path = %q, want canonical root %q", repo.Path, root)
	}
	requireProjectIDs(t, repo, first.ID)
	link := filepath.Join(t.TempDir(), "repo-link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	if _, err := s.AddRepo(ctx, "alice", first.ID, filepath.Join(link, "sub")); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate canonical repo error = %v, want ErrConflict", err)
	}
	if _, err := s.AddRepo(ctx, "alice", second.ID, link); !errors.Is(err, ErrConflict) {
		t.Fatalf("cross-project duplicate canonical repo error = %v, want ErrConflict", err)
	}
	linked, err := s.LinkRepo(ctx, "alice", repo.ID, second.ID)
	if err != nil {
		t.Fatalf("LinkRepo: %v", err)
	}
	requireProjectIDs(t, linked, first.ID, second.ID)
	if _, err := s.AddRepo(ctx, "alice", first.ID, t.TempDir()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("non-git repo error = %v, want ErrInvalid", err)
	}
	if _, err := s.AddRepo(ctx, "  ", first.ID, root); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank repo actor error = %v, want ErrInvalid", err)
	}
	if _, err := s.AddRepo(ctx, "alice", -1, root); !errors.Is(err, ErrInvalid) {
		t.Fatalf("negative project error = %v, want ErrInvalid", err)
	}
	if _, err := s.AddRepo(ctx, "alice", 99999, newGitRepo(t)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing project error = %v, want ErrNotFound", err)
	}

	repos, err := s.ListRepos(ctx, first.ID)
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(repos) != 1 || repos[0].ID != repo.ID {
		t.Fatalf("repos = %+v, want only %+v", repos, repo)
	}
	requireProjectIDs(t, repos[0], first.ID, second.ID)
	global, err := s.ListRepos(ctx, 0)
	if err != nil || len(global) != 1 || global[0].ID != repo.ID {
		t.Fatalf("ListRepos(0) = %+v, %v", global, err)
	}
	if empty, err := s.ListRepos(ctx, 99999); err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("ListRepos(missing) = %#v, %v; want empty non-nil", empty, err)
	}
	if _, err := s.ListRepos(ctx, -1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ListRepos(-1) error = %v, want ErrInvalid", err)
	}

	renamed, err := s.RenameProject(ctx, "bob", first.ID, "first-renamed")
	if err != nil {
		t.Fatalf("RenameProject: %v", err)
	}
	if renamed.Name != "first-renamed" {
		t.Fatalf("renamed project = %+v", renamed)
	}
	if _, err := s.RenameProject(ctx, "bob", second.ID, "first-renamed"); !errors.Is(err, ErrConflict) {
		t.Fatalf("rename to taken name error = %v, want ErrConflict", err)
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
		t.Fatalf("DeleteProject with task error = %v, want ErrConflict", err)
	}
	if err := s.DeleteProject(ctx, "alice", second.ID); err != nil {
		t.Fatalf("DeleteProject with shared repo: %v", err)
	}
	afterDelete, err := s.ListRepos(ctx, 0)
	if err != nil || len(afterDelete) != 1 || afterDelete[0].ID != repo.ID {
		t.Fatalf("repos after shared-project delete = %+v, %v", afterDelete, err)
	}
	requireProjectIDs(t, afterDelete[0], first.ID)
	if err := s.DeleteProject(ctx, "alice", second.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second DeleteProject error = %v, want ErrNotFound", err)
	}

	removable := mustAddRepo(t, s, first.ID, newGitRepo(t))
	if err := s.RemoveRepo(ctx, "alice", removable.ID); err != nil {
		t.Fatalf("RemoveRepo empty: %v", err)
	}
	if err := s.RemoveRepo(ctx, "alice", removable.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second RemoveRepo error = %v, want ErrNotFound", err)
	}
	remaining, err := s.ListRepos(ctx, 0)
	if err != nil || len(remaining) != 1 || remaining[0].ID != repo.ID {
		t.Fatalf("repos after removal = %+v, %v", remaining, err)
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
