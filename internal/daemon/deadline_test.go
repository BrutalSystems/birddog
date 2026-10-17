package daemon

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/BrutalSystems/birddog/internal/config"
	"github.com/BrutalSystems/birddog/internal/ipc"
	"github.com/BrutalSystems/birddog/internal/policy"
)

// overdueAlerts is testConfig's alert list with overdue added, which a
// deadline needs before it can be set.
var overdueAlerts = []string{config.ConditionInputRequested, config.ConditionExit, config.ConditionObservationLost, config.ConditionOverdue}

func updateDeadline(t *testing.T, socket, deadline string) {
	t.Helper()
	if err := ipc.Call(socket, "watch.update", WatchUpdateParams{
		TargetID: "worker-1", AlertOn: overdueAlerts, Deadline: &deadline,
	}, &WatchResult{}); err != nil {
		t.Fatalf("watch.update deadline=%q: %v", deadline, err)
	}
}

func hasIncident(ts TargetStatus, condition string) bool {
	for _, inc := range ts.Incidents {
		if inc.Condition == condition {
			return true
		}
	}
	return false
}

// A live target past its deadline raises overdue; moving the deadline ahead
// resolves it, and clearing it removes the deadline outright.
func TestWatchUpdateSetsMovesAndClearsADeadline(t *testing.T) {
	_, opts := start(t, &spyObserver{status: policy.StatusActive, live: true})

	updateDeadline(t, opts.SocketPath, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339))
	waitFor(t, "overdue to open", func() bool {
		return hasIncident(targets(t, opts.SocketPath)[0], config.ConditionOverdue)
	})
	if p := targets(t, opts.SocketPath)[0].Policy; p.Deadline == nil || !p.DeadlinePassed {
		t.Errorf("policy = %+v, want the deadline shown as passed", p)
	}

	later := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	updateDeadline(t, opts.SocketPath, later.Format(time.RFC3339))
	waitFor(t, "overdue to resolve once the deadline moved ahead", func() bool {
		return !hasIncident(targets(t, opts.SocketPath)[0], config.ConditionOverdue)
	})
	if p := targets(t, opts.SocketPath)[0].Policy; p.Deadline == nil || !p.Deadline.Equal(later) || p.DeadlinePassed {
		t.Errorf("policy = %+v, want the moved deadline, not passed", p)
	}

	updateDeadline(t, opts.SocketPath, "")
	if p := targets(t, opts.SocketPath)[0].Policy; p.Deadline != nil {
		t.Errorf("policy = %+v, want no deadline after clearing", p)
	}
}

func TestWatchUpdateRefusesADeadlineThatCouldNotAlert(t *testing.T) {
	_, opts := start(t, &spyObserver{status: policy.StatusActive, live: true})
	at := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	err := ipc.Call(opts.SocketPath, "watch.update", WatchUpdateParams{TargetID: "worker-1", Deadline: &at}, &WatchResult{})
	if err == nil {
		t.Fatal("watch.update set a deadline on a target whose alert_on leaves out overdue")
	}
	if p := targets(t, opts.SocketPath)[0].Policy; p.Deadline != nil {
		t.Errorf("policy = %+v, want the refused deadline not applied", p)
	}
}

// A relative deadline runs from when the target was added, and re-syncing the
// same file does not push it back.
func TestARelativeDeadlineRunsFromTheAddAndSurvivesASync(t *testing.T) {
	_, opts := start(t, &spyObserver{status: policy.StatusActive, live: true})

	cfg := map[string]any{"schema_version": 1, "name": "checkout-refactor", "targets": []any{
		map[string]any{
			"id": "worker-1", "provider": "fake",
			"attachment": map[string]any{"kind": "existing-session", "session_id": "s"},
			"policy": map[string]any{
				"alert_on":            []string{config.ConditionInputRequested, config.ConditionExit, config.ConditionObservationLost},
				"quiet_after_seconds": 300, "idle_grace_seconds": 30,
			},
		},
		map[string]any{
			"id": "worker-2", "provider": "fake",
			"attachment": map[string]any{"kind": "existing-session", "session_id": "s2"},
			"policy":     map[string]any{"deadline_after_seconds": 2700},
		},
	}}
	data, _ := json.Marshal(cfg)

	before := time.Now()
	syncWith(t, opts.SocketPath, WatchSyncParams{Config: data})
	first := targets(t, opts.SocketPath)[1].Policy.Deadline
	if first == nil || first.Before(before.Add(45*time.Minute).Truncate(time.Second)) || first.After(time.Now().Add(45*time.Minute)) {
		t.Fatalf("deadline = %v, want 45m from the add", first)
	}

	time.Sleep(1100 * time.Millisecond)
	if res := syncWith(t, opts.SocketPath, WatchSyncParams{Config: data}); res.Changed {
		t.Errorf("sync = %+v, want the same file to change nothing", res)
	}
	if again := targets(t, opts.SocketPath)[1].Policy.Deadline; again == nil || !again.Equal(*first) {
		t.Errorf("deadline = %v after re-sync, want it kept at %v", again, first)
	}
}

// A deadline moved by watch update survives a sync from a file that still says
// what it said, as an expected-quiet override does.
func TestSyncKeepsADeadlineSetByUpdate(t *testing.T) {
	_, opts := start(t, &spyObserver{status: policy.StatusActive, live: true})
	at := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	updateDeadline(t, opts.SocketPath, at.Format(time.RFC3339))

	// The file's alert list must include overdue for the deadline to stand.
	var cfg map[string]any
	_ = json.Unmarshal(ledger("worker-1"), &cfg)
	cfg["targets"].([]any)[0].(map[string]any)["policy"].(map[string]any)["alert_on"] = overdueAlerts
	data, _ := json.Marshal(cfg)
	syncWith(t, opts.SocketPath, WatchSyncParams{Config: data})

	if p := targets(t, opts.SocketPath)[0].Policy; p.Deadline == nil || !p.Deadline.Equal(at) {
		t.Errorf("policy = %+v, want the updated deadline kept across the sync", p)
	}
}
