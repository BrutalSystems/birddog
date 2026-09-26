package observe

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/BrutalSystems/birddog/internal/config"
	"github.com/BrutalSystems/birddog/internal/discover/claude"
	"github.com/BrutalSystems/birddog/internal/hooks"
	"github.com/BrutalSystems/birddog/internal/policy"
)

var t0 = time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)

func claudeTarget(sessionID string) config.Target {
	return config.Target{
		ID: "worker-1", Provider: "claude",
		Attachment: config.Attachment{Kind: "existing-session", SessionID: sessionID},
	}
}

func listing(ss ...claude.Session) func() ([]claude.Session, error) {
	return func() ([]claude.Session, error) { return ss, nil }
}

func TestClaudeObserverMapsBusyToActive(t *testing.T) {
	o := &Claude{List: listing(claude.Session{
		SessionID: "s1", Status: "busy", Live: true, StatusUpdatedAt: t0, ProcStart: "start",
	})}

	got, err := o.Observe(claudeTarget("s1"))
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if got.Status != policy.StatusActive || !got.StatusKnown {
		t.Errorf("Status = %q known=%v, want active", got.Status, got.StatusKnown)
	}
	if !got.Live {
		t.Error("Live = false, want true")
	}
}

func TestClaudeObserverMapsIdle(t *testing.T) {
	o := &Claude{List: listing(claude.Session{
		SessionID: "s1", Status: "idle", Live: true, StatusUpdatedAt: t0,
	})}

	got, _ := o.Observe(claudeTarget("s1"))
	if got.Status != policy.StatusIdle || !got.StatusKnown {
		t.Errorf("Status = %q known=%v, want idle", got.Status, got.StatusKnown)
	}
}

// The harness's status vocabulary is only partly known. Anything unrecognised
// is reported as unreadable rather than guessed at. "waiting" was once in this
// list and left it by being observed rather than by being assumed; the rest
// stay until the same is done for them.
func TestClaudeObserverDoesNotGuessAtUnknownStatusValues(t *testing.T) {
	for _, status := range []string{"compacting", "requesting", "something-new"} {
		t.Run(status, func(t *testing.T) {
			o := &Claude{List: listing(claude.Session{
				SessionID: "s1", Status: status, Live: true, StatusUpdatedAt: t0,
			})}

			got, _ := o.Observe(claudeTarget("s1"))
			if got.StatusKnown {
				t.Errorf("StatusKnown = true for %q — that claims a meaning nobody verified", status)
			}
			if !got.Live {
				t.Error("Live = false — the session was reachable, only its status was unreadable")
			}
		})
	}
}

// Session identity must change when the process behind it does, so a restarted
// session is a new generation rather than a continuation.
func TestClaudeSessionIdentityIncludesTheProcessStart(t *testing.T) {
	first := &Claude{List: listing(claude.Session{
		SessionID: "s1", Status: "idle", Live: true, ProcStart: "Mon Sep 21 15:53:11 2026",
	})}
	second := &Claude{List: listing(claude.Session{
		SessionID: "s1", Status: "idle", Live: true, ProcStart: "Mon Sep 21 16:10:00 2026",
	})}

	a, _ := first.Observe(claudeTarget("s1"))
	b, _ := second.Observe(claudeTarget("s1"))
	if a.SessionIdentity == b.SessionIdentity {
		t.Error("a restarted session kept its identity — the replacement would inherit the old run's state")
	}
}

// A session present but not live: the registry record outlived its process.
func TestClaudeObserverReportsANotLiveSessionAsUnreachable(t *testing.T) {
	o := &Claude{List: listing(claude.Session{
		SessionID: "s1", Status: "idle", Live: false, StatusUpdatedAt: t0,
	})}

	got, _ := o.Observe(claudeTarget("s1"))
	if got.Live {
		t.Error("Live = true for a session whose socket did not answer")
	}
}

// Absence alone is not an exit. Without positive evidence the process ended,
// the honest report is that the session could not be observed.
func TestMissingSessionWithoutProcessEvidenceIsNotAnExit(t *testing.T) {
	o := &Claude{List: listing()}

	got, err := o.Observe(claudeTarget("s1"))
	if err == nil {
		t.Fatal("Observe = nil error for a session that is not there, want it reported")
	}
	if got.Status == policy.StatusExited {
		t.Error("a missing registry record was reported as an exit — nothing verified the process ended")
	}
}

