package localhttp

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
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

const (
	testPort   = "8080"
	testHost   = "localhost:8080"
	testAPI    = "/api/v1/command"
	testOrigin = "http://localhost:8080"
)

func serveRequest(handler http.Handler, request *http.Request) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func commandRequest(body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, testAPI, strings.NewReader(body))
	request.Host = testHost
	request.Header.Set("Content-Type", "application/json")
	return request
}

func pageRequest(method, target string) *http.Request {
	request := httptest.NewRequest(method, target, nil)
	request.Host = testHost
	return request
}

func unexpectedExecute(t *testing.T) func(context.Context, string, string, json.RawMessage) (any, error) {
	t.Helper()
	return func(context.Context, string, string, json.RawMessage) (any, error) {
		t.Error("execute was called for a static request")
		return nil, nil
	}
}

func decodeObject(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("response body is not a JSON object: %v (body %q)", err, recorder.Body.String())
	}
	return body
}

func assertNoCORS(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	for _, header := range []string{
		"Access-Control-Allow-Origin",
		"Access-Control-Allow-Methods",
		"Access-Control-Allow-Headers",
		"Access-Control-Allow-Credentials",
		"Access-Control-Expose-Headers",
	} {
		if got := recorder.Header().Get(header); got != "" {
			t.Errorf("%s = %q, want no CORS headers", header, got)
		}
	}
}

func assertAPIError(t *testing.T, recorder *httptest.ResponseRecorder, wantStatus int, wantCode string) map[string]any {
	t.Helper()
	if recorder.Code != wantStatus {
		t.Fatalf("status = %d, want %d (body %q)", recorder.Code, wantStatus, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if got := recorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	assertNoCORS(t, recorder)

	body := decodeObject(t, recorder)
	errorValue, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("response has no error object: %q", recorder.Body.String())
	}
	if got, _ := errorValue["code"].(string); got != wantCode {
		t.Errorf("error code = %q, want %q", got, wantCode)
	}
	if message, _ := errorValue["message"].(string); message == "" {
		t.Error("error message is empty")
	}
	return body
}

func assertAPISuccess(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %q)", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if got := recorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	assertNoCORS(t, recorder)

	body := decodeObject(t, recorder)
	if _, ok := body["data"]; !ok {
		t.Fatalf("response has no data field: %q", recorder.Body.String())
	}
	if _, ok := body["error"]; ok {
		t.Fatalf("success response has an error field: %q", recorder.Body.String())
	}
	return body
}

func assertStaticHeaders(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	csp := recorder.Header().Get("Content-Security-Policy")
	for _, want := range []string{
		"default-src 'self'",
		"script-src 'self'",
		"connect-src 'self'",
		"object-src 'none'",
		"frame-ancestors 'none'",
	} {
		if !strings.Contains(csp, want) {
			t.Errorf("Content-Security-Policy = %q, want %q", csp, want)
		}
	}
	for _, disallowed := range []string{"unsafe-inline", "unsafe-eval"} {
		if strings.Contains(csp, disallowed) {
			t.Errorf("Content-Security-Policy = %q, must not allow %q", csp, disallowed)
		}
	}
	if got := recorder.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q, want DENY", got)
	}
	if got := recorder.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy = %q, want no-referrer", got)
	}
	if got := recorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := recorder.Header().Get("Cross-Origin-Resource-Policy"); got != "same-origin" {
		t.Errorf("Cross-Origin-Resource-Policy = %q, want same-origin", got)
	}
	assertNoCORS(t, recorder)
}

func TestCommandAPISuccess(t *testing.T) {
	tests := []struct {
		name        string
		port        string
		host        string
		origin      string
		body        string
		wantCommand string
		wantActor   string
		wantArgs    string
	}{
		{
			name:        "defaults on localhost",
			port:        testPort,
			host:        testHost,
			body:        `{"command":"status"}`,
			wantCommand: "status",
			wantActor:   "user",
			wantArgs:    "{}",
		},
		{
			name:        "explicit actor and args on ipv4",
			port:        testPort,
			host:        "127.0.0.1:8080",
			body:        `{"command":"run","actor":"alice","args":{"path":"a"}}`,
			wantCommand: "run",
			wantActor:   "alice",
			wantArgs:    `{"path":"a"}`,
		},
		{
			name:        "matching origin on ipv6",
			port:        ":8080",
			host:        "[::1]:8080",
			origin:      "http://[::1]:8080",
			body:        `{"command":"status","args":{}}`,
			wantCommand: "status",
			wantActor:   "user",
			wantArgs:    "{}",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			type contextKey struct{}
			var gotCommand, gotActor, gotArgs, gotContext string
			execute := func(ctx context.Context, command, actor string, args json.RawMessage) (any, error) {
				gotCommand = command
				gotActor = actor
				gotArgs = string(args)
				gotContext, _ = ctx.Value(contextKey{}).(string)
				return map[string]any{"result": "ok"}, nil
			}

			handler := Handler(tt.port, nil, execute, nil)
			request := commandRequest(tt.body)
			request.Host = tt.host
			request = request.WithContext(context.WithValue(request.Context(), contextKey{}, "live"))
			if tt.origin != "" {
				request.Header.Set("Origin", tt.origin)
			}

			recorder := serveRequest(handler, request)
			body := assertAPISuccess(t, recorder)
			if want := map[string]any{"result": "ok"}; !reflect.DeepEqual(body["data"], want) {
				t.Errorf("data = %#v, want %#v", body["data"], want)
			}
			if gotCommand != tt.wantCommand {
				t.Errorf("command = %q, want %q", gotCommand, tt.wantCommand)
			}
			if gotActor != tt.wantActor {
				t.Errorf("actor = %q, want %q", gotActor, tt.wantActor)
			}
			if gotArgs != tt.wantArgs {
				t.Errorf("args = %q, want %q", gotArgs, tt.wantArgs)
			}
			if gotContext != "live" {
				t.Errorf("execute context value = %q, want live", gotContext)
			}
		})
	}
}

