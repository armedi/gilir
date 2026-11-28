package opencode

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

type request struct {
	Method, Path, Host, Auth string
	Query                    string
	Body                     map[string]any
}

type reply struct {
	status int
	body   string
}

// fixture serves canned replies keyed by "METHOD path" and records requests.
func fixture(t *testing.T, password string, routes map[string]reply) (*Client, *[]request, string) {
	t.Helper()
	var got []request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := request{Method: r.Method, Path: r.URL.EscapedPath(), Host: r.Host, Auth: r.Header.Get("Authorization"), Query: r.URL.Query().Encode()}
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			if err := json.Unmarshal(b, &req.Body); err != nil {
				t.Errorf("request body is not JSON: %s", b)
			}
		}
		got = append(got, req)
		rep, ok := routes[r.Method+" "+req.Path]
		if !ok {
			rep = reply{404, `{"_tag":"RouteNotFound","message":"no fixture"}`}
		}
		w.Header().Set("Content-Type", "application/json")
		if rep.status/100 == 3 {
			w.Header().Set("Location", "/api/elsewhere")
		}
		w.WriteHeader(rep.status)
		io.WriteString(w, rep.body)
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, password)
	if err != nil {
		t.Fatal(err)
	}
	return c, &got, srv.URL
}

func sessionJSON(id, parent, outcome string) string {
	p := ""
	if parent != "" {
		p = `"parentID":"` + parent + `",`
	}
	o := ""
	if outcome != "" {
		o = `"outcome":"` + outcome + `",`
	}
	return `{"data":{"id":"` + id + `",` + p + o + `"projectID":"prj_1","title":"T","cost":0,` +
		`"tokens":{},"time":{"created":1700000000000,"updated":1700000000500,"busy":1700000000100,"idle":1700000000400.7},` +
		`"location":{"directory":"/work/repo"}}}`
}

func TestNewAcceptsOnlyLoopbackBaseURLs(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1:4096", "http://localhost:4096/", "https://[::1]:4096", "http://127.0.0.2"} {
		if _, err := New(raw, ""); err != nil {
			t.Errorf("New(%q) = %v, want ok", raw, err)
		}
	}
	for _, raw := range []string{
		"http://example.com:4096", "http://10.0.0.1", "http://0.0.0.0:4096", "ftp://127.0.0.1",
		"http://u:secret@127.0.0.1", "http://127.0.0.1/api", "http://127.0.0.1/?x=1",
		"http://127.0.0.1/#f", "127.0.0.1:4096", "http://localhost.example.com",
		"http://127.0.0.1:", "http://[::1]:", "http://127.0.0.1:0", "http://127.0.0.1:65536",
		"http://[::ffff:127.0.0.1]:4096",
	} {
		_, err := New(raw, "")
		if err == nil {
			t.Errorf("New(%q) succeeded, want error", raw)
		} else if strings.Contains(err.Error(), "secret") {
			t.Errorf("New(%q) error leaks credentials: %v", raw, err)
		}
	}
}

func TestGetParsesSessionInfo(t *testing.T) {
	c, _, _ := fixture(t, "", map[string]reply{
		"GET /api/session/ses_a": {200, sessionJSON("ses_a", "ses_p", "succeeded")},
	})
	got, err := c.Get(context.Background(), "ses_a")
	if err != nil {
		t.Fatal(err)
	}
	want := Session{ID: "ses_a", ParentID: "ses_p", Title: "T", Directory: "/work/repo", Outcome: "succeeded",
		CreatedAt: 1700000000000, UpdatedAt: 1700000000500, BusyAt: 1700000000100, IdleAt: 1700000000400}
	if got != want {
		t.Fatalf("Get = %+v\nwant  %+v", got, want)
	}
}

func TestGetRejectsMalformedSessions(t *testing.T) {
	cases := map[string]string{
		"unknown outcome":  sessionJSON("ses_a", "", "exploded"),
		"other id":         sessionJSON("ses_b", "", ""),
		"missing data":     `{}`,
		"missing location": `{"data":{"id":"ses_a","time":{"created":1,"updated":1}}}`,
		"missing time":     `{"data":{"id":"ses_a","location":{"directory":"/w"}}}`,
		"not json":         `<html>`,
	}
	for name, body := range cases {
		c, _, _ := fixture(t, "", map[string]reply{"GET /api/session/ses_a": {200, body}})
		if _, err := c.Get(context.Background(), "ses_a"); err == nil {
			t.Errorf("%s: Get succeeded, want error", name)
		}
	}
}

