package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"time"

	"github.com/armedi/gilir/internal/taskid"
)

const taskColumns = `id, project_id, repo_id, title, body, status, priority, worktree, created_at, updated_at`

func scanTask(sc scanner) (Task, error) {
	var (
		task      Task
		status    string
		priority  string
		worktree  sql.NullString
		createdAt string
		updatedAt string
	)
	if err := sc.Scan(
		&task.ID, &task.ProjectID, &task.RepoID, &task.Title, &task.Body,
		&status, &priority, &worktree, &createdAt, &updatedAt,
	); err != nil {
		return Task{}, err
	}
	task.Status = Status(status)
	if !task.Status.Valid() {
		return Task{}, fmt.Errorf("store: task %s has unsupported status %q", task.ID, status)
	}
	task.Priority = Priority(priority)
	if !task.Priority.Valid() {
		return Task{}, fmt.Errorf("store: task %s has unsupported priority %q", task.ID, priority)
	}
	task.Worktree = nullableText(worktree)
	var err error
	task.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return Task{}, fmt.Errorf("store: task %s created_at: %w", task.ID, err)
	}
	task.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return Task{}, fmt.Errorf("store: task %s updated_at: %w", task.ID, err)
	}
	task.Markings = []string{}
	return task, nil
}

// nullableText converts an optional text column to a pointer, treating an
// empty value as absent.
func nullableText(value sql.NullString) *string {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil
	}
	return stringPointer(value.String)
}

func loadMarkings(ctx context.Context, tx *sql.Tx, taskID string) ([]string, error) {
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

func taskByID(ctx context.Context, tx *sql.Tx, taskID string) (Task, error) {
	task, err := scanTask(tx.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id = ?`, taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, fmt.Errorf("%w: task %s", ErrNotFound, taskID)
	}
	if err != nil {
		return Task{}, fmt.Errorf("store: load task %s: %w", taskID, err)
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
func setMarkingTx(ctx context.Context, tx *sql.Tx, actor string, taskID string, marking string, present bool, at time.Time) (bool, error) {
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
		TaskID:    stringPointer(taskID),
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
			INSERT INTO tasks (project_id, repo_id, title, body, status, priority, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			projectID, in.RepoID, title, in.Body, string(status), string(priority),
			formatTime(now), formatTime(now),
		)
		if err != nil {
			return fmt.Errorf("store: create task: %w", mapConstraintError(err))
		}
		seq, err := result.LastInsertId()
		if err != nil {
			return fmt.Errorf("store: read task sequence: %w", err)
		}
		id := taskid.Format(seq)
		created = Task{
			ID:        id,
			ProjectID: projectID,
			RepoID:    in.RepoID,
			Title:     title,
			Body:      in.Body,
			Status:    status,
			Priority:  priority,
			CreatedAt: now,
			UpdatedAt: now,
			Markings:  []string{},
		}
		_, err = appendActivity(ctx, tx, activityInput{
			Type:      ActivityTaskCreated,
			Actor:     actor,
			TaskID:    stringPointer(id),
			ProjectID: int64Pointer(projectID),
			Timestamp: now,
			Data: map[string]any{
				"repo_id":  in.RepoID,
				"title":    title,
				"status":   string(status),
				"priority": string(priority),
			},
		})
		return err
	})
	if err != nil {
		return Task{}, err
	}
	return created, nil
}

// DeleteTask removes a task and its local dispatch history. A task with a
// reserved dispatch attempt or an incoming dependency cannot be deleted.
// Outgoing dependencies, terminal attempts and their sessions, and markings are
// removed in the same transaction that appends the task.deleted audit event;
// the append-only activity history is never discarded.
func (s *Store) DeleteTask(ctx context.Context, actor string, taskID string) error {
	actor, err := normalizeActor(actor)
	if err != nil {
		return err
	}
	if taskID, err = normalizeTaskID(taskID); err != nil {
		return err
	}

	return s.withTx(ctx, func(tx *sql.Tx) error {
		task, err := taskByID(ctx, tx, taskID)
		if err != nil {
			return err
		}

		var reservedAttempt string
		err = tx.QueryRowContext(ctx, `
			SELECT id
			FROM dispatch_attempts
			WHERE task_id = ? AND state = 'reserved'
			ORDER BY seq
			LIMIT 1`, taskID,
		).Scan(&reservedAttempt)
		if err == nil {
			return fmt.Errorf("%w: task %s has reserved dispatch attempt %s; wait for it to finish before deleting the task",
				ErrConflict, taskID, reservedAttempt)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("store: load reserved task attempt: %w", err)
		}

		rows, err := tx.QueryContext(ctx,
			`SELECT task_id FROM task_dependencies WHERE depends_on_id = ? ORDER BY length(task_id), task_id`, taskID,
		)
		if err != nil {
			return fmt.Errorf("store: list task dependents: %w", err)
		}
		dependentIDs := []string{}
		for rows.Next() {
			var dependentID string
			if err := rows.Scan(&dependentID); err != nil {
				rows.Close()
				return fmt.Errorf("store: scan task dependent: %w", err)
			}
			dependentIDs = append(dependentIDs, dependentID)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("store: list task dependents: %w", err)
		}
		rows.Close()
		if len(dependentIDs) > 0 {
			return fmt.Errorf("%w: task %s still has incoming dependencies from tasks %v; remove those dependencies before deleting the task",
				ErrConflict, taskID, dependentIDs)
		}

		if _, err := tx.ExecContext(ctx,
			`DELETE FROM task_dependencies WHERE task_id = ?`, taskID,
		); err != nil {
			return fmt.Errorf("store: delete task %s dependencies: %w", taskID, err)
		}
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM sessions
			WHERE attempt_id IN (
				SELECT id FROM dispatch_attempts
				WHERE task_id = ? AND state <> 'reserved'
			)`, taskID,
		); err != nil {
			return fmt.Errorf("store: delete task %s sessions: %w", taskID, err)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM dispatch_attempts WHERE task_id = ? AND state <> 'reserved'`, taskID,
		); err != nil {
			return fmt.Errorf("store: delete task %s dispatch attempts: %w", taskID, err)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM tasks WHERE id = ?`, taskID,
		); err != nil {
			return fmt.Errorf("store: delete task %s: %w", taskID, err)
		}
		if err := clearWorktreeName(ctx, tx, taskID); err != nil {
			return err
		}

		_, err = appendActivity(ctx, tx, activityInput{
			Type:      ActivityTaskDeleted,
			Actor:     actor,
			TaskID:    stringPointer(taskID),
			ProjectID: int64Pointer(task.ProjectID),
		})
		return err
	})
}

