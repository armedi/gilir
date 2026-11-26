package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const taskColumns = `id, project_id, repo_id, title, description, status, priority, created_at, updated_at`

func scanTask(sc scanner) (Task, error) {
	var (
		task      Task
		status    string
		priority  string
		createdAt string
		updatedAt string
	)
	if err := sc.Scan(
		&task.ID, &task.ProjectID, &task.RepoID, &task.Title, &task.Description,
		&status, &priority, &createdAt, &updatedAt,
	); err != nil {
		return Task{}, err
	}
	task.Status = Status(status)
	if !task.Status.Valid() {
		return Task{}, fmt.Errorf("store: task %d has unsupported status %q", task.ID, status)
	}
	task.Priority = Priority(priority)
	if !task.Priority.Valid() {
		return Task{}, fmt.Errorf("store: task %d has unsupported priority %q", task.ID, priority)
	}
	var err error
	task.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return Task{}, fmt.Errorf("store: task %d created_at: %w", task.ID, err)
	}
	task.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return Task{}, fmt.Errorf("store: task %d updated_at: %w", task.ID, err)
	}
	task.Markings = []string{}
	return task, nil
}

func loadMarkings(ctx context.Context, tx *sql.Tx, taskID int64) ([]string, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT marking FROM task_markings WHERE task_id = ? ORDER BY marking`, taskID,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list task markings: %w", err)
	}
	defer rows.Close()

	markings := []string{}
	for rows.Next() {
		var marking string
		if err := rows.Scan(&marking); err != nil {
			return nil, fmt.Errorf("store: scan task marking: %w", err)
		}
		markings = append(markings, marking)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list task markings: %w", err)
	}
	return markings, nil
}

func taskByID(ctx context.Context, tx *sql.Tx, taskID int64) (Task, error) {
	task, err := scanTask(tx.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id = ?`, taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, fmt.Errorf("%w: task %d", ErrNotFound, taskID)
	}
	if err != nil {
		return Task{}, fmt.Errorf("store: load task %d: %w", taskID, err)
	}
	task.Markings, err = loadMarkings(ctx, tx, taskID)
	if err != nil {
		return Task{}, err
	}
	return task, nil
}

func normalizeMarking(marking string) (string, error) {
	marking = strings.TrimSpace(marking)
	if marking == "" {
		return "", fmt.Errorf("%w: marking is required", ErrInvalid)
	}
	return marking, nil
}

// setMarkingTx adds or removes a marking inside an existing transaction. It
// returns false without writing an event when the requested state already
// holds.
func setMarkingTx(ctx context.Context, tx *sql.Tx, actor string, taskID int64, marking string, present bool, at time.Time) (bool, error) {
	marking, err := normalizeMarking(marking)
	if err != nil {
		return false, err
	}
	var marker int
	err = tx.QueryRowContext(ctx,
		`SELECT 1 FROM task_markings WHERE task_id = ? AND marking = ?`, taskID, marking,
	).Scan(&marker)
	current := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("store: load task marking: %w", err)
	}
	if current == present {
		return false, nil
	}

	if present {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO task_markings (task_id, marking, created_at) VALUES (?, ?, ?)`,
			taskID, marking, formatTime(at),
		); err != nil {
			return false, fmt.Errorf("store: add task marking: %w", err)
		}
	} else {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM task_markings WHERE task_id = ? AND marking = ?`, taskID, marking,
		); err != nil {
			return false, fmt.Errorf("store: remove task marking: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE tasks SET updated_at = ? WHERE id = ?`, formatTime(at), taskID,
	); err != nil {
		return false, fmt.Errorf("store: touch task after marking change: %w", err)
	}
	projectID, err := requireTask(ctx, tx, taskID)
	if err != nil {
		return false, err
	}
	_, err = appendActivity(ctx, tx, activityInput{
		Type:      ActivityTaskMarking,
		Actor:     actor,
		TaskID:    int64Pointer(taskID),
		ProjectID: int64Pointer(projectID),
		Timestamp: at,
		Data: map[string]any{
			"marking": marking,
			"old":     current,
			"new":     present,
		},
	})
	if err != nil {
		return false, err
	}
	return true, nil
}

