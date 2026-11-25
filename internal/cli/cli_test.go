package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type recordedCall struct {
	ctx     context.Context
	command string
	actor   string
	args    map[string]any
}

type recordingExecutor struct {
	calls  []recordedCall
	result any
	err    error
}

func (r *recordingExecutor) execute(ctx context.Context, command, actor string, args map[string]any) (any, error) {
	r.calls = append(r.calls, recordedCall{ctx: ctx, command: command, actor: actor, args: args})
	return r.result, r.err
}

func runCLI(argv []string, defaultActor string, executor *recordingExecutor) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), argv, &stdout, &stderr, defaultActor, executor.execute)
	return code, stdout.String(), stderr.String()
}

func TestRunCommandWireContract(t *testing.T) {
	tests := []struct {
		name         string
		argv         []string
		defaultActor string
		actor        string
		command      string
		args         map[string]any
	}{
		{
			name:    "project list",
			argv:    []string{"project", "list"},
			command: "project.list",
			args:    map[string]any{},
		},
		{
			name:    "project create",
			argv:    []string{"project", "create", "Gilir"},
			command: "project.create",
			args:    map[string]any{"name": "Gilir"},
		},
		{
			name:    "project rename",
			argv:    []string{"project", "rename", "12", "New Name"},
			command: "project.rename",
			args:    map[string]any{"id": int64(12), "name": "New Name"},
		},
		{
			name:    "project remove",
			argv:    []string{"project", "remove", "3"},
			command: "project.remove",
			args:    map[string]any{"id": int64(3)},
		},
		{
			name:    "repo list",
			argv:    []string{"repo", "list", "--project", "4"},
			command: "repo.list",
			args:    map[string]any{"project_id": int64(4)},
		},
		{
			name:    "repo add",
			argv:    []string{"repo", "add", "/srv/repo", "--project", "4"},
			command: "repo.add",
			args:    map[string]any{"project_id": int64(4), "path": "/srv/repo"},
		},
		{
			name:    "repo remove",
			argv:    []string{"repo", "remove", "5"},
			command: "repo.remove",
			args:    map[string]any{"id": int64(5)},
		},
		{
			name:    "task list required filter",
			argv:    []string{"list", "--project", "4"},
			command: "task.list",
			args:    map[string]any{"project_id": int64(4)},
		},
		{
			name:    "task list all filters",
			argv:    []string{"list", "--project", "4", "--repo", "5", "--status", "todo", "--priority", "high", "--marking", "ready"},
			command: "task.list",
			args:    map[string]any{"project_id": int64(4), "repo_id": int64(5), "status": "todo", "priority": "high", "marking": "ready"},
		},
		{
			name:    "task create minimal",
			argv:    []string{"create", "Fix login", "--project", "4", "--repo", "5"},
			command: "task.create",
			args:    map[string]any{"project_id": int64(4), "repo_id": int64(5), "title": "Fix login"},
		},
		{
			name:    "task create all fields",
			argv:    []string{"create", "Fix login", "--project", "4", "--repo", "5", "--description", "Session expires early.", "--status", "todo", "--priority", "high"},
			command: "task.create",
			args:    map[string]any{"project_id": int64(4), "repo_id": int64(5), "title": "Fix login", "description": "Session expires early.", "status": "todo", "priority": "high"},
		},
		{
			name:    "task show",
			argv:    []string{"show", "6"},
			command: "task.show",
			args:    map[string]any{"id": int64(6)},
		},
		{
			name:    "task update no fields",
			argv:    []string{"update", "6"},
			command: "task.update",
			args:    map[string]any{"id": int64(6)},
		},
		{
			name:    "task update all fields",
			argv:    []string{"update", "6", "--title", "New title", "--description", "More detail.", "--repo", "7", "--status", "in-review", "--priority", "low"},
			command: "task.update",
			args:    map[string]any{"id": int64(6), "title": "New title", "description": "More detail.", "repo_id": int64(7), "status": "in-review", "priority": "low"},
		},
		{
			name:    "task mark",
			argv:    []string{"mark", "6", "ready"},
			command: "task.mark",
			args:    map[string]any{"id": int64(6), "marking": "ready", "present": true},
		},
		{
			name:    "task unmark",
			argv:    []string{"unmark", "6", "ready"},
			command: "task.mark",
			args:    map[string]any{"id": int64(6), "marking": "ready", "present": false},
		},
		{
			name:    "task depend",
			argv:    []string{"depend", "6", "2"},
			command: "task.depend",
			args:    map[string]any{"id": int64(6), "blocker_id": int64(2), "present": true},
		},
		{
			name:    "task undepend",
			argv:    []string{"undepend", "6", "2"},
			command: "task.depend",
			args:    map[string]any{"id": int64(6), "blocker_id": int64(2), "present": false},
		},
		{
			name:    "task comment preserves body",
			argv:    []string{"comment", "6", "First line.\nSecond line."},
			command: "task.comment",
			args:    map[string]any{"id": int64(6), "body": "First line.\nSecond line."},
		},
		{
			name:    "terminator keeps flag-like body",
			argv:    []string{"comment", "6", "--", "--json"},
			command: "task.comment",
			args:    map[string]any{"id": int64(6), "body": "--json"},
		},
		{
			name:    "task approve",
			argv:    []string{"approve", "6"},
			command: "task.review",
			args:    map[string]any{"id": int64(6), "approve": true},
		},
		{
			name:    "task request changes",
			argv:    []string{"request-changes", "6"},
			command: "task.review",
			args:    map[string]any{"id": int64(6), "approve": false},
		},
		{
			name:    "daemon run",
			argv:    []string{"daemon", "run"},
			command: "daemon.run",
			args:    map[string]any{},
		},
		{
			name:    "daemon status",
			argv:    []string{"daemon", "status"},
			command: "daemon.status",
			args:    map[string]any{},
		},
		{
			name:    "daemon pause",
			argv:    []string{"daemon", "pause"},
			command: "daemon.update",
			args:    map[string]any{"paused": true},
		},
		{
			name:    "daemon resume",
			argv:    []string{"daemon", "resume"},
			command: "daemon.update",
			args:    map[string]any{"paused": false},
		},
		{
			name:    "daemon concurrency",
			argv:    []string{"daemon", "concurrency", "8"},
			command: "daemon.update",
			args:    map[string]any{"max_concurrency": 8},
		},
		{
			name:    "actor override anywhere",
			argv:    []string{"--json", "--actor", "agent:override", "show", "6"},
			actor:   "agent:override",
			command: "task.show",
			args:    map[string]any{"id": int64(6)},
		},
		{
			name:         "default actor from environment",
			argv:         []string{"show", "6"},
			defaultActor: "agent:environment",
			actor:        "agent:environment",
			command:      "task.show",
			args:         map[string]any{"id": int64(6)},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executor := &recordingExecutor{}
			code, _, stderr := runCLI(test.argv, test.defaultActor, executor)

			if code != 0 {
				t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
			}
			if stderr != "" {
				t.Fatalf("stderr = %q, want empty", stderr)
			}
			if len(executor.calls) != 1 {
				t.Fatalf("execute calls = %d, want 1", len(executor.calls))
			}

			call := executor.calls[0]
			if call.command != test.command {
				t.Errorf("command = %q, want %q", call.command, test.command)
			}
			wantActor := test.actor
			if wantActor == "" {
				wantActor = "user"
			}
			if call.actor != wantActor {
				t.Errorf("actor = %q, want %q", call.actor, wantActor)
			}
			if !reflect.DeepEqual(call.args, test.args) {
				t.Errorf("args = %#v, want %#v", call.args, test.args)
			}
		})
	}
}

