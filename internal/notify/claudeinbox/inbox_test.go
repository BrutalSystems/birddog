package claudeinbox

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BrutalSystems/birddog/internal/notify"
)

// fakeInbox stands in for a Claude Code session's socket. It records the
// frames it receives and can reply as the harness does.
type fakeInbox struct {
	path   string
	frames chan map[string]any
	reply  func(msgID string) string

	// hangUpAfterFrames closes the connection once this many frames have been
	// read, standing in for a session that died mid-delivery. Counting frames
	// rather than closing after the first one is what makes it deterministic:
	// closing after the auth frame races the connector's message write, which
	// then either lands in the socket buffer (read fails later: Unknown) or
	// takes EPIPE (write fails: Failed). CI drew the second one.
	hangUpAfterFrames int
}

func newFakeInbox(t *testing.T) *fakeInbox {
	t.Helper()
	// Short path: macOS refuses to bind a unix socket past 104 bytes.
	dir, err := os.MkdirTemp("/tmp", "bdinbox")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	f := &fakeInbox{path: filepath.Join(dir, "s.sock"), frames: make(chan map[string]any, 8)}
	ln, err := net.Listen("unix", f.path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				read := 0
				defer conn.Close()
				sc := bufio.NewScanner(conn)
				for sc.Scan() {
					var frame map[string]any
					if json.Unmarshal(sc.Bytes(), &frame) != nil {
						continue
					}
					f.frames <- frame
					read++
					if f.hangUpAfterFrames > 0 && read >= f.hangUpAfterFrames {
						return
					}
					if f.reply != nil {
						if id, _ := frame["msg_id"].(string); id != "" {
							_, _ = conn.Write([]byte(f.reply(id) + "\n"))
						}
					}
				}
			}()
		}
	}()
	return f
}

func (f *fakeInbox) next(t *testing.T) map[string]any {
	t.Helper()
	select {
	case frame := <-f.frames:
		return frame
	case <-time.After(3 * time.Second):
		t.Fatal("no frame received")
		return nil
	}
}

func alert() notify.Alert {
	return notify.Alert{
		InstanceID: "bd-1", TargetID: "worker-1", IncidentID: 7,
		Condition: "input_requested", OpenedAt: time.Now().UTC(),
	}
}

func connector(f *fakeInbox, token string) *Connector {
	return &Connector{
		Resolve: func() (Recipient, error) {
			return Recipient{SocketPath: f.path, PeerToken: token, ProcStart: "start", PIDDomain: "darwin"}, nil
		},
		ReceiptWindow: 200 * time.Millisecond,
	}
}

func TestDeliverAuthenticatesBeforeSending(t *testing.T) {
	f := newFakeInbox(t)
	go func() { _, _ = connector(f, "tok-123").Deliver(alert()) }()

	auth := f.next(t)
	if auth["type"] != "auth" {
		t.Fatalf("first frame = %v, want auth", auth)
	}
	if auth["peerToken"] != "tok-123" {
		t.Errorf("peerToken = %v", auth["peerToken"])
	}
}

func TestDeliverSendsTheAlertAsAUserMessage(t *testing.T) {
	f := newFakeInbox(t)
	go func() { _, _ = connector(f, "tok").Deliver(alert()) }()

	_ = f.next(t) // auth
	msg := f.next(t)
	if msg["type"] != "user" {
		t.Fatalf("second frame = %v, want user", msg)
	}
	body, _ := msg["message"].(map[string]any)
	content, _ := body["content"].(string)
	for _, want := range []string{"bd-1", "worker-1", "input_requested"} {
		if !strings.Contains(content, want) {
			t.Errorf("content missing %q:\n%s", want, content)
		}
	}
}

// Non-disruptive delivery is the whole requirement. "next" queues the message
// for the turn boundary; anything that interrupts a running turn would make
// birddog disturb the orchestrator it is reporting to.
func TestDeliverQueuesRatherThanInterrupting(t *testing.T) {
	f := newFakeInbox(t)
	go func() { _, _ = connector(f, "tok").Deliver(alert()) }()

	_ = f.next(t)
	msg := f.next(t)
	if msg["priority"] != "next" {
		t.Errorf("priority = %v, want next — birddog must not interrupt a running turn", msg["priority"])
	}
}

// A stable id per incident, so a destination that can deduplicate has what it
// needs to.
func TestDeliverCarriesAStableMessageIDPerIncident(t *testing.T) {
	f := newFakeInbox(t)
	c := connector(f, "tok")

	go func() { _, _ = c.Deliver(alert()) }()
	_ = f.next(t)
	first := f.next(t)

	go func() { _, _ = c.Deliver(alert()) }()
	_ = f.next(t)
	second := f.next(t)

	if first["msg_id"] != second["msg_id"] {
		t.Errorf("msg_id changed for the same incident: %v then %v", first["msg_id"], second["msg_id"])
	}
	if first["msg_id"] == "" {
		t.Error("no msg_id sent")
	}
}

// An acknowledgement naming our message is the only thing that establishes
// delivery.
func TestAcknowledgedMessageIsDelivered(t *testing.T) {
	f := newFakeInbox(t)
	f.reply = func(id string) string {
		return `{"type":"peer_message_status","orig_msg_id":"` + id + `","status":"queued"}`
	}

	outcome, err := connector(f, "tok").Deliver(alert())
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if outcome != notify.Delivered {
		t.Errorf("Outcome = %v, want Delivered once the receiver acknowledged it", outcome)
	}
}

