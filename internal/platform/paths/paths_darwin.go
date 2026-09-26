//go:build darwin

// Package paths decides where birddog keeps its own state.
//
// Two locations, for one platform reason. Durable state goes in the user's
// application-support directory, where it belongs and where it is safe from a
// clean checkout. The IPC socket cannot: macOS caps sockaddr_un.sun_path at
// 104 bytes, and the application-support path consumes most of that before an
// instance id is even added. So sockets live in a short directory of their own.
package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// stateDirEnv overrides where durable state is kept, for tests and for
// operators who want it elsewhere.
const stateDirEnv = "BIRDDOG_STATE_DIR"

// MaxInstanceIDLen bounds an instance id so its socket path always fits within
// the platform limit. Ids are birddog's to mint, so this is a design choice
// rather than a restriction on anyone.
const MaxInstanceIDLen = 24

// sunPathMax is the macOS limit on a unix socket path, including its
// terminator. Exceeding it fails at bind with a bare "invalid argument".
const sunPathMax = 104

// StateDir returns the directory holding birddog's durable state.
func StateDir() (string, error) {
	if override := os.Getenv(stateDirEnv); override != "" {
		return override, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home directory: %w", err)
	}
	return filepath.Join(home, "Library", "Application Support", "birddog"), nil
}

// EnsureStateDir creates the state directory if needed and returns it.
//
// Owner-only: it records what the operator's sessions were doing, which is
// nobody else's business on a shared machine.
func EnsureStateDir() (string, error) {
	dir, err := StateDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create state directory: %w", err)
	}
	// MkdirAll respects umask, so set the mode explicitly.
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("restrict state directory: %w", err)
	}
	return dir, nil
}

// InstanceDB returns the database path for one monitoring instance.
//
// It is kept well away from repositories and worktrees, so instance state is
// never committed, scanned by a watcher, or lost to a clean checkout.
func InstanceDB(instanceID string) (string, error) {
	if err := validInstanceID(instanceID); err != nil {
		return "", err
	}
	dir, err := instancesDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, instanceID+".db"), nil
}

// instancesDir holds one database and one lock per instance.
func instancesDir() (string, error) {
	dir, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "instances"), nil
}

// EnsureInstanceDir creates the per-instance directory, owner-only.
//
// It exists because the instance lock is taken before the store opens, so
// nothing else has created the directory by the time it is needed.
func EnsureInstanceDir() (string, error) {
	dir, err := instancesDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create instance directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("restrict instance directory: %w", err)
	}
	return dir, nil
}

// InstanceLock returns the lock path for one instance, beside its database.
func InstanceLock(instanceID string) (string, error) {
	if err := validInstanceID(instanceID); err != nil {
		return "", err
	}
	dir, err := instancesDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, instanceID+".lock"), nil
}

// socketDir is short by necessity — see the package comment. It is per-uid so
// two operators on one machine do not collide.
func socketDir() string {
	return filepath.Join("/tmp", fmt.Sprintf("birddog-%d", os.Getuid()))
}

// SocketPath returns the IPC socket path for an instance.
func SocketPath(instanceID string) (string, error) {
	if err := validInstanceID(instanceID); err != nil {
		return "", err
	}
	path := filepath.Join(socketDir(), instanceID+".sock")
	if len(path) >= sunPathMax {
		// Unreachable given MaxInstanceIDLen, but a silent bind failure here
		// is unhelpful enough to be worth ruling out explicitly.
		return "", fmt.Errorf("socket path %d bytes, limit %d: %s", len(path), sunPathMax, path)
	}
	return path, nil
}

// EnsureSocketDir creates the socket directory, owner-only.
func EnsureSocketDir() (string, error) {
	dir := socketDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create socket directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("restrict socket directory: %w", err)
	}
	return dir, nil
}

// validInstanceID refuses anything that could name a file outside the
// directories above, or overrun the socket limit.
func validInstanceID(id string) error {
	switch {
	case id == "":
		return fmt.Errorf("instance id is empty")
	case len(id) > MaxInstanceIDLen:
		return fmt.Errorf("instance id %q is %d bytes, limit %d", id, len(id), MaxInstanceIDLen)
	case strings.ContainsAny(id, `/\`):
		return fmt.Errorf("instance id %q contains a path separator", id)
	case id == "." || id == "..":
		return fmt.Errorf("instance id %q is not a name", id)
	}
	return nil
}
