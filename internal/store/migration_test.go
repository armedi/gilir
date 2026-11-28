package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// TestOpenRejectsUnsupportedSchemaVersion checks that a database from any other
// schema version is refused instead of opened against a schema it does not
// match. The store never migrates, so the only supported version is the current
// one and the error must tell the caller to recreate the file.
func TestOpenRejectsUnsupportedSchemaVersion(t *testing.T) {
	ctx := context.Background()
	for _, version := range []int{2, 3, 4, 5} {
		path := filepath.Join(t.TempDir(), "old.db")
		s, err := Open(path)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		// PRAGMA takes a literal, not a bound parameter.
		if _, err := s.db.ExecContext(ctx,
			fmt.Sprintf("PRAGMA user_version = %d", version),
		); err != nil {
			t.Fatalf("set user_version %d: %v", version, err)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}

		refused, err := Open(path)
		if err == nil {
			_ = refused.Close()
			t.Fatalf("Open accepted schema version %d", version)
		}
		if !strings.Contains(err.Error(), "not supported") ||
			!strings.Contains(err.Error(), "delete the database file") {
			t.Fatalf("Open version %d error = %v, want an actionable refusal", version, err)
		}

		// The refusal must leave the file untouched so the operator's next step
		// is still deleting it.
		if got := rawUserVersion(t, path); got != version {
			t.Fatalf("user_version after refusal = %d, want %d", got, version)
		}
	}
}

// rawUserVersion reads PRAGMA user_version without going through Open, which
// would reject the file.
func rawUserVersion(t *testing.T, path string) int {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}
	defer db.Close()
	var version int
	if err := db.QueryRowContext(context.Background(), "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	return version
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