// With a recorded process identity, absence can be verified: the process is
// gone, so the exit is a fact rather than an inference.
func TestMissingSessionWithADeadProcessIsAVerifiedExit(t *testing.T) {
	target := claudeTarget("s1")
	target.Attachment.PID = 4242
	target.Attachment.ProcStart = "Mon Sep 21 15:53:11 2026"

	o := &Claude{
		List:        listing(),
		SameProcess: func(int, string) bool { return false }, // the pid is not that process
	}

	got, _ := o.Observe(target)
	if got.Status != policy.StatusExited || !got.StatusKnown {
		t.Errorf("Status = %q known=%v, want a verified exit", got.Status, got.StatusKnown)
	}
	if got.Live {
		t.Error("Live = true for an exited session")
	}
}

// The process is alive but its registry record is gone. That is a gap in
// observation, not an exit — claiming otherwise would report a session as
// finished while it is still running.
func TestMissingRecordWithALiveProcessIsObservationLossNotExit(t *testing.T) {
	target := claudeTarget("s1")
	target.Attachment.PID = 4242
	target.Attachment.ProcStart = "Mon Sep 21 15:53:11 2026"

	o := &Claude{
		List:        listing(),
		SameProcess: func(int, string) bool { return true }, // still that process
	}

	got, _ := o.Observe(target)
	if got.Status == policy.StatusExited {
		t.Error("a live process with no registry record was reported as exited")
	}
	if got.Live {
		t.Error("Live = true though nothing about the session could be read")
	}
}

func TestClaudeObserverSurfacesARegistryFailure(t *testing.T) {
	o := &Claude{List: func() ([]claude.Session, error) { return nil, errors.New("permission denied") }}

	got, err := o.Observe(claudeTarget("s1"))
	if err == nil {
		t.Fatal("Observe = nil error when the registry could not be read")
	}
	if got.Live || got.StatusKnown {
		t.Error("an unreadable registry produced a confident observation")
	}
}

// Criterion 6: the registry's own timestamps describe the session, but only
// the status timestamp reflects session activity.
func TestClaudeActivityComesFromTheSessionsOwnTimestamp(t *testing.T) {
	o := &Claude{List: listing(claude.Session{
		SessionID: "s1", Status: "busy", Live: true, StatusUpdatedAt: t0,
	})}

	got, _ := o.Observe(claudeTarget("s1"))
	if !got.LastActivityAt.Equal(t0) {
		t.Errorf("LastActivityAt = %v, want the session's own status timestamp %v", got.LastActivityAt, t0)
	}
}

// With hooks installed, Claude Code stops being a provider birddog cannot
// see into. These tests are the difference between "unavailable" and a real
// answer.

func withHooks(sessions []claude.Session, state hooks.State) *Claude {
	return &Claude{
		List:      func() ([]claude.Session, error) { return sessions, nil },
		HookState: func(string) (hooks.State, error) { return state, nil },
	}
}

func liveSession() claude.Session {
	return claude.Session{SessionID: "s1", Status: "busy", Live: true, StatusUpdatedAt: t0, ProcStart: "start"}
}

func TestPendingPermissionMakesTheSessionWaitOnInput(t *testing.T) {
	o := withHooks([]claude.Session{liveSession()}, hooks.State{
		Present:            true,
		PendingPermissions: []hooks.Permission{{Tool: "Bash", ID: "t1", AskedAt: t0}},
	})

	got, _ := o.Observe(claudeTarget("s1"))
	if got.Status != policy.StatusWaitingInput || !got.StatusKnown {
		t.Errorf("Status = %q known=%v, want waiting_input", got.Status, got.StatusKnown)
	}
	if got.InputRequestVisibility != policy.VisibilityObserved {
		t.Errorf("InputRequestVisibility = %q, want observed", got.InputRequestVisibility)
	}
}

func TestRunningToolIsReportedFromHooks(t *testing.T) {
	o := withHooks([]claude.Session{liveSession()}, hooks.State{
		Present:      true,
		RunningTools: []hooks.Tool{{Name: "Bash", ID: "t1", StartedAt: t0}},
	})

	got, _ := o.Observe(claudeTarget("s1"))
	if got.Status != policy.StatusRunningTool || !got.StatusKnown {
		t.Errorf("Status = %q known=%v, want running_tool", got.Status, got.StatusKnown)
	}
}

// Waiting on a human outranks a tool: it is the condition an orchestrator
// most needs, and a tool running underneath does not make the session
// unblocked.
func TestWaitingOnInputOutranksARunningTool(t *testing.T) {
	o := withHooks([]claude.Session{liveSession()}, hooks.State{
		Present:            true,
		RunningTools:       []hooks.Tool{{Name: "Bash", ID: "t1", StartedAt: t0}},
		PendingPermissions: []hooks.Permission{{Tool: "Bash", ID: "t1", AskedAt: t0}},
	})

	got, _ := o.Observe(claudeTarget("s1"))
	if got.Status != policy.StatusWaitingInput {
		t.Errorf("Status = %q, want waiting_input", got.Status)
	}
}