// CreateTask creates a task targeting one repository in its project. A blank
// status defaults to backlog and a blank priority defaults to medium.
func (s *Store) CreateTask(ctx context.Context, actor string, in CreateTaskParams) (Task, error) {
	actor, err := normalizeActor(actor)
	if err != nil {
		return Task{}, err
	}
	if in.ProjectID < 0 {
		return Task{}, fmt.Errorf("%w: project must be a non-negative ID", ErrInvalid)
	}
	if err := validateID("repository", in.RepoID); err != nil {
		return Task{}, err
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return Task{}, fmt.Errorf("%w: task title is required", ErrInvalid)
	}
	status := in.Status
	if status == "" {
		status = StatusBacklog
	}
	if !status.Valid() {
		return Task{}, fmt.Errorf("%w: unsupported task status %q", ErrInvalid, status)
	}
	priority := in.Priority
	if priority == "" {
		priority = PriorityMedium
	}
	if !priority.Valid() {
		return Task{}, fmt.Errorf("%w: unsupported task priority %q", ErrInvalid, priority)
	}

	var created Task
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		projectID := in.ProjectID
		if projectID > 0 {
			if _, err := requireProjectName(ctx, tx, projectID); err != nil {
				return err
			}
		}
		repo, err := repoByID(ctx, tx, in.RepoID)
		if err != nil {
			return err
		}
		if projectID == 0 {
			switch len(repo.ProjectIDs) {
			case 1:
				projectID = repo.ProjectIDs[0]
			case 0:
				return fmt.Errorf("%w: repository %d is not linked to any project", ErrConflict, in.RepoID)
			default:
				return fmt.Errorf("%w: project_id is required: repository %d belongs to projects %v",
					ErrInvalid, in.RepoID, repo.ProjectIDs)
			}
		} else {
			linked, err := projectRepoLinked(ctx, tx, projectID, in.RepoID)
			if err != nil {
				return err
			}
			if !linked {
				return fmt.Errorf("%w: repository %d does not belong to project %d", ErrInvalid, in.RepoID, projectID)
			}
		}

		now := nowUTC()
		result, err := tx.ExecContext(ctx, `
			INSERT INTO tasks (project_id, repo_id, title, description, status, priority, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			projectID, in.RepoID, title, in.Description, string(status), string(priority),
			formatTime(now), formatTime(now),
		)
		if err != nil {
			return fmt.Errorf("store: create task: %w", mapConstraintError(err))
		}
		id, err := result.LastInsertId()
		if err != nil {
			return fmt.Errorf("store: read task ID: %w", err)
		}
		created = Task{
			ID:          id,
			ProjectID:   projectID,
			RepoID:      in.RepoID,
			Title:       title,
			Description: in.Description,
			Status:      status,
			Priority:    priority,
			CreatedAt:   now,
			UpdatedAt:   now,
			Markings:    []string{},
		}
		_, err = appendActivity(ctx, tx, activityInput{
			Type:      ActivityTaskCreated,
			Actor:     actor,
			TaskID:    int64Pointer(id),
			ProjectID: int64Pointer(projectID),
			Timestamp: now,
			Data: map[string]any{
				"repo_id":     in.RepoID,
				"title":       title,
				"description": in.Description,
				"status":      string(status),
				"priority":    string(priority),
			},
		})
		return err
	})
	if err != nil {
		return Task{}, err
	}
	return created, nil
}

// UpdateTask applies a partial edit. Nil patch fields are unchanged. A no-op
// patch writes no audit event. Moving a task from in-review back to todo adds
// the changes-requested marking in the same transaction.
func (s *Store) UpdateTask(ctx context.Context, actor string, taskID int64, patch TaskPatch) (Task, error) {
	actor, err := normalizeActor(actor)
	if err != nil {
		return Task{}, err
	}
	if err := validateID("task", taskID); err != nil {
		return Task{}, err
	}

	if patch.Title != nil {
		title := strings.TrimSpace(*patch.Title)
		if title == "" {
			return Task{}, fmt.Errorf("%w: task title is required", ErrInvalid)
		}
		patch.Title = &title
	}
	if patch.Priority != nil && !patch.Priority.Valid() {
		return Task{}, fmt.Errorf("%w: unsupported task priority %q", ErrInvalid, *patch.Priority)
	}
	if patch.Status != nil && !patch.Status.Valid() {
		return Task{}, fmt.Errorf("%w: unsupported task status %q", ErrInvalid, *patch.Status)
	}
	if patch.RepoID != nil {
		if err := validateID("repository", *patch.RepoID); err != nil {
			return Task{}, err
		}
	}

	var updated Task
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		old, err := taskByID(ctx, tx, taskID)
		if err != nil {
			return err
		}

		newTitle := old.Title
		newDescription := old.Description
		newPriority := old.Priority
		newRepoID := old.RepoID
		newStatus := old.Status
		if patch.Title != nil {
			newTitle = *patch.Title
		}
		if patch.Description != nil {
			newDescription = *patch.Description
		}
		if patch.Priority != nil {
			newPriority = *patch.Priority
		}
		if patch.RepoID != nil {
			newRepoID = *patch.RepoID
		}
		if patch.Status != nil {
			newStatus = *patch.Status
		}

		repoChanged := newRepoID != old.RepoID
		if repoChanged {
			var openAttempts int64
			if err := tx.QueryRowContext(ctx,
				`SELECT count(*) FROM dispatch_attempts WHERE task_id = ? AND state = 'reserved'`,
				taskID,
			).Scan(&openAttempts); err != nil {
				return fmt.Errorf("store: count open task attempts: %w", err)
			}
			if openAttempts > 0 {
				return fmt.Errorf("%w: task %d has an outstanding dispatch attempt", ErrConflict, taskID)
			}
			linked, err := projectRepoLinked(ctx, tx, old.ProjectID, newRepoID)
			if err != nil {
				return err
			}
			if !linked {
				return fmt.Errorf("%w: repository %d does not belong to project %d", ErrInvalid, newRepoID, old.ProjectID)
			}
		}

		statusChanged := newStatus != old.Status
		titleChanged := newTitle != old.Title
		descriptionChanged := newDescription != old.Description
		priorityChanged := newPriority != old.Priority
		if !statusChanged && !titleChanged && !descriptionChanged && !priorityChanged && !repoChanged {
			updated = old
			return nil
		}

		now := nowUTC()
		if _, err := tx.ExecContext(ctx, `
			UPDATE tasks
			SET title = ?, description = ?, priority = ?, repo_id = ?, status = ?, updated_at = ?
			WHERE id = ?`,
			newTitle, newDescription, string(newPriority), newRepoID, string(newStatus), formatTime(now), taskID,
		); err != nil {
			return fmt.Errorf("store: update task %d: %w", taskID, err)
		}

		if titleChanged || descriptionChanged || priorityChanged || repoChanged {
			oldFields := map[string]any{}
			newFields := map[string]any{}
			if titleChanged {
				oldFields["title"] = old.Title
				newFields["title"] = newTitle
			}
			if descriptionChanged {
				oldFields["description"] = old.Description
				newFields["description"] = newDescription
			}
			if priorityChanged {
				oldFields["priority"] = string(old.Priority)
				newFields["priority"] = string(newPriority)
			}
			if repoChanged {
				oldFields["repo_id"] = old.RepoID
				newFields["repo_id"] = newRepoID
			}
			if _, err := appendActivity(ctx, tx, activityInput{
				Type:      ActivityTaskUpdated,
				Actor:     actor,
				TaskID:    int64Pointer(taskID),
				ProjectID: int64Pointer(old.ProjectID),
				Timestamp: now,
				Data:      map[string]any{"old": oldFields, "new": newFields},
			}); err != nil {
				return err
			}
		}

		if statusChanged {
			if _, err := appendActivity(ctx, tx, activityInput{
				Type:      ActivityTaskStatusChanged,
				Actor:     actor,
				TaskID:    int64Pointer(taskID),
				ProjectID: int64Pointer(old.ProjectID),
				Timestamp: now,
				Data: map[string]any{
					"old": string(old.Status),
					"new": string(newStatus),
				},
			}); err != nil {
				return err
			}
			if old.Status == StatusInReview && newStatus == StatusTodo {
				if _, err := setMarkingTx(ctx, tx, actor, taskID, MarkingChangesRequested, true, now); err != nil {
					return err
				}
			}
		}

		updated, err = taskByID(ctx, tx, taskID)
		return err
	})
	if err != nil {
		return Task{}, err
	}
	return updated, nil
}

// SetMarking adds or removes a task marking. A duplicate add or remove is a
// no-op without an audit event.
func (s *Store) SetMarking(ctx context.Context, actor string, taskID int64, marking string, present bool) (Task, error) {
	actor, err := normalizeActor(actor)
	if err != nil {
		return Task{}, err
	}
	if err := validateID("task", taskID); err != nil {
		return Task{}, err
	}
	marking, err = normalizeMarking(marking)
	if err != nil {
		return Task{}, err
	}

	var task Task
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := taskByID(ctx, tx, taskID); err != nil {
			return err
		}
		if _, err := setMarkingTx(ctx, tx, actor, taskID, marking, present, nowUTC()); err != nil {
			return err
		}
		var err error
		task, err = taskByID(ctx, tx, taskID)
		return err
	})
	if err != nil {
		return Task{}, err
	}
	return task, nil
}

// SetDependency adds or removes a blocker relationship. Both tasks must belong
// to the same project. Adding a dependency that would create a cycle fails with
// ErrConflict. Duplicate adds and removes are no-ops without audit events.
func (s *Store) SetDependency(ctx context.Context, actor string, taskID, dependsOnTaskID int64, present bool) error {
	actor, err := normalizeActor(actor)
	if err != nil {
		return err
	}
	if err := validateID("task", taskID); err != nil {
		return err
	}
	if err := validateID("blocker task", dependsOnTaskID); err != nil {
		return err
	}
	if taskID == dependsOnTaskID {
		return fmt.Errorf("%w: a task cannot depend on itself", ErrInvalid)
	}

	return s.withTx(ctx, func(tx *sql.Tx) error {
		projectID, err := requireTask(ctx, tx, taskID)
		if err != nil {
			return err
		}
		blockerProjectID, err := requireTask(ctx, tx, dependsOnTaskID)
		if err != nil {
			return err
		}
		if projectID != blockerProjectID {
			return fmt.Errorf("%w: dependencies must stay inside one project", ErrInvalid)
		}

		var marker int
		err = tx.QueryRowContext(ctx,
			`SELECT 1 FROM task_dependencies WHERE task_id = ? AND depends_on_id = ?`,
			taskID, dependsOnTaskID,
		).Scan(&marker)
		exists := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("store: load dependency: %w", err)
		}

		if present {
			if exists {
				return nil
			}
			cycle, err := dependencyCreatesCycle(ctx, tx, taskID, dependsOnTaskID)
			if err != nil {
				return err
			}
			if cycle {
				return fmt.Errorf("%w: dependency would create a cycle", ErrConflict)
			}
			now := nowUTC()
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO task_dependencies (task_id, depends_on_id, project_id, created_at) VALUES (?, ?, ?, ?)`,
				taskID, dependsOnTaskID, projectID, formatTime(now),
			); err != nil {
				return fmt.Errorf("store: add dependency: %w", mapConstraintError(err))
			}
			if _, err := tx.ExecContext(ctx,
				`UPDATE tasks SET updated_at = ? WHERE id = ?`, formatTime(now), taskID,
			); err != nil {
				return fmt.Errorf("store: touch task after dependency add: %w", err)
			}
			_, err = appendActivity(ctx, tx, activityInput{
				Type:      ActivityDependencyAdded,
				Actor:     actor,
				TaskID:    int64Pointer(taskID),
				ProjectID: int64Pointer(projectID),
				Timestamp: now,
				Data:      map[string]any{"depends_on_id": dependsOnTaskID},
			})
			return err
		}

		if !exists {
			return nil
		}
		now := nowUTC()
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM task_dependencies WHERE task_id = ? AND depends_on_id = ?`,
			taskID, dependsOnTaskID,
		); err != nil {
			return fmt.Errorf("store: remove dependency: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE tasks SET updated_at = ? WHERE id = ?`, formatTime(now), taskID,
		); err != nil {
			return fmt.Errorf("store: touch task after dependency remove: %w", err)
		}
		_, err = appendActivity(ctx, tx, activityInput{
			Type:      ActivityDependencyRemoved,
			Actor:     actor,
			TaskID:    int64Pointer(taskID),
			ProjectID: int64Pointer(projectID),
			Timestamp: now,
			Data:      map[string]any{"depends_on_id": dependsOnTaskID},
		})
		return err
	})
}

