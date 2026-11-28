package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

const worktreeNameKey = "worktree_name/"

// ReserveWorktreeName keeps the first name chosen for this task and OpenCode
// project until its directory is recorded. existing reports whether an earlier
// call already reserved the name and may have sent the create request.
func (s *Store) ReserveWorktreeName(ctx context.Context, attemptID, projectID string, directories []string) (name string, existing bool, err error) {
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		attempt, _, err := s.attemptByID(ctx, tx, attemptID)
		if err != nil {
			return err
		}
		if attempt.State != AttemptStateReserved {
			return fmt.Errorf("%w: attempt %s is already finished", ErrConflict, attemptID)
		}

		prefix := worktreeNameKey + projectID + "/"
		key := prefix + attempt.TaskID
		err = tx.QueryRowContext(ctx, `SELECT value FROM store_meta WHERE key = ?`, key).Scan(&name)
		switch {
		case err == nil:
			existing = true
			return nil
		case !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("store: load reserved worktree name for %s: %w", attempt.TaskID, err)
		}

		used := make(map[string]bool, len(directories))
		for _, directory := range directories {
			used[strings.ToLower(filepath.Base(directory))] = true
		}
		rows, err := tx.QueryContext(ctx, `SELECT value FROM store_meta WHERE substr(key, 1, length(?)) = ?`, prefix, prefix)
		if err != nil {
			return fmt.Errorf("store: list reserved worktree names: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var reserved string
			if err := rows.Scan(&reserved); err != nil {
				return fmt.Errorf("store: read reserved worktree name: %w", err)
			}
			used[reserved] = true
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("store: list reserved worktree names: %w", err)
		}

		var title string
		if err := tx.QueryRowContext(ctx, `SELECT title FROM tasks WHERE id = ?`, attempt.TaskID).Scan(&title); err != nil {
			return fmt.Errorf("store: load title for %s: %w", attempt.TaskID, err)
		}
		base := worktreeSlug(title)
		name = base
		for suffix := 2; used[name]; suffix++ {
			name = fmt.Sprintf("%s-%d", base, suffix)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO store_meta (key, value) VALUES (?, ?)`, key, name); err != nil {
			return fmt.Errorf("store: reserve worktree name for %s: %w", attempt.TaskID, err)
		}
		return nil
	})
	return name, existing, err
}

func worktreeSlug(title string) string {
	const maxLength = 48
	slug := strings.Join(strings.FieldsFunc(strings.ToLower(title), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}), "-")
	if len(slug) > maxLength {
		cut := strings.LastIndexByte(slug[:maxLength+1], '-')
		if cut <= 0 {
			cut = maxLength
		}
		slug = slug[:cut]
	}
	if !filepath.IsLocal(slug) {
		return "task"
	}
	return slug
}

func clearWorktreeName(ctx context.Context, tx *sql.Tx, taskID string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM store_meta WHERE key GLOB ?`, worktreeNameKey+"*/"+taskID); err != nil {
		return fmt.Errorf("store: release worktree name for %s: %w", taskID, err)
	}
	return nil
}
