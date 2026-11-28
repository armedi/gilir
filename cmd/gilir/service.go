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
	serviceListenEnvName   = "GILIR_SERVICE_LISTEN"
	serviceVersion         = 1

	serviceStartTimeout = 60 * time.Second
	serviceStopTimeout  = 30 * time.Second
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

// resolveListen applies the --host/--port flags on top of the defaults,
// 127.0.0.1:44547. The host must be an IP literal or localhost; a bracketed IPv6
// literal is accepted.
func resolveListen(host, port string) (listenConfig, error) {
	if host == "" {
		host = defaultHost()
	}
	if port == "" {
		port = defaultPort()
	}
	return normalizeListen(host, port)
}

// resolveWorkerListen returns the address of a detached worker. The launcher
// passes the already resolved address through the private
// GILIR_SERVICE_LISTEN variable, so a GILIR_URL or any other client setting
// inherited by the worker can never choose the bind address. Explicit
// arguments still win for direct in-process starts and tests.
func resolveWorkerListen(host, port string) (listenConfig, error) {
	if host == "" && port == "" {
		if value := os.Getenv(serviceListenEnvName); value != "" {
			workerHost, workerPort, err := net.SplitHostPort(value)
			if err != nil {
				return listenConfig{}, fmt.Errorf("%s %q: %w", serviceListenEnvName, value, err)
			}
			host, port = workerHost, workerPort
		}
	}
	return resolveListen(host, port)
}

func defaultHost() string {
	host, _, err := net.SplitHostPort(defaultListen)
	if err != nil {
		return "127.0.0.1"
	}
	return host
}

func defaultPort() string {
	_, port, err := net.SplitHostPort(defaultListen)
	if err != nil {
		return "44547"
	}
	return port
}

func normalizeListen(host, port string) (listenConfig, error) {
	host = normalizeHost(host)
	if ip := net.ParseIP(host); ip != nil {
		// Canonical spelling makes wildcard detection and identity checks
		// independent of how the address was written (::0 vs ::, and so on).
		host = ip.String()
	} else if !validListenHost(host) {
		return listenConfig{}, fmt.Errorf("listen host %q: must be an IP address or localhost", host)
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

// boundURLFor uses the actual TCP listener's address, mapping wildcard binds
// to a dialable loopback address in the requested address family.
func boundURLFor(c listenConfig, tcp *net.TCPAddr) string {
	host := tcp.IP.String()
	if tcp.IP.IsUnspecified() {
		host = "::1"
		if c.host == "0.0.0.0" {
			host = "127.0.0.1"
		}
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(tcp.Port))
}

func (c listenConfig) matches(other listenConfig) bool {
	return strings.EqualFold(c.host, other.host) && c.port == other.port
}

func requestedPort(port string) int {
	n, _ := strconv.Atoi(port)
	return n
}

// serviceState is written by the running managed worker and read by lifecycle
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
	DBPath        string    `json:"db_path"`
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
	DBPath         string `json:"db_path,omitempty"`
	StartedAt      string `json:"started_at,omitempty"`
	LogPath        string `json:"log_path,omitempty"`
	Paused         bool   `json:"paused"`
	MaxConcurrency int    `json:"max_concurrency"`
	ActiveParents  *int   `json:"active_parents"`
	WatcherError   string `json:"watcher_error,omitempty"`
}

type serviceStopped struct {
	Stopped bool `json:"stopped"`
}

type serviceStatusPayload struct {
	Paused         bool   `json:"paused"`
	MaxConcurrency int    `json:"max_concurrency"`
	ActiveParents  *int   `json:"active_parents"`
	WatcherError   string `json:"watcher_error"`
	Instance       string `json:"instance"`
}

// servicePaths names the singleton managed-service files. They live in the
// user data directory rather than beside the database, so one service is
// managed per user and a foreground `gilir serve` never touches them.
type servicePaths struct {
	Dir    string
	State  string
	Log    string
	OpLock string
}

func resolveServicePaths() (servicePaths, error) {
	dir, err := dataDir()
	if err != nil {
		return servicePaths{}, fmt.Errorf("locate data directory: %w", err)
	}
	dir = filepath.Join(dir, "gilir")
	return servicePaths{
		Dir:    dir,
		State:  filepath.Join(dir, "service.json"),
		Log:    filepath.Join(dir, "service.log"),
		OpLock: filepath.Join(dir, "service.lock"),
	}, nil
}

// prepare creates the managed-service directory (0700) so state, log, and
// operation lock files can be written there.
func (p servicePaths) prepare() error {
	if err := os.MkdirAll(p.Dir, 0o700); err != nil {
		return fmt.Errorf("create service directory: %w", err)
	}
	return nil
}

func readServiceState(paths servicePaths) (serviceState, bool) {
	data, err := os.ReadFile(paths.State)
	if err != nil {
		return serviceState{}, false
	}
	var state serviceState
	if err := json.Unmarshal(data, &state); err != nil {
		return serviceState{}, false
	}
	if state.Version != serviceVersion || state.Instance == "" || state.Port <= 0 ||
		state.ControlPort <= 0 || state.Token == "" || state.URL == "" || state.DBPath == "" {
		return serviceState{}, false
	}
	return state, true
}

// writeServiceState publishes the state atomically so a concurrent read sees
// either the old record or the new one, never a partial file.
func writeServiceState(paths servicePaths, state serviceState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode service state: %w", err)
	}
	if err := paths.prepare(); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(paths.Dir, ".gilir-service-*")
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
	if err := os.Rename(name, paths.State); err != nil {
		return fmt.Errorf("publish service state: %w", err)
	}
	return nil
}

