package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/armedi/gilir/internal/taskid"
	"modernc.org/sqlite"
)

// Sentinel errors returned (usually wrapped) at API boundaries.
var (
	ErrNotFound = errors.New("store: not found")
	ErrInvalid  = errors.New("store: invalid")
	ErrConflict = errors.New("store: conflict")
)

// schemaVersion 6 stores the task text field as tasks.body. Version 5 added the
// durable per-database ID used to namespace external resources such as OpenCode
// worktrees, and version 4 added the per-task worktree directory. The schema is
// never migrated: a database from any other version is rejected at Open so the
// caller deletes the file and recreates it.
const schemaVersion = 6

const databaseBusyTimeout = "5000"

// ActorSystem is used for scheduler-driven mutations that do not represent a
// human or external client action.
const ActorSystem = "scheduler"

// Store is a handle to one Gilir SQLite database. It is safe for concurrent use
// by multiple goroutines; access is serialized in-process.
type Store struct {
	db *sql.DB
}

// Open opens or creates the SQLite database at path. It verifies the connection
// settings, installs the current schema on a fresh database, and rejects
// databases created by a newer schema version.
func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("%w: database path is required", ErrInvalid)
	}

	dsn, err := databaseDSN(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	if err := checkDatabaseState(context.Background(), db); err != nil {
		_ = db.Close()
		return nil, err
	}

	s := &Store{db: db}
	if err := s.initialize(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// checkDatabaseState refuses a database before any write, including the WAL
// switch in the DSN, so a rejected file is left byte-identical for the operator
// to delete. It rejects a database from another schema version and a
// non-empty file that carries no schema version at all; only an empty file is
// initialized from scratch.
func checkDatabaseState(ctx context.Context, db *sql.DB) error {
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("store: read schema version: %w", err)
	}
	switch {
	case version == schemaVersion:
		return nil
	case version > schemaVersion:
		return fmt.Errorf("store: database schema version %d is newer than supported version %d", version, schemaVersion)
	case version != 0:
		return fmt.Errorf("store: database schema version %d is not supported; delete the database file and restart to recreate it", version)
	}

	// Version 0 is either a fresh file or a foreign database that never recorded
	// a version. Only an empty file may be initialized, or gilir would stamp its
	// schema onto unrelated data.
	empty, err := isDatabaseEmpty(ctx, db)
	if err != nil {
		return err
	}
	if !empty {
		return errors.New("store: database file is not empty and carries no gilir schema version; delete the database file and restart to recreate it")
	}
	return nil
}

// isDatabaseEmpty reports whether the database has no schema objects at all.
func isDatabaseEmpty(ctx context.Context, db *sql.DB) (bool, error) {
	var count int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'`,
	).Scan(&count); err != nil {
		return false, fmt.Errorf("store: inspect database schema: %w", err)
	}
	return count == 0, nil
}

// Close releases the database handle.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("store: close database: %w", err)
	}
	return nil
}

func databaseDSN(path string) (string, error) {
	if path == ":memory:" {
		suffix, err := randomHex(8)
		if err != nil {
			return "", err
		}
		return "file:gilir-memory-" + suffix +
			"?mode=memory&cache=shared" +
			"&_pragma=foreign_keys(1)" +
			"&_pragma=busy_timeout(" + databaseBusyTimeout + ")" +
			"&_txlock=immediate", nil
	}

	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve database path: %w", err)
	}
	u := url.URL{Scheme: "file", Path: absolute}
	q := u.Query()
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout("+databaseBusyTimeout+")")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Set("_txlock", "immediate")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (s *Store) initialize(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return fmt.Errorf("store: ping database: %w", err)
	}
	var foreignKeys int
	if err := s.db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return fmt.Errorf("store: read foreign_keys pragma: %w", err)
	}
	if foreignKeys != 1 {
		return errors.New("store: foreign key enforcement could not be enabled")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin schema transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	// checkDatabaseState already refused every version but 0 and the current one,
	// so reaching here with an empty schema means this is a fresh database.
	var version int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("store: read schema version: %w", err)
	}
	if version == 0 {
		for _, statement := range schemaStatements {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("store: create schema: %w", err)
			}
		}
		now := formatTime(nowUTC())
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO queue_settings (id, paused, max_concurrency, updated_at) VALUES (1, 0, 1, ?)`,
			now,
		); err != nil {
			return fmt.Errorf("store: initialize queue settings: %w", err)
		}
		// A database created here has no ID yet. Recording one keeps externally
		// created resources (worktrees) attributable to this database.
		databaseID, err := randomHex(16)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO store_meta (key, value) VALUES ('database_id', ?)`,
			databaseID,
		); err != nil {
			return fmt.Errorf("store: initialize database ID: %w", err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
			return fmt.Errorf("store: set schema version: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit schema transaction: %w", err)
	}
	return nil
}

