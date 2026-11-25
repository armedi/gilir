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

// AddRepo registers the canonical Git root containing path in a project. The
// input may be any directory inside the work tree, including a symlinked path;
// the stored path is the resolved absolute Git top level. A canonical root may
// be registered at most once per project.
func (s *Store) AddRepo(ctx context.Context, actor string, projectID int64, path string) (Repo, error) {
	actor, err := normalizeActor(actor)
	if err != nil {
		return Repo{}, err
	}
	if err := validateID("project", projectID); err != nil {
		return Repo{}, err
	}

	root, err := canonicalGitRoot(ctx, path)
	if err != nil {
		return Repo{}, err
	}

	var repo Repo
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		var marker int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM projects WHERE id = ?`, projectID).Scan(&marker)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: project %d", ErrNotFound, projectID)
		}
		if err != nil {
			return fmt.Errorf("store: load project %d: %w", projectID, err)
		}

		var existingID int64
		err = tx.QueryRowContext(ctx,
			`SELECT id FROM repos WHERE project_id = ? AND path = ?`, projectID, root,
		).Scan(&existingID)
		if err == nil {
			return fmt.Errorf("%w: repository %q is already registered in project %d", ErrConflict, root, projectID)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("store: check repository path: %w", err)
		}

		result, err := tx.ExecContext(ctx,
			`INSERT INTO repos (project_id, path) VALUES (?, ?)`, projectID, root,
		)
		if err != nil {
			return fmt.Errorf("store: add repository: %w", mapConstraintError(err))
		}
		id, err := result.LastInsertId()
		if err != nil {
			return fmt.Errorf("store: read repository ID: %w", err)
		}
		repo = Repo{ID: id, ProjectID: projectID, Path: root}
		_, err = appendActivity(ctx, tx, activityInput{
			Type:      ActivityRepoAdded,
			Actor:     actor,
			ProjectID: int64Pointer(projectID),
			Data:      map[string]any{"path": root},
		})
		return err
	})
	if err != nil {
		return Repo{}, err
	}
	return repo, nil
}

// RemoveRepo removes a repository that no task targets. The removal is audited.
func (s *Store) RemoveRepo(ctx context.Context, actor string, repoID int64) error {
	actor, err := normalizeActor(actor)
	if err != nil {
		return err
	}
	if err := validateID("repository", repoID); err != nil {
		return err
	}

	return s.withTx(ctx, func(tx *sql.Tx) error {
		var projectID int64
		var path string
		err := tx.QueryRowContext(ctx, `SELECT project_id, path FROM repos WHERE id = ?`, repoID).Scan(&projectID, &path)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: repository %d", ErrNotFound, repoID)
		}
		if err != nil {
			return fmt.Errorf("store: load repository %d: %w", repoID, err)
		}

		var tasks int64
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM tasks WHERE repo_id = ?`, repoID).Scan(&tasks); err != nil {
			return fmt.Errorf("store: count repository tasks: %w", err)
		}
		if tasks > 0 {
			return fmt.Errorf("%w: repository %d still has dependent tasks", ErrConflict, repoID)
		}

		if _, err := tx.ExecContext(ctx, `DELETE FROM repos WHERE id = ?`, repoID); err != nil {
			return fmt.Errorf("store: remove repository %d: %w", repoID, err)
		}
		_, err = appendActivity(ctx, tx, activityInput{
			Type:      ActivityRepoRemoved,
			Actor:     actor,
			ProjectID: int64Pointer(projectID),
			Data:      map[string]any{"path": path, "repo_id": repoID},
		})
		return err
	})
}

// ListRepos returns a project's repositories in registration order.
func (s *Store) ListRepos(ctx context.Context, projectID int64) ([]Repo, error) {
	if err := validateID("project", projectID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, project_id, path FROM repos WHERE project_id = ? ORDER BY id`, projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list repositories: %w", err)
	}
	defer rows.Close()

	repos := []Repo{}
	for rows.Next() {
		var repo Repo
		if err := rows.Scan(&repo.ID, &repo.ProjectID, &repo.Path); err != nil {
			return nil, fmt.Errorf("store: scan repository: %w", err)
		}
		repos = append(repos, repo)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list repositories: %w", err)
	}
	return repos, nil
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