// UpdateTask applies a partial edit. Nil patch fields are unchanged. A no-op
// patch writes no audit event. Moving a task from in-review back to todo adds
// the changes-requested marking in the same transaction.
func (s *Store) UpdateTask(ctx context.Context, actor string, taskID string, patch TaskPatch) (Task, error) {
	actor, err := normalizeActor(actor)
	if err != nil {
		return Task{}, err
	}
	if taskID, err = normalizeTaskID(taskID); err != nil {
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
		newBody := old.Body
		newPriority := old.Priority
		newRepoID := old.RepoID
		newStatus := old.Status
		if patch.Title != nil {
			newTitle = *patch.Title
		}
		if patch.Body != nil {
			newBody = *patch.Body
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
			if old.Worktree != nil {
				return fmt.Errorf("%w: task %s already has worktree %s; its repository cannot change",
					ErrConflict, taskID, *old.Worktree)
			}
			var openAttempts int64
			if err := tx.QueryRowContext(ctx,
				`SELECT count(*) FROM dispatch_attempts WHERE task_id = ? AND state = 'reserved'`,
				taskID,
			).Scan(&openAttempts); err != nil {
				return fmt.Errorf("store: count open task attempts: %w", err)
			}
			if openAttempts > 0 {
				return fmt.Errorf("%w: task %s has an outstanding dispatch attempt", ErrConflict, taskID)
			}
			linked, err := projectRepoLinked(ctx, tx, old.ProjectID, newRepoID)
			if err != nil {
				return err
			}
			if !linked {
				return fmt.Errorf("%w: repository %d does not belong to project %d", ErrInvalid, newRepoID, old.ProjectID)
			}
			if err := clearWorktreeName(ctx, tx, taskID); err != nil {
				return err
			}
		}

		statusChanged := newStatus != old.Status
		titleChanged := newTitle != old.Title
		bodyChanged := newBody != old.Body
		priorityChanged := newPriority != old.Priority

		// A blocked task waits for a human decision, so it must not be moved to
		// a state that would let it leave the queue or be reviewed.
		if statusChanged && hasMarking(old.Markings, MarkingBlocked) && !blockable(newStatus) {
			return fmt.Errorf("%w: task %s is blocked; unblock it before moving it to %s",
				ErrConflict, taskID, newStatus)
		}

		requestingChanges := old.Status == StatusInReview && newStatus == StatusTodo
		message := ""
		if requestingChanges {
			if patch.Message != nil {
				message = strings.TrimSpace(*patch.Message)
			}
			if message == "" {
				return fmt.Errorf("%w: a message is required when requesting changes", ErrInvalid)
			}
		} else if patch.Message != nil && strings.TrimSpace(*patch.Message) != "" {
			return fmt.Errorf("%w: a message is only valid when requesting changes", ErrInvalid)
		}

		if !statusChanged && !titleChanged && !bodyChanged && !priorityChanged && !repoChanged {
			updated = old
			return nil
		}

		now := nowUTC()
		if _, err := tx.ExecContext(ctx, `
			UPDATE tasks
			SET title = ?, body = ?, priority = ?, repo_id = ?, status = ?, updated_at = ?
			WHERE id = ?`,
			newTitle, newBody, string(newPriority), newRepoID, string(newStatus), formatTime(now), taskID,
		); err != nil {
			return fmt.Errorf("store: update task %s: %w", taskID, err)
		}

		if titleChanged || bodyChanged || priorityChanged || repoChanged {
			oldFields := map[string]any{}
			newFields := map[string]any{}
			if titleChanged {
				oldFields["title"] = old.Title
				newFields["title"] = newTitle
			}
			if bodyChanged {
				oldFields["body"] = old.Body
				newFields["body"] = newBody
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
				TaskID:    stringPointer(taskID),
				ProjectID: int64Pointer(old.ProjectID),
				Timestamp: now,
				Data:      map[string]any{"old": oldFields, "new": newFields},
			}); err != nil {
				return err
			}
		}

		if statusChanged {
			if requestingChanges {
				if _, err := appendActivity(ctx, tx, activityInput{
					Type:      ActivityTaskCommented,
					Actor:     actor,
					TaskID:    stringPointer(taskID),
					ProjectID: int64Pointer(old.ProjectID),
					Timestamp: now,
					Data:      map[string]any{"message": message},
				}); err != nil {
					return err
				}
			}
			if _, err := appendActivity(ctx, tx, activityInput{
				Type:      ActivityTaskStatusChanged,
				Actor:     actor,
				TaskID:    stringPointer(taskID),
				ProjectID: int64Pointer(old.ProjectID),
				Timestamp: now,
				Data: map[string]any{
					"old": string(old.Status),
					"new": string(newStatus),
				},
			}); err != nil {
				return err
			}
			if requestingChanges {
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

// hasMarking reports whether the loaded task carries marking.
func hasMarking(markings []string, marking string) bool {
	for _, candidate := range markings {
		if candidate == marking {
			return true
		}
	}
	return false
}

// Block appends a required Markdown message as a comment and marks a task
// blocked in one transaction. Blocked tasks are never reserved, and a
// successful run leaves them in progress so dependents stay held until Unblock.
// Only backlog, todo and in-progress tasks can be blocked; a task already
// blocked is a conflict because the message would be an ordinary comment.
func (s *Store) Block(ctx context.Context, actor string, taskID, message string) (Task, error) {
	actor, err := normalizeActor(actor)
	if err != nil {
		return Task{}, err
	}
	if taskID, err = normalizeTaskID(taskID); err != nil {
		return Task{}, err
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return Task{}, fmt.Errorf("%w: a message is required when blocking a task", ErrInvalid)
	}

	var task Task
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		current, err := taskByID(ctx, tx, taskID)
		if err != nil {
			return err
		}
		if !blockable(current.Status) {
			return fmt.Errorf("%w: task %s is %s; only backlog, todo and in-progress tasks can be blocked",
				ErrConflict, taskID, current.Status)
		}
		if hasMarking(current.Markings, MarkingBlocked) {
			return fmt.Errorf("%w: task %s is already blocked; add an ordinary comment instead", ErrConflict, taskID)
		}

		now := nowUTC()
		if _, err := appendActivity(ctx, tx, activityInput{
			Type:      ActivityTaskCommented,
			Actor:     actor,
			TaskID:    stringPointer(taskID),
			ProjectID: int64Pointer(current.ProjectID),
			Timestamp: now,
			Data:      map[string]any{"message": message},
		}); err != nil {
			return err
		}
		if _, err := setMarkingTx(ctx, tx, actor, taskID, MarkingBlocked, true, now); err != nil {
			return err
		}
		// A block supersedes a pending dispatch boost: the task is not queued
		// until it is unblocked again.
		if _, err := setMarkingTx(ctx, tx, actor, taskID, MarkingUnblocked, false, now); err != nil {
			return err
		}
		task, err = taskByID(ctx, tx, taskID)
		return err
	})
	if err != nil {
		return Task{}, err
	}
	return task, nil
}

// Unblock removes the blocked marking in one transaction, optionally recording
// a Markdown message as a comment. An in-progress task with no open dispatch
// attempt is moved back to todo and gets the transient dispatch boost so it
// re-queues immediately. todo, backlog and actively running in-progress tasks
// keep their status.
func (s *Store) Unblock(ctx context.Context, actor string, taskID, message string) (Task, error) {
	actor, err := normalizeActor(actor)
	if err != nil {
		return Task{}, err
	}
	if taskID, err = normalizeTaskID(taskID); err != nil {
		return Task{}, err
	}
	message = strings.TrimSpace(message)

	var task Task
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		current, err := taskByID(ctx, tx, taskID)
		if err != nil {
			return err
		}
		if !hasMarking(current.Markings, MarkingBlocked) {
			return fmt.Errorf("%w: task %s is not blocked", ErrConflict, taskID)
		}

		now := nowUTC()
		if message != "" {
			if _, err := appendActivity(ctx, tx, activityInput{
				Type:      ActivityTaskCommented,
				Actor:     actor,
				TaskID:    stringPointer(taskID),
				ProjectID: int64Pointer(current.ProjectID),
				Timestamp: now,
				Data:      map[string]any{"message": message, "purpose": CommentPurposeUnblock},
			}); err != nil {
				return err
			}
		}
		if _, err := setMarkingTx(ctx, tx, actor, taskID, MarkingBlocked, false, now); err != nil {
			return err
		}

		next := current.Status
		if next == StatusInProgress {
			open, err := hasOpenAttempt(ctx, tx, taskID)
			if err != nil {
				return err
			}
			if !open {
				next = StatusTodo
			}
		}
		if next != current.Status {
			if _, err := tx.ExecContext(ctx,
				`UPDATE tasks SET status = ?, updated_at = ? WHERE id = ?`,
				string(next), formatTime(now), taskID,
			); err != nil {
				return fmt.Errorf("store: requeue unblocked task %s: %w", taskID, err)
			}
			if _, err := appendActivity(ctx, tx, activityInput{
				Type:      ActivityTaskStatusChanged,
				Actor:     actor,
				TaskID:    stringPointer(taskID),
				ProjectID: int64Pointer(current.ProjectID),
				Timestamp: now,
				Data: map[string]any{
					"old":    string(current.Status),
					"new":    string(next),
					"reason": "unblocked",
				},
			}); err != nil {
				return err
			}
		}
		if next == StatusTodo {
			if _, err := setMarkingTx(ctx, tx, actor, taskID, MarkingUnblocked, true, now); err != nil {
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

// blockable reports whether a status may carry the blocked marking.
func blockable(status Status) bool {
	switch status {
	case StatusBacklog, StatusTodo, StatusInProgress:
		return true
	default:
		return false
	}
}

// hasOpenAttempt reports whether the task has a reserved dispatch attempt.
func hasOpenAttempt(ctx context.Context, tx *sql.Tx, taskID string) (bool, error) {
	var open bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM dispatch_attempts WHERE task_id = ? AND state = 'reserved'
		)`, taskID,
	).Scan(&open); err != nil {
		return false, fmt.Errorf("store: check open task attempt: %w", err)
	}
	return open, nil
}

// AbandonAttemptBeforePrompt terminalizes a reserved attempt as abandoned when
// its task is no longer in progress or is blocked, in one transaction with the
// check. It reports whether the attempt was abandoned, so a watcher racing an
// Unblock cannot abandon a task that is about to re-queue: an unblock leaves
// the attempt open only while the task is still running, and this method reads
// the same state it acts on.
func (s *Store) AbandonAttemptBeforePrompt(ctx context.Context, attemptID string) (bool, error) {
	attemptID = strings.TrimSpace(attemptID)
	if attemptID == "" {
		return false, fmt.Errorf("%w: attempt ID is required", ErrInvalid)
	}

	var abandoned bool
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		attempt, _, err := s.attemptByID(ctx, tx, attemptID)
		if err != nil {
			return err
		}
		if attempt.State != AttemptStateReserved {
			return nil
		}
		task, err := taskByID(ctx, tx, attempt.TaskID)
		if err != nil {
			return err
		}
		reason := ""
		switch {
		case task.Status != StatusInProgress:
			reason = fmt.Sprintf("task moved to %s before the prompt was sent", task.Status)
		case hasMarking(task.Markings, MarkingBlocked):
			reason = "task was blocked before the prompt was sent"
		default:
			return nil
		}

		// An abandoned outcome never changes task state or markings; only the
		// attempt and its sessions settle.
		now := nowUTC()
		result, err := tx.ExecContext(ctx, `
			UPDATE dispatch_attempts
			SET state = 'abandoned', outcome = 'abandoned', detail = ?, finished_at = ?
			WHERE id = ? AND state = 'reserved'`,
			reason, formatTime(now), attemptID,
		)
		if err != nil {
			return fmt.Errorf("store: abandon dispatch attempt %s: %w", attemptID, err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("store: abandon dispatch attempt %s: %w", attemptID, err)
		}
		if affected != 1 {
			return nil
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE sessions SET state = 'abandoned' WHERE attempt_id = ?`, attemptID,
		); err != nil {
			return fmt.Errorf("store: update session state: %w", err)
		}
		_, err = appendActivity(ctx, tx, activityInput{
			Type:      ActivitySessionOutcome,
			Actor:     ActorSystem,
			TaskID:    stringPointer(task.ID),
			ProjectID: int64Pointer(task.ProjectID),
			Timestamp: now,
			Data: map[string]any{
				"attempt_id":  attemptID,
				"outcome":     string(OutcomeAbandoned),
				"detail":      reason,
				"session_ids": []string{},
			},
		})
		if err != nil {
			return err
		}
		abandoned = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return abandoned, nil
}

// SetMarking adds or removes a task marking. A duplicate add or remove is a
// no-op without an audit event. The blocked marking is managed by Block and
// Unblock, which also record the required message and re-queue the task, so it
// cannot be toggled directly.
func (s *Store) SetMarking(ctx context.Context, actor string, taskID string, marking string, present bool) (Task, error) {
	actor, err := normalizeActor(actor)
	if err != nil {
		return Task{}, err
	}
	if taskID, err = normalizeTaskID(taskID); err != nil {
		return Task{}, err
	}
	marking, err = normalizeMarking(marking)
	if err != nil {
		return Task{}, err
	}
	if marking == MarkingBlocked {
		return Task{}, fmt.Errorf("%w: use `gilir block` and `gilir unblock` to change the blocked marking", ErrInvalid)
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
func (s *Store) SetDependency(ctx context.Context, actor string, taskID, dependsOnTaskID string, present bool) error {
	actor, err := normalizeActor(actor)
	if err != nil {
		return err
	}
	if taskID, err = normalizeTaskID(taskID); err != nil {
		return err
	}
	if dependsOnTaskID, err = normalizeTaskID(dependsOnTaskID); err != nil {
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
				TaskID:    stringPointer(taskID),
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
			TaskID:    stringPointer(taskID),
			ProjectID: int64Pointer(projectID),
			Timestamp: now,
			Data:      map[string]any{"depends_on_id": dependsOnTaskID},
		})
		return err
	})
}

func dependencyCreatesCycle(ctx context.Context, tx *sql.Tx, taskID, dependsOnTaskID string) (bool, error) {
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
func (s *Store) Comment(ctx context.Context, actor string, taskID string, message string) (Activity, error) {
	actor, err := normalizeActor(actor)
	if err != nil {
		return Activity{}, err
	}
	if taskID, err = normalizeTaskID(taskID); err != nil {
		return Activity{}, err
	}
	if strings.TrimSpace(message) == "" {
		return Activity{}, fmt.Errorf("%w: comment message is required", ErrInvalid)
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
			TaskID:    stringPointer(taskID),
			ProjectID: int64Pointer(projectID),
			Data:      map[string]any{"message": message},
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
func (s *Store) EditComment(ctx context.Context, actor string, taskID string, commentID int64, message string) (Activity, error) {
	if strings.TrimSpace(message) == "" {
		return Activity{}, fmt.Errorf("%w: comment message is required", ErrInvalid)
	}
	return s.mutateComment(ctx, actor, taskID, commentID, ActivityTaskCommentEdited, map[string]any{"message": message})
}

// RemoveComment appends a removal event for an existing comment. The comment
// must be a task.commented event on taskID owned by actor. Removing an already
// removed comment is a conflict.
func (s *Store) RemoveComment(ctx context.Context, actor string, taskID string, commentID int64) (Activity, error) {
	return s.mutateComment(ctx, actor, taskID, commentID, ActivityTaskCommentRemoved, nil)
}

// mutateComment validates a comment mutation and appends its audit event in
// one transaction. Removal is terminal, so a removed comment cannot be mutated
// again.
func (s *Store) mutateComment(ctx context.Context, actor string, taskID string, commentID int64, activityType string, data map[string]any) (Activity, error) {
	actor, err := normalizeActor(actor)
	if err != nil {
		return Activity{}, err
	}
	if taskID, err = normalizeTaskID(taskID); err != nil {
		return Activity{}, err
	}
	if err := validateID("comment", commentID); err != nil {
		return Activity{}, err
	}
	commentKey := strconv.FormatInt(commentID, 10)
	if data == nil {
		data = map[string]any{}
	}

	var mutation Activity
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		projectID, err := commentProjectID(ctx, tx, actor, taskID, commentID)
		if err != nil {
			return err
		}
		removed, err := commentRemoved(ctx, tx, taskID, commentKey)
		if err != nil {
			return err
		}
		if removed {
			return fmt.Errorf("%w: comment %d has been removed", ErrConflict, commentID)
		}
		data["comment_id"] = commentKey
		mutation, err = appendActivity(ctx, tx, activityInput{
			Type:      activityType,
			Actor:     actor,
			TaskID:    stringPointer(taskID),
			ProjectID: int64Pointer(projectID),
			Data:      data,
		})
		return err
	})
	if err != nil {
		return Activity{}, err
	}
	return mutation, nil
}

// commentProjectID verifies that commentID is a task.commented event on taskID
// owned by actor and returns the project to record on the mutation.
func commentProjectID(ctx context.Context, tx *sql.Tx, actor string, taskID string, commentID int64) (int64, error) {
	var projectID sql.NullInt64
	err := tx.QueryRowContext(ctx, `
		SELECT project_id
		FROM activity
		WHERE id = ? AND task_id = ? AND actor = ? AND type = ?`,
		commentID, taskID, actor, ActivityTaskCommented,
	).Scan(&projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("%w: comment %d", ErrNotFound, commentID)
	}
	if err != nil {
		return 0, fmt.Errorf("store: load comment %d: %w", commentID, err)
	}
	if !projectID.Valid {
		return requireTask(ctx, tx, taskID)
	}
	return projectID.Int64, nil
}

func commentRemoved(ctx context.Context, tx *sql.Tx, taskID string, commentKey string) (bool, error) {
	var removed bool
	err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM activity
			WHERE task_id = ? AND type = ? AND json_extract(data, '$.comment_id') = ?
		)`,
		taskID, ActivityTaskCommentRemoved, commentKey,
	).Scan(&removed)
	if err != nil {
		return false, fmt.Errorf("store: check comment removal: %w", err)
	}
	return removed, nil
}

func commentEventID(data map[string]any) (int64, bool) {
	value, ok := data["comment_id"].(string)
	if !ok {
		return 0, false
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// projectTaskActivity hides comment mutation events and folds the latest
// mutation for each comment into its original task.commented event. An edit
// replaces the message and adds edited_at; removal hides the original comment. Raw
// transaction history remains available through ActivityAfter.
func projectTaskActivity(activities []Activity) []Activity {
	latest := map[int64]Activity{}
	for _, activity := range activities {
		if activity.Type != ActivityTaskCommentEdited && activity.Type != ActivityTaskCommentRemoved {
			continue
		}
		if id, ok := commentEventID(activity.Data); ok {
			latest[id] = activity
		}
	}

	projected := make([]Activity, 0, len(activities))
	for _, activity := range activities {
		switch activity.Type {
		case ActivityTaskCommentEdited, ActivityTaskCommentRemoved:
			continue
		case ActivityTaskCommented:
			mutation, ok := latest[activity.ID]
			if !ok {
				break
			}
			if mutation.Type == ActivityTaskCommentRemoved {
				continue
			}
			data := maps.Clone(activity.Data)
			if data == nil {
				data = map[string]any{}
			}
			data["message"] = mutation.Data["message"]
			data["edited_at"] = formatTime(mutation.Timestamp)
			activity.Data = data
		}
		projected = append(projected, activity)
	}
	return projected
}

// Review records a review decision. Only tasks currently in review may be
// reviewed. Approval moves the task to done. Requesting changes requires a
// non-blank message, moves the task to todo, records the message as a comment,
// and applies the changes-requested marking. A non-blank approval message is
// recorded as a comment too.
func (s *Store) Review(ctx context.Context, actor string, taskID string, decision ReviewDecision, message string) (Task, error) {
	actor, err := normalizeActor(actor)
	if err != nil {
		return Task{}, err
	}
	if taskID, err = normalizeTaskID(taskID); err != nil {
		return Task{}, err
	}
	if decision != ReviewApprove && decision != ReviewRequestChanges {
		return Task{}, fmt.Errorf("%w: unsupported review decision %q", ErrInvalid, decision)
	}
	message = strings.TrimSpace(message)
	if decision == ReviewRequestChanges && message == "" {
		return Task{}, fmt.Errorf("%w: a message is required when requesting changes", ErrInvalid)
	}

	var task Task
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		current, err := taskByID(ctx, tx, taskID)
		if err != nil {
			return err
		}
		if current.Status != StatusInReview {
			return fmt.Errorf("%w: task %s is not in review", ErrConflict, taskID)
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
			return fmt.Errorf("store: apply review to task %s: %w", taskID, err)
		}
		if message != "" {
			if _, err := appendActivity(ctx, tx, activityInput{
				Type:      ActivityTaskCommented,
				Actor:     actor,
				TaskID:    stringPointer(taskID),
				ProjectID: int64Pointer(current.ProjectID),
				Timestamp: now,
				Data:      map[string]any{"message": message},
			}); err != nil {
				return err
			}
		}
		if _, err := appendActivity(ctx, tx, activityInput{
			Type:      ActivityTaskStatusChanged,
			Actor:     actor,
			TaskID:    stringPointer(taskID),
			ProjectID: int64Pointer(current.ProjectID),
			Timestamp: now,
			Data: map[string]any{
				"old":    string(current.Status),
				"new":    string(next),
				"reason": reason,
			},
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
		query += ` ORDER BY seq`

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
// order, and projected task activity in audit order.
func (s *Store) Show(ctx context.Context, taskID string) (TaskDetail, error) {
	normalized, err := normalizeTaskID(taskID)
	if err != nil {
		return TaskDetail{}, err
	}
	taskID = normalized

	var detail TaskDetail
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		task, err := taskByID(ctx, tx, taskID)
		if err != nil {
			return err
		}
		detail = TaskDetail{
			Task:     task,
			Blockers: []string{},
			Sessions: []Session{},
			Activity: []Activity{},
		}

		var openAttemptID string
		err = tx.QueryRowContext(ctx,
			`SELECT id FROM dispatch_attempts WHERE task_id = ? AND state = 'reserved' ORDER BY seq LIMIT 1`,
			taskID,
		).Scan(&openAttemptID)
		switch {
		case err == nil:
			attempt, _, err := s.attemptByID(ctx, tx, openAttemptID)
			if err != nil {
				return err
			}
			detail.Attempt = &attempt
		case errors.Is(err, sql.ErrNoRows):
			// No running attempt.
		default:
			return fmt.Errorf("store: load open task attempt: %w", err)
		}

		blockerRows, err := tx.QueryContext(ctx,
			`SELECT depends_on_id FROM task_dependencies WHERE task_id = ? ORDER BY length(depends_on_id), depends_on_id`, taskID,
		)
		if err != nil {
			return fmt.Errorf("store: list task blockers: %w", err)
		}
		for blockerRows.Next() {
			var blockerID string
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
			SELECT s.id, s.url, s.state, s.attempt_id, s.worktree, s.created_at
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
				worktree  sql.NullString
				createdAt string
			)
			if err := sessionRows.Scan(&session.ID, &session.URL, &session.State, &session.AttemptID, &worktree, &createdAt); err != nil {
				sessionRows.Close()
				return fmt.Errorf("store: scan task session: %w", err)
			}
			session.Worktree = nullableText(worktree)
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
