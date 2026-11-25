package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// AddRepo registers the canonical Git root containing path. The input may be
// any directory inside the work tree, including a symlinked path; the stored
// path is the resolved absolute Git top level and is registered globally at
// most once.
//
// A positive projectID links the repo to that existing project. A zero
// projectID creates a project named after the repository basename and links
// the two, disambiguating the name with "<parent>/<basename>" and then
// "<basename>-2", "<basename>-3", ... when the name is already taken.
func (s *Store) AddRepo(ctx context.Context, actor string, projectID int64, path string) (Repo, error) {
	actor, err := normalizeActor(actor)
	if err != nil {
		return Repo{}, err
	}
	if projectID < 0 {
		return Repo{}, fmt.Errorf("%w: project must be a non-negative ID", ErrInvalid)
	}

	root, err := canonicalGitRoot(ctx, path)
	if err != nil {
		return Repo{}, err
	}

	var repo Repo
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if projectID > 0 {
			if _, err := requireProjectName(ctx, tx, projectID); err != nil {
				return err
			}
		}

		var existingID int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM repos WHERE path = ?`, root).Scan(&existingID)
		if err == nil {
			return fmt.Errorf("%w: repository %q is already registered as repo %d", ErrConflict, root, existingID)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("store: check repository path: %w", err)
		}

		resolvedProjectID := projectID
		if resolvedProjectID == 0 {
			name, err := autoProjectName(ctx, tx, root)
			if err != nil {
				return err
			}
			project, err := insertProjectTx(ctx, tx, actor, name)
			if err != nil {
				return err
			}
			resolvedProjectID = project.ID
		}

		result, err := tx.ExecContext(ctx, `INSERT INTO repos (path) VALUES (?)`, root)
		if err != nil {
			return fmt.Errorf("store: add repository: %w", mapConstraintError(err))
		}
		id, err := result.LastInsertId()
		if err != nil {
			return fmt.Errorf("store: read repository ID: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO project_repos (project_id, repo_id) VALUES (?, ?)`, resolvedProjectID, id,
		); err != nil {
			return fmt.Errorf("store: link repository %d to project %d: %w", id, resolvedProjectID, mapConstraintError(err))
		}

		repo = Repo{ID: id, Path: root, ProjectIDs: []int64{resolvedProjectID}}
		_, err = appendActivity(ctx, tx, activityInput{
			Type:      ActivityRepoAdded,
			Actor:     actor,
			ProjectID: int64Pointer(resolvedProjectID),
			Data:      map[string]any{"path": root},
		})
		return err
	})
	if err != nil {
		return Repo{}, err
	}
	return repo, nil
}

