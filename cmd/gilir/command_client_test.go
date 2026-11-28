package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/armedi/gilir/internal/cli"
)

func TestCommandEndpointDefaultsToLoopback44547(t *testing.T) {
	t.Setenv("GILIR_URL", "")
	t.Setenv("GILIR_DB", filepath.Join(t.TempDir(), "other.sqlite"))

	endpoint, err := commandEndpoint(envOr("GILIR_URL", defaultCommandURL))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := endpoint.String(), "http://127.0.0.1:44547/api/v1/command"; got != want {
		t.Fatalf("endpoint = %q, want %q", got, want)
	}
}

func TestCommandEndpointAcceptsHTTPAndHTTPSOrigins(t *testing.T) {
	tests := []struct {
		value string
		want  string
	}{
		{"http://127.0.0.1:9000", "http://127.0.0.1:9000/api/v1/command"},
		{"http://127.0.0.1:9000/", "http://127.0.0.1:9000/api/v1/command"},
		{"https://gilir.example.test", "https://gilir.example.test/api/v1/command"},
		{"http://[::1]:44547", "http://[::1]:44547/api/v1/command"},
		{"http://localhost:8080", "http://localhost:8080/api/v1/command"},
	}
	for _, test := range tests {
		t.Run(test.value, func(t *testing.T) {
			endpoint, err := commandEndpoint(test.value)
			if err != nil {
				t.Fatalf("commandEndpoint(%q): %v", test.value, err)
			}
			if got := endpoint.String(); got != test.want {
				t.Fatalf("endpoint = %q, want %q", got, test.want)
			}
		})
	}
}

func TestCommandEndpointRejectsMalformedValues(t *testing.T) {
	for _, value := range []string{
		"127.0.0.1:44547",
		"ftp://127.0.0.1:44547",
		"http://",
		"http:///path",
		"http://:44547",
		"http://user:pass@127.0.0.1:44547",
		"http://127.0.0.1:44547/path",
		"http://127.0.0.1:44547?x=1",
		"http://127.0.0.1:44547#fragment",
		"http://127.0.0.1:44547#",
		"http://::1",
		"http://[::1",
		"http://127.0.0.1:044547",
		"http://127.0.0.1:44547/ ",
		"http://127.0.0.1:",
		"http://127.0.0.1:0",
		"http://127.0.0.1:65536",
		"http://127.0.0.1:http",
		" http://127.0.0.1:44547",
		"http://127.0.0.1:44547 ",
	} {
		t.Run(value, func(t *testing.T) {
			_, err := commandEndpoint(value)
			if err == nil || !strings.Contains(err.Error(), "invalid GILIR_URL") {
				t.Fatalf("commandEndpoint(%q) = %v, want invalid GILIR_URL", value, err)
			}
			if strings.Contains(err.Error(), "pass@") {
				t.Fatalf("error leaks credentials: %v", err)
			}
		})
	}
}

type capturedRequest struct {
	method      string
	path        string
	contentType string
	command     string
	actor       string
	args        map[string]json.RawMessage
}

// requestRecorder keeps the first request without blocking the server, so a
// regression that sends an extra request cannot deadlock cleanup.
type requestRecorder struct {
	mu    sync.Mutex
	count int
	first capturedRequest
}

