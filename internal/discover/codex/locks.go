// Package codex discovers live Codex threads.
//
// Verified against codex-cli 0.155.1; see docs/providers.md#codex. The App
// Server does not serve external observers on this version, so liveness comes
// from the thread writer locks and state comes from a separately spawned
// app-server child.
package codex

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// LockHolder reports the pid of the process holding a lock file, if any.
type LockHolder func(lockPath string) (pid int, held bool)

// LockParams configures a liveness scan. LockHolder is injected so tests need
// no real locked files.
type LockParams struct {
	LockDir    string
	LockHolder LockHolder
}

// Holder identifies the process keeping a thread alive.
type Holder struct {
	PID int
}

const lockSuffix = ".lock"

// DefaultLockDir is where codex-cli 0.155.1 keeps thread writer locks.
//
// CODEX_HOME is honoured because Codex honours it: a developer pointed at an
// alternate home must not silently be scanning the default one.
func DefaultLockDir() (string, error) {
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		home = filepath.Join(userHome, ".codex")
	}
	return filepath.Join(home, "thread-writer-locks"), nil
}

// threadIDFromLockPath recovers the thread UUID from its lock file name.
func threadIDFromLockPath(path string) string {
	return strings.TrimSuffix(filepath.Base(path), lockSuffix)
}

// LiveThreads returns the threads whose writer lock is held by a running
// process, keyed by thread id.
//
// A lock file on its own proves nothing: one left behind by a crashed process
// looks identical. Only the holder makes a thread live.
func LiveThreads(p LockParams) (map[string]Holder, error) {
	live := map[string]Holder{}

	entries, err := os.ReadDir(p.LockDir)
	if err != nil {
		// No lock directory means Codex has never run here: zero threads,
		// which is an answer rather than a failure.
		if errors.Is(err, fs.ErrNotExist) {
			return live, nil
		}
		return nil, err
	}

	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, lockSuffix) || name == lockSuffix {
			continue
		}
		path := filepath.Join(p.LockDir, name)
		pid, held := p.LockHolder(path)
		if !held {
			continue
		}
		live[threadIDFromLockPath(path)] = Holder{PID: pid}
	}
	return live, nil
}
