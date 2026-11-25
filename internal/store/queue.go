package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// QueueSettings returns the persisted dispatch queue settings.
func (s *Store) QueueSettings(ctx context.Context) (QueueSettings, error) {
	return readQueueSettings(ctx, s.db.QueryRowContext(ctx,
		`SELECT paused, max_concurrency, updated_at FROM queue_settings WHERE id = 1`,
	))
}

// UpdateQueueSettings applies a partial queue-settings mutation. A
// nonpositive max_concurrency is rejected. A no-op patch writes no event.
func (s *Store) UpdateQueueSettings(ctx context.Context, actor string, patch QueueSettingsPatch) (QueueSettings, error) {
	actor, err := normalizeActor(actor)
	if err != nil {
		return QueueSettings{}, err
	}
	if patch.MaxConcurrency != nil && *patch.MaxConcurrency <= 0 {
		return QueueSettings{}, fmt.Errorf("%w: max_concurrency must be positive", ErrInvalid)
	}

	var result QueueSettings
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		current, err := readQueueSettings(ctx, tx.QueryRowContext(ctx,
			`SELECT paused, max_concurrency, updated_at FROM queue_settings WHERE id = 1`,
		))
		if err != nil {
			return err
		}
		next := current
		if patch.Paused != nil {
			next.Paused = *patch.Paused
		}
		if patch.MaxConcurrency != nil {
			next.MaxConcurrency = *patch.MaxConcurrency
		}
		if next.Paused == current.Paused && next.MaxConcurrency == current.MaxConcurrency {
			result = current
			return nil
		}
		now := nowUTC()
		next.UpdatedAt = now
		if _, err := tx.ExecContext(ctx, `
			UPDATE queue_settings
			SET paused = ?, max_concurrency = ?, updated_at = ?
			WHERE id = 1`,
			boolInt(next.Paused), next.MaxConcurrency, formatTime(now),
		); err != nil {
			return fmt.Errorf("store: update queue settings: %w", err)
		}
		_, err = appendActivity(ctx, tx, activityInput{
			Type:      ActivityQueueUpdated,
			Actor:     actor,
			Timestamp: now,
			Data: map[string]any{
				"old": map[string]any{
					"paused":          current.Paused,
					"max_concurrency": current.MaxConcurrency,
				},
				"new": map[string]any{
					"paused":          next.Paused,
					"max_concurrency": next.MaxConcurrency,
				},
			},
		})
		if err != nil {
			return err
		}
		result = next
		return nil
	})
	if err != nil {
		return QueueSettings{}, err
	}
	return result, nil
}

func readQueueSettings(ctx context.Context, sc scanner) (QueueSettings, error) {
	var (
		settings  QueueSettings
		paused    int
		updatedAt string
	)
	if err := sc.Scan(&paused, &settings.MaxConcurrency, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return QueueSettings{}, fmt.Errorf("%w: queue settings", ErrNotFound)
		}
		return QueueSettings{}, fmt.Errorf("store: load queue settings: %w", err)
	}
	settings.Paused = paused != 0
	parsed, err := parseTime(updatedAt)
	if err != nil {
		return QueueSettings{}, fmt.Errorf("store: queue settings updated_at: %w", err)
	}
	settings.UpdatedAt = parsed
	return settings, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// Reserve atomically selects and claims one eligible todo task. Eligibility
