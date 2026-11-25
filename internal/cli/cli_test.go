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
	command string
	actor   string
	args    map[string]any
}

type recordingExecutor struct {
	calls  []recordedCall
	result any
	err    error
}

func (r *recordingExecutor) execute(_ context.Context, command, actor string, args map[string]any) (any, error) {
	r.calls = append(r.calls, recordedCall{command: command, actor: actor, args: args})
	return r.result, r.err
}

func runCLI(argv []string, defaultActor string, executor *recordingExecutor) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), argv, &stdout, &stderr, defaultActor, executor.execute)
	return code, stdout.String(), stderr.String()
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
			argv:        []string{"--actor", "agent:before", "--json", "show", "7"},
			wantActor:   "agent:before",
			wantCommand: "task.show",
			wantArgs:    map[string]any{"id": int64(7)},
			wantJSON:    true,
		},
		{
			name:        "global flags after positionals",
			argv:        []string{"show", "7", "--json", "--actor=agent:after"},
			wantActor:   "agent:after",
			wantCommand: "task.show",
			wantArgs:    map[string]any{"id": int64(7)},
			wantJSON:    true,
		},
		{
			name:        "empty default actor falls back",
			argv:        []string{"show", "7"},
			wantActor:   "user",
			wantCommand: "task.show",
			wantArgs:    map[string]any{"id": int64(7)},
		},
		{
			name:         "default actor from main",
			argv:         []string{"show", "7"},
			defaultActor: "agent:environment",
			wantActor:    "agent:environment",
			wantCommand:  "task.show",
			wantArgs:     map[string]any{"id": int64(7)},
		},
		{
			name:         "explicit actor overrides default",
			argv:         []string{"show", "7", "--actor", "agent:explicit"},
			defaultActor: "agent:environment",
			wantActor:    "agent:explicit",
			wantCommand:  "task.show",
			wantArgs:     map[string]any{"id": int64(7)},
		},
		{
			name:        "terminator protects flag-like body",
			argv:        []string{"comment", "7", "--", "--json"},
			wantActor:   "user",
			wantCommand: "task.comment",
			wantArgs:    map[string]any{"id": int64(7), "body": "--json"},
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
			name:        "repo add explicit project",
			argv:        []string{"repo", "add", "/src/app", "--project", "3"},
			wantCommand: "repo.add",
			wantArgs:    map[string]any{"path": "/src/app", "project_id": int64(3)},
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
			argv:        []string{"create", "Fix rounding", "--repo", "2", "--project", "3", "--description", "body", "--status", "todo", "--priority", "high"},
			wantCommand: "task.create",
			wantArgs: map[string]any{
				"repo_id":     int64(2),
				"project_id":  int64(3),
				"title":       "Fix rounding",
				"description": "body",
				"status":      "todo",
				"priority":    "high",
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

func TestRunRejectsRequiredAndInvalidArguments(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want string
	}{
		{name: "missing required flag", argv: []string{"create", "Title", "--project", "1"}, want: "missing required flag --repo"},
		{name: "invalid task id", argv: []string{"show", "nope"}, want: "invalid task id"},
		{name: "invalid optional repo id", argv: []string{"list", "--project", "1", "--repo", "nope"}, want: "invalid repo id"},
		{name: "invalid concurrency", argv: []string{"daemon", "concurrency", "0"}, want: "invalid concurrency"},
		{name: "whitespace actor", argv: []string{"show", "1", "--actor", "   "}, want: "non-empty value"},
		{name: "unknown flag", argv: []string{"show", "1", "--bogus"}, want: "unknown flag"},
		{name: "duplicate flag", argv: []string{"show", "1", "--json", "--json"}, want: "duplicate flag --json"},
		{name: "extra argument", argv: []string{"show", "1", "extra"}, want: "unexpected argument"},
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

func TestRunJSONSemantics(t *testing.T) {
	t.Run("success is bare data", func(t *testing.T) {
		executor := &recordingExecutor{result: map[string]any{"id": int64(7), "name": "Demo"}}
		code, stdout, stderr := runCLI([]string{"show", "7", "--json"}, "", executor)

		if code != 0 {
			t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
		}
		if stderr != "" {
			t.Fatalf("stderr = %q, want empty", stderr)
		}
		var decoded any
		if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
			t.Fatalf("stdout is not JSON: %v (%q)", err, stdout)
		}
		want := map[string]any{"id": float64(7), "name": "Demo"}
		if !reflect.DeepEqual(decoded, want) {
			t.Fatalf("decoded = %#v, want %#v", decoded, want)
		}
	})

	t.Run("command failure", func(t *testing.T) {
		executor := &recordingExecutor{err: errors.New("store offline")}
		code, stdout, stderr := runCLI([]string{"show", "7", "--json"}, "", executor)

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
	})

	t.Run("malformed invocation", func(t *testing.T) {
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
	})
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
	if !strings.HasPrefix(plain, "---\nname: gilir\ndescription: ") {
		t.Fatalf("skill is missing frontmatter:\n%s", plain)
	}
	for _, want := range []string{
		`gilir show "$task_id" --json`,
		`gilir comment "$task_id"`,
		`--actor "agent:$session_id"`,
		`Progress:`,
		`Blocked by:`,
		`gilir update "$task_id" --status in-review --actor "agent:$session_id"`,
		`gilir --help`,
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("skill missing %q:\n%s", want, plain)
		}
	}
	for _, unwanted := range []string{"gilir approve", "gilir mark"} {
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
				"repo list [--project <id>]",
				"repo add <path> [--project <id>]",
				"repo link <repo-id> <project-id>",
				"repo unlink <repo-id> <project-id>",
				"list [--project <id>] [--repo <id>]",
				"create <title> --repo <id> [--project <id>]",
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

func TestRunSkillDocumentsRepoFirstWorkflow(t *testing.T) {
	executor := &recordingExecutor{}
	code, plain, stderr := runCLI([]string{"skill"}, "", executor)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	for _, want := range []string{
		"gilir repo add ~/src/payments                                    # auto-creates project",
		"gilir repo add ~/src/payments --project 3",
		"gilir repo list",
		"gilir repo list --project 3",
		"gilir repo link 1 4",
		"gilir repo unlink 1 4",
		`gilir create "Fix rounding" --repo 1 --status todo --priority high`,
		"gilir list --project 3",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("skill missing %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "--project 1 --repo 1") {
		t.Fatalf("skill still shows --project as required for create:\n%s", plain)
	}
}

func TestRunHelp(t *testing.T) {
	executor := &recordingExecutor{}
	code, plain, stderr := runCLI([]string{"--help"}, "", executor)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(plain, "Usage:") {
		t.Fatalf("help missing usage:\n%s", plain)
	}
	if len(executor.calls) != 0 {
		t.Fatalf("execute calls = %d, want 0", len(executor.calls))
	}

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
}
