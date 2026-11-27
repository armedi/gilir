package cli

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/armedi/gilir/internal/taskid"
)

type commandKind int

const (
	executeCommand commandKind = iota
	skillCommand
)

type command struct {
	path        string
	kind        commandKind
	wire        string
	usage       string
	summary     string
	positionals []string
	flags       []string
	required    []string
	build       func(positionals []string, flags map[string]string) (map[string]any, error)
}

var commandSpecs = []command{
	{
		path:    "project list",
		wire:    "project.list",
		usage:   "gilir project list",
		summary: "List projects.",
		build: func(_ []string, _ map[string]string) (map[string]any, error) {
			return map[string]any{}, nil
		},
	},
	{
		path:        "project create",
		wire:        "project.create",
		usage:       "gilir project create <name>",
		summary:     "Create a project.",
		positionals: []string{"<name>"},
		build: func(positionals []string, _ map[string]string) (map[string]any, error) {
			return map[string]any{"name": positionals[0]}, nil
		},
	},
	{
		path:        "project rename",
		wire:        "project.rename",
		usage:       "gilir project rename <id> <name>",
		summary:     "Rename a project.",
		positionals: []string{"<id>", "<name>"},
		build: func(positionals []string, _ map[string]string) (map[string]any, error) {
			id, err := parseID("project id", positionals[0])
			if err != nil {
				return nil, err
			}
			return map[string]any{"id": id, "name": positionals[1]}, nil
		},
	},
	{
		path:        "project remove",
		wire:        "project.remove",
		usage:       "gilir project remove <id>",
		summary:     "Remove a project.",
		positionals: []string{"<id>"},
		build: func(positionals []string, _ map[string]string) (map[string]any, error) {
			id, err := parseID("project id", positionals[0])
			if err != nil {
				return nil, err
			}
			return map[string]any{"id": id}, nil
		},
	},
	{
		path:    "repo list",
		wire:    "repo.list",
		usage:   "gilir repo list [--project <id>]",
		summary: "List repositories, optionally restricted to one project.",
		flags:   []string{"project"},
		build: func(_ []string, flags map[string]string) (map[string]any, error) {
			args := map[string]any{}
			if err := addIDFlag(args, flags, "project", "project_id", "project id"); err != nil {
				return nil, err
			}
			return args, nil
		},
	},
	{
		path:        "repo add",
		wire:        "repo.add",
		usage:       "gilir repo add <path> [--project <id>]",
		summary:     "Add a repository, creating a project from its basename when --project is omitted.",
		positionals: []string{"<path>"},
		flags:       []string{"project"},
		build: func(positionals []string, flags map[string]string) (map[string]any, error) {
			args := map[string]any{"path": positionals[0]}
			if err := addIDFlag(args, flags, "project", "project_id", "project id"); err != nil {
				return nil, err
			}
			return args, nil
		},
	},
	{
		path:        "repo link",
		wire:        "repo.link",
		usage:       "gilir repo link <repo-id> <project-id>",
		summary:     "Link a repository to another project.",
		positionals: []string{"<repo-id>", "<project-id>"},
		build: func(positionals []string, _ map[string]string) (map[string]any, error) {
			repoID, err := parseID("repo id", positionals[0])
			if err != nil {
				return nil, err
			}
			projectID, err := parseID("project id", positionals[1])
			if err != nil {
				return nil, err
			}
			return map[string]any{"repo_id": repoID, "project_id": projectID}, nil
		},
	},
	{
		path:        "repo unlink",
		wire:        "repo.unlink",
		usage:       "gilir repo unlink <repo-id> <project-id>",
		summary:     "Unlink a repository from a project.",
		positionals: []string{"<repo-id>", "<project-id>"},
		build: func(positionals []string, _ map[string]string) (map[string]any, error) {
			repoID, err := parseID("repo id", positionals[0])
			if err != nil {
				return nil, err
			}
			projectID, err := parseID("project id", positionals[1])
			if err != nil {
				return nil, err
			}
			return map[string]any{"repo_id": repoID, "project_id": projectID}, nil
		},
	},
	{
		path:        "repo remove",
		wire:        "repo.remove",
		usage:       "gilir repo remove <id>",
		summary:     "Remove a repository.",
		positionals: []string{"<id>"},
		build: func(positionals []string, _ map[string]string) (map[string]any, error) {
			id, err := parseID("repo id", positionals[0])
			if err != nil {
				return nil, err
			}
			return map[string]any{"id": id}, nil
		},
	},
	{
		path:    "list",
		wire:    "task.list",
		usage:   "gilir list [--project <id>] [--repo <id>] [--status <status>] [--priority <priority>] [--marking <marking>]",
		summary: "List tasks.",
		flags:   []string{"project", "repo", "status", "priority", "marking"},
		build: func(_ []string, flags map[string]string) (map[string]any, error) {
			args := map[string]any{}
			if err := addIDFlag(args, flags, "project", "project_id", "project id"); err != nil {
				return nil, err
			}
			if err := addIDFlag(args, flags, "repo", "repo_id", "repo id"); err != nil {
				return nil, err
			}
			addStringFlag(args, flags, "status", "status")
			addStringFlag(args, flags, "priority", "priority")
			addStringFlag(args, flags, "marking", "marking")
			return args, nil
		},
	},
	{
		path:        "create",
		wire:        "task.create",
		usage:       "gilir create <title> --repo <id> [--project <id>] [--description <markdown>] [--status <status>] [--priority <priority>]",
		summary:     "Create a task.",
		positionals: []string{"<title>"},
		flags:       []string{"project", "repo", "description", "status", "priority"},
		required:    []string{"repo"},
		build: func(positionals []string, flags map[string]string) (map[string]any, error) {
			repoID, err := parseID("repo id", flags["repo"])
			if err != nil {
				return nil, err
			}
			args := map[string]any{
				"repo_id": repoID,
				"title":   positionals[0],
			}
			if err := addIDFlag(args, flags, "project", "project_id", "project id"); err != nil {
				return nil, err
			}
			addStringFlag(args, flags, "description", "description")
			addStringFlag(args, flags, "status", "status")
			addStringFlag(args, flags, "priority", "priority")
			return args, nil
		},
	},
	{
		path:        "show",
		wire:        "task.show",
		usage:       "gilir show <task-id>",
		summary:     "Show a task by its GILIR- prefixed ID.",
		positionals: []string{"<task-id>"},
		build: func(positionals []string, _ map[string]string) (map[string]any, error) {
			id, err := parseTaskID(positionals[0])
			if err != nil {
				return nil, err
			}
			return map[string]any{"id": id}, nil
		},
	},
	{
		path:        "delete",
		wire:        "task.delete",
		usage:       "gilir delete <task-id>",
		summary:     "Delete a task and its terminal dispatch history.",
		positionals: []string{"<task-id>"},
		build: func(positionals []string, _ map[string]string) (map[string]any, error) {
			id, err := parseTaskID(positionals[0])
			if err != nil {
				return nil, err
			}
			return map[string]any{"id": id}, nil
		},
	},
	{
		path:        "update",
		wire:        "task.update",
		usage:       "gilir update <task-id> [--title <title>] [--description <markdown>] [--repo <id>] [--status <status>] [--priority <priority>] [--message <markdown>]",
		summary:     "Update task fields. Moving in-review back to todo requires --message.",
		positionals: []string{"<task-id>"},
		flags:       []string{"title", "description", "repo", "status", "priority", "message"},
		build: func(positionals []string, flags map[string]string) (map[string]any, error) {
			id, err := parseTaskID(positionals[0])
			if err != nil {
				return nil, err
			}
			args := map[string]any{"id": id}
			addStringFlag(args, flags, "title", "title")
			addStringFlag(args, flags, "description", "description")
			if err := addIDFlag(args, flags, "repo", "repo_id", "repo id"); err != nil {
				return nil, err
			}
			addStringFlag(args, flags, "status", "status")
			addStringFlag(args, flags, "priority", "priority")
			addStringFlag(args, flags, "message", "message")
			return args, nil
		},
	},
	{
		path:        "mark",
		wire:        "task.mark",
		usage:       "gilir mark <task-id> <marking>",
		summary:     "Add a marking to a task.",
		positionals: []string{"<task-id>", "<marking>"},
		build: func(positionals []string, _ map[string]string) (map[string]any, error) {
			return buildMark(positionals, true)
		},
	},
	{
		path:        "unmark",
		wire:        "task.mark",
		usage:       "gilir unmark <task-id> <marking>",
		summary:     "Remove a marking from a task.",
		positionals: []string{"<task-id>", "<marking>"},
		build: func(positionals []string, _ map[string]string) (map[string]any, error) {
			return buildMark(positionals, false)
		},
	},
	{
		path:        "depend",
		wire:        "task.depend",
		usage:       "gilir depend <task-id> <blocker-task-id>",
		summary:     "Record that a task is blocked by another task.",
		positionals: []string{"<task-id>", "<blocker-task-id>"},
		build: func(positionals []string, _ map[string]string) (map[string]any, error) {
			return buildDependency(positionals, true)
		},
	},
	{
		path:        "undepend",
		wire:        "task.depend",
		usage:       "gilir undepend <task-id> <blocker-task-id>",
		summary:     "Remove a task dependency.",
		positionals: []string{"<task-id>", "<blocker-task-id>"},
		build: func(positionals []string, _ map[string]string) (map[string]any, error) {
			return buildDependency(positionals, false)
		},
	},
	{
		path:        "comment",
		wire:        "task.comment",
		usage:       "gilir comment <task-id> <markdown>",
		summary:     "Add a comment to a task.",
		positionals: []string{"<task-id>", "<markdown>"},
		build: func(positionals []string, _ map[string]string) (map[string]any, error) {
			id, err := parseTaskID(positionals[0])
			if err != nil {
				return nil, err
			}
			return map[string]any{"id": id, "body": positionals[1]}, nil
		},
	},
	{
		path:        "edit-comment",
		wire:        "task.comment.edit",
		usage:       "gilir edit-comment <task-id> <comment-id> <markdown>",
		summary:     "Edit one of your task comments.",
		positionals: []string{"<task-id>", "<comment-id>", "<markdown>"},
		build: func(positionals []string, _ map[string]string) (map[string]any, error) {
			id, err := parseTaskID(positionals[0])
			if err != nil {
				return nil, err
			}
			commentID, err := parseID("comment id", positionals[1])
			if err != nil {
				return nil, err
			}
			return map[string]any{"id": id, "comment_id": commentID, "body": positionals[2]}, nil
		},
	},
	{
		path:        "remove-comment",
		wire:        "task.comment.remove",
		usage:       "gilir remove-comment <task-id> <comment-id>",
		summary:     "Remove one of your task comments.",
		positionals: []string{"<task-id>", "<comment-id>"},
		build: func(positionals []string, _ map[string]string) (map[string]any, error) {
			id, err := parseTaskID(positionals[0])
			if err != nil {
				return nil, err
			}
			commentID, err := parseID("comment id", positionals[1])
			if err != nil {
				return nil, err
			}
			return map[string]any{"id": id, "comment_id": commentID}, nil
		},
	},
	{
		path:        "approve",
		wire:        "task.review",
		usage:       "gilir approve <task-id> [--message <markdown>]",
		summary:     "Approve a task, optionally with a message.",
		positionals: []string{"<task-id>"},
		flags:       []string{"message"},
		build: func(positionals []string, flags map[string]string) (map[string]any, error) {
			return buildReview(positionals, flags, true)
		},
	},
	{
		path:        "request-changes",
		wire:        "task.review",
		usage:       "gilir request-changes <task-id> --message <markdown>",
		summary:     "Request changes on a task with a required message.",
		positionals: []string{"<task-id>"},
		flags:       []string{"message"},
		required:    []string{"message"},
		build: func(positionals []string, flags map[string]string) (map[string]any, error) {
			return buildReview(positionals, flags, false)
		},
	},
	{
		path:    "service start",
		wire:    "service.start",
		usage:   "gilir service start [--host <host>] [--port <port>]",
		summary: "Start the background service.",
		flags:   []string{"host", "port"},
		build: func(_ []string, flags map[string]string) (map[string]any, error) {
			return buildServiceListen(flags)
		},
	},
	{
		path:    "service stop",
		wire:    "service.stop",
		usage:   "gilir service stop",
		summary: "Stop the background service.",
		build: func(_ []string, _ map[string]string) (map[string]any, error) {
			return map[string]any{}, nil
		},
	},
	{
		path:    "service restart",
		wire:    "service.restart",
		usage:   "gilir service restart [--host <host>] [--port <port>]",
		summary: "Restart the background service.",
		flags:   []string{"host", "port"},
		build: func(_ []string, flags map[string]string) (map[string]any, error) {
			return buildServiceListen(flags)
		},
	},
	{
		path:    "service status",
		wire:    "service.status",
		usage:   "gilir service status",
		summary: "Show background service status.",
		build: func(_ []string, _ map[string]string) (map[string]any, error) {
			return map[string]any{}, nil
		},
	},
	{
		path:    "daemon pause",
		wire:    "daemon.update",
		usage:   "gilir daemon pause",
		summary: "Pause task dispatch.",
		build: func(_ []string, _ map[string]string) (map[string]any, error) {
			return map[string]any{"paused": true}, nil
		},
	},
	{
		path:    "daemon resume",
		wire:    "daemon.update",
		usage:   "gilir daemon resume",
		summary: "Resume task dispatch.",
		build: func(_ []string, _ map[string]string) (map[string]any, error) {
			return map[string]any{"paused": false}, nil
		},
	},
	{
		path:        "daemon concurrency",
		wire:        "daemon.update",
		usage:       "gilir daemon concurrency <positive-integer>",
		summary:     "Set the maximum number of concurrent tasks.",
		positionals: []string{"<positive-integer>"},
		build: func(positionals []string, _ map[string]string) (map[string]any, error) {
			limit, err := parsePositiveInt("concurrency", positionals[0])
			if err != nil {
				return nil, err
			}
			return map[string]any{"max_concurrency": limit}, nil
		},
	},
	{
		path:    "skill",
		kind:    skillCommand,
		usage:   "gilir skill",
		summary: "Print the embedded agent skill.",
	},
}