// Hooks reporting nothing outstanding is a real observation — looked for and
// absent — which is what makes it different from having no hooks at all.
func TestHooksPresentWithNoRequestIsNotObserved(t *testing.T) {
	o := withHooks([]claude.Session{liveSession()}, hooks.State{Present: true})

	got, _ := o.Observe(claudeTarget("s1"))
	if got.InputRequestVisibility != policy.VisibilityNotObserved {
		t.Errorf("InputRequestVisibility = %q, want not_observed", got.InputRequestVisibility)
	}
}

// Criterion 14 is that an absence of observation must never read as an absence
// of requests. It used to be satisfied here by reporting unavailable, because
// without hooks birddog genuinely could not look.
//
// It can now: the registry reports a waiting session by itself. So a live
// session the registry does not report as waiting is one birddog looked at and
// saw no request for, and not_observed is the honest answer. The criterion is
// unchanged — what changed is that birddog acquired the ability to look.
func TestWithoutHooksTheRegistryStillAnswersForInputRequests(t *testing.T) {
	o := withHooks([]claude.Session{liveSession()}, hooks.State{Present: false})

	got, _ := o.Observe(claudeTarget("s1"))
	if got.InputRequestVisibility != policy.VisibilityNotObserved {
		t.Errorf("InputRequestVisibility = %q, want not_observed: the registry was readable", got.InputRequestVisibility)
	}
}

// And the enrichment being absent must not downgrade what the registry said.
func TestWithoutHooksAWaitingSessionIsStillReported(t *testing.T) {
	waiting := liveSession()
	waiting.Status = "waiting"
	waiting.WaitingFor = "permission prompt"
	o := withHooks([]claude.Session{waiting}, hooks.State{Present: false})

	got, _ := o.Observe(claudeTarget("s1"))
	if got.Status != policy.StatusWaitingInput || got.InputRequestVisibility != policy.VisibilityObserved {
		t.Errorf("status = %q visibility = %q, want a waiting session reported with no hooks installed",
			got.Status, got.InputRequestVisibility)
	}
}

// The registry still decides the base state; hooks only add what it cannot
// carry.
func TestHooksWithNothingOutstandingLeaveTheRegistryStatusAlone(t *testing.T) {
	o := withHooks([]claude.Session{liveSession()}, hooks.State{Present: true})

	got, _ := o.Observe(claudeTarget("s1"))
	if got.Status != policy.StatusActive {
		t.Errorf("Status = %q, want the registry's own busy/active", got.Status)
	}
}

// A hook state that cannot be read costs the enrichment, not the observation.
func TestUnreadableHookStateDoesNotLoseTheSession(t *testing.T) {
	o := &Claude{
		List:      listing(liveSession()),
		HookState: func(string) (hooks.State, error) { return hooks.State{}, errors.New("permission denied") },
	}

	got, err := o.Observe(claudeTarget("s1"))
	if err == nil {
		t.Error("Observe = nil error though the hook state could not be read")
	}
	if !got.Live || got.Status != policy.StatusActive {
		t.Errorf("observation = %+v, want the registry observation kept", got.Observation)
	}
	// The hooks were what failed. The registry was read successfully and had
	// its own answer, so the observation keeps it.
	if got.InputRequestVisibility != policy.VisibilityNotObserved {
		t.Errorf("InputRequestVisibility = %q, want the registry's answer kept", got.InputRequestVisibility)
	}
}

// A session that is not live gets no claims from hooks either: its markers
// are as stale as everything else about it.
func TestHooksDoNotReviveADeadSession(t *testing.T) {
	dead := liveSession()
	dead.Live = false
	o := withHooks([]claude.Session{dead}, hooks.State{
		Present:            true,
		PendingPermissions: []hooks.Permission{{Tool: "Bash", ID: "t1", AskedAt: t0}},
	})

	got, _ := o.Observe(claudeTarget("s1"))
	if got.Live {
		t.Error("Live = true for a session whose socket did not answer")
	}
	if got.StatusKnown {
		t.Error("StatusKnown = true for a session that cannot be reached")
	}
}

