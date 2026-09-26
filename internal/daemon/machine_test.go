package daemon

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BrutalSystems/birddog/internal/ipc"
	"github.com/BrutalSystems/birddog/internal/machine"
	"github.com/BrutalSystems/birddog/internal/monitor"
	"github.com/BrutalSystems/birddog/internal/policy"
	"github.com/BrutalSystems/birddog/internal/store"
)

// startWithMachine mirrors start() in daemon_test.go, with an identity
// configured. Kept separate rather than changing start's signature, which
// every other test in this package calls.
func startWithMachine(t *testing.T, obs monitor.Observer, machineID string) *Daemon {
	t.Helper()
	dir := shortDir(t)
	d, err := Start(Options{
		InstanceID:  "bd-test01",
		Machine:     machineID,
		Config:      testConfig(),
		SocketPath:  filepath.Join(dir, "s.sock"),
		DBPath:      filepath.Join(dir, "i.db"),
		LockPath:    filepath.Join(dir, "i.lock"),
		RegistryDir: filepath.Join(dir, "registry"),
		Interval:    20 * time.Millisecond,
		Observers:   map[string]monitor.Observer{"fake": obs},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = d.Shutdown() })
	return d
}

// eventsParams encodes params the way the control channel delivers them.
// d.events takes the raw JSON, so a test that passed a struct would skip the
// unmarshal the real path performs.
func eventsParams(t *testing.T, p EventsParams) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return data
}

// The identity has to reach the durable record, not just the daemon's own
// memory. This is what makes a cursor from this instance comparable with one
// from another host, and it is the whole point of supplying it.
func TestObservationsRecordTheConfiguredMachine(t *testing.T) {
	d := startWithMachine(t, &spyObserver{status: policy.StatusActive, live: true}, "ferry:7c3a91b2")

	waitFor(t, "an observation to be recorded", func() bool {
		events, err := d.store.After(0, 10)
		return err == nil && len(events) > 0
	})

	events, err := d.store.After(0, 10)
	if err != nil {
		t.Fatalf("after: %v", err)
	}
	if events[0].Machine != "ferry:7c3a91b2" {
		t.Errorf("recorded machine = %q, want ferry:7c3a91b2", events[0].Machine)
	}
}

// Local mode records no machine. The column is empty, not a placeholder.
func TestLocalModeRecordsNoMachine(t *testing.T) {
	d := startWithMachine(t, &spyObserver{status: policy.StatusActive, live: true}, "")

	waitFor(t, "an observation to be recorded", func() bool {
		events, err := d.store.After(0, 10)
		return err == nil && len(events) > 0
	})

	events, _ := d.store.After(0, 10)
	if events[0].Machine != "" {
		t.Errorf("recorded machine = %q, want empty in local mode", events[0].Machine)
	}
}

// A malformed identity fails the start. Dropping it silently would leave an
// operator believing cursors were qualified while they were not.
func TestAMalformedMachineIdentityIsRefused(t *testing.T) {
	t.Setenv(machine.EnvVar, "has a space")

	_, err := machine.FromEnv()
	if err == nil {
		t.Fatal("FromEnv() = nil, want an error for a value containing whitespace")
	}
	if !strings.Contains(err.Error(), machine.EnvVar) {
		t.Errorf("error %q does not name %s, which is what an operator has to fix", err, machine.EnvVar)
	}
}

// Local mode must emit no key at all. A consumer that branches on the key
// being present would read "" as an identity that exists and is blank.
func TestLocalModeOmitsTheMachineKeyEntirely(t *testing.T) {
	for _, v := range []any{
		StatusResult{InstanceID: "i1"},
		EventsResult{Events: []EventView{}},
		EventView{Cursor: 1},
		TargetStatus{ID: "t1"},
		TargetStatus{ID: "t2", SessionID: "ses_1"}, // unobserved: no machine
	} {
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal %T: %v", v, err)
		}
		if strings.Contains(string(data), `"machine"`) {
			t.Errorf("%T in local mode marshalled %s, want no machine key", v, data)
		}
	}
}

// A configured daemon carries it on everything a consumer could resume from.
func TestAConfiguredMachineAppearsOnTheWire(t *testing.T) {
	data, err := json.Marshal(StatusResult{InstanceID: "i1", Machine: "ferry:7c3a91b2"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), `"machine":"ferry:7c3a91b2"`) {
		t.Errorf("marshalled %s, want the machine carried", data)
	}
}

// Status qualifies its cursor, and each target qualifies its session
// reference. A consumer holding feeds from two hosts needs both.
func TestStatusAndTargetsCarryTheMachine(t *testing.T) {
	d := startWithMachine(t, &spyObserver{status: policy.StatusActive, live: true}, "ferry:7c3a91b2")

	// After an observation: a target qualifies its session reference with the
	// machine that reference was observed on, which does not exist until
	// something has been observed.
	waitFor(t, "state to be recorded", func() bool {
		_, ok, err := d.store.TargetState("worker-1")
		return err == nil && ok
	})

	st, err := d.status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.Machine != "ferry:7c3a91b2" {
		t.Errorf("status machine = %q, want ferry:7c3a91b2", st.Machine)
	}
	if len(st.Targets) == 0 {
		t.Fatal("no targets in status")
	}
	if st.Targets[0].Machine != "ferry:7c3a91b2" {
		t.Errorf("target machine = %q, want the session reference qualified", st.Targets[0].Machine)
	}
}

