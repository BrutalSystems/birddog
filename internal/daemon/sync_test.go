package daemon

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BrutalSystems/birddog/internal/config"
	"github.com/BrutalSystems/birddog/internal/ipc"
	"github.com/BrutalSystems/birddog/internal/monitor"
	"github.com/BrutalSystems/birddog/internal/platform/where"
	"github.com/BrutalSystems/birddog/internal/policy"
)

func startWith(t *testing.T, obs monitor.Observer, mod func(*Options)) (*Daemon, Options) {
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
	mod(&opts)
	d, err := Start(opts)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = d.Shutdown() })
	return d, opts
}

// ledger is a configuration file in the format an orchestrator writes.
func ledger(targets ...string) []byte {
	type tgt struct {
		ID         string            `json:"id"`
		Provider   string            `json:"provider"`
		Attachment config.Attachment `json:"attachment"`
		Policy     map[string]any    `json:"policy"`
	}
	cfg := map[string]any{"schema_version": 1, "name": "checkout-refactor"}
	var ts []tgt
	for _, id := range targets {
		ts = append(ts, tgt{
			ID: id, Provider: "fake",
			Attachment: config.Attachment{Kind: "existing-session", SessionID: "s"},
			Policy: map[string]any{
				"alert_on":            []string{config.ConditionInputRequested, config.ConditionExit, config.ConditionObservationLost},
				"quiet_after_seconds": 300, "idle_grace_seconds": 30,
			},
		})
	}
	cfg["targets"] = ts
	b, _ := json.Marshal(cfg)
	return b
}

func syncWith(t *testing.T, socket string, p WatchSyncParams) WatchSyncResult {
	t.Helper()
	var res WatchSyncResult
	if err := ipc.Call(socket, "watch.sync", p, &res); err != nil {
		t.Fatalf("watch.sync: %v", err)
	}
	return res
}

func TestSyncAddsRemovesAndLeavesUnchangedTargetsAlone(t *testing.T) {
	_, opts := start(t, &spyObserver{status: policy.StatusActive, live: true})
	waitFor(t, "worker-1 observed", func() bool {
		ts := targets(t, opts.SocketPath)
		return len(ts) == 1 && ts[0].Generation > 0
	})
	before := targets(t, opts.SocketPath)[0]

	res := syncWith(t, opts.SocketPath, WatchSyncParams{Config: ledger("worker-1", "worker-2")})
	if !res.Changed || len(res.Added) != 1 || res.Added[0] != "worker-2" ||
		len(res.Unchanged) != 1 || res.Unchanged[0] != "worker-1" || len(res.Updated) != 0 {
		t.Fatalf("sync = %+v, want worker-2 added and worker-1 unchanged", res)
	}
	if res.ConfigRevision == 0 {
		t.Error("ConfigRevision = 0, want the change to be identifiable")
	}

	after := targets(t, opts.SocketPath)
	if len(after) != 2 || after[0].ID != "worker-1" || after[0].Generation != before.Generation {
		t.Errorf("targets after sync = %+v, want worker-1 kept with its generation", after)
	}

	again := syncWith(t, opts.SocketPath, WatchSyncParams{Config: ledger("worker-1", "worker-2")})
	if again.Changed || again.ConfigRevision != res.ConfigRevision {
		t.Errorf("repeat sync = %+v, want nothing changed and the revision kept", again)
	}

	gone := syncWith(t, opts.SocketPath, WatchSyncParams{Config: ledger("worker-2")})
	if len(gone.Removed) != 1 || gone.Removed[0] != "worker-1" {
		t.Errorf("sync = %+v, want worker-1 removed", gone)
	}
	if ts := targets(t, opts.SocketPath); len(ts) != 1 || ts[0].ID != "worker-2" {
		t.Errorf("targets = %+v, want only worker-2", ts)
	}
}