func dependencyCreatesCycle(ctx context.Context, tx *sql.Tx, taskID, dependsOnTaskID int64) (bool, error) {
	var found int
	err := tx.QueryRowContext(ctx, `
		WITH RECURSIVE reachable(id) AS (
			SELECT depends_on_id FROM task_dependencies WHERE task_id = ?
			UNION
			SELECT d.depends_on_id
			FROM task_dependencies d
			JOIN reachable r ON d.task_id = r.id
		)
		SELECT 1 FROM reachable WHERE id = ? LIMIT 1`,
		dependsOnTaskID, taskID,
	).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: check dependency cycle: %w", err)
	}
	return true, nil
}

// Comment appends a comment as an immutable task.commented activity record.
// Blank comments are rejected.
func (s *Store) Comment(ctx context.Context, actor string, taskID int64, body string) (Activity, error) {
	actor, err := normalizeActor(actor)
	if err != nil {
		return Activity{}, err
	}
	if err := validateID("task", taskID); err != nil {
		return Activity{}, err
	}
	if strings.TrimSpace(body) == "" {
		return Activity{}, fmt.Errorf("%w: comment body is required", ErrInvalid)
	}

	var comment Activity
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		projectID, err := requireTask(ctx, tx, taskID)
		if err != nil {
			return err
		}
		comment, err = appendActivity(ctx, tx, activityInput{
			Type:      ActivityTaskCommented,
			Actor:     actor,
			TaskID:    int64Pointer(taskID),
			ProjectID: int64Pointer(projectID),
			Data:      map[string]any{"body": body},
		})
		return err
	})
	if err != nil {
		return Activity{}, err
	}
	return comment, nil
}

