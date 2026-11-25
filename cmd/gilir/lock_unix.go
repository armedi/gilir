//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package main

import (
	"errors"
	"os"
	"syscall"
)

// lockFile takes a non-blocking exclusive flock on path. The kernel releases
// it when the returned file is closed or the process dies.
func lockFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errLocked
		}
		return nil, err
	}
	return f, nil
}
