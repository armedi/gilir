package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func mustLinkRepo(t *testing.T, s *Store, repoID, projectID int64) Repo {
	t.Helper()
	repo, err := s.LinkRepo(context.Background(), "alice", repoID, projectID)
	if err != nil {
		t.Fatalf("LinkRepo(%d, %d): %v", repoID, projectID, err)
	}
	return repo
}

func mustUnlinkRepo(t *testing.T, s *Store, repoID, projectID int64) Repo {
	t.Helper()
	repo, err := s.UnlinkRepo(context.Background(), "alice", repoID, projectID)
	if err != nil {
		t.Fatalf("UnlinkRepo(%d, %d): %v", repoID, projectID, err)
	}
	return repo
}

func TestAddRepoAutoCreatesProjectAndDisambiguates(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()

	firstRoot := newGitRepo(t)
	first, err := s.AddRepo(ctx, "alice", 0, filepath.Join(firstRoot, "sub"))
	if err != nil {
		t.Fatalf("auto AddRepo: %v", err)
	}
	if first.Path != firstRoot {
		t.Fatalf("auto repo path = %q, want canonical root %q", first.Path, firstRoot)
	}
	if len(first.ProjectIDs) != 1 {
		t.Fatalf("auto repo project IDs = %v, want one", first.ProjectIDs)
	}
	firstProject := mustProject(t, s, first.ProjectIDs[0])
	if firstProject.Name != filepath.Base(firstRoot) {
		t.Fatalf("auto project name = %q, want %q", firstProject.Name, filepath.Base(firstRoot))
	}
	requireProjectIDs(t, first, firstProject.ID)
	list := mustListRepos(t, s, firstProject.ID)
	if len(list) != 1 || list[0].ID != first.ID {
		t.Fatalf("auto project repos = %+v, want repo %d", list, first.ID)
	}

	secondRoot := newGitRepo(t)
	second, err := s.AddRepo(ctx, "alice", 0, secondRoot)
	if err != nil {
		t.Fatalf("second auto AddRepo: %v", err)
	}
	secondProject := mustProject(t, s, second.ProjectIDs[0])
	wantSecondName := filepath.Join(filepath.Base(filepath.Dir(secondRoot)), filepath.Base(secondRoot))
	if secondProject.Name != wantSecondName {
		t.Fatalf("collision project name = %q, want %q", secondProject.Name, wantSecondName)
	}
	if firstProject.ID == secondProject.ID {
		t.Fatal("unrelated repos merged into one project")
	}

	thirdRoot := newGitRepo(t)
	thirdCandidate := filepath.Join(filepath.Base(filepath.Dir(thirdRoot)), filepath.Base(thirdRoot))
	mustCreateProject(t, s, thirdCandidate)
	third, err := s.AddRepo(ctx, "alice", 0, thirdRoot)
	if err != nil {
		t.Fatalf("third auto AddRepo: %v", err)
	}
	thirdProject := mustProject(t, s, third.ProjectIDs[0])
	if thirdProject.Name != "repo-2" {
		t.Fatalf("numeric fallback project name = %q, want repo-2", thirdProject.Name)
	}

	fourthRoot := newGitRepo(t)
	fourthCandidate := filepath.Join(filepath.Base(filepath.Dir(fourthRoot)), filepath.Base(fourthRoot))
	mustCreateProject(t, s, fourthCandidate)
	fourth, err := s.AddRepo(ctx, "alice", 0, fourthRoot)
	if err != nil {
		t.Fatalf("fourth auto AddRepo: %v", err)
	}
	fourthProject := mustProject(t, s, fourth.ProjectIDs[0])
	if fourthProject.Name != "repo-3" {
		t.Fatalf("second numeric fallback project name = %q, want repo-3", fourthProject.Name)
	}

	projectsBefore, err := s.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if _, err := s.AddRepo(ctx, "alice", 0, firstRoot); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate auto repo error = %v, want ErrConflict", err)
	}
	projectsAfter, err := s.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects after conflict: %v", err)
	}
	if len(projectsAfter) != len(projectsBefore) {
		t.Fatalf("failed auto AddRepo created a project: %d -> %d", len(projectsBefore), len(projectsAfter))
	}
	if count := mustActivityCount(t, s, ActivityRepoAdded); count != 4 {
		t.Fatalf("repo.added events = %d, want 4", count)
	}
}