func TestActiveParentsFiltersChildSessions(t *testing.T) {
	c, _, _ := fixture(t, "", map[string]reply{
		"GET /api/session/active":    {200, `{"data":{"ses_root":{"type":"running"},"ses_child":{"type":"running"}}}`},
		"GET /api/session/ses_root":  {200, sessionJSON("ses_root", "", "")},
		"GET /api/session/ses_child": {200, sessionJSON("ses_child", "ses_root", "")},
	})
	got, err := c.ActiveParents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]struct{}{"ses_root": {}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ActiveParents = %v, want %v", got, want)
	}
}

func TestActiveParentsFailsClosed(t *testing.T) {
	cases := map[string]map[string]reply{
		"unknown state": {"GET /api/session/active": {200, `{"data":{"ses_a":{"type":"paused"}}}`}},
		"bad id":        {"GET /api/session/active": {200, `{"data":{"xyz":{"type":"running"}}}`}},
		"missing data":  {"GET /api/session/active": {200, `{}`}},
		"active error":  {"GET /api/session/active": {500, `{}`}},
		"lookup 404":    {"GET /api/session/active": {200, `{"data":{"ses_a":{"type":"running"}}}`}},
		"lookup error": {
			"GET /api/session/active": {200, `{"data":{"ses_a":{"type":"running"}}}`},
			"GET /api/session/ses_a":  {502, `{}`},
		},
	}
	for name, routes := range cases {
		c, _, _ := fixture(t, "", routes)
		if got, err := c.ActiveParents(context.Background()); err == nil {
			t.Errorf("%s: ActiveParents = %v, want error", name, got)
		}
	}
}

func TestCreateSendsStableIDAndVerifiesResult(t *testing.T) {
	c, reqs, _ := fixture(t, "", map[string]reply{
		"POST /api/session": {200, sessionJSON("ses_new", "", "")},
	})
	s, err := c.Create(context.Background(), "ses_new", "Task 1", "/work/repo")
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "ses_new" || s.Directory != "/work/repo" {
		t.Fatalf("Create = %+v", s)
	}
	want := map[string]any{"id": "ses_new", "title": "Task 1", "location": map[string]any{"directory": "/work/repo"}}
	if got := (*reqs)[0].Body; !reflect.DeepEqual(got, want) {
		t.Fatalf("Create body = %v, want %v", got, want)
	}

	// Uncleaned request and response paths compare equal after filepath.Clean;
	// the cleaned path is what goes on the wire.
	unclean := strings.Replace(sessionJSON("ses_new", "", ""), `"/work/repo"`, `"/work/repo/"`, 1)
	c, reqs, _ = fixture(t, "", map[string]reply{"POST /api/session": {200, unclean}})
	if _, err := c.Create(context.Background(), "ses_new", "Task 1", "/work/./repo//"); err != nil {
		t.Fatalf("Create with uncleaned directories: %v", err)
	}
	if got := (*reqs)[0].Body["location"]; !reflect.DeepEqual(got, map[string]any{"directory": "/work/repo"}) {
		t.Errorf("Create location = %v, want cleaned /work/repo", got)
	}

	for name, body := range map[string]string{
		"other id":        sessionJSON("ses_other", "", ""),
		"child session":   sessionJSON("ses_new", "ses_p", ""),
		"other directory": strings.Replace(sessionJSON("ses_new", "", ""), "/work/repo", "/elsewhere", 1),
	} {
		c, _, _ := fixture(t, "", map[string]reply{"POST /api/session": {200, body}})
		if _, err := c.Create(context.Background(), "ses_new", "Task 1", "/work/repo"); err == nil {
			t.Errorf("%s: Create succeeded, want error", name)
		}
	}
}

func TestPromptSendsOnlyIDAndText(t *testing.T) {
	ok := `{"data":{"id":"msg_1","sessionID":"ses_a","time":{"created":5},"type":"user","payload":{},"delivery":"queue"}}`
	c, reqs, _ := fixture(t, "", map[string]reply{"POST /api/session/ses_a/prompt": {200, ok}})
	if err := c.Prompt(context.Background(), "ses_a", "msg_1", "do it"); err != nil {
		t.Fatal(err)
	}
	if got, want := (*reqs)[0].Body, map[string]any{"id": "msg_1", "text": "do it"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Prompt body = %v, want %v", got, want)
	}

	c, _, _ = fixture(t, "", map[string]reply{"POST /api/session/ses_a/prompt": {200, strings.Replace(ok, "msg_1", "msg_2", 1)}})
	if err := c.Prompt(context.Background(), "ses_a", "msg_1", "do it"); err == nil {
		t.Error("Prompt accepted a response for another message")
	}

	c, _, _ = fixture(t, "", map[string]reply{"POST /api/session/ses_a/prompt": {409, `{"_tag":"ConflictError","message":"id reused"}`}})
	var se *StatusError
	if err := c.Prompt(context.Background(), "ses_a", "msg_1", "do it"); !errors.As(err, &se) || se.StatusCode != 409 || se.Tag != "ConflictError" {
		t.Errorf("Prompt conflict = %v, want 409 ConflictError", err)
	}
}