func TestCLIStatefulCommandsUseOneHTTPCommandEndpoint(t *testing.T) {
	tests := []struct {
		name         string
		argv         []string
		defaultActor string
		wantCommand  string
		wantActor    string
		wantArgs     map[string]string
	}{
		{
			name:         "project create",
			argv:         []string{"project", "create", "Payments"},
			defaultActor: "agent:env",
			wantCommand:  "project.create",
			wantActor:    "agent:env",
			wantArgs:     map[string]string{"name": `"Payments"`},
		},
		{
			name:         "repo add with project",
			argv:         []string{"repo", "add", "/srv/app", "--project", "3"},
			defaultActor: "agent:env",
			wantCommand:  "repo.add",
			wantActor:    "agent:env",
			wantArgs:     map[string]string{"path": `"/srv/app"`, "project_id": "3"},
		},
		{
			name:         "task list with the maximum int64",
			argv:         []string{"list", "--project", "9223372036854775807"},
			defaultActor: "agent:env",
			wantCommand:  "task.list",
			wantActor:    "agent:env",
			wantArgs:     map[string]string{"project_id": "9223372036854775807"},
		},
		{
			name:         "task update",
			argv:         []string{"update", "GILIR-1", "--status", "todo"},
			defaultActor: "agent:env",
			wantCommand:  "task.update",
			wantActor:    "agent:env",
			wantArgs:     map[string]string{"id": `"GILIR-1"`, "status": `"todo"`},
		},
		{
			name:         "explicit actor wins",
			argv:         []string{"approve", "GILIR-1", "--actor", "agent:flag"},
			defaultActor: "agent:env",
			wantCommand:  "task.review",
			wantActor:    "agent:flag",
			wantArgs:     map[string]string{"id": `"GILIR-1"`, "approve": "true"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := &requestRecorder{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Command string                     `json:"command"`
					Actor   string                     `json:"actor"`
					Args    map[string]json.RawMessage `json:"args"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				recorder.mu.Lock()
				recorder.count++
				if recorder.count == 1 {
					recorder.first = capturedRequest{
						method:      r.Method,
						path:        r.URL.Path,
						contentType: r.Header.Get("Content-Type"),
						command:     request.Command,
						actor:       request.Actor,
						args:        request.Args,
					}
				}
				recorder.mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"data":{"id":9223372036854775807,"repo_ids":[3]}}`)
			}))
			defer server.Close()

			dbPath := filepath.Join(t.TempDir(), "nested", "gilir.sqlite")
			t.Setenv("GILIR_URL", server.URL)
			t.Setenv("GILIR_DB", dbPath)

			var stdout, stderr bytes.Buffer
			code := cli.Run(context.Background(), append([]string{"--json"}, test.argv...), &stdout, &stderr, test.defaultActor, execute)
			if code != 0 {
				t.Fatalf("exit code = %d (stderr %q)", code, stderr.String())
			}
			if stderr.Len() != 0 {
				t.Fatalf("stderr = %q", stderr.String())
			}
			var decoded map[string]json.RawMessage
			if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
				t.Fatalf("stdout is not a JSON object: %v (%q)", err, stdout.String())
			}
			var repoIDs []int64
			if len(decoded) != 2 || string(decoded["id"]) != "9223372036854775807" ||
				json.Unmarshal(decoded["repo_ids"], &repoIDs) != nil || len(repoIDs) != 1 || repoIDs[0] != 3 {
				t.Fatalf("stdout = %q, want bare data with an exact int64 ID and repo IDs", stdout.String())
			}

			recorder.mu.Lock()
			if recorder.count != 1 {
				recorder.mu.Unlock()
				t.Fatalf("requests = %d, want exactly 1", recorder.count)
			}
			request := recorder.first
			recorder.mu.Unlock()
			if request.method != http.MethodPost {
				t.Errorf("method = %q, want POST", request.method)
			}
			if request.path != "/api/v1/command" {
				t.Errorf("path = %q, want /api/v1/command", request.path)
			}
			if request.contentType != "application/json" {
				t.Errorf("content type = %q, want application/json", request.contentType)
			}
			if request.command != test.wantCommand {
				t.Errorf("command = %q, want %q", request.command, test.wantCommand)
			}
			if request.actor != test.wantActor {
				t.Errorf("actor = %q, want %q", request.actor, test.wantActor)
			}
			if len(request.args) != len(test.wantArgs) {
				t.Errorf("args = %v, want %v", request.args, test.wantArgs)
			}
			for key, want := range test.wantArgs {
				if got := string(request.args[key]); got != want {
					t.Errorf("arg %s = %s, want %s", key, got, want)
				}
			}
			if _, err := os.Stat(dbPath); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("client touched GILIR_DB: %v", err)
			}
			if _, err := os.Stat(filepath.Dir(dbPath)); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("client created the database directory: %v", err)
			}
		})
	}
}