func TestSyncUpdatesAChangedPolicy(t *testing.T) {
	_, opts := start(t, &spyObserver{status: policy.StatusActive, live: true})

	var cfg map[string]any
	_ = json.Unmarshal(ledger("worker-1"), &cfg)
	cfg["targets"].([]any)[0].(map[string]any)["policy"].(map[string]any)["quiet_after_seconds"] = 900
	data, _ := json.Marshal(cfg)

	res := syncWith(t, opts.SocketPath, WatchSyncParams{Config: data})
	if len(res.Updated) != 1 || res.Updated[0] != "worker-1" {
		t.Fatalf("sync = %+v, want worker-1 updated", res)
	}
	if got := targets(t, opts.SocketPath)[0].Policy.QuietAfterSeconds; got != 900 {
		t.Errorf("quiet_after_seconds = %d after sync, want 900", got)
	}
}

func TestSyncDryRunChangesNothing(t *testing.T) {
	_, opts := start(t, &spyObserver{status: policy.StatusActive, live: true})

	res := syncWith(t, opts.SocketPath, WatchSyncParams{Config: ledger("worker-2"), DryRun: true})
	if !res.DryRun || len(res.Added) != 1 || len(res.Removed) != 1 {
		t.Fatalf("dry run = %+v, want worker-2 added and worker-1 removed reported", res)
	}
	if ts := targets(t, opts.SocketPath); len(ts) != 1 || ts[0].ID != "worker-1" {
		t.Errorf("targets = %+v after a dry run, want them untouched", ts)
	}
}

// All or nothing: one bad target refuses the whole file.
func TestSyncRefusesAnInvalidFileEntirely(t *testing.T) {
	_, opts := start(t, &spyObserver{status: policy.StatusActive, live: true})

	bad := []byte(`{"schema_version": 1, "name": "x", "targets": [{"id": "w", "provider": "nonsense", "attachment": {"session_id": "s"}}]}`)
	err := ipc.Call(opts.SocketPath, "watch.sync", WatchSyncParams{Config: bad}, &WatchSyncResult{})
	var e *ipc.Error
	if !errors.As(err, &e) || e.Code != ipc.CodeBadRequest {
		t.Fatalf("error = %v, want bad_request", err)
	}
	if ts := targets(t, opts.SocketPath); len(ts) != 1 || ts[0].ID != "worker-1" {
		t.Errorf("targets = %+v, want the watch list untouched", ts)
	}
}

// An expected-quiet override set at runtime is what a ledger does not record.
// A sync from a file that does not mention one keeps it.
func TestSyncKeepsARuntimeExpectedQuietOverride(t *testing.T) {
	_, opts := start(t, &spyObserver{status: policy.StatusActive, live: true})

	until := time.Now().Add(20 * time.Minute).UTC().Format(time.RFC3339)
	if err := ipc.Call(opts.SocketPath, "watch.update", WatchUpdateParams{
		TargetID: "worker-1", ExpectedQuietUntil: &until, ExpectedQuietReason: "integration suite",
	}, &WatchResult{}); err != nil {
		t.Fatalf("watch.update: %v", err)
	}

	res := syncWith(t, opts.SocketPath, WatchSyncParams{Config: ledger("worker-1")})
	if res.Changed {
		t.Errorf("sync = %+v, want the override carried and nothing changed", res)
	}
	p := targets(t, opts.SocketPath)[0].Policy
	if p.ExpectedQuietUntil == nil || !p.ExpectedQuietActive || p.ExpectedQuietReason != "integration suite" {
		t.Errorf("policy = %+v, want the runtime override still in force", p)
	}
}

