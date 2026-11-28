package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
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

// errLocked reports that another process holds the service lock.
var errLocked = errors.New("lock is held by another process")

// opencodeEndpoint describes where the service will look for OpenCode, for the
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

// serveForeground runs the service in the current process until SIGINT,
// SIGTERM, or a server failure. It never publishes managed-service state,
// never starts the private control listener, and prints nothing on stdout on a
// clean exit.
func serveForeground(ctx context.Context, host, port, db string) (any, error) {
	listen, err := resolveListen(host, port)
	if err != nil {
		return nil, err
	}
	dbPath, err := prepareDatabase(db)
	if err != nil {
		return nil, err
	}
	if err := serve(ctx, listen, dbPath, false); err != nil {
		return nil, err
	}
	return nil, nil
}

// runServiceWorker is the private detached-worker entry point replumbed by
// `gilir service start`. It reads its bind address and database from the
// launcher's private environment.
func runServiceWorker(ctx context.Context) error {
	listen, err := resolveWorkerListen("", "")
	if err != nil {
		return err
	}
	dbPath, err := prepareDatabase("")
	if err != nil {
		return err
	}
	return serve(ctx, listen, dbPath, true)
}

// serve runs the HTTP API, web UI, watcher and hooks until shutdown. managed
// selects the detached-service behavior: publish service state under
// <dataDir>/gilir and serve the private loopback stop endpoint.
func serve(parent context.Context, listen listenConfig, dbPath string, managed bool) error {
	overrides := opencodeOverrides()
	conn, err := opencode.NewConnector(opencode.NewCLI(), overrides)
	if err != nil {
		return fmt.Errorf("GILIR_OPENCODE_URL: %w", err)
	}
	// Resolve once at startup so a stopped service is reported immediately and
	// the first watcher tick reuses the cached client instead of running the
	// discovery CLI again. Discovery failure never stops the service: the
	// watcher keeps retrying with backoff once OpenCode is available.
	if _, _, err := conn.Client(parent); err != nil {
		fmt.Fprintf(os.Stderr, "gilir: warning: %s\n", conn.Redact(opencode.Describe(err)))
	}

	var hookConfigs []hooks.Config
	if path := os.Getenv("GILIR_HOOKS"); path != "" {
		if hookConfigs, err = hooks.Load(path); err != nil {
			return fmt.Errorf("GILIR_HOOKS: %w", err)
		}
	}

	lock, err := lockFile(dbPath + ".lock")
	if errors.Is(err, errLocked) {
		return fmt.Errorf("another gilir service is already running for %s", dbPath)
	}
	if err != nil {
		return fmt.Errorf("acquire service lock: %w", err)
	}
	defer lock.Close() // released by close or process exit; the file stays

	st, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()

	source := storeSource{st}
	if err := initHookCursors(parent, source, hookConfigs); err != nil {
		return err
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

	ln, err := net.Listen("tcp", listen.address())
	if err != nil {
		return fmt.Errorf("listen on %s: %w", listen.address(), err)
	}
	// Hooks are local clients and need a dialable URL even on a wildcard bind.
	boundAddr := ln.Addr().(*net.TCPAddr)
	boundURL := boundURLFor(listen, boundAddr)

	instance := ""
	var paths servicePaths
	if managed {
		if paths, err = resolveServicePaths(); err != nil {
			ln.Close()
			return err
		}
		instance = os.Getenv(serviceInstanceEnvName)
		if instance == "" {
			if instance, err = randomHex(16); err != nil {
				ln.Close()
				return fmt.Errorf("generate service identity: %w", err)
			}
		}
	}

	// A foreground `gilir serve` stops on signals only. A managed worker also
	// listens on a private loopback listener so a public bind never exposes
	// process shutdown.
	stopRequests := make(chan struct{}, 1)
	var controlServer *http.Server
	if managed {
		controlLn, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			ln.Close()
			return fmt.Errorf("listen for service control: %w", err)
		}
		token, err := randomHex(32)
		if err != nil {
			ln.Close()
			controlLn.Close()
			return fmt.Errorf("generate service token: %w", err)
		}
		state := serviceState{
			Version:       serviceVersion,
			Instance:      instance,
			PID:           os.Getpid(),
			RequestedHost: listen.host,
			RequestedPort: requestedPort(listen.port),
			Host:          listen.host,
			Port:          boundAddr.Port,
			URL:           boundURL,
			ControlPort:   controlLn.Addr().(*net.TCPAddr).Port,
			Token:         token,
			DBPath:        dbPath,
			LogPath:       paths.Log,
			StartedAt:     time.Now().UTC(),
		}
		if err := writeServiceState(paths, state); err != nil {
			ln.Close()
			controlLn.Close()
			return err
		}
		defer removeServiceState(paths)

		controlServer = &http.Server{
			Handler:           &controlHandler{token: token, stop: stopRequests},
			ReadHeaderTimeout: 5 * time.Second,
		}
		go func() { _ = controlServer.Serve(controlLn) }()
	}

	w := watcher.NewWithConnection(st, conn)
	service := commands.New(st, func() commands.Snapshot {
		snap := w.Snapshot()
		return commands.Snapshot{
			ActiveParents: snap.ActiveParents,
			WatcherError:  snap.Error,
			Instance:      instance,
		}
	})
	server := &http.Server{
		Handler:           localhttp.Handler(web, service.Execute, commands.Classify),
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
		hooks.RunWithURL(ctx, hookConfigs, source, boundURL, func(err error) {
			fmt.Fprintf(os.Stderr, "gilir: %v\n", err)
		})
	}()
	served := make(chan error, 1)
	go func() { served <- server.Serve(ln) }()

	fmt.Fprintf(os.Stderr, "gilir: service listening on %s, local URL %s (database %s, OpenCode %s, %s, %d hooks)\n",
		ln.Addr().String(), boundURL, dbPath, opencodeEndpoint(overrides), webNote, len(hookConfigs))

	var runErr error
	select {
	case <-ctx.Done():
	case <-stopRequests:
	case err := <-served:
		runErr = fmt.Errorf("http server: %w", err)
	}
	fmt.Fprintln(os.Stderr, "gilir: service shutting down")
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
	}
	if controlServer != nil {
		controlServer.Close()
	}
	cancel()
	wg.Wait()
	return runErr
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
