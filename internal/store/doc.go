// Package store implements Gilir's durable SQLite domain model.
//
// A Store owns a single database file. Open configures every connection with
// foreign-key enforcement, a busy timeout, WAL journaling, and immediate
// transactions via the SQLite DSN, sets the schema user_version, and serializes
// access through one pooled connection so readers and writers observe a
// coherent database. Multiple processes may open the same file; SQLite's WAL
// mode and busy timeout coordinate them, and every transaction takes the write
// lock up front to avoid deferred-lock upgrade failures. Tools that open the
// database directly should use an equivalent DSN:
//
//	file:<absolute-path>?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_txlock=immediate
//
// Projects, repositories, and tasks use AUTOINCREMENT integer primary keys, so
// IDs are positive and allocated in insertion order. Tasks refer to exactly one
// repository in their project through a composite foreign key, and task
// dependencies use composite foreign keys to prove both ends belong to the same
// project. Self-dependencies and dependency cycles are rejected inside the
// writing transaction using a recursive CTE.
//
// All domain mutations execute in one transaction and append an immutable
// activity row. Activity rows are ordered by their AUTOINCREMENT ID and can be
// read incrementally with ActivityAfter for hook outboxes. Per-hook cursors are
// durable and monotonic. No API deletes activity, and database triggers reject
// UPDATE and DELETE on the activity table.
//
// A project or repository with dependent work cannot be deleted, and task
// deletion is intentionally unsupported. This keeps the audit trail complete.
// Queue dispatch reservations are persisted before any external effect so the
// scheduler can recover after a restart. Attempt IDs are random 128-bit
// identifiers that are unique across independent Gilir databases; session and
// prompt IDs are derived from them by the scheduler.
package store
