package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BrutalSystems/birddog/internal/config"
	"github.com/BrutalSystems/birddog/internal/instance"
	"github.com/BrutalSystems/birddog/internal/ipc"
	"github.com/BrutalSystems/birddog/internal/monitor"
	"github.com/BrutalSystems/birddog/internal/policy"
	"github.com/BrutalSystems/birddog/internal/store"
)

// spyObserver is read by the test goroutine while the observation loop writes
// it, so its counter is atomic.
type spyObserver struct {
	status string
	live   bool

	// known says whether the status is readable, which is not the same as
	// being in contact: a verified exit is not live and is certain anyway.
	// Unset means it follows live, which is what every existing test wants.
	known   *bool
	observe atomic.Int64
}

func (s *spyObserver) Observe(config.Target) (monitor.Sighting, error) {
	s.observe.Add(1)
	now := time.Now()
	known := s.live
	if s.known != nil {
		known = *s.known
	}
	return monitor.Sighting{
		Observation: policy.Observation{
			Live: s.live, Status: s.status, StatusKnown: known,
			StatusSince: now, LastActivityAt: now, ObservedAt: now,
		},
		SessionIdentity: "session-a",
	}, nil
}

func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "bdd")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func testConfig() *config.Config {
	return &config.Config{
		SchemaVersion: 1,
		Name:          "checkout-refactor",
		Targets: []config.Target{{
			ID: "worker-1", Provider: "fake",
			Attachment: config.Attachment{Kind: "existing-session", SessionID: "s"},
			Policy: config.Policy{
				AlertOn:    []string{config.ConditionInputRequested, config.ConditionExit, config.ConditionObservationLost},
				QuietAfter: 5 * time.Minute,
				IdleGrace:  30 * time.Second,
			},
		}},
	}
}

func start(t *testing.T, obs monitor.Observer) (*Daemon, Options) {
	t.Helper()
	dir := shortDir(t)
	opts := Options{
		InstanceID:  "bd-test01",
		Config:      testConfig(),
		SocketPath:  filepath.Join(dir, "s.sock"),
		DBPath:      filepath.Join(dir, "i.db"),
		LockPath:    filepath.Join(dir, "i.lock"),
		RegistryDir: filepath.Join(dir, "registry"),
		Interval:    20 * time.Millisecond,
		Observers:   map[string]monitor.Observer{"fake": obs},
	}
	d, err := Start(opts)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = d.Shutdown() })
	return d, opts
}

// waitFor polls until cond holds, so tests do not depend on loop timing.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestStartedInstanceAnswersStatus(t *testing.T) {
	_, opts := start(t, &spyObserver{status: policy.StatusActive, live: true})

	var got StatusResult
	if err := ipc.Call(opts.SocketPath, "status", nil, &got); err != nil {
		t.Fatalf("status: %v", err)
	}
	if got.Name != "checkout-refactor" {
		t.Errorf("Name = %q", got.Name)
	}
	if got.InstanceID != "bd-test01" {
		t.Errorf("InstanceID = %q", got.InstanceID)
	}
	if len(got.Targets) != 1 || got.Targets[0].ID != "worker-1" {
		t.Errorf("Targets = %+v", got.Targets)
	}
}

func TestStatusReportsObservedStateOnceTheLoopHasRun(t *testing.T) {
	_, opts := start(t, &spyObserver{status: policy.StatusActive, live: true})

	var got StatusResult
	waitFor(t, "an observation", func() bool {
		_ = ipc.Call(opts.SocketPath, "status", nil, &got)
		return len(got.Targets) > 0 && got.Targets[0].Status == policy.StatusActive
	})
	if !got.Targets[0].StatusIsCurrent {
		t.Error("StatusIsCurrent = false for a freshly observed live session")
	}
}