func TestLinkAndUnlinkRepoManyToMany(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	projectA := mustCreateProject(t, s, "A")
	projectB := mustCreateProject(t, s, "B")
	repo := mustAddRepo(t, s, projectA.ID, newGitRepo(t))
	requireProjectIDs(t, repo, projectA.ID)

	linked := mustLinkRepo(t, s, repo.ID, projectB.ID)
	requireProjectIDs(t, linked, projectA.ID, projectB.ID)
	if linked.Path != repo.Path {
		t.Fatalf("linked repo path = %q, want %q", linked.Path, repo.Path)
	}
	if count := mustActivityCount(t, s, ActivityRepoLinked); count != 1 {
		t.Fatalf("repo.linked events = %d, want 1", count)
	}
	again := mustLinkRepo(t, s, repo.ID, projectB.ID)
	requireProjectIDs(t, again, projectA.ID, projectB.ID)
	if count := mustActivityCount(t, s, ActivityRepoLinked); count != 1 {
		t.Fatalf("idempotent link wrote activity: count = %d", count)
	}

	for _, projectID := range []int64{projectA.ID, projectB.ID} {
		repos := mustListRepos(t, s, projectID)
		if len(repos) != 1 || repos[0].ID != repo.ID {
			t.Fatalf("ListRepos(%d) = %+v, want repo %d", projectID, repos, repo.ID)
		}
		requireProjectIDs(t, repos[0], projectA.ID, projectB.ID)
	}
	global := mustListRepos(t, s, 0)
	if len(global) != 1 || global[0].ID != repo.ID {
		t.Fatalf("ListRepos(0) = %+v, want repo %d", global, repo.ID)
	}
	requireProjectIDs(t, global[0], projectA.ID, projectB.ID)

	encoded, err := json.Marshal(linked)
	if err != nil {
		t.Fatalf("marshal repo: %v", err)
	}
	var object map[string]any
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatalf("unmarshal repo: %v", err)
	}
	if len(object) != 3 {
		t.Fatalf("repo JSON keys = %v, want id, path, project_ids only: %s", object, encoded)
	}
	if _, present := object["project_id"]; present {
		t.Fatalf("repo JSON still has project_id: %s", encoded)
	}
	projectIDs, ok := object["project_ids"].([]any)
	if !ok || len(projectIDs) != 2 ||
		projectIDs[0] != float64(projectA.ID) || projectIDs[1] != float64(projectB.ID) {
		t.Fatalf("repo project_ids JSON = %#v", object["project_ids"])
	}
	if object["path"] != repo.Path {
		t.Fatalf("repo path JSON = %#v", object["path"])
	}

	unlinked := mustUnlinkRepo(t, s, repo.ID, projectB.ID)
	requireProjectIDs(t, unlinked, projectA.ID)
	if count := mustActivityCount(t, s, ActivityRepoUnlinked); count != 1 {
		t.Fatalf("repo.unlinked events = %d, want 1", count)
	}
	again = mustUnlinkRepo(t, s, repo.ID, projectB.ID)
	requireProjectIDs(t, again, projectA.ID)
	if count := mustActivityCount(t, s, ActivityRepoUnlinked); count != 1 {
		t.Fatalf("idempotent unlink wrote activity: count = %d", count)
	}

	if _, err := s.UnlinkRepo(ctx, "alice", repo.ID, projectA.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("unlink last project error = %v, want ErrConflict", err)
	}
	remaining := mustListRepos(t, s, 0)
	if len(remaining) != 1 || remaining[0].ID != repo.ID {
		t.Fatalf("repos after refused unlink = %+v", remaining)
	}
	requireProjectIDs(t, remaining[0], projectA.ID)

	checks := []struct {
		name string
		call func() error
		want error
	}{
		{"link blank actor", func() error {
			_, err := s.LinkRepo(ctx, " ", repo.ID, projectB.ID)
			return err
		}, ErrInvalid},
		{"link missing repo", func() error {
			_, err := s.LinkRepo(ctx, "alice", 99999, projectB.ID)
			return err
		}, ErrNotFound},
		{"link missing project", func() error {
			_, err := s.LinkRepo(ctx, "alice", repo.ID, 99999)
			return err
		}, ErrNotFound},
		{"link zero repo", func() error {
			_, err := s.LinkRepo(ctx, "alice", 0, projectB.ID)
			return err
		}, ErrInvalid},
		{"unlink blank actor", func() error {
			_, err := s.UnlinkRepo(ctx, " ", repo.ID, projectA.ID)
			return err
		}, ErrInvalid},
		{"unlink missing repo", func() error {
			_, err := s.UnlinkRepo(ctx, "alice", 99999, projectA.ID)
			return err
		}, ErrNotFound},
		{"unlink missing project", func() error {
			_, err := s.UnlinkRepo(ctx, "alice", repo.ID, 99999)
			return err
		}, ErrNotFound},
		{"unlink zero project", func() error {
			_, err := s.UnlinkRepo(ctx, "alice", repo.ID, 0)
			return err
		}, ErrInvalid},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.call(); !errors.Is(err, check.want) {
				t.Fatalf("error = %v, want %v", err, check.want)
			}
		})
	}
}