var commandByPath = func() map[string]*command {
	commands := make(map[string]*command, len(commandSpecs))
	for i := range commandSpecs {
		commands[commandSpecs[i].path] = &commandSpecs[i]
	}
	return commands
}()

var commandGroupNames = map[string]bool{
	"project": true,
	"repo":    true,
	"daemon":  true,
	"service": true,
}

var knownCommandFlags = func() map[string]bool {
	flags := make(map[string]bool)
	for _, spec := range commandSpecs {
		for _, flag := range spec.flags {
			flags[flag] = true
		}
	}
	return flags
}()

func (c *command) validate(positionals []string, inv *arguments) error {
	if len(positionals) < len(c.positionals) {
		return fmt.Errorf("%s: missing %s", c.path, c.positionals[len(positionals)])
	}
	if len(positionals) > len(c.positionals) {
		return fmt.Errorf("%s: unexpected argument %q", c.path, positionals[len(c.positionals)])
	}
	allowed := make(map[string]bool, len(c.flags))
	for _, flag := range c.flags {
		allowed[flag] = true
	}
	for _, flag := range inv.flagOrder {
		if !allowed[flag] {
			return fmt.Errorf("%s: flag --%s is not valid", c.path, flag)
		}
	}
	for _, flag := range c.required {
		if _, ok := inv.flags[flag]; !ok {
			return fmt.Errorf("%s: missing required flag --%s", c.path, flag)
		}
	}
	return nil
}

