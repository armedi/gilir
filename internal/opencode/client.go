// Package opencode is a minimal client for the OpenCode V2 HTTP API of a
// local OpenCode server.
package opencode

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	requestTimeout   = 30 * time.Second
	maxResponseBytes = 8 << 20
	basicAuthUser    = "opencode"
)

// Client talks to one loopback OpenCode server.
type Client struct {
	base     *url.URL
	password string
	http     *http.Client
}

// Session is the subset of Session.Info the scheduler needs. Times are Unix
// milliseconds; zero means the server did not report the field.
type Session struct {
	ID        string
	ParentID  string
	Title     string
	Directory string
	Outcome   string
	CreatedAt int64
	UpdatedAt int64
	BusyAt    int64
	IdleAt    int64
}

// StatusError is a non-2xx response. Tag is the server's _tag when present.
type StatusError struct {
	Method     string
	Path       string
	StatusCode int
	Tag        string
	Message    string
}

func (e *StatusError) Error() string {
	s := fmt.Sprintf("opencode: %s %s: HTTP %d", e.Method, e.Path, e.StatusCode)
	if e.Tag != "" {
		s += " " + e.Tag
	}
	if e.Message != "" {
		s += ": " + e.Message
	}
	return s
}

// IsNotFound reports whether err is a 404 response from the server.
func IsNotFound(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && se.StatusCode == http.StatusNotFound
}

// IsUnauthorized reports whether err is a 401 response, which means the
// password the client was built with is no longer accepted.
func IsUnauthorized(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && se.StatusCode == http.StatusUnauthorized
}