// Established by observation against Claude Code 2.1.267 on 2026-09-22: a
// session blocked on a permission prompt reports status "waiting" and carries
// a waitingFor field, and both clear when the prompt is answered. The observed
// transition was idle -> busy -> waiting, with waitingFor reading "permission
// prompt".
//
// So the registry answers this without hooks, for the provider birddog watches
// most. Claimed for that version and no further.
func TestClaudeReportsAWaitingSessionAsRequestingInput(t *testing.T) {
	o := &Claude{List: listing(claude.Session{
		SessionID: "s1", Status: "waiting", WaitingFor: "permission prompt",
		Live: true, StatusUpdatedAt: t0,
	})}

	got, err := o.Observe(claudeTarget("s1"))
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if got.Status != policy.StatusWaitingInput || !got.StatusKnown {
		t.Errorf("Status = %q known=%v, want waiting_input", got.Status, got.StatusKnown)
	}
	if got.InputRequestVisibility != policy.VisibilityObserved {
		t.Errorf("InputRequestVisibility = %q, want observed", got.InputRequestVisibility)
	}
	// "permission prompt" describes the kind of wait, not what is being asked
	// to act on, so it belongs in the kind rather than the subject.
	if got.InputRequestKind != "permission prompt" {
		t.Errorf("InputRequestKind = %q, want what the session said it waits on", got.InputRequestKind)
	}
}

// Having looked and seen no request is not the same as having no way to look.
// The registry does report a waiting session, so its silence is an
// observation — bounded by what the registry covers, which is what the alert
// text already says about not_observed.
func TestClaudeReportsNoRequestAsNotObservedRatherThanUnavailable(t *testing.T) {
	o := &Claude{List: listing(claude.Session{
		SessionID: "s1", Status: "busy", Live: true, StatusUpdatedAt: t0,
	})}

	got, _ := o.Observe(claudeTarget("s1"))
	if got.InputRequestVisibility != policy.VisibilityNotObserved {
		t.Errorf("InputRequestVisibility = %q, want not_observed", got.InputRequestVisibility)
	}
}

// A session that is not live has nothing current to say about a request,
// whatever its last status was.
func TestClaudeReportsVisibilityUnavailableWhenItCannotSeeTheSession(t *testing.T) {
	o := &Claude{List: listing(claude.Session{
		SessionID: "s1", Status: "waiting", WaitingFor: "permission prompt",
		Live: false, StatusUpdatedAt: t0,
	})}

	got, _ := o.Observe(claudeTarget("s1"))
	if got.InputRequestVisibility != policy.VisibilityUnavailable {
		t.Errorf("InputRequestVisibility = %q, want unavailable for a session that cannot be seen", got.InputRequestVisibility)
	}
}

// waitingFor is what the session said, and goes into the durable record, so
// the reader bounds it like any other observed subject.
func TestClaudeBoundsAnOverlongWaitingReason(t *testing.T) {
	o := &Claude{List: listing(claude.Session{
		SessionID: "s1", Status: "waiting", WaitingFor: strings.Repeat("x", maxDetail*2),
		Live: true, StatusUpdatedAt: t0,
	})}

	got, _ := o.Observe(claudeTarget("s1"))
	if n := len([]rune(got.InputRequestKind)); n > maxDetail+1 {
		t.Errorf("InputRequestKind = %d runes, want it bounded", n)
	}
}

func TestClaudeWithoutHooksReportsTransitionResolution(t *testing.T) {
	o := &Claude{List: listing(claude.Session{
		SessionID: "s1", Status: "busy", Live: true, StatusUpdatedAt: t0, ProcStart: "start",
	})}

	got, err := o.Observe(claudeTarget("s1"))
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if got.ActivityResolution != policy.ResolutionTransition {
		t.Errorf("ActivityResolution = %q, want transition: with no hook reporting this session, the registry's status timestamp is all there is",
			got.ActivityResolution)
	}
}

func TestClaudeWithHooksReportsActivityResolution(t *testing.T) {
	o := withHooks([]claude.Session{liveSession()}, hooks.State{
		Present:        true,
		LastActivityAt: t0.Add(30 * time.Second),
	})

	got, err := o.Observe(claudeTarget("s1"))
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if got.ActivityResolution != policy.ResolutionActivity {
		t.Errorf("ActivityResolution = %q, want activity", got.ActivityResolution)
	}
}

