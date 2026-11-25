package cli

import (
	"fmt"
	"strconv"
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
		usage:       "gilir show <id>",
		summary:     "Show a task.",
		positionals: []string{"<id>"},
		build: func(positionals []string, _ map[string]string) (map[string]any, error) {
			id, err := parseID("task id", positionals[0])
			if err != nil {
				return nil, err
			}
			return map[string]any{"id": id}, nil
		},
	},
	{
		path:        "update",
		wire:        "task.update",
		usage:       "gilir update <id> [--title <title>] [--description <markdown>] [--repo <id>] [--status <status>] [--priority <priority>]",
		summary:     "Update task fields.",
		positionals: []string{"<id>"},
		flags:       []string{"title", "description", "repo", "status", "priority"},
		build: func(positionals []string, flags map[string]string) (map[string]any, error) {
			id, err := parseID("task id", positionals[0])
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
			return args, nil
		},
	},
	{
		path:        "mark",
		wire:        "task.mark",
		usage:       "gilir mark <id> <marking>",
		summary:     "Add a marking to a task.",
		positionals: []string{"<id>", "<marking>"},
		build: func(positionals []string, _ map[string]string) (map[string]any, error) {
			return buildMark(positionals, true)
		},
	},
	{
		path:        "unmark",
		wire:        "task.mark",
		usage:       "gilir unmark <id> <marking>",
		summary:     "Remove a marking from a task.",
		positionals: []string{"<id>", "<marking>"},
		build: func(positionals []string, _ map[string]string) (map[string]any, error) {
			return buildMark(positionals, false)
		},
	},
	{
		path:        "depend",
		wire:        "task.depend",
		usage:       "gilir depend <id> <blocker-id>",
		summary:     "Record that a task is blocked by another task.",
		positionals: []string{"<id>", "<blocker-id>"},
		build: func(positionals []string, _ map[string]string) (map[string]any, error) {
			return buildDependency(positionals, true)
		},
	},
	{
		path:        "undepend",
		wire:        "task.depend",
		usage:       "gilir undepend <id> <blocker-id>",
		summary:     "Remove a task dependency.",
		positionals: []string{"<id>", "<blocker-id>"},
		build: func(positionals []string, _ map[string]string) (map[string]any, error) {
			return buildDependency(positionals, false)
		},
	},
	{
		path:        "comment",
		wire:        "task.comment",
		usage:       "gilir comment <id> <markdown>",
		summary:     "Add a comment to a task.",
		positionals: []string{"<id>", "<markdown>"},
		build: func(positionals []string, _ map[string]string) (map[string]any, error) {
			id, err := parseID("task id", positionals[0])
			if err != nil {
				return nil, err
			}
			return map[string]any{"id": id, "body": positionals[1]}, nil
		},
	},
	{
		path:        "approve",
		wire:        "task.review",
		usage:       "gilir approve <id>",
		summary:     "Approve a task.",
		positionals: []string{"<id>"},
		build: func(positionals []string, _ map[string]string) (map[string]any, error) {
			return buildReview(positionals, true)
		},
	},
	{
		path:        "request-changes",
		wire:        "task.review",
		usage:       "gilir request-changes <id>",
		summary:     "Request changes on a task.",
		positionals: []string{"<id>"},
		build: func(positionals []string, _ map[string]string) (map[string]any, error) {
			return buildReview(positionals, false)
		},
	},
	{
		path:    "daemon run",
		wire:    "daemon.run",
		usage:   "gilir daemon run",
		summary: "Run the daemon in the foreground.",
		build: func(_ []string, _ map[string]string) (map[string]any, error) {
			return map[string]any{}, nil
		},
	},
	{
		path:    "daemon status",
		wire:    "daemon.status",
		usage:   "gilir daemon status",
		summary: "Show daemon status.",
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
	id, err := parseID("task id", positionals[0])
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "marking": positionals[1], "present": present}, nil
}

func buildDependency(positionals []string, present bool) (map[string]any, error) {
	id, err := parseID("task id", positionals[0])
	if err != nil {
		return nil, err
	}
	blockerID, err := parseID("blocker id", positionals[1])
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "blocker_id": blockerID, "present": present}, nil
}

func buildReview(positionals []string, approve bool) (map[string]any, error) {
	id, err := parseID("task id", positionals[0])
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "approve": approve}, nil
}

func addStringFlag(args map[string]any, flags map[string]string, flag, key string) {
	if value, ok := flags[flag]; ok {
		args[key] = value
	}
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