// Snapshot then follow: status carries the cursor so nothing falls between the
// snapshot and the first events call.
func TestStatusCarriesTheLatestCursor(t *testing.T) {
	_, opts := start(t, &spyObserver{status: policy.StatusActive, live: true})

	var got StatusResult
	waitFor(t, "a cursor", func() bool {
		_ = ipc.Call(opts.SocketPath, "status", nil, &got)
		return got.Cursor > 0
	})
}

func TestEventsReturnsWhatWasObserved(t *testing.T) {
	_, opts := start(t, &spyObserver{status: policy.StatusActive, live: true})

	var got EventsResult
	waitFor(t, "events", func() bool {
		_ = ipc.Call(opts.SocketPath, "events", EventsParams{After: 0, Limit: 100}, &got)
		return len(got.Events) > 0
	})
	if got.Events[0].TargetID != "worker-1" {
		t.Errorf("TargetID = %q", got.Events[0].TargetID)
	}
	if got.Cursor == 0 {
		t.Error("Cursor = 0, want the position to follow from")
	}
}

// Long polling: the call waits for something to happen rather than returning
// an empty result immediately.
func TestEventsWaitsForNewEvents(t *testing.T) {
	_, opts := start(t, &spyObserver{status: policy.StatusActive, live: true})

	var first EventsResult
	waitFor(t, "an initial event", func() bool {
		_ = ipc.Call(opts.SocketPath, "events", EventsParams{After: 0, Limit: 100}, &first)
		return len(first.Events) > 0
	})

	started := time.Now()
	var got EventsResult
	err := ipc.Call(opts.SocketPath, "events", EventsParams{After: first.Cursor, Limit: 100, WaitSeconds: 5}, &got)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if len(got.Events) == 0 {
		t.Error("long poll returned nothing within its wait")
	}
	if time.Since(started) > 5*time.Second {
		t.Error("long poll outlasted its own wait")
	}
}

// A wait that elapses is an empty answer, not a failure.
func TestEventsLongPollReturnsEmptyWhenNothingHappens(t *testing.T) {
	d, opts := start(t, &spyObserver{status: policy.StatusActive, live: true})

	var head EventsResult
	waitFor(t, "an initial event", func() bool {
		_ = ipc.Call(opts.SocketPath, "events", EventsParams{After: 0, Limit: 100}, &head)
		return len(head.Events) > 0
	})
	d.PauseObservation() // nothing more will be recorded

	var got EventsResult
	if err := ipc.Call(opts.SocketPath, "events", EventsParams{After: head.Cursor, Limit: 10, WaitSeconds: 1}, &got); err != nil {
		t.Fatalf("events: %v, want an empty result rather than a failure", err)
	}
	if len(got.Events) != 0 {
		t.Errorf("got %d events, want none", len(got.Events))
	}
	if got.Cursor != head.Cursor {
		t.Errorf("Cursor = %d, want it unchanged at %d", got.Cursor, head.Cursor)
	}
}

func TestWaitBlocksAfterAVerifiedExit(t *testing.T) {
	known := true
	obs := &spyObserver{status: policy.StatusExited, live: false, known: &known}
	_, opts := start(t, obs)

	var head EventsResult
	waitFor(t, "the exit to be recorded", func() bool {
		_ = ipc.Call(opts.SocketPath, "events", EventsParams{After: 0, Limit: 100}, &head)
		return len(head.Events) > 0
	})
	recorded := len(head.Events)
	passes := obs.observe.Load()

	// Let the loop run many more passes. Each one re-observes a session that is
	// verifiably gone, and none of them has anything new to say.
	waitFor(t, "ten more observation passes", func() bool {
		return obs.observe.Load() >= passes+10
	})

	// The target is gone and nothing else will ever happen to it, so a long
	// poll must wait out its bound rather than return a restatement.
	var got EventsResult
	started := time.Now()
	if err := ipc.Call(opts.SocketPath, "events",
		EventsParams{After: head.Cursor, Limit: 10, WaitSeconds: 1}, &got); err != nil {
		t.Fatalf("events: %v", err)
	}
	if len(got.Events) != 0 {
		t.Fatalf("got %d events after %d further passes, want 0: an exited target has nothing left to report",
			len(got.Events), obs.observe.Load()-passes)
	}
	if elapsed := time.Since(started); elapsed < 900*time.Millisecond {
		t.Errorf("returned after %v, want the full 1s wait: returning early is indistinguishable from an empty answer", elapsed)
	}
	if got.Cursor != head.Cursor {
		t.Errorf("Cursor = %d, want %d unchanged", got.Cursor, head.Cursor)
	}

	// The count is the half that cannot pass by accident: without the
	// suppression, ten passes are ten more events.
	var all EventsResult
	if err := ipc.Call(opts.SocketPath, "events", EventsParams{After: 0, Limit: 100}, &all); err != nil {
		t.Fatalf("events: %v", err)
	}
	if len(all.Events) != recorded {
		t.Errorf("event count went %d -> %d across %d passes, want unchanged",
			recorded, len(all.Events), obs.observe.Load()-passes)
	}
}

