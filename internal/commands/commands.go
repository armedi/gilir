// Package commands implements the shared Gilir command service. The same
// service handles wire commands for both the CLI and the local HTTP API:
// requests are strict JSON objects and the result is a store object, a small
// status document, or {"ok":true} for mutations with no natural result.
package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/armedi/gilir/internal/store"
)

// Snapshot is the dynamic watch state of a running daemon. ActiveParents is
// nil when watcher health is unknown; zero is a known count of zero.
type Snapshot struct {
	ActiveParents *int
	WatcherError  string
}

// Service executes wire commands against an open store.
type Service struct {
	store  *store.Store
	status func() Snapshot
}

// New returns a command service. status is optional; without it
// daemon.status reports an unknown (null) active parent count.
func New(st *store.Store, status func() Snapshot) *Service {
	return &Service{store: st, status: status}
}

// Execute runs one wire command with strict argument decoding. Store
// ErrNotFound, ErrInvalid, and ErrConflict sentinels survive wrapping so
// transports can classify them with Classify.
func (s *Service) Execute(ctx context.Context, command, actor string, args json.RawMessage) (any, error) {
	switch command {
	case "project.list":
		var in projectListArgs
		if err := decodeArgs(args, &in); err != nil {
			return nil, err
		}
		return s.store.ListProjects(ctx)

	case "project.create":
		var in projectCreateArgs
		if err := decodeArgs(args, &in); err != nil {
			return nil, err
		}
		if err := requireName("name", in.Name); err != nil {
			return nil, err
		}
		return s.store.CreateProject(ctx, actor, in.Name)

	case "project.rename":
		var in projectRenameArgs
		if err := decodeArgs(args, &in); err != nil {
			return nil, err
		}
		if err := requireID("id", in.ID); err != nil {
			return nil, err
		}
		if err := requireName("name", in.Name); err != nil {
			return nil, err
		}
		return s.store.RenameProject(ctx, actor, in.ID, in.Name)

	case "project.remove":
		var in projectRemoveArgs
		if err := decodeArgs(args, &in); err != nil {
			return nil, err
		}
		if err := requireID("id", in.ID); err != nil {
			return nil, err
		}
		if err := s.store.DeleteProject(ctx, actor, in.ID); err != nil {
			return nil, err
		}
		return okResult{OK: true}, nil

	case "repo.list":
		var in repoListArgs
		if err := decodeArgs(args, &in); err != nil {
			return nil, err
		}
		if err := requireID("project_id", in.ProjectID); err != nil {
			return nil, err
		}
		return s.store.ListRepos(ctx, in.ProjectID)

	case "repo.add":
		var in repoAddArgs
		if err := decodeArgs(args, &in); err != nil {
			return nil, err
		}
		if err := requireID("project_id", in.ProjectID); err != nil {
			return nil, err
		}
		if strings.TrimSpace(in.Path) == "" {
			return nil, fmt.Errorf("%w: path is required", store.ErrInvalid)
		}
		return s.store.AddRepo(ctx, actor, in.ProjectID, in.Path)

	case "repo.remove":
		var in repoRemoveArgs
		if err := decodeArgs(args, &in); err != nil {
			return nil, err
		}
		if err := requireID("id", in.ID); err != nil {
			return nil, err
		}
		if err := s.store.RemoveRepo(ctx, actor, in.ID); err != nil {
			return nil, err
		}
		return okResult{OK: true}, nil

	case "task.list":
		var in taskListArgs
		if err := decodeArgs(args, &in); err != nil {
			return nil, err
		}
		if err := requireID("project_id", in.ProjectID); err != nil {
			return nil, err
		}
		var repoID int64
		if in.RepoID != nil {
			if err := requireID("repo_id", *in.RepoID); err != nil {
				return nil, err
			}
			repoID = *in.RepoID
		}
		return s.store.ListTasks(ctx, store.TaskFilter{
			ProjectID: in.ProjectID,
			RepoID:    repoID,
			Status:    store.Status(in.Status),
			Priority:  store.Priority(in.Priority),
			Marking:   in.Marking,
		})

	case "task.create":
		var in taskCreateArgs
		if err := decodeArgs(args, &in); err != nil {
			return nil, err
		}
		if err := requireID("project_id", in.ProjectID); err != nil {
			return nil, err
		}
		if err := requireID("repo_id", in.RepoID); err != nil {
			return nil, err
		}
		if strings.TrimSpace(in.Title) == "" {
			return nil, fmt.Errorf("%w: title is required", store.ErrInvalid)
		}
		return s.store.CreateTask(ctx, actor, store.CreateTaskParams{
			ProjectID:   in.ProjectID,
			RepoID:      in.RepoID,
			Title:       in.Title,
			Description: in.Description,
			Status:      store.Status(in.Status),
			Priority:    store.Priority(in.Priority),
		})

	case "task.show":
		var in taskShowArgs
		if err := decodeArgs(args, &in); err != nil {
			return nil, err
		}
		if err := requireID("id", in.ID); err != nil {
			return nil, err
		}
		return s.store.Show(ctx, in.ID)

	case "task.update":
		var in taskUpdateArgs
		if err := decodeArgs(args, &in); err != nil {
			return nil, err
		}
		if err := requireID("id", in.ID); err != nil {
			return nil, err
		}
		patch := store.TaskPatch{
			Title:       in.Title,
			Description: in.Description,
			RepoID:      in.RepoID,
		}
		if in.Status != nil {
			status := store.Status(*in.Status)
			patch.Status = &status
		}
		if in.Priority != nil {
			priority := store.Priority(*in.Priority)
			patch.Priority = &priority
		}
		return s.store.UpdateTask(ctx, actor, in.ID, patch)

	case "task.mark":
		var in taskMarkArgs
		if err := decodeArgs(args, &in); err != nil {
			return nil, err
		}
		if err := requireID("id", in.ID); err != nil {
			return nil, err
		}
		if strings.TrimSpace(in.Marking) == "" {
			return nil, fmt.Errorf("%w: marking is required", store.ErrInvalid)
		}
		if err := requireBool("present", in.Present); err != nil {
			return nil, err
		}
		return s.store.SetMarking(ctx, actor, in.ID, in.Marking, *in.Present)

	case "task.depend":
		var in taskDependArgs
		if err := decodeArgs(args, &in); err != nil {
			return nil, err
		}
		if err := requireID("id", in.ID); err != nil {
			return nil, err
		}
		if err := requireID("blocker_id", in.BlockerID); err != nil {
			return nil, err
		}
		if err := requireBool("present", in.Present); err != nil {
			return nil, err
		}
		if err := s.store.SetDependency(ctx, actor, in.ID, in.BlockerID, *in.Present); err != nil {
			return nil, err
		}
		return okResult{OK: true}, nil

	case "task.comment":
		var in taskCommentArgs
		if err := decodeArgs(args, &in); err != nil {
			return nil, err
		}
		if err := requireID("id", in.ID); err != nil {
			return nil, err
		}
		if strings.TrimSpace(in.Body) == "" {
			return nil, fmt.Errorf("%w: body is required", store.ErrInvalid)
		}
		return s.store.Comment(ctx, actor, in.ID, in.Body)

	case "task.review":
		var in taskReviewArgs
		if err := decodeArgs(args, &in); err != nil {
			return nil, err
		}
		if err := requireID("id", in.ID); err != nil {
			return nil, err
		}
		if err := requireBool("approve", in.Approve); err != nil {
			return nil, err
		}
		decision := store.ReviewRequestChanges
		if *in.Approve {
			decision = store.ReviewApprove
		}
		return s.store.Review(ctx, actor, in.ID, decision, "")

	case "daemon.status":
		var in daemonStatusArgs
		if err := decodeArgs(args, &in); err != nil {
			return nil, err
		}
		settings, err := s.store.QueueSettings(ctx)
		if err != nil {
			return nil, err
		}
		status := daemonStatus{
			Running:        true,
			Paused:         settings.Paused,
			MaxConcurrency: settings.MaxConcurrency,
		}
		if s.status != nil {
			snapshot := s.status()
			status.ActiveParents = snapshot.ActiveParents
			status.WatcherError = snapshot.WatcherError
		}
		return status, nil

	case "daemon.update":
		var in daemonUpdateArgs
		if err := decodeArgs(args, &in); err != nil {
			return nil, err
		}
		return s.store.UpdateQueueSettings(ctx, actor, store.QueueSettingsPatch{
			Paused:         in.Paused,
			MaxConcurrency: in.MaxConcurrency,
		})
	}
	return nil, fmt.Errorf("%w: unknown command %q", store.ErrInvalid, command)
}

