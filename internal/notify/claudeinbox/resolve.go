package claudeinbox

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/BrutalSystems/birddog/internal/discover/claude"
	"github.com/BrutalSystems/birddog/internal/notify"
)

func init() {
	notify.Register(Kind, func(o notify.Options) (notify.Notifier, error) {
		return newConnector(o.Settings)
	})
}

// settingSessionID names the orchestrator session alerts are delivered to.
const settingSessionID = "session_id"

// newConnector builds the connector from its configuration.
func newConnector(settings map[string]string) (*Connector, error) {
	for key := range settings {
		if key != settingSessionID {
			return nil, fmt.Errorf("unknown setting %q; this connector takes only %q", key, settingSessionID)
		}
	}
	sessionID := settings[settingSessionID]
	if sessionID == "" {
		return nil, fmt.Errorf("%q is required: it names the orchestrator session alerts go to", settingSessionID)
	}

	registryDir, err := claude.DefaultRegistryDir()
	if err != nil {
		return nil, err
	}
	return &Connector{
		// Resolved per delivery: a session that restarts gets a new pid,
		// socket and token, and a recipient captured at startup would point
		// at a session that no longer exists.
		Resolve: func() (Recipient, error) { return ResolveSession(registryDir, sessionID) },
	}, nil
}

// keyFileName matches the credential files the harness writes beside each
// session record: <pid>.<hash>.key.
var keyFileName = regexp.MustCompile(`^(\d+)\.[0-9a-f]+\.key$`)

// ResolveSession finds where to deliver, and what to authenticate with.
//
// The peer token is read here and nowhere else in birddog. Discovery
// deliberately refuses to touch these files; this is the one component with a
// reason to, and the token goes straight onto the wire — never into an event,
// a log line or an alert body.
func ResolveSession(registryDir, sessionID string) (Recipient, error) {
	sessions, err := claude.List(claude.Params{
		RegistryDir: registryDir,
		// Resolution needs the record, not a liveness verdict: an unreachable
		// session produces a delivery failure, which is a better report than
		// a resolution failure.
		Probe:       func(string) bool { return true },
		SameProcess: func(int, string) bool { return true },
	})
	if err != nil {
		return Recipient{}, fmt.Errorf("read Claude Code session registry: %w", err)
	}

	for _, s := range sessions {
		if s.SessionID != sessionID {
			continue
		}
		token, domain, err := readPeerToken(registryDir, s.PID)
		if err != nil {
			return Recipient{}, err
		}
		return Recipient{
			SocketPath: s.SocketPath,
			PeerToken:  token,
			ProcStart:  s.ProcStart,
			PIDDomain:  domain,
		}, nil
	}
	return Recipient{}, fmt.Errorf("orchestrator session %q: %w", sessionID, errNoSession)
}

// readPeerToken reads the credential the session will accept.
func readPeerToken(registryDir string, pid int) (token, pidDomain string, err error) {
	entries, err := os.ReadDir(registryDir)
	if err != nil {
		return "", "", fmt.Errorf("read session registry: %w", err)
	}

	want := strconv.Itoa(pid)
	for _, e := range entries {
		match := keyFileName.FindStringSubmatch(e.Name())
		if match == nil || match[1] != want {
			continue
		}

		data, err := os.ReadFile(filepath.Join(registryDir, e.Name()))
		if err != nil {
			return "", "", fmt.Errorf("read peer token: %w", err)
		}
		var body struct {
			PeerToken string `json:"peerToken"`
			PIDDomain string `json:"pidDomain"`
		}
		if err := json.Unmarshal(data, &body); err != nil {
			// Deliberately not quoting the file's contents into the error.
			return "", "", fmt.Errorf("peer token file for pid %d is unreadable", pid)
		}
		if strings.TrimSpace(body.PeerToken) == "" {
			return "", "", fmt.Errorf("peer token file for pid %d carries no token", pid)
		}
		return body.PeerToken, body.PIDDomain, nil
	}

	return "", "", fmt.Errorf(
		"no peer token for pid %d: birddog cannot authenticate to that session", pid)
}