// Criterion 11: a cursor that cannot be honoured says so, with a stable code.
func TestStaleCursorIsACodedError(t *testing.T) {
	d, opts := start(t, &spyObserver{status: policy.StatusActive, live: true})

	waitFor(t, "several events", func() bool {
		var r EventsResult
		_ = ipc.Call(opts.SocketPath, "events", EventsParams{After: 0, Limit: 100}, &r)
		return len(r.Events) >= 3
	})
	if err := d.PruneBefore(3); err != nil {
		t.Fatalf("PruneBefore: %v", err)
	}

	err := ipc.Call(opts.SocketPath, "events", EventsParams{After: 1, Limit: 10}, &EventsResult{})
	var e *ipc.Error
	if !errors.As(err, &e) {
		t.Fatalf("error = %v, want a coded error", err)
	}
	if e.Code != ipc.CodeCursorStale {
		t.Errorf("Code = %q, want %q", e.Code, ipc.CodeCursorStale)
	}
}

func TestIncidentsAppearInStatus(t *testing.T) {
	_, opts := start(t, &spyObserver{status: policy.StatusWaitingInput, live: true})

	var got StatusResult
	waitFor(t, "an incident", func() bool {
		_ = ipc.Call(opts.SocketPath, "status", nil, &got)
		return len(got.Targets) > 0 && len(got.Targets[0].Incidents) > 0
	})
	if got.Targets[0].Incidents[0].Condition != config.ConditionInputRequested {
		t.Errorf("Condition = %q", got.Targets[0].Incidents[0].Condition)
	}
}

// Criterion 4: acknowledging records that the orchestrator saw the alert. The
// worker is still waiting afterwards.
func TestAcknowledgingDoesNotResolveTheIncident(t *testing.T) {
	_, opts := start(t, &spyObserver{status: policy.StatusWaitingInput, live: true})

	var st StatusResult
	waitFor(t, "an incident", func() bool {
		_ = ipc.Call(opts.SocketPath, "status", nil, &st)
		return len(st.Targets) > 0 && len(st.Targets[0].Incidents) > 0
	})
	id := st.Targets[0].Incidents[0].ID

	if err := ipc.Call(opts.SocketPath, "ack", AckParams{IncidentID: id}, &AckResult{}); err != nil {
		t.Fatalf("ack: %v", err)
	}

	var after StatusResult
	if err := ipc.Call(opts.SocketPath, "status", nil, &after); err != nil {
		t.Fatalf("status: %v", err)
	}
	if len(after.Targets[0].Incidents) != 1 {
		t.Fatalf("incidents = %+v, want the incident still open after acknowledgement", after.Targets[0].Incidents)
	}
	if after.Targets[0].Incidents[0].AcknowledgedAt == nil {
		t.Error("acknowledgement not recorded")
	}
}

