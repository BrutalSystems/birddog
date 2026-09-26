package daemon

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/BrutalSystems/birddog/internal/config"
	"github.com/BrutalSystems/birddog/internal/ipc"
	"github.com/BrutalSystems/birddog/internal/monitor"
	"github.com/BrutalSystems/birddog/internal/policy"
)

func newTarget(id string) config.Target {
	return config.Target{
		ID: id, Provider: "fake",
		Attachment: config.Attachment{Kind: "existing-session", SessionID: "/tmp/x.json"},
		Policy: config.Policy{
			AlertOn:    []string{config.ConditionExit},
			QuietAfter: 5 * time.Minute,
			IdleGrace:  30 * time.Second,
		},
	}
}

func targets(t *testing.T, socket string) []TargetStatus {
	t.Helper()
	var st StatusResult
	if err := ipc.Call(socket, "status", nil, &st); err != nil {
		t.Fatalf("status: %v", err)
	}
	return st.Targets
}

func TestWatchAddStartsObservingANewTarget(t *testing.T) {
	_, opts := start(t, &spyObserver{status: policy.StatusActive, live: true})

	var res WatchResult
	if err := ipc.Call(opts.SocketPath, "watch.add", WatchAddParams{Target: newTarget("worker-2")}, &res); err != nil {
		t.Fatalf("watch.add: %v", err)
	}
	if res.ConfigRevision == 0 {
		t.Error("ConfigRevision = 0, want the change to be identifiable")
	}

	got := targets(t, opts.SocketPath)
	if len(got) != 2 {
		t.Fatalf("got %d targets, want 2", len(got))
	}
}

func TestWatchAddRefusesADuplicateID(t *testing.T) {
	_, opts := start(t, &spyObserver{status: policy.StatusActive, live: true})

	err := ipc.Call(opts.SocketPath, "watch.add", WatchAddParams{Target: newTarget("worker-1")}, &WatchResult{})
	if err == nil {
		t.Fatal("watch.add = nil error for a duplicate target id")
	}
	var e *ipc.Error
	if !errors.As(err, &e) || e.Code != ipc.CodeBadRequest {
		t.Errorf("error = %v, want a bad_request", err)
	}
}

// A target that would not survive validation must not be half-applied.
func TestWatchAddRejectsAnInvalidTarget(t *testing.T) {
	_, opts := start(t, &spyObserver{status: policy.StatusActive, live: true})

	bad := newTarget("worker-2")
	bad.Provider = "nonsense"
	if err := ipc.Call(opts.SocketPath, "watch.add", WatchAddParams{Target: bad}, &WatchResult{}); err == nil {
		t.Fatal("watch.add = nil error for an unknown provider")
	}
	if got := targets(t, opts.SocketPath); len(got) != 1 {
		t.Errorf("got %d targets, want the invalid one rejected entirely", len(got))
	}
}

func TestWatchRemoveStopsObservingATarget(t *testing.T) {
	_, opts := start(t, &spyObserver{status: policy.StatusActive, live: true})

	if err := ipc.Call(opts.SocketPath, "watch.remove", WatchRemoveParams{TargetID: "worker-1"}, &WatchResult{}); err != nil {
		t.Fatalf("watch.remove: %v", err)
	}
	if got := targets(t, opts.SocketPath); len(got) != 0 {
		t.Errorf("got %d targets, want 0", len(got))
	}
}

func TestWatchRemoveReportsAnUnknownTarget(t *testing.T) {
	_, opts := start(t, &spyObserver{status: policy.StatusActive, live: true})

	err := ipc.Call(opts.SocketPath, "watch.remove", WatchRemoveParams{TargetID: "never-existed"}, &WatchResult{})
	var e *ipc.Error
	if !errors.As(err, &e) || e.Code != ipc.CodeNotFound {
		t.Errorf("error = %v, want a not_found", err)
	}
}

// The Definition of Done's own example: "expect this worker to be quiet for
// twenty minutes while its tests run".
func TestWatchUpdateAppliesAnExpectedQuietOverride(t *testing.T) {
	_, opts := start(t, &spyObserver{status: policy.StatusActive, live: true})

	until := time.Now().Add(20 * time.Minute).UTC().Format(time.RFC3339)
	params := WatchUpdateParams{
		TargetID:            "worker-1",
		ExpectedQuietUntil:  &until,
		ExpectedQuietReason: "running the integration suite",
	}
	var res WatchResult
	if err := ipc.Call(opts.SocketPath, "watch.update", params, &res); err != nil {
		t.Fatalf("watch.update: %v", err)
	}
	if res.ConfigRevision == 0 {
		t.Error("ConfigRevision = 0, want the change identifiable")
	}

	got := targets(t, opts.SocketPath)
	if got[0].ExpectedQuietUntil == nil {
		t.Fatal("the override was not applied")
	}
	if got[0].ExpectedQuietReason != "running the integration suite" {
		t.Errorf("reason = %q", got[0].ExpectedQuietReason)
	}
}