func removeServiceState(paths servicePaths) {
	_ = os.Remove(paths.State)
}

// removeServiceStateIf deletes the record only when it still names instance, so
// an exiting worker never discards another worker's state after its own record
// was lost and replaced.
func removeServiceStateIf(paths servicePaths, instance string) {
	state, ok := readServiceState(paths)
	if ok && state.Instance != instance {
		return
	}
	if !ok {
		// Without a readable record there is nothing to protect; an unreadable
		// file must not keep a dead service's record forever.
		if _, err := os.Stat(paths.State); errors.Is(err, os.ErrNotExist) {
			return
		}
	}
	removeServiceState(paths)
}

// serviceLockHeld reports whether a service currently owns dbPath. The lock
// file is probed, never unlinked; the kernel releases the lock when the
// process exits.
func serviceLockHeld(dbPath string) bool {
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

func startService(ctx context.Context, host, port, db string) (any, error) {
	requested, err := resolveListen(host, port)
	if err != nil {
		return nil, err
	}
	paths, err := resolveServicePaths()
	if err != nil {
		return nil, err
	}
	dbPath, err := prepareDatabase(db)
	if err != nil {
		return nil, err
	}
	if err := paths.prepare(); err != nil {
		return nil, err
	}
	lock, err := lockFile(paths.OpLock)
	if errors.Is(err, errLocked) {
		return nil, errors.New("another gilir service operation is in progress")
	}
	if err != nil {
		return nil, fmt.Errorf("lock service state: %w", err)
	}
	defer lock.Close()
	return startServiceLocked(ctx, paths, dbPath, requested)
}

func startServiceLocked(ctx context.Context, paths servicePaths, dbPath string, requested listenConfig) (any, error) {
	if state, ok := readServiceState(paths); ok {
		if live, err := serviceStatusAt(ctx, state.URL); err == nil && live.Instance == state.Instance {
			current := listenConfig{host: state.RequestedHost, port: strconv.Itoa(state.RequestedPort)}
			if current.matches(requested) && state.DBPath == dbPath {
				return statusFrom(state, live), nil
			}
			return nil, fmt.Errorf("gilir service is already running at %s for %s; run \"gilir service stop\" first", state.URL, state.DBPath)
		}
		if serviceLockHeld(state.DBPath) {
			return nil, fmt.Errorf("%s is in use by another gilir process (not the managed service at %s); stop it or run \"gilir service stop\" to clear the stale record %s", state.DBPath, state.URL, paths.State)
		}
		removeServiceState(paths)
	} else if serviceLockHeld(dbPath) {
		return nil, fmt.Errorf("%s is in use by another gilir process (not the managed service recorded in %s); stop it first", dbPath, paths.State)
	} else {
		removeServiceState(paths)
	}

	// A foreground `gilir serve` (or another process) may own the database
	// without publishing managed state. Refuse before spawning a worker that
	// would only fail the same way.
	if serviceLockHeld(dbPath) {
		return nil, fmt.Errorf("another gilir process is already using %s; stop it or choose a different --db", dbPath)
	}

	logFile, err := os.OpenFile(paths.Log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
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
			if state, ok := readServiceState(paths); ok && state.Instance == instance {
				if live, probeErr := serviceStatusAt(ctx, state.URL); probeErr == nil {
					return statusFrom(state, live), nil
				}
			}
			return nil, serviceStartError(paths.Log, offset, err)
		case <-ticker.C:
			state, ok := readServiceState(paths)
			if !ok || state.Instance != instance {
				continue
			}
			// Require the launched worker's identity: a stale state file may
			// name a port that another database's service now answers on.
			if live, probeErr := serviceStatusAt(ctx, state.URL); probeErr == nil && live.Instance == instance {
				return statusFrom(state, live), nil
			}
		case <-deadline.C:
			_ = cmd.Process.Kill()
			return nil, fmt.Errorf("gilir service did not become ready within %s (see %s)%s",
				serviceStartTimeout, paths.Log, tailSuffix(paths.Log, offset))
		}
	}
}

