package instrument

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BrutalSystems/birddog/internal/hooks"
	"github.com/BrutalSystems/birddog/internal/policy"
)

// installedSettings is a settings document with birddog's hooks in it, built
// by the real installer rather than by hand so these tests cannot drift from
// what `birddog hooks install` actually writes.
func installedSettings(t *testing.T, binary string) map[string]any {
	t.Helper()
	settings := map[string]any{}
	if _, err := hooks.Install(settings, binary); err != nil {
		t.Fatalf("install hooks: %v", err)
	}
	return settings
}

// runnableBinary is a stand-in for the birddog binary a handler entry names.
func runnableBinary(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "birddog")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	return path
}

// ---- claude hooks ----

// No hooks is a legitimate way to run birddog, so it must not be dressed up as
// a fault. This is the distinction hook_handler_problem alone could never
// carry: it is the empty string here and for a working install both.
func TestHooksReportsNotInstalledWithoutCallingItAFault(t *testing.T) {
	got := Hooks(map[string]any{})

	if got.State != policy.InstrumentNotInstalled {
		t.Errorf("state = %q, want %q", got.State, policy.InstrumentNotInstalled)
	}
	if got.Problem != "" {
		t.Errorf("problem = %q, want empty — choosing not to install is not a fault", got.Problem)
	}
}

func TestHooksReportsInstalledWhenEveryEventIsRegisteredAndTheHandlerRuns(t *testing.T) {
	binary := runnableBinary(t)
	got := Hooks(installedSettings(t, binary))

	if got.State != policy.InstrumentInstalled {
		t.Errorf("state = %q, want %q", got.State, policy.InstrumentInstalled)
	}
	if got.Problem != "" {
		t.Errorf("problem = %q, want empty", got.Problem)
	}
	if len(got.Missing) != 0 {
		t.Errorf("missing = %v, want none", got.Missing)
	}
	if len(got.Events) != len(hooks.InstalledEvents) {
		t.Errorf("events = %v, want all %d", got.Events, len(hooks.InstalledEvents))
	}
}

// A partial install is worse than none because it looks like coverage, so it
// gets its own value rather than being rounded to installed.
func TestHooksReportsIncompleteWhenSomeEventsAreMissing(t *testing.T) {
	binary := runnableBinary(t)
	settings := installedSettings(t, binary)
	delete(settings["hooks"].(map[string]any), "Stop")

	got := Hooks(settings)

	if got.State != policy.InstrumentIncomplete {
		t.Errorf("state = %q, want %q", got.State, policy.InstrumentIncomplete)
	}
	if len(got.Missing) == 0 {
		t.Error("missing is empty, want the events that are not registered")
	}
}

// Registered but unrunnable is the case that looks complete and works for
// nothing — every hook fires and fails.
func TestHooksReportsBrokenWhenTheRegisteredHandlerCannotRun(t *testing.T) {
	// Named birddog, because that is what the installer recognises as its own
	// handler — and a removed birddog binary is how this fails in practice.
	settings := installedSettings(t, filepath.Join(t.TempDir(), "birddog"))

	got := Hooks(settings)

	if got.State != policy.InstrumentBroken {
		t.Errorf("state = %q, want %q", got.State, policy.InstrumentBroken)
	}
	if got.Problem == "" {
		t.Error("problem is empty, want it to say the handler cannot run")
	}
}

// ---- opencode plugin ----

func opencodeAt(dir string) Opencode {
	return Opencode{
		RecordsDir: filepath.Join(dir, "opencode"),
		LogPath:    filepath.Join(dir, "opencode-plugin.log"),
		StaleAfter: 90 * time.Second,
		Now:        time.Now,
	}
}

func writeRecord(t *testing.T, o Opencode, sessionID, version string, updated time.Time) {
	t.Helper()
	if err := os.MkdirAll(o.RecordsDir, 0o700); err != nil {
		t.Fatalf("create records dir: %v", err)
	}
	rec := map[string]any{
		"session_id":     sessionID,
		"state":          "idle",
		"pid":            os.Getpid(),
		"updated_at":     updated.UTC().Format(time.RFC3339Nano),
		"plugin_version": version,
	}
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	path := filepath.Join(o.RecordsDir, sessionID+".json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write record: %v", err)
	}
}

func writeLog(t *testing.T, o Opencode, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(o.LogPath), 0o700); err != nil {
		t.Fatalf("create log dir: %v", err)
	}
	var body string
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(o.LogPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
}

// The one case this mechanism cannot resolve. Reporting not_installed here
// would turn an absence into evidence — the same mistake the opencode observer
// refuses to make when a record is missing.
func TestOpencodeReportsUnknownWhenNothingHasEverBeenSeen(t *testing.T) {
	got := opencodeAt(t.TempDir()).Report()

	if got.State != policy.InstrumentUnknown {
		t.Errorf("state = %q, want %q", got.State, policy.InstrumentUnknown)
	}
	if got.Problem != "" {
		t.Errorf("problem = %q, want empty — not knowing is not a fault", got.Problem)
	}
	if got.Detail == "" {
		t.Error("detail is empty, want both readings stated")
	}
}

func TestOpencodeReportsInstalledWhenASessionIsPublishing(t *testing.T) {
	o := opencodeAt(t.TempDir())
	writeLog(t, o, "[birddog] event=started version=0.3.6 pid=42 dir=/x")
	writeRecord(t, o, "ses_1", "0.3.6", time.Now())

	got := o.Report()

	if got.State != policy.InstrumentInstalled {
		t.Errorf("state = %q, want %q", got.State, policy.InstrumentInstalled)
	}
	if got.PublishingSessions != 1 {
		t.Errorf("publishing sessions = %d, want 1", got.PublishingSessions)
	}
	if got.Version != "0.3.6" {
		t.Errorf("version = %q, want 0.3.6", got.Version)
	}
}

