// Package localhttp implements the loopback-only HTTP transport for the
// Gilir local UI. It serves a JSON command API and, when provided, a static
// web bundle.
package localhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/armedi/gilir/internal/taskid"
)

const (
	apiCommandPath         = "/api/v1/command"
	maxBodyBytes           = 1 << 20
	maxStaticFallbackBytes = 8 << 20
	defaultActor           = "user"

	codeInternal             = "internal_error"
	codeInvalidHost          = "invalid_host"
	codeInvalidOrigin        = "invalid_origin"
	codeMethodNotAllowed     = "method_not_allowed"
	codeUnsupportedMediaType = "unsupported_media_type"
	codeRequestTooLarge      = "request_too_large"
	codeInvalidJSON          = "invalid_json"
	codeInvalidRequest       = "invalid_request"
	codeUnknownField         = "unknown_field"
	codeInvalidCommand       = "invalid_command"
	codeInvalidActor         = "invalid_actor"
	codeInvalidArgs          = "invalid_args"

	staticContentSecurityPolicy = "default-src 'self'; script-src 'self'; " +
		"style-src 'self'; img-src 'self' data:; font-src 'self'; " +
		"connect-src 'self'; object-src 'none'; base-uri 'none'; " +
		"frame-ancestors 'none'; form-action 'self'"
)

type handler struct {
	port     string
	web      fs.FS
	execute  func(context.Context, string, string, json.RawMessage) (any, error)
	classify func(error) (int, string)
}

type successEnvelope struct {
	Data any `json:"data"`
}

type errorEnvelope struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Handler returns the HTTP handler for the local UI. port is normalized to
// its canonical decimal form; a leading colon is allowed. Requests are
// accepted only when Host is 127.0.0.1, [::1], or localhost at that port.
// When the canonical port is 80, the port may be omitted from Host. When an
// Origin header is present it must be exactly http:// followed by the request
// Host; no CORS headers are emitted.
//
// execute is called for valid POST /api/v1/command requests with the command,
// the actor (default "user"), and the JSON args object (default {}). classify
// maps execution errors to an HTTP status and a lower snake case error code;
// 5xx responses always use a generic message. web may be nil; when set its
// regular files are served read-only for GET and HEAD with directory listings
// disabled. web must confine path resolution to a trusted root; main uses
// os.OpenRoot. Plain os.DirFS can follow symlinks outside the served root.
func Handler(port string, web fs.FS, execute func(context.Context, string, string, json.RawMessage) (any, error), classify func(error) (int, string)) http.Handler {
	return &handler{
		port:     canonicalPort(port),
		web:      web,
		execute:  execute,
		classify: classify,
	}
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == apiCommandPath {
		h.serveAPI(w, r)
		return
	}
	h.serveStatic(w, r)
}

