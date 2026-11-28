package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"runtime/debug"
	"strings"
	"testing"
)

type recordedCall struct {
	command string
	actor   string
	args    map[string]any
}

type recordingExecutor struct {
	calls []recordedCall
}

func (r *recordingExecutor) execute(_ context.Context, command, actor string, args map[string]any) (any, error) {
	r.calls = append(r.calls, recordedCall{command: command, actor: actor, args: args})
	return nil, nil
}

func runCLI(argv []string, defaultActor string, executor *recordingExecutor) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), argv, &stdout, &stderr, defaultActor, executor.execute)
	return code, stdout.String(), stderr.String()
}

// taskDetailFixture is a TaskDetail as the service returns it, with the keys
// deliberately inserted out of order.
func taskDetailFixture() map[string]any {
	return map[string]any{
		"activity": []any{
			map[string]any{"id": 2, "type": "task.commented", "actor": "agent:ses_1", "data": map[string]any{"message": "Progress"}},
		},
		"sessions": []any{
			map[string]any{"id": "ses_gilir_1", "state": "linked", "attempt_id": "abc", "worktree": "/work/gilir-1", "url": "http://127.0.0.1:4096/session/x"},
		},
		"blockers": []any{"GILIR-1"},
		"task": map[string]any{
			"id": "GILIR-2", "title": "Ship", "status": "in-progress", "priority": "high",
			"markings": []any{"blocked"}, "worktree": "/work/gilir-2", "repo_id": 1, "project_id": 1,
		},
	}
}

type detailExecutor struct{ detail map[string]any }

func (d detailExecutor) execute(_ context.Context, command, actor string, args map[string]any) (any, error) {
	return d.detail, nil
}