func TestUnknownMethodIsACodedError(t *testing.T) {
	_, opts := start(t, &spyObserver{status: policy.StatusActive, live: true})

	err := ipc.Call(opts.SocketPath, "nudge", nil, &struct{}{})
	var e *ipc.Error
	if !errors.As(err, &e) {
		t.Fatalf("error = %v, want a coded error", err)
	}
	if e.Code != ipc.CodeUnknownMethod {
		t.Errorf("Code = %q, want %q", e.Code, ipc.CodeUnknownMethod)
	}
}

// Criterion 12: there is no way to reach a watched worker through the control
// channel. This is the boundary written down where it can be checked.
func TestNoControlMethodCanReachAWatchedWorker(t *testing.T) {
	_, opts := start(t, &spyObserver{status: policy.StatusActive, live: true})

	for _, method := range []string{"send", "prompt", "nudge", "continue", "approve", "kill", "restart", "stop_worker"} {
		err := ipc.Call(opts.SocketPath, method, nil, &struct{}{})
		var e *ipc.Error
		if !errors.As(err, &e) || e.Code != ipc.CodeUnknownMethod {
			t.Errorf("method %q is served — birddog must expose no way to act on a worker", method)
		}
	}
}

func TestSecondDaemonForTheSameInstanceIsRefused(t *testing.T) {
	_, opts := start(t, &spyObserver{status: policy.StatusActive, live: true})

	second, err := Start(opts)
	if err == nil {
		_ = second.Shutdown()
		t.Fatal("Start = nil error for an instance already running")
	}
	if !strings.Contains(err.Error(), "already running") {
		t.Errorf("error = %v, want it to say the instance is already running", err)
	}
}

func TestStartRegistersTheInstanceForDiscovery(t *testing.T) {
	_, opts := start(t, &spyObserver{status: policy.StatusActive, live: true})

	got, ok, err := instance.Load(opts.RegistryDir, "bd-test01")
	if err != nil || !ok {
		t.Fatalf("Load: %v ok=%v", err, ok)
	}
	if got.SocketPath != opts.SocketPath {
		t.Errorf("SocketPath = %q, want %q", got.SocketPath, opts.SocketPath)
	}
}

func TestShutdownReleasesEverythingItTook(t *testing.T) {
	d, opts := start(t, &spyObserver{status: policy.StatusActive, live: true})

	if err := d.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if _, err := os.Stat(opts.SocketPath); !os.IsNotExist(err) {
		t.Error("socket still present after shutdown")
	}
	// The record is kept so the instance stays discoverable and resumable,
	// but it must not still look like it is running.
	rec, ok, _ := instance.Load(opts.RegistryDir, "bd-test01")
	if !ok {
		t.Fatal("instance record removed on shutdown; a stopped instance cannot be resumed")
	}
	if rec.StoppedAt == nil {
		t.Error("record does not say the instance stopped")
	}

	// The lock is free, so the instance can be started again.
	again, err := Start(opts)
	if err != nil {
		t.Fatalf("Start after shutdown: %v", err)
	}
	_ = again.Shutdown()
}

func TestShutdownIsIdempotent(t *testing.T) {
	d, _ := start(t, &spyObserver{status: policy.StatusActive, live: true})
	if err := d.Shutdown(); err != nil {
		t.Fatalf("first Shutdown: %v", err)
	}
	if err := d.Shutdown(); err != nil {
		t.Errorf("second Shutdown: %v, want it to be safe to repeat", err)
	}
}

// The stop command shuts the instance down over its own control channel.
func TestStopMethodShutsTheInstanceDown(t *testing.T) {
	d, opts := start(t, &spyObserver{status: policy.StatusActive, live: true})

	if err := ipc.Call(opts.SocketPath, "stop", nil, &StopResult{}); err != nil {
		t.Fatalf("stop: %v", err)
	}
	waitFor(t, "the instance to stop", func() bool { return d.Done() })
	// Done only reports that the observation loop ended; the socket, the lock
	// and the stopped-at stamp are given back after it. Wait for the release
	// before asserting on any of them.
	d.Wait()

	if err := ipc.Call(opts.SocketPath, "status", nil, &StatusResult{}); err == nil {
		t.Error("status still answered after stop")
	}
}