// With no configuration sent, the instance re-reads the file it was started
// with.
func TestSyncRereadsTheInstancesOwnFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.json")
	if err := os.WriteFile(path, ledger("worker-1"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, opts := startWith(t, &spyObserver{status: policy.StatusActive, live: true}, func(o *Options) { o.ConfigPath = path })

	if err := os.WriteFile(path, ledger("worker-1", "worker-3"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := syncWith(t, opts.SocketPath, WatchSyncParams{})
	if res.Source != path || len(res.Added) != 1 || res.Added[0] != "worker-3" {
		t.Fatalf("sync = %+v, want worker-3 added from %s", res, path)
	}
}

// follow_file: the instance notices its file changing and follows it, and a
// file that does not parse leaves the watch list as it was and says why.
func TestFollowFileTracksItsConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.json")
	write := func(b []byte) {
		t.Helper()
		var cfg map[string]any
		if err := json.Unmarshal(b, &cfg); err == nil {
			cfg["follow_file"] = true
			b, _ = json.Marshal(cfg)
		}
		if err := os.WriteFile(path, b, 0o600); err != nil {
			t.Fatal(err)
		}
		// A distinct modification time, so the change is visible to a
		// filesystem with coarse timestamps.
		later := time.Now().Add(time.Duration(len(b)) * time.Second)
		_ = os.Chtimes(path, later, later)
	}
	write(ledger("worker-1"))
	cfg, err := config.Parse(mustRead(t, path))
	if err != nil {
		t.Fatal(err)
	}
	_, opts := startWith(t, &spyObserver{status: policy.StatusActive, live: true}, func(o *Options) {
		o.ConfigPath = path
		o.Config = cfg
	})

	write(ledger("worker-1", "worker-2"))
	waitFor(t, "worker-2 to be followed from the file", func() bool { return len(targets(t, opts.SocketPath)) == 2 })

	write([]byte(`{"schema_version": 1, "name": "checkout-refactor", "targets": [`))
	waitFor(t, "the refused file to be reported", func() bool {
		var st StatusResult
		_ = callStatus(opts.SocketPath, &st)
		return st.ConfigSync != nil && st.ConfigSync.LastError != ""
	})
	var st StatusResult
	if err := callStatus(opts.SocketPath, &st); err != nil {
		t.Fatal(err)
	}
	if len(st.Targets) != 2 || !st.ConfigSync.FollowFile || st.ConfigSync.File != path {
		t.Errorf("status = %+v / %+v, want both targets kept while the file is broken", st.Targets, st.ConfigSync)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestStatusReportsEachTargetsPolicy(t *testing.T) {
	_, opts := start(t, &spyObserver{status: policy.StatusActive, live: true})

	p := targets(t, opts.SocketPath)[0].Policy
	if len(p.AlertOn) != 3 || p.QuietAfterSeconds != 300 || p.IdleGraceSeconds != 30 ||
		p.NoProgressAfterSeconds != 0 || p.ExpectedQuietUntil != nil || p.ExpectedQuietActive {
		t.Errorf("policy = %+v, want the configured one", p)
	}

	quiet := 120
	if err := ipc.Call(opts.SocketPath, "watch.update", WatchUpdateParams{TargetID: "worker-1", QuietAfterSeconds: &quiet}, &WatchResult{}); err != nil {
		t.Fatalf("watch.update: %v", err)
	}
	if got := targets(t, opts.SocketPath)[0].Policy.QuietAfterSeconds; got != 120 {
		t.Errorf("quiet_after_seconds = %d after update, want 120: status must show what is in force", got)
	}
}

// watch add sends the file format, which is the only form whose thresholds
// survive the trip.
func TestWatchAddSpecKeepsThresholdsAndDefaults(t *testing.T) {
	_, opts := start(t, &spyObserver{status: policy.StatusActive, live: true})

	spec := []byte(`{"id": "worker-2", "provider": "fake", "attachment": {"kind": "existing-session", "session_id": "s2"},
		"policy": {"quiet_after_seconds": 600, "no_progress_after_seconds": 3600}}`)
	if err := ipc.Call(opts.SocketPath, "watch.add", WatchAddParams{Spec: spec}, &WatchResult{}); err != nil {
		t.Fatalf("watch.add: %v", err)
	}
	p := targets(t, opts.SocketPath)[1].Policy
	if p.QuietAfterSeconds != 600 || p.NoProgressAfterSeconds != 3600 || p.IdleGraceSeconds != 30 || len(p.AlertOn) != 4 {
		t.Errorf("policy = %+v, want the file's thresholds and the default alert list", p)
	}
}

// An earlier client sends the in-memory form, whose durations never cross the
// wire. What it could not send gets the defaults, not zero.
func TestWatchAddLegacyTargetGetsDefaultsNotZero(t *testing.T) {
	_, opts := start(t, &spyObserver{status: policy.StatusActive, live: true})

	legacy := config.Target{ID: "worker-2", Provider: "fake", Attachment: config.Attachment{SessionID: "s2"}}
	if err := ipc.Call(opts.SocketPath, "watch.add", WatchAddParams{Target: legacy}, &WatchResult{}); err != nil {
		t.Fatalf("watch.add: %v", err)
	}
	p := targets(t, opts.SocketPath)[1].Policy
	if p.QuietAfterSeconds != int(config.DefaultQuietAfter/time.Second) || len(p.AlertOn) == 0 {
		t.Errorf("policy = %+v, want default quiet threshold and alert list", p)
	}
}

// pidObserver reports a live session with a process behind it.
type pidObserver struct{ live bool }

func (p *pidObserver) Observe(config.Target) (monitor.Sighting, error) {
	now := time.Now()
	return monitor.Sighting{
		Observation: policy.Observation{
			Live: p.live, Status: policy.StatusIdle, StatusKnown: p.live,
			StatusSince: now, LastActivityAt: now, ObservedAt: now,
		},
		SessionIdentity: "s@P", PID: 4242, ProcStart: "P",
	}, nil
}

func TestStatusSaysWhereALiveSessionLives(t *testing.T) {
	located := where.Location{TTY: "/dev/ttys005", Tmux: &where.Tmux{
		Socket: "/private/tmp/tmux-501/muster", SocketName: "muster",
		Session: "muster-1", Window: "0", Pane: "%38", Target: "muster-1:0.0",
	}}
	var asked []int
	_, opts := startWith(t, &pidObserver{live: true}, func(o *Options) {
		o.Locate = func(pids []int) (map[int]where.Location, error) {
			asked = append(asked, pids...)
			return map[int]where.Location{4242: located}, nil
		}
	})
	waitFor(t, "the process to be seen", func() bool { return targets(t, opts.SocketPath)[0].PID == 4242 })

	ts := targets(t, opts.SocketPath)[0]
	if ts.Location == nil || ts.Location.Tmux == nil || ts.Location.Tmux.Pane != "%38" {
		t.Errorf("location = %+v, want the muster pane", ts.Location)
	}
	if ts.AttachedSessionID != "s" {
		t.Errorf("attached_session_id = %q, want s", ts.AttachedSessionID)
	}
}

// A session that has gone lives nowhere, and is not looked for.
func TestStatusDoesNotLocateASessionThatIsNotSeen(t *testing.T) {
	_, opts := startWith(t, &pidObserver{live: false}, func(o *Options) {
		o.Locate = func([]int) (map[int]where.Location, error) {
			return map[int]where.Location{4242: {TTY: "/dev/ttys009"}}, nil
		}
	})
	waitFor(t, "a pass", func() bool { return targets(t, opts.SocketPath)[0].PID == 4242 })
	if loc := targets(t, opts.SocketPath)[0].Location; loc != nil {
		t.Errorf("location = %+v for a session that is not live", loc)
	}
}

// A sync sent with the request says nothing about the instance's own file.
func TestASyncSentWithTheRequestIsNotReportedAsTheFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.json")
	if err := os.WriteFile(path, ledger("worker-1"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, opts := startWith(t, &spyObserver{status: policy.StatusActive, live: true}, func(o *Options) { o.ConfigPath = path })

	syncWith(t, opts.SocketPath, WatchSyncParams{Config: ledger("worker-1", "worker-2")})
	var st StatusResult
	if err := callStatus(opts.SocketPath, &st); err != nil {
		t.Fatal(err)
	}
	if st.ConfigSync == nil || st.ConfigSync.LastSyncedAt != nil {
		t.Errorf("config_sync = %+v, want no sync from the file recorded", st.ConfigSync)
	}

	// A re-read of the file that fails is recorded against the file.
	if err := os.WriteFile(path, []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = ipc.Call(opts.SocketPath, "watch.sync", WatchSyncParams{}, &WatchSyncResult{})
	if err := callStatus(opts.SocketPath, &st); err != nil {
		t.Fatal(err)
	}
	if st.ConfigSync.LastError == "" {
		t.Error("last_error empty after the file was refused")
	}
}
