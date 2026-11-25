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

	"modernc.org/sqlite"
)

// Sentinel errors returned (usually wrapped) at API boundaries.
var (
	ErrNotFound = errors.New("store: not found")
	ErrInvalid  = errors.New("store: invalid")
	ErrConflict = errors.New("store: conflict")
)

const schemaVersion = 1

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

	s := &Store{db: db}
	if err := s.initialize(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
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

	var version int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("store: read schema version: %w", err)
	}
	switch {
	case version == 0:
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
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
			return fmt.Errorf("store: set schema version: %w", err)
		}
	case version == schemaVersion:
		// Current schema.
	case version > schemaVersion:
		return fmt.Errorf("store: database schema version %d is newer than supported version %d", version, schemaVersion)
	default:
		return fmt.Errorf("store: database schema version %d is not supported", version)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit schema transaction: %w", err)
	}
	return nil
}

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
	TaskID    *int64
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
		in.Type, actor, nullableInt64(in.TaskID), nullableInt64(in.ProjectID), formatTime(at), string(encoded),
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
		TaskID:    copyInt64(in.TaskID),
		ProjectID: copyInt64(in.ProjectID),
		Timestamp: at.UTC(),
		Data:      data,
	}, nil
}

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
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

type scanner interface {
	Scan(dest ...any) error
}

func scanActivity(sc scanner) (Activity, error) {
	var (
		activity  Activity
		taskID    sql.NullInt64
		projectID sql.NullInt64
		timestamp string
		encoded   string
	)
	if err := sc.Scan(&activity.ID, &activity.Type, &activity.Actor, &taskID, &projectID, &timestamp, &encoded); err != nil {
		return Activity{}, err
	}
	if taskID.Valid {
		activity.TaskID = int64Pointer(taskID.Int64)
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

func requireTask(ctx context.Context, tx *sql.Tx, taskID int64) (int64, error) {
	var projectID int64
	err := tx.QueryRowContext(ctx, `SELECT project_id FROM tasks WHERE id = ?`, taskID).Scan(&projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("%w: task %d", ErrNotFound, taskID)
	}
	if err != nil {
		return 0, fmt.Errorf("store: load task %d: %w", taskID, err)
	}
	return projectID, nil
}