func TestCommandAPIHostValidation(t *testing.T) {
	hosts := []struct {
		name string
		host string
	}{
		{name: "external host", host: "example.com:8080"},
		{name: "other loopback address", host: "127.0.0.2:8080"},
		{name: "wrong port", host: "localhost:9090"},
		{name: "missing port", host: "localhost"},
		{name: "trailing dot", host: "localhost.:8080"},
		{name: "userinfo trick", host: "localhost:8080@evil.example:8080"},
		{name: "other ipv6 address", host: "[::2]:8080"},
	}

	for _, tt := range hosts {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			handler := Handler(testPort, nil, func(context.Context, string, string, json.RawMessage) (any, error) {
				called = true
				return nil, nil
			}, nil)
			request := commandRequest(`{"command":"status"}`)
			request.Host = tt.host
			recorder := serveRequest(handler, request)
			assertAPIError(t, recorder, http.StatusForbidden, "invalid_host")
			if called {
				t.Error("execute was called for a rejected Host")
			}
		})
	}
}

func TestCommandAPIPortNormalizationAndDefaultPort(t *testing.T) {
	tests := []struct {
		name       string
		port       string
		host       string
		origin     string
		wantStatus int
		wantCode   string
	}{
		{
			name:       "leading zeros in configured port",
			port:       "0008080",
			host:       "localhost:8080",
			wantStatus: http.StatusOK,
		},
		{
			name:       "leading colon and zeros",
			port:       ":008080",
			host:       "127.0.0.1:8080",
			wantStatus: http.StatusOK,
		},
		{
			name:       "port 80 omitted on localhost",
			port:       "080",
			host:       "localhost",
			origin:     "http://localhost",
			wantStatus: http.StatusOK,
		},
		{
			name:       "port 80 omitted on ipv4",
			port:       "00080",
			host:       "127.0.0.1",
			wantStatus: http.StatusOK,
		},
		{
			name:       "port 80 omitted on ipv6",
			port:       "80",
			host:       "[::1]",
			origin:     "http://[::1]",
			wantStatus: http.StatusOK,
		},
		{
			name:       "port 80 explicit",
			port:       "80",
			host:       "localhost:80",
			wantStatus: http.StatusOK,
		},
		{
			name:       "omitted port rejected for non-80",
			port:       "8080",
			host:       "localhost",
			wantStatus: http.StatusForbidden,
			wantCode:   "invalid_host",
		},
		{
			name:       "empty host port rejected",
			port:       "80",
			host:       "localhost:",
			wantStatus: http.StatusForbidden,
			wantCode:   "invalid_host",
		},
		{
			name:       "unbracketed ipv6 rejected",
			port:       "80",
			host:       "::1",
			wantStatus: http.StatusForbidden,
			wantCode:   "invalid_host",
		},
		{
			name:       "bare external host rejected",
			port:       "80",
			host:       "example.com",
			wantStatus: http.StatusForbidden,
			wantCode:   "invalid_host",
		},
		{
			name:       "noncanonical host port rejected",
			port:       "00080",
			host:       "localhost:0080",
			wantStatus: http.StatusForbidden,
			wantCode:   "invalid_host",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			handler := Handler(tt.port, nil, func(context.Context, string, string, json.RawMessage) (any, error) {
				called = true
				return map[string]any{"ok": true}, nil
			}, nil)
			request := commandRequest(`{"command":"status"}`)
			request.Host = tt.host
			if tt.origin != "" {
				request.Header.Set("Origin", tt.origin)
			}

			recorder := serveRequest(handler, request)
			if tt.wantStatus == http.StatusOK {
				assertAPISuccess(t, recorder)
				if !called {
					t.Error("execute was not called for an accepted Host")
				}
				return
			}
			assertAPIError(t, recorder, tt.wantStatus, tt.wantCode)
			if called {
				t.Error("execute was called for a rejected Host")
			}
		})
	}
}

func TestCommandAPIOriginValidation(t *testing.T) {
	tests := []struct {
		name    string
		origins []string
	}{
		{name: "https origin", origins: []string{"https://localhost:8080"}},
		{name: "different port", origins: []string{"http://localhost:9090"}},
		{name: "different loopback alias", origins: []string{"http://127.0.0.1:8080"}},
		{name: "opaque origin", origins: []string{"null"}},
		{name: "trailing slash", origins: []string{"http://localhost:8080/"}},
		{name: "comma separated", origins: []string{"http://localhost:8080, http://localhost:8080"}},
		{name: "empty origin", origins: []string{""}},
		{name: "duplicate origin", origins: []string{testOrigin, testOrigin}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			handler := Handler(testPort, nil, func(context.Context, string, string, json.RawMessage) (any, error) {
				called = true
				return nil, nil
			}, nil)
			request := commandRequest(`{"command":"status"}`)
			for _, origin := range tt.origins {
				request.Header.Add("Origin", origin)
			}
			recorder := serveRequest(handler, request)
			assertAPIError(t, recorder, http.StatusForbidden, "invalid_origin")
			if called {
				t.Error("execute was called for a rejected Origin")
			}
		})
	}
}