// The plugin loaded, so it is installed. Nothing publishing only means no
// opencode session is running, which is not a fault and must not read as one.
func TestOpencodeReportsInstalledWhenItLoadedButNoSessionIsRunning(t *testing.T) {
	o := opencodeAt(t.TempDir())
	writeLog(t, o, "[birddog] event=started version=0.3.6 pid=42 dir=/x")

	got := o.Report()

	if got.State != policy.InstrumentInstalled {
		t.Errorf("state = %q, want %q", got.State, policy.InstrumentInstalled)
	}
	if got.PublishingSessions != 0 {
		t.Errorf("publishing sessions = %d, want 0", got.PublishingSessions)
	}
	if got.Problem != "" {
		t.Errorf("problem = %q, want empty — an idle machine is not a broken one", got.Problem)
	}
}

// Loaded and cannot write is the case records alone could never show: there is
// nothing to read precisely because publishing is failing.
func TestOpencodeReportsBrokenWhenPublishingFailed(t *testing.T) {
	o := opencodeAt(t.TempDir())
	writeLog(t, o,
		"[birddog] event=started version=0.3.6 pid=42 dir=/x",
		"[birddog] event=publish.failed detail=EACCES: permission denied",
	)

	got := o.Report()

	if got.State != policy.InstrumentBroken {
		t.Errorf("state = %q, want %q", got.State, policy.InstrumentBroken)
	}
	if got.Problem == "" {
		t.Error("problem is empty, want the failure the plugin recorded")
	}
}

// A later start means the plugin recovered — a failure earlier in the log is
// history, not the current state.
func TestOpencodeTreatsAFailureBeforeALaterStartAsHistory(t *testing.T) {
	o := opencodeAt(t.TempDir())
	writeLog(t, o,
		"[birddog] event=publish.failed detail=EACCES: permission denied",
		"[birddog] event=started version=0.3.6 pid=99 dir=/x",
	)

	got := o.Report()

	if got.State != policy.InstrumentInstalled {
		t.Errorf("state = %q, want %q — the plugin started again after the failure", got.State, policy.InstrumentInstalled)
	}
}

// A record left behind by a session that has gone is not something publishing.
func TestOpencodeDoesNotCountAStaleRecordAsPublishing(t *testing.T) {
	o := opencodeAt(t.TempDir())
	writeLog(t, o, "[birddog] event=started version=0.3.6 pid=42 dir=/x")
	writeRecord(t, o, "ses_old", "0.3.6", time.Now().Add(-10*time.Minute))

	got := o.Report()

	if got.PublishingSessions != 0 {
		t.Errorf("publishing sessions = %d, want 0 — the record is stale", got.PublishingSessions)
	}
}

// Records without a log still prove the plugin ran: the log is diagnostics and
// may have been removed, but a record could only have been written by it.
func TestOpencodeReportsInstalledFromRecordsAloneWhenTheLogIsGone(t *testing.T) {
	o := opencodeAt(t.TempDir())
	writeRecord(t, o, "ses_1", "0.3.6", time.Now())

	got := o.Report()

	if got.State != policy.InstrumentInstalled {
		t.Errorf("state = %q, want %q", got.State, policy.InstrumentInstalled)
	}
}

// ---- the rule that holds across both mechanisms ----

// not_installed and unknown are answers, not faults. A consumer branching on
// Problem must never be handed one for either.
func TestAbsenceNeverCarriesAProblem(t *testing.T) {
	reports := []Report{
		Hooks(map[string]any{}),
		opencodeAt(t.TempDir()).Report(),
	}
	for _, r := range reports {
		switch r.State {
		case policy.InstrumentNotInstalled, policy.InstrumentUnknown:
			if r.Problem != "" {
				t.Errorf("%s: state %q carries problem %q, want empty", r.Mechanism, r.State, r.Problem)
			}
		}
	}
}

// Every report names the mechanism and provider it is about, so a consumer can
// join it to the capability whose support value says requires_setup.
func TestEveryReportNamesItsMechanismAndProvider(t *testing.T) {
	binary := runnableBinary(t)
	reports := []Report{
		Hooks(installedSettings(t, binary)),
		opencodeAt(t.TempDir()).Report(),
	}
	for _, r := range reports {
		if r.Mechanism == "" {
			t.Error("a report has no mechanism")
		}
		if r.Provider == "" {
			t.Errorf("%s has no provider", r.Mechanism)
		}
		if r.Detail == "" {
			t.Errorf("%s has no detail sentence", r.Mechanism)
		}
	}
}

// A live record and the log can disagree, and the record is the one that is
// currently true: the log's last start may be weeks old and name a version
// since replaced. Reporting the older of the two would misdescribe what is
// running right now.
func TestOpencodePrefersAPublishingSessionsVersionOverTheLogs(t *testing.T) {
	o := opencodeAt(t.TempDir())
	writeLog(t, o, "[birddog] event=started version=0.1.1 pid=42 dir=/x")
	writeRecord(t, o, "ses_1", "0.3.6", time.Now())

	got := o.Report()

	if got.Version != "0.3.6" {
		t.Errorf("version = %q, want 0.3.6 from the publishing session, not 0.1.1 from the log", got.Version)
	}
}