// The loop keeps running: monitoring does not depend on anyone asking.
func TestObservationContinuesWithoutAnyRequests(t *testing.T) {
	obs := &spyObserver{status: policy.StatusActive, live: true}
	start(t, obs)

	waitFor(t, "repeated observation", func() bool { return obs.observe.Load() >= 3 })
}

// Wait is what the foreground process blocks on, so it must not return until
// shutdown has finished releasing things. Returning when the loop ends lets
// the process exit mid-cleanup, stranding the socket, the lock and the
// registry record.
func TestWaitReturnsOnlyAfterEverythingIsReleased(t *testing.T) {
	d, opts := start(t, &spyObserver{status: policy.StatusActive, live: true})

	waited := make(chan struct{})
	go func() {
		d.Wait()
		close(waited)
	}()

	if err := ipc.Call(opts.SocketPath, "stop", nil, &StopResult{}); err != nil {
		t.Fatalf("stop: %v", err)
	}

	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return after stop")
	}

	// Everything below is what a process exiting here would strand.
	if _, err := os.Stat(opts.SocketPath); !os.IsNotExist(err) {
		t.Error("socket still present when Wait returned")
	}
	if _, err := os.Stat(opts.LockPath); !os.IsNotExist(err) {
		t.Error("lock file still present when Wait returned")
	}
	if rec, ok, _ := instance.Load(opts.RegistryDir, opts.InstanceID); !ok || rec.StoppedAt == nil {
		t.Error("the record was not marked stopped when Wait returned")
	}
}

func TestRetentionPassDoesNothingWhenUnconfigured(t *testing.T) {
	d, opts := start(t, &spyObserver{status: policy.StatusActive, live: true})

	var before EventsResult
	waitFor(t, "an event to be recorded", func() bool {
		_ = ipc.Call(opts.SocketPath, "events", EventsParams{After: 0, Limit: 100}, &before)
		return len(before.Events) > 0
	})

	if err := d.retentionPass(time.Now().Add(365 * 24 * time.Hour)); err != nil {
		t.Fatalf("retentionPass: %v", err)
	}

	// A cursor from before the pass must still replay: with no window
	// configured there is nothing to enforce, however old the events look.
	var after EventsResult
	if err := ipc.Call(opts.SocketPath, "events", EventsParams{After: 0, Limit: 100}, &after); err != nil {
		t.Fatalf("events: %v", err)
	}
	if len(after.Events) == 0 {
		t.Error("history was dropped with no retention configured")
	}
}

func TestRetentionPassDropsHistoryPastTheWindow(t *testing.T) {
	obs := &spyObserver{status: policy.StatusActive, live: true}
	d, opts := start(t, obs)
	d.cfgMu.Lock()
	d.opts.Config.Retention = &config.Retention{
		MaxAge:          time.Hour,
		HoldUncollected: time.Hour,
	}
	d.cfgMu.Unlock()

	var head EventsResult
	waitFor(t, "several events", func() bool {
		_ = ipc.Call(opts.SocketPath, "events", EventsParams{After: 0, Limit: 100}, &head)
		return len(head.Events) >= 3
	})

	// Run the pass far enough in the future that every recorded event is past
	// the window and the hold.
	if err := d.retentionPass(time.Now().Add(24 * time.Hour)); err != nil {
		t.Fatalf("retentionPass: %v", err)
	}

	// The old cursor has fallen behind the retained history and must be
	// refused rather than answered with whatever remains.
	err := ipc.Call(opts.SocketPath, "events", EventsParams{After: 1, Limit: 10}, &EventsResult{})
	var e *ipc.Error
	if !errors.As(err, &e) {
		t.Fatalf("error = %v, want a coded error", err)
	}
	if e.Code != ipc.CodeCursorStale {
		t.Errorf("Code = %q, want %q: a consumer is told it lost something", e.Code, ipc.CodeCursorStale)
	}
}