// Thresholds are configuration, and changing them must not rewrite history.
func TestWatchUpdatePreservesTheEventHistory(t *testing.T) {
	_, opts := start(t, &spyObserver{status: policy.StatusActive, live: true})

	var before EventsResult
	waitFor(t, "events", func() bool {
		_ = ipc.Call(opts.SocketPath, "events", EventsParams{After: 0, Limit: 100}, &before)
		return len(before.Events) > 0
	})

	seconds := 600
	if err := ipc.Call(opts.SocketPath, "watch.update",
		WatchUpdateParams{TargetID: "worker-1", QuietAfterSeconds: &seconds}, &WatchResult{}); err != nil {
		t.Fatalf("watch.update: %v", err)
	}

	var after EventsResult
	if err := ipc.Call(opts.SocketPath, "events", EventsParams{After: 0, Limit: 100}, &after); err != nil {
		t.Fatalf("events: %v", err)
	}
	if len(after.Events) < len(before.Events) {
		t.Errorf("events went from %d to %d — a configuration change lost history",
			len(before.Events), len(after.Events))
	}
}

// Every mutation advances the revision, so a delayed observation can be told
// apart from one made under the current rules.
func TestEachMutationAdvancesTheConfigRevision(t *testing.T) {
	_, opts := start(t, &spyObserver{status: policy.StatusActive, live: true})

	var first, second WatchResult
	if err := ipc.Call(opts.SocketPath, "watch.add", WatchAddParams{Target: newTarget("worker-2")}, &first); err != nil {
		t.Fatalf("watch.add: %v", err)
	}
	if err := ipc.Call(opts.SocketPath, "watch.add", WatchAddParams{Target: newTarget("worker-3")}, &second); err != nil {
		t.Fatalf("watch.add: %v", err)
	}
	if second.ConfigRevision <= first.ConfigRevision {
		t.Errorf("revision did not advance: %d then %d", first.ConfigRevision, second.ConfigRevision)
	}
}

func TestWatchUpdateReportsAnUnknownTarget(t *testing.T) {
	_, opts := start(t, &spyObserver{status: policy.StatusActive, live: true})

	seconds := 60
	err := ipc.Call(opts.SocketPath, "watch.update",
		WatchUpdateParams{TargetID: "never-existed", QuietAfterSeconds: &seconds}, &WatchResult{})
	var e *ipc.Error
	if !errors.As(err, &e) || e.Code != ipc.CodeNotFound {
		t.Errorf("error = %v, want a not_found", err)
	}
}

// Clearing an override is how an operator says the wait is over early.
func TestWatchUpdateCanClearAnOverride(t *testing.T) {
	_, opts := start(t, &spyObserver{status: policy.StatusActive, live: true})

	until := time.Now().Add(20 * time.Minute).UTC().Format(time.RFC3339)
	_ = ipc.Call(opts.SocketPath, "watch.update",
		WatchUpdateParams{TargetID: "worker-1", ExpectedQuietUntil: &until}, &WatchResult{})

	empty := ""
	if err := ipc.Call(opts.SocketPath, "watch.update",
		WatchUpdateParams{TargetID: "worker-1", ExpectedQuietUntil: &empty}, &WatchResult{}); err != nil {
		t.Fatalf("watch.update: %v", err)
	}
	if got := targets(t, opts.SocketPath); got[0].ExpectedQuietUntil != nil {
		t.Error("the override was not cleared")
	}
}

