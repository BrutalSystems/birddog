package tincan

import (
	"strings"
	"testing"
	"time"
)

// The real binaries write this to stderr, not stdout — an earlier probe read
// stdout, where neither writes anything, and accepted a tincan without send.
func probeReturning(code int, stderr string) Runner {
	return func(time.Duration, string, ...string) ([]byte, []byte, int, error) {
		return nil, []byte(stderr), code, nil
	}
}

// The tincan on PATH is installed separately and cannot be version-pinned by
// birddog. 1.9.2 — the released version at the time this was written — has no
// `send` subcommand at all, so a connector built against it would accept the
// configuration and fail on the first alert.
func TestBuildRefusesATincanWithoutSend(t *testing.T) {
	_, err := newConnector(
		map[string]string{"peer": "orchestrator"},
		probeReturning(2, "tincan: unrecognised argument 'send'"),
	)
	if err == nil {
		t.Fatal("newConnector accepted a tincan with no send subcommand")
	}
	if !strings.Contains(err.Error(), "send") {
		t.Errorf("err = %v, want it to name the missing subcommand", err)
	}
}

func TestBuildAcceptsATincanWithSend(t *testing.T) {
	if _, err := newConnector(
		map[string]string{"peer": "orchestrator"},
		probeReturning(64, "tincan send: --to is required."),
	); err != nil {
		t.Fatalf("newConnector: %v", err)
	}
}

func TestPeerIsRequired(t *testing.T) {
	_, err := newConnector(map[string]string{}, probeReturning(64, "tincan send: --to is required."))
	if err == nil || !strings.Contains(err.Error(), "peer") {
		t.Errorf("err = %v, want it to require peer", err)
	}
}

// An operator who configured a route is entitled to be told a setting does
// not exist, rather than having it silently ignored.
func TestUnknownSettingIsRefused(t *testing.T) {
	_, err := newConnector(
		map[string]string{"peer": "o", "sesion_id": "typo"},
		probeReturning(64, "tincan send: --to is required."),
	)
	if err == nil || !strings.Contains(err.Error(), "sesion_id") {
		t.Errorf("err = %v, want the unknown setting named", err)
	}
}

func TestBinaryIsConfigurable(t *testing.T) {
	c, err := newConnector(
		map[string]string{"peer": "o", "binary": "/opt/tincan/bin/tincan"},
		probeReturning(64, "tincan send: --to is required."),
	)
	if err != nil {
		t.Fatalf("newConnector: %v", err)
	}
	if c.Binary != "/opt/tincan/bin/tincan" {
		t.Errorf("Binary = %q", c.Binary)
	}
}
