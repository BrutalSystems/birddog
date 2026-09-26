// internal/observe/transcript/watch_test.go
package transcript

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/BrutalSystems/birddog/internal/config"
	"github.com/BrutalSystems/birddog/internal/monitor"
	"github.com/BrutalSystems/birddog/internal/policy"
)

type stub struct {
	s   monitor.Sighting
	err error
}

func (s stub) Observe(config.Target) (monitor.Sighting, error) { return s.s, s.err }

func idleSighting() monitor.Sighting {
	return monitor.Sighting{Observation: policy.Observation{
		Live: true, Status: policy.StatusIdle, StatusKnown: true,
		InputRequestVisibility: policy.VisibilityNotObserved,
	}}
}

func target(sessionID string, on bool) config.Target {
	return config.Target{
		ID: "w1", Provider: "claude",
		Attachment:   config.Attachment{SessionID: sessionID},
		Observations: config.Observations{Transcript: on},
	}
}

// projectsWith writes one transcript and returns the projects dir.
func projectsWith(t *testing.T, sessionID string, lines ...string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "-proj", sessionID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

const questionLine = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Which one do you want?"}]}}`
const statementLine = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"All done."}]}}`

func TestWatcherIsAPassThroughWhenTheOptionIsOff(t *testing.T) {
	dir := projectsWith(t, "s1", questionLine)
	w := Watch(stub{s: idleSighting()}, []string{dir})

	got, err := w.Observe(target("s1", false))
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if got.Status != policy.StatusIdle || got.Evidence != "" {
		t.Errorf("sighting was modified with the option off: %+v", got.Observation)
	}
}

func TestWatcherReportsAnInferredQuestion(t *testing.T) {
	dir := projectsWith(t, "s1", questionLine)
	w := Watch(stub{s: idleSighting()}, []string{dir})

	got, err := w.Observe(target("s1", true))
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if got.Status != policy.StatusWaitingInput || !got.StatusKnown {
		t.Errorf("Status = %q, want waiting_input", got.Status)
	}
	if got.InputRequestKind != policy.RequestKindQuestionInferred {
		t.Errorf("Kind = %q", got.InputRequestKind)
	}
	if got.InputRequestDetail != "Which one do you want?" {
		t.Errorf("Detail = %q", got.InputRequestDetail)
	}
	if got.Evidence != policy.EvidenceTranscript {
		t.Errorf("Evidence = %q, want transcript", got.Evidence)
	}
	if got.InputRequestVisibility != policy.VisibilityObserved {
		t.Errorf("Visibility = %q, want observed", got.InputRequestVisibility)
	}
}

func TestWatcherReportsNotObservedWhenThereIsNoQuestion(t *testing.T) {
	dir := projectsWith(t, "s1", statementLine)
	w := Watch(stub{s: idleSighting()}, []string{dir})

	got, _ := w.Observe(target("s1", true))
	if got.Status != policy.StatusIdle {
		t.Errorf("Status = %q, want idle unchanged", got.Status)
	}
	if got.InputRequestVisibility != policy.VisibilityNotObserved {
		t.Errorf("Visibility = %q, want not_observed", got.InputRequestVisibility)
	}
}

func TestWatcherReportsUnavailableWithNoTranscript(t *testing.T) {
	dir := projectsWith(t, "other", statementLine)
	w := Watch(stub{s: idleSighting()}, []string{dir})

	got, _ := w.Observe(target("s1", true))
	if got.InputRequestVisibility != policy.VisibilityUnavailable {
		t.Errorf("Visibility = %q, want unavailable", got.InputRequestVisibility)
	}
	// Reason stays empty: it says why the observation is not a current,
	// readable answer, and it is one. Only the enrichment failed, which
	// InputRequestVisibility already reports.
	if got.Reason != "" {
		t.Errorf("Reason = %q, want empty", got.Reason)
	}
}

// Enrichment must never revive a session the inner adapter reported gone.
func TestWatcherLeavesADeadSessionAlone(t *testing.T) {
	dir := projectsWith(t, "s1", questionLine)
	dead := monitor.Sighting{Observation: policy.Observation{
		Live: false, Status: policy.StatusUnknown,
		InputRequestVisibility: policy.VisibilityUnavailable,
	}}
	w := Watch(stub{s: dead}, []string{dir})

	got, _ := w.Observe(target("s1", true))
	if got.Live || got.Status != policy.StatusUnknown {
		t.Errorf("a dead session was revived: %+v", got.Observation)
	}
	if got.InputRequestKind != "" {
		t.Errorf("Kind = %q, want empty", got.InputRequestKind)
	}
}

func TestWatcherPassesThroughAnInnerError(t *testing.T) {
	dir := projectsWith(t, "s1", questionLine)
	boom := errors.New("inner failed")
	w := Watch(stub{s: idleSighting(), err: boom}, []string{dir})

	if _, err := w.Observe(target("s1", true)); !errors.Is(err, boom) {
		t.Errorf("err = %v, want the inner error", err)
	}
}

// A permission prompt is stronger evidence than a heuristic; the decorator
// must not overwrite it.
func TestWatcherDoesNotOverwriteAnExistingRequest(t *testing.T) {
	dir := projectsWith(t, "s1", questionLine)
	waiting := monitor.Sighting{Observation: policy.Observation{
		Live: true, Status: policy.StatusWaitingInput, StatusKnown: true,
		InputRequestVisibility: policy.VisibilityObserved,
		InputRequestKind:       "permission prompt",
		Evidence:               policy.EvidenceRegistry,
	}}
	w := Watch(stub{s: waiting}, []string{dir})

	got, _ := w.Observe(target("s1", true))
	if got.InputRequestKind != "permission prompt" || got.Evidence != policy.EvidenceRegistry {
		t.Errorf("the registry's answer was overwritten: %+v", got.Observation)
	}
}