func buildMark(positionals []string, present bool) (map[string]any, error) {
	id, err := parseTaskID(positionals[0])
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "marking": positionals[1], "present": present}, nil
}

func buildDependency(positionals []string, present bool) (map[string]any, error) {
	id, err := parseTaskID(positionals[0])
	if err != nil {
		return nil, err
	}
	blockerID, err := parseTaskID(positionals[1])
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "blocker_id": blockerID, "present": present}, nil
}

func buildReview(positionals []string, flags map[string]string, approve bool) (map[string]any, error) {
	id, err := parseTaskID(positionals[0])
	if err != nil {
		return nil, err
	}
	message := strings.TrimSpace(flags["message"])
	if !approve && message == "" {
		return nil, fmt.Errorf("a message is required when requesting changes")
	}
	args := map[string]any{"id": id, "approve": approve}
	if message != "" {
		args["message"] = message
	}
	return args, nil
}

func addStringFlag(args map[string]any, flags map[string]string, flag, key string) {
	if value, ok := flags[flag]; ok {
		args[key] = value
	}
}

func buildServiceListen(flags map[string]string) (map[string]any, error) {
	args := map[string]any{}
	if err := addHostFlag(args, flags); err != nil {
		return nil, err
	}
	if err := addPortFlag(args, flags); err != nil {
		return nil, err
	}
	return args, nil
}