func TestMessageCreatedAtDistinguishesNotFound(t *testing.T) {
	c, _, _ := fixture(t, "", map[string]reply{
		"GET /api/session/ses_a/message/msg_1": {200, `{"data":{"id":"msg_1","time":{"created":1700000000123},"text":"x","type":"user"}}`},
		"GET /api/session/ses_a/message/msg_q": {404, `{"_tag":"MessageNotFoundError","sessionID":"ses_a","messageID":"msg_q","message":"not found"}`},
		"GET /api/session/ses_a/message/msg_s": {500, `{}`},
		"GET /api/session/ses_a/message/msg_a": {200, `{"data":{"id":"msg_a","time":{"created":1},"type":"assistant"}}`},
	})
	ctx := context.Background()
	if got, err := c.MessageCreatedAt(ctx, "ses_a", "msg_1"); err != nil || got != 1700000000123 {
		t.Fatalf("MessageCreatedAt = %d, %v", got, err)
	}
	_, err := c.MessageCreatedAt(ctx, "ses_a", "msg_q")
	var se *StatusError
	if !IsNotFound(err) || !errors.As(err, &se) || se.Tag != "MessageNotFoundError" {
		t.Errorf("queued message err = %v, want typed 404", err)
	}
	if !IsMessageNotFound(err) || IsSessionNotFound(err) {
		t.Errorf("queued message err = %v, want IsMessageNotFound only", err)
	}
	if _, err := c.MessageCreatedAt(ctx, "ses_a", "msg_s"); err == nil || IsNotFound(err) {
		t.Errorf("server error = %v, want non-404 error", err)
	}
	if _, err := c.MessageCreatedAt(ctx, "ses_a", "msg_a"); err == nil {
		t.Error("assistant message accepted as user message")
	}
}

func TestInboxReturnsQueuedIDs(t *testing.T) {
	c, _, _ := fixture(t, "", map[string]reply{
		"GET /api/session/ses_a/inbox":    {200, `{"data":[{"id":"msg_1","type":"user"},{"id":"msg_2","type":"synthetic"}]}`},
		"GET /api/session/ses_b/inbox":    {200, `{"data":[{"type":"user"}]}`},
		"GET /api/session/ses_gone/inbox": {404, `{"_tag":"SessionNotFoundError","sessionID":"ses_gone","message":"gone"}`},
	})
	got, err := c.Inbox(context.Background(), "ses_a")
	if err != nil || !reflect.DeepEqual(got, []string{"msg_1", "msg_2"}) {
		t.Fatalf("Inbox = %v, %v", got, err)
	}
	if _, err := c.Inbox(context.Background(), "ses_b"); err == nil {
		t.Error("Inbox accepted an item without id")
	}
	if _, err := c.Inbox(context.Background(), "ses_gone"); !IsSessionNotFound(err) || IsMessageNotFound(err) {
		t.Errorf("Inbox deleted session = %v, want IsSessionNotFound only", err)
	}
	// An untagged 404 (e.g. unknown route) is still IsNotFound but not a
	// confirmed missing session.
	_, err = c.Inbox(context.Background(), "ses_missing")
	if !IsNotFound(err) || IsSessionNotFound(err) || IsMessageNotFound(err) {
		t.Errorf("Inbox untagged 404 = %v, want IsNotFound only", err)
	}
}