func TestCommandAPIMethodAndMediaType(t *testing.T) {
	tests := []struct {
		name         string
		method       string
		contentTypes []string
		wantStatus   int
		wantCode     string
		wantAllow    string
	}{
		{
			name:         "get",
			method:       http.MethodGet,
			contentTypes: []string{"application/json"},
			wantStatus:   http.StatusMethodNotAllowed,
			wantCode:     "method_not_allowed",
			wantAllow:    "POST",
		},
		{
			name:         "options preflight",
			method:       http.MethodOptions,
			contentTypes: []string{"application/json"},
			wantStatus:   http.StatusMethodNotAllowed,
			wantCode:     "method_not_allowed",
			wantAllow:    "POST",
		},
		{
			name:       "missing content type",
			method:     http.MethodPost,
			wantStatus: http.StatusUnsupportedMediaType,
			wantCode:   "unsupported_media_type",
		},
		{
			name:         "plain text",
			method:       http.MethodPost,
			contentTypes: []string{"text/plain"},
			wantStatus:   http.StatusUnsupportedMediaType,
			wantCode:     "unsupported_media_type",
		},
		{
			name:         "json-looking media type",
			method:       http.MethodPost,
			contentTypes: []string{"application/jsonx"},
			wantStatus:   http.StatusUnsupportedMediaType,
			wantCode:     "unsupported_media_type",
		},
		{
			name:         "malformed media type",
			method:       http.MethodPost,
			contentTypes: []string{"application/json; charset"},
			wantStatus:   http.StatusUnsupportedMediaType,
			wantCode:     "unsupported_media_type",
		},
		{
			name:         "duplicate media type",
			method:       http.MethodPost,
			contentTypes: []string{"application/json", "application/json"},
			wantStatus:   http.StatusUnsupportedMediaType,
			wantCode:     "unsupported_media_type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			handler := Handler(testPort, nil, func(context.Context, string, string, json.RawMessage) (any, error) {
				called = true
				return nil, nil
			}, nil)
			request := httptest.NewRequest(tt.method, testAPI, strings.NewReader(`{"command":"status"}`))
			request.Host = testHost
			for _, contentType := range tt.contentTypes {
				request.Header.Add("Content-Type", contentType)
			}
			if tt.method == http.MethodOptions {
				request.Header.Set("Origin", testOrigin)
				request.Header.Set("Access-Control-Request-Method", http.MethodPost)
			}

			recorder := serveRequest(handler, request)
			assertAPIError(t, recorder, tt.wantStatus, tt.wantCode)
			if tt.wantAllow != "" {
				if got := recorder.Header().Get("Allow"); got != tt.wantAllow {
					t.Errorf("Allow = %q, want %q", got, tt.wantAllow)
				}
			}
			if called {
				t.Error("execute was called for a rejected request")
			}
		})
	}
}

func TestCommandAPIRequestValidation(t *testing.T) {
	oversized := `{"command":"` + strings.Repeat("a", 1<<20) + `"}`
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantCode   string
	}{
		{name: "empty body", body: "", wantStatus: http.StatusBadRequest, wantCode: "invalid_json"},
		{name: "whitespace body", body: " \n\t", wantStatus: http.StatusBadRequest, wantCode: "invalid_json"},
		{name: "malformed json", body: `{"command":`, wantStatus: http.StatusBadRequest, wantCode: "invalid_json"},
		{name: "trailing json", body: `{"command":"status"}{"command":"other"}`, wantStatus: http.StatusBadRequest, wantCode: "invalid_json"},
		{name: "top-level array", body: `[]`, wantStatus: http.StatusBadRequest, wantCode: "invalid_request"},
		{name: "top-level null", body: `null`, wantStatus: http.StatusBadRequest, wantCode: "invalid_request"},
		{name: "top-level string", body: `"status"`, wantStatus: http.StatusBadRequest, wantCode: "invalid_request"},
		{name: "missing command", body: `{}`, wantStatus: http.StatusBadRequest, wantCode: "invalid_command"},
		{name: "empty command", body: `{"command":""}`, wantStatus: http.StatusBadRequest, wantCode: "invalid_command"},
		{name: "blank command", body: `{"command":"  "}`, wantStatus: http.StatusBadRequest, wantCode: "invalid_command"},
		{name: "null command", body: `{"command":null}`, wantStatus: http.StatusBadRequest, wantCode: "invalid_command"},
		{name: "non-string command", body: `{"command":7}`, wantStatus: http.StatusBadRequest, wantCode: "invalid_command"},
		{name: "empty actor", body: `{"command":"status","actor":""}`, wantStatus: http.StatusBadRequest, wantCode: "invalid_actor"},
		{name: "blank actor", body: `{"command":"status","actor":"  "}`, wantStatus: http.StatusBadRequest, wantCode: "invalid_actor"},
		{name: "null actor", body: `{"command":"status","actor":null}`, wantStatus: http.StatusBadRequest, wantCode: "invalid_actor"},
		{name: "non-string actor", body: `{"command":"status","actor":7}`, wantStatus: http.StatusBadRequest, wantCode: "invalid_actor"},
		{name: "null args", body: `{"command":"status","args":null}`, wantStatus: http.StatusBadRequest, wantCode: "invalid_args"},
		{name: "array args", body: `{"command":"status","args":[]}`, wantStatus: http.StatusBadRequest, wantCode: "invalid_args"},
		{name: "string args", body: `{"command":"status","args":"x"}`, wantStatus: http.StatusBadRequest, wantCode: "invalid_args"},
		{name: "unknown field", body: `{"command":"status","extra":true}`, wantStatus: http.StatusBadRequest, wantCode: "unknown_field"},
		{name: "oversized body", body: oversized, wantStatus: http.StatusRequestEntityTooLarge, wantCode: "request_too_large"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			handler := Handler(testPort, nil, func(context.Context, string, string, json.RawMessage) (any, error) {
				called = true
				return nil, nil
			}, nil)
			recorder := serveRequest(handler, commandRequest(tt.body))
			assertAPIError(t, recorder, tt.wantStatus, tt.wantCode)
			if called {
				t.Error("execute was called for an invalid request")
			}
		})
	}
}

