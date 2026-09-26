package cli

import (
	"strings"
	"testing"

	"github.com/BrutalSystems/birddog/internal/instrument"
	"github.com/BrutalSystems/birddog/internal/policy"
)

// doctor's whole job is stating capabilities accurately, so its prose is a
// claim like any other and drifts the same way. It said permission requests
// needed `birddog hooks install` for two releases after the registry began
// reporting a waiting session without them — understating what birddog could
// do, in the one command an operator reads to find out.
//
// These assert the shape of the claim rather than its wording, so it can be
// rephrased but not silently reverted.
func TestDoctorDoesNotClaimClaudeNeedsHooksForInputRequests(t *testing.T) {
	if len(capabilities()) == 0 {
		// capabilities() reads a registry populated by package init. An
		// empty one would make every loop below pass vacuously, which is
		// how a reverted capability claim would go quiet instead of red.
		t.Fatal("capabilities() is empty — internal/observe is not linked into this test binary")
	}

	for _, c := range capabilities() {
		if c.Provider != "claude" {
			continue
		}
		if contains(c.InputRequests, "with hooks installed") {
			t.Errorf("claude input requests = %q, but the registry reports a waiting session with no hooks", c.InputRequests)
		}
		if !contains(c.InputRequests, "no hooks") {
			t.Errorf("claude input requests = %q, want it to say hooks are not needed", c.InputRequests)
		}
	}
}

// opencode is no longer the only provider that can see a request, and saying
// so would understate Claude Code rather than overstate opencode — the same
// drift in the other direction.
func TestDoctorDoesNotClaimOpencodeIsTheOnlyProviderThatSeesRequests(t *testing.T) {
	for _, c := range capabilities() {
		if c.Provider == "opencode" && contains(c.InputRequests, "only provider") {
			t.Errorf("opencode input requests = %q, but Claude Code reports waiting sessions too", c.InputRequests)
		}
	}
}