func TestProjectIDResolvesAbsoluteDirectory(t *testing.T) {
	c, reqs, _ := fixture(t, "", map[string]reply{
		"GET /api/location": {200, `{"directory":"/work/repo","project":{"id":"prj_1","directory":"/work/repo","canonical":"/work/repo"}}`},
	})
	got, err := c.ProjectID(context.Background(), "/work/repo/")
	if err != nil || got != "prj_1" {
		t.Fatalf("ProjectID = %q, %v", got, err)
	}
	if query := (*reqs)[0].Query; !strings.Contains(query, "location%5Bdirectory%5D=%2Fwork%2Frepo") {
		t.Fatalf("ProjectID query = %q, want deep-object location", query)
	}

	for name, body := range map[string]string{
		"no project":      `{"directory":"/work/repo"}`,
		"blank project":   `{"directory":"/work/repo","project":{"id":"","canonical":"/work/repo"}}`,
		"other directory": `{"directory":"/work/other","project":{"id":"prj_1"}}`,
		"missing dir":     `{"project":{"id":"prj_1"}}`,
	} {
		c, _, _ := fixture(t, "", map[string]reply{"GET /api/location": {200, body}})
		if _, err := c.ProjectID(context.Background(), "/work/repo"); err == nil {
			t.Errorf("%s: ProjectID accepted a malformed response", name)
		}
	}
	c, _, _ = fixture(t, "", map[string]reply{})
	if _, err := c.ProjectID(context.Background(), "relative/repo"); err == nil {
		t.Error("ProjectID accepted a relative directory")
	}
	if _, err := c.ProjectID(context.Background(), "/work/repo"); !IsNotFound(err) {
		t.Errorf("ProjectID unknown route = %v, want a 404", err)
	}
}

func TestListWorktreesRejectsBadDirectories(t *testing.T) {
	c, reqs, _ := fixture(t, "", map[string]reply{
		"GET /api/worktree": {200, `[{"directory":"/work/main"},{"directory":"/work/wt","strategy":"git"}]`},
	})
	got, err := c.ListWorktrees(context.Background(), "prj_1")
	if err != nil {
		t.Fatal(err)
	}
	want := []Worktree{{Directory: "/work/main"}, {Directory: "/work/wt", Strategy: "git"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListWorktrees = %+v, want %+v", got, want)
	}
	if query := (*reqs)[0].Query; query != "projectID=prj_1" {
		t.Fatalf("ListWorktrees query = %q", query)
	}

	if _, err := c.ListWorktrees(context.Background(), "  "); err == nil {
		t.Error("ListWorktrees accepted a blank project ID")
	}
	for name, body := range map[string]string{
		"relative": `[{"directory":"work/wt"}]`,
		"missing":  `[{"strategy":"git"}]`,
		"not list": `{"directory":"/work/wt"}`,
	} {
		c, _, _ := fixture(t, "", map[string]reply{"GET /api/worktree": {200, body}})
		if _, err := c.ListWorktrees(context.Background(), "prj_1"); err == nil {
			t.Errorf("%s: ListWorktrees accepted a malformed response", name)
		}
	}
}

func TestCreateWorktreePassesNameAndReturnsDirectory(t *testing.T) {
	c, reqs, _ := fixture(t, "", map[string]reply{
		"POST /api/worktree": {200, `{"directory":"/work/gilir-1","strategy":"git"}`},
	})
	got, err := c.CreateWorktree(context.Background(), "prj_1", "gilir-1")
	if err != nil {
		t.Fatal(err)
	}
	if want := (Worktree{Directory: "/work/gilir-1", Strategy: "git"}); got != want {
		t.Fatalf("CreateWorktree = %+v, want %+v", got, want)
	}
	if got, want := (*reqs)[0].Body, map[string]any{"projectID": "prj_1", "name": "gilir-1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("CreateWorktree body = %v, want %v", got, want)
	}

	if _, err := c.CreateWorktree(context.Background(), "", "gilir-1"); err == nil {
		t.Error("CreateWorktree accepted a blank project ID")
	}
	if _, err := c.CreateWorktree(context.Background(), "prj_1", "  "); err == nil {
		t.Error("CreateWorktree accepted a blank name")
	}

	c, _, _ = fixture(t, "", map[string]reply{"POST /api/worktree": {200, `{"directory":"relative"}`}})
	if _, err := c.CreateWorktree(context.Background(), "prj_1", "gilir-1"); err == nil {
		t.Error("CreateWorktree accepted a relative directory")
	}
	c, _, _ = fixture(t, "", map[string]reply{
		"POST /api/worktree": {400, `{"_tag":"WorktreeError","data":{"message":"not a git repository"}}`},
	})
	var se *StatusError
	if _, err := c.CreateWorktree(context.Background(), "prj_1", "gilir-1"); !errors.As(err, &se) || se.StatusCode != 400 {
		t.Errorf("CreateWorktree refusal = %v, want a 400 StatusError", err)
	}
}

func TestServerPIDReadsUnwrappedInfo(t *testing.T) {
	c, _, _ := fixture(t, "", map[string]reply{
		"GET /api/info": {200, `{"version":"2.4.1","pid":4242,"urls":[],"paths":{"tmp":"/tmp"}}`},
	})
	if got, err := c.ServerPID(context.Background()); err != nil || got != 4242 {
		t.Fatalf("ServerPID = %d, %v", got, err)
	}
	c, _, _ = fixture(t, "", map[string]reply{"GET /api/info": {200, `{"data":{"pid":4242}}`}})
	if _, err := c.ServerPID(context.Background()); err == nil {
		t.Error("ServerPID accepted a wrapped response")
	}
}

func TestAuthRedirectsAndBounds(t *testing.T) {
	c, reqs, _ := fixture(t, "hunter2", map[string]reply{
		"GET /api/info": {401, `{"_tag":"UnauthorizedError","message":"bad"}`},
	})
	_, err := c.ServerPID(context.Background())
	if (*reqs)[0].Auth != "Basic b3BlbmNvZGU6aHVudGVyMg==" { // opencode:hunter2
		t.Errorf("Authorization = %q", (*reqs)[0].Auth)
	}
	if err == nil || strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), "b3BlbmNvZGU6") {
		t.Errorf("401 error = %v, want error without credentials", err)
	}

	c, reqs, _ = fixture(t, "", map[string]reply{"GET /api/info": {200, `{"pid":1}`}})
	c.ServerPID(context.Background())
	if (*reqs)[0].Auth != "" {
		t.Errorf("Authorization sent without password: %q", (*reqs)[0].Auth)
	}

	c, reqs, _ = fixture(t, "", map[string]reply{
		"GET /api/info":      {302, ``},
		"GET /api/elsewhere": {200, `{"pid":1}`},
	})
	if _, err := c.ServerPID(context.Background()); err == nil || len(*reqs) != 1 {
		t.Errorf("redirect: err = %v after %d requests, want error without following", err, len(*reqs))
	}

	big := `{"pid":1,"version":"` + strings.Repeat("x", maxResponseBytes) + `"}`
	c, _, _ = fixture(t, "", map[string]reply{"GET /api/info": {200, big}})
	if _, err := c.ServerPID(context.Background()); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("oversized response err = %v", err)
	}
}