// Spec D-2. The field is laterOf(transition, newest marker), so on this pass the
// transition is the newer of the two — but hooks are watching, and tool work
// would have moved the marker. The resolution is a property of what is watching,
// not of which value happened to win, so it must not flip to transition here.
func TestClaudeResolutionStaysActivityWhenATransitionIsNewerThanTheMarker(t *testing.T) {
	session := claude.Session{
		SessionID: "s1", Status: "busy", Live: true,
		StatusUpdatedAt: t0.Add(time.Hour), ProcStart: "start",
	}
	o := withHooks([]claude.Session{session}, hooks.State{
		Present:        true,
		LastActivityAt: t0, // older than the transition
	})

	got, err := o.Observe(claudeTarget("s1"))
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !got.LastActivityAt.Equal(t0.Add(time.Hour)) {
		t.Errorf("LastActivityAt = %v, want the newer transition", got.LastActivityAt)
	}
	if got.ActivityResolution != policy.ResolutionActivity {
		t.Errorf("ActivityResolution = %q, want activity: hooks are watching, so staleness is still meaningful",
			got.ActivityResolution)
	}
}

func TestClaudeVerifiedExitReportsUnavailableResolution(t *testing.T) {
	target := claudeTarget("s1")
	target.Attachment.PID = 4242
	target.Attachment.ProcStart = "start"
	o := &Claude{
		List:        listing(),
		SameProcess: func(int, string) bool { return false },
	}

	got, err := o.Observe(target)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if got.Status != policy.StatusExited {
		t.Fatalf("Status = %q, want exited", got.Status)
	}
	if got.ActivityResolution != policy.ResolutionUnavailable {
		t.Errorf("ActivityResolution = %q, want unavailable: a verified exit carries no timestamp to qualify",
			got.ActivityResolution)
	}
}

func TestClaudeUnreadableSourceReportsUnavailableResolution(t *testing.T) {
	o := &Claude{List: func() ([]claude.Session, error) { return nil, errors.New("registry unreadable") }}

	got, _ := o.Observe(claudeTarget("s1"))
	if got.ActivityResolution != policy.ResolutionUnavailable {
		t.Errorf("ActivityResolution = %q, want unavailable", got.ActivityResolution)
	}
}

// An observation with no input request must carry no evidence: a consumer
// reading `evidence` is asking where a request came from, and answering when
// there is no request sources something that does not exist.
func TestNoEvidenceWithoutAnInputRequest(t *testing.T) {
	o := &Claude{List: listing(claude.Session{
		SessionID: "s1", Status: "idle", Live: true, StatusUpdatedAt: t0,
	})}

	got, _ := o.Observe(claudeTarget("s1"))
	if got.Evidence != "" {
		t.Errorf("Evidence = %q, want empty for an idle session", got.Evidence)
	}
}

func TestRegistryWaitingIsSourcedToTheRegistry(t *testing.T) {
	o := &Claude{List: listing(claude.Session{
		SessionID: "s1", Status: "waiting", Live: true, StatusUpdatedAt: t0,
	})}

	got, _ := o.Observe(claudeTarget("s1"))
	if got.Evidence != policy.EvidenceRegistry {
		t.Errorf("Evidence = %q, want registry", got.Evidence)
	}
}

// Finding 4: the registry says "waiting" and the hooks, which are looking,
// report no pending permission. Visibility is downgraded to not_observed but
// the registry's evidence stamp survives, sourcing a request that is no
// longer being reported.
func TestEvidenceIsClearedWhenHooksReportNoRequest(t *testing.T) {
	o := withHooks(
		[]claude.Session{{SessionID: "s1", Status: "waiting", Live: true, StatusUpdatedAt: t0, ProcStart: "start"}},
		hooks.State{Present: true},
	)

	got, _ := o.Observe(claudeTarget("s1"))
	if got.InputRequestVisibility == policy.VisibilityObserved {
		t.Fatalf("precondition: visibility is %q, expected a downgrade", got.InputRequestVisibility)
	}
	if got.Evidence != "" {
		t.Errorf("Evidence = %q with visibility %q; want empty", got.Evidence, got.InputRequestVisibility)
	}
}

func TestEvidenceIsClearedWhenHooksReportARunningTool(t *testing.T) {
	o := withHooks(
		[]claude.Session{{SessionID: "s1", Status: "waiting", Live: true, StatusUpdatedAt: t0, ProcStart: "start"}},
		hooks.State{Present: true, RunningTools: []hooks.Tool{{Name: "Bash", ID: "t1", StartedAt: t0}}},
	)

	got, _ := o.Observe(claudeTarget("s1"))
	if got.Status != policy.StatusRunningTool {
		t.Fatalf("precondition: status is %q, want running_tool", got.Status)
	}
	if got.Evidence != "" {
		t.Errorf("Evidence = %q on a session reported as working; want empty", got.Evidence)
	}
}
