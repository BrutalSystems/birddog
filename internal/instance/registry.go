// Package instance is the machine-wide index of monitoring instances.
//
// It exists so an orchestrator can find the instances on this machine. It is
// an index and nothing more: whether an instance is actually running is
// decided by its lock, never by the presence of a record here. A daemon that
// crashed leaves its record behind.
package instance

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Record describes one instance.
type Record struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	PID        int    `json:"pid"`
	SocketPath string `json:"socket_path"`
	ConfigPath string `json:"config_path,omitempty"`
	DBPath     string `json:"db_path,omitempty"`

	// Owner is the session this instance is tied to, when one was named.
	// Kept so resume restores the tie rather than silently dropping it.
	Owner     string    `json:"owner,omitempty"`
	StartedAt time.Time `json:"started_at"`

	// StoppedAt marks an instance that was shut down cleanly. Its record is
	// kept so it stays discoverable and can be resumed; its state on disk
	// outlives the process.
	StoppedAt *time.Time `json:"stopped_at,omitempty"`

	// Running is filled in by ListWithLiveness; it is never stored, because a
	// stored answer would be a claim that goes stale the moment it is written.
	Running bool `json:"-"`
}

// idBytes gives a short, collision-resistant id that is also safe as a
// filename and leaves room in a unix socket path.
const idBytes = 6

// NewID mints an instance id.
func NewID() string {
	b := make([]byte, idBytes)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand does not fail in practice; fall back to a time-derived
		// id rather than returning an unusable empty one.
		return fmt.Sprintf("bd-%x", time.Now().UnixNano())[:2*idBytes]
	}
	return "bd-" + hex.EncodeToString(b)
}

func recordPath(dir, id string) (string, error) {
	if err := validID(id); err != nil {
		return "", err
	}
	return filepath.Join(dir, id+".json"), nil
}

func validID(id string) error {
	switch {
	case id == "":
		return errors.New("instance id is empty")
	case strings.ContainsAny(id, `/\`):
		return fmt.Errorf("instance id %q contains a path separator", id)
	case id == "." || id == "..":
		return fmt.Errorf("instance id %q is not a name", id)
	}
	return nil
}

// Register writes or replaces an instance's record.
func Register(dir string, r Record) error {
	path, err := recordPath(dir, r.ID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create registry directory: %w", err)
	}

	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("encode instance record: %w", err)
	}

	// Write and rename, so a reader never sees a half-written record.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write instance record: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("write instance record: %w", err)
	}
	return nil
}

// MarkStopped records that an instance shut down, keeping its record.
//
// The record is what makes a stopped instance discoverable and resumable, so
// it is kept rather than removed. An instance that is gone entirely is removed
// with Deregister.
func MarkStopped(dir, id string, at time.Time) error {
	r, ok, err := Load(dir, id)
	if err != nil || !ok {
		return err
	}
	stopped := at.UTC()
	r.StoppedAt = &stopped
	return Register(dir, r)
}

// Load reads one instance's record.
func Load(dir, id string) (Record, bool, error) {
	path, err := recordPath(dir, id)
	if err != nil {
		return Record{}, false, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, fmt.Errorf("read instance record: %w", err)
	}

	var r Record
	if err := json.Unmarshal(data, &r); err != nil {
		return Record{}, false, fmt.Errorf("instance record %s: %w", id, err)
	}
	return r, true, nil
}

// List returns every registered instance, by id.
func List(dir string) ([]Record, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		// No registry directory means no instance has ever run here.
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read registry: %w", err)
	}

	var out []Record
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		r, ok, err := Load(dir, id)
		if err != nil || !ok {
			continue // an unreadable record is not a reason to hide the rest
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// ListWithLiveness returns every registered instance, saying which are still
// running. alive is injected so the check is testable without real processes.
func ListWithLiveness(dir string, alive func(pid int) bool) ([]Record, error) {
	records, err := List(dir)
	if err != nil {
		return nil, err
	}
	for i := range records {
		// A stopped instance is not running whatever its pid says: that pid
		// may since belong to something else entirely.
		records[i].Running = records[i].StoppedAt == nil && alive(records[i].PID)
	}
	return records, nil
}

// Deregister removes an instance's record. An instance that is already gone is
// the state the caller wanted, so absence is success.
func Deregister(dir, id string) error {
	path, err := recordPath(dir, id)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove instance record: %w", err)
	}
	return nil
}

// ProcessAlive reports whether a pid is running. Signal 0 checks for the
// process without disturbing it.
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}