func TestLink(t *testing.T) {
	c, err := New("http://127.0.0.1:4096/", "secret")
	if err != nil {
		t.Fatal(err)
	}
	// base64url("http://127.0.0.1:4096") without padding.
	want := "http://127.0.0.1:4096/server/aHR0cDovLzEyNy4wLjAuMTo0MDk2/session/ses_abc"
	if got := c.Link("ses_abc"); got != want {
		t.Fatalf("Link = %q, want %q", got, want)
	}
}

// Browser origins are lowercase, use compressed bracketed IPv6 and omit
// default ports; the link must embed exactly that origin to match the web UI.
func TestLinkUsesBrowserOrigin(t *testing.T) {
	for raw, origin := range map[string]string{
		"http://LOCALHOST:4096":         "http://localhost:4096",
		"HTTP://LocalHost:4096/":        "http://localhost:4096",
		"http://[0:0:0:0:0:0:0:1]:4096": "http://[::1]:4096",
		"https://[0000::0001]":          "https://[::1]",
		"http://127.0.0.1:80":           "http://127.0.0.1",
		"https://localhost:443":         "https://localhost",
		"https://localhost:80":          "https://localhost:80",
		"http://127.0.0.1:04096":        "http://127.0.0.1:4096",
	} {
		c, err := New(raw, "")
		if err != nil {
			t.Errorf("New(%q): %v", raw, err)
			continue
		}
		want := origin + "/server/" + base64.RawURLEncoding.EncodeToString([]byte(origin)) + "/session/ses_abc"
		if got := c.Link("ses_abc"); got != want {
			t.Errorf("Link via %q = %q, want %q", raw, got, want)
		}
	}
}

func TestRequestsUseCanonicalBase(t *testing.T) {
	_, reqs, srvURL := fixture(t, "", map[string]reply{"GET /api/info": {200, `{"pid":7}`}})
	c, err := New(strings.Replace(srvURL, "http://127.0.0.1:", "HTTP://127.0.0.1:0", 1), "")
	if err != nil {
		t.Fatal(err)
	}
	if pid, err := c.ServerPID(context.Background()); err != nil || pid != 7 || len(*reqs) != 1 {
		t.Fatalf("ServerPID via uncanonical URL = %d, %v", pid, err)
	}
	if got, want := (*reqs)[0].Host, strings.TrimPrefix(srvURL, "http://"); got != want {
		t.Fatalf("HTTP Host = %q, want %q", got, want)
	}
}
