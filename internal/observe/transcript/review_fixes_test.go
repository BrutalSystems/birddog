package transcript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BrutalSystems/birddog/internal/policy"
)

// Finding 1: the monitor discards any sighting that arrives with an error, so
// returning one here throws away the inner observer's good answer and opens a
// false observation_lost on a session that is running fine.
func TestEnrichmentFailureDoesNotDestroyTheObservation(t *testing.T) {
	a := projects(t, "-p1/dup.jsonl")
	b := projects(t, "-p2/dup.jsonl")
	w := Watch(stub{s: idleSighting()}, []string{a, b}) // ambiguous: two matches

	got, err := w.Observe(target("dup", true))
	if err != nil {
		t.Errorf("Observe returned err = %v; the monitor discards the whole sighting when it does", err)
	}
	if !got.Live || got.Status != policy.StatusIdle {
		t.Errorf("inner observation lost: live=%v status=%q", got.Live, got.Status)
	}
	if got.InputRequestVisibility != policy.VisibilityUnavailable {
		t.Errorf("Visibility = %q, want unavailable", got.InputRequestVisibility)
	}
}

// Minor 3 re-graded: Reason says why the observation is not a current,
// readable answer. It is one — only the enrichment failed.
func TestNoTranscriptDoesNotStampAReasonOnAHealthyObservation(t *testing.T) {
	dir := projects(t, "-p/other.jsonl")
	w := Watch(stub{s: idleSighting()}, []string{dir})

	got, _ := w.Observe(target("s1", true))
	if got.Reason != "" {
		t.Errorf("Reason = %q on a live, readable session; want empty", got.Reason)
	}
	if got.InputRequestVisibility != policy.VisibilityUnavailable {
		t.Errorf("Visibility = %q, want unavailable", got.InputRequestVisibility)
	}
}

// Finding 2: records between the last message and EOF are unbounded — a real
// attachment record of 633KB exists. When the window holds no message record,
// birddog could not look; reporting not_observed claims it looked and saw none.
func TestWindowTooSmallToReachAMessageIsUnavailable(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "-p", "s1.jsonl")
	os.MkdirAll(filepath.Dir(p), 0o755)
	body := questionLine + "\n" +
		`{"type":"attachment","blob":"` + strings.Repeat("x", 4096) + `"}` + "\n"
	os.WriteFile(p, []byte(body), 0o644)

	w := Watch(stub{s: idleSighting()}, []string{dir})
	w.Window = 512 // reaches neither the message nor the start of the file

	got, _ := w.Observe(target("s1", true))
	if got.InputRequestVisibility != policy.VisibilityUnavailable {
		t.Errorf("Visibility = %q, want unavailable — the window never reached a message record", got.InputRequestVisibility)
	}
}

// The same shape, but the window covers the whole file: birddog really did
// look and really did see no question.
func TestWholeFileReadWithNoQuestionIsNotObserved(t *testing.T) {
	dir := projects(t)
	p := filepath.Join(dir, "-p", "s1.jsonl")
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte(statementLine+"\n"), 0o644)

	w := Watch(stub{s: idleSighting()}, []string{dir})
	got, _ := w.Observe(target("s1", true))
	if got.InputRequestVisibility != policy.VisibilityNotObserved {
		t.Errorf("Visibility = %q, want not_observed", got.InputRequestVisibility)
	}
}

// Finding 3: every other path into InputRequestDetail is bounded. This one
// reads free-form prose, so it is the widest.
func TestInferredDetailIsBounded(t *testing.T) {
	long := strings.Repeat("a very long clause with no sentence break ", 200)
	dir := t.TempDir()
	p := filepath.Join(dir, "-p", "s1.jsonl")
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"`+long+` shall I?"}]}}`+"\n"), 0o644)

	w := Watch(stub{s: idleSighting()}, []string{dir})
	got, _ := w.Observe(target("s1", true))
	if n := len([]rune(got.InputRequestDetail)); n > 201 {
		t.Errorf("InputRequestDetail is %d runes; every other path bounds it at 200", n)
	}
}

// Finding 4: a stale evidence value from the inner observer must not survive
// a pass that reports no request.
func TestStaleEvidenceIsClearedWhenNoRequestIsReported(t *testing.T) {
	dir := projects(t, "-p/other.jsonl")
	inner := idleSighting()
	inner.Evidence = policy.EvidenceRegistry
	w := Watch(stub{s: inner}, []string{dir})

	got, _ := w.Observe(target("s1", true))
	if got.Evidence != "" {
		t.Errorf("Evidence = %q with no input request reported; want empty", got.Evidence)
	}
}