// A caller that did not hear the answer will retry. The retry must return the
// original result, not add the watch a second time.
func TestRepeatingAMutationWithTheSameKeyAppliesItOnce(t *testing.T) {
	_, opts := start(t, &spyObserver{status: policy.StatusActive, live: true})

	params := WatchAddParams{Target: newTarget("worker-2"), IdempotencyKey: "abc-123"}

	var first, second WatchResult
	if err := ipc.Call(opts.SocketPath, "watch.add", params, &first); err != nil {
		t.Fatalf("first watch.add: %v", err)
	}
	if err := ipc.Call(opts.SocketPath, "watch.add", params, &second); err != nil {
		t.Fatalf("retry: %v, want the original result rather than a duplicate error", err)
	}

	if second.ConfigRevision != first.ConfigRevision {
		t.Errorf("revision changed on retry: %d then %d", first.ConfigRevision, second.ConfigRevision)
	}
	if got := targets(t, opts.SocketPath); len(got) != 2 {
		t.Errorf("got %d targets, want the add applied exactly once", len(got))
	}
}

func TestDifferentKeysApplySeparately(t *testing.T) {
	_, opts := start(t, &spyObserver{status: policy.StatusActive, live: true})

	if err := ipc.Call(opts.SocketPath, "watch.add",
		WatchAddParams{Target: newTarget("worker-2"), IdempotencyKey: "k1"}, &WatchResult{}); err != nil {
		t.Fatalf("watch.add: %v", err)
	}
	if err := ipc.Call(opts.SocketPath, "watch.add",
		WatchAddParams{Target: newTarget("worker-3"), IdempotencyKey: "k2"}, &WatchResult{}); err != nil {
		t.Fatalf("watch.add: %v", err)
	}
	if got := targets(t, opts.SocketPath); len(got) != 3 {
		t.Errorf("got %d targets, want 3", len(got))
	}
}

// Acknowledgement is a mutation too, and an orchestrator may well retry it.
func TestRepeatedAckWithTheSameKeyIsAppliedOnce(t *testing.T) {
	_, opts := start(t, &spyObserver{status: policy.StatusWaitingInput, live: true})

	var st StatusResult
	waitFor(t, "an incident", func() bool {
		_ = ipc.Call(opts.SocketPath, "status", nil, &st)
		return len(st.Targets) > 0 && len(st.Targets[0].Incidents) > 0
	})
	id := st.Targets[0].Incidents[0].ID

	params := AckParams{IncidentID: id, IdempotencyKey: "ack-1"}
	var first, second AckResult
	if err := ipc.Call(opts.SocketPath, "ack", params, &first); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if err := ipc.Call(opts.SocketPath, "ack", params, &second); err != nil {
		t.Fatalf("ack retry: %v", err)
	}
	if second.IncidentID != first.IncidentID || !second.Acknowledged {
		t.Errorf("retry returned %+v, want the original result", second)
	}
}

// Without a key every call is applied, which is what a caller who sent none
// asked for.
func TestMutationsWithoutAKeyAreAlwaysApplied(t *testing.T) {
	_, opts := start(t, &spyObserver{status: policy.StatusActive, live: true})

	var first, second WatchResult
	_ = ipc.Call(opts.SocketPath, "watch.add", WatchAddParams{Target: newTarget("worker-2")}, &first)
	_ = ipc.Call(opts.SocketPath, "watch.add", WatchAddParams{Target: newTarget("worker-3")}, &second)

	if second.ConfigRevision <= first.ConfigRevision {
		t.Errorf("revision did not advance without keys: %d then %d", first.ConfigRevision, second.ConfigRevision)
	}
}