// DatabaseID returns the durable random identifier recorded for this database
// file at creation. Callers namespace external resources with it so a recreated
// database cannot adopt resources created by the one it replaced.
func (s *Store) DatabaseID(ctx context.Context) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx,
		`SELECT value FROM store_meta WHERE key = 'database_id'`, nil,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: database ID", ErrNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("store: load database ID: %w", err)
	}
	return id, nil
}

// withTx runs fn in one immediate transaction, rolling back on any error.
func (s *Store) withTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit transaction: %w", err)
	}
	committed = true
	return nil
}

func nowUTC() time.Time {
	return time.Now().UTC().Truncate(time.Microsecond)
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(value string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse stored timestamp %q: %w", value, err)
	}
	return t.UTC(), nil
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate random identifier: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func validateID(label string, id int64) error {
	if id <= 0 {
		return fmt.Errorf("%w: %s must be a positive ID", ErrInvalid, label)
	}
	return nil
}

// normalizeTaskID validates and canonicalizes a Gilir task ID such as
// "GILIR-42". It accepts any case for the prefix and returns the uppercase
// form so lookups always match the stored key. The error never echoes the
// malformed input as a valid identifier; it explains the expected shape.
func normalizeTaskID(id string) (string, error) {
	canonical, err := taskid.Normalize(id)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrInvalid, err)
	}
	return canonical, nil
}

func normalizeActor(actor string) (string, error) {
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return "", fmt.Errorf("%w: actor is required", ErrInvalid)
	}
	return actor, nil
}

func isConstraintError(err error) bool {
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) {
		return sqliteErr.Code()&0xff == 19 // SQLITE_CONSTRAINT
	}
	return false
}

func mapConstraintError(err error) error {
	if err == nil {
		return nil
	}
	if isConstraintError(err) {
		return fmt.Errorf("%w: %v", ErrConflict, err)
	}
	return err
}

type activityInput struct {
	Type      string
	Actor     string
	TaskID    *string
	ProjectID *int64
	Timestamp time.Time
	Data      map[string]any
}

func appendActivity(ctx context.Context, tx *sql.Tx, in activityInput) (Activity, error) {
	if strings.TrimSpace(in.Type) == "" {
		return Activity{}, fmt.Errorf("%w: activity type is required", ErrInvalid)
	}
	actor, err := normalizeActor(in.Actor)
	if err != nil {
		return Activity{}, err
	}
	at := in.Timestamp
	if at.IsZero() {
		at = nowUTC()
	}
	data := in.Data
	if data == nil {
		data = map[string]any{}
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return Activity{}, fmt.Errorf("store: encode activity data: %w", err)
	}

	result, err := tx.ExecContext(ctx, `
		INSERT INTO activity (type, actor, task_id, project_id, timestamp, data)
		VALUES (?, ?, ?, ?, ?, ?)`,
		in.Type, actor, nullableString(in.TaskID), nullableInt64(in.ProjectID), formatTime(at), string(encoded),
	)
	if err != nil {
		return Activity{}, fmt.Errorf("store: append activity: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Activity{}, fmt.Errorf("store: read activity ID: %w", err)
	}
	return Activity{
		ID:        id,
		Type:      in.Type,
		Actor:     actor,
		TaskID:    copyString(in.TaskID),
		ProjectID: copyInt64(in.ProjectID),
		Timestamp: at.UTC(),
		Data:      data,
	}, nil
}

func nullableString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func copyString(value *string) *string {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func copyInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func int64Pointer(value int64) *int64 {
	return &value
}

func stringPointer(value string) *string {
	return &value
}

type scanner interface {
	Scan(dest ...any) error
}

func scanActivity(sc scanner) (Activity, error) {
	var (
		activity  Activity
		taskID    sql.NullString
		projectID sql.NullInt64
		timestamp string
		encoded   string
	)
	if err := sc.Scan(&activity.ID, &activity.Type, &activity.Actor, &taskID, &projectID, &timestamp, &encoded); err != nil {
		return Activity{}, err
	}
	if taskID.Valid {
		activity.TaskID = stringPointer(taskID.String)
	}
	if projectID.Valid {
		activity.ProjectID = int64Pointer(projectID.Int64)
	}
	parsed, err := parseTime(timestamp)
	if err != nil {
		return Activity{}, fmt.Errorf("store: scan activity: %w", err)
	}
	activity.Timestamp = parsed
	data := map[string]any{}
	if err := json.Unmarshal([]byte(encoded), &data); err != nil {
		return Activity{}, fmt.Errorf("store: decode activity data: %w", err)
	}
	if data == nil {
		data = map[string]any{}
	}
	activity.Data = data
	return activity, nil
}

func requireTask(ctx context.Context, tx *sql.Tx, taskID string) (int64, error) {
	var projectID int64
	err := tx.QueryRowContext(ctx, `SELECT project_id FROM tasks WHERE id = ?`, taskID).Scan(&projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("%w: task %s", ErrNotFound, taskID)
	}
	if err != nil {
		return 0, fmt.Errorf("store: load task %s: %w", taskID, err)
	}
	return projectID, nil
}
