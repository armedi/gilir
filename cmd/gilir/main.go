// Command gilir is the Gilir CLI and background service.
//
// The service is the only process that opens SQLite. "gilir serve" runs it in
// the foreground; "gilir service start" launches the same work as a detached
// managed process. Every ordinary command and host-local queue control sends
// one POST to that service's HTTP command API, so help, skill and command
// failures never create or open SQLite.
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/armedi/gilir/internal/cli"
)

func main() {
	// The lifecycle commands re-exec this binary as a detached worker. The
	// marker is checked before CLI parsing so that path is private and cannot
	// recurse into another launcher.
	if len(os.Args) == 2 && os.Args[1] == serviceWorkerCommand {
		if err := runServiceWorker(context.Background()); err != nil {
			fmt.Fprintf(os.Stderr, "gilir: %v\n", err)
			os.Exit(1)
		}
		return
	}
	os.Exit(cli.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, strings.TrimSpace(os.Getenv("GILIR_ACTOR")), execute))
}

// execute routes one parsed CLI command. `serve` runs in-process; service
// lifecycle and queue-control commands use local state; every ordinary command
// is sent to the service at GILIR_URL, so help, skill and command failures
// never create or open SQLite.
func execute(ctx context.Context, command, actor string, args map[string]any) (any, error) {
	switch command {
	case "serve":
		host, err := hostArg(args)
		if err != nil {
			return nil, err
		}
		port, err := portArg(args)
		if err != nil {
			return nil, err
		}
		db, err := dbArg(args)
		if err != nil {
			return nil, err
		}
		return serveForeground(ctx, host, port, db)
	case "service.start":
		host, err := hostArg(args)
		if err != nil {
			return nil, err
		}
		port, err := portArg(args)
		if err != nil {
			return nil, err
		}
		db, err := dbArg(args)
		if err != nil {
			return nil, err
		}
		return startService(ctx, host, port, db)
	case "service.stop":
		return stopService(ctx)
	case "service.restart":
		host, err := hostArg(args)
		if err != nil {
			return nil, err
		}
		port, err := portArg(args)
		if err != nil {
			return nil, err
		}
		db, err := dbArg(args)
		if err != nil {
			return nil, err
		}
		return restartService(ctx, host, port, db)
	case "service.status":
		return queryServiceStatus(ctx)
	case "service.pause":
		return updateServiceQueue(ctx, actor, map[string]any{"paused": true}, "pause")
	case "service.resume":
		return updateServiceQueue(ctx, actor, map[string]any{"paused": false}, "resume")
	case "service.concurrency":
		return updateServiceQueue(ctx, actor, map[string]any{"max_concurrency": args["max_concurrency"]}, "concurrency")
	}
	return postCommand(ctx, command, actor, args)
}

// dbArg returns the optional --db value, trimmed. A present but blank value is
// rejected so it never silently falls back to the default database.
func dbArg(args map[string]any) (string, error) {
	value, ok := args["db"]
	if !ok {
		return "", nil
	}
	db, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("invalid database path %v", value)
	}
	db = strings.TrimSpace(db)
	if db == "" {
		return "", errors.New("flag --db requires a non-empty value")
	}
	return db, nil
}

// hostArg returns the optional --host value, leaving it empty so the service
// uses its default loopback host or the running instance's address on restart.
func hostArg(args map[string]any) (string, error) {
	value, ok := args["host"]
	if !ok {
		return "", nil
	}
	host, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("invalid host %v", value)
	}
	return host, nil
}

// portArg returns the optional --port value as a decimal string, leaving it
// empty so the service uses its default port or the stored restart address.
func portArg(args map[string]any) (string, error) {
	value, ok := args["port"]
	if !ok {
		return "", nil
	}
	port, ok := value.(int)
	if !ok {
		return "", fmt.Errorf("invalid port %v", value)
	}
	return strconv.Itoa(port), nil
}

// prepareDatabase resolves the database path, creates its parent directory
// (0700) and the database file (0600) when missing, and returns the
// canonical path. An existing file keeps its mode; SQLite gives the -wal and
// -shm sidecars the main file's permissions.
func prepareDatabase(override string) (string, error) {
	path, err := databasePath(override)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create database directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	switch {
	case err == nil:
		if err := f.Close(); err != nil {
			return "", fmt.Errorf("create database: %w", err)
		}
	case !errors.Is(err, fs.ErrExist):
		return "", fmt.Errorf("create database: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve database path: %w", err)
	}
	return canonical, nil
}

// databasePath returns the absolute database path from the --db override,
// GILIR_DB, or the platform data directory.
func databasePath(override string) (string, error) {
	if override != "" {
		return filepath.Abs(override)
	}
	if path := os.Getenv("GILIR_DB"); path != "" {
		return filepath.Abs(path)
	}
	dir, err := dataDir()
	if err != nil {
		return "", fmt.Errorf("locate data directory (set GILIR_DB): %w", err)
	}
	return filepath.Join(dir, "gilir", "gilir.sqlite"), nil
}

func dataDir() (string, error) {
	if runtime.GOOS == "windows" {
		if dir := os.Getenv("LOCALAPPDATA"); filepath.IsAbs(dir) {
			return dir, nil
		}
		return os.UserConfigDir()
	}
	if dir := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(dir) {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share"), nil
}
