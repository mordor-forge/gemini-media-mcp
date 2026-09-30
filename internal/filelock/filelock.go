// Package filelock provides an exclusive advisory lock on a file, shared by
// every process that opens the same path. The server uses it so that several
// instances (one per agent harness, or stdio next to HTTP) can check budgets
// and append to the usage ledger without racing each other.
package filelock

import (
	"fmt"
	"os"
)

// Lock is a held lock. Unlock releases it.
type Lock struct {
	f *os.File
}

// Acquire blocks until it holds an exclusive lock on path, creating the file
// (mode 0600) if needed. Holders are expected to keep the lock briefly.
func Acquire(path string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening lock file: %w", err)
	}
	if err := lock(f); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("locking %s: %w", path, err)
	}
	return &Lock{f: f}, nil
}

// Unlock releases the lock.
func (l *Lock) Unlock() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := unlock(l.f)
	if cerr := l.f.Close(); err == nil {
		err = cerr
	}
	l.f = nil
	return err
}