// requires the queue to be unpaused, the task to have no unresolved attempt,
// and every direct blocker to be in-review or done. Candidates are ordered by
// strict priority (urgent, high, medium, low), changes-requested first, fewer
// blockers currently in review, then task ID. Capacity and external service
// health are scheduler concerns; this method only persists the reservation.
//
// serverURL and serverPID are stored on the attempt so a restarted scheduler
// can fail closed against a different OpenCode process. A missing candidate or
// a paused queue returns (nil, nil).
func (s *Store) Reserve(ctx context.Context, serverURL string, serverPID int64) (*DispatchAttempt, error) {
	serverURL = strings.TrimSpace(serverURL)
	if serverURL == "" {
		return nil, fmt.Errorf("%w: OpenCode server URL is required", ErrInvalid)
	}
	if serverPID <= 0 {
		return nil, fmt.Errorf("%w: OpenCode server PID must be positive", ErrInvalid)
	}
	actor := ActorSystem

	var reserved *DispatchAttempt
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		settings, err := readQueueSettings(ctx, tx.QueryRowContext(ctx,
			`SELECT paused, max_concurrency, updated_at FROM queue_settings WHERE id = 1`,
		))
		if err != nil {
			return err
		}
		if settings.Paused {
			return nil
		}

		var (
			taskID   int64
			repoPath string
		)
		err = tx.QueryRowContext(ctx, `
			SELECT t.id, r.path
			FROM tasks t
			JOIN repos r ON r.id = t.repo_id
			WHERE t.status = 'todo'
				AND NOT EXISTS (
					SELECT 1 FROM dispatch_attempts a
					WHERE a.task_id = t.id AND a.state = 'reserved'
				)
				AND NOT EXISTS (
					SELECT 1
					FROM task_dependencies d
					JOIN tasks b ON b.id = d.depends_on_id
					WHERE d.task_id = t.id
						AND b.status NOT IN ('in-review', 'done')
				)
			ORDER BY
				CASE t.priority
					WHEN 'urgent' THEN 0
					WHEN 'high' THEN 1
					WHEN 'medium' THEN 2
					WHEN 'low' THEN 3
					ELSE 4
				END,
				CASE WHEN EXISTS (
					SELECT 1 FROM task_markings m
					WHERE m.task_id = t.id AND m.marking = 'changes-requested'
				) THEN 0 ELSE 1 END,
				(
					SELECT count(*)
					FROM task_dependencies d
					JOIN tasks b ON b.id = d.depends_on_id
					WHERE d.task_id = t.id AND b.status = 'in-review'
				),
				t.id
			LIMIT 1`,
		).Scan(&taskID, &repoPath)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("store: select dispatch candidate: %w", err)
		}

		now := nowUTC()
		var attemptID string
		for attempt := 0; attempt < 3; attempt++ {
			candidate, err := randomHex(16)
			if err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `
				INSERT INTO dispatch_attempts
					(id, task_id, repo_path, server_url, server_pid, state, detail, created_at)
				VALUES (?, ?, ?, ?, ?, 'reserved', '', ?)`,
				candidate, taskID, repoPath, serverURL, serverPID, formatTime(now),
			)
			if err == nil {
				attemptID = candidate
				break
			}
			if !isConstraintError(err) {
				return fmt.Errorf("store: create dispatch attempt: %w", err)
			}
		}
		if attemptID == "" {
			return errors.New("store: could not allocate a unique dispatch attempt ID")
		}

		if _, err := tx.ExecContext(ctx,
			`UPDATE tasks SET status = 'in-progress', updated_at = ? WHERE id = ?`,
			formatTime(now), taskID,
		); err != nil {
			return fmt.Errorf("store: mark task %d in progress: %w", taskID, err)
		}

		projectID, err := requireTask(ctx, tx, taskID)
		if err != nil {
			return err
		}
		if _, err := appendActivity(ctx, tx, activityInput{
			Type:      ActivityTaskClaimed,
			Actor:     actor,
			TaskID:    int64Pointer(taskID),
			ProjectID: int64Pointer(projectID),
			Timestamp: now,
			Data: map[string]any{
				"attempt_id": attemptID,
				"repo_path":  repoPath,
			},
		}); err != nil {
			return err
		}
		if _, err := appendActivity(ctx, tx, activityInput{
			Type:      ActivityTaskStatusChanged,
			Actor:     actor,
			TaskID:    int64Pointer(taskID),
			ProjectID: int64Pointer(projectID),
			Timestamp: now,
			Data: map[string]any{
				"old": string(StatusTodo),
				"new": string(StatusInProgress),
			},
		}); err != nil {
			return err
		}
		for _, marking := range []string{MarkingChangesRequested, MarkingError} {
			if _, err := setMarkingTx(ctx, tx, actor, taskID, marking, false, now); err != nil {
				return err
			}
		}

		reserved = &DispatchAttempt{
			ID:        attemptID,
			TaskID:    taskID,
			RepoPath:  repoPath,
			ServerURL: serverURL,
			ServerPID: serverPID,
			State:     AttemptStateReserved,
			CreatedAt: now,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return reserved, nil
}

