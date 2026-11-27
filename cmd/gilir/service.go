package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// serviceWorkerCommand is the private argv marker the launcher uses to
	// re-exec this binary as the detached worker. It is checked before CLI
	// parsing, so it never appears in help or collides with a real command.
	serviceWorkerCommand   = "__service-worker"
	serviceInstanceEnvName = "GILIR_SERVICE_INSTANCE"
	serviceVersion         = 1

	serviceStartTimeout = 30 * time.Second
	serviceStopTimeout  = 15 * time.Second
	servicePollInterval = 100 * time.Millisecond
	serviceLogTailBytes = 8 << 10
	serviceLogTailLines = 12
)

// listenConfig is a requested bind address. port is kept as a string because
// "0" asks the kernel for an ephemeral port; the actual port is read from the
// listener and recorded in the service state.
type listenConfig struct {
	host string
	port string
}

// resolveListen applies the --host/--port flags on top of GILIR_LISTEN, which
// defaults to 127.0.0.1:44547. The host must be an IP literal or localhost so
// the request guard can reason about it; a bracketed IPv6 literal is accepted.
func resolveListen(host, port string) (listenConfig, error) {
	listen := envOr("GILIR_LISTEN", defaultListen)
	envHost, envPort, err := net.SplitHostPort(listen)
	if err != nil {
		return listenConfig{}, fmt.Errorf("GILIR_LISTEN %q: %w", listen, err)
	}
	if host == "" {
		host = envHost
	}
	host = normalizeHost(host)
	if !validListenHost(host) {
		return listenConfig{}, fmt.Errorf("listen host %q: must be an IP address or localhost", host)
	}
	if port == "" {
		port = envPort
	}
	n, err := strconv.Atoi(port)
	if err != nil || port != strconv.Itoa(n) || n < 0 || n > 65535 {
		return listenConfig{}, fmt.Errorf("listen port %q: must be a number from 0 to 65535", port)
	}
	return listenConfig{host: host, port: port}, nil
}

func normalizeHost(host string) string {
	host = strings.TrimSpace(host)
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	return host
}

func validListenHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	return net.ParseIP(host) != nil
}

func (c listenConfig) address() string {
	return net.JoinHostPort(c.host, c.port)
}

// urlFor returns a URL a local client can dial. Wildcard binds are probed
// through loopback because "0.0.0.0" and "::" are not routable destinations
// on every platform.
func (c listenConfig) urlFor(port string) string {
	return "http://" + net.JoinHostPort(c.clientHost(), port)
}

func (c listenConfig) clientHost() string {
	switch c.host {
	case "0.0.0.0":
		return "127.0.0.1"
	case "::":
		return "::1"
	default:
		return c.host
	}
}

func (c listenConfig) matches(other listenConfig) bool {
	return strings.EqualFold(c.host, other.host) && c.port == other.port
}

func requestedPort(port string) int {
	n, _ := strconv.Atoi(port)
	return n
}

// serviceState is written by the running daemon and read by lifecycle
// commands. It carries the private control credential, so the file is mode
// 0600 and its contents never appear in command output.
type serviceState struct {
	Version       int       `json:"version"`
	Instance      string    `json:"instance"`
	PID           int       `json:"pid"`
	RequestedHost string    `json:"requested_host"`
	RequestedPort int       `json:"requested_port"`
	Host          string    `json:"host"`
	Port          int       `json:"port"`
	URL           string    `json:"url"`
	ControlPort   int       `json:"control_port"`
	Token         string    `json:"token"`
	LogPath       string    `json:"log_path"`
	StartedAt     time.Time `json:"started_at"`
}

// serviceStatus is the public shape of `service start` and `service status`.
type serviceStatus struct {
	Running        bool   `json:"running"`
	PID            int    `json:"pid,omitempty"`
	Host           string `json:"host,omitempty"`
	Port           int    `json:"port,omitempty"`
	URL            string `json:"url,omitempty"`
	StartedAt      string `json:"started_at,omitempty"`
	LogPath        string `json:"log_path,omitempty"`
	Paused         bool   `json:"paused,omitempty"`
	MaxConcurrency int    `json:"max_concurrency,omitempty"`
	ActiveParents  *int   `json:"active_parents,omitempty"`
	WatcherError   string `json:"watcher_error,omitempty"`
}

type serviceStopped struct {
	Stopped bool `json:"stopped"`
}

type daemonStatusPayload struct {
	Paused         bool   `json:"paused"`
	MaxConcurrency int    `json:"max_concurrency"`
	ActiveParents  *int   `json:"active_parents"`
	WatcherError   string `json:"watcher_error"`
}

func serviceStatePath(dbPath string) string {
	return dbPath + ".service.json"
}

func serviceLogPath(dbPath string) string {
	return dbPath + ".service.log"
}

