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
// Projects and repositories use AUTOINCREMENT integer primary keys, so their
// IDs are positive and allocated in insertion order. Tasks use a hidden
// AUTOINCREMENT "seq" as their true key and expose the derived text id
// "GILIR-<seq>" as the task ID. The prefix is stored with every task row and is
// what agents see in prompts and what callers pass back, so a task reference
// always identifies Gilir task management. Because the text ID embeds a
// variable-length number, ordering uses seq, never the text key. A repository is a
// canonical Git root registered once globally and linked to one or more
// projects through project_repos. Tasks target a linked (project, repository)
// pair through a composite foreign key, so every task's repository belongs to
// its project, while task dependencies use composite foreign keys to prove both
// ends belong to the same project. Referencing tables (markings, dependencies,
// attempts, activity) store the text task ID. Self-dependencies and dependency cycles are
// rejected inside the writing transaction using a recursive CTE.
//
// All domain mutations execute in one transaction and append an immutable
// activity row. Activity rows are ordered by their AUTOINCREMENT ID and can be
// read incrementally with ActivityAfter for hook outboxes. Per-hook cursors are
// durable and monotonic. No API deletes activity, and database triggers reject
// UPDATE and DELETE on the activity table.
//
// Every repository stays linked to at least one project. A project with tasks,
// or whose deletion would orphan a repository's last link, cannot be deleted; a
// repository with tasks cannot be removed, and a repository link cannot be
// removed while a task in that project targets it. A task with a reserved
// dispatch attempt or an incoming dependency cannot be deleted. Deleting a task
// removes its outgoing dependencies, terminal dispatch attempts and their local
// sessions, and markings, while the append-only activity history records the
// deletion and is never discarded. This keeps the audit trail complete.
// Queue dispatch reservations are persisted before any external effect so the
// scheduler can recover after a restart. Attempt IDs are random 128-bit
// identifiers that are unique across independent Gilir databases; session and
// prompt IDs are derived from them by the scheduler.
package store