// IsUnreachable reports whether err means no HTTP response was received:
// the server refused the connection, the name did not resolve, or the
// request timed out. The keep-alive client can recover from these by itself.
func IsUnreachable(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

// IsSessionNotFound reports whether err is OpenCode's tagged 404 for a
// missing session.
func IsSessionNotFound(err error) bool { return isTagged404(err, "SessionNotFoundError") }

// IsMessageNotFound reports whether err is OpenCode's tagged 404 for a
// missing (or not yet delivered) message.
func IsMessageNotFound(err error) bool { return isTagged404(err, "MessageNotFoundError") }

func isTagged404(err error, tag string) bool {
	var se *StatusError
	return errors.As(err, &se) && se.StatusCode == http.StatusNotFound && se.Tag == tag
}

// New returns a client for a loopback OpenCode server at rawURL. The URL must
// be http or https, name localhost or a loopback IP, and carry no
// credentials, path, query or fragment. A non-empty password is sent as HTTP
// Basic auth with user "opencode".
func New(rawURL, password string) (*Client, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, errors.New("opencode: invalid server URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("opencode: server URL scheme must be http or https, got %q", u.Scheme)
	}
	if u.User != nil {
		return nil, errors.New("opencode: server URL must not contain credentials")
	}
	if u.Opaque != "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" {
		return nil, errors.New("opencode: server URL must not contain a path, query or fragment")
	}
	host, err := canonicalHost(u)
	if err != nil {
		return nil, err
	}
	base := &url.URL{Scheme: u.Scheme, Host: host}

	dialer := &net.Dialer{Timeout: requestTimeout, Control: requireLoopbackDial}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   requestTimeout,
		ResponseHeaderTimeout: requestTimeout,
		MaxIdleConns:          4,
		IdleConnTimeout:       90 * time.Second,
	}
	return &Client{
		base:     base,
		password: password,
		http: &http.Client{
			Transport: transport,
			Timeout:   requestTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// canonicalHost returns u's host in the form a browser serializes it in an
// origin: lowercase localhost or a canonical loopback IP (IPv6 bracketed and
// compressed), with the port omitted when it is the scheme default. Web UI
// links depend on an exact origin match.
func canonicalHost(u *url.URL) (string, error) {
	name := strings.ToLower(u.Hostname())
	if name != "localhost" {
		ip := net.ParseIP(name)
		if ip == nil || !ip.IsLoopback() {
			return "", fmt.Errorf("opencode: server host %q is not loopback", u.Hostname())
		}
		if strings.Contains(name, ":") {
			if ip.To4() != nil {
				return "", fmt.Errorf("opencode: server host %q is an IPv4-mapped IPv6 address", u.Hostname())
			}
			name = "[" + ip.String() + "]"
		} else {
			name = ip.String()
		}
	}
	if strings.HasSuffix(u.Host, ":") {
		return "", errors.New("opencode: server URL has an empty port")
	}
	port := u.Port()
	if port == "" {
		return name, nil
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", fmt.Errorf("opencode: server URL port %q is out of range", port)
	}
	if (u.Scheme == "http" && n == 80) || (u.Scheme == "https" && n == 443) {
		return name, nil
	}
	return name + ":" + strconv.Itoa(n), nil
}

// requireLoopbackDial rejects connections whose resolved address is not
// loopback, so a remapped "localhost" cannot reach another host.
func requireLoopbackDial(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("opencode: refusing non-loopback address %s", host)
	}
	return nil
}

// ServerURL returns the canonical origin this client talks to. It never
// contains credentials.
func (c *Client) ServerURL() string { return c.base.Scheme + "://" + c.base.Host }

// Link returns the web UI URL for a session. It never contains credentials.
func (c *Client) Link(sessionID string) string {
	origin := c.base.Scheme + "://" + c.base.Host
	return origin + "/server/" + base64.RawURLEncoding.EncodeToString([]byte(origin)) +
		"/session/" + url.PathEscape(sessionID)
}

// ServerPID returns the process ID reported by GET /api/info.
func (c *Client) ServerPID(ctx context.Context) (int64, error) {
	var body struct {
		PID *int64 `json:"pid"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/info", nil, &body); err != nil {
		return 0, err
	}
	if body.PID == nil || *body.PID <= 0 {
		return 0, errors.New("opencode: GET /api/info: missing or invalid pid")
	}
	return *body.PID, nil
}

// ActiveParents returns the IDs of running root sessions (no parentID). Any
// malformed entry or failed lookup fails the whole call.
func (c *Client) ActiveParents(ctx context.Context) (map[string]struct{}, error) {
	var body struct {
		Data *map[string]struct {
			Type string `json:"type"`
		} `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/session/active", nil, &body); err != nil {
		return nil, err
	}
	if body.Data == nil {
		return nil, errors.New("opencode: GET /api/session/active: missing data")
	}
	parents := make(map[string]struct{})
	for id, state := range *body.Data {
		if !strings.HasPrefix(id, "ses") {
			return nil, fmt.Errorf("opencode: active session has invalid id %q", id)
		}
		if state.Type != "running" {
			return nil, fmt.Errorf("opencode: active session %s has unknown state %q", id, state.Type)
		}
		s, err := c.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if s.ParentID == "" {
			parents[id] = struct{}{}
		}
	}
	return parents, nil
}

// Get returns the session with the given ID.
func (c *Client) Get(ctx context.Context, id string) (Session, error) {
	if err := checkID("session", id, "ses"); err != nil {
		return Session{}, err
	}
	var body struct {
		Data *sessionInfo `json:"data"`
	}
	path := "/api/session/" + url.PathEscape(id)
	if err := c.do(ctx, http.MethodGet, path, nil, &body); err != nil {
		return Session{}, err
	}
	s, err := body.Data.session()
	if err != nil {
		return Session{}, fmt.Errorf("opencode: GET %s: %w", path, err)
	}
	if s.ID != id {
		return Session{}, fmt.Errorf("opencode: GET %s: returned session %q", path, s.ID)
	}
	return s, nil
}

// Create creates a root session with a caller-chosen ID in directory. The
// server returns an existing session with the same ID unchanged, so retries
// are safe; the result must match the request.
func (c *Client) Create(ctx context.Context, id, title, directory string) (Session, error) {
	if err := checkID("session", id, "ses"); err != nil {
		return Session{}, err
	}
	if !filepath.IsAbs(directory) {
		return Session{}, fmt.Errorf("opencode: session directory %q is not absolute", directory)
	}
	directory = filepath.Clean(directory)
	req := struct {
		ID       string      `json:"id"`
		Title    string      `json:"title"`
		Location locationRef `json:"location"`
	}{id, title, locationRef{Directory: directory}}
	var body struct {
		Data *sessionInfo `json:"data"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/session", req, &body); err != nil {
		return Session{}, err
	}
	s, err := body.Data.session()
	if err != nil {
		return Session{}, fmt.Errorf("opencode: POST /api/session: %w", err)
	}
	switch {
	case s.ID != id:
		return Session{}, fmt.Errorf("opencode: POST /api/session: returned session %q, want %q", s.ID, id)
	case s.ParentID != "":
		return Session{}, fmt.Errorf("opencode: POST /api/session: session %s is a child of %s", id, s.ParentID)
	case filepath.Clean(s.Directory) != directory:
		return Session{}, fmt.Errorf("opencode: POST /api/session: session %s is in %q, want %q", id, s.Directory, directory)
	}
	return s, nil
}

// Prompt sends text as a user message with a caller-chosen ID. Resending the
// same ID returns the original message, so retries are safe.
func (c *Client) Prompt(ctx context.Context, sessionID, messageID, text string) error {
	if err := checkID("session", sessionID, "ses"); err != nil {
		return err
	}
	if err := checkID("message", messageID, "msg_"); err != nil {
		return err
	}
	req := struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	}{messageID, text}
	var body struct {
		Data *struct {
			ID        string `json:"id"`
			SessionID string `json:"sessionID"`
			Type      string `json:"type"`
		} `json:"data"`
	}
	path := "/api/session/" + url.PathEscape(sessionID) + "/prompt"
	if err := c.do(ctx, http.MethodPost, path, req, &body); err != nil {
		return err
	}
	if d := body.Data; d == nil || d.ID != messageID || d.SessionID != sessionID || d.Type != "user" {
		return fmt.Errorf("opencode: POST %s: response does not confirm user message %s", path, messageID)
	}
	return nil
}

// Inbox returns the IDs of items queued for the session and not yet delivered.
func (c *Client) Inbox(ctx context.Context, sessionID string) ([]string, error) {
	if err := checkID("session", sessionID, "ses"); err != nil {
		return nil, err
	}
	var body struct {
		Data *[]struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	path := "/api/session/" + url.PathEscape(sessionID) + "/inbox"
	if err := c.do(ctx, http.MethodGet, path, nil, &body); err != nil {
		return nil, err
	}
	if body.Data == nil {
		return nil, fmt.Errorf("opencode: GET %s: missing data", path)
	}
	ids := make([]string, 0, len(*body.Data))
	for _, item := range *body.Data {
		if !strings.HasPrefix(item.ID, "msg_") {
			return nil, fmt.Errorf("opencode: GET %s: invalid inbox id %q", path, item.ID)
		}
		ids = append(ids, item.ID)
	}
	return ids, nil
}

// MessageCreatedAt returns the creation time (Unix ms) of a delivered user
// message. A message still queued in the inbox is a 404; check IsNotFound.
func (c *Client) MessageCreatedAt(ctx context.Context, sessionID, messageID string) (int64, error) {
	if err := checkID("session", sessionID, "ses"); err != nil {
		return 0, err
	}
	if err := checkID("message", messageID, "msg_"); err != nil {
		return 0, err
	}
	var body struct {
		Data *struct {
			ID   string `json:"id"`
			Type string `json:"type"`
			Time *struct {
				Created *millis `json:"created"`
			} `json:"time"`
		} `json:"data"`
	}
	path := "/api/session/" + url.PathEscape(sessionID) + "/message/" + url.PathEscape(messageID)
	if err := c.do(ctx, http.MethodGet, path, nil, &body); err != nil {
		return 0, err
	}
	d := body.Data
	if d == nil || d.ID != messageID || d.Type != "user" || d.Time == nil || d.Time.Created == nil {
		return 0, fmt.Errorf("opencode: GET %s: response is not user message %s", path, messageID)
	}
	return int64(*d.Time.Created), nil
}

type locationRef struct {
	Directory string `json:"directory"`
}

type sessionInfo struct {
	ID       string `json:"id"`
	ParentID string `json:"parentID"`
	Title    string `json:"title"`
	Outcome  string `json:"outcome"`
	Time     *struct {
		Created *millis `json:"created"`
		Updated *millis `json:"updated"`
		Busy    millis  `json:"busy"`
		Idle    millis  `json:"idle"`
	} `json:"time"`
	Location *locationRef `json:"location"`
}

func (s *sessionInfo) session() (Session, error) {
	switch {
	case s == nil:
		return Session{}, errors.New("missing data")
	case !strings.HasPrefix(s.ID, "ses"):
		return Session{}, fmt.Errorf("invalid session id %q", s.ID)
	case s.ParentID != "" && !strings.HasPrefix(s.ParentID, "ses"):
		return Session{}, fmt.Errorf("invalid parent id %q", s.ParentID)
	case s.Time == nil || s.Time.Created == nil || s.Time.Updated == nil:
		return Session{}, errors.New("missing session time")
	case s.Location == nil || s.Location.Directory == "":
		return Session{}, errors.New("missing session location")
	}
	switch s.Outcome {
	case "", "succeeded", "failed", "interrupted":
	default:
		return Session{}, fmt.Errorf("unknown session outcome %q", s.Outcome)
	}
	return Session{
		ID:        s.ID,
		ParentID:  s.ParentID,
		Title:     s.Title,
		Directory: s.Location.Directory,
		Outcome:   s.Outcome,
		CreatedAt: int64(*s.Time.Created),
		UpdatedAt: int64(*s.Time.Updated),
		BusyAt:    int64(s.Time.Busy),
		IdleAt:    int64(s.Time.Idle),
	}, nil
}

// millis decodes a JSON number of Unix milliseconds, truncating fractions.
type millis int64

func (m *millis) UnmarshalJSON(b []byte) error {
	var f float64
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	if f < 0 || f > 1<<53 {
		return fmt.Errorf("timestamp %v out of range", f)
	}
	*m = millis(f)
	return nil
}

func checkID(kind, id, prefix string) error {
	if !strings.HasPrefix(id, prefix) {
		return fmt.Errorf("opencode: %s id %q must start with %q", kind, id, prefix)
	}
	return nil
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var reqBody io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		reqBody = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base.String()+path, reqBody)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.password != "" {
		req.SetBasicAuth(basicAuthUser, c.password)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("opencode: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("opencode: %s %s: reading response: %w", method, path, err)
	}
	if len(data) > maxResponseBytes {
		return fmt.Errorf("opencode: %s %s: response exceeds %d bytes", method, path, maxResponseBytes)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		se := &StatusError{Method: method, Path: path, StatusCode: resp.StatusCode}
		var e struct {
			Tag     string `json:"_tag"`
			Message string `json:"message"`
		}
		if json.Unmarshal(data, &e) == nil {
			se.Tag, se.Message = truncate(e.Tag, 100), truncate(e.Message, 300)
		}
		return se
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("opencode: %s %s: malformed response: %w", method, path, err)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
