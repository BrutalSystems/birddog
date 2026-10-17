// Package claude discovers live Claude Code sessions from the registry the
// harness maintains at ~/.claude/sessions.
//
// Verified against Claude Code 2.1.267; see docs/integration-findings.md §2.
// Reading the registry mutates nothing and needs no hooks, no configuration
// change and no session restart.
package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	iofs "io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/BrutalSystems/birddog/internal/platform/proc"
)

// Session is one live Claude Code session as the registry describes it.
type Session struct {
	SessionID string
	PID       int
	ProcStart string
	CWD       string
	Name      string
	Status    string
	Kind      string

	// WaitingFor says what a waiting session is waiting on. Present only
	// while it waits; the harness removes the field on resolution.
	WaitingFor string

	Entrypoint string
	Version    string
	SocketPath string

	UpdatedAt       time.Time
	StatusUpdatedAt time.Time

	// Dir is the registry directory the record lives in. Where the record
	// was reached through a link, it is the directory of the file linked to:
	// the credential file belonging to the session is beside the real
	// record, not necessarily beside the link.
	Dir string

	// linked says the record was read through a symbolic link.
	linked bool

	// Live reports whether the session's socket answered a connect probe.
	// The registry record can outlive the process that wrote it, so this —
	// not the record's existence — is the liveness test.
	Live bool
}

// StatusIsCurrent reports whether Status may be presented as a current fact.
// When the session is not live, Status and StatusUpdatedAt are preserved as
// the last thing observed, but they describe the past.
func (s Session) StatusIsCurrent() bool { return s.Live }

// Params configures a listing. Probe and SameProcess are injected so tests
// need no real sockets and no real processes.
type Params struct {
	RegistryDir string

	// Registries, when set, is called on every listing for the directories
	// to read, and RegistryDir is ignored. See Registries.
	Registries func() []string

	// Probe reports whether a session's socket accepts a connection.
	// Defaults to ProbeSocket.
	Probe func(socketPath string) bool

	// PIDAlive reports whether any process holds pid, for a record that
	// cannot be read and so carries no start time to check. Defaults to a
	// process-table lookup.
	PIDAlive func(pid int) bool

	// SameProcess reports whether the process at pid is still the one whose
	// start time was recorded. Defaults to proc.SameProcess.
	SameProcess func(pid int, procStart string) bool
}

// recordName matches only <pid>.json. The same directory holds
// <pid>.<hash>.key files carrying a peerToken: they are valid JSON, so
// anything looser both invents sessions and reads credential material.
var recordName = regexp.MustCompile(`^\d+\.json$`)

// registryRecord mirrors the JSON the harness writes.
type registryRecord struct {
	PID                 int    `json:"pid"`
	SessionID           string `json:"sessionId"`
	CWD                 string `json:"cwd"`
	ProcStart           string `json:"procStart"`
	Version             string `json:"version"`
	Kind                string `json:"kind"`
	Entrypoint          string `json:"entrypoint"`
	MessagingSocketPath string `json:"messagingSocketPath"`
	Name                string `json:"name"`
	Status              string `json:"status"`
	WaitingFor          string `json:"waitingFor"`
	UpdatedAt           int64  `json:"updatedAt"`
	StatusUpdatedAt     int64  `json:"statusUpdatedAt"`
}

// vanished decides whether a record that could not be opened is gone, or only
// unreadable from here.
//
// "No such file" on a record says a path does not resolve now. It does not on
// its own say the session ended: a link can outlive a renamed or unmounted
// directory while the process it describes runs on, and treating that as
// absence is how an owner gets counted gone and its instance stopped. So it is
// gone only on evidence:
//
//   - the directory entry itself is no longer there — the session ended
//     between the directory being listed and the record being opened, which
//     covers a link removed after it was resolved; or
//   - it is a link whose name gives a pid no process holds. With the record
//     unreadable there is no start time to check, so a live pid is taken as a
//     session that may be running: a reused pid then costs a partial listing,
//     which is the safe direction.
//
// Anything else is reported, with the reason.
func vanished(path string, linked bool, name string, pidAlive func(int) bool) (bool, error) {
	if _, err := os.Lstat(path); errors.Is(err, iofs.ErrNotExist) {
		return true, nil
	}
	if !linked {
		// A plain file that is still listed and cannot be opened as missing
		// is not explained by anything; report it.
		return false, fmt.Errorf("listed but cannot be opened")
	}
	pid, err := strconv.Atoi(strings.TrimSuffix(name, ".json"))
	if err != nil {
		return false, fmt.Errorf("link to a missing record, with no pid to check")
	}
	if !pidAlive(pid) {
		return true, nil
	}
	return false, fmt.Errorf("link to a missing record, for pid %d, which is running", pid)
}