func TestRunRejectsMalformedInvocations(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want string
	}{
		{name: "missing command", want: "missing command"},
		{name: "unknown command", argv: []string{"frobnicate"}, want: "unknown command"},
		{name: "unknown group command", argv: []string{"project", "frobnicate"}, want: `unknown command "project frobnicate"`},
		{name: "missing group command", argv: []string{"project"}, want: "missing project command"},
		{name: "missing argument", argv: []string{"project", "create"}, want: "missing <name>"},
		{name: "extra argument", argv: []string{"project", "list", "extra"}, want: "unexpected argument"},
		{name: "skill extra argument", argv: []string{"skill", "extra"}, want: "unexpected argument"},
		{name: "unknown flag", argv: []string{"list", "--project", "1", "--bogus", "x"}, want: "unknown flag --bogus"},
		{name: "duplicate flag", argv: []string{"list", "--project", "1", "--project", "2"}, want: "duplicate flag --project"},
		{name: "invalid flag for command", argv: []string{"project", "list", "--status", "todo"}, want: "flag --status is not valid"},
		{name: "missing required flag", argv: []string{"create", "Title", "--project", "1"}, want: "missing required flag --repo"},
		{name: "invalid task id", argv: []string{"show", "abc"}, want: `invalid task id "abc"`},
		{name: "empty task id", argv: []string{"show", ""}, want: `invalid task id ""`},
		{name: "zero project id", argv: []string{"project", "remove", "0"}, want: `invalid project id "0"`},
		{name: "negative repo id", argv: []string{"repo", "remove", "-1"}, want: `invalid repo id "-1"`},
		{name: "invalid optional repo id", argv: []string{"update", "1", "--repo", "nope"}, want: `invalid repo id "nope"`},
		{name: "invalid blocker id", argv: []string{"depend", "1", "two"}, want: `invalid blocker id "two"`},
		{name: "invalid concurrency", argv: []string{"daemon", "concurrency", "many"}, want: `invalid concurrency "many"`},
		{name: "zero concurrency", argv: []string{"daemon", "concurrency", "0"}, want: `invalid concurrency "0"`},
		{name: "empty actor", argv: []string{"show", "1", "--actor="}, want: "non-empty value"},
		{name: "missing actor value", argv: []string{"show", "1", "--actor"}, want: "flag --actor requires a value"},
		{name: "missing flag value", argv: []string{"list", "--project"}, want: "flag --project requires a value"},
		{name: "duplicate json", argv: []string{"--json", "--json", "show", "1"}, want: "duplicate flag --json"},
		{name: "json with value", argv: []string{"--json=yes", "show", "1"}, want: "does not take a value"},
		{name: "terminator leaves extra argument", argv: []string{"show", "1", "--", "--bogus"}, want: "unexpected argument"},
		{name: "command flag after skill", argv: []string{"skill", "--project", "1"}, want: "flag --project is not valid"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executor := &recordingExecutor{}
			code, stdout, stderr := runCLI(test.argv, "", executor)

			if code != 2 {
				t.Fatalf("exit code = %d, want 2 (stderr %q)", code, stderr)
			}
			if stdout != "" {
				t.Fatalf("stdout = %q, want empty", stdout)
			}
			if !strings.Contains(stderr, test.want) {
				t.Fatalf("stderr = %q, want substring %q", stderr, test.want)
			}
			if len(executor.calls) != 0 {
				t.Fatalf("execute calls = %d, want 0", len(executor.calls))
			}
		})
	}
}

