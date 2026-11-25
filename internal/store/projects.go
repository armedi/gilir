package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// CreateProject creates a project with a nonblank, trimmed, unique name.
func (s *Store) CreateProject(ctx context.Context, actor, name string) (Project, error) {
	actor, err := normalizeActor(actor)
	if err != nil {
		return Project{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return Project{}, fmt.Errorf("%w: project name is required", ErrInvalid)
	}

	var created Project
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		taken, err := projectNameTaken(ctx, tx, name)
		if err != nil {
			return err
		}
		if taken {
			return fmt.Errorf("%w: project name %q is already in use", ErrConflict, name)
		}
		created, err = insertProjectTx(ctx, tx, actor, name)
		return err
	})
	if err != nil {
		return Project{}, err
	}
	return created, nil
}

// RenameProject changes a project's name to a nonblank, trimmed, unique name.
// Renaming to the current name is a no-op without an audit event.
func (s *Store) RenameProject(ctx context.Context, actor string, projectID int64, name string) (Project, error) {
	actor, err := normalizeActor(actor)
	if err != nil {
		return Project{}, err
	}
	if err := validateID("project", projectID); err != nil {
		return Project{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return Project{}, fmt.Errorf("%w: project name is required", ErrInvalid)
	}

	var renamed Project
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		oldName, err := requireProjectName(ctx, tx, projectID)
		if err != nil {
			return err
		}
		renamed = Project{ID: projectID, Name: name}
		if oldName == name {
			return nil
		}
		taken, err := projectNameTaken(ctx, tx, name)
		if err != nil {
			return err
		}
		if taken {
			return fmt.Errorf("%w: project name %q is already in use", ErrConflict, name)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE projects SET name = ? WHERE id = ?`, name, projectID); err != nil {
			return fmt.Errorf("store: rename project %d: %w", projectID, mapConstraintError(err))
		}
		_, err = appendActivity(ctx, tx, activityInput{
			Type:      ActivityProjectRenamed,
			Actor:     actor,
			ProjectID: int64Pointer(projectID),
			Data: map[string]any{
				"old": map[string]any{"name": oldName},
				"new": map[string]any{"name": name},
			},
		})
		return err
	})
	if err != nil {
		return Project{}, err
	}
	return renamed, nil
}

// DeleteProject deletes a project that has no tasks and whose repositories all
// remain linked to at least one other project. Dependent work, orphaned
// repositories, and their audit trail are never discarded.
func (s *Store) DeleteProject(ctx context.Context, actor string, projectID int64) error {
	actor, err := normalizeActor(actor)
	if err != nil {
		return err
	}
	if err := validateID("project", projectID); err != nil {
		return err
	}

	return s.withTx(ctx, func(tx *sql.Tx) error {
		name, err := requireProjectName(ctx, tx, projectID)
		if err != nil {
			return err
		}

		var tasks int64
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM tasks WHERE project_id = ?`, projectID).Scan(&tasks); err != nil {
			return fmt.Errorf("store: count project tasks: %w", err)
		}
		if tasks > 0 {
			return fmt.Errorf("%w: project %d still has dependent tasks", ErrConflict, projectID)
		}

		var orphaned int64
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*)
			FROM project_repos current
			WHERE current.project_id = ?
				AND NOT EXISTS (
					SELECT 1 FROM project_repos other
					WHERE other.repo_id = current.repo_id
						AND other.project_id <> current.project_id
				)`, projectID,
		).Scan(&orphaned); err != nil {
			return fmt.Errorf("store: count project repositories: %w", err)
		}
		if orphaned > 0 {
			return fmt.Errorf("%w: project %d owns the only link for %d repositories", ErrConflict, projectID, orphaned)
		}

		if _, err := tx.ExecContext(ctx, `DELETE FROM project_repos WHERE project_id = ?`, projectID); err != nil {
			return fmt.Errorf("store: unlink project %d repositories: %w", projectID, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM projects WHERE id = ?`, projectID); err != nil {
			return fmt.Errorf("store: delete project %d: %w", projectID, err)
		}
		_, err = appendActivity(ctx, tx, activityInput{
			Type:      ActivityProjectDeleted,
			Actor:     actor,
			ProjectID: int64Pointer(projectID),
			Data:      map[string]any{"name": name},
		})
		return err
	})
}

// ListProjects returns all projects in creation order.
func (s *Store) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name FROM projects ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: list projects: %w", err)
	}
	defer rows.Close()

	projects := []Project{}
	for rows.Next() {
		var project Project
		if err := rows.Scan(&project.ID, &project.Name); err != nil {
			return nil, fmt.Errorf("store: scan project: %w", err)
		}
		projects = append(projects, project)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list projects: %w", err)
	}
	return projects, nil
}

func insertProjectTx(ctx context.Context, tx *sql.Tx, actor, name string) (Project, error) {
	result, err := tx.ExecContext(ctx, `INSERT INTO projects (name) VALUES (?)`, name)
	if err != nil {
		return Project{}, fmt.Errorf("store: create project: %w", mapConstraintError(err))
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Project{}, fmt.Errorf("store: read project ID: %w", err)
	}
	created := Project{ID: id, Name: name}
	_, err = appendActivity(ctx, tx, activityInput{
		Type:      ActivityProjectCreated,
		Actor:     actor,
		ProjectID: int64Pointer(id),
		Data:      map[string]any{"name": name},
	})
	if err != nil {
		return Project{}, err
	}
	return created, nil
}

func requireProjectName(ctx context.Context, tx *sql.Tx, projectID int64) (string, error) {
	var name string
	err := tx.QueryRowContext(ctx, `SELECT name FROM projects WHERE id = ?`, projectID).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: project %d", ErrNotFound, projectID)
	}
	if err != nil {
		return "", fmt.Errorf("store: load project %d: %w", projectID, err)
	}
	return name, nil
}

func projectNameTaken(ctx context.Context, tx *sql.Tx, name string) (bool, error) {
	var marker int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM projects WHERE name = ?`, name).Scan(&marker)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: check project name: %w", err)
	}
	return true, nil
}