func TestCommandAPIBodyLimitWithoutContentLength(t *testing.T) {
	oversized := `{"command":"` + strings.Repeat("a", 1<<20) + `"}`
	called := false
	handler := Handler(testPort, nil, func(context.Context, string, string, json.RawMessage) (any, error) {
		called = true
		return nil, nil
	}, nil)
	request := httptest.NewRequest(http.MethodPost, testAPI, struct{ io.Reader }{strings.NewReader(oversized)})
	request.Host = testHost
	request.Header.Set("Content-Type", "application/json")
	if request.ContentLength != -1 {
		t.Fatalf("ContentLength = %d, want -1 to exercise the streaming limit", request.ContentLength)
	}

	recorder := serveRequest(handler, request)
	assertAPIError(t, recorder, http.StatusRequestEntityTooLarge, "request_too_large")
	if called {
		t.Error("execute was called for an oversized request")
	}
}

func TestCommandAPIApplicationErrors(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		code        string
		execErr     string
		wantStatus  int
		wantCode    string
		wantMessage string
		generic     bool
	}{
		{
			name:        "invalid",
			status:      http.StatusBadRequest,
			code:        "invalid",
			execErr:     "value is invalid",
			wantStatus:  http.StatusBadRequest,
			wantCode:    "invalid",
			wantMessage: "value is invalid",
		},
		{
			name:        "empty message falls back to status text",
			status:      http.StatusBadRequest,
			code:        "invalid",
			execErr:     "",
			wantStatus:  http.StatusBadRequest,
			wantCode:    "invalid",
			wantMessage: "Bad Request",
		},
		{
			name:        "conflict",
			status:      http.StatusConflict,
			code:        "conflict",
			execErr:     "already exists",
			wantStatus:  http.StatusConflict,
			wantCode:    "conflict",
			wantMessage: "already exists",
		},
		{
			name:        "not found",
			status:      http.StatusNotFound,
			code:        "not_found",
			execErr:     "missing",
			wantStatus:  http.StatusNotFound,
			wantCode:    "not_found",
			wantMessage: "missing",
		},
		{
			name:        "internal",
			status:      http.StatusInternalServerError,
			code:        "internal_error",
			execErr:     "database password secret",
			wantStatus:  http.StatusInternalServerError,
			wantCode:    "internal_error",
			wantMessage: "internal server error",
			generic:     true,
		},
		{
			name:        "unavailable",
			status:      http.StatusServiceUnavailable,
			code:        "unavailable",
			execErr:     "network secret",
			wantStatus:  http.StatusServiceUnavailable,
			wantCode:    "unavailable",
			wantMessage: "internal server error",
			generic:     true,
		},
		{
			name:        "invalid status falls back",
			status:      http.StatusOK,
			code:        "ok",
			execErr:     "boom",
			wantStatus:  http.StatusInternalServerError,
			wantCode:    "internal_error",
			wantMessage: "internal server error",
			generic:     true,
		},
		{
			name:        "empty code falls back",
			status:      http.StatusConflict,
			code:        "",
			execErr:     "already exists",
			wantStatus:  http.StatusConflict,
			wantCode:    "conflict",
			wantMessage: "already exists",
		},
		{
			name:        "code is normalized",
			status:      http.StatusConflict,
			code:        "Conflict!",
			execErr:     "already exists",
			wantStatus:  http.StatusConflict,
			wantCode:    "conflict",
			wantMessage: "already exists",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			execErr := errors.New(tt.execErr)
			execute := func(context.Context, string, string, json.RawMessage) (any, error) {
				return map[string]any{"ignored": true}, execErr
			}
			classify := func(err error) (int, string) {
				if !errors.Is(err, execErr) {
					t.Errorf("classify received %v, want %v", err, execErr)
				}
				return tt.status, tt.code
			}

			handler := Handler(testPort, nil, execute, classify)
			recorder := serveRequest(handler, commandRequest(`{"command":"status"}`))
			body := assertAPIError(t, recorder, tt.wantStatus, tt.wantCode)
			errorValue := body["error"].(map[string]any)
			if got, _ := errorValue["message"].(string); got != tt.wantMessage {
				t.Errorf("message = %q, want %q", got, tt.wantMessage)
			}
			if tt.generic && strings.Contains(recorder.Body.String(), tt.execErr) {
				t.Errorf("5xx response exposed the raw error: %q", recorder.Body.String())
			}
		})
	}
}