func TestRunShowPrintsSectionsInReadingOrder(t *testing.T) {
	detail := taskDetailFixture()
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"show", "GILIR-2"}, &stdout, &stderr, "", detailExecutor{detail}.execute)
	if code != 0 {
		t.Fatalf("exit code = %d (stderr %q)", code, stderr.String())
	}
	out := stdout.String()
	order := []string{"task:", "blockers:", "sessions:", "activity:"}
	last := -1
	for _, section := range order {
		index := strings.Index(out, section)
		if index <= last {
			t.Fatalf("show output is out of order at %q:\n%s", section, out)
		}
		last = index
	}
	for _, want := range []string{"id: GILIR-2", "worktree: /work/gilir-2", "- GILIR-1", "sessions:\n  - attempt_id", "ses_gilir_1", "task.commented"} {
		if !strings.Contains(out, want) {
			t.Fatalf("show output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "error") {
		t.Fatalf("show output reports an error:\n%s", out)
	}

	var jsonOut, jsonErr bytes.Buffer
	code = Run(context.Background(), []string{"show", "GILIR-2", "--json"}, &jsonOut, &jsonErr, "", detailExecutor{detail}.execute)
	if code != 0 {
		t.Fatalf("json exit code = %d (stderr %q)", code, jsonErr.String())
	}
	var decoded map[string]any
	if err := json.Unmarshal(jsonOut.Bytes(), &decoded); err != nil {
		t.Fatalf("--json output is not JSON: %v", err)
	}
	if _, ok := decoded["task"]; !ok {
		t.Fatalf("--json output lost the task section: %s", jsonOut.String())
	}
	if !strings.HasPrefix(strings.TrimSpace(jsonOut.String()), "{") {
		t.Fatalf("--json output is not a single object: %s", jsonOut.String())
	}
}

func TestRunFlagPlacementAndActorPrecedence(t *testing.T) {
	tests := []struct {
		name         string
		argv         []string
		defaultActor string
		wantActor    string
		wantCommand  string
		wantArgs     map[string]any
		wantJSON     bool
	}{
		{
			name:        "global flags before command",
			argv:        []string{"--actor", "agent:before", "--json", "show", "GILIR-7"},
			wantActor:   "agent:before",
			wantCommand: "task.show",
			wantArgs:    map[string]any{"id": "GILIR-7"},
			wantJSON:    true,
		},
		{
			name:        "global flags after positionals",
			argv:        []string{"show", "GILIR-7", "--json", "--actor=agent:after"},
			wantActor:   "agent:after",
			wantCommand: "task.show",
			wantArgs:    map[string]any{"id": "GILIR-7"},
			wantJSON:    true,
		},
		{
			name:        "empty default actor falls back",
			argv:        []string{"show", "GILIR-7"},
			wantActor:   "user",
			wantCommand: "task.show",
			wantArgs:    map[string]any{"id": "GILIR-7"},
		},
		{
			name:         "default actor from main",
			argv:         []string{"show", "GILIR-7"},
			defaultActor: "agent:environment",
			wantActor:    "agent:environment",
			wantCommand:  "task.show",
			wantArgs:     map[string]any{"id": "GILIR-7"},
		},
		{
			name:         "explicit actor overrides default",
			argv:         []string{"show", "GILIR-7", "--actor", "agent:explicit"},
			defaultActor: "agent:environment",
			wantActor:    "agent:explicit",
			wantCommand:  "task.show",
			wantArgs:     map[string]any{"id": "GILIR-7"},
		},
		{
			name:        "terminator protects flag-like body",
			argv:        []string{"comment", "GILIR-7", "--", "--json"},
			wantActor:   "user",
			wantCommand: "task.comment",
			wantArgs:    map[string]any{"id": "GILIR-7", "message": "--json"},
		},
		{
			name:        "short version reaches a command as a value",
			argv:        []string{"comment", "GILIR-7", "-v"},
			wantActor:   "user",
			wantCommand: "task.comment",
			wantArgs:    map[string]any{"id": "GILIR-7", "message": "-v"},
		},
		{
			name:        "short version can be a task title",
			argv:        []string{"create", "-v", "--repo", "1"},
			wantActor:   "user",
			wantCommand: "task.create",
			wantArgs:    map[string]any{"title": "-v", "repo_id": int64(1)},
		},
		{
			name:        "edit comment",
			argv:        []string{"edit-comment", "GILIR-7", "9", "new body"},
			wantActor:   "user",
			wantCommand: "task.comment.edit",
			wantArgs:    map[string]any{"id": "GILIR-7", "comment_id": int64(9), "message": "new body"},
		},
		{
			name:        "remove comment",
			argv:        []string{"remove-comment", "GILIR-7", "9"},
			wantActor:   "user",
			wantCommand: "task.comment.remove",
			wantArgs:    map[string]any{"id": "GILIR-7", "comment_id": int64(9)},
		},
		{
			name:        "delete task",
			argv:        []string{"delete", "GILIR-7"},
			wantActor:   "user",
			wantCommand: "task.delete",
			wantArgs:    map[string]any{"id": "GILIR-7"},
		},
		{
			name:        "approve without message",
			argv:        []string{"approve", "GILIR-7"},
			wantActor:   "user",
			wantCommand: "task.review",
			wantArgs:    map[string]any{"id": "GILIR-7", "approve": true},
		},
		{
			name:        "approve carries optional message",
			argv:        []string{"approve", "GILIR-7", "--message", "looks good"},
			wantActor:   "user",
			wantCommand: "task.review",
			wantArgs:    map[string]any{"id": "GILIR-7", "approve": true, "message": "looks good"},
		},
		{
			name:        "request changes carries message flag",
			argv:        []string{"request-changes", "GILIR-7", "--message", "add tests"},
			wantActor:   "user",
			wantCommand: "task.review",
			wantArgs:    map[string]any{"id": "GILIR-7", "approve": false, "message": "add tests"},
		},
		{
			name:        "block carries required message",
			argv:        []string{"block", "GILIR-7", "--message", "Waiting on **ledger**"},
			wantActor:   "user",
			wantCommand: "task.block",
			wantArgs:    map[string]any{"id": "GILIR-7", "message": "Waiting on **ledger**"},
		},
		{
			name:        "unblock without message",
			argv:        []string{"unblock", "GILIR-7"},
			wantActor:   "user",
			wantCommand: "task.unblock",
			wantArgs:    map[string]any{"id": "GILIR-7"},
		},
		{
			name:        "unblock with message",
			argv:        []string{"unblock", "GILIR-7", "--message", "Migration landed"},
			wantActor:   "user",
			wantCommand: "task.unblock",
			wantArgs:    map[string]any{"id": "GILIR-7", "message": "Migration landed"},
		},
		{
			name:        "serve without flags",
			argv:        []string{"serve"},
			wantActor:   "user",
			wantCommand: "serve",
			wantArgs:    map[string]any{},
		},
		{
			name:        "serve with host port and db",
			argv:        []string{"serve", "--host", "0.0.0.0", "--port", "9000", "--db", "/tmp/dev.sqlite"},
			wantActor:   "user",
			wantCommand: "serve",
			wantArgs:    map[string]any{"host": "0.0.0.0", "port": 9000, "db": "/tmp/dev.sqlite"},
		},
		{
			name:        "service start without flags",
			argv:        []string{"service", "start"},
			wantActor:   "user",
			wantCommand: "service.start",
			wantArgs:    map[string]any{},
		},
		{
			name:        "service start with host and port",
			argv:        []string{"service", "start", "--host", "0.0.0.0", "--port", "9000"},
			wantActor:   "user",
			wantCommand: "service.start",
			wantArgs:    map[string]any{"host": "0.0.0.0", "port": 9000},
		},
		{
			name:        "service start with inline flags",
			argv:        []string{"service", "start", "--host=127.0.0.1", "--port=9000"},
			wantActor:   "user",
			wantCommand: "service.start",
			wantArgs:    map[string]any{"host": "127.0.0.1", "port": 9000},
		},
		{
			name:        "service start with db",
			argv:        []string{"service", "start", "--db", "/tmp/dev.sqlite"},
			wantActor:   "user",
			wantCommand: "service.start",
			wantArgs:    map[string]any{"db": "/tmp/dev.sqlite"},
		},
		{
			name:        "service restart with port",
			argv:        []string{"service", "restart", "--port", "9000"},
			wantActor:   "user",
			wantCommand: "service.restart",
			wantArgs:    map[string]any{"port": 9000},
		},
		{
			name:        "service restart with db",
			argv:        []string{"service", "restart", "--db", "/tmp/dev.sqlite"},
			wantActor:   "user",
			wantCommand: "service.restart",
			wantArgs:    map[string]any{"db": "/tmp/dev.sqlite"},
		},
		{
			name:        "service stop",
			argv:        []string{"service", "stop"},
			wantActor:   "user",
			wantCommand: "service.stop",
			wantArgs:    map[string]any{},
		},
		{
			name:        "service status",
			argv:        []string{"service", "status"},
			wantActor:   "user",
			wantCommand: "service.status",
			wantArgs:    map[string]any{},
		},
		{
			name:        "service pause is host-local",
			argv:        []string{"service", "pause"},
			wantActor:   "user",
			wantCommand: "service.pause",
			wantArgs:    map[string]any{},
		},
		{
			name:        "service resume is host-local",
			argv:        []string{"service", "resume"},
			wantActor:   "user",
			wantCommand: "service.resume",
			wantArgs:    map[string]any{},
		},
		{
			name:        "service concurrency is host-local",
			argv:        []string{"service", "concurrency", "3"},
			wantActor:   "user",
			wantCommand: "service.concurrency",
			wantArgs:    map[string]any{"max_concurrency": 3},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executor := &recordingExecutor{}
			code, stdout, stderr := runCLI(test.argv, test.defaultActor, executor)

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
			if call.command != test.wantCommand {
				t.Errorf("command = %q, want %q", call.command, test.wantCommand)
			}
			if call.actor != test.wantActor {
				t.Errorf("actor = %q, want %q", call.actor, test.wantActor)
			}
			if !reflect.DeepEqual(call.args, test.wantArgs) {
				t.Errorf("args = %#v, want %#v", call.args, test.wantArgs)
			}
			if gotJSON := json.Valid([]byte(strings.TrimSpace(stdout))); gotJSON != test.wantJSON {
				t.Errorf("JSON output = %t, want %t (stdout %q)", gotJSON, test.wantJSON, stdout)
			}
		})
	}
}