// ListOpenAttempts returns unresolved attempts in reservation order.
func (s *Store) ListOpenAttempts(ctx context.Context) ([]DispatchAttempt, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT a.id, a.task_id, a.repo_path, a.server_url, a.server_pid, a.state, a.outcome,
			a.detail, a.created_at, a.finished_at,
			(SELECT s.id FROM sessions s WHERE s.attempt_id = a.id ORDER BY s.seq LIMIT 1)
		FROM dispatch_attempts a
		WHERE a.state = 'reserved'
		ORDER BY a.seq`)
	if err != nil {
		return nil, fmt.Errorf("store: list open attempts: %w", err)
	}
	defer rows.Close()

	attempts := []DispatchAttempt{}
	for rows.Next() {
		attempt, err := scanAttempt(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan open attempt: %w", err)
		}
		attempts = append(attempts, attempt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list open attempts: %w", err)
	}
	return attempts, nil
}

func (s *Store) attemptByID(ctx context.Context, tx *sql.Tx, attemptID string) (DispatchAttempt, int64, error) {
	var (
		attempt   DispatchAttempt
		seq       int64
		state     string
		outcome   sql.NullString
		detail    sql.NullString
		created   string
		finished  sql.NullString
		sessionID sql.NullString
	)
	err := tx.QueryRowContext(ctx, `
		SELECT a.seq, a.id, a.task_id, a.repo_path, a.server_url, a.server_pid, a.state,
			a.outcome, a.detail, a.created_at, a.finished_at,
			(SELECT s.id FROM sessions s WHERE s.attempt_id = a.id ORDER BY s.seq LIMIT 1)
		FROM dispatch_attempts a
		WHERE a.id = ?`, attemptID,
	).Scan(
		&seq, &attempt.ID, &attempt.TaskID, &attempt.RepoPath, &attempt.ServerURL,
		&attempt.ServerPID, &state, &outcome, &detail, &created, &finished, &sessionID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return DispatchAttempt{}, 0, fmt.Errorf("%w: dispatch attempt %s", ErrNotFound, attemptID)
	}
	if err != nil {
		return DispatchAttempt{}, 0, fmt.Errorf("store: load dispatch attempt %s: %w", attemptID, err)
	}
	attempt.State = AttemptState(state)
	if outcome.Valid {
		attempt.Outcome = AttemptOutcome(outcome.String)
	}
	attempt.Detail = detail.String
	if sessionID.Valid {
		id := sessionID.String
		attempt.SessionID = &id
	}
	attempt.CreatedAt, err = parseTime(created)
	if err != nil {
		return DispatchAttempt{}, 0, fmt.Errorf("store: dispatch attempt created_at: %w", err)
	}
	if finished.Valid {
		finishedAt, err := parseTime(finished.String)
		if err != nil {
			return DispatchAttempt{}, 0, fmt.Errorf("store: dispatch attempt finished_at: %w", err)
		}
		attempt.FinishedAt = &finishedAt
	}
	return attempt, seq, nil
}

func scanAttempt(sc scanner) (DispatchAttempt, error) {
	var (
		attempt   DispatchAttempt
		state     string
		outcome   sql.NullString
		detail    sql.NullString
		created   string
		finished  sql.NullString
		sessionID sql.NullString
	)
	if err := sc.Scan(
		&attempt.ID, &attempt.TaskID, &attempt.RepoPath, &attempt.ServerURL,
		&attempt.ServerPID, &state, &outcome, &detail, &created, &finished, &sessionID,
	); err != nil {
		return DispatchAttempt{}, err
	}
	attempt.State = AttemptState(state)
	if outcome.Valid {
		attempt.Outcome = AttemptOutcome(outcome.String)
	}
	attempt.Detail = detail.String
	if sessionID.Valid {
		id := sessionID.String
		attempt.SessionID = &id
	}
	var err error
	attempt.CreatedAt, err = parseTime(created)
	if err != nil {
		return DispatchAttempt{}, fmt.Errorf("dispatch attempt created_at: %w", err)
	}
	if finished.Valid {
		finishedAt, err := parseTime(finished.String)
		if err != nil {
			return DispatchAttempt{}, fmt.Errorf("dispatch attempt finished_at: %w", err)
		}
		attempt.FinishedAt = &finishedAt
	}
	return attempt, nil
}

// LinkSession durably records an OpenCode session against a reserved attempt.
// Repeating the same link is a no-op without another audit event. The session
// timeline is written before the scheduler prompts the model.
func (s *Store) LinkSession(ctx context.Context, attemptID, sessionID, url string) error {
	attemptID = strings.TrimSpace(attemptID)
	sessionID = strings.TrimSpace(sessionID)
	url = strings.TrimSpace(url)
	if attemptID == "" {
		return fmt.Errorf("%w: attempt ID is required", ErrInvalid)
	}
	if sessionID == "" {
		return fmt.Errorf("%w: session ID is required", ErrInvalid)
	}
	if url == "" {
		return fmt.Errorf("%w: session URL is required", ErrInvalid)
	}
	actor := ActorSystem

	return s.withTx(ctx, func(tx *sql.Tx) error {
		attempt, _, err := s.attemptByID(ctx, tx, attemptID)
		if err != nil {
			return err
		}

		var existingAttempt, existingURL string
		err = tx.QueryRowContext(ctx,
			`SELECT attempt_id, url FROM sessions WHERE id = ?`, sessionID,
		).Scan(&existingAttempt, &existingURL)
		if err == nil {
			if existingAttempt == attemptID && existingURL == url {
				return nil
			}
			return fmt.Errorf("%w: session %s is already linked elsewhere", ErrConflict, sessionID)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("store: load session %s: %w", sessionID, err)
		}
		if attempt.State != AttemptStateReserved {
			return fmt.Errorf("%w: attempt %s is already finished", ErrConflict, attemptID)
		}

		now := nowUTC()
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO sessions (id, url, state, attempt_id, created_at)
			VALUES (?, ?, 'linked', ?, ?)`,
			sessionID, url, attemptID, formatTime(now),
		); err != nil {
			return fmt.Errorf("store: link session: %w", mapConstraintError(err))
		}
		projectID, err := requireTask(ctx, tx, attempt.TaskID)
		if err != nil {
			return err
		}
		_, err = appendActivity(ctx, tx, activityInput{
			Type:      ActivitySessionLinked,
			Actor:     actor,
			TaskID:    int64Pointer(attempt.TaskID),
			ProjectID: int64Pointer(projectID),
			Timestamp: now,
			Data: map[string]any{
				"attempt_id": attemptID,
				"session_id": sessionID,
				"url":        url,
			},
		})
		return err
	})
}

