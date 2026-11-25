package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// CreateProject creates a project with a nonblank name.
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
		result, err := tx.ExecContext(ctx, `INSERT INTO projects (name) VALUES (?)`, name)
		if err != nil {
			return fmt.Errorf("store: create project: %w", err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			return fmt.Errorf("store: read project ID: %w", err)
		}
		created = Project{ID: id, Name: name}
		_, err = appendActivity(ctx, tx, activityInput{
			Type:      ActivityProjectCreated,
			Actor:     actor,
			ProjectID: int64Pointer(id),
			Data:      map[string]any{"name": name},
		})
		return err
	})
	if err != nil {
		return Project{}, err
	}
	return created, nil
}

// RenameProject changes a project's name. Renaming to the current name is a
// no-op without an audit event.
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
		var oldName string
		err := tx.QueryRowContext(ctx, `SELECT name FROM projects WHERE id = ?`, projectID).Scan(&oldName)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: project %d", ErrNotFound, projectID)
		}
		if err != nil {
			return fmt.Errorf("store: load project %d: %w", projectID, err)
		}
		renamed = Project{ID: projectID, Name: name}
		if oldName == name {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE projects SET name = ? WHERE id = ?`, name, projectID); err != nil {
			return fmt.Errorf("store: rename project %d: %w", projectID, err)
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

// DeleteProject deletes an empty project. Projects with repositories or tasks
// are refused so dependent work and its audit trail are never discarded.
func (s *Store) DeleteProject(ctx context.Context, actor string, projectID int64) error {
	actor, err := normalizeActor(actor)
	if err != nil {
		return err
	}
	if err := validateID("project", projectID); err != nil {
		return err
	}

	return s.withTx(ctx, func(tx *sql.Tx) error {
		var name string
		err := tx.QueryRowContext(ctx, `SELECT name FROM projects WHERE id = ?`, projectID).Scan(&name)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: project %d", ErrNotFound, projectID)
		}
		if err != nil {
			return fmt.Errorf("store: load project %d: %w", projectID, err)
		}

		var repos, tasks int64
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM repos WHERE project_id = ?`, projectID).Scan(&repos); err != nil {
			return fmt.Errorf("store: count project repositories: %w", err)
		}
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM tasks WHERE project_id = ?`, projectID).Scan(&tasks); err != nil {
			return fmt.Errorf("store: count project tasks: %w", err)
		}
		if repos > 0 || tasks > 0 {
			return fmt.Errorf("%w: project %d still has dependent work", ErrConflict, projectID)
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