func serviceOpLockPath(dbPath string) string {
	return dbPath + ".service.lock"
}

// serviceDatabasePath resolves the database path for read-only lifecycle
// commands without creating the file, following symlinks when it exists.
func serviceDatabasePath() (string, error) {
	path, err := databasePath()
	if err != nil {
		return "", err
	}
	if canonical, err := filepath.EvalSymlinks(path); err == nil {
		return canonical, nil
	}
	return path, nil
}

func readServiceState(dbPath string) (serviceState, bool) {
	data, err := os.ReadFile(serviceStatePath(dbPath))
	if err != nil {
		return serviceState{}, false
	}
	var state serviceState
	if err := json.Unmarshal(data, &state); err != nil {
		return serviceState{}, false
	}
	if state.Version != serviceVersion || state.Instance == "" || state.Port <= 0 ||
		state.ControlPort <= 0 || state.Token == "" || state.URL == "" {
		return serviceState{}, false
	}
	return state, true
}

// writeServiceState publishes the state atomically so a concurrent read sees
// either the old record or the new one, never a partial file.
func writeServiceState(dbPath string, state serviceState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode service state: %w", err)
	}
	path := serviceStatePath(dbPath)
	tmp, err := os.CreateTemp(filepath.Dir(path), ".gilir-service-*")
	if err != nil {
		return fmt.Errorf("create service state: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("protect service state: %w", err)
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return fmt.Errorf("write service state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write service state: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("publish service state: %w", err)
	}
	return nil
}

func removeServiceState(dbPath string) {
	_ = os.Remove(serviceStatePath(dbPath))
}

// daemonLockHeld reports whether a daemon currently owns the database. The
// lock file is probed, never unlinked; the kernel releases the lock when the
// process exits.
func daemonLockHeld(dbPath string) bool {
	path := dbPath + ".lock"
	if _, err := os.Stat(path); err != nil {
		return false
	}
	lock, err := lockFile(path)
	if err != nil {
		return errors.Is(err, errLocked)
	}
	lock.Close()
	return false
}

func startService(ctx context.Context, host, port string) (any, error) {
	requested, err := resolveListen(host, port)
	if err != nil {
		return nil, err
	}
	dbPath, err := prepareDatabase()
	if err != nil {
		return nil, err
	}
	lock, err := lockFile(serviceOpLockPath(dbPath))
	if errors.Is(err, errLocked) {
		return nil, errors.New("another gilir service operation is in progress")
	}
	if err != nil {
		return nil, fmt.Errorf("lock service state: %w", err)
	}
	defer lock.Close()
	return startServiceLocked(ctx, dbPath, requested)
}

func startServiceLocked(ctx context.Context, dbPath string, requested listenConfig) (any, error) {
	if state, ok := readServiceState(dbPath); ok {
		if live, err := daemonStatusAt(ctx, state.URL); err == nil {
			current := listenConfig{host: state.RequestedHost, port: strconv.Itoa(state.RequestedPort)}
			if current.matches(requested) {
				return statusFrom(state, live), nil
			}
			return nil, fmt.Errorf("gilir service is already running at %s; run \"gilir service stop\" first", state.URL)
		}
		if daemonLockHeld(dbPath) {
			return nil, fmt.Errorf("gilir service is running but not responding at %s (see %s)", state.URL, state.LogPath)
		}
		removeServiceState(dbPath)
	} else if daemonLockHeld(dbPath) {
		return nil, fmt.Errorf("gilir service is already running but its state file %s is missing or invalid", serviceStatePath(dbPath))
	} else {
		removeServiceState(dbPath)
	}

	logPath := serviceLogPath(dbPath)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open service log: %w", err)
	}
	offset, err := logFile.Seek(0, io.SeekEnd)
	if err != nil {
		logFile.Close()
		return nil, fmt.Errorf("read service log: %w", err)
	}

	exe, err := os.Executable()
	if err != nil {
		logFile.Close()
		return nil, fmt.Errorf("locate gilir executable: %w", err)
	}
	instance, err := randomHex(16)
	if err != nil {
		logFile.Close()
		return nil, fmt.Errorf("generate service identity: %w", err)
	}

	cmd := exec.Command(exe, serviceWorkerCommand)
	cmd.Env = serviceWorkerEnv(os.Environ(), instance, requested.address(), dbPath)
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	configureDetached(cmd)
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return nil, fmt.Errorf("start gilir service: %w", err)
	}
	logFile.Close()

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	deadline := time.NewTimer(serviceStartTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(servicePollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			return nil, ctx.Err()
		case err := <-exited:
			// The worker may have published its state just before exiting.
			if state, ok := readServiceState(dbPath); ok && state.Instance == instance {
				if live, probeErr := daemonStatusAt(ctx, state.URL); probeErr == nil {
					return statusFrom(state, live), nil
				}
			}
			return nil, serviceStartError(logPath, offset, err)
		case <-ticker.C:
			state, ok := readServiceState(dbPath)
			if !ok || state.Instance != instance {
				continue
			}
			if live, probeErr := daemonStatusAt(ctx, state.URL); probeErr == nil {
				return statusFrom(state, live), nil
			}
		case <-deadline.C:
			_ = cmd.Process.Kill()
			return nil, fmt.Errorf("gilir service did not become ready within %s (see %s)%s",
				serviceStartTimeout, logPath, tailSuffix(logPath, offset))
		}
	}
}

