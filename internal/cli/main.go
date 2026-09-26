package cli

import (
	"errors"
	"fmt"
	"io"

	"github.com/BrutalSystems/birddog/internal/ipc"
	"github.com/BrutalSystems/birddog/internal/version"
)

// ExitUsage and ExitFailure are the nonzero statuses birddog returns, so a
// script can tell a bad invocation from a failed operation.
const (
	ExitOK      = 0
	ExitFailure = 1
	ExitUsage   = 2
)

// Main runs one command and returns the process exit status.
func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return ExitUsage
	}

	var err error
	switch args[0] {
	case "discover":
		err = runDiscover(args[1:], stdout)
	case "start":
		err = runStart(args[1:], stdout)
	case "serve":
		err = runServe(args[1:], stderr)
	case "status":
		err = runStatus(args[1:], stdout)
	case "events":
		err = runEvents(args[1:], stdout)
	case "ack":
		err = runAck(args[1:], stdout)
	case "watch":
		err = runWatch(args[1:], stdout)
	case "hook":
		// The handler Claude Code runs. Always exits 0; see runHook.
		err = runHook(args[1:], stderr)
	case "hooks":
		err = runHooks(args[1:], stdout)
	case "stop":
		err = runStop(args[1:], stdout)
	case "resume":
		err = runResume(args[1:], stdout)
	case "list":
		err = runList(args[1:], stdout)
	case "prune":
		err = runPrune(args[1:], stdout)
	case "doctor":
		err = runDoctor(args[1:], stdout)
	case "version", "--version":
		fmt.Fprintf(stdout, "birddog %s\n", version.Version)
		return ExitOK
	case "-h", "--help", "help":
		usage(stdout)
		return ExitOK
	default:
		fmt.Fprintf(stderr, "birddog: unknown command %q\n\n", args[0])
		usage(stderr)
		return ExitUsage
	}

	if err != nil {
		if errors.Is(err, errUsage) {
			return ExitUsage
		}
		// A coded error keeps its code in the message, so a caller reading
		// stderr can still branch on it.
		var e *ipc.Error
		if errors.As(err, &e) {
			fmt.Fprintf(stderr, "birddog: [%s] %s\n", e.Code, e.Message)
		} else {
			fmt.Fprintf(stderr, "birddog: %v\n", err)
		}
		return ExitFailure
	}
	return ExitOK
}

var errUsage = errors.New("usage")

func usage(w io.Writer) {
	fmt.Fprint(w, `birddog — observe live coding-agent sessions on this machine

Usage:
  birddog discover [--json]
        List sessions that can be registered as targets.

  birddog start --config FILE [--json] [--foreground] [--owner SESSION]
        Start a monitoring instance. Returns once it is reachable.
        --owner ties it to a session: it stops when that session is gone.
        Without it the instance outlives everything until stopped.

  birddog status --instance ID [--json]
        Current state of every target, with the cursor to follow from.

  birddog events --instance ID [--after CURSOR] [--wait SECONDS] [--limit N] [--json]
        Observations since a cursor. --wait holds the request open.

  birddog ack --instance ID --incident N [--json]
        Record that you have seen an alert. Resolves nothing.

  birddog watch add    --instance ID --file TARGET.json
  birddog watch remove --instance ID --target ID
  birddog watch update --instance ID --target ID [--expect-quiet-for 20m --reason "..."]
        Adjust what an instance watches, without restarting it.

  birddog stop --instance ID [--json]
        Stop an instance. Every watched session keeps running.

  birddog resume --instance ID [--config FILE] [--json]
        Restart a stopped instance. Its event history continues.

  birddog list [--json]
        Monitoring instances on this machine, running and stopped.

  birddog prune [--dry-run] [--json]
        Forget instances that are over. Never stops a running one.

  birddog hooks install|status|uninstall [--json] [--dry-run]
        Let birddog see tool calls and permission requests in Claude Code.
        Edits ~/.claude/settings.json, preserving what is already there, and
        affects sessions started afterwards.

  birddog --version
        Print the version and exit.

  birddog doctor [--json]
        Adapter capabilities and what cannot be observed.

birddog only observes. It sends nothing to a watched session, approves nothing
on its behalf, and never starts, stops or restarts one. Stopping birddog leaves
every watched agent exactly as it was.
`)
}