// LinkRepo links a registered repository to another project. Linking an
// already-linked pair is a no-op without an audit event.
func (s *Store) LinkRepo(ctx context.Context, actor string, repoID, projectID int64) (Repo, error) {
	actor, err := normalizeActor(actor)
	if err != nil {
		return Repo{}, err
	}
	if err := validateID("repository", repoID); err != nil {
		return Repo{}, err
	}
	if err := validateID("project", projectID); err != nil {
		return Repo{}, err
	}

	var linked Repo
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		repo, err := repoByID(ctx, tx, repoID)
		if err != nil {
			return err
		}
		if _, err := requireProjectName(ctx, tx, projectID); err != nil {
			return err
		}
		present, err := projectRepoLinked(ctx, tx, projectID, repoID)
		if err != nil {
			return err
		}
		if present {
			linked = repo
			return nil
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO project_repos (project_id, repo_id) VALUES (?, ?)`, projectID, repoID,
		); err != nil {
			return fmt.Errorf("store: link repository %d to project %d: %w", repoID, projectID, mapConstraintError(err))
		}
		if _, err := appendActivity(ctx, tx, activityInput{
			Type:      ActivityRepoLinked,
			Actor:     actor,
			ProjectID: int64Pointer(projectID),
			Data:      map[string]any{"repo_id": repoID, "path": repo.Path},
		}); err != nil {
			return err
		}
		linked, err = repoByID(ctx, tx, repoID)
		return err
	})
	if err != nil {
		return Repo{}, err
	}
	return linked, nil
}

// UnlinkRepo removes a repository's link to a project. Every repository must
// stay linked to at least one project, and a link cannot be removed while a
// task in that project targets the repository. Unlinking an absent link is a
// no-op without an audit event.
func (s *Store) UnlinkRepo(ctx context.Context, actor string, repoID, projectID int64) (Repo, error) {
	actor, err := normalizeActor(actor)
	if err != nil {
		return Repo{}, err
	}
	if err := validateID("repository", repoID); err != nil {
		return Repo{}, err
	}
	if err := validateID("project", projectID); err != nil {
		return Repo{}, err
	}

	var unlinked Repo
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		repo, err := repoByID(ctx, tx, repoID)
		if err != nil {
			return err
		}
		if _, err := requireProjectName(ctx, tx, projectID); err != nil {
			return err
		}
		present, err := projectRepoLinked(ctx, tx, projectID, repoID)
		if err != nil {
			return err
		}
		if !present {
			unlinked = repo
			return nil
		}
		if len(repo.ProjectIDs) <= 1 {
			return fmt.Errorf("%w: repository %d must remain linked to at least one project", ErrConflict, repoID)
		}
		var tasks int64
		if err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM tasks WHERE project_id = ? AND repo_id = ?`, projectID, repoID,
		).Scan(&tasks); err != nil {
			return fmt.Errorf("store: count repository tasks in project %d: %w", projectID, err)
		}
		if tasks > 0 {
			return fmt.Errorf("%w: repository %d still has tasks in project %d", ErrConflict, repoID, projectID)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM project_repos WHERE project_id = ? AND repo_id = ?`, projectID, repoID,
		); err != nil {
			return fmt.Errorf("store: unlink repository %d from project %d: %w", repoID, projectID, err)
		}
		if _, err := appendActivity(ctx, tx, activityInput{
			Type:      ActivityRepoUnlinked,
			Actor:     actor,
			ProjectID: int64Pointer(projectID),
			Data:      map[string]any{"repo_id": repoID, "path": repo.Path},
		}); err != nil {
			return err
		}
		unlinked, err = repoByID(ctx, tx, repoID)
		return err
	})
	if err != nil {
		return Repo{}, err
	}
	return unlinked, nil
}

// RemoveRepo removes a repository and every project link when no task targets
// it. The removal is audited.
func (s *Store) RemoveRepo(ctx context.Context, actor string, repoID int64) error {
	actor, err := normalizeActor(actor)
	if err != nil {
		return err
	}
	if err := validateID("repository", repoID); err != nil {
		return err
	}

	return s.withTx(ctx, func(tx *sql.Tx) error {
		repo, err := repoByID(ctx, tx, repoID)
		if err != nil {
			return err
		}
		var tasks int64
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM tasks WHERE repo_id = ?`, repoID).Scan(&tasks); err != nil {
			return fmt.Errorf("store: count repository tasks: %w", err)
		}
		if tasks > 0 {
			return fmt.Errorf("%w: repository %d still has dependent tasks", ErrConflict, repoID)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM project_repos WHERE repo_id = ?`, repoID); err != nil {
			return fmt.Errorf("store: remove repository %d links: %w", repoID, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM repos WHERE id = ?`, repoID); err != nil {
			return fmt.Errorf("store: remove repository %d: %w", repoID, err)
		}

		var projectID *int64
		if len(repo.ProjectIDs) == 1 {
			projectID = int64Pointer(repo.ProjectIDs[0])
		}
		_, err = appendActivity(ctx, tx, activityInput{
			Type:      ActivityRepoRemoved,
			Actor:     actor,
			ProjectID: projectID,
			Data: map[string]any{
				"repo_id":     repoID,
				"path":        repo.Path,
				"project_ids": repo.ProjectIDs,
			},
		})
		return err
	})
}

// ListRepos returns repositories in registration order. A positive projectID
// restricts the result to repositories linked to that project; zero lists every
// repository. Each repo carries all of its project IDs, sorted ascending.
func (s *Store) ListRepos(ctx context.Context, projectID int64) ([]Repo, error) {
	if projectID < 0 {
		return nil, fmt.Errorf("%w: project must be a non-negative ID", ErrInvalid)
	}

	query := `
		SELECT r.id, r.path, links.project_id
		FROM repos r
		LEFT JOIN project_repos links ON links.repo_id = r.id`
	args := []any{}
	if projectID > 0 {
		query += ` WHERE r.id IN (SELECT repo_id FROM project_repos WHERE project_id = ?)`
		args = append(args, projectID)
	}
	query += ` ORDER BY r.id, links.project_id`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list repositories: %w", err)
	}
	defer rows.Close()

	repos := []Repo{}
	for rows.Next() {
		var (
			repoID int64
			path   string
			linkID sql.NullInt64
		)
		if err := rows.Scan(&repoID, &path, &linkID); err != nil {
			return nil, fmt.Errorf("store: scan repository: %w", err)
		}
		if len(repos) == 0 || repos[len(repos)-1].ID != repoID {
			repos = append(repos, Repo{ID: repoID, Path: path, ProjectIDs: []int64{}})
		}
		if linkID.Valid {
			last := &repos[len(repos)-1]
			last.ProjectIDs = append(last.ProjectIDs, linkID.Int64)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list repositories: %w", err)
	}
	return repos, nil
}