func serviceWorkerEnv(base []string, instance, listen, dbPath string) []string {
	env := make([]string, 0, len(base)+3)
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		switch {
		case strings.EqualFold(key, serviceInstanceEnvName),
			strings.EqualFold(key, "GILIR_LISTEN"),
			strings.EqualFold(key, "GILIR_DB"):
			continue
		}
		env = append(env, entry)
	}
	return append(env,
		serviceInstanceEnvName+"="+instance,
		"GILIR_LISTEN="+listen,
		"GILIR_DB="+dbPath,
	)
}

func serviceStartError(logPath string, offset int64, err error) error {
	detail := "gilir service exited before becoming ready"
	if err != nil {
		detail = fmt.Sprintf("gilir service failed to start: %v", err)
	}
	return fmt.Errorf("%s (see %s)%s", detail, logPath, tailSuffix(logPath, offset))
}

func tailSuffix(path string, offset int64) string {
	tail := logTail(path, offset)
	if tail == "" {
		return ""
	}
	return "\n" + tail
}

// logTail returns the last few log lines written since offset so a failed
// launch can explain itself without dumping the whole log.
func logTail(path string, offset int64) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return ""
	}
	start := offset
	if start < 0 {
		start = 0
	}
	if size := info.Size(); size-start > serviceLogTailBytes {
		start = size - serviceLogTailBytes
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return ""
	}
	data, err := io.ReadAll(io.LimitReader(file, serviceLogTailBytes))
	if err != nil {
		return ""
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	if len(lines) > serviceLogTailLines {
		lines = lines[len(lines)-serviceLogTailLines:]
	}
	return strings.Join(lines, "\n")
}

func stopService(ctx context.Context) (any, error) {
	dbPath, err := serviceDatabasePath()
	if err != nil {
		return nil, err
	}
	lock, err := lockFile(serviceOpLockPath(dbPath))
	if errors.Is(err, errLocked) {
		return nil, errors.New("another gilir service operation is in progress")
	}
	if errors.Is(err, os.ErrNotExist) {
		// The database directory was never created, so nothing can be running.
		return serviceStopped{Stopped: false}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lock service state: %w", err)
	}
	defer lock.Close()
	return stopServiceLocked(ctx, dbPath)
}

func stopServiceLocked(ctx context.Context, dbPath string) (any, error) {
	state, ok := readServiceState(dbPath)
	if !ok {
		if daemonLockHeld(dbPath) {
			return nil, fmt.Errorf("gilir service is running but its state file %s is missing or invalid", serviceStatePath(dbPath))
		}
		removeServiceState(dbPath)
		return serviceStopped{Stopped: false}, nil
	}

	controlErr := requestStop(ctx, state)
	// The listener is closed before watcher, hook, and store cleanup, so wait
	// for the database lock to be released rather than for HTTP to fail.
	if err := waitDaemonLockFree(ctx, dbPath, serviceStopTimeout); err != nil {
		if controlErr != nil {
			return nil, fmt.Errorf("stop gilir service at %s: %v (see %s)", state.URL, controlErr, state.LogPath)
		}
		return nil, fmt.Errorf("gilir service at %s did not stop within %s", state.URL, serviceStopTimeout)
	}
	removeServiceState(dbPath)
	return serviceStopped{Stopped: true}, nil
}

func restartService(ctx context.Context, host, port string) (any, error) {
	dbPath, err := serviceDatabasePath()
	if err != nil {
		return nil, err
	}
	state, ok := readServiceState(dbPath)
	requested, err := resolveRestartListen(host, port, state, ok)
	if err != nil {
		return nil, err
	}
	if dbPath, err = prepareDatabase(); err != nil {
		return nil, err
	}

	lock, err := lockFile(serviceOpLockPath(dbPath))
	if errors.Is(err, errLocked) {
		return nil, errors.New("another gilir service operation is in progress")
	}
	if err != nil {
		return nil, fmt.Errorf("lock service state: %w", err)
	}
	defer lock.Close()

	if _, err := stopServiceLocked(ctx, dbPath); err != nil {
		return nil, err
	}
	return startServiceLocked(ctx, dbPath, requested)
}

