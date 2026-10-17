// Package claude discovers live Claude Code sessions from the registry the
// harness maintains at ~/.claude/sessions.
//
// Verified against Claude Code 2.1.267; see docs/integration-findings.md §2.
// Reading the registry mutates nothing and needs no hooks, no configuration
// change and no session restart.
package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
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

	// Dir is the registry directory the record was read from. The
	// credential file beside it is there, not in any other profile's.
	Dir string

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

	// RegistryDirs, when set, is read instead of RegistryDir: every
	// profile's registry at once. See DefaultRegistryDirs.
	RegistryDirs []string

	// Probe reports whether a session's socket accepts a connection.
	// Defaults to ProbeSocket.
	Probe func(socketPath string) bool

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

// DefaultRegistryDir is where Claude Code 2.1.267 maintains its live session
// registry.
func DefaultRegistryDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "sessions"), nil
}

// DefaultRegistryDirs lists every Claude Code config profile's session
// registry under the home directory: ~/.claude/sessions and each
// ~/.claude-<profile>/sessions.
//
// Claude Code writes its registry under $CLAUDE_CONFIG_DIR. Reading only
// ~/.claude/sessions made every session of another profile invisible to a
// running instance, to --owner and to the claude-inbox route, unless it also
// happened to be mirrored there — so whether a session could be watched
// depended on how it had been launched.
func DefaultRegistryDirs() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dirs, err := filepath.Glob(filepath.Join(home, ".claude*", "sessions"))
	if err != nil {
		return nil, err
	}
	return dirs, nil
}

// List returns the sessions described by the registry directories.
//
// A process recorded in more than one directory is one session, and the
// record most recently written describes it.
func List(p Params) ([]Session, error) {
	dirs := p.RegistryDirs
	if len(dirs) == 0 {
		return listDir(p, p.RegistryDir)
	}
	var out []Session
	at := map[int]int{}
	read := 0
	var firstErr error
	for _, dir := range dirs {
		ss, err := listDir(p, dir)
		if err != nil {
			// One profile with no registry is ordinary; all of them
			// unreadable is not, and is reported below.
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		read++
		for _, s := range ss {
			if i, ok := at[s.PID]; ok && s.PID != 0 {
				if s.UpdatedAt.After(out[i].UpdatedAt) {
					out[i] = s
				}
				continue
			}
			at[s.PID] = len(out)
			out = append(out, s)
		}
	}
	if read == 0 && firstErr != nil {
		return nil, firstErr
	}
	return out, nil
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

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var out []Session
	for _, e := range entries {
		if !recordName.MatchString(e.Name()) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var r registryRecord
		if err := json.Unmarshal(b, &r); err != nil {
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
			Dir:             dir,
			// Live requires both: a socket file can outlive the process
			// that bound it, and the PID can meanwhile be reused.
			Live: probe(r.MessagingSocketPath) && sameProcess(r.PID, r.ProcStart),
		})
	}
	return out, nil
}
