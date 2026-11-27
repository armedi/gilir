package store

import "time"

// Status is the workflow state of a task.
type Status string

const (
	StatusBacklog    Status = "backlog"
	StatusTodo       Status = "todo"
	StatusInProgress Status = "in-progress"
	StatusInReview   Status = "in-review"
	StatusDone       Status = "done"
)

// Valid reports whether s is a supported task status.
func (s Status) Valid() bool {
	switch s {
	case StatusBacklog, StatusTodo, StatusInProgress, StatusInReview, StatusDone:
		return true
	default:
		return false
	}
}

// Priority is the ordering priority of a task. Higher priorities are reserved
// before lower ones.
type Priority string

const (
	PriorityLow    Priority = "low"
	PriorityMedium Priority = "medium"
	PriorityHigh   Priority = "high"
	PriorityUrgent Priority = "urgent"
)

// Valid reports whether p is a supported task priority.
func (p Priority) Valid() bool {
	switch p {
	case PriorityLow, PriorityMedium, PriorityHigh, PriorityUrgent:
		return true
	default:
		return false
	}
}

// Well-known task markings. Markings are free-form nonblank strings; these two
// participate in dispatch and review behavior.
const (
	MarkingChangesRequested = "changes-requested"
	MarkingError            = "error"
)

// Project identifies a named Gilir project.
type Project struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Repo identifies a globally registered Git repository. ProjectIDs lists the
// projects the repository is linked to, sorted ascending. A repo is always
// linked to at least one project.
type Repo struct {
	ID         int64   `json:"id"`
	Path       string  `json:"path"`
	ProjectIDs []int64 `json:"project_ids"`
}

// Task is the task representation exposed by the store.
type Task struct {
	ID          string    `json:"id"`
	ProjectID   int64     `json:"project_id"`
	RepoID      int64     `json:"repo_id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Status      Status    `json:"status"`
	Priority    Priority  `json:"priority"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	Markings    []string  `json:"markings"`
}

// Session is an OpenCode session linked to a dispatch attempt. Sessions are
// returned in link order.
type Session struct {
	ID        string    `json:"id"`
	URL       string    `json:"url"`
	State     string    `json:"state"`
	AttemptID string    `json:"attempt_id"`
	CreatedAt time.Time `json:"created_at"`
}

// Activity is one immutable audit record. Data is always a JSON object.
type Activity struct {
	ID        int64          `json:"id"`
	Type      string         `json:"type"`
	Actor     string         `json:"actor"`
	TaskID    *string        `json:"task_id"`
	ProjectID *int64         `json:"project_id"`
	Timestamp time.Time      `json:"timestamp"`
	Data      map[string]any `json:"data"`
}

// TaskDetail is the complete view of a task returned by Show.
type TaskDetail struct {
	Task     Task       `json:"task"`
	Blockers []string   `json:"blockers"`
	Sessions []Session  `json:"sessions"`
	Activity []Activity `json:"activity"`
}

// Audit event types recorded by the store.
const (
	ActivityProjectCreated = "project.created"
	ActivityProjectRenamed = "project.renamed"
	ActivityProjectDeleted = "project.deleted"
	ActivityRepoAdded      = "repo.added"
	ActivityRepoLinked     = "repo.linked"
	ActivityRepoUnlinked   = "repo.unlinked"
	ActivityRepoRemoved    = "repo.removed"
	ActivityQueueUpdated   = "queue.updated"

	ActivityTaskCreated        = "task.created"
	ActivityTaskUpdated        = "task.updated"
	ActivityTaskDeleted        = "task.deleted"
	ActivityTaskStatusChanged  = "task.status_changed"
	ActivityTaskMarking        = "task.marking_changed"
	ActivityTaskCommented      = "task.commented"
	ActivityTaskCommentEdited  = "task.comment_edited"
	ActivityTaskCommentRemoved = "task.comment_removed"
	ActivityTaskClaimed        = "task.claimed"
	ActivityDependencyAdded    = "dependency.added"
	ActivityDependencyRemoved  = "dependency.removed"
	ActivitySessionLinked      = "session.linked"
	ActivitySessionOutcome     = "session.outcome"
)

// CreateTaskParams describes a new task. RepoID is required. A zero ProjectID
// infers the project when the repository belongs to exactly one; a repository
// linked to multiple projects requires an explicit ProjectID. Empty Status and
// Priority select the defaults backlog and medium.
type CreateTaskParams struct {
	ProjectID   int64
	RepoID      int64
	Title       string
	Description string
	Status      Status
	Priority    Priority
}

// TaskPatch is a partial task edit. Nil pointer fields are left unchanged;
// non-nil pointers distinguish an explicit empty value from an unset field.
// Message is required when Status moves a task from in-review back to todo and
// is recorded as a review comment.
type TaskPatch struct {
	Title       *string
	Description *string
	Priority    *Priority
	RepoID      *int64
	Status      *Status
	Message     *string
}

// TaskFilter restricts ListTasks. Zero values mean no restriction.
type TaskFilter struct {
	ProjectID int64
	RepoID    int64
	Status    Status
	Priority  Priority
	Marking   string
}

// ReviewDecision is the outcome of a task review.
type ReviewDecision string

const (
	ReviewApprove        ReviewDecision = "approve"
	ReviewRequestChanges ReviewDecision = "request_changes"
)

// AttemptState is the persisted lifecycle state of a dispatch attempt.
type AttemptState string

const (
	AttemptStateReserved  AttemptState = "reserved"
	AttemptStateSuccess   AttemptState = "success"
	AttemptStateFailure   AttemptState = "failure"
	AttemptStateAbandoned AttemptState = "abandoned"
)

// AttemptOutcome is a terminal dispatch outcome accepted by FinishAttempt.
type AttemptOutcome string

const (
	OutcomeSuccess   AttemptOutcome = "success"
	OutcomeFailure   AttemptOutcome = "failure"
	OutcomeAbandoned AttemptOutcome = "abandoned"
)

// Valid reports whether the outcome is terminal and supported.
func (o AttemptOutcome) Valid() bool {
	switch o {
	case OutcomeSuccess, OutcomeFailure, OutcomeAbandoned:
		return true
	default:
		return false
	}
}

// DispatchAttempt records a durable reservation of a task for one external
// OpenCode run. RepoPath is snapshotted at reservation time. SessionID is the
// first session linked to the attempt, when one exists; ListOpenAttempts fills
// it from the session table so watchers need not load TaskDetail.
type DispatchAttempt struct {
	ID         string         `json:"id"`
	TaskID     string         `json:"task_id"`
	RepoPath   string         `json:"repo_path"`
	ServerURL  string         `json:"server_url"`
	ServerPID  int64          `json:"server_pid"`
	State      AttemptState   `json:"state"`
	Outcome    AttemptOutcome `json:"outcome,omitempty"`
	Detail     string         `json:"detail,omitempty"`
	SessionID  *string        `json:"session_id,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
	FinishedAt *time.Time     `json:"finished_at"`
}

// QueueSettings controls dispatch reservation.
type QueueSettings struct {
	Paused         bool      `json:"paused"`
	MaxConcurrency int       `json:"max_concurrency"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// QueueSettingsPatch is a partial queue-settings update.
type QueueSettingsPatch struct {
	Paused         *bool
	MaxConcurrency *int
}
