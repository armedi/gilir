package store

import (
	"context"
	"path/filepath"
	"testing"
)

// TestMigrationFromVersion3AddsWorktreeColumns checks that a version 3 database
// keeps its data and gains the worktree columns. The upgrade path matters
// because existing installations carry live task history.
func TestMigrationFromVersion3AddsWorktreeColumns(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v3.db")

	// Build a current database, record data, then roll it back to version 3 by
	// dropping the columns and table the migrations add.
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	project := mustCreateProject(t, s, "legacy")
	repo := mustAddRepo(t, s, project.ID, newGitRepo(t))
	task := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: "legacy task", Status: StatusTodo,
	})
	if _, err := s.db.ExecContext(ctx, `DROP TABLE store_meta`); err != nil {
		t.Fatalf("drop store_meta: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE tasks DROP COLUMN worktree`); err != nil {
		t.Fatalf("drop tasks.worktree: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE sessions DROP COLUMN worktree`); err != nil {
		t.Fatalf("drop sessions.worktree: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA user_version = 3`); err != nil {
		t.Fatalf("set user_version: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	migrated, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = migrated.Close() })

	var version int
	if err := migrated.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d, want %d", version, schemaVersion)
	}
	for _, table := range []string{"tasks", "sessions"} {
		if !tableHasColumn(t, migrated, table, "worktree") {
			t.Fatalf("table %s is missing the worktree column after migration", table)
		}
	}
	restored := mustShow(t, migrated, task.ID)
	if restored.Task.Title != "legacy task" || restored.Task.Worktree != nil {
		t.Fatalf("task after migration = %+v", restored.Task)
	}

	// The migrated schema accepts the same worktree writes as a fresh one.
	attempt := mustReserve(t, migrated, "http://127.0.0.1:4096", 100)
	worktree := mustRecordWorktree(t, migrated, attempt.ID)
	if detail := mustShow(t, migrated, task.ID); detail.Task.Worktree == nil || *detail.Task.Worktree != worktree {
		t.Fatalf("worktree after migration = %v, want %q", detail.Task.Worktree, worktree)
	}
}

func tableHasColumn(t *testing.T, s *Store, table, column string) bool {
	t.Helper()
	rows, err := s.db.QueryContext(context.Background(), `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatalf("pragma_table_info(%s): %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		if name == column {
			return true
		}
	}
	return false
}

func TestDatabaseIDIsDurableAndUniquePerFile(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ids.db")
	first, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	id, err := first.DatabaseID(ctx)
	if err != nil || len(id) != 32 {
		t.Fatalf("DatabaseID = %q, %v; want 32 hex characters", id, err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if again, err := reopened.DatabaseID(ctx); err != nil || again != id {
		t.Fatalf("DatabaseID after reopen = %q, %v; want %q", again, err, id)
	}

	other, err := Open(filepath.Join(t.TempDir(), "other.db"))
	if err != nil {
		t.Fatalf("Open other: %v", err)
	}
	t.Cleanup(func() { _ = other.Close() })
	if otherID, err := other.DatabaseID(ctx); err != nil || otherID == id {
		t.Fatalf("second database ID = %q, %v; want a distinct ID from %q", otherID, err, id)
	}
}

// TestMigrationFromVersion4AddsDatabaseID checks the single-step upgrade that a
// version 4 installation running before this change performs. The worktree
// columns are already present, so only the metadata table is added.
func TestMigrationFromVersion4AddsDatabaseID(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v4.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	project := mustCreateProject(t, s, "v4")
	repo := mustAddRepo(t, s, project.ID, newGitRepo(t))
	task := mustCreateTask(t, s, CreateTaskParams{
		ProjectID: project.ID, RepoID: repo.ID, Title: "kept", Status: StatusTodo,
	})
	if _, err := s.db.ExecContext(ctx, `DROP TABLE store_meta`); err != nil {
		t.Fatalf("drop store_meta: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA user_version = 4`); err != nil {
		t.Fatalf("set user_version: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	migrated, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = migrated.Close() })
	if id, err := migrated.DatabaseID(ctx); err != nil || len(id) != 32 {
		t.Fatalf("DatabaseID = %q, %v", id, err)
	}
	if restored := mustShow(t, migrated, task.ID); restored.Task.Title != "kept" {
		t.Fatalf("task after migration = %+v", restored.Task)
	}
}
