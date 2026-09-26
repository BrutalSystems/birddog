package tincan

import (
	"strings"
	"testing"
	"time"
)

// probeReturning stands in for the installed binary. stderr is still returned
// because a refusal quotes it, but nothing keys on it any more: tincan 2.0.0
// made `send --help` exit 0 as a capability signal, and an exit code is a
// contract in a way its usage prose never was.
func probeReturning(code int, stderr string) Runner {
	return func(time.Duration, string, ...string) ([]byte, []byte, int, error) {
		return nil, []byte(stderr), code, nil
	}
}

// probeRecording answers like a tincan 2.0.0 and keeps the argv it was asked
// to run, so a test can pin *what* is probed and not only the answer.
func probeRecording(argv *[]string) Runner {
	return func(_ time.Duration, name string, args ...string) ([]byte, []byte, int, error) {
		*argv = append([]string{name}, args...)
		return nil, nil, 0, nil
	}
}

// The probe is `send --help`, and tincan documents its exit code as the
// capability check. Probing bare `send` instead would go back to reading
// usage prose, which is what this replaced.
func TestProbeAsksSendHelp(t *testing.T) {
	var argv []string
	if _, err := newConnector(map[string]string{"peer": "orchestrator"}, probeRecording(&argv)); err != nil {
		t.Fatalf("newConnector: %v", err)
	}
	want := []string{"tincan", "send", "--help"}
	if len(argv) != len(want) {
		t.Fatalf("argv = %q, want %q", argv, want)
	}
	for i := range want {
		if argv[i] != want[i] {
			t.Fatalf("argv = %q, want %q", argv, want)
		}
	}
}

func TestBuildAcceptsATincanWhoseSendHelpExitsZero(t *testing.T) {
	if _, err := newConnector(
		map[string]string{"peer": "orchestrator"},
		probeReturning(0, ""),
	); err != nil {
		t.Fatalf("newConnector: %v", err)
	}
}

// The tincan on PATH is installed separately and cannot be version-pinned by
// birddog, so every build older than the capability signal has to be refused
// at startup rather than discovered at the first alert.
//
// Both rows matter and they fail differently. 1.9.2 has no `send` at all.
// 1.10.1 has `send` and delivers, but predates `duplicate_peer_moved` — and
// the connector now reports `duplicate_send` as delivered, which is only true
// once that refusal exists to carry the other case. Accepting 1.10.1 would
// make birddog claim a worker was told something it may never have seen.
func TestBuildRefusesEveryTincanOlderThanTheCapabilitySignal(t *testing.T) {
	// The second row carries stderr without the phrase the old probe matched
	// on. That is the regression this guards: refusal must follow from the
	// exit code alone, so a build that reworded its usage text — or never
	// used that wording — is still refused rather than silently accepted.
	for _, tc := range []struct {
		version string
		code    int
		stderr  string
	}{
		{"1.9.2, no send subcommand", 2, "tincan: unrecognised argument 'send'"},
		{"older build, send present but --help not a signal", 64, "tincan send: --to is required."},
	} {
		t.Run(tc.version, func(t *testing.T) {
			_, err := newConnector(
				map[string]string{"peer": "orchestrator"},
				probeReturning(tc.code, tc.stderr),
			)
			if err == nil {
				t.Fatal("newConnector accepted a tincan older than the capability signal")
			}
			if !strings.Contains(err.Error(), "send") {
				t.Errorf("err = %v, want it to name the subcommand", err)
			}
		})
	}
}

func TestPeerIsRequired(t *testing.T) {
	_, err := newConnector(map[string]string{}, probeReturning(0, ""))
	if err == nil || !strings.Contains(err.Error(), "peer") {
		t.Errorf("err = %v, want it to require peer", err)
	}
}

// An operator who configured a route is entitled to be told a setting does
// not exist, rather than having it silently ignored.
func TestUnknownSettingIsRefused(t *testing.T) {
	_, err := newConnector(
		map[string]string{"peer": "o", "sesion_id": "typo"},
		probeReturning(0, ""),
	)
	if err == nil || !strings.Contains(err.Error(), "sesion_id") {
		t.Errorf("err = %v, want the unknown setting named", err)
	}
}

func TestBinaryIsConfigurable(t *testing.T) {
	c, err := newConnector(
		map[string]string{"peer": "o", "binary": "/opt/tincan/bin/tincan"},
		probeReturning(0, ""),
	)
	if err != nil {
		t.Fatalf("newConnector: %v", err)
	}
	if c.Binary != "/opt/tincan/bin/tincan" {
		t.Errorf("Binary = %q", c.Binary)
	}
}
