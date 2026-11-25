// Command gilir is the Gilir CLI and local daemon.
//
// Ordinary commands run directly against the local SQLite database, so they
// work while the daemon is stopped. "gilir daemon run" serves the loopback
// HTTP API and web UI and runs the queue watcher and hooks; "gilir daemon
// status" asks that running daemon for its live state.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"

	"github.com/armedi/gilir/internal/cli"
	"github.com/armedi/gilir/internal/commands"
	"github.com/armedi/gilir/internal/store"
)

func main() {
	os.Exit(cli.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, os.Getenv("GILIR_ACTOR"), execute))
}

// execute routes one parsed CLI command. The store is opened only here, so
// help and skill output never touch the database.
func execute(ctx context.Context, command, actor string, args map[string]any) (any, error) {
	switch command {
	case "daemon.run":
		return runDaemon(ctx)
	case "daemon.status":
		return daemonStatus(ctx)
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("encode arguments: %w", err)
	}
	dbPath, err := prepareDatabase()
	if err != nil {
		return nil, err
	}
	st, err := store.Open(dbPath)
	if err != nil {
		return nil, err
	}
	defer st.Close()
	return commands.New(st, nil).Execute(ctx, command, actor, raw)
}

// prepareDatabase resolves the database path, creates its parent directory
// (0700) and the database file (0600) when missing, and returns the
// canonical path. An existing file keeps its mode; SQLite gives the -wal and
// -shm sidecars the main file's permissions.
func prepareDatabase() (string, error) {
	path, err := databasePath()
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

// databasePath returns the absolute database path from GILIR_DB or the
// platform data directory.
func databasePath() (string, error) {
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