func serviceWorkerEnv(base []string, instance, listen, dbPath string) []string {
	env := make([]string, 0, len(base)+3)
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		switch {
		case strings.EqualFold(key, serviceInstanceEnvName),
			strings.EqualFold(key, serviceListenEnvName),
			strings.EqualFold(key, "GILIR_LISTEN"),
			strings.EqualFold(key, "GILIR_DB"):
			continue
		}
		env = append(env, entry)
	}
	return append(env,
		serviceInstanceEnvName+"="+instance,
		serviceListenEnvName+"="+listen,
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
	paths, err := resolveServicePaths()
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(paths.Dir); errors.Is(err, os.ErrNotExist) {
		// The data directory was never created, so nothing can be running.
		return serviceStopped{Stopped: false}, nil
	}
	lock, err := lockFile(paths.OpLock)
	if errors.Is(err, errLocked) {
		return nil, errors.New("another gilir service operation is in progress")
	}
	if errors.Is(err, os.ErrNotExist) {
		return serviceStopped{Stopped: false}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lock service state: %w", err)
	}
	defer lock.Close()
	return stopServiceLocked(ctx, paths)
}

func stopServiceLocked(ctx context.Context, paths servicePaths) (any, error) {
	state, ok := readServiceState(paths)
	if !ok {
		removeServiceState(paths)
		return serviceStopped{Stopped: false}, nil
	}

	if !serviceLockHeld(state.DBPath) {
		removeServiceState(paths)
		return serviceStopped{Stopped: false}, nil
	}

	controlErr := requestStop(ctx, state)
	if controlErr != nil {
		// A stale record (the worker was killed, then a foreground serve took
		// the database) cannot answer, and its lock will never be released by
		// this record's owner. Verify the instance before waiting the full
		// timeout: if the URL does not answer as this record's instance, drop
		// the record instead of blocking on an unrelated process.
		if live, probeErr := serviceStatusAt(ctx, state.URL); probeErr != nil || live.Instance != state.Instance {
			removeServiceState(paths)
			return serviceStopped{Stopped: false}, nil
		}
	}
	// The listener is closed before watcher, hook, and store cleanup, so wait
	// for the database lock to be released rather than for HTTP to fail.
	if err := waitServiceLockFree(ctx, state.DBPath, serviceStopTimeout); err != nil {
		if controlErr != nil {
			return nil, fmt.Errorf("stop gilir service at %s: %v (see %s)", state.URL, controlErr, state.LogPath)
		}
		return nil, fmt.Errorf("gilir service at %s did not stop within %s", state.URL, serviceStopTimeout)
	}
	removeServiceState(paths)
	return serviceStopped{Stopped: true}, nil
}

func restartService(ctx context.Context, host, port, db string) (any, error) {
	paths, err := resolveServicePaths()
	if err != nil {
		return nil, err
	}
	if err := paths.prepare(); err != nil {
		return nil, err
	}

	lock, err := lockFile(paths.OpLock)
	if errors.Is(err, errLocked) {
		return nil, errors.New("another gilir service operation is in progress")
	}
	if err != nil {
		return nil, fmt.Errorf("lock service state: %w", err)
	}
	defer lock.Close()

	state, ok := readServiceState(paths)
	if ok {
		// Only a live instance's address and database are worth keeping; a
		// stale record must not make restart dial a port that no longer
		// belongs to this service.
		live, probeErr := serviceStatusAt(ctx, state.URL)
		ok = probeErr == nil && live.Instance == state.Instance
	}
	requested, err := resolveRestartListen(host, port, state, ok)
	if err != nil {
		return nil, err
	}
	dbPath, err := restartDatabasePath(db, state, ok)
	if err != nil {
		return nil, err
	}
	if _, err := stopServiceLocked(ctx, paths); err != nil {
		return nil, err
	}
	return startServiceLocked(ctx, paths, dbPath, requested)
}