func TestTheLoopRunsRetentionOnItsOwnCadence(t *testing.T) {
	obs := &spyObserver{status: policy.StatusActive, live: true}
	dir := shortDir(t)
	cfg := testConfig()
	// Every event this instance records will be far past this window by the
	// time the retention ticker fires.
	cfg.Retention = &config.Retention{MaxAge: time.Millisecond, HoldUncollected: time.Millisecond}

	d, err := Start(Options{
		InstanceID:        "bd-test02",
		Config:            cfg,
		SocketPath:        filepath.Join(dir, "s.sock"),
		DBPath:            filepath.Join(dir, "i.db"),
		LockPath:          filepath.Join(dir, "i.lock"),
		RegistryDir:       filepath.Join(dir, "registry"),
		Interval:          20 * time.Millisecond,
		RetentionInterval: 50 * time.Millisecond,
		Observers:         map[string]monitor.Observer{"fake": obs},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = d.Shutdown() })

	// Nothing calls retentionPass here. If the loop does not run it, the cursor
	// never goes stale and this times out.
	socket := filepath.Join(dir, "s.sock")
	stale := func(after int64) bool {
		err := ipc.Call(socket, "events", EventsParams{After: after, Limit: 10}, &EventsResult{})
		var e *ipc.Error
		return errors.As(err, &e) && e.Code == ipc.CodeCursorStale
	}
	waitFor(t, "retention to be enforced at all", func() bool { return stale(1) })

	// The startup pass alone satisfies the assertion above, so on its own it
	// would no longer prove the ticker arm exists. Take a cursor the store
	// currently accepts and wait for it to be refused: the floor can only rise
	// past it on a *later* pass, and the ticker is the only thing that runs one.
	var snap StatusResult
	if err := ipc.Call(socket, "status", nil, &snap); err != nil {
		t.Fatalf("status: %v", err)
	}
	if stale(snap.Cursor) {
		t.Fatalf("cursor %d is already refused, so this cannot detect a later pass", snap.Cursor)
	}
	waitFor(t, "a second, ticker-driven pass to raise the floor", func() bool {
		return stale(snap.Cursor)
	})
}

// An all-exited instance records nothing further, so retention empties its log
// and then runs over an empty one for as long as the daemon lives. Every pass
// after the first must leave the floor where it is and must leave the cursor
// `status` reports followable — otherwise the documented recovery from
// cursor_stale, "take a fresh status and use the cursor it reports", loops
// forever on an instance that will never report again.
func TestRepeatedRetentionPassesOverAnEmptiedLogKeepTheCursorFollowable(t *testing.T) {
	known := true
	obs := &spyObserver{status: policy.StatusExited, live: false, known: &known}
	d, opts := start(t, obs)
	d.cfgMu.Lock()
	d.opts.Config.Retention = &config.Retention{
		MaxAge:          time.Hour,
		HoldUncollected: time.Hour,
	}
	d.cfgMu.Unlock()

	// A verified exit is recorded once, so the log stops growing here and the
	// passes below run over a store nothing is adding to.
	var head EventsResult
	waitFor(t, "the exit to be recorded", func() bool {
		_ = ipc.Call(opts.SocketPath, "events", EventsParams{After: 0, Limit: 100}, &head)
		return len(head.Events) == 1
	})

	future := time.Now().Add(24 * time.Hour)
	if err := d.retentionPass(future); err != nil {
		t.Fatalf("retentionPass (first): %v", err)
	}

	var first StatusResult
	if err := ipc.Call(opts.SocketPath, "status", nil, &first); err != nil {
		t.Fatalf("status: %v", err)
	}
	if first.Cursor == 0 {
		t.Fatalf("Cursor = 0 after the log was emptied: events refuses it and status cannot offer anything else")
	}

	if err := d.retentionPass(future.Add(time.Hour)); err != nil {
		t.Fatalf("retentionPass (second): %v", err)
	}

	var second StatusResult
	if err := ipc.Call(opts.SocketPath, "status", nil, &second); err != nil {
		t.Fatalf("status: %v", err)
	}
	if second.Cursor != first.Cursor {
		t.Errorf("Cursor = %d after a second pass, want %d: the floor a real prune raised was reset",
			second.Cursor, first.Cursor)
	}

	// Followable, which is the whole point of reporting it.
	if err := ipc.Call(opts.SocketPath, "events", EventsParams{After: second.Cursor, Limit: 10}, &EventsResult{}); err != nil {
		t.Errorf("events after the reported cursor: %v, want it accepted", err)
	}
	// And the history really is gone, so a cursor from before it must still be
	// refused rather than un-staled by the second pass.
	err := ipc.Call(opts.SocketPath, "events", EventsParams{After: 0, Limit: 10}, &EventsResult{})
	var e *ipc.Error
	if !errors.As(err, &e) || e.Code != ipc.CodeCursorStale {
		t.Errorf("events after cursor 0 error = %v, want %s", err, ipc.CodeCursorStale)
	}
}