// EditComment appends an edit event for an existing comment. The comment must
// be a task.commented event on taskID owned by actor. Removed comments cannot
// be edited.
func (s *Store) EditComment(ctx context.Context, actor string, taskID, commentID int64, body string) (Activity, error) {
	actor, err := normalizeActor(actor)
	if err != nil {
		return Activity{}, err
	}
	if err := validateID("task", taskID); err != nil {
		return Activity{}, err
	}
	if err := validateID("comment", commentID); err != nil {
		return Activity{}, err
	}
	if strings.TrimSpace(body) == "" {
		return Activity{}, fmt.Errorf("%w: comment body is required", ErrInvalid)
	}

	var edit Activity
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		projectID, err := requireTask(ctx, tx, taskID)
		if err != nil {
			return err
		}
		if err := requireCommentOwner(ctx, tx, actor, taskID, commentID); err != nil {
			return err
		}
		edit, err = appendActivity(ctx, tx, activityInput{
			Type:      ActivityTaskCommentEdited,
			Actor:     actor,
			TaskID:    int64Pointer(taskID),
			ProjectID: int64Pointer(projectID),
			Data:      map[string]any{"comment_id": commentID, "body": body},
		})
		return err
	})
	if err != nil {
		return Activity{}, err
	}
	return edit, nil
}