func TestRunJSONErrors(t *testing.T) {
	executor := &recordingExecutor{err: errors.New("store offline")}
	code, stdout, stderr := runCLI([]string{"show", "1", "--json"}, "", executor)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	var failure struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(stderr), &failure); err != nil {
		t.Fatalf("stderr is not JSON: %v (%q)", err, stderr)
	}
	if failure.Error != "store offline" {
		t.Fatalf("error = %q, want %q", failure.Error, "store offline")
	}

	executor = &recordingExecutor{}
	code, stdout, stderr = runCLI([]string{"frobnicate", "--json"}, "", executor)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if err := json.Unmarshal([]byte(stderr), &failure); err != nil {
		t.Fatalf("stderr is not JSON: %v (%q)", err, stderr)
	}
	if !strings.Contains(failure.Error, "unknown command") {
		t.Fatalf("error = %q, want unknown command", failure.Error)
	}
	if len(executor.calls) != 0 {
		t.Fatalf("execute calls = %d, want 0", len(executor.calls))
	}
}

func TestRunPlainCommandFailure(t *testing.T) {
	executor := &recordingExecutor{err: errors.New("store offline")}
	code, stdout, stderr := runCLI([]string{"show", "1"}, "", executor)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if stderr != "gilir: store offline\n" {
		t.Fatalf("stderr = %q, want %q", stderr, "gilir: store offline\n")
	}
}

func TestRunHumanOutput(t *testing.T) {
	tests := []struct {
		name   string
		result any
		want   string
	}{
		{name: "nil result", want: "ok\n"},
		{name: "string result", result: "already formatted\n", want: "already formatted\n"},
		{
			name:   "map with list",
			result: map[string]any{"name": "Demo", "id": int64(1), "tags": []string{"a", "b"}},
			want:   "id: 1\nname: Demo\ntags:\n  - a\n  - b\n",
		},
		{
			name:   "nested map with multiline text",
			result: map[string]any{"task": map[string]any{"title": "Fix", "description": "first\nsecond"}},
			want:   "task:\n  description:\n    first\n    second\n  title: Fix\n",
		},
		{
			name:   "list of records",
			result: []any{map[string]any{"id": int64(2), "title": "B"}, map[string]any{"id": int64(1), "title": "A"}},
			want:   "- id: 2\n  title: B\n- id: 1\n  title: A\n",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executor := &recordingExecutor{result: test.result}
			code, stdout, stderr := runCLI([]string{"show", "1"}, "", executor)

			if code != 0 {
				t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
			}
			if stderr != "" {
				t.Fatalf("stderr = %q, want empty", stderr)
			}
			if stdout != test.want {
				t.Fatalf("stdout = %q, want %q", stdout, test.want)
			}
		})
	}
}