func TestRunRepoAndTaskProjectOptionality(t *testing.T) {
	tests := []struct {
		name        string
		argv        []string
		wantCommand string
		wantArgs    map[string]any
	}{
		{
			name:        "repo list global",
			argv:        []string{"repo", "list"},
			wantCommand: "repo.list",
			wantArgs:    map[string]any{},
		},
		{
			name:        "repo list scoped",
			argv:        []string{"repo", "list", "--project", "3"},
			wantCommand: "repo.list",
			wantArgs:    map[string]any{"project_id": int64(3)},
		},
		{
			name:        "repo add auto project",
			argv:        []string{"repo", "add", "/src/app"},
			wantCommand: "repo.add",
			wantArgs:    map[string]any{"path": "/src/app"},
		},
		{
			name:        "repo link",
			argv:        []string{"repo", "link", "2", "3"},
			wantCommand: "repo.link",
			wantArgs:    map[string]any{"repo_id": int64(2), "project_id": int64(3)},
		},
		{
			name:        "repo unlink",
			argv:        []string{"repo", "unlink", "2", "3"},
			wantCommand: "repo.unlink",
			wantArgs:    map[string]any{"repo_id": int64(2), "project_id": int64(3)},
		},
		{
			name:        "list global",
			argv:        []string{"list"},
			wantCommand: "task.list",
			wantArgs:    map[string]any{},
		},
		{
			name:        "list scoped with filters",
			argv:        []string{"list", "--project", "3", "--repo", "2", "--status", "todo", "--priority", "high", "--marking", "error"},
			wantCommand: "task.list",
			wantArgs: map[string]any{
				"project_id": int64(3),
				"repo_id":    int64(2),
				"status":     "todo",
				"priority":   "high",
				"marking":    "error",
			},
		},
		{
			name:        "create infers project",
			argv:        []string{"create", "Fix rounding", "--repo", "2"},
			wantCommand: "task.create",
			wantArgs:    map[string]any{"repo_id": int64(2), "title": "Fix rounding"},
		},
		{
			name:        "create with project and fields",
			argv:        []string{"create", "Fix rounding", "--repo", "2", "--project", "3", "--body", "body", "--status", "todo", "--priority", "high"},
			wantCommand: "task.create",
			wantArgs: map[string]any{
				"repo_id":    int64(2),
				"project_id": int64(3),
				"title":      "Fix rounding",
				"body":       "body",
				"status":     "todo",
				"priority":   "high",
			},
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
			if len(executor.calls) != 1 {
				t.Fatalf("execute calls = %d, want 1", len(executor.calls))
			}
			call := executor.calls[0]
			if call.command != test.wantCommand {
				t.Errorf("command = %q, want %q", call.command, test.wantCommand)
			}
			if !reflect.DeepEqual(call.args, test.wantArgs) {
				t.Errorf("args = %#v, want %#v", call.args, test.wantArgs)
			}
			if strings.TrimSpace(stdout) != "ok" {
				t.Errorf("stdout = %q, want ok", stdout)
			}
		})
	}
}

