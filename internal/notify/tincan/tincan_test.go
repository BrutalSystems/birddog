package tincan

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/BrutalSystems/birddog/internal/notify"
)

func alert() notify.Alert {
	return notify.Alert{
		InstanceID: "bd-8bcbebb485fa",
		TargetID:   "worker-1",
		IncidentID: 42,
		Condition:  "input_requested",
		Status:     "waiting_input",
		OpenedAt:   time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC),
	}
}

// run stands in for the CLI: it records the argv it was given and returns a
// canned result.
type run struct {
	stdout string
	stderr string
	code   int
	err    error
	argv   []string
}

func (r *run) fn(_ time.Duration, name string, args ...string) ([]byte, []byte, int, error) {
	r.argv = append([]string{name}, args...)
	return []byte(r.stdout), []byte(r.stderr), r.code, r.err
}

func connector(r *run) *Connector {
	return &Connector{Peer: "orchestrator", Binary: "tincan", Run: r.fn}
}

func TestAcceptedIsDelivered(t *testing.T) {
	r := &run{stdout: `{"outcome":"accepted","message_id":"msg_1","peer_state":"idle"}`, code: 0}
	got, err := connector(r).Deliver(alert())
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if got != notify.Delivered {
		t.Errorf("Outcome = %v, want Delivered", got)
	}
}

// From tincan 2.0.0 this refusal means the peer being addressed really does
// hold the message: the case where the name moved to another session since
// the first send has its own refusal, below. So it makes the same claim
// `accepted` does, and reporting it as uncertain would leave an alert that
// did arrive looking like one that might not have.
//
// The probe is what makes this safe. Only a tincan that draws the
// distinction gets past startup.
func TestDuplicateSendIsDelivered(t *testing.T) {
	r := &run{stdout: `{"outcome":"rejected","refusal":"duplicate_send","message_id":"msg_1"}`, code: 2}
	got, err := connector(r).Deliver(alert())
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if got != notify.Delivered {
		t.Errorf("Outcome = %v, want Delivered: duplicate_send now means the peer addressed has the message", got)
	}
}

// The retry's target name has since moved to a different session, so the
// session holding it now never saw this alert. Nothing was established:
// `delivered` would claim a worker was told something it may never have been
// told, and `failed` invites a retry that resolves identically. Unknown is
// the value for a call that settled nothing, and it stops further attempts so
// the event feed carries the recovery.
func TestDuplicatePeerMovedIsUnknown(t *testing.T) {
	r := &run{stdout: `{"outcome":"rejected","refusal":"duplicate_peer_moved","message_id":"msg_1"}`, code: 2}
	got, err := connector(r).Deliver(alert())
	if got != notify.Unknown {
		t.Errorf("Outcome = %v, want Unknown: the name moved, so the session holding it may never have seen this", got)
	}
	if err == nil || !strings.Contains(err.Error(), "duplicate_peer_moved") {
		t.Errorf("err = %v, want it to name duplicate_peer_moved", err)
	}
	// Unknown is also where an unrecognised refusal lands, so the outcome
	// alone cannot tell whether this case is handled or merely unhandled.
	// A build that does not know this refusal says so, and that is the
	// difference worth pinning.
	if err != nil && strings.Contains(err.Error(), "unrecognised") {
		t.Errorf("err = %v, want a refusal this build knows, not the unrecognised-reason fallback", err)
	}
}

// A birddog bug: its key derivation collided. Nothing was sent, which is what
// Failed means, and it must not be silently treated as a recipient problem.
func TestKeyReusedIsFailed(t *testing.T) {
	r := &run{stdout: `{"outcome":"rejected","refusal":"key_reused","message_id":"msg_other"}`, code: 2}
	got, err := connector(r).Deliver(alert())
	if got != notify.Failed {
		t.Errorf("Outcome = %v, want Failed", got)
	}
	if err == nil || !strings.Contains(err.Error(), "key_reused") {
		t.Errorf("err = %v, want it to name key_reused", err)
	}
}

// A peer that cannot be resolved resolves identically next time, so retrying
// spends the budget on a configuration error.
func TestUnresolvablePeerIsNoRecipient(t *testing.T) {
	for _, refusal := range []string{"peer_unknown", "peer_ambiguous", "peer_unreachable", "peer_changed", "self_send"} {
		r := &run{stdout: `{"outcome":"rejected","refusal":"` + refusal + `","message_id":"m"}`, code: 2}
		got, _ := connector(r).Deliver(alert())
		if got != notify.NoRecipient {
			t.Errorf("%s -> %v, want NoRecipient", refusal, got)
		}
	}
}

func TestTransportFailureIsFailed(t *testing.T) {
	r := &run{stdout: `{"outcome":"failed","refusal":"delivery_failed","message_id":"m"}`, code: 1}
	got, _ := connector(r).Deliver(alert())
	if got != notify.Failed {
		t.Errorf("Outcome = %v, want Failed", got)
	}
}

// Exit 64 carries no JSON by design: birddog invoked it wrongly. Nothing
// arrived, and the error must say so rather than leaving an empty parse.
func TestUsageErrorIsFailedAndExplains(t *testing.T) {
	r := &run{stdout: "", stderr: "tincan send: --to is required.", code: 64}
	got, err := connector(r).Deliver(alert())
	if got != notify.Failed {
		t.Errorf("Outcome = %v, want Failed", got)
	}
	if err == nil || !strings.Contains(err.Error(), "64") {
		t.Errorf("err = %v, want it to name the usage exit", err)
	}
}

// A timeout establishes nothing: the send may or may not have landed, so
// resending risks a duplicate the recipient cannot deduplicate.
func TestTimeoutIsUnknown(t *testing.T) {
	r := &run{err: errors.New("signal: killed"), code: -1}
	got, _ := connector(r).Deliver(alert())
	if got != notify.Unknown {
		t.Errorf("Outcome = %v, want Unknown", got)
	}
}

func TestUnparseableOutputIsUnknown(t *testing.T) {
	r := &run{stdout: "not json at all", code: 0}
	got, _ := connector(r).Deliver(alert())
	if got != notify.Unknown {
		t.Errorf("Outcome = %v, want Unknown", got)
	}
}

// The argv is the contract. --from must be the instance id, which is already
// slug-shaped; the key must be stable per incident so a retry deduplicates;
// and --reply-via must name the route that actually reaches birddog.
func TestArgvCarriesTheContract(t *testing.T) {
	r := &run{stdout: `{"outcome":"accepted","message_id":"m"}`, code: 0}
	if _, err := connector(r).Deliver(alert()); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	joined := strings.Join(r.argv, " ")
	for _, want := range []string{
		"tincan send",
		"--to orchestrator",
		"--from bd-8bcbebb485fa",
		"--idempotency-key bd-8bcbebb485fa-42",
		"birddog ack --instance bd-8bcbebb485fa --incident 42",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv missing %q\n  got: %s", want, joined)
		}
	}
}

// Retrying the same incident must reuse the key, or tincan cannot deduplicate.
func TestTheKeyIsStableAcrossRetries(t *testing.T) {
	c := connector(&run{stdout: `{"outcome":"accepted","message_id":"m"}`, code: 0})
	first := c.idempotencyKey(alert())
	second := c.idempotencyKey(alert())
	if first != second {
		t.Errorf("key changed between attempts: %q vs %q", first, second)
	}
}