func TestCommandAPISuccessMarshalFailure(t *testing.T) {
	handler := Handler(testPort, nil, func(context.Context, string, string, json.RawMessage) (any, error) {
		return func() {}, nil
	}, nil)
	recorder := serveRequest(handler, commandRequest(`{"command":"status"}`))
	assertAPIError(t, recorder, http.StatusInternalServerError, "internal_error")
	if strings.Contains(recorder.Body.String(), "unsupported type") {
		t.Errorf("response exposed serialization details: %q", recorder.Body.String())
	}
}

func TestStaticServing(t *testing.T) {
	web := fstest.MapFS{
		"index.html":       &fstest.MapFile{Data: []byte("<!doctype html><title>gilir</title>")},
		"app.js":           &fstest.MapFile{Data: []byte("console.log('gilir')")},
		"assets/style.css": &fstest.MapFile{Data: []byte("body { color: black; }")},
		"nested/index.html": &fstest.MapFile{
			Data: []byte("nested index"),
		},
	}
	handler := Handler(testPort, web, unexpectedExecute(t), nil)

	t.Run("index", func(t *testing.T) {
		recorder := serveRequest(handler, pageRequest(http.MethodGet, "/"))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
		}
		assertStaticHeaders(t, recorder)
		if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
			t.Errorf("Content-Type = %q, want text/html", got)
		}
		if !strings.Contains(recorder.Body.String(), "gilir") {
			t.Errorf("body = %q, want index content", recorder.Body.String())
		}
	})

	t.Run("index head", func(t *testing.T) {
		recorder := serveRequest(handler, pageRequest(http.MethodHead, "/"))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
		}
		assertStaticHeaders(t, recorder)
		if recorder.Body.Len() != 0 {
			t.Errorf("HEAD body = %q, want empty", recorder.Body.String())
		}
	})

	t.Run("root asset", func(t *testing.T) {
		recorder := serveRequest(handler, pageRequest(http.MethodGet, "/app.js"))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
		}
		assertStaticHeaders(t, recorder)
		if got := recorder.Header().Get("Content-Type"); !strings.Contains(got, "javascript") {
			t.Errorf("Content-Type = %q, want a JavaScript type", got)
		}
		if got := recorder.Body.String(); got != "console.log('gilir')" {
			t.Errorf("body = %q, want app.js content", got)
		}
	})

	t.Run("nested asset", func(t *testing.T) {
		recorder := serveRequest(handler, pageRequest(http.MethodGet, "/assets/style.css"))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
		}
		assertStaticHeaders(t, recorder)
		if got := recorder.Header().Get("Content-Type"); !strings.Contains(got, "text/css") {
			t.Errorf("Content-Type = %q, want text/css", got)
		}
	})

	t.Run("directory has no listing", func(t *testing.T) {
		recorder := serveRequest(handler, pageRequest(http.MethodGet, "/assets/"))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNotFound)
		}
		assertStaticHeaders(t, recorder)
		if strings.Contains(recorder.Body.String(), "style.css") {
			t.Errorf("directory response listed files: %q", recorder.Body.String())
		}
	})

	t.Run("nested index is not served as a directory", func(t *testing.T) {
		recorder := serveRequest(handler, pageRequest(http.MethodGet, "/nested/"))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNotFound)
		}
		if strings.Contains(recorder.Body.String(), "nested index") {
			t.Errorf("directory response served nested index: %q", recorder.Body.String())
		}
	})

	t.Run("unknown path", func(t *testing.T) {
		recorder := serveRequest(handler, pageRequest(http.MethodGet, "/missing"))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNotFound)
		}
		assertStaticHeaders(t, recorder)
		if !strings.Contains(recorder.Body.String(), "not found") {
			t.Errorf("body = %q, want not found message", recorder.Body.String())
		}
	})

	t.Run("method not allowed", func(t *testing.T) {
		recorder := serveRequest(handler, pageRequest(http.MethodPost, "/"))
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusMethodNotAllowed)
		}
		assertStaticHeaders(t, recorder)
		if got := recorder.Header().Get("Allow"); got != "GET, HEAD" {
			t.Errorf("Allow = %q, want GET, HEAD", got)
		}
	})

	t.Run("head error has no body", func(t *testing.T) {
		recorder := serveRequest(handler, pageRequest(http.MethodHead, "/missing"))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNotFound)
		}
		if recorder.Body.Len() != 0 {
			t.Errorf("HEAD error body = %q, want empty", recorder.Body.String())
		}
	})
}