func repoByID(ctx context.Context, tx *sql.Tx, repoID int64) (Repo, error) {
	var path string
	err := tx.QueryRowContext(ctx, `SELECT path FROM repos WHERE id = ?`, repoID).Scan(&path)
	if errors.Is(err, sql.ErrNoRows) {
		return Repo{}, fmt.Errorf("%w: repository %d", ErrNotFound, repoID)
	}
	if err != nil {
		return Repo{}, fmt.Errorf("store: load repository %d: %w", repoID, err)
	}
	projectIDs, err := loadProjectIDs(ctx, tx, repoID)
	if err != nil {
		return Repo{}, err
	}
	return Repo{ID: repoID, Path: path, ProjectIDs: projectIDs}, nil
}

func loadProjectIDs(ctx context.Context, tx *sql.Tx, repoID int64) ([]int64, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT project_id FROM project_repos WHERE repo_id = ? ORDER BY project_id`, repoID,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list repository projects: %w", err)
	}
	defer rows.Close()

	projectIDs := []int64{}
	for rows.Next() {
		var projectID int64
		if err := rows.Scan(&projectID); err != nil {
			return nil, fmt.Errorf("store: scan repository project: %w", err)
		}
		projectIDs = append(projectIDs, projectID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list repository projects: %w", err)
	}
	return projectIDs, nil
}

func projectRepoLinked(ctx context.Context, tx *sql.Tx, projectID, repoID int64) (bool, error) {
	var marker int
	err := tx.QueryRowContext(ctx,
		`SELECT 1 FROM project_repos WHERE project_id = ? AND repo_id = ?`, projectID, repoID,
	).Scan(&marker)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: check repository link: %w", err)
	}
	return true, nil
}

func autoProjectName(ctx context.Context, tx *sql.Tx, root string) (string, error) {
	base := strings.TrimSpace(filepath.Base(root))
	if base == "" {
		base = "project"
	}
	taken, err := projectNameTaken(ctx, tx, base)
	if err != nil {
		return "", err
	}
	if !taken {
		return base, nil
	}

	parent := strings.TrimSpace(filepath.Base(filepath.Dir(root)))
	if parent != "" && parent != "." && parent != string(filepath.Separator) {
		candidate := filepath.Join(parent, base)
		taken, err := projectNameTaken(ctx, tx, candidate)
		if err != nil {
			return "", err
		}
		if !taken {
			return candidate, nil
		}
	}

	for suffix := 2; ; suffix++ {
		candidate := fmt.Sprintf("%s-%d", base, suffix)
		taken, err := projectNameTaken(ctx, tx, candidate)
		if err != nil {
			return "", err
		}
		if !taken {
			return candidate, nil
		}
	}
}

func canonicalGitRoot(ctx context.Context, path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("%w: repository path is required", ErrInvalid)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("%w: resolve repository path: %v", ErrInvalid, err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("%w: resolve repository path: %v", ErrInvalid, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("%w: inspect repository path: %v", ErrInvalid, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: repository path is not a directory", ErrInvalid)
	}

	command := exec.CommandContext(ctx, "git", "-C", resolved, "rev-parse", "--show-toplevel")
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%w: %q is not inside a Git work tree: %s", ErrInvalid, path, strings.TrimSpace(string(output)))
	}
	root := strings.TrimSpace(string(output))
	if root == "" {
		return "", fmt.Errorf("%w: git did not report a top-level directory", ErrInvalid)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("%w: resolve Git root: %v", ErrInvalid, err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("%w: resolve Git root: %v", ErrInvalid, err)
	}
	info, err = os.Stat(root)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("%w: Git root is not an accessible directory", ErrInvalid)
	}
	return root, nil
}