// Classify maps an execution error to an HTTP status and a stable error code.
func Classify(err error) (int, string) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return 404, "not_found"
	case errors.Is(err, store.ErrInvalid):
		return 400, "invalid"
	case errors.Is(err, store.ErrConflict):
		return 409, "conflict"
	default:
		return 500, "internal_error"
	}
}

func decodeArgs(args json.RawMessage, target any) error {
	trimmed := bytes.TrimSpace(args)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return fmt.Errorf("%w: args must be a JSON object", store.ErrInvalid)
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: %v", store.ErrInvalid, err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: args must contain a single JSON object", store.ErrInvalid)
	}
	return nil
}

func requireID(field string, id int64) error {
	if id <= 0 {
		return fmt.Errorf("%w: %s must be a positive ID", store.ErrInvalid, field)
	}
	return nil
}

func requireName(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: %s is required", store.ErrInvalid, field)
	}
	return nil
}

func requireBool(field string, value *bool) error {
	if value == nil {
		return fmt.Errorf("%w: %s is required", store.ErrInvalid, field)
	}
	return nil
}

type okResult struct {
	OK bool `json:"ok"`
}

type projectListArgs struct{}

type projectCreateArgs struct {
	Name string `json:"name"`
}

type projectRenameArgs struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type projectRemoveArgs struct {
	ID int64 `json:"id"`
}