func TestStaticPageFallback(t *testing.T) {
	web := fstest.MapFS{
		"index.html":       &fstest.MapFile{Data: []byte("<!doctype html><title>gilir app</title>")},
		"app.js":           &fstest.MapFile{Data: []byte("console.log('gilir')")},
		"assets/style.css": &fstest.MapFile{Data: []byte("body { color: black; }")},
	}
	handler := Handler(testPort, web, unexpectedExecute(t), nil)

	pages := []string{
		"/tasks/GILIR-42",
		"/tasks/GILIR-42/",
		"/tasks/gilir-7",
		"/tasks/new",
		"/tasks/new/",
		"/projects",
		"/projects/",
		"/projects/repos/new",
		"/projects/repos/new/",
		"/Projects",
		"/TASKS/NEW",
		"/Tasks/GILIR-1",
	}
	for _, target := range pages {
		t.Run("page "+target, func(t *testing.T) {
			recorder := serveRequest(handler, pageRequest(http.MethodGet, target))
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
			}
			assertStaticHeaders(t, recorder)
			if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
				t.Errorf("Content-Type = %q, want text/html", got)
			}
			if !strings.Contains(recorder.Body.String(), "gilir app") {
				t.Errorf("body = %q, want index content", recorder.Body.String())
			}
		})
	}

	t.Run("page head", func(t *testing.T) {
		recorder := serveRequest(handler, pageRequest(http.MethodHead, "/tasks/GILIR-42"))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
		}
		if recorder.Body.Len() != 0 {
			t.Errorf("HEAD body = %q, want empty", recorder.Body.String())
		}
	})

	unknown := []string{
		"/missing",
		"/tasks",
		"/tasks/",
		"/tasks/not-a-task",
		"/tasks/GILIR-0",
		"/tasks/GILIR-042",
		"/tasks/42",
		"/tasks/GILIR-42/extra",
		"/tasks/missing.js",
		"/tasks/GILIR-9223372036854775808",
		"/tasks/GILIR-42%20",
		"/projects/missing",
		"/projects/repos",
		"/projects/repos/new/extra",
		"//",
		"//projects",
		"/assets/missing.js",
		"/api/other",
	}
	for _, target := range unknown {
		t.Run("not page "+target, func(t *testing.T) {
			recorder := serveRequest(handler, pageRequest(http.MethodGet, target))
			if recorder.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNotFound)
			}
			assertStaticHeaders(t, recorder)
			if strings.Contains(recorder.Body.String(), "gilir app") {
				t.Errorf("non-page path served the entry point: %q", recorder.Body.String())
			}
			if got := recorder.Body.String(); !strings.Contains(got, "not found") {
				t.Errorf("body = %q, want a not found message", got)
			}
		})
	}
}

func TestStaticPageFallbackWithoutIndex(t *testing.T) {
	web := fstest.MapFS{
		"app.js": &fstest.MapFile{Data: []byte("console.log('gilir')")},
	}
	handler := Handler(testPort, web, unexpectedExecute(t), nil)

	t.Run("page path", func(t *testing.T) {
		recorder := serveRequest(handler, pageRequest(http.MethodGet, "/tasks/GILIR-42"))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNotFound)
		}
		assertStaticHeaders(t, recorder)
		if strings.Contains(recorder.Body.String(), "console.log") {
			t.Errorf("missing index fell back to an asset: %q", recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), "web interface not available") {
			t.Errorf("body = %q, want unavailable message", recorder.Body.String())
		}
	})

	t.Run("nil web", func(t *testing.T) {
		recorder := serveRequest(
			Handler(testPort, nil, unexpectedExecute(t), nil),
			pageRequest(http.MethodGet, "/projects"),
		)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNotFound)
		}
		if !strings.Contains(recorder.Body.String(), "web interface not available") {
			t.Errorf("body = %q, want unavailable message", recorder.Body.String())
		}
	})
}

func TestStaticPageFallbackPrefersRealFile(t *testing.T) {
	web := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("entry point")},
		"projects":   &fstest.MapFile{Data: []byte("real projects file")},
	}
	handler := Handler(testPort, web, unexpectedExecute(t), nil)

	recorder := serveRequest(handler, pageRequest(http.MethodGet, "/projects"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if got := recorder.Body.String(); got != "real projects file" {
		t.Errorf("body = %q, want the real file to win over the page fallback", got)
	}
}

func TestStaticPageFallbackKeepsMethodAndHostGuards(t *testing.T) {
	web := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<!doctype html><title>gilir app</title>")},
	}
	handler := Handler(testPort, web, unexpectedExecute(t), nil)

	t.Run("post page path", func(t *testing.T) {
		recorder := serveRequest(handler, pageRequest(http.MethodPost, "/projects"))
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusMethodNotAllowed)
		}
		if got := recorder.Header().Get("Allow"); got != "GET, HEAD" {
			t.Errorf("Allow = %q, want GET, HEAD", got)
		}
	})

	t.Run("unexpected host", func(t *testing.T) {
		request := pageRequest(http.MethodGet, "/tasks/GILIR-42")
		request.Host = "example.com:8080"
		recorder := serveRequest(handler, request)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
		}
		if strings.Contains(recorder.Body.String(), "gilir app") {
			t.Errorf("page served for an unexpected Host: %q", recorder.Body.String())
		}
	})
}

func TestStaticRejectsNonRegularFiles(t *testing.T) {
	tests := []struct {
		name string
		mode fs.FileMode
	}{
		{name: "named pipe", mode: fs.ModeNamedPipe},
		{name: "device", mode: fs.ModeDevice},
		{name: "socket", mode: fs.ModeSocket},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			web := fstest.MapFS{
				"item": &fstest.MapFile{Data: []byte("non-regular payload"), Mode: tt.mode},
			}
			handler := Handler(testPort, web, unexpectedExecute(t), nil)
			recorder := serveRequest(handler, pageRequest(http.MethodGet, "/item"))
			if recorder.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNotFound)
			}
			assertStaticHeaders(t, recorder)
			if strings.Contains(recorder.Body.String(), "non-regular payload") {
				t.Errorf("non-regular file was served: %q", recorder.Body.String())
			}
		})
	}
}