func TestRepoProjectInvariantsAndDeletion(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	projectA := mustCreateProject(t, s, "A")
	projectB := mustCreateProject(t, s, "B")
	projectC := mustCreateProject(t, s, "C")
	repo := mustAddRepo(t, s, projectA.ID, newGitRepo(t))
	mustLinkRepo(t, s, repo.ID, projectB.ID)

	if err := s.DeleteProject(ctx, "alice", projectA.ID); err != nil {
		t.Fatalf("DeleteProject with shared repo: %v", err)
	}
	repos := mustListRepos(t, s, 0)
	if len(repos) != 1 || repos[0].ID != repo.ID {
		t.Fatalf("repos after deleting project A = %+v", repos)
	}
	requireProjectIDs(t, repos[0], projectB.ID)

	if err := s.DeleteProject(ctx, "alice", projectB.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("DeleteProject orphaning last project error = %v, want ErrConflict", err)
	}
	if repos := mustListRepos(t, s, projectB.ID); len(repos) != 1 || repos[0].ID != repo.ID {
		t.Fatalf("refused DeleteProject removed the repo: %+v", repos)
	}

	mustLinkRepo(t, s, repo.ID, projectC.ID)
	if err := s.DeleteProject(ctx, "alice", projectB.ID); err != nil {
		t.Fatalf("DeleteProject with repo still linked to C: %v", err)
	}
	repos = mustListRepos(t, s, 0)
	requireProjectIDs(t, repos[0], projectC.ID)

	if _, err := s.UnlinkRepo(ctx, "alice", repo.ID, projectC.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("unlink last project error = %v, want ErrConflict", err)
	}
	if err := s.DeleteProject(ctx, "alice", projectC.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("DeleteProject orphaning last project error = %v, want ErrConflict", err)
	}

	if err := s.RemoveRepo(ctx, "alice", repo.ID); err != nil {
		t.Fatalf("RemoveRepo: %v", err)
	}
	if repos := mustListRepos(t, s, 0); len(repos) != 0 {
		t.Fatalf("repos after RemoveRepo = %+v, want none", repos)
	}
	if repos := mustListRepos(t, s, projectC.ID); len(repos) != 0 {
		t.Fatalf("project repos after RemoveRepo = %+v, want none", repos)
	}
	if count := mustActivityCount(t, s, ActivityRepoRemoved); count != 1 {
		t.Fatalf("repo.removed events = %d, want 1", count)
	}
	if err := s.DeleteProject(ctx, "alice", projectC.ID); err != nil {
		t.Fatalf("DeleteProject after RemoveRepo: %v", err)
	}
}

func TestUnlinkRepoRefusesTasksAndLastProject(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	projectA := mustCreateProject(t, s, "A")
	projectB := mustCreateProject(t, s, "B")
	repo := mustAddRepo(t, s, projectA.ID, newGitRepo(t))
	mustLinkRepo(t, s, repo.ID, projectB.ID)
	mustCreateTask(t, s, CreateTaskParams{
		ProjectID: projectA.ID, RepoID: repo.ID, Title: "uses repo",
	})

	if _, err := s.UnlinkRepo(ctx, "alice", repo.ID, projectA.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("unlink with tasks error = %v, want ErrConflict", err)
	}
	repos := mustListRepos(t, s, 0)
	if len(repos) != 1 || repos[0].ID != repo.ID {
		t.Fatalf("repos after refused unlink = %+v", repos)
	}
	requireProjectIDs(t, repos[0], projectA.ID, projectB.ID)

	unlinked := mustUnlinkRepo(t, s, repo.ID, projectB.ID)
	requireProjectIDs(t, unlinked, projectA.ID)

	if err := s.RemoveRepo(ctx, "alice", repo.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("RemoveRepo with task error = %v, want ErrConflict", err)
	}
	if err := s.DeleteProject(ctx, "alice", projectA.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("DeleteProject with task error = %v, want ErrConflict", err)
	}

	single := mustAddRepo(t, s, projectA.ID, newGitRepo(t))
	if _, err := s.UnlinkRepo(ctx, "alice", single.ID, projectA.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("unlink last project error = %v, want ErrConflict", err)
	}
	found := false
	for _, candidate := range mustListRepos(t, s, projectA.ID) {
		if candidate.ID == single.ID {
			found = true
			requireProjectIDs(t, candidate, projectA.ID)
		}
	}
	if !found {
		t.Fatalf("repo %d disappeared after refused unlink", single.ID)
	}
	if err := s.RemoveRepo(ctx, "alice", single.ID); err != nil {
		t.Fatalf("RemoveRepo without tasks: %v", err)
	}
}