// FinishAttempt terminalizes a reserved attempt exactly once. The outcome is
// always audited, even if the task was edited manually. Only a success or
// failure whose attempt is still the task's latest attempt and whose task is
// still in progress mutates the task: success moves it to in-review and clears
// the error marking, failure leaves it in progress and applies the error
// marking. An abandoned outcome never changes task state or markings; it is
// used when a reserved task was manually moved away before a prompt.
func (s *Store) FinishAttempt(ctx context.Context, attemptID string, outcome AttemptOutcome, detail string) error {
	attemptID = strings.TrimSpace(attemptID)
	if attemptID == "" {
		return fmt.Errorf("%w: attempt ID is required", ErrInvalid)
	}
	if !outcome.Valid() {
		return fmt.Errorf("%w: unsupported attempt outcome %q", ErrInvalid, outcome)
	}
	actor := ActorSystem

	return s.withTx(ctx, func(tx *sql.Tx) error {
		attempt, seq, err := s.attemptByID(ctx, tx, attemptID)
		if err != nil {
			return err
		}
		if attempt.State != AttemptStateReserved {
			return fmt.Errorf("%w: attempt %s is already finished", ErrConflict, attemptID)
		}
		task, err := taskByID(ctx, tx, attempt.TaskID)
		if err != nil {
			return err
		}

		now := nowUTC()
		result, err := tx.ExecContext(ctx, `
			UPDATE dispatch_attempts
			SET state = ?, outcome = ?, detail = ?, finished_at = ?
			WHERE id = ? AND state = 'reserved'`,
			string(outcome), string(outcome), detail, formatTime(now), attemptID,
		)
		if err != nil {
			return fmt.Errorf("store: finish dispatch attempt %s: %w", attemptID, err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("store: finish dispatch attempt %s: %w", attemptID, err)
		}
		if affected != 1 {
			return fmt.Errorf("%w: attempt %s is already finished", ErrConflict, attemptID)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE sessions SET state = ? WHERE attempt_id = ?`, string(outcome), attemptID,
		); err != nil {
			return fmt.Errorf("store: update session state: %w", err)
		}

		var latestSeq int64
		if err := tx.QueryRowContext(ctx,
			`SELECT max(seq) FROM dispatch_attempts WHERE task_id = ?`, attempt.TaskID,
		).Scan(&latestSeq); err != nil {
			return fmt.Errorf("store: load latest attempt: %w", err)
		}

		mutable := task.Status == StatusInProgress && latestSeq == seq
		switch outcome {
		case OutcomeSuccess:
			if mutable {
				if _, err := tx.ExecContext(ctx,
					`UPDATE tasks SET status = 'in-review', updated_at = ? WHERE id = ?`,
					formatTime(now), task.ID,
				); err != nil {
					return fmt.Errorf("store: complete task %d: %w", task.ID, err)
				}
				if _, err := appendActivity(ctx, tx, activityInput{
					Type:      ActivityTaskStatusChanged,
					Actor:     actor,
					TaskID:    int64Pointer(task.ID),
					ProjectID: int64Pointer(task.ProjectID),
					Timestamp: now,
					Data: map[string]any{
						"old":    string(task.Status),
						"new":    string(StatusInReview),
						"reason": "dispatch_success",
					},
				}); err != nil {
					return err
				}
				if _, err := setMarkingTx(ctx, tx, actor, task.ID, MarkingError, false, now); err != nil {
					return err
				}
			}
		case OutcomeFailure:
			if mutable {
				if _, err := setMarkingTx(ctx, tx, actor, task.ID, MarkingError, true, now); err != nil {
					return err
				}
			}
		case OutcomeAbandoned:
			// Deliberately no task state or marking mutation.
		}

		sessionIDs := []string{}
		sessionRows, err := tx.QueryContext(ctx,
			`SELECT id FROM sessions WHERE attempt_id = ? ORDER BY seq`, attemptID,
		)
		if err != nil {
			return fmt.Errorf("store: list attempt sessions: %w", err)
		}
		for sessionRows.Next() {
			var sessionID string
			if err := sessionRows.Scan(&sessionID); err != nil {
				sessionRows.Close()
				return fmt.Errorf("store: scan attempt session: %w", err)
			}
			sessionIDs = append(sessionIDs, sessionID)
		}
		if err := sessionRows.Err(); err != nil {
			sessionRows.Close()
			return fmt.Errorf("store: list attempt sessions: %w", err)
		}
		sessionRows.Close()

		_, err = appendActivity(ctx, tx, activityInput{
			Type:      ActivitySessionOutcome,
			Actor:     actor,
			TaskID:    int64Pointer(task.ID),
			ProjectID: int64Pointer(task.ProjectID),
			Timestamp: now,
			Data: map[string]any{
				"attempt_id":  attemptID,
				"outcome":     string(outcome),
				"detail":      detail,
				"session_ids": sessionIDs,
			},
		})
		return err
	})
}
