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
// separately installed binary birddog cannot version-pin, and the released
// version at the time of writing (1.9.2) has no `send` subcommand at all. An
// operator who configured this route is entitled to be told at startup that
// it cannot work, not to discover it when the first worker blocks.
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

// probe checks that this tincan has a `send` subcommand.
//
// `tincan send` with no arguments exits non-zero either way, and writes to
// stderr either way, so neither the code nor the stream distinguishes them.
// The text does. Measured against the real binaries:
//
//	1.9.2, no subcommand:  exit 2,  "tincan: unrecognised argument 'send'"
//	with the subcommand:   exit 64, "tincan send: --to is required."
//
// The first is tincan's documented unknownArgText, so that is what is keyed
// on. An earlier version of this probe read stdout, where neither writes
// anything, and accepted a tincan that could not deliver.
func probe(run Runner, binary string) error {
	_, stderr, _, err := run(probeTimeout, binary, "send")
	if err != nil {
		return fmt.Errorf("cannot run %q: %w — install tincan, or name it with the %q setting",
			binary, err, settingBinary)
	}
	if strings.Contains(string(stderr), "unrecognised argument") {
		return fmt.Errorf(
			"the installed %q has no `send` subcommand, so birddog cannot deliver through it; "+
				"upgrade tincan, or use the claude-inbox connector for a Claude Code orchestrator",
			binary)
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