func TestRunRejectsLinkAndOptionalIDArguments(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want string
	}{
		{name: "repo link missing project", argv: []string{"repo", "link", "2"}, want: "missing <project-id>"},
		{name: "repo link invalid repo", argv: []string{"repo", "link", "nope", "3"}, want: "invalid repo id"},
		{name: "repo link invalid project", argv: []string{"repo", "link", "2", "nope"}, want: "invalid project id"},
		{name: "repo unlink zero repo", argv: []string{"repo", "unlink", "0", "3"}, want: "invalid repo id"},
		{name: "repo unlink invalid project", argv: []string{"repo", "unlink", "2", "-1"}, want: "invalid project id"},
		{name: "repo list invalid project", argv: []string{"repo", "list", "--project", "0"}, want: "invalid project id"},
		{name: "list invalid project", argv: []string{"list", "--project", "nope"}, want: "invalid project id"},
		{name: "create invalid project", argv: []string{"create", "Title", "--repo", "1", "--project", "0"}, want: "invalid project id"},
		{name: "repo add unexpected flag", argv: []string{"repo", "add", "/src", "--status", "todo"}, want: "flag --status is not valid"},
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

func TestCommandFlagsHaveHelpMetadata(t *testing.T) {
	for _, spec := range commandSpecs {
		for _, flag := range spec.flags {
			if flagDescriptions[flag] == "" {
				t.Errorf("%s: flag --%s has no description", spec.path, flag)
			}
			if flagPlaceholders[flag] == "" {
				t.Errorf("%s: flag --%s has no placeholder", spec.path, flag)
			}
		}
		for _, flag := range spec.required {
			if !containsFlag(spec.flags, flag) {
				t.Errorf("%s: required flag --%s is not in its flag list", spec.path, flag)
			}
		}
	}
}

func TestRunRejectsRequiredAndInvalidArguments(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want string
	}{
		{name: "missing required flag", argv: []string{"create", "Title", "--project", "1"}, want: "missing required flag --repo"},
		{name: "show invalid task id", argv: []string{"show", "nope"}, want: "invalid task id"},
		{name: "show bare numeric task id", argv: []string{"show", "7"}, want: "invalid task id"},
		{name: "delete invalid task id", argv: []string{"delete", "nope"}, want: "invalid task id"},
		{name: "delete lowercase prefix", argv: []string{"delete", "gilir-"}, want: "invalid task id"},
		{name: "invalid optional repo id", argv: []string{"list", "--project", "1", "--repo", "nope"}, want: "invalid repo id"},
		{name: "invalid concurrency", argv: []string{"service", "concurrency", "0"}, want: "invalid concurrency"},
		{name: "invalid port", argv: []string{"service", "start", "--port", "0"}, want: "invalid port"},
		{name: "out of range port", argv: []string{"service", "start", "--port", "65536"}, want: "invalid port"},
		{name: "non-numeric port", argv: []string{"service", "start", "--port", "nope"}, want: "invalid port"},
		{name: "port on status", argv: []string{"service", "status", "--port", "9000"}, want: "flag --port is not valid"},
		{name: "invalid host", argv: []string{"service", "start", "--host", "example.com"}, want: "invalid host"},
		{name: "blank host", argv: []string{"service", "restart", "--host", "  "}, want: "invalid host"},
		{name: "daemon group removed", argv: []string{"daemon", "status"}, want: "unknown command"},
		{name: "daemon run removed", argv: []string{"daemon", "run"}, want: "unknown command"},
		{name: "daemon pause removed", argv: []string{"daemon", "pause"}, want: "unknown command"},
		{name: "blank db on serve", argv: []string{"serve", "--db", "   "}, want: "flag --db requires a non-empty value"},
		{name: "blank db on service start", argv: []string{"service", "start", "--db", "   "}, want: "flag --db requires a non-empty value"},
		{name: "blank db on service restart", argv: []string{"service", "restart", "--db", ""}, want: "flag --db requires a non-empty value"},
		{name: "db on list", argv: []string{"list", "--db", "/tmp/x.sqlite"}, want: "flag --db is not valid"},
		{name: "db on service stop", argv: []string{"service", "stop", "--db", "/tmp/x.sqlite"}, want: "flag --db is not valid"},
		{name: "db on service status", argv: []string{"service", "status", "--db", "/tmp/x.sqlite"}, want: "flag --db is not valid"},
		{name: "db on service pause", argv: []string{"service", "pause", "--db", "/tmp/x.sqlite"}, want: "flag --db is not valid"},
		{name: "serve rejects json", argv: []string{"serve", "--json"}, want: "serve: flag --json is not valid"},
		{name: "whitespace actor", argv: []string{"show", "GILIR-1", "--actor", "   "}, want: "non-empty value"},
		{name: "unknown flag", argv: []string{"show", "GILIR-1", "--bogus"}, want: "unknown flag"},
		{name: "duplicate flag", argv: []string{"show", "GILIR-1", "--json", "--json"}, want: "duplicate flag --json"},
		{name: "extra argument", argv: []string{"show", "GILIR-1", "extra"}, want: "unexpected argument"},
		{name: "request changes missing message", argv: []string{"request-changes", "GILIR-1"}, want: "missing required flag --message"},
		{name: "request changes blank message", argv: []string{"request-changes", "GILIR-1", "--message", "   "}, want: "a message is required"},
		{name: "block missing message", argv: []string{"block", "GILIR-1"}, want: "missing required flag --message"},
		{name: "block blank message", argv: []string{"block", "GILIR-1", "--message", "   "}, want: "a message is required when blocking a task"},
		{name: "block bare task id", argv: []string{"block", "1", "--message", "x"}, want: "GILIR-"},
		{name: "edit comment invalid comment id", argv: []string{"edit-comment", "GILIR-1", "nope", "body"}, want: "invalid comment id"},
		{name: "remove comment missing comment id", argv: []string{"remove-comment", "GILIR-1"}, want: "missing <comment-id>"},
		{name: "version with extra argument", argv: []string{"version", "extra"}, want: `version: unexpected argument "extra"`},
		{name: "short version with extra argument", argv: []string{"-v", "extra"}, want: `version: unexpected argument "extra"`},
		{name: "version flag with a command", argv: []string{"--version", "list"}, want: `version: unexpected argument "list"`},
		{name: "version flag on a server command", argv: []string{"serve", "--version"}, want: `version: unexpected argument "serve"`},
		{name: "version rejects an ordinary flag", argv: []string{"version", "--repo", "1"}, want: "version: flag --repo is not valid"},
		{name: "duplicate version flags", argv: []string{"--version", "--version"}, want: "duplicate flag --version"},
		{name: "version flag takes no value", argv: []string{"--version=1"}, want: "flag --version does not take a value"},
		{name: "terminator protects the version flag", argv: []string{"--", "--version"}, want: `unknown command "--version"`},
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

func TestRunJSONUsageError(t *testing.T) {
	executor := &recordingExecutor{}
	code, stdout, stderr := runCLI([]string{"frobnicate", "--json"}, "", executor)

	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
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
	if !strings.Contains(failure.Error, "unknown command") {
		t.Fatalf("error = %q, want unknown command", failure.Error)
	}
	if len(executor.calls) != 0 {
		t.Fatalf("execute calls = %d, want 0", len(executor.calls))
	}
}

func TestRunSkillObservableContract(t *testing.T) {
	executor := &recordingExecutor{}
	code, plain, stderr := runCLI([]string{"skill"}, "", executor)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	// `gilir skill` prints a plain Markdown manual. Agent frontmatter belongs to
	// skills/gilir/SKILL.md, not to the embedded text.
	if strings.HasPrefix(strings.TrimSpace(plain), "---") {
		t.Fatalf("skill still starts with frontmatter:\n%s", plain)
	}
	normalized := strings.Join(strings.Fields(plain), " ")
	for _, want := range []string{
		`gilir show GILIR-42`,
		`gilir comment "$task_id"`,
		`--actor "agent:$session_id"`,
		`Progress:`,
		`gilir update "$task_id" --status in-review`,
		`gilir --help`,
		"gilir repo add /srv/payments # auto-creates project",
		"gilir repo add /srv/payments --project 3",
		"gilir repo list",
		"gilir create \"Fix rounding\" --repo 1 --status todo --priority high",
		"gilir depend GILIR-7 GILIR-5",
		"gilir block \"$task_id\" --message",
		"gilir unblock \"$task_id\"",
		"gilir request-changes GILIR-7 --message",
		"never approve a task you worked on",
		"worktree",
	} {
		if !strings.Contains(normalized, want) {
			t.Fatalf("skill missing %q:\n%s", want, plain)
		}
	}
	for _, unwanted := range []string{"name: gilir", "--project 1 --repo 1"} {
		if strings.Contains(plain, unwanted) {
			t.Fatalf("skill still contains %q:\n%s", unwanted, plain)
		}
	}
	if len(executor.calls) != 0 {
		t.Fatalf("execute calls = %d, want 0", len(executor.calls))
	}

	code, stdout, stderr := runCLI([]string{"skill", "--json"}, "", executor)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
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
	if len(executor.calls) != 0 {
		t.Fatalf("execute calls = %d, want 0", len(executor.calls))
	}
}

func TestRunVersionObservableContract(t *testing.T) {
	tests := []struct {
		name string
		argv []string
	}{
		{name: "version command", argv: []string{"version"}},
		{name: "short alias", argv: []string{"-v"}},
		{name: "long flag", argv: []string{"--version"}},
		{name: "json before short alias", argv: []string{"--json", "-v"}},
		{name: "json after long flag", argv: []string{"--version", "--json"}},
		{name: "json as a command flag", argv: []string{"version", "--json"}},
	}
	want := "gilir " + currentVersion() + "\n"

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
			if json.Valid([]byte(strings.TrimSpace(stdout))) {
				var payload struct {
					Version string `json:"version"`
				}
				if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
					t.Fatalf("stdout is not a version payload: %v (%q)", err, stdout)
				}
				if payload.Version != currentVersion() {
					t.Fatalf("json version = %q, want %q", payload.Version, currentVersion())
				}
			} else if stdout != want {
				t.Fatalf("stdout = %q, want %q", stdout, want)
			}
			if len(executor.calls) != 0 {
				t.Fatalf("execute calls = %d, want 0", len(executor.calls))
			}
		})
	}
}