// seedOldEvents writes events dated before the daemon starts, so a retention
// window can already be overdue at the moment it comes up. The store is closed
// again because the daemon takes its own handle.
func seedOldEvents(t *testing.T, dbPath string, at time.Time, n int) {
	t.Helper()
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	for i := 0; i < n; i++ {
		if _, _, err := st.Record(store.Event{
			InstanceID: "bd-test01", TargetID: "worker-1", Type: "observation",
			Source: "fake", At: at.Add(time.Duration(i) * time.Second),
			SessionID: "old-session", Generation: 1, Status: policy.StatusIdle,
		}); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
}

func TestRetentionRunsOnceAtStartup(t *testing.T) {
	dir := shortDir(t)
	dbPath := filepath.Join(dir, "i.db")
	seedOldEvents(t, dbPath, time.Now().Add(-2*time.Hour), 3)

	cfg := testConfig()
	cfg.Retention = &config.Retention{MaxAge: time.Hour, HoldUncollected: time.Minute}

	// The ticker is set an hour out, so it cannot fire during this test. Only a
	// pass at startup can enforce the window here — without one this times out
	// rather than passing late, which is the whole point of the assertion.
	d, err := Start(Options{
		InstanceID:        "bd-test01",
		Config:            cfg,
		SocketPath:        filepath.Join(dir, "s.sock"),
		DBPath:            dbPath,
		LockPath:          filepath.Join(dir, "i.lock"),
		RegistryDir:       filepath.Join(dir, "registry"),
		Interval:          20 * time.Millisecond,
		RetentionInterval: time.Hour,
		Observers:         map[string]monitor.Observer{"fake": &spyObserver{status: policy.StatusActive, live: true}},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = d.Shutdown() })

	socket := filepath.Join(dir, "s.sock")
	waitFor(t, "the window to be enforced without waiting for the ticker", func() bool {
		err := ipc.Call(socket, "events", EventsParams{After: 0, Limit: 10}, &EventsResult{})
		var e *ipc.Error
		return errors.As(err, &e) && e.Code == ipc.CodeCursorStale
	})

	// The cursor status reports must still be followable, so the operator is not
	// left in the loop #39 and I1 both exist to prevent.
	var got StatusResult
	if err := ipc.Call(socket, "status", nil, &got); err != nil {
		t.Fatalf("status: %v", err)
	}
	if got.Cursor == 0 {
		t.Fatal("status cursor is 0 after a startup prune, which events would refuse")
	}
	if err := ipc.Call(socket, "events", EventsParams{After: got.Cursor, Limit: 10}, &EventsResult{}); err != nil {
		t.Errorf("events from the reported cursor: %v", err)
	}
}