// resolveRestartListen keeps the running instance's requested address unless
// a flag or an explicitly set GILIR_LISTEN overrides it.
func resolveRestartListen(host, port string, state serviceState, ok bool) (listenConfig, error) {
	if value, present := os.LookupEnv("GILIR_LISTEN"); present && value != "" {
		envHost, envPort, err := net.SplitHostPort(value)
		if err != nil {
			return listenConfig{}, fmt.Errorf("GILIR_LISTEN %q: %w", value, err)
		}
		if host == "" {
			host = envHost
		}
		if port == "" {
			port = envPort
		}
	}
	if ok {
		if host == "" {
			host = state.RequestedHost
		}
		if port == "" {
			port = strconv.Itoa(state.RequestedPort)
		}
	}
	return resolveListen(host, port)
}

func queryServiceStatus(ctx context.Context) (any, error) {
	dbPath, err := serviceDatabasePath()
	if err != nil {
		return nil, err
	}
	out := serviceStatus{LogPath: serviceLogPath(dbPath)}
	state, ok := readServiceState(dbPath)
	if !ok {
		if daemonLockHeld(dbPath) {
			return nil, fmt.Errorf("gilir service is running but its state file %s is missing or invalid", serviceStatePath(dbPath))
		}
		return out, nil
	}
	live, err := daemonStatusAt(ctx, state.URL)
	if err != nil {
		if daemonLockHeld(dbPath) {
			return nil, fmt.Errorf("gilir service is running but not responding at %s: %v (see %s)", state.URL, err, state.LogPath)
		}
		return out, nil
	}
	return statusFrom(state, live), nil
}

func statusFrom(state serviceState, live daemonStatusPayload) serviceStatus {
	return serviceStatus{
		Running:        true,
		PID:            state.PID,
		Host:           state.Host,
		Port:           state.Port,
		URL:            state.URL,
		StartedAt:      state.StartedAt.UTC().Format(time.RFC3339),
		LogPath:        state.LogPath,
		Paused:         live.Paused,
		MaxConcurrency: live.MaxConcurrency,
		ActiveParents:  live.ActiveParents,
		WatcherError:   live.WatcherError,
	}
}

// daemonStatusAt asks a running daemon for daemon.status over its public API.
func daemonStatusAt(ctx context.Context, baseURL string) (daemonStatusPayload, error) {
	raw, err := callDaemon(ctx, baseURL, "daemon.status", json.RawMessage(`{}`), statusTimeout)
	if err != nil {
		return daemonStatusPayload{}, err
	}
	var payload daemonStatusPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return daemonStatusPayload{}, fmt.Errorf("decode daemon status: %w", err)
	}
	return payload, nil
}

func callDaemon(ctx context.Context, baseURL, command string, args json.RawMessage, timeout time.Duration) (json.RawMessage, error) {
	body, err := json.Marshal(struct {
		Command string          `json:"command"`
		Args    json.RawMessage `json:"args"`
	}{Command: command, Args: args})
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", command, err)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(baseURL, "/")+"/api/v1/command", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{
		Transport:     &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("daemon is not running at %s (start it with \"gilir service start\"): %w", baseURL, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxStatusBytes))
	if err != nil {
		return nil, fmt.Errorf("read daemon status: %w", err)
	}
	var envelope struct {
		Data  json.RawMessage `json:"data"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("daemon at %s returned HTTP %d without a Gilir response", baseURL, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK || envelope.Error != nil || len(envelope.Data) == 0 {
		message := http.StatusText(resp.StatusCode)
		if envelope.Error != nil && envelope.Error.Message != "" {
			message = envelope.Error.Message
		}
		return nil, fmt.Errorf("daemon status failed: %s", message)
	}
	return envelope.Data, nil
}

// controlHandler is the private loopback endpoint used by `service stop`. It
// is deliberately separate from the public API so exposing the UI on a
// network never exposes process shutdown.
type controlHandler struct {
	token string
	stop  chan struct{}
}

func (h *controlHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/stop" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	presented := r.Header.Get("Authorization")
	if subtle.ConstantTimeCompare([]byte(presented), []byte("Bearer "+h.token)) != 1 {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	select {
	case h.stop <- struct{}{}:
	default:
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"stopping":true}`+"\n")
}

func requestStop(ctx context.Context, state serviceState) error {
	ctx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()
	url := fmt.Sprintf("http://127.0.0.1:%d/v1/stop", state.ControlPort)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+state.Token)
	client := &http.Client{
		Transport:     &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("control endpoint returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func waitDaemonLockFree(ctx context.Context, dbPath string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if !daemonLockHeld(dbPath) {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("timed out")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(servicePollInterval):
		}
	}
}

func randomHex(bytesCount int) (string, error) {
	buf := make([]byte, bytesCount)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
