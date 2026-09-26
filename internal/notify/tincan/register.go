package tincan

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/BrutalSystems/birddog/internal/notify"
)

func init() {
	notify.Register(Kind, func(o notify.Options) (notify.Notifier, error) {
		return newConnector(o.Settings, execRunner)
	})
}

// Settings this connector takes.
const (
	settingPeer   = "peer"
	settingBinary = "binary"
)

// probeTimeout bounds the capability check. It prints usage and exits, so it
// is fast or it is broken.
const probeTimeout = 5 * time.Second

// newConnector builds the connector and refuses a tincan that cannot serve it.
//
// The probe is here rather than at the first alert because tincan is a
// separately installed binary birddog cannot version-pin, and builds older
// than 2.0.0 cannot serve this connector — 1.9.2 has no `send` subcommand at
// all. An operator who configured this route is entitled to be told at
// startup that it cannot work, not to discover it when the first worker
// blocks.
func newConnector(settings map[string]string, run Runner) (*Connector, error) {
	var unknown []string
	for key := range settings {
		if key != settingPeer && key != settingBinary {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("unknown setting(s) %s; this connector takes %q and %q",
			strings.Join(unknown, ", "), settingPeer, settingBinary)
	}

	peer := settings[settingPeer]
	if peer == "" {
		return nil, fmt.Errorf("%q is required: it names the orchestrator alerts go to, as tincan lists it", settingPeer)
	}

	binary := settings[settingBinary]
	if binary == "" {
		binary = "tincan"
	}

	if err := probe(run, binary); err != nil {
		return nil, err
	}
	return &Connector{Peer: peer, Binary: binary, Run: run}, nil
}

// probe checks that this tincan is one birddog can deliver through.
//
// tincan 2.0.0 made `send --help` exit 0 for exactly this, and documents the
// exit code as the capability check; every older build exits non-zero on the
// unrecognised flag. So a zero exit here is the whole signal.
//
// It used to match `unrecognised argument` on stderr, because 1.9.2 and
// 1.10.1 both exited non-zero from a bare `send` and only the prose told them
// apart. That text was tincan's documented unknownArgText — documentation,
// not an interface — and a rewording would have silently turned this probe
// into one that accepts a tincan that cannot deliver.
//
// Refusing every older build is deliberate and not only about `send`. The
// connector now reports `duplicate_send` as delivered, which is only true
// where `duplicate_peer_moved` exists to carry the case where the peer name
// moved; 1.10.1 has the subcommand but not that refusal, so accepting it
// would make birddog claim a worker was told something it may never have
// seen. The two changes are one change.
func probe(run Runner, binary string) error {
	_, stderr, code, err := run(probeTimeout, binary, "send", "--help")
	if err != nil {
		return fmt.Errorf("cannot run %q: %w — install tincan, or name it with the %q setting",
			binary, err, settingBinary)
	}
	if code != 0 {
		return fmt.Errorf(
			"the installed %q does not answer `send --help` (exit %d: %s), so it predates tincan 2.0.0 "+
				"and birddog cannot deliver through it; upgrade tincan, or use the claude-inbox "+
				"connector for a Claude Code orchestrator",
			binary, code, strings.TrimSpace(string(stderr)))
	}
	return nil
}

// execRunner runs the real CLI, bounded.
//
// Both streams are captured separately: the result line is on stdout, the
// diagnostics and the probe's answer are on stderr, and merging them would
// break the parse.
func execRunner(timeout time.Duration, name string, args ...string) ([]byte, []byte, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err := cmd.Run()

	if ctx.Err() != nil {
		// Killed at the deadline: no exit status to report, and whether the
		// send landed is unknown.
		return out.Bytes(), errOut.Bytes(), -1, fmt.Errorf("timed out after %s", timeout)
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return out.Bytes(), errOut.Bytes(), exit.ExitCode(), nil
	}
	if err != nil {
		return out.Bytes(), errOut.Bytes(), -1, err
	}
	return out.Bytes(), errOut.Bytes(), 0, nil
}