func TestStaticSeekableRangeDoesNotReadWholeFile(t *testing.T) {
	data := bytes.Repeat([]byte("x"), 1<<16)
	web := &countingSeekableFS{
		FS: fstest.MapFS{
			"asset.txt": &fstest.MapFile{Data: data},
		},
	}
	handler := Handler(testPort, web, unexpectedExecute(t), nil)

	request := pageRequest(http.MethodGet, "/asset.txt")
	request.Header.Set("Range", "bytes=0-3")
	recorder := serveRequest(handler, request)
	if recorder.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusPartialContent)
	}
	if got := recorder.Body.String(); got != "xxxx" {
		t.Errorf("body = %q, want requested range", got)
	}
	if web.bytesRead >= len(data) {
		t.Errorf("read %d of %d bytes for a 4-byte range", web.bytesRead, len(data))
	}
}

func TestStaticNonSeekableFallback(t *testing.T) {
	web := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("fallback index")},
		"asset.txt":  &fstest.MapFile{Data: []byte("non-seekable asset")},
	}
	handler := Handler(testPort, nonSeekableFS{FS: web}, unexpectedExecute(t), nil)

	t.Run("get", func(t *testing.T) {
		recorder := serveRequest(handler, pageRequest(http.MethodGet, "/"))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
		}
		if !strings.Contains(recorder.Body.String(), "fallback index") {
			t.Errorf("body = %q, want index content", recorder.Body.String())
		}
	})

	t.Run("head", func(t *testing.T) {
		recorder := serveRequest(handler, pageRequest(http.MethodHead, "/asset.txt"))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
		}
		if recorder.Body.Len() != 0 {
			t.Errorf("HEAD body = %q, want empty", recorder.Body.String())
		}
	})

	t.Run("range", func(t *testing.T) {
		request := pageRequest(http.MethodGet, "/asset.txt")
		request.Header.Set("Range", "bytes=0-2")
		recorder := serveRequest(handler, request)
		if recorder.Code != http.StatusPartialContent {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusPartialContent)
		}
		if got := recorder.Body.String(); got != "non" {
			t.Errorf("body = %q, want requested range", got)
		}
	})

	t.Run("oversized", func(t *testing.T) {
		handler := Handler(testPort, oversizedStaticFS{}, unexpectedExecute(t), nil)
		recorder := serveRequest(handler, pageRequest(http.MethodGet, "/huge.bin"))
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
		}
		if !strings.Contains(recorder.Body.String(), "internal server error") {
			t.Errorf("body = %q, want generic error", recorder.Body.String())
		}
	})

	t.Run("under-reported size is bounded", func(t *testing.T) {
		web := &oversizedStreamFS{}
		handler := Handler(testPort, web, unexpectedExecute(t), nil)
		recorder := serveRequest(handler, pageRequest(http.MethodGet, "/huge.bin"))
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
		}
		if web.bytesRead <= maxStaticFallbackBytes {
			t.Errorf("read %d bytes, want the fallback limit to be exceeded", web.bytesRead)
		}
		if web.bytesRead > maxStaticFallbackBytes+1 {
			t.Errorf("read %d bytes, want at most %d", web.bytesRead, maxStaticFallbackBytes+1)
		}
	})
}

func TestStaticRequestGuards(t *testing.T) {
	web := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<h1>private ui</h1>")},
	}
	handler := Handler(testPort, web, unexpectedExecute(t), nil)

	t.Run("unexpected host", func(t *testing.T) {
		request := pageRequest(http.MethodGet, "/")
		request.Host = "example.com:8080"
		recorder := serveRequest(handler, request)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
		}
		assertStaticHeaders(t, recorder)
		if strings.Contains(recorder.Body.String(), "private ui") {
			t.Errorf("static content was served for an unexpected Host: %q", recorder.Body.String())
		}
	})

	t.Run("unexpected origin", func(t *testing.T) {
		request := pageRequest(http.MethodGet, "/")
		request.Header.Set("Origin", "http://example.com:8080")
		recorder := serveRequest(handler, request)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
		}
		if strings.Contains(recorder.Body.String(), "private ui") {
			t.Errorf("static content was served for an unexpected Origin: %q", recorder.Body.String())
		}
	})

	t.Run("duplicate origin", func(t *testing.T) {
		request := pageRequest(http.MethodGet, "/")
		request.Header.Add("Origin", testOrigin)
		request.Header.Add("Origin", testOrigin)
		recorder := serveRequest(handler, request)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
		}
	})

	t.Run("matching origin", func(t *testing.T) {
		request := pageRequest(http.MethodGet, "/")
		request.Header.Set("Origin", testOrigin)
		recorder := serveRequest(handler, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
		}
		assertStaticHeaders(t, recorder)
		if !strings.Contains(recorder.Body.String(), "private ui") {
			t.Errorf("body = %q, want index content", recorder.Body.String())
		}
	})
}