func TestListReposGlobalAndProjectFilters(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	projectA := mustCreateProject(t, s, "A")
	projectB := mustCreateProject(t, s, "B")
	projectC := mustCreateProject(t, s, "C")

	first := mustAddRepo(t, s, projectA.ID, newGitRepo(t))
	second := mustAddRepo(t, s, projectB.ID, newGitRepo(t))
	third := mustAddRepo(t, s, projectB.ID, newGitRepo(t))
	mustLinkRepo(t, s, third.ID, projectA.ID)

	global := mustListRepos(t, s, 0)
	if len(global) != 3 || global[0].ID != first.ID || global[1].ID != second.ID || global[2].ID != third.ID {
		t.Fatalf("ListRepos(0) = %+v, want [%d %d %d]", global, first.ID, second.ID, third.ID)
	}
	requireProjectIDs(t, global[0], projectA.ID)
	requireProjectIDs(t, global[1], projectB.ID)
	requireProjectIDs(t, global[2], projectA.ID, projectB.ID)

	projectAReps := mustListRepos(t, s, projectA.ID)
	if len(projectAReps) != 2 || projectAReps[0].ID != first.ID || projectAReps[1].ID != third.ID {
		t.Fatalf("ListRepos(A) = %+v", projectAReps)
	}
	requireProjectIDs(t, projectAReps[1], projectA.ID, projectB.ID)

	projectBRepos := mustListRepos(t, s, projectB.ID)
	if len(projectBRepos) != 2 || projectBRepos[0].ID != second.ID || projectBRepos[1].ID != third.ID {
		t.Fatalf("ListRepos(B) = %+v", projectBRepos)
	}
	requireProjectIDs(t, projectBRepos[1], projectA.ID, projectB.ID)

	if empty := mustListRepos(t, s, projectC.ID); len(empty) != 0 {
		t.Fatalf("ListRepos(empty project) = %+v, want none", empty)
	}
	if _, err := s.ListRepos(ctx, -1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ListRepos(-1) error = %v, want ErrInvalid", err)
	}
}