// DefaultRegistryDir is where Claude Code 2.1.267 maintains its live session
// registry.
func DefaultRegistryDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "sessions"), nil
}

// List returns the sessions described by the registry directory, or by every
// directory Registries names.
//
// With Registries set, an error that IsPartial comes with sessions that are
// real and may be incomplete.
func List(p Params) ([]Session, error) {
	if p.Registries != nil {
		return listMany(p, p.Registries())
	}
	return listDir(p, p.RegistryDir)
}

func listDir(p Params, dir string) ([]Session, error) {
	probe := p.Probe
	if probe == nil {
		probe = ProbeSocket
	}
	sameProcess := p.SameProcess
	if sameProcess == nil {
		sameProcess = proc.SameProcess
	}
	pidAlive := p.PIDAlive
	if pidAlive == nil {
		pidAlive = func(pid int) bool { _, err := proc.StartTime(pid); return err == nil }
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	// A record that could not be read is a session this listing cannot rule
	// out. Skipping it silently made an incomplete listing look complete,
	// and an owner whose record was unreadable was counted absent — three
	// times, and its instance stopped. Only a record that is not there any
	// more is skipped without saying so: the session ended between the
	// directory being listed and the file being opened, or a link outlived
	// the record it pointed at, and neither describes a session.
	unreadable := map[string]error{}
	var out []Session
	for _, e := range entries {
		if !recordName.MatchString(e.Name()) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		recordDir, linked := dir, e.Type()&os.ModeSymlink != 0
		var b []byte
		var err error
		if linked {
			var real string
			if real, err = filepath.EvalSymlinks(path); err == nil {
				recordDir = filepath.Dir(real)
				b, err = os.ReadFile(path)
			}
		} else {
			b, err = os.ReadFile(path)
		}
		if errors.Is(err, iofs.ErrNotExist) {
			if gone, why := vanished(path, linked, e.Name(), pidAlive); !gone {
				unreadable[path] = why
			}
			continue
		}
		if err != nil {
			unreadable[path] = err
			continue
		}
		var r registryRecord
		if err := json.Unmarshal(b, &r); err != nil {
			// Possibly caught mid-write. Either way it is a session this
			// pass cannot see, not one that is not there.
			unreadable[path] = fmt.Errorf("not a readable record: %w", err)
			continue
		}
		out = append(out, Session{
			SessionID:       r.SessionID,
			PID:             r.PID,
			ProcStart:       r.ProcStart,
			CWD:             r.CWD,
			Name:            r.Name,
			Status:          r.Status,
			WaitingFor:      r.WaitingFor,
			Kind:            r.Kind,
			Entrypoint:      r.Entrypoint,
			Version:         r.Version,
			SocketPath:      r.MessagingSocketPath,
			UpdatedAt:       time.UnixMilli(r.UpdatedAt),
			StatusUpdatedAt: time.UnixMilli(r.StatusUpdatedAt),
			Dir:             recordDir,
			linked:          linked,
			// Live requires both: a socket file can outlive the process
			// that bound it, and the PID can meanwhile be reused.
			Live: probe(r.MessagingSocketPath) && sameProcess(r.PID, r.ProcStart),
		})
	}
	if len(unreadable) > 0 {
		return out, &PartialError{Unreadable: unreadable}
	}
	return out, nil
}
