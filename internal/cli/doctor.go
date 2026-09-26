package cli

import (
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/BrutalSystems/birddog/internal/instrument"
	"github.com/BrutalSystems/birddog/internal/notify"
	"github.com/BrutalSystems/birddog/internal/policy"
	"github.com/BrutalSystems/birddog/internal/provider"
	"text/tabwriter"
)

// Capability is what birddog can observe of one provider, and what it cannot.
//
// The gaps are the point. An adapter that quietly degraded into a guess would
// be worse than one that says it cannot see something.
type Capability struct {
	Provider string `json:"provider"`

	// Command and Version identify what is actually installed; capabilities
	// are not claimed beyond the version they were established against.
	Command   string `json:"command"`
	Version   string `json:"version,omitempty"`
	Installed bool   `json:"installed"`

	// Each capability is stated twice: a sentence for an operator reading a
	// terminal, and a support value for a program, which cannot branch on
	// prose and must not be asked to. The two are the same claim and are
	// kept in agreement by doctor's tests.
	AttachToRunningSession string `json:"attach_to_running_session"`
	RuntimeState           string `json:"runtime_state"`
	ToolEvents             string `json:"tool_events"`
	InputRequests          string `json:"input_requests"`
	Notes                  string `json:"notes,omitempty"`

	// Support values from internal/policy. The discriminator they exist for
	// is provider_limited against requires_setup: one says asking again will
	// never help, the other says an operator could make it true.
	AttachToRunningSessionSupport string `json:"attach_to_running_session_support"`
	RuntimeStateSupport           string `json:"runtime_state_support"`
	ToolEventsSupport             string `json:"tool_events_support"`
	InputRequestsSupport          string `json:"input_requests_support"`
}

// Instrumentation is the state of one piece of birddog's own observation
// machinery on this machine.
//
// A capability's support value says what could be observed once the setup is
// in place. This says whether it is. `requires_setup` reads identically on a
// machine where hooks are installed and one where they are not, and an
// operator could always tell those apart by running `hooks status` — this is
// the same answer, for a program.
type Instrumentation struct {
	// Mechanism names the thing and Provider the runtime it instruments, so a
	// consumer can join this to the capability it resolves.
	Mechanism string `json:"mechanism"`
	Provider  string `json:"provider"`

	// State is one of the policy.Instrument* values, and Detail is the same
	// claim as a sentence. Both, for the same reason a capability carries
	// both: a program cannot branch on prose and an operator should not have
	// to read an enum.
	State  string `json:"state"`
	Detail string `json:"detail"`

	// Version is the version last seen running, not what is installed on
	// disk — the two differ on a machine where the instrumentation has not
	// run since it was upgraded.
	Version string `json:"version,omitempty"`

	// Problem is set only for something installed that cannot work. Absence
	// is never a fault, so not_installed and unknown never carry one.
	Problem string `json:"problem,omitempty"`

	// Events and Missing are the lifecycle events a hook install covers and
	// does not, for a consumer that needs to know which coverage is absent.
	Events  []string `json:"events,omitempty"`
	Missing []string `json:"missing,omitempty"`

	// PublishingSessions counts sessions currently publishing. Zero usually
	// means nothing is running rather than anything being wrong.
	PublishingSessions int `json:"publishing_sessions,omitempty"`
}

// DoctorResult is the whole report.
type DoctorResult struct {
	Platform     string       `json:"platform"`
	Capabilities []Capability `json:"capabilities"`

	// Instrumentation resolves the capabilities that say requires_setup:
	// whether birddog's own machinery for them is in place here.
	Instrumentation []Instrumentation `json:"instrumentation"`

	// NotificationKinds are the alert routes this build can be configured
	// with. "none" always works and means polling the event feed.
	NotificationKinds []string `json:"notification_kinds"`

	// Limitations are things no adapter can do on this platform today, stated
	// once rather than repeated per provider.
	Limitations []string `json:"limitations"`

	// HookHandlerProblem reports birddog's own installation being broken —
	// a registered hook handler that cannot run. Empty when there is nothing
	// wrong, including when no hooks are installed at all.
	//
	// doctor reports what cannot be observed. A handler that fires and fails
	// on every tool call is exactly that, and it is the one cause on this
	// list that is birddog's own fault rather than a provider's limit.
	HookHandlerProblem string `json:"hook_handler_problem,omitempty"`
}