func addHostFlag(args map[string]any, flags map[string]string) error {
	value, ok := flags["host"]
	if !ok {
		return nil
	}
	host := normalizeHost(value)
	if !validHostValue(host) {
		return fmt.Errorf("invalid host %q: must be an IP address or localhost", value)
	}
	args["host"] = host
	return nil
}

func normalizeHost(host string) string {
	host = strings.TrimSpace(host)
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	return host
}

func validHostValue(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	return net.ParseIP(host) != nil
}

func addPortFlag(args map[string]any, flags map[string]string) error {
	value, ok := flags["port"]
	if !ok {
		return nil
	}
	if !isPositiveInteger(value) {
		return fmt.Errorf("invalid port %q: must be a number from 1 to 65535", value)
	}
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("invalid port %q: must be a number from 1 to 65535", value)
	}
	args["port"] = port
	return nil
}

func addIDFlag(args map[string]any, flags map[string]string, flag, key, kind string) error {
	value, ok := flags[flag]
	if !ok {
		return nil
	}
	id, err := parseID(kind, value)
	if err != nil {
		return err
	}
	args[key] = id
	return nil
}

// parseTaskID validates a task ID positional and returns its canonical
// spelling, for example "GILIR-42". Bare numbers are rejected so every task
// reference names the tool that owns it.
func parseTaskID(value string) (string, error) {
	id, err := taskid.Normalize(value)
	if err != nil {
		return "", err
	}
	return id, nil
}