// A mismatch is refused on the first request, not only on a resumption. A
// consumer starting fresh is the one most likely to be pointed at the wrong
// machine, and after=0 must not be a hole in the check.
func TestAMismatchIsRefusedOnTheFirstRequest(t *testing.T) {
	d := startWithMachine(t, &spyObserver{status: policy.StatusActive, live: true}, "A")

	_, err := d.events(eventsParams(t, EventsParams{After: 0, Machine: "B"}))
	if err == nil {
		t.Fatal("events with a mismatched machine and after=0 = nil, want machine_mismatch")
	}
	var ipcErr *ipc.Error
	if !errors.As(err, &ipcErr) || ipcErr.Code != ipc.CodeMachineMismatch {
		t.Errorf("error = %v, want code %s", err, ipc.CodeMachineMismatch)
	}
}

// A caller that sends none keeps the behaviour it had before identities
// existed, even against a configured daemon. This is what keeps the change
// additive.
func TestACallerThatSendsNoMachineIsStillAnswered(t *testing.T) {
	d := startWithMachine(t, &spyObserver{status: policy.StatusActive, live: true}, "A")

	if _, err := d.events(eventsParams(t, EventsParams{After: 0})); err != nil {
		t.Errorf("events without a machine = %v, want it answered", err)
	}
}

// Every page of events says which machine its cursors belong to, including an
// empty one — an empty page still advances a consumer's position.
func TestAnEmptyPageStillCarriesTheMachine(t *testing.T) {
	d := startWithMachine(t, &spyObserver{status: policy.StatusActive, live: true}, "A")

	r, err := d.events(eventsParams(t, EventsParams{After: 1 << 40}))
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if r.Machine != "A" {
		t.Errorf("empty page machine = %q, want A", r.Machine)
	}
}

// Machine is the coarser qualifier — which host — and store the finer — which
// history on that host. Resolving them in the wrong order makes the machine
// answer unreachable: store ids are random per store, so a cursor from another
// machine always carries a foreign store id too, and a consumer following the
// documented advice to send both was told store_replaced. Its remediation for
// that is to discard the position and re-baseline, so being pointed at the
// wrong host cost it a position that was never lost.
func TestAWrongMachineIsReportedAsSuchEvenWhenTheStoreIsAlsoForeign(t *testing.T) {
	d := startWithMachine(t, &spyObserver{status: policy.StatusActive, live: true}, "A")

	_, err := d.events(eventsParams(t, EventsParams{
		After: 4271, Machine: "B", Store: "a-store-id-from-machine-B",
	}))
	if err == nil {
		t.Fatal("events = nil, want a refusal")
	}
	var ipcErr *ipc.Error
	if !errors.As(err, &ipcErr) {
		t.Fatalf("error %v is not an ipc.Error", err)
	}
	if ipcErr.Code != ipc.CodeMachineMismatch {
		t.Errorf("code = %s, want %s: the wrong host is the accurate answer, and %s tells the consumer its history was destroyed",
			ipcErr.Code, ipc.CodeMachineMismatch, ipc.CodeStoreReplaced)
	}
}

// The store check still fires when the machine agrees, which is the case it
// was built for: same host, database replaced beneath it.
func TestAReplacedStoreOnTheSameMachineIsStillStoreReplaced(t *testing.T) {
	d := startWithMachine(t, &spyObserver{status: policy.StatusActive, live: true}, "A")

	_, err := d.events(eventsParams(t, EventsParams{
		After: 4271, Machine: "A", Store: "a-store-that-no-longer-exists",
	}))
	if err == nil {
		t.Fatal("events = nil, want a refusal")
	}
	var ipcErr *ipc.Error
	if !errors.As(err, &ipcErr) || ipcErr.Code != ipc.CodeStoreReplaced {
		t.Errorf("error = %v, want %s", err, ipc.CodeStoreReplaced)
	}
}

// A target's machine comes from the state that was stored, not from whoever
// is reading it now. An instance resumed under a relabelled identity must not
// restamp a session it never observed: the stored reference is the one thing
// that knows where it came from.
func TestATargetsMachineComesFromTheStoredStateNotTheReader(t *testing.T) {
	// Local mode: the daemon's own identity is empty, so anything non-empty
	// in the answer can only have come from the stored record.
	d := startWithMachine(t, &spyObserver{status: policy.StatusActive, live: true}, "")

	waitFor(t, "state to be recorded", func() bool {
		_, ok, err := d.store.TargetState("worker-1")
		return err == nil && ok
	})
	cur, _, err := d.store.TargetState("worker-1")
	if err != nil {
		t.Fatalf("read state: %v", err)
	}

	// Stand in for a state observed under another identity: the same session
	// generation, at a later moment so it is not rejected as overtaken.
	if _, applied, err := d.store.Record(store.Event{
		InstanceID: "bd-test01", TargetID: "worker-1", Type: "observation",
		Source: "fake", At: time.Now().Add(time.Hour), Status: "active",
		SessionID: cur.SessionID, Generation: cur.Generation, Machine: "A",
	}); err != nil || !applied {
		t.Fatalf("seed state: applied=%v err=%v", applied, err)
	}

	st, err := d.status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	var found bool
	for _, ts := range st.Targets {
		if ts.ID != "worker-1" {
			continue
		}
		found = true
		if ts.Machine != "A" {
			t.Errorf("target machine = %q, want A from the stored state; the daemon's own identity is empty", ts.Machine)
		}
	}
	if !found {
		t.Fatal("fake-target not in status")
	}
}