func (h *handler) serveAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	if !h.validHost(r.Host) {
		writeAPIError(w, r, http.StatusForbidden, codeInvalidHost, "invalid host")
		return
	}
	if !validOrigin(r) {
		writeAPIError(w, r, http.StatusForbidden, codeInvalidOrigin, "invalid origin")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeAPIError(w, r, http.StatusMethodNotAllowed, codeMethodNotAllowed, "method not allowed")
		return
	}
	if !isJSONContentType(r) {
		writeAPIError(w, r, http.StatusUnsupportedMediaType, codeUnsupportedMediaType, "content type must be application/json")
		return
	}

	fields, ok := readFields(w, r)
	if !ok {
		return
	}

	var unknown []string
	for key := range fields {
		switch key {
		case "command", "actor", "args":
		default:
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		writeAPIError(w, r, http.StatusBadRequest, codeUnknownField, fmt.Sprintf("unknown field %q", unknown[0]))
		return
	}

	rawCommand, ok := fields["command"]
	if !ok {
		writeAPIError(w, r, http.StatusBadRequest, codeInvalidCommand, "command is required")
		return
	}
	command, ok := decodeString(rawCommand)
	if !ok || strings.TrimSpace(command) == "" {
		writeAPIError(w, r, http.StatusBadRequest, codeInvalidCommand, "command must be a non-empty string")
		return
	}

	actor := defaultActor
	if rawActor, present := fields["actor"]; present {
		actor, ok = decodeString(rawActor)
		if !ok || strings.TrimSpace(actor) == "" {
			writeAPIError(w, r, http.StatusBadRequest, codeInvalidActor, "actor must be a non-empty string")
			return
		}
	}

	args := json.RawMessage(`{}`)
	if rawArgs, present := fields["args"]; present {
		trimmed := bytes.TrimSpace(rawArgs)
		if len(trimmed) == 0 || trimmed[0] != '{' {
			writeAPIError(w, r, http.StatusBadRequest, codeInvalidArgs, "args must be a JSON object")
			return
		}
		args = append(json.RawMessage(nil), trimmed...)
	}

	if h.execute == nil {
		writeAPIError(w, r, http.StatusInternalServerError, codeInternal, "internal server error")
		return
	}
	result, err := h.execute(r.Context(), command, actor, args)
	if err != nil {
		status, code, expose := h.classifyError(err)
		message := "internal server error"
		if expose {
			message = err.Error()
			if message == "" {
				message = http.StatusText(status)
			}
		}
		writeAPIError(w, r, status, code, message)
		return
	}

	body, err := json.Marshal(successEnvelope{Data: result})
	if err != nil {
		writeAPIError(w, r, http.StatusInternalServerError, codeInternal, "internal server error")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (h *handler) serveStatic(w http.ResponseWriter, r *http.Request) {
	setStaticHeaders(w.Header())

	if !h.validHost(r.Host) {
		writePlainError(w, r, http.StatusForbidden, "forbidden")
		return
	}
	if !validOrigin(r) {
		writePlainError(w, r, http.StatusForbidden, "forbidden")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writePlainError(w, r, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.web == nil {
		writeStaticNotFound(w, r)
		return
	}

	name, ok := h.staticFile(r.URL.Path)
	if !ok {
		writeStaticNotFound(w, r)
		return
	}
	file, err := h.web.Open(name)
	if err != nil {
		writePlainError(w, r, http.StatusInternalServerError, "internal server error")
		return
	}
	defer file.Close()

	openedInfo, err := file.Stat()
	if err != nil {
		writePlainError(w, r, http.StatusInternalServerError, "internal server error")
		return
	}
	if !openedInfo.Mode().IsRegular() {
		writeStaticNotFound(w, r)
		return
	}
	if seeker, ok := file.(io.ReadSeeker); ok {
		http.ServeContent(w, r, name, openedInfo.ModTime(), seeker)
		return
	}
	if openedInfo.Size() > maxStaticFallbackBytes {
		writePlainError(w, r, http.StatusInternalServerError, "internal server error")
		return
	}
	data, err := io.ReadAll(io.LimitReader(file, maxStaticFallbackBytes+1))
	if err != nil || int64(len(data)) > maxStaticFallbackBytes {
		writePlainError(w, r, http.StatusInternalServerError, "internal server error")
		return
	}
	http.ServeContent(w, r, name, openedInfo.ModTime(), bytes.NewReader(data))
}

func (h *handler) validHost(authority string) bool {
	if h.port == "" {
		return false
	}
	host, port, err := net.SplitHostPort(authority)
	if err != nil {
		return h.port == "80" && isBareLoopbackAuthority(authority)
	}
	return port == h.port && isLoopbackHost(host)
}

func isLoopbackHost(host string) bool {
	if host == "127.0.0.1" || host == "::1" {
		return true
	}
	return strings.EqualFold(host, "localhost")
}

func isBareLoopbackAuthority(authority string) bool {
	if authority == "127.0.0.1" || authority == "[::1]" {
		return true
	}
	return strings.EqualFold(authority, "localhost")
}

func (h *handler) classifyError(err error) (status int, code string, expose bool) {
	status = http.StatusInternalServerError
	rawCode := ""
	if h.classify != nil {
		status, rawCode = h.classify(err)
	}
	if status < 400 || status > 599 {
		status = http.StatusInternalServerError
		rawCode = ""
	}
	if status >= 500 {
		code = normalizeCode(rawCode)
		if code == "" {
			code = codeInternal
		}
		return status, code, false
	}
	code = normalizeCode(rawCode)
	if code == "" {
		code = normalizeCode(http.StatusText(status))
	}
	if code == "" {
		code = "request_failed"
	}
	return status, code, true
}

func canonicalPort(port string) string {
	port = strings.TrimPrefix(port, ":")
	if port == "" {
		return ""
	}
	for i := 0; i < len(port); i++ {
		if port[i] < '0' || port[i] > '9' {
			return ""
		}
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return ""
	}
	return strconv.Itoa(n)
}

func validOrigin(r *http.Request) bool {
	values := r.Header.Values("Origin")
	if len(values) == 0 {
		return true
	}
	return len(values) == 1 && values[0] == "http://"+r.Host
}

func isJSONContentType(r *http.Request) bool {
	values := r.Header.Values("Content-Type")
	if len(values) != 1 {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(values[0])
	if err != nil {
		return false
	}
	return strings.EqualFold(mediaType, "application/json")
}

func readFields(w http.ResponseWriter, r *http.Request) (map[string]json.RawMessage, bool) {
	if r.ContentLength > maxBodyBytes {
		writeAPIError(w, r, http.StatusRequestEntityTooLarge, codeRequestTooLarge, "request body too large")
		return nil, false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeAPIError(w, r, http.StatusRequestEntityTooLarge, codeRequestTooLarge, "request body too large")
			return nil, false
		}
		writeAPIError(w, r, http.StatusBadRequest, codeInvalidJSON, "could not read request body")
		return nil, false
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		writeAPIError(w, r, http.StatusBadRequest, codeInvalidJSON, "request body is empty")
		return nil, false
	}
	if !json.Valid(body) {
		writeAPIError(w, r, http.StatusBadRequest, codeInvalidJSON, "request body must be valid JSON")
		return nil, false
	}
	if body[0] != '{' {
		writeAPIError(w, r, http.StatusBadRequest, codeInvalidRequest, "request body must be a JSON object")
		return nil, false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		writeAPIError(w, r, http.StatusBadRequest, codeInvalidJSON, "request body must be valid JSON")
		return nil, false
	}
	return fields, true
}

func decodeString(raw json.RawMessage) (string, bool) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

func normalizeCode(code string) string {
	var b strings.Builder
	underscore := false
	for _, r := range strings.ToLower(code) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			underscore = false
		default:
			if b.Len() > 0 && !underscore {
				b.WriteByte('_')
				underscore = true
			}
		}
	}
	return strings.TrimRight(b.String(), "_")
}

func staticName(urlPath string) (string, bool) {
	if urlPath == "" || urlPath == "/" {
		return "index.html", true
	}
	if !strings.HasPrefix(urlPath, "/") {
		return "", false
	}
	name := urlPath[1:]
	if !fs.ValidPath(name) || strings.ContainsRune(name, '\\') {
		return "", false
	}
	return name, true
}

// staticFile resolves a request path to a bundle file. Page routes are
// client-side destinations: a direct load or refresh asks for the route's
// path, which is not a file in the bundle, so the entry point answers and the
// router renders the view. Unknown assets still 404: only recognized page
// shapes fall back, never arbitrary paths, directories, or files with an
// extension.
func (h *handler) staticFile(urlPath string) (string, bool) {
	name, ok := staticName(urlPath)
	if ok && h.hasRegularFile(name) {
		return name, true
	}
	if !staticPagePath(urlPath) || !h.hasRegularFile("index.html") {
		return "", false
	}
	return "index.html", true
}

// staticPagePath reports whether urlPath is a client-side page destination
// rather than a bundle file. Only the shapes the dashboard routes own are
// recognized so a request for a missing asset (anything with an extension,
// anything under a bundle directory) still 404s instead of answering with the
// entry point. A trailing slash is accepted because the browser may add one,
// and fixed segments are matched case-insensitively to mirror the client
// router.
func staticPagePath(urlPath string) bool {
	// The client matcher rejects empty interior segments, so `//` (or any
	// doubled slash) must not become an HTML response.
	if strings.Contains(urlPath, "//") {
		return false
	}
	for len(urlPath) > 1 && strings.HasSuffix(urlPath, "/") {
		urlPath = strings.TrimSuffix(urlPath, "/")
	}
	switch strings.ToLower(urlPath) {
	case "/", "/tasks/new", "/projects", "/projects/repos/new":
		return true
	}
	// /tasks/:taskId — only canonical IDs (case-insensitive prefix, positive
	// decimal, int64 range) are pages. Anything else is an unknown path, so a
	// direct load cannot turn an arbitrary segment into an HTML response.
	// taskid.Parse trims surrounding space; the client rejects it, so require
	// the literal segment to match too.
	id := ""
	switch {
	case len(urlPath) >= len("/tasks/") && strings.EqualFold(urlPath[:len("/tasks/")], "/tasks/"):
		id = urlPath[len("/tasks/"):]
	default:
		return false
	}
	if id != strings.TrimSpace(id) {
		return false
	}
	_, err := taskid.Parse(id)
	return err == nil
}

func (h *handler) hasRegularFile(name string) bool {
	info, err := fs.Stat(h.web, name)
	return err == nil && info.Mode().IsRegular()
}

func setStaticHeaders(header http.Header) {
	header.Set("Content-Security-Policy", staticContentSecurityPolicy)
	header.Set("X-Frame-Options", "DENY")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Cross-Origin-Resource-Policy", "same-origin")
}

func writeStaticNotFound(w http.ResponseWriter, r *http.Request) {
	message := "not found"
	if r.URL.Path == "" || r.URL.Path == "/" || staticPagePath(r.URL.Path) {
		message = "web interface not available"
	}
	writePlainError(w, r, http.StatusNotFound, message)
}

func writePlainError(w http.ResponseWriter, r *http.Request, status int, message string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = io.WriteString(w, message+"\n")
	}
}

func writeAPIError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	body, _ := json.Marshal(errorEnvelope{Error: errorDetail{Code: code, Message: message}})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}
