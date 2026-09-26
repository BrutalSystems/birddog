//go:build darwin || linux

// Package lockfile gives one instance exclusive ownership of its state.
//
// Two daemons for the same instance would write the same database and
// duplicate every alert, so startup takes a lock first. The lock is an
// advisory flock held on an open descriptor — not the file's existence — which
// is what makes it survive a crash correctly: the kernel drops it when the
// holder dies, so a stale file never leaves an instance unstartable.
package lockfile

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// ErrHeld reports that another process owns this instance.
var ErrHeld = errors.New("lock is held by another process")

// IsHeld reports whether an error means the lock was already taken, as opposed
// to something having gone wrong.
func IsHeld(err error) bool { return errors.Is(err, ErrHeld) }

// Lock is a held instance lock.
type Lock struct {
	f    *os.File
	path string
}

// Acquire takes the lock, or returns an error wrapping ErrHeld.
func Acquire(path string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			if pid, ok := HolderPID(path); ok {
				return nil, fmt.Errorf("%w (pid %d)", ErrHeld, pid)
			}
			return nil, ErrHeld
		}
		return nil, fmt.Errorf("lock: %w", err)
	}

	// Record the holder so an operator told "already running" can find it.
	// Truncate first: a previous holder's pid may be longer than ours.
	if err := f.Truncate(0); err != nil {
		f.Close()
		return nil, fmt.Errorf("lock: %w", err)
	}
	if _, err := f.WriteAt([]byte(strconv.Itoa(os.Getpid())), 0); err != nil {
		f.Close()
		return nil, fmt.Errorf("lock: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return nil, fmt.Errorf("lock: %w", err)
	}

	return &Lock{f: f, path: path}, nil
}

// Release drops the lock and removes the file.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	// Remove before unlocking: while the lock is still held, no one else can
	// be mid-acquire on this path.
	_ = os.Remove(l.path)
	err := syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	if cerr := l.f.Close(); err == nil {
		err = cerr
	}
	l.f = nil
	return err
}

// HolderPID reads the pid recorded in a lock file.
//
// It is for diagnostics only. The file may be stale — a dead holder leaves it
// behind — so its presence proves nothing about whether the lock is held.
func HolderPID(path string) (int, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}