// RemoveComment appends a removal event for an existing comment. The comment
// must be a task.commented event on taskID owned by actor. Removing an already
// removed comment is a conflict.
func (s *Store) RemoveComment(ctx context.Context, actor string, taskID, commentID int64) (Activity, error) {
	actor, err := normalizeActor(actor)
	if err != nil {
		return Activity{}, err
	}
	if err := validateID("task", taskID); err != nil {
		return Activity{}, err
	}
	if err := validateID("comment", commentID); err != nil {
		return Activity{}, err
	}

	var removal Activity
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		projectID, err := requireTask(ctx, tx, taskID)
		if err != nil {
			return err
		}
		if err := requireCommentOwner(ctx, tx, actor, taskID, commentID); err != nil {
			return err
		}
		removal, err = appendActivity(ctx, tx, activityInput{
			Type:      ActivityTaskCommentRemoved,
			Actor:     actor,
			TaskID:    int64Pointer(taskID),
			ProjectID: int64Pointer(projectID),
			Data:      map[string]any{"comment_id": commentID},
		})
		return err
	})
	if err != nil {
		return Activity{}, err
	}
	return removal, nil
}

// requireCommentOwner verifies that commentID is a task.commented event on
// taskID owned by actor and has not already been removed.
func requireCommentOwner(ctx context.Context, tx *sql.Tx, actor string, taskID, commentID int64) error {
	comment, err := activityByID(ctx, tx, commentID)
	if err != nil {
		return err
	}
	if comment.Type != ActivityTaskCommented || comment.TaskID == nil ||
		*comment.TaskID != taskID || comment.Actor != actor {
		return fmt.Errorf("%w: comment %d", ErrNotFound, commentID)
	}
	removed, err := commentRemoved(ctx, tx, taskID, commentID)
	if err != nil {
		return err
	}
	if removed {
		return fmt.Errorf("%w: comment %d has been removed", ErrConflict, commentID)
	}
	return nil
}