// capabilities is what doctor reports, built from the provider registry so a
// provider cannot exist without being described.
//
// A function rather than a variable: the registry is populated by package
// init across several packages, and a package-level variable here would be
// evaluated before some of them have run.
//
// The prose lives beside each adapter, in the init that registers it. See
// internal/provider.
func capabilities() []Capability {
	var out []Capability
	for _, s := range provider.Specs() {
		if s.Internal {
			continue
		}
		out = append(out, Capability{
			Provider: s.Name, Command: s.Command,
			AttachToRunningSession: s.AttachToRunningSession,
			RuntimeState:           s.RuntimeState,
			ToolEvents:             s.ToolEvents,
			InputRequests:          s.InputRequests,
			Notes:                  s.Notes,

			AttachToRunningSessionSupport: s.AttachToRunningSessionSupport,
			RuntimeStateSupport:           s.RuntimeStateSupport,
			ToolEventsSupport:             s.ToolEventsSupport,
			InputRequestsSupport:          s.InputRequestsSupport,
		})
	}
	return out
}

var platformLimitations = []string{
	"Desktop applications are out of scope in this version; no adapter observes them.",
	"Permission requests are visible on an instrumented opencode session, and on a Claude Code session with no setup at all — its registry reports a waiting session, and hooks add which tool. Codex exposes no such signal. Where visibility is unavailable it means birddog cannot look — never that a worker is unblocked.",
	"A Claude Code session blocked on an AskUserQuestion menu is reported as waiting with no setup: the registry says so. A question asked in plain prose is not — the registry reports it as idle, and only a target opted into transcript reading (`observations.transcript`) reports it. Without that, `not_observed` means looked for and not seen, never that there is none.",
	"A question read from a transcript reports `evidence: transcript`. `input_request_kind: question` is exact — an unanswered AskUserQuestion call. `question_inferred` is a heuristic read from a trailing question mark and can be wrong.",
	"A Claude Code session sitting at the folder-trust prompt has no registry record at all, so it cannot be observed rather than being reported as waiting.",
	"Hooks reach sessions already running, observed on Claude Code 2.1.267 — but that is not a documented guarantee, and a new session is the sure way.",
	"An opencode session started by hand, without the plugin, cannot be observed at all.",
	"A turn ending is not work finishing, and birddog never reports it as such.",
}

// instrumentationReports inspects birddog's own installation.
//
// Reads only. An unreadable or absent settings file is not a fault: it means
// no hooks are installed, which is a legitimate way to run birddog and is
// reported as a choice rather than an error.
func instrumentationReports() []instrument.Report {
	var settings map[string]any
	if path, err := defaultSettingsPath(); err == nil {
		if read, err := readSettings(path); err == nil {
			settings = read
		}
	}

	records := opencodeRecordsDir()
	return []instrument.Report{
		instrument.Hooks(settings),
		instrument.Opencode{
			RecordsDir: records,
			LogPath:    opencodePluginLog(records),
			Now:        time.Now,
		}.Report(),
	}
}

// doctorResult assembles the report from what was found.
//
// Kept free of IO so the relationship between the fields can be tested: in
// particular HookHandlerProblem is derived from the hooks mechanism rather
// than computed alongside it, which is what stops the two disagreeing.
func doctorResult(reports []instrument.Report) DoctorResult {
	result := DoctorResult{
		Platform:          "darwin",
		Limitations:       platformLimitations,
		NotificationKinds: notify.Kinds(),
	}

	for _, r := range reports {
		result.Instrumentation = append(result.Instrumentation, Instrumentation{
			Mechanism:          r.Mechanism,
			Provider:           r.Provider,
			State:              r.State,
			Detail:             r.Detail,
			Version:            r.Version,
			Problem:            r.Problem,
			Events:             r.Events,
			Missing:            r.Missing,
			PublishingSessions: r.PublishingSessions,
		})

		// The pre-existing field, kept to its documented meaning: birddog's
		// own hook handler being broken, and empty for everything else
		// including no hooks at all.
		if r.Mechanism == "claude_hooks" && r.State == policy.InstrumentBroken {
			result.HookHandlerProblem = r.Problem
		}
	}

	for _, c := range capabilities() {
		// Reading a version is a read. Nothing here starts, resumes or
		// otherwise disturbs a session.
		c.Version, c.Installed = probeVersion(c.Command)
		result.Capabilities = append(result.Capabilities, c)
	}

	return result
}