func parseID(kind, value string) (int64, error) {
	if !isPositiveInteger(value) {
		return 0, fmt.Errorf("invalid %s %q: must be a positive integer", kind, value)
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid %s %q: must be a positive integer", kind, value)
	}
	return id, nil
}

func parsePositiveInt(kind, value string) (int, error) {
	if !isPositiveInteger(value) {
		return 0, fmt.Errorf("invalid %s %q: must be a positive integer", kind, value)
	}
	number, err := strconv.Atoi(value)
	if err != nil || number <= 0 {
		return 0, fmt.Errorf("invalid %s %q: must be a positive integer", kind, value)
	}
	return number, nil
}

func isPositiveInteger(value string) bool {
	if value == "" {
		return false
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

func resolveCommand(positionals []string) (*command, []string, error) {
	if len(positionals) == 0 {
		return nil, nil, fmt.Errorf("missing command")
	}
	first := positionals[0]
	if commandGroupNames[first] {
		if len(positionals) < 2 {
			return nil, nil, fmt.Errorf("missing %s command", first)
		}
		path := first + " " + positionals[1]
		spec, ok := commandByPath[path]
		if !ok {
			return nil, nil, fmt.Errorf("unknown command %q", path)
		}
		return spec, positionals[2:], nil
	}
	spec, ok := commandByPath[first]
	if !ok {
		return nil, nil, fmt.Errorf("unknown command %q", first)
	}
	return spec, positionals[1:], nil
}

func containsFlag(flags []string, flag string) bool {
	for _, candidate := range flags {
		if candidate == flag {
			return true
		}
	}
	return false
}