// resolveRestartListen keeps the running instance's requested address unless
// the --host/--port flags override it. A stopped service uses the defaults.
func resolveRestartListen(host, port string, state serviceState, ok bool) (listenConfig, error) {
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

// restartDatabasePath keeps the running instance's database unless --db
// overrides it. A stopped service uses --db, GILIR_DB, or the default.
func restartDatabasePath(db string, state serviceState, ok bool) (string, error) {
	if ok && strings.TrimSpace(db) == "" {
		return prepareDatabase(state.DBPath)
	}
	return prepareDatabase(db)
}

func queryServiceStatus(ctx context.Context) (any, error) {
	paths, err := resolveServicePaths()
	if err != nil {
		return nil, err
	}
	out := serviceStatus{LogPath: paths.Log}
	state, ok := readServiceState(paths)
	if !ok {
		return out, nil
	}
	live, err := serviceStatusAt(ctx, state.URL)
	if err != nil {
		if serviceLockHeld(state.DBPath) {
			return nil, fmt.Errorf("gilir service is running but not responding at %s: %v (see %s)", state.URL, err, state.LogPath)
		}
		return out, nil
	}
	if live.Instance != state.Instance {
		// Another database's service reused this address; this service's
		// state is stale.
		return out, nil
	}
	return statusFrom(state, live), nil
}

// updateServiceQueue is the host-local path for pause, resume, and concurrency.
// It reads the singleton service.json and never GILIR_URL, verifies the
// database lock and live instance identity, then forwards service.update.
func updateServiceQueue(ctx context.Context, actor string, args map[string]any, action string) (any, error) {
	paths, err := resolveServicePaths()
	if err != nil {
		return nil, err
	}
	state, ok := readServiceState(paths)
	if !ok {
		return nil, fmt.Errorf("gilir service is not running; start it with \"gilir service start\"")
	}
	if !serviceLockHeld(state.DBPath) {
		return nil, fmt.Errorf("gilir service is not running (stale state file %s); start it with \"gilir service start\"", paths.State)
	}
	live, err := serviceStatusAt(ctx, state.URL)
	if err != nil {
		return nil, fmt.Errorf("gilir service is not responding at %s: %v", state.URL, err)
	}
	if live.Instance != state.Instance {
		return nil, fmt.Errorf("gilir service at %s is not the one recorded in %s; run \"gilir service status\"", state.URL, paths.State)
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", action, err)
	}
	return callService(ctx, state.URL, "service.update", actor, raw, statusTimeout)
}

func statusFrom(state serviceState, live serviceStatusPayload) serviceStatus {
	return serviceStatus{
		Running:        true,
		PID:            state.PID,
		Host:           state.Host,
		Port:           state.Port,
		URL:            state.URL,
		DBPath:         state.DBPath,
		StartedAt:      state.StartedAt.UTC().Format(time.RFC3339),
		LogPath:        state.LogPath,
		Paused:         live.Paused,
		MaxConcurrency: live.MaxConcurrency,
		ActiveParents:  live.ActiveParents,
		WatcherError:   live.WatcherError,
	}
}

// serviceStatusAt asks a running service for service.status over its public API.
func serviceStatusAt(ctx context.Context, baseURL string) (serviceStatusPayload, error) {
	raw, err := callService(ctx, baseURL, "service.status", "", json.RawMessage(`{}`), statusTimeout)
	if err != nil {
		return serviceStatusPayload{}, err
	}
	var payload serviceStatusPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return serviceStatusPayload{}, fmt.Errorf("decode service status: %w", err)
	}
	return payload, nil
}

func callService(ctx context.Context, baseURL, command, actor string, args json.RawMessage, timeout time.Duration) (json.RawMessage, error) {
	body, err := json.Marshal(struct {
		Command string          `json:"command"`
		Actor   string          `json:"actor,omitempty"`
		Args    json.RawMessage `json:"args"`
	}{Command: command, Actor: actor, Args: args})
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
		return nil, fmt.Errorf("service is not running at %s (start it with \"gilir service start\"): %w", baseURL, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxStatusBytes))
	if err != nil {
		return nil, fmt.Errorf("read service status: %w", err)
	}
	var envelope struct {
		Data  json.RawMessage `json:"data"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("service at %s returned HTTP %d without a Gilir response", baseURL, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK || envelope.Error != nil || len(envelope.Data) == 0 {
		message := http.StatusText(resp.StatusCode)
		if envelope.Error != nil && envelope.Error.Message != "" {
			message = envelope.Error.Message
		}
		return nil, fmt.Errorf("service request failed: %s", message)
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

func waitServiceLockFree(ctx context.Context, dbPath string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if !serviceLockHeld(dbPath) {
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