func activityByID(ctx context.Context, tx *sql.Tx, id int64) (Activity, error) {
	activity, err := scanActivity(tx.QueryRowContext(ctx, `
		SELECT id, type, actor, task_id, project_id, timestamp, data
		FROM activity
		WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Activity{}, fmt.Errorf("%w: activity %d", ErrNotFound, id)
	}
	if err != nil {
		return Activity{}, fmt.Errorf("store: load activity %d: %w", id, err)
	}
	return activity, nil
}

func commentRemoved(ctx context.Context, tx *sql.Tx, taskID, commentID int64) (bool, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, type, actor, task_id, project_id, timestamp, data
		FROM activity
		WHERE task_id = ? AND type = ?
		ORDER BY id`, taskID, ActivityTaskCommentRemoved,
	)
	if err != nil {
		return false, fmt.Errorf("store: list comment removals: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		activity, err := scanActivity(rows)
		if err != nil {
			return false, fmt.Errorf("store: scan comment removal: %w", err)
		}
		if id, ok := activityCommentID(activity.Data); ok && id == commentID {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("store: list comment removals: %w", err)
	}
	return false, nil
}

func activityCommentID(data map[string]any) (int64, bool) {
	switch value := data["comment_id"].(type) {
	case float64:
		id := int64(value)
		if id <= 0 || float64(id) != value {
			return 0, false
		}
		return id, true
	case int64:
		if value <= 0 {
			return 0, false
		}
		return value, true
	default:
		return 0, false
	}
}

// projectTaskActivity hides comment mutation events and folds the latest edit
// for each comment into its original task.commented event. Removal hides the
// original comment. Raw transaction history remains available through
// ActivityAfter.
func projectTaskActivity(activities []Activity) []Activity {
	type commentState struct {
		edited   bool
		editedAt time.Time
		body     string
		removed  bool
	}
	states := map[int64]*commentState{}
	for _, activity := range activities {
		switch activity.Type {
		case ActivityTaskCommentEdited:
			id, ok := activityCommentID(activity.Data)
			if !ok {
				continue
			}
			body, ok := activity.Data["body"].(string)
			if !ok {
				continue
			}
			state := states[id]
			if state == nil {
				state = &commentState{}
				states[id] = state
			}
			state.edited = true
			state.editedAt = activity.Timestamp
			state.body = body
		case ActivityTaskCommentRemoved:
			id, ok := activityCommentID(activity.Data)
			if !ok {
				continue
			}
			state := states[id]
			if state == nil {
				state = &commentState{}
				states[id] = state
			}
			state.removed = true
		}
	}

	projected := make([]Activity, 0, len(activities))
	for _, activity := range activities {
		switch activity.Type {
		case ActivityTaskCommentEdited, ActivityTaskCommentRemoved:
			continue
		case ActivityTaskCommented:
			state := states[activity.ID]
			if state != nil && state.removed {
				continue
			}
			if state != nil && state.edited {
				data := make(map[string]any, len(activity.Data)+2)
				for key, value := range activity.Data {
					data[key] = value
				}
				data["body"] = state.body
				data["edited_at"] = formatTime(state.editedAt)
				activity.Data = data
			}
		}
		projected = append(projected, activity)
	}
	return projected
}

// Review records a review decision. Only tasks currently in review may be
// reviewed. Approval moves the task to done; requesting changes moves it to
// todo and applies the changes-requested marking.
func (s *Store) Review(ctx context.Context, actor string, taskID int64, decision ReviewDecision, detail string) (Task, error) {
	actor, err := normalizeActor(actor)
	if err != nil {
		return Task{}, err
	}
	if err := validateID("task", taskID); err != nil {
		return Task{}, err
	}
	if decision != ReviewApprove && decision != ReviewRequestChanges {
		return Task{}, fmt.Errorf("%w: unsupported review decision %q", ErrInvalid, decision)
	}

	var task Task
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		current, err := taskByID(ctx, tx, taskID)
		if err != nil {
			return err
		}
		if current.Status != StatusInReview {
			return fmt.Errorf("%w: task %d is not in review", ErrConflict, taskID)
		}

		now := nowUTC()
		next := StatusDone
		reason := "approved"
		if decision == ReviewRequestChanges {
			next = StatusTodo
			reason = "changes_requested"
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE tasks SET status = ?, updated_at = ? WHERE id = ?`,
			string(next), formatTime(now), taskID,
		); err != nil {
			return fmt.Errorf("store: apply review to task %d: %w", taskID, err)
		}
		data := map[string]any{
			"old":    string(current.Status),
			"new":    string(next),
			"reason": reason,
		}
		if detail != "" {
			data["detail"] = detail
		}
		if _, err := appendActivity(ctx, tx, activityInput{
			Type:      ActivityTaskStatusChanged,
			Actor:     actor,
			TaskID:    int64Pointer(taskID),
			ProjectID: int64Pointer(current.ProjectID),
			Timestamp: now,
			Data:      data,
		}); err != nil {
			return err
		}
		if decision == ReviewRequestChanges {
			if _, err := setMarkingTx(ctx, tx, actor, taskID, MarkingChangesRequested, true, now); err != nil {
				return err
			}
		}
		task, err = taskByID(ctx, tx, taskID)
		return err
	})
	if err != nil {
		return Task{}, err
	}
	return task, nil
}

// ListTasks returns tasks matching all nonzero filter fields, in creation
// order. Markings are loaded in the same transaction as the task rows.
func (s *Store) ListTasks(ctx context.Context, filter TaskFilter) ([]Task, error) {
	if filter.ProjectID < 0 || filter.RepoID < 0 {
		return nil, fmt.Errorf("%w: filters must use positive IDs", ErrInvalid)
	}
	if filter.Status != "" && !filter.Status.Valid() {
		return nil, fmt.Errorf("%w: unsupported task status %q", ErrInvalid, filter.Status)
	}
	if filter.Priority != "" && !filter.Priority.Valid() {
		return nil, fmt.Errorf("%w: unsupported task priority %q", ErrInvalid, filter.Priority)
	}
	marking := ""
	if filter.Marking != "" {
		var err error
		marking, err = normalizeMarking(filter.Marking)
		if err != nil {
			return nil, err
		}
	}

	var tasks []Task
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		query := `SELECT ` + taskColumns + ` FROM tasks`
		conditions := []string{}
		args := []any{}
		if filter.ProjectID > 0 {
			conditions = append(conditions, `project_id = ?`)
			args = append(args, filter.ProjectID)
		}
		if filter.RepoID > 0 {
			conditions = append(conditions, `repo_id = ?`)
			args = append(args, filter.RepoID)
		}
		if filter.Status != "" {
			conditions = append(conditions, `status = ?`)
			args = append(args, string(filter.Status))
		}
		if filter.Priority != "" {
			conditions = append(conditions, `priority = ?`)
			args = append(args, string(filter.Priority))
		}
		if marking != "" {
			conditions = append(conditions,
				`EXISTS (SELECT 1 FROM task_markings m WHERE m.task_id = tasks.id AND m.marking = ?)`)
			args = append(args, marking)
		}
		if len(conditions) > 0 {
			query += ` WHERE ` + strings.Join(conditions, ` AND `)
		}
		query += ` ORDER BY id`

		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("store: list tasks: %w", err)
		}
		defer rows.Close()

		tasks = []Task{}
		for rows.Next() {
			task, err := scanTask(rows)
			if err != nil {
				return fmt.Errorf("store: scan task: %w", err)
			}
			tasks = append(tasks, task)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("store: list tasks: %w", err)
		}
		rows.Close()
		for i := range tasks {
			tasks[i].Markings, err = loadMarkings(ctx, tx, tasks[i].ID)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return tasks, nil
}

// Show returns a task with its direct blocker IDs, linked sessions in link
// order, and task activity in audit order.
func (s *Store) Show(ctx context.Context, taskID int64) (TaskDetail, error) {
	if err := validateID("task", taskID); err != nil {
		return TaskDetail{}, err
	}

	var detail TaskDetail
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		task, err := taskByID(ctx, tx, taskID)
		if err != nil {
			return err
		}
		detail = TaskDetail{
			Task:     task,
			Blockers: []int64{},
			Sessions: []Session{},
			Activity: []Activity{},
		}

		blockerRows, err := tx.QueryContext(ctx,
			`SELECT depends_on_id FROM task_dependencies WHERE task_id = ? ORDER BY depends_on_id`, taskID,
		)
		if err != nil {
			return fmt.Errorf("store: list task blockers: %w", err)
		}
		for blockerRows.Next() {
			var blockerID int64
			if err := blockerRows.Scan(&blockerID); err != nil {
				blockerRows.Close()
				return fmt.Errorf("store: scan task blocker: %w", err)
			}
			detail.Blockers = append(detail.Blockers, blockerID)
		}
		if err := blockerRows.Err(); err != nil {
			blockerRows.Close()
			return fmt.Errorf("store: list task blockers: %w", err)
		}
		blockerRows.Close()

		sessionRows, err := tx.QueryContext(ctx, `
			SELECT s.id, s.url, s.state, s.attempt_id, s.created_at
			FROM sessions s
			JOIN dispatch_attempts a ON a.id = s.attempt_id
			WHERE a.task_id = ?
			ORDER BY s.seq`, taskID,
		)
		if err != nil {
			return fmt.Errorf("store: list task sessions: %w", err)
		}
		for sessionRows.Next() {
			var (
				session   Session
				createdAt string
			)
			if err := sessionRows.Scan(&session.ID, &session.URL, &session.State, &session.AttemptID, &createdAt); err != nil {
				sessionRows.Close()
				return fmt.Errorf("store: scan task session: %w", err)
			}
			session.CreatedAt, err = parseTime(createdAt)
			if err != nil {
				sessionRows.Close()
				return fmt.Errorf("store: task session %s: %w", session.ID, err)
			}
			detail.Sessions = append(detail.Sessions, session)
		}
		if err := sessionRows.Err(); err != nil {
			sessionRows.Close()
			return fmt.Errorf("store: list task sessions: %w", err)
		}
		sessionRows.Close()

		activityRows, err := tx.QueryContext(ctx, `
			SELECT id, type, actor, task_id, project_id, timestamp, data
			FROM activity
			WHERE task_id = ?
			ORDER BY id`, taskID,
		)
		if err != nil {
			return fmt.Errorf("store: list task activity: %w", err)
		}
		for activityRows.Next() {
			activity, err := scanActivity(activityRows)
			if err != nil {
				activityRows.Close()
				return fmt.Errorf("store: scan task activity: %w", err)
			}
			detail.Activity = append(detail.Activity, activity)
		}
		if err := activityRows.Err(); err != nil {
			activityRows.Close()
			return fmt.Errorf("store: list task activity: %w", err)
		}
		activityRows.Close()
		detail.Activity = projectTaskActivity(detail.Activity)
		return nil
	})
	if err != nil {
		return TaskDetail{}, err
	}
	return detail, nil
}
