package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const (
	defaultActivityLimit = 100
	maxActivityLimit     = 1000
)

// ActivityAfter returns up to limit activity rows with IDs greater than afterID,
// in ascending audit order. A nonpositive limit selects 100 and the maximum
// page is 1000. Passing 0 reads from the beginning.
func (s *Store) ActivityAfter(ctx context.Context, afterID int64, limit int) ([]Activity, error) {
	if afterID < 0 {
		return nil, fmt.Errorf("%w: activity cursor must not be negative", ErrInvalid)
	}
	if limit <= 0 {
		limit = defaultActivityLimit
	}
	if limit > maxActivityLimit {
		limit = maxActivityLimit
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, type, actor, task_id, project_id, timestamp, data
		FROM activity
		WHERE id > ?
		ORDER BY id
		LIMIT ?`, afterID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list activity after %d: %w", afterID, err)
	}
	defer rows.Close()

	activities := []Activity{}
	for rows.Next() {
		activity, err := scanActivity(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan activity: %w", err)
		}
		activities = append(activities, activity)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list activity after %d: %w", afterID, err)
	}
	return activities, nil
}

// HookCursor returns a durable hook cursor. initialized is false when the hook
// has never saved a cursor, allowing a new hook to start at LatestActivityID
// rather than replaying history. A missing hook returns (0, false, nil).
func (s *Store) HookCursor(ctx context.Context, hook string) (lastID int64, initialized bool, err error) {
	hook = strings.TrimSpace(hook)
	if hook == "" {
		return 0, false, fmt.Errorf("%w: hook name is required", ErrInvalid)
	}
	err = s.db.QueryRowContext(ctx,
		`SELECT last_id FROM hook_cursors WHERE hook = ?`, hook,
	).Scan(&lastID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("store: load hook cursor: %w", err)
	}
	return lastID, true, nil
}

// SaveHookCursor persists a hook cursor monotonically. A lower value than the
// stored cursor is ignored; a negative value is rejected. Cursor state is
// operational metadata and is not written to the activity stream.
func (s *Store) SaveHookCursor(ctx context.Context, hook string, cursor int64) error {
	hook = strings.TrimSpace(hook)
	if hook == "" {
		return fmt.Errorf("%w: hook name is required", ErrInvalid)
	}
	if cursor < 0 {
		return fmt.Errorf("%w: hook cursor must not be negative", ErrInvalid)
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO hook_cursors (hook, last_id, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(hook) DO UPDATE SET
			last_id = excluded.last_id,
			updated_at = excluded.updated_at
		WHERE excluded.last_id > hook_cursors.last_id`,
		hook, cursor, formatTime(nowUTC()),
	); err != nil {
		return fmt.Errorf("store: save hook cursor: %w", err)
	}
	return nil
}

// LatestActivityID returns the current maximum activity ID, or 0 when no
// activity exists. New hook configurations use it as their initial cursor.
func (s *Store) LatestActivityID(ctx context.Context) (int64, error) {
	var latest int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT coalesce(max(id), 0) FROM activity`,
	).Scan(&latest); err != nil {
		return 0, fmt.Errorf("store: load latest activity ID: %w", err)
	}
	return latest, nil
}