// Retrying a mutation whose reply was lost must not apply it twice. The
// returned result is only half of that: a retry that re-ran the write and
// happened to produce the same shape would pass a check on the reply alone.
// What must hold is that nothing moved — so the acknowledgement time is read
// before and after, across a clock that has advanced in between.
//
// Ordinary over a link, where a caller retries because it did not hear an
// answer rather than because anything went wrong.
func TestARetriedAckDoesNotMoveTheAcknowledgementTime(t *testing.T) {
	// The observation loop reads this clock on its own goroutine while the
	// test advances it, so it is guarded. An unguarded variable here is a
	// genuine data race, not a test artefact.
	var clockMu sync.Mutex
	clock := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	now := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return clock
	}
	advance := func(d time.Duration) {
		clockMu.Lock()
		defer clockMu.Unlock()
		clock = clock.Add(d)
	}

	dir := shortDir(t)
	opts := Options{
		InstanceID:  "bd-test02",
		Config:      testConfig(),
		SocketPath:  filepath.Join(dir, "s.sock"),
		DBPath:      filepath.Join(dir, "i.db"),
		LockPath:    filepath.Join(dir, "i.lock"),
		RegistryDir: filepath.Join(dir, "registry"),
		Interval:    20 * time.Millisecond,
		Observers:   map[string]monitor.Observer{"fake": &spyObserver{status: policy.StatusWaitingInput, live: true}},
		Now:         now,
	}
	d, err := Start(opts)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = d.Shutdown() })

	var st StatusResult
	waitFor(t, "an incident", func() bool {
		_ = ipc.Call(opts.SocketPath, "status", nil, &st)
		return len(st.Targets) > 0 && len(st.Targets[0].Incidents) > 0
	})
	id := st.Targets[0].Incidents[0].ID

	params := AckParams{IncidentID: id, IdempotencyKey: "ack-across-a-lost-reply"}
	var first, second AckResult
	if err := ipc.Call(opts.SocketPath, "ack", params, &first); err != nil {
		t.Fatalf("ack: %v", err)
	}
	acknowledgedAt := incidentAckTime(t, opts.SocketPath, id)

	// The reply was lost; the caller retries. Time has moved on, so a second
	// application would be visible.
	advance(5 * time.Second)
	if err := ipc.Call(opts.SocketPath, "ack", params, &second); err != nil {
		t.Fatalf("ack retry: %v", err)
	}

	if got := incidentAckTime(t, opts.SocketPath, id); !got.Equal(acknowledgedAt) {
		t.Errorf("acknowledged at %s after the retry, was %s: the mutation was applied twice", got, acknowledgedAt)
	}
	if second != first {
		t.Errorf("retry returned %+v, want exactly what the first call produced: %+v", second, first)
	}
}

func incidentAckTime(t *testing.T, socket string, id int64) time.Time {
	t.Helper()
	var st StatusResult
	if err := ipc.Call(socket, "status", nil, &st); err != nil {
		t.Fatalf("status: %v", err)
	}
	for _, target := range st.Targets {
		for _, inc := range target.Incidents {
			if inc.ID == id {
				if inc.AcknowledgedAt == nil {
					t.Fatalf("incident %d is not acknowledged", id)
				}
				return *inc.AcknowledgedAt
			}
		}
	}
	t.Fatalf("incident %d is no longer reported", id)
	return time.Time{}
}

// A cursor carries a position; it does not carry the history that position
// belongs to. If the store behind an instance is replaced — birddog
// reinstalled, its state directory cleared — a cursor from the old one passes
// the staleness check, matches nothing, and comes back as an empty page. The
// consumer is told there is nothing new, which is indistinguishable from a
// watched session that has gone quiet.
func TestACursorFromAReplacedStoreIsRefusedRatherThanAnswered(t *testing.T) {
	_, opts := start(t, &spyObserver{status: policy.StatusActive, live: true})

	var st StatusResult
	waitFor(t, "a store identity", func() bool {
		_ = ipc.Call(opts.SocketPath, "status", nil, &st)
		return st.Store != ""
	})

	// A cursor the consumer kept from a store that no longer exists.
	var result EventsResult
	err := ipc.Call(opts.SocketPath, "events", EventsParams{
		After: st.Cursor, Store: "0123456789abcdef0123456789abcdef",
	}, &result)

	if err == nil {
		t.Fatalf("a cursor from a replaced store was answered with %d events", len(result.Events))
	}
	var ipcErr *ipc.Error
	if !errors.As(err, &ipcErr) || ipcErr.Code != ipc.CodeStoreReplaced {
		t.Errorf("err = %v, want %s", err, ipc.CodeStoreReplaced)
	}
}

// The identity this instance reports is the one it accepts, and a caller that
// carries none is answered as before.
func TestTheReportedStoreIdentityIsAccepted(t *testing.T) {
	_, opts := start(t, &spyObserver{status: policy.StatusActive, live: true})

	var st StatusResult
	waitFor(t, "a store identity", func() bool {
		_ = ipc.Call(opts.SocketPath, "status", nil, &st)
		return st.Store != ""
	})

	var matching, absent EventsResult
	if err := ipc.Call(opts.SocketPath, "events", EventsParams{After: 0, Store: st.Store}, &matching); err != nil {
		t.Errorf("the instance refused the identity it reported: %v", err)
	}
	if matching.Store != st.Store {
		t.Errorf("events reported store %q, status reported %q", matching.Store, st.Store)
	}
	if err := ipc.Call(opts.SocketPath, "events", EventsParams{After: 0}, &absent); err != nil {
		t.Errorf("a caller carrying no identity was refused: %v", err)
	}
}