// This protocol has no positive acknowledgement: the session replies only to
// report a hold. Silence is therefore its normal acceptance, and treating it
// as uncertain would label every successful delivery "unknown" forever —
// never marking anything notified, and never retrying anything either.
//
// Verified live: an alert delivered to a real Claude Code session arrives, and
// nothing comes back.
func TestSilenceIsAcceptanceBecauseThisRouteNeverAcknowledges(t *testing.T) {
	f := newFakeInbox(t) // never replies, as the real harness does not

	outcome, err := connector(f, "tok").Deliver(alert())
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if outcome != notify.Delivered {
		t.Errorf("Outcome = %v, want Delivered — silence is how this route accepts", outcome)
	}
}

// The genuinely uncertain case, and the one criterion 18 is about: the alert
// went out whole, and then the connection broke instead of answering. Whether
// the session acted on it is unknowable from this side — "died before reading
// it" and "read it, then died" are the same two bytes of nothing — so it is
// neither resent nor rerouted.
//
// Distinct from a write that fails outright, which Deliver reports as Failed:
// there the bytes never left this process, so the alert definitely did not
// arrive and a retry is right. That path has no test because it cannot be
// produced deterministically through a socket — it is the race this harness
// used to lose — so the two outcomes are kept apart by Deliver's own doc
// comment rather than by an assertion here.
func TestConnectionFailingAfterTheWriteIsUncertain(t *testing.T) {
	f := newFakeInbox(t)
	f.hangUpAfterFrames = 2 // auth, then the alert itself — both accepted

	outcome, err := connector(f, "tok").Deliver(alert())
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if outcome != notify.Unknown {
		t.Errorf("Outcome = %v, want Unknown when the connection broke mid-delivery", outcome)
	}
}

// A held message was received; the recipient's human may release it later.
// That is an acknowledgement, not a failure.
func TestHeldMessageCountsAsDelivered(t *testing.T) {
	f := newFakeInbox(t)
	f.reply = func(id string) string {
		return `{"type":"peer_message_status","orig_msg_id":"` + id + `","status":"held","detail":"awaiting approval"}`
	}

	outcome, _ := connector(f, "tok").Deliver(alert())
	if outcome != notify.Delivered {
		t.Errorf("Outcome = %v, want Delivered — a hold is receipt, not refusal", outcome)
	}
}

// An acknowledgement for somebody else's message is not ours.
func TestAcknowledgementForAnotherMessageIsIgnored(t *testing.T) {
	f := newFakeInbox(t)
	f.reply = func(string) string {
		return `{"type":"peer_message_status","orig_msg_id":"someone-elses","status":"queued"}`
	}

	// Still accepted — silence about *our* message is this route's normal
	// acceptance — but the other message's receipt must not be read as ours.
	outcome, _ := connector(f, "tok").Deliver(alert())
	if outcome != notify.Delivered {
		t.Errorf("Outcome = %v, want Delivered", outcome)
	}
}

// A session that is gone definitely did not receive it, so a retry is safe.
func TestUnreachableSessionIsADefiniteFailure(t *testing.T) {
	c := &Connector{
		Resolve: func() (Recipient, error) {
			return Recipient{SocketPath: "/tmp/bdinbox-nonexistent/s.sock", PeerToken: "tok"}, nil
		},
		ReceiptWindow: 50 * time.Millisecond,
	}

	outcome, err := c.Deliver(alert())
	if outcome != notify.Failed {
		t.Errorf("Outcome = %v, want Failed for an unreachable session", outcome)
	}
	if err == nil {
		t.Error("err = nil, want the failure explained")
	}
}

// Being unable to find the recipient at all is not a failed delivery: nothing
// was attempted, and the next attempt would resolve the same way. Reporting it
// as Failed spends the retry budget on a configuration error and then presents
// it to the operator as a transport fault.
func TestUnresolvableRecipientReportsNoRecipient(t *testing.T) {
	c := &Connector{Resolve: func() (Recipient, error) { return Recipient{}, os.ErrNotExist }}

	outcome, err := c.Deliver(alert())
	if outcome != notify.NoRecipient || err == nil {
		t.Errorf("Outcome = %v err = %v, want no_recipient explained", outcome, err)
	}
}

// The token authenticates birddog to the session. It must never travel
// anywhere else — least of all into an alert a human will read.
func TestTheAlertTextNeverCarriesTheToken(t *testing.T) {
	f := newFakeInbox(t)
	go func() { _, _ = connector(f, "super-secret-token").Deliver(alert()) }()

	_ = f.next(t)
	msg := f.next(t)
	body, _ := msg["message"].(map[string]any)
	content, _ := body["content"].(string)
	if strings.Contains(content, "super-secret-token") {
		t.Error("the peer token appeared in the message body")
	}
}

func TestConnectorIsNamedForItsRoute(t *testing.T) {
	if got := (&Connector{}).Name(); got != Kind {
		t.Errorf("Name = %q, want %q", got, Kind)
	}
}