// The limits that came with that capability are real and must keep being
// stated. The first was wrong until 2026-09-26 and is now stated precisely:
// an AskUserQuestion menu IS reported as waiting by the registry, and a
// question asked in plain prose is not. Asserting the distinction, rather
// than the phrase "natural-language", is what stops the old over-broad claim
// coming back.
func TestDoctorStatesTheLimitsOfWhatWaitingCovers(t *testing.T) {
	var menuSeen, proseNotSeen, transcriptOptIn, trustPrompt bool
	for _, l := range platformLimitations {
		if contains(l, "AskUserQuestion menu") && contains(l, "reported as waiting") {
			menuSeen = true
		}
		if contains(l, "plain prose") {
			proseNotSeen = true
		}
		if contains(l, "observations.transcript") {
			transcriptOptIn = true
		}
		if contains(l, "folder-trust") {
			trustPrompt = true
		}
	}
	if !menuSeen {
		t.Error("doctor does not say an AskUserQuestion menu is reported as waiting with no setup")
	}
	if !proseNotSeen {
		t.Error("doctor does not say a question asked in plain prose is the case the registry misses")
	}
	if !transcriptOptIn {
		t.Error("doctor does not name observations.transcript as what reports the prose case")
	}
	if !trustPrompt {
		t.Error("doctor does not say a session at the folder-trust prompt has no record at all")
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// The structured support values are the machine-readable half of the same
// claim the prose makes. They exist so a consumer can tell a permanent
// provider limit from something an operator could fix, which is the one
// distinction the sentence conveys only to a human.
func TestEveryCapabilityCarriesASupportValue(t *testing.T) {
	valid := map[string]bool{
		policy.SupportSupported:       true,
		policy.SupportRequiresSetup:   true,
		policy.SupportPartial:         true,
		policy.SupportProviderLimited: true,
	}
	for _, c := range capabilities() {
		for _, f := range supportFields(c) {
			if !valid[f.value] {
				t.Errorf("%s %s support = %q, want one of the four support values",
					c.Provider, f.name, f.value)
			}
		}
	}
}

// A parallel field can drift from the sentence beside it, and a support value
// that disagrees with its prose is worse than no support value at all: the
// consumer and the operator would be told different things.
func TestSupportValuesAgreeWithTheProse(t *testing.T) {
	for _, c := range capabilities() {
		for _, f := range supportFields(c) {
			switch f.value {
			case policy.SupportProviderLimited:
				if !contains(f.prose, "unavailable") {
					t.Errorf("%s %s is provider_limited but its prose %q does not say unavailable",
						c.Provider, f.name, f.prose)
				}
			case policy.SupportPartial:
				if !contains(f.prose, "partial") {
					t.Errorf("%s %s is partial but its prose %q does not say so",
						c.Provider, f.name, f.prose)
				}
			case policy.SupportSupported, policy.SupportRequiresSetup:
				if contains(f.prose, "unavailable") {
					t.Errorf("%s %s = %q claims support but its prose %q says unavailable",
						c.Provider, f.name, f.value, f.prose)
				}
			}
		}
	}
}

// The discriminator this is all for. Codex cannot report these to an outside
// observer at any version, so a consumer that asks again learns nothing —
// which is exactly what it must be able to tell apart from a broken install.
func TestCodexUnavailableCapabilitiesAreProviderLimits(t *testing.T) {
	for _, c := range capabilities() {
		if c.Provider != "codex" {
			continue
		}
		if c.ToolEventsSupport != policy.SupportProviderLimited {
			t.Errorf("codex tool events support = %q, want provider_limited", c.ToolEventsSupport)
		}
		if c.InputRequestsSupport != policy.SupportProviderLimited {
			t.Errorf("codex input requests support = %q, want provider_limited", c.InputRequestsSupport)
		}
	}
}

// The other half of the discriminator: these are not limits. birddog's own
// instrumentation carries them, and an operator who installs it gets the
// capability — so reporting them as merely supported would hide the one
// action that makes them true.
func TestInstrumentedCapabilitiesRequireSetup(t *testing.T) {
	for _, c := range capabilities() {
		switch c.Provider {
		case "claude":
			if c.ToolEventsSupport != policy.SupportRequiresSetup {
				t.Errorf("claude tool events support = %q, want requires_setup: hooks carry them", c.ToolEventsSupport)
			}
		case "opencode":
			if c.RuntimeStateSupport != policy.SupportRequiresSetup {
				t.Errorf("opencode runtime state support = %q, want requires_setup: the plugin carries it", c.RuntimeStateSupport)
			}
		}
	}
}

// The prose is the operator's answer and keeps its field and its meaning. A
// support value that arrived by replacing a sentence would be a change to
// what a consumer relies on rather than an addition beside it.
func TestProseCapabilityFieldsSurvive(t *testing.T) {
	for _, c := range capabilities() {
		for _, f := range supportFields(c) {
			if f.prose == "" {
				t.Errorf("%s %s has no prose; the sentence is the operator's answer and must stay", c.Provider, f.name)
			}
		}
	}
}

type supportField struct{ name, prose, value string }

func supportFields(c Capability) []supportField {
	return []supportField{
		{"attach", c.AttachToRunningSession, c.AttachToRunningSessionSupport},
		{"runtime state", c.RuntimeState, c.RuntimeStateSupport},
		{"tool events", c.ToolEvents, c.ToolEventsSupport},
		{"input requests", c.InputRequests, c.InputRequestsSupport},
	}
}

// ---- instrumentation ----

// The join this exists for. A capability that says requires_setup is only
// half an answer: a consumer still has to find out whether the setup is in
// place here, and it can only do that if doctor reports the mechanism that
// would carry it, for the same provider.
func TestEveryCapabilityThatRequiresSetupHasAMechanismReported(t *testing.T) {
	reported := map[string]bool{}
	for _, r := range instrumentationReports() {
		reported[r.Provider] = true
	}

	for _, c := range capabilities() {
		for _, f := range supportFields(c) {
			if f.value != policy.SupportRequiresSetup {
				continue
			}
			if !reported[c.Provider] {
				t.Errorf("%s %s is requires_setup but no instrumentation is reported for %s, "+
					"so a consumer cannot tell whether the setup is in place",
					c.Provider, f.name, c.Provider)
			}
		}
	}
}

// hook_handler_problem keeps its name and meaning, so it must keep agreeing
// with the mechanism that now reports the same fact in a form a program can
// branch on. Deriving one from the other is what makes them unable to drift.
func TestHookHandlerProblemIsDerivedFromTheHooksInstrumentation(t *testing.T) {
	broken := instrument.Report{
		Mechanism: "claude_hooks", Provider: "claude",
		State:   policy.InstrumentBroken,
		Problem: "/nowhere/birddog is registered as the hook handler but is not there",
	}

	got := doctorResult([]instrument.Report{broken})

	if got.HookHandlerProblem != broken.Problem {
		t.Errorf("hook_handler_problem = %q, want it to match the mechanism's problem %q",
			got.HookHandlerProblem, broken.Problem)
	}
}

// The documented meaning of the old field: empty when the handler is runnable
// AND empty when none is installed, because an absent installation is a
// choice. The new value is what tells those apart, so the old one must not
// start reporting absence as a fault.
func TestHookHandlerProblemStaysEmptyWhenHooksAreSimplyAbsent(t *testing.T) {
	absent := instrument.Report{
		Mechanism: "claude_hooks", Provider: "claude",
		State: policy.InstrumentNotInstalled,
	}

	got := doctorResult([]instrument.Report{absent})

	if got.HookHandlerProblem != "" {
		t.Errorf("hook_handler_problem = %q, want empty: not installing is not a fault", got.HookHandlerProblem)
	}
	if len(got.Instrumentation) != 1 || got.Instrumentation[0].State != policy.InstrumentNotInstalled {
		t.Errorf("instrumentation = %+v, want the absence reported as not_installed", got.Instrumentation)
	}
}

// Every instrumentation value doctor emits has to be one a consumer can
// branch on, for the same reason the support values do.
func TestInstrumentationStatesAreFromTheVocabulary(t *testing.T) {
	valid := map[string]bool{
		policy.InstrumentNotInstalled: true,
		policy.InstrumentInstalled:    true,
		policy.InstrumentIncomplete:   true,
		policy.InstrumentBroken:       true,
		policy.InstrumentUnknown:      true,
	}
	for _, r := range instrumentationReports() {
		if !valid[r.State] {
			t.Errorf("%s state = %q, want one of the five instrumentation values", r.Mechanism, r.State)
		}
		if r.Detail == "" {
			t.Errorf("%s has no detail sentence; the prose is the operator's answer", r.Mechanism)
		}
	}
}

// The operator half. A structured value that only a program can read would
// leave the person running doctor worse off than before, and `hooks status`
// is a different command they have no reason to know to run.
func TestDoctorTextReportsWhatIsInstalledHere(t *testing.T) {
	var out strings.Builder
	err := renderDoctor(&out, doctorResult([]instrument.Report{{
		Mechanism: "claude_hooks", Provider: "claude",
		State:   policy.InstrumentBroken,
		Detail:  "hooks are registered but the handler cannot run",
		Problem: "/nowhere/birddog is registered as the hook handler but is not there",
	}}))
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	got := out.String()
	if !contains(got, "hooks are registered but the handler cannot run") {
		t.Errorf("doctor text does not state what is installed here:\n%s", got)
	}
	if !contains(got, "claude_hooks") {
		t.Errorf("doctor text does not name the mechanism:\n%s", got)
	}
}
