package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/armedi/gilir/internal/commands"
	"github.com/armedi/gilir/internal/hooks"
	"github.com/armedi/gilir/internal/localhttp"
	"github.com/armedi/gilir/internal/opencode"
	"github.com/armedi/gilir/internal/store"
	"github.com/armedi/gilir/internal/watcher"
)

const (
	defaultListen   = "127.0.0.1:44547"
	defaultWebDir   = "web/dist"
	shutdownTimeout = 10 * time.Second
	statusTimeout   = 5 * time.Second
	maxStatusBytes  = 1 << 20
)

// errLocked reports that another process holds the daemon lock.
var errLocked = errors.New("lock is held by another process")

type stopped struct {
	Stopped bool `json:"stopped"`
}

// opencodeEndpoint describes where the daemon will look for OpenCode, for the
// startup line. It never contains the password.
func opencodeEndpoint(overrides opencode.Overrides) string {
	if overrides.URL != nil {
		return *overrides.URL
	}
	return "auto-discovered from the managed opencode service"
}

// opencodeOverrides returns the explicitly configured OpenCode connection
// values. An unset variable means "discover it from the managed service"; an
// explicitly empty one keeps its current meaning (no password, or the default
// URL).
func opencodeOverrides() opencode.Overrides {
	var overrides opencode.Overrides
	if value, ok := os.LookupEnv("GILIR_OPENCODE_URL"); ok {
		overrides.URL = &value
	}
	if value, ok := os.LookupEnv("OPENCODE_PASSWORD"); ok {
		overrides.Password = &value
	}
	return overrides
}

// runDaemon serves until SIGINT/SIGTERM or a server failure, then stops the
// HTTP server, cancels and waits for the watcher and hooks, and only then
// closes SQLite and releases the lock.
func runDaemon(parent context.Context, port string) (any, error) {
	listen, err := listenAddress(port)
	if err != nil {
		return nil, err
	}
	overrides := opencodeOverrides()
	conn, err := opencode.NewConnector(opencode.NewCLI(), overrides)
	if err != nil {
		return nil, fmt.Errorf("GILIR_OPENCODE_URL: %w", err)
	}
	// Resolve once at startup so a stopped service is reported immediately and
	// the first watcher tick reuses the cached client instead of running the
	// discovery CLI again. Discovery failure never stops the daemon: the
	// watcher keeps retrying with backoff once OpenCode is available.
	if _, _, err := conn.Client(parent); err != nil {
		fmt.Fprintf(os.Stderr, "gilir: warning: %s\n", conn.Redact(opencode.Describe(err)))
	}

	var hookConfigs []hooks.Config
	if path := os.Getenv("GILIR_HOOKS"); path != "" {
		if hookConfigs, err = hooks.Load(path); err != nil {
			return nil, fmt.Errorf("GILIR_HOOKS: %w", err)
		}
	}

	dbPath, err := prepareDatabase()
	if err != nil {
		return nil, err
	}
	lock, err := lockFile(dbPath + ".lock")
	if errors.Is(err, errLocked) {
		return nil, fmt.Errorf("another gilir daemon is already running for %s", dbPath)
	}
	if err != nil {
		return nil, fmt.Errorf("acquire daemon lock: %w", err)
	}
	defer lock.Close() // released by close or process exit; the file stays

	st, err := store.Open(dbPath)
	if err != nil {
		return nil, err
	}
	defer st.Close()

	source := storeSource{st}
	if err := initHookCursors(parent, source, hookConfigs); err != nil {
		return nil, err
	}

	web := embeddedWeb()
	webNote := "embedded web UI"
	webDir := os.Getenv("GILIR_WEB_DIR")
	if webDir != "" || web == nil {
		if webDir == "" {
			webDir = defaultWebDir
		}
		diskWeb, closeWeb := openWeb(webDir)
		defer closeWeb()
		web = diskWeb
		webNote = "web UI from " + webDir
		if web == nil {
			webNote = "no web UI (" + webDir + "/index.html not found)"
		}
	}

	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", listen, err)
	}
	host, _, _ := net.SplitHostPort(listen)
	boundPort := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)

	w := watcher.NewWithConnection(st, conn)
	service := commands.New(st, func() commands.Snapshot {
		snap := w.Snapshot()
		return commands.Snapshot{ActiveParents: snap.ActiveParents, WatcherError: snap.Error}
	})
	server := &http.Server{
		Handler:           localhttp.Handler(boundPort, web, service.Execute, commands.Classify),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          log.New(os.Stderr, "gilir: http: ", 0),
	}

	sigCtx, stopSignals := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	ctx, cancel := context.WithCancel(sigCtx)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		w.Run(ctx)
	}()
	go func() {
		defer wg.Done()
		hooks.Run(ctx, hookConfigs, source, func(err error) {
			fmt.Fprintf(os.Stderr, "gilir: %v\n", err)
		})
	}()
	served := make(chan error, 1)
	go func() { served <- server.Serve(ln) }()

	fmt.Fprintf(os.Stderr, "gilir: daemon listening on http://%s (database %s, OpenCode %s, %s, %d hooks)\n",
		net.JoinHostPort(host, boundPort), dbPath, opencodeEndpoint(overrides), webNote, len(hookConfigs))

	var runErr error
	select {
	case <-ctx.Done():
	case err := <-served:
		runErr = fmt.Errorf("http server: %w", err)
	}
	fmt.Fprintln(os.Stderr, "gilir: daemon shutting down")
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
	}
	cancel()
	wg.Wait()
	if runErr != nil {
		return nil, runErr
	}
	return stopped{Stopped: true}, nil
}