func runDoctor(args []string, stdout io.Writer) error {
	fs := flags("doctor")
	asJSON := fs.Bool("json", false, "emit JSON for programmatic use")
	if err := parse(fs, args); err != nil {
		return err
	}

	result := doctorResult(instrumentationReports())

	return emit(stdout, *asJSON, result, func(w io.Writer) error {
		return renderDoctor(w, result)
	})
}

// renderDoctor writes the operator's report.
//
// The structured values are for programs. Everything a person needs stays a
// sentence, because dropping the prose to make room for an enum would be a
// loss rather than a trade.
func renderDoctor(w io.Writer, result DoctorResult) error {
	// First, because it describes birddog being broken rather than a
	// provider being limited, and everything below it assumes the hooks
	// work.
	if result.HookHandlerProblem != "" {
		fmt.Fprintf(w, "birddog's own hook handler cannot run.\n  %s\n\n", result.HookHandlerProblem)
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PROVIDER\tINSTALLED\tATTACH\tRUNTIME STATE")
	for _, c := range result.Capabilities {
		installed := "no"
		if c.Installed {
			installed = c.Version
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", c.Provider, installed,
			firstWord(c.AttachToRunningSession), firstWord(c.RuntimeState))
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	fmt.Fprintf(w, "\nAlert routes: %s\n", strings.Join(result.NotificationKinds, ", "))
	fmt.Fprintln(w, "  \"none\" means alerts are polled from the event feed, which always works.")

	fmt.Fprintln(w, "\nWhat cannot be observed:")
	for _, l := range result.Limitations {
		fmt.Fprintf(w, "  - %s\n", l)
	}

	// Between the platform limits and the per-provider detail on purpose:
	// what cannot be observed anywhere, then what this machine has done about
	// what can be, then the providers themselves.
	fmt.Fprintln(w, "\nbirddog's own instrumentation on this machine:")
	for _, in := range result.Instrumentation {
		fmt.Fprintf(w, "  %s (%s): %s\n", in.Mechanism, in.Provider, in.State)
		fmt.Fprintf(w, "    %s\n", in.Detail)
		if len(in.Missing) > 0 {
			fmt.Fprintf(w, "    missing: %s\n", strings.Join(in.Missing, ", "))
		}
	}

	fmt.Fprintln(w, "\nPer-provider detail:")
	for _, c := range result.Capabilities {
		fmt.Fprintf(w, "\n  %s\n", c.Provider)
		fmt.Fprintf(w, "    attach:         %s\n", c.AttachToRunningSession)
		fmt.Fprintf(w, "    runtime state:  %s\n", c.RuntimeState)
		fmt.Fprintf(w, "    tool events:    %s\n", c.ToolEvents)
		fmt.Fprintf(w, "    input requests: %s\n", c.InputRequests)
		if c.Notes != "" {
			fmt.Fprintf(w, "    note:           %s\n", c.Notes)
		}
	}
	return nil
}

// firstWord keeps the summary table narrow without inventing a shorter answer.
func firstWord(s string) string {
	if i := strings.IndexAny(s, " —"); i > 0 {
		return s[:i]
	}
	return s
}

// probeVersion asks a CLI its version, which is a read and nothing more.
func probeVersion(command string) (string, bool) {
	path, err := exec.LookPath(command)
	if err != nil {
		return "", false
	}
	out, err := exec.Command(path, "--version").Output()
	if err != nil {
		return "", true // installed, but would not say
	}
	return strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0]), true
}
