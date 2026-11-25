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
