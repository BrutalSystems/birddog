package instrument

import (
	"fmt"

	"github.com/BrutalSystems/birddog/internal/hooks"
	"github.com/BrutalSystems/birddog/internal/policy"
)

// Hooks reports the state of birddog's Claude Code hook installation.
//
// Every fact here already existed — hooks.Status knows which events are
// registered, hooks.CheckHandler knows whether the handler can run. What was
// missing was somewhere a program could read them, which is why this does no
// detection of its own.
func Hooks(settings map[string]any) Report {
	r := Report{Mechanism: "claude_hooks", Provider: "claude"}

	st := hooks.Status(settings)
	r.Events, r.Missing = st.Events, st.Missing
	handler := hooks.CheckHandler(st.BinaryPath)

	switch {
	case !st.Installed && !st.Partial:
		r.State = policy.InstrumentNotInstalled
		r.Detail = "no hooks are installed, so tool calls and permission decisions " +
			"are not reported for Claude Code sessions. This is a choice, not a fault — " +
			"`birddog hooks install` changes it."

	// Before the partial check: a handler that cannot run makes the event
	// list describe coverage that does not exist, whether or not it is
	// complete. `hooks status` orders its output the same way.
	case handler.Problem != "":
		r.State = policy.InstrumentBroken
		r.Problem = handler.Problem
		r.Detail = "hooks are registered but the handler cannot run, so every hook fires and fails"

	case st.Partial:
		r.State = policy.InstrumentIncomplete
		r.Detail = fmt.Sprintf("hooks are installed for some events and not %d of them, "+
			"so coverage is incomplete in a way that looks complete — re-run `birddog hooks install`",
			len(st.Missing))

	default:
		r.State = policy.InstrumentInstalled
		r.Detail = "hooks are installed for every event birddog subscribes to, and the handler runs"
	}

	return r
}