func TestCreateTaskProjectInferenceAndAmbiguity(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	projectA := mustCreateProject(t, s, "A")
	projectB := mustCreateProject(t, s, "B")
	projectC := mustCreateProject(t, s, "C")
	repo := mustAddRepo(t, s, projectA.ID, newGitRepo(t))

	inferred := mustCreateTask(t, s, CreateTaskParams{
		RepoID: repo.ID, Title: "inferred project",
	})
	if inferred.ProjectID != projectA.ID {
		t.Fatalf("inferred project = %d, want %d", inferred.ProjectID, projectA.ID)
	}
	detail := mustShow(t, s, inferred.ID)
	if len(detail.Activity) == 0 || detail.Activity[0].ProjectID == nil ||
		*detail.Activity[0].ProjectID != projectA.ID {
		t.Fatalf("inferred task activity project = %+v", detail.Activity)
	}
	explicitA := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: projectA.ID, RepoID: repo.ID, Title: "explicit A",
	})
	if explicitA.ProjectID != projectA.ID {
		t.Fatalf("explicit task project = %d, want %d", explicitA.ProjectID, projectA.ID)
	}

	mustLinkRepo(t, s, repo.ID, projectB.ID)
	if _, err := s.CreateTask(ctx, "alice", CreateTaskParams{
		RepoID: repo.ID, Title: "ambiguous",
	}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "project_id is required") {
		t.Fatalf("ambiguous project error = %v, want ErrInvalid naming project_id", err)
	}
	explicitB := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: projectB.ID, RepoID: repo.ID, Title: "explicit B",
	})
	if explicitB.ProjectID != projectB.ID {
		t.Fatalf("explicit B project = %d, want %d", explicitB.ProjectID, projectB.ID)
	}

	if _, err := s.CreateTask(ctx, "alice", CreateTaskParams{
		ProjectID: projectA.ID, RepoID: 99999, Title: "missing repo",
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing repo error = %v, want ErrNotFound", err)
	}
	if _, err := s.CreateTask(ctx, "alice", CreateTaskParams{
		RepoID: 99999, Title: "missing repo unqualified",
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unqualified missing repo error = %v, want ErrNotFound", err)
	}
	if _, err := s.CreateTask(ctx, "alice", CreateTaskParams{
		ProjectID: -1, RepoID: repo.ID, Title: "negative project",
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("negative project error = %v, want ErrInvalid", err)
	}
	if _, err := s.CreateTask(ctx, "alice", CreateTaskParams{
		ProjectID: 99999, RepoID: repo.ID, Title: "missing project",
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing project error = %v, want ErrNotFound", err)
	}

	repoC := mustAddRepo(t, s, projectC.ID, newGitRepo(t))
	if _, err := s.CreateTask(ctx, "alice", CreateTaskParams{
		ProjectID: projectA.ID, RepoID: repoC.ID, Title: "wrong project",
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unlinked pair error = %v, want ErrInvalid", err)
	}
	inferredC := mustCreateTask(t, s, CreateTaskParams{
		RepoID: repoC.ID, Title: "inferred C",
	})
	if inferredC.ProjectID != projectC.ID {
		t.Fatalf("inferred C project = %d, want %d", inferredC.ProjectID, projectC.ID)
	}

	if _, err := s.UpdateTask(ctx, "alice", explicitA.ID, TaskPatch{RepoID: &repoC.ID}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("UpdateTask to unlinked repo error = %v, want ErrInvalid", err)
	}
	mustLinkRepo(t, s, repoC.ID, projectA.ID)
	updated, err := s.UpdateTask(ctx, "alice", explicitA.ID, TaskPatch{RepoID: &repoC.ID})
	if err != nil {
		t.Fatalf("UpdateTask to linked repo: %v", err)
	}
	if updated.ProjectID != projectA.ID || updated.RepoID != repoC.ID {
		t.Fatalf("updated task ownership = %+v", updated)
	}

	repoBOnly := mustAddRepo(t, s, projectB.ID, newGitRepo(t))
	if _, err := s.UpdateTask(ctx, "alice", explicitA.ID, TaskPatch{RepoID: &repoBOnly.ID}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("UpdateTask to other-project repo error = %v, want ErrInvalid", err)
	}
}

func TestProjectRepoSchemaConstraints(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	projectA := mustCreateProject(t, s, "A")
	projectB := mustCreateProject(t, s, "B")
	repo := mustAddRepo(t, s, projectA.ID, newGitRepo(t))

	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO project_repos (project_id, repo_id) VALUES (?, ?)`, projectA.ID, repo.ID,
	); err == nil {
		t.Fatal("duplicate project_repos row was accepted")
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO project_repos (project_id, repo_id) VALUES (?, ?)`, 99999, repo.ID,
	); err == nil {
		t.Fatal("project_repos with a missing project was accepted")
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO project_repos (project_id, repo_id) VALUES (?, ?)`, projectB.ID, 99999,
	); err == nil {
		t.Fatal("project_repos with a missing repo was accepted")
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM projects WHERE id = ?`, projectA.ID); err == nil {
		t.Fatal("deleting a project with a repo link succeeded; RESTRICT missing")
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM repos WHERE id = ?`, repo.ID); err == nil {
		t.Fatal("deleting a repo with a project link succeeded; RESTRICT missing")
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO projects (name) VALUES (?)`, projectA.Name); err == nil {
		t.Fatal("duplicate project name was accepted")
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO repos (path) VALUES (?)`, repo.Path); err == nil {
		t.Fatal("duplicate repo path was accepted")
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO projects (name) VALUES ('   ')`); err == nil {
		t.Fatal("blank project name was accepted")
	}

	mustCreateTask(t, s, CreateTaskParams{
		ProjectID: projectA.ID, RepoID: repo.ID, Title: "protects link",
	})
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM project_repos WHERE project_id = ? AND repo_id = ?`, projectA.ID, repo.ID,
	); err == nil {
		t.Fatal("deleting a project_repos row used by a task succeeded; RESTRICT missing")
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO tasks (project_id, repo_id, title, description, status, priority, created_at, updated_at)
		VALUES (?, ?, 'raw', '', 'backlog', 'medium', ?, ?)`,
		projectB.ID, repo.ID, formatTime(nowUTC()), formatTime(nowUTC()),
	); err == nil {
		t.Fatal("direct insert of an unlinked task pair succeeded")
	}
}