type repoListArgs struct {
	ProjectID int64 `json:"project_id"`
}

type repoAddArgs struct {
	ProjectID int64  `json:"project_id"`
	Path      string `json:"path"`
}

type repoRemoveArgs struct {
	ID int64 `json:"id"`
}

type taskListArgs struct {
	ProjectID int64  `json:"project_id"`
	RepoID    *int64 `json:"repo_id"`
	Status    string `json:"status"`
	Priority  string `json:"priority"`
	Marking   string `json:"marking"`
}

type taskCreateArgs struct {
	ProjectID   int64  `json:"project_id"`
	RepoID      int64  `json:"repo_id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Status      string `json:"status"`
	Priority    string `json:"priority"`
}

type taskShowArgs struct {
	ID int64 `json:"id"`
}

type taskUpdateArgs struct {
	ID          int64   `json:"id"`
	Title       *string `json:"title"`
	Description *string `json:"description"`
	RepoID      *int64  `json:"repo_id"`
	Status      *string `json:"status"`
	Priority    *string `json:"priority"`
}

type taskMarkArgs struct {
	ID      int64  `json:"id"`
	Marking string `json:"marking"`
	Present *bool  `json:"present"`
}

type taskDependArgs struct {
	ID        int64 `json:"id"`
	BlockerID int64 `json:"blocker_id"`
	Present   *bool `json:"present"`
}

type taskCommentArgs struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
}

type taskReviewArgs struct {
	ID      int64 `json:"id"`
	Approve *bool `json:"approve"`
}

type daemonStatusArgs struct{}

type daemonUpdateArgs struct {
	Paused         *bool `json:"paused"`
	MaxConcurrency *int  `json:"max_concurrency"`
}

type daemonStatus struct {
	Running        bool   `json:"running"`
	Paused         bool   `json:"paused"`
	MaxConcurrency int    `json:"max_concurrency"`
	ActiveParents  *int   `json:"active_parents"`
	WatcherError   string `json:"watcher_error,omitempty"`
}
