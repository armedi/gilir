package store

var schemaStatements = []string{
	`CREATE TABLE projects (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL CHECK (length(trim(name)) > 0),
		UNIQUE (name)
	) STRICT`,

	`CREATE TABLE repos (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		path TEXT NOT NULL CHECK (length(trim(path)) > 0),
		UNIQUE (path)
	) STRICT`,

	`CREATE TABLE project_repos (
		project_id INTEGER NOT NULL,
		repo_id INTEGER NOT NULL,
		PRIMARY KEY (project_id, repo_id),
		FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE RESTRICT,
		FOREIGN KEY (repo_id) REFERENCES repos(id) ON DELETE RESTRICT
	) STRICT`,

	`CREATE TABLE tasks (
		seq INTEGER PRIMARY KEY AUTOINCREMENT,
		id TEXT NOT NULL UNIQUE GENERATED ALWAYS AS ('GILIR-' || seq) STORED,
		project_id INTEGER NOT NULL,
		repo_id INTEGER NOT NULL,
		title TEXT NOT NULL CHECK (length(trim(title)) > 0),
		body TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'backlog'
			CHECK (status IN ('backlog', 'todo', 'in-progress', 'in-review', 'done')),
		priority TEXT NOT NULL DEFAULT 'medium'
			CHECK (priority IN ('low', 'medium', 'high', 'urgent')),
		worktree TEXT CHECK (worktree IS NULL OR length(trim(worktree)) > 0),
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		UNIQUE (id, project_id),
		FOREIGN KEY (project_id, repo_id)
			REFERENCES project_repos(project_id, repo_id) ON DELETE RESTRICT
	) STRICT`,

	`CREATE TABLE task_markings (
		task_id TEXT NOT NULL,
		marking TEXT NOT NULL CHECK (length(trim(marking)) > 0),
		created_at TEXT NOT NULL,
		PRIMARY KEY (task_id, marking),
		FOREIGN KEY (task_id) REFERENCES tasks(id) ON DELETE CASCADE
	) STRICT`,

	`CREATE TABLE task_dependencies (
		task_id TEXT NOT NULL,
		depends_on_id TEXT NOT NULL,
		project_id INTEGER NOT NULL,
		created_at TEXT NOT NULL,
		PRIMARY KEY (task_id, depends_on_id),
		CHECK (task_id <> depends_on_id),
		FOREIGN KEY (task_id, project_id) REFERENCES tasks(id, project_id) ON DELETE RESTRICT,
		FOREIGN KEY (depends_on_id, project_id) REFERENCES tasks(id, project_id) ON DELETE RESTRICT
	) STRICT`,

	`CREATE TABLE dispatch_attempts (
		seq INTEGER PRIMARY KEY AUTOINCREMENT,
		id TEXT NOT NULL UNIQUE CHECK (length(trim(id)) > 0),
		task_id TEXT NOT NULL,
		repo_path TEXT NOT NULL CHECK (length(trim(repo_path)) > 0),
		server_url TEXT NOT NULL CHECK (length(trim(server_url)) > 0),
		server_pid INTEGER NOT NULL CHECK (server_pid > 0),
		state TEXT NOT NULL DEFAULT 'reserved'
			CHECK (state IN ('reserved', 'success', 'failure', 'abandoned')),
		outcome TEXT
			CHECK (outcome IS NULL OR outcome IN ('success', 'failure', 'abandoned')),
		detail TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		finished_at TEXT,
		FOREIGN KEY (task_id) REFERENCES tasks(id) ON DELETE RESTRICT
	) STRICT`,

	`CREATE TABLE sessions (
		seq INTEGER PRIMARY KEY AUTOINCREMENT,
		id TEXT NOT NULL UNIQUE CHECK (length(trim(id)) > 0),
		url TEXT NOT NULL CHECK (length(trim(url)) > 0),
		state TEXT NOT NULL,
		attempt_id TEXT NOT NULL,
		worktree TEXT CHECK (worktree IS NULL OR length(trim(worktree)) > 0),
		created_at TEXT NOT NULL,
		FOREIGN KEY (attempt_id) REFERENCES dispatch_attempts(id) ON DELETE RESTRICT
	) STRICT`,

	`CREATE TABLE activity (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		type TEXT NOT NULL,
		actor TEXT NOT NULL CHECK (length(trim(actor)) > 0),
		task_id TEXT,
		project_id INTEGER,
		timestamp TEXT NOT NULL,
		data TEXT NOT NULL DEFAULT '{}'
	) STRICT`,

	`CREATE TABLE hook_cursors (
		hook TEXT PRIMARY KEY CHECK (length(trim(hook)) > 0),
		last_id INTEGER NOT NULL DEFAULT 0 CHECK (last_id >= 0),
		updated_at TEXT NOT NULL
	) STRICT`,

	`CREATE TABLE queue_settings (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		paused INTEGER NOT NULL DEFAULT 0 CHECK (paused IN (0, 1)),
		max_concurrency INTEGER NOT NULL DEFAULT 1 CHECK (max_concurrency > 0),
		updated_at TEXT NOT NULL
	) STRICT`,

	// store_meta holds the database ID and pending worktree name reservations.
	`CREATE TABLE store_meta (
		key TEXT PRIMARY KEY CHECK (length(trim(key)) > 0),
		value TEXT NOT NULL
	) STRICT`,

	`CREATE INDEX idx_project_repos_repo ON project_repos (repo_id)`,
	`CREATE INDEX idx_tasks_project ON tasks (project_id)`,
	`CREATE INDEX idx_tasks_repo ON tasks (repo_id)`,
	`CREATE INDEX idx_tasks_status ON tasks (status)`,
	`CREATE INDEX idx_tasks_priority ON tasks (priority)`,
	`CREATE INDEX idx_task_markings_marking ON task_markings (marking, task_id)`,
	`CREATE INDEX idx_dependencies_blocker ON task_dependencies (depends_on_id)`,
	`CREATE INDEX idx_attempts_task ON dispatch_attempts (task_id, state)`,
	`CREATE UNIQUE INDEX idx_attempts_one_reserved_per_task
		ON dispatch_attempts (task_id) WHERE state = 'reserved'`,
	`CREATE INDEX idx_sessions_attempt ON sessions (attempt_id)`,
	`CREATE INDEX idx_activity_task ON activity (task_id, id)`,

	`CREATE TRIGGER activity_append_only_update
		BEFORE UPDATE ON activity
		BEGIN
			SELECT RAISE(ABORT, 'activity is append-only');
		END`,
	`CREATE TRIGGER activity_append_only_delete
		BEFORE DELETE ON activity
		BEGIN
			SELECT RAISE(ABORT, 'activity is append-only');
		END`,
}