func TestResolveVersion(t *testing.T) {
	tests := []struct {
		name    string
		stamped string
		info    *debug.BuildInfo
		ok      bool
		want    string
	}{
		{
			name:    "stamped version wins over build info",
			stamped: "0.3.2",
			info:    &debug.BuildInfo{Main: debug.Module{Version: "v9.9.9"}},
			ok:      true,
			want:    "0.3.2",
		},
		{
			name:    "stamped version drops a leading v",
			stamped: "v0.3.2",
			ok:      false,
			want:    "0.3.2",
		},
		{
			name: "go install module version",
			info: &debug.BuildInfo{Main: debug.Module{Version: "v0.3.2"}},
			ok:   true,
			want: "0.3.2",
		},
		{
			name: "pseudo-version keeps the commit and dirty suffix",
			info: &debug.BuildInfo{Main: debug.Module{Version: "v0.3.3-0.20261128120000-803e23255e1a+dirty"}},
			ok:   true,
			want: "0.3.3-0.20261128120000-803e23255e1a+dirty",
		},
		{name: "devel build info is not a version", info: &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, ok: true, want: devVersion},
		{name: "empty build info version", info: &debug.BuildInfo{}, ok: true, want: devVersion},
		{name: "unavailable build info", ok: false, want: devVersion},
		{name: "nil build info", info: nil, ok: true, want: devVersion},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := resolveVersion(test.stamped, test.info, test.ok); got != test.want {
				t.Fatalf("resolveVersion = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRunHelpReflectsRepoFirstCommands(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want []string
	}{
		{
			name: "general help",
			argv: []string{"--help"},
			want: []string{
				"Usage:",
				"repo list [--project <id>]",
				"repo add <path> [--project <id>]",
				"repo link <repo-id> <project-id>",
				"repo unlink <repo-id> <project-id>",
				"list [--project <id>] [--repo <id>]",
				"create <title> --repo <id> [--project <id>]",
				"delete <task-id>",
				"serve [--host <host>] [--port <port>] [--db <path>]",
				"service start [--host <host>] [--port <port>] [--db <path>]",
				"service status",
				"Local commands:",
				"skill",
				"version",
				"--version, -v    Print the version",
			},
		},
		{
			name: "service group help",
			argv: []string{"service", "--help"},
			want: []string{
				"start [--host <host>] [--port <port>] [--db <path>]",
				"stop",
				"restart [--host <host>] [--port <port>] [--db <path>]",
				"status",
				"pause",
				"resume",
				"concurrency <positive-integer>",
			},
		},
		{
			name: "service start help",
			argv: []string{"service", "start", "--help"},
			want: []string{
				"gilir service start [--host <host>] [--port <port>] [--db <path>]",
				"--host <host>               HTTP host to bind (default: 127.0.0.1)",
				"--port <port>               HTTP port to bind (default: 44547)",
				"--db <path>                 SQLite database path",
			},
		},
		{
			name: "serve help",
			argv: []string{"serve", "--help"},
			want: []string{
				"gilir serve [--host <host>] [--port <port>] [--db <path>]",
				"Run the service in the foreground.",
				"--db <path>                 SQLite database path",
			},
		},
		{
			name: "repo group help",
			argv: []string{"repo", "--help"},
			want: []string{
				"link <repo-id> <project-id>",
				"unlink <repo-id> <project-id>",
				"add <path> [--project <id>]",
			},
		},
		{
			name: "repo add help",
			argv: []string{"repo", "add", "--help"},
			want: []string{
				"gilir repo add <path> [--project <id>]",
				"creating a project from its basename when --project is omitted",
			},
		},
		{
			name: "create help marks only repo required",
			argv: []string{"create", "--help"},
			want: []string{
				"gilir create <title> --repo <id> [--project <id>]",
				"--repo <id>                 Repository ID (required)",
				"--project <id>              Project ID",
			},
		},
		{
			name: "version command help",
			argv: []string{"version", "--help"},
			want: []string{
				"gilir version",
				"`gilir -v` and `gilir --version` are aliases",
			},
		},
		{
			name: "version flag help",
			argv: []string{"--version", "--help"},
			want: []string{
				"gilir version",
				"are aliases",
			},
		},
		{
			name: "short version help",
			argv: []string{"-v", "--help"},
			want: []string{
				"gilir version",
				"are aliases",
			},
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
			for _, want := range test.want {
				if !strings.Contains(stdout, want) {
					t.Fatalf("help missing %q:\n%s", want, stdout)
				}
			}
			if len(executor.calls) != 0 {
				t.Fatalf("execute calls = %d, want 0", len(executor.calls))
			}
		})
	}
}

func TestRunHelpJSON(t *testing.T) {
	executor := &recordingExecutor{}
	code, stdout, stderr := runCLI([]string{"--json", "--help"}, "", executor)
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
	if !strings.Contains(payload.Help, "Usage:") {
		t.Fatalf("help payload missing usage: %q", payload.Help)
	}
	if len(executor.calls) != 0 {
		t.Fatalf("execute calls = %d, want 0", len(executor.calls))
	}
}