func TestStaticWithoutWebIndex(t *testing.T) {
	execute := func(context.Context, string, string, json.RawMessage) (any, error) {
		return map[string]any{"ok": true}, nil
	}

	t.Run("nil web", func(t *testing.T) {
		handler := Handler(testPort, nil, execute, nil)
		recorder := serveRequest(handler, pageRequest(http.MethodGet, "/"))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNotFound)
		}
		assertStaticHeaders(t, recorder)
		if !strings.Contains(recorder.Body.String(), "web interface not available") {
			t.Errorf("body = %q, want unavailable message", recorder.Body.String())
		}
	})

	t.Run("no index", func(t *testing.T) {
		web := fstest.MapFS{
			"app.js": &fstest.MapFile{Data: []byte("console.log('gilir')")},
		}
		handler := Handler(testPort, web, execute, nil)
		recorder := serveRequest(handler, pageRequest(http.MethodGet, "/"))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNotFound)
		}
		if !strings.Contains(recorder.Body.String(), "web interface not available") {
			t.Errorf("body = %q, want unavailable message", recorder.Body.String())
		}

		recorder = serveRequest(handler, pageRequest(http.MethodGet, "/app.js"))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
		}
	})
}

func TestStaticRejectsPathTraversal(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "web")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("public index"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "outside.txt"), []byte("outside secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	handler := Handler(testPort, os.DirFS(root), unexpectedExecute(t), nil)

	recorder := serveRequest(handler, pageRequest(http.MethodGet, "/"))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "public index") {
		t.Fatalf("root request = %d %q, want public index", recorder.Code, recorder.Body.String())
	}

	targets := []string{
		"/../outside.txt",
		"/%2e%2e/outside.txt",
		"/..%2foutside.txt",
		"/nested/../../outside.txt",
		"/..\\outside.txt",
	}
	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			recorder := serveRequest(handler, pageRequest(http.MethodGet, target))
			if recorder.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNotFound)
			}
			if strings.Contains(recorder.Body.String(), "outside secret") {
				t.Errorf("outside file was served: %q", recorder.Body.String())
			}
		})
	}
}

type countingSeekableFS struct {
	fs.FS
	bytesRead int
}

func (f *countingSeekableFS) Open(name string) (fs.File, error) {
	file, err := f.FS.Open(name)
	if err != nil {
		return nil, err
	}
	seeker, ok := file.(io.ReadSeeker)
	if !ok {
		_ = file.Close()
		return nil, errors.New("test file is not seekable")
	}
	return &countingSeekableFile{File: file, seeker: seeker, fs: f}, nil
}

type countingSeekableFile struct {
	fs.File
	seeker io.Seeker
	fs     *countingSeekableFS
}

func (f *countingSeekableFile) Read(p []byte) (int, error) {
	n, err := f.File.Read(p)
	f.fs.bytesRead += n
	return n, err
}

func (f *countingSeekableFile) Seek(offset int64, whence int) (int64, error) {
	return f.seeker.Seek(offset, whence)
}

type nonSeekableFS struct {
	fs.FS
}

func (f nonSeekableFS) Open(name string) (fs.File, error) {
	file, err := f.FS.Open(name)
	if err != nil {
		return nil, err
	}
	return nonSeekableFile{File: file}, nil
}

type nonSeekableFile struct {
	fs.File
}

type oversizedStaticFS struct{}

func (oversizedStaticFS) Open(string) (fs.File, error) {
	return oversizedStaticFile{}, nil
}

type oversizedStaticFile struct{}

func (oversizedStaticFile) Read([]byte) (int, error) {
	return 0, io.EOF
}

func (oversizedStaticFile) Close() error {
	return nil
}

func (oversizedStaticFile) Stat() (fs.FileInfo, error) {
	return oversizedStaticInfo{}, nil
}

type oversizedStaticInfo struct{}

func (oversizedStaticInfo) Name() string       { return "huge.bin" }
func (oversizedStaticInfo) Size() int64        { return maxStaticFallbackBytes + 1 }
func (oversizedStaticInfo) Mode() fs.FileMode  { return 0 }
func (oversizedStaticInfo) ModTime() time.Time { return time.Time{} }
func (oversizedStaticInfo) IsDir() bool        { return false }
func (oversizedStaticInfo) Sys() any           { return nil }

type oversizedStreamFS struct {
	bytesRead int
}

func (f *oversizedStreamFS) Open(string) (fs.File, error) {
	return &oversizedStreamFile{fs: f}, nil
}

type oversizedStreamFile struct {
	fs *oversizedStreamFS
}

func (f *oversizedStreamFile) Read(p []byte) (int, error) {
	remaining := maxStaticFallbackBytes + 2 - f.fs.bytesRead
	if remaining <= 0 {
		return 0, io.EOF
	}
	n := len(p)
	if n > remaining {
		n = remaining
	}
	for i := 0; i < n; i++ {
		p[i] = 'x'
	}
	f.fs.bytesRead += n
	return n, nil
}

func (f *oversizedStreamFile) Close() error {
	return nil
}

func (f *oversizedStreamFile) Stat() (fs.FileInfo, error) {
	return underReportedStaticInfo{}, nil
}

type underReportedStaticInfo struct{}

func (underReportedStaticInfo) Name() string       { return "huge.bin" }
func (underReportedStaticInfo) Size() int64        { return 1 }
func (underReportedStaticInfo) Mode() fs.FileMode  { return 0 }
func (underReportedStaticInfo) ModTime() time.Time { return time.Time{} }
func (underReportedStaticInfo) IsDir() bool        { return false }
func (underReportedStaticInfo) Sys() any           { return nil }