// daemonStatus asks the running daemon for daemon.status over its local
// HTTP API, since only the daemon knows the live watcher state.
func daemonStatus(ctx context.Context) (any, error) {
	listen, err := listenAddress("")
	if err != nil {
		return nil, err
	}
	if _, port, _ := net.SplitHostPort(listen); port == "0" {
		return nil, errors.New("GILIR_LISTEN uses port 0; set an explicit port to query daemon status")
	}
	ctx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+listen+"/api/v1/command",
		strings.NewReader(`{"command":"daemon.status","args":{}}`))
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
		return nil, fmt.Errorf("daemon is not running at %s (start it with \"gilir daemon run\"): %w", listen, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxStatusBytes))
	if err != nil {
		return nil, fmt.Errorf("read daemon status: %w", err)
	}
	var envelope struct {
		Data  json.RawMessage `json:"data"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("daemon at %s returned HTTP %d without a Gilir response", listen, resp.StatusCode)
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

// listenAddress returns GILIR_LISTEN, which must be a literal loopback IP
// (127.0.0.1 or ::1) and a decimal port. A non-empty port overrides the port
// from GILIR_LISTEN while keeping its host.
func listenAddress(port string) (string, error) {
	listen := envOr("GILIR_LISTEN", defaultListen)
	host, listenPort, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("GILIR_LISTEN %q: %w", listen, err)
	}
	if host != "127.0.0.1" && host != "::1" {
		return "", fmt.Errorf("GILIR_LISTEN %q: host must be 127.0.0.1 or [::1]", listen)
	}
	if port != "" {
		listenPort = port
	}
	n, err := strconv.Atoi(listenPort)
	if err != nil || listenPort != strconv.Itoa(n) || n < 0 || n > 65535 {
		return "", fmt.Errorf("GILIR_LISTEN %q: port must be a number from 0 to 65535", net.JoinHostPort(host, listenPort))
	}
	return net.JoinHostPort(host, listenPort), nil
}

// openWeb returns the static bundle in dir when it has an index.html, read
// through an os.Root so requests cannot escape it, or nil otherwise.
func openWeb(dir string) (fs.FS, func()) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, func() {}
	}
	if info, err := root.Stat("index.html"); err != nil || !info.Mode().IsRegular() {
		root.Close()
		return nil, func() {}
	}
	return root.FS(), func() { root.Close() }
}

// initHookCursors starts each configured hook that has no cursor at the
// current activity ID, so events after startup are delivered and history is
// not replayed.
func initHookCursors(ctx context.Context, source storeSource, configs []hooks.Config) error {
	if len(configs) == 0 {
		return nil
	}
	latest, err := source.Latest(ctx)
	if err != nil {
		return err
	}
	for _, config := range configs {
		_, initialized, err := source.Cursor(ctx, config.Name)
		if err != nil {
			return err
		}
		if !initialized {
			if err := source.SetCursor(ctx, config.Name, latest); err != nil {
				return err
			}
		}
	}
	return nil
}

// storeSource adapts the store's activity feed and hook cursors to hooks.Source.
type storeSource struct{ st *store.Store }

func (s storeSource) After(ctx context.Context, afterID int64, limit int) ([]hooks.Event, error) {
	activities, err := s.st.ActivityAfter(ctx, afterID, limit)
	if err != nil {
		return nil, err
	}
	events := make([]hooks.Event, 0, len(activities))
	for _, activity := range activities {
		payload, err := json.Marshal(activity)
		if err != nil {
			return nil, fmt.Errorf("encode activity %d: %w", activity.ID, err)
		}
		events = append(events, hooks.Event{ID: activity.ID, Type: activity.Type, Actor: activity.Actor, Payload: payload})
	}
	return events, nil
}

func (s storeSource) Cursor(ctx context.Context, name string) (int64, bool, error) {
	return s.st.HookCursor(ctx, name)
}

func (s storeSource) SetCursor(ctx context.Context, name string, lastID int64) error {
	return s.st.SaveHookCursor(ctx, name, lastID)
}

func (s storeSource) Latest(ctx context.Context) (int64, error) {
	return s.st.LatestActivityID(ctx)
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
