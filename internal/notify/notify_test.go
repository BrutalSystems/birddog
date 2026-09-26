package notify

import (
	"errors"
	"testing"
	"time"
)

func alert() Alert {
	return Alert{
		InstanceID: "bd-1", TargetID: "worker-1", IncidentID: 7,
		Condition: "input_requested", OpenedAt: time.Now().UTC(),
		Labels: map[string]string{"task_id": "T-42"},
	}
}

// Every alert names its instance, so an orchestrator with overlapping watches
// can tell which one is speaking (criterion 20).
func TestAlertTextIdentifiesTheInstanceAndTarget(t *testing.T) {
	got := alert().Text()
	for _, want := range []string{"bd-1", "worker-1", "input_requested"} {
		if !contains(got, want) {
			t.Errorf("alert text missing %q:\n%s", want, got)
		}
	}
}

// The text must not overclaim. An observed condition is not a diagnosis.
func TestAlertTextSaysWhatWasObservedNotWhatItMeans(t *testing.T) {
	got := alert().Text()
	for _, forbidden := range []string{"stalled", "stuck", "finished", "complete", "blocked"} {
		if contains(got, forbidden) {
			t.Errorf("alert text says %q, which is a judgement birddog did not make:\n%s", forbidden, got)
		}
	}
}

func TestNoneDeliversNothingAndSucceeds(t *testing.T) {
	outcome, err := None{}.Deliver(alert())
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if outcome != Suppressed {
		t.Errorf("Outcome = %v, want Suppressed", outcome)
	}
}

func TestFakeRecordsWhatItWasAskedToDeliver(t *testing.T) {
	f := &Fake{}
	if _, err := f.Deliver(alert()); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(f.Delivered) != 1 || f.Delivered[0].IncidentID != 7 {
		t.Errorf("Delivered = %+v", f.Delivered)
	}
}

func TestFakeCanReportAFailure(t *testing.T) {
	f := &Fake{Outcome: Failed, Err: errors.New("no route")}
	outcome, err := f.Deliver(alert())
	if outcome != Failed {
		t.Errorf("Outcome = %v, want Failed", outcome)
	}
	if err == nil {
		t.Error("err = nil, want the failure surfaced")
	}
}

// The case the handoff is most careful about: an asynchronous call that
// returned without confirming anything. It is neither success nor failure.
func TestFakeCanReportAnUncertainDelivery(t *testing.T) {
	f := &Fake{Outcome: Unknown}
	outcome, _ := f.Deliver(alert())
	if outcome != Unknown {
		t.Errorf("Outcome = %v, want Unknown", outcome)
	}
}

func TestOutcomesAreDistinguishable(t *testing.T) {
	seen := map[string]bool{}
	for _, o := range []Outcome{Delivered, Failed, Unknown, Suppressed, NoRecipient} {
		s := o.String()
		if s == "" || seen[s] {
			t.Errorf("outcome %d has an unusable or duplicate name %q", o, s)
		}
		seen[s] = true
	}
}

func contains(h, n string) bool {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return true
		}
	}
	return false
}