func TestCLIHumanOutputKeepsExactInt64(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":{"id":9223372036854775807}}`)
	}))
	defer server.Close()
	t.Setenv("GILIR_URL", server.URL)

	var stdout, stderr bytes.Buffer
	if code := cli.Run(context.Background(), []string{"show", "GILIR-1"}, &stdout, &stderr, "", execute); code != 0 {
		t.Fatalf("exit code = %d (stderr %q)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "id: 9223372036854775807") {
		t.Fatalf("human output lost the exact int64: %q", stdout.String())
	}
}

func TestCLIRejectsTrailingAndTruncatedResponses(t *testing.T) {
	for name, body := range map[string]string{
		"trailing json":   `{"data":{"id":1}}garbage`,
		"trailing object": `{"data":{"id":1}}{"data":{"id":2}}`,
		"empty":           ``,
		"truncated":       `{"data":{"id":`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, body)
			}))
			defer server.Close()
			t.Setenv("GILIR_URL", server.URL)

			var stdout, stderr bytes.Buffer
			code := cli.Run(context.Background(), []string{"list", "--json"}, &stdout, &stderr, "", execute)
			if code != 1 {
				t.Fatalf("exit code = %d, want 1", code)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
			if !strings.Contains(stderr.String(), "valid command response") &&
				!strings.Contains(stderr.String(), "trailing data after the command response") {
				t.Fatalf("stderr = %q", stderr.String())
			}
		})
	}
}

func TestCLIWhitespaceDefaultActorStillWorksForReads(t *testing.T) {
	var stdout, stderr bytes.Buffer
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Actor string `json:"actor"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode: %v", err)
		}
		if request.Actor != "user" {
			t.Errorf("actor = %q, want user", request.Actor)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[]}`)
	}))
	defer server.Close()
	t.Setenv("GILIR_URL", server.URL)

	if code := cli.Run(context.Background(), []string{"project", "list"}, &stdout, &stderr, "   ", execute); code != 0 {
		t.Fatalf("exit code = %d (stderr %q)", code, stderr.String())
	}
}

func TestCLICommandFailureComesFromTheResponseEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":{"code":"not_found","message":"task GILIR-9 not found"}}`)
	}))
	defer server.Close()
	t.Setenv("GILIR_URL", server.URL)

	var stdout, stderr bytes.Buffer
	code := cli.Run(context.Background(), []string{"show", "GILIR-9", "--json"}, &stdout, &stderr, "", execute)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	var failure struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(stderr.Bytes(), &failure); err != nil {
		t.Fatalf("stderr is not JSON: %v (%q)", err, stderr.String())
	}
	if failure.Error != "task GILIR-9 not found" {
		t.Fatalf("error = %q", failure.Error)
	}
}

func TestCLIUnreachableTargetFailsWithoutLocalFallback(t *testing.T) {
	// Port 1 on loopback is reserved and needs privilege to bind, so this
	// target stays unreachable instead of racing another listener.
	target := "http://127.0.0.1:1"
	dbPath := filepath.Join(t.TempDir(), "nested", "gilir.sqlite")
	t.Setenv("GILIR_URL", target)
	t.Setenv("GILIR_DB", dbPath)

	var stdout, stderr bytes.Buffer
	code := cli.Run(context.Background(), []string{"project", "create", "Offline", "--json"}, &stdout, &stderr, "", execute)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	var failure struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(stderr.Bytes(), &failure); err != nil {
		t.Fatalf("stderr is not JSON: %v (%q)", err, stderr.String())
	}
	for _, want := range []string{target, "gilir service start", "GILIR_URL"} {
		if !strings.Contains(failure.Error, want) {
			t.Fatalf("error %q does not mention %q", failure.Error, want)
		}
	}
	if _, err := os.Stat(dbPath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("client fell back to GILIR_DB: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(dbPath)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("client created the database directory: %v", err)
	}
}

func TestCLIMutationsAreNotRetried(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"error":{"code":"internal_error","message":"store failed"}}`)
	}))
	defer server.Close()
	t.Setenv("GILIR_URL", server.URL)

	_, err := execute(context.Background(), "task.update", "user", map[string]any{"id": "GILIR-1", "status": "done"})
	if err == nil || !strings.Contains(err.Error(), "store failed") {
		t.Fatalf("mutation error = %v", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("mutation requests = %d, want 1", got)
	}
}