func TestRunJSONOutputIsBareData(t *testing.T) {
	executor := &recordingExecutor{result: map[string]any{"name": "Demo", "id": int64(7)}}
	code, stdout, stderr := runCLI([]string{"--json", "show", "7"}, "", executor)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	want := "{\n  \"id\": 7,\n  \"name\": \"Demo\"\n}\n"
	if stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
}

func TestRunPropagatesContext(t *testing.T) {
	type contextKey string
	ctx := context.WithValue(context.Background(), contextKey("request"), "abc")
	executor := &recordingExecutor{}

	var stdout, stderr bytes.Buffer
	code := Run(ctx, []string{"show", "1"}, &stdout, &stderr, "", executor.execute)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	if len(executor.calls) != 1 {
		t.Fatalf("execute calls = %d, want 1", len(executor.calls))
	}
	if got := executor.calls[0].ctx.Value(contextKey("request")); got != "abc" {
		t.Fatalf("context value = %v, want abc", got)
	}
}

func TestRunHelp(t *testing.T) {
	tests := []struct {
		name     string
		argv     []string
		contains []string
	}{
		{
			name:     "general",
			argv:     []string{"--help"},
			contains: []string{"Usage:", "project create <name>", "daemon concurrency <positive-integer>", "--actor <actor>"},
		},
		{
			name:     "command",
			argv:     []string{"project", "create", "--help"},
			contains: []string{"gilir project create <name>", "Create a project.", "Global options:"},
		},
		{
			name:     "group",
			argv:     []string{"daemon", "--help"},
			contains: []string{"gilir daemon <command>", "concurrency <positive-integer>", "Run the daemon in the foreground."},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executor := &recordingExecutor{}
			code, stdout, stderr := runCLI(test.argv, "", executor)

			if code != 0 {
				t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
			}
			if stderr != "" {
				t.Fatalf("stderr = %q, want empty", stderr)
			}
			for _, want := range test.contains {
				if !strings.Contains(stdout, want) {
					t.Fatalf("stdout missing %q:\n%s", want, stdout)
				}
			}
			if len(executor.calls) != 0 {
				t.Fatalf("execute calls = %d, want 0", len(executor.calls))
			}
		})
	}
}

func TestRunJSONHelp(t *testing.T) {
	executor := &recordingExecutor{}
	code, stdout, stderr := runCLI([]string{"project", "create", "--json", "--help"}, "", executor)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	var payload struct {
		Help string `json:"help"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("stdout is not JSON: %v (%q)", err, stdout)
	}
	if !strings.Contains(payload.Help, "gilir project create <name>") {
		t.Fatalf("help = %q, want project create usage", payload.Help)
	}
	if len(executor.calls) != 0 {
		t.Fatalf("execute calls = %d, want 0", len(executor.calls))
	}
}

func TestRunSkill(t *testing.T) {
	executor := &recordingExecutor{}
	code, plain, stderr := runCLI([]string{"skill"}, "", executor)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if !strings.HasPrefix(plain, "---\nname: gilir\ndescription: ") {
		t.Fatalf("skill is missing frontmatter:\n%s", plain)
	}
	for _, want := range []string{
		"gilir show <id> --json",
		"gilir comment <id> <markdown> --actor agent:<openCode-session-id>",
		"gilir update <id> --status in-review --actor agent:<openCode-session-id>",
		"audit provenance",
		"Delegate freely",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("skill missing %q:\n%s", want, plain)
		}
	}
	if len(executor.calls) != 0 {
		t.Fatalf("execute calls = %d, want 0", len(executor.calls))
	}

	code, stdout, stderr := runCLI([]string{"skill", "--json"}, "", executor)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	var payload struct {
		Skill string `json:"skill"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("stdout is not JSON: %v (%q)", err, stdout)
	}
	if payload.Skill != plain {
		t.Fatalf("skill JSON and plain output differ")
	}
}
