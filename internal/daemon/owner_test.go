package daemon

import (
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BrutalSystems/birddog/internal/instance"
	"github.com/BrutalSystems/birddog/internal/policy"
)

// An instance deliberately outlives the shell that started it — that is what
// makes monitoring survive an orchestrator's turn ending. The cost is that
// nothing reaps it when the orchestrator goes away for good, and strays
// accumulate silently.
//
// Naming an owner opts into being reaped: when the session that asked for the
// watch is gone, there is nobody left to read the answers.

func startOwned(t *testing.T, alive func() bool) (*Daemon, Options) {
	t.Helper()
	_, opts := start(t, &spyObserver{status: policy.StatusActive, live: true})
	_ = (&Daemon{}).Done

	owned := opts
	owned.InstanceID = "bd-owned01"
	owned.SocketPath = opts.SocketPath + ".owned"
	owned.LockPath = opts.LockPath + ".owned"
	owned.DBPath = opts.DBPath + ".owned"
	owned.Owner = &Owner{
		Description: "claude session abc",
		Alive:       alive,
	}

	d, err := Start(owned)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = d.Shutdown() })
	return d, owned
}

func TestAnOwnedInstanceKeepsRunningWhileItsOwnerIsAlive(t *testing.T) {
	d, _ := startOwned(t, func() bool { return true })

	time.Sleep(150 * time.Millisecond)
	if d.Done() {
		t.Error("the instance stopped while its owner was alive")
	}
}

func TestAnOwnedInstanceStopsWhenItsOwnerIsGone(t *testing.T) {
	var alive atomic.Bool
	alive.Store(true)
	d, opts := startOwned(t, func() bool { return alive.Load() })

	alive.Store(false)
	waitFor(t, "the instance to stop", func() bool { return d.Done() })
	// Done only reports that the observation loop ended; the socket, the lock
	// and the stopped-at stamp are given back after it. Wait for the release
	// before asserting on any of them.
	d.Wait()

	// It shuts down properly rather than simply dying: the lock and socket
	// are given back, so the id can be started again.
	if _, err := os.Stat(opts.SocketPath); !os.IsNotExist(err) {
		t.Error("socket left behind after an owner-triggered shutdown")
	}
	rec, ok, _ := instance.Load(opts.RegistryDir, opts.InstanceID)
	if !ok || rec.StoppedAt == nil {
		t.Error("the instance record was not marked stopped")
	}
}

// A session that is briefly unobservable — a socket that did not answer once —
// must not end the watch. Only a sustained absence should.
func TestABriefOwnerOutageDoesNotStopTheInstance(t *testing.T) {
	var misses atomic.Int32
	d, _ := startOwned(t, func() bool {
		// Absent for a single check, then back.
		return misses.Add(1) != 2
	})

	time.Sleep(250 * time.Millisecond)
	if d.Done() {
		t.Error("a single missed check ended the watch")
	}
}

// Without an owner the old behaviour stands: the instance outlives everything
// and waits to be stopped explicitly.
func TestAnUnownedInstanceIsNeverReaped(t *testing.T) {
	d, _ := start(t, &spyObserver{status: policy.StatusActive, live: true})

	time.Sleep(200 * time.Millisecond)
	if d.Done() {
		t.Error("an instance with no owner stopped by itself")
	}
}

// The reason has to reach status, or an orchestrator that comes back finds an
// instance gone with nothing saying why.
func TestTheOwnerIsVisibleInStatus(t *testing.T) {
	_, opts := startOwned(t, func() bool { return true })

	var got StatusResult
	if err := callStatus(opts.SocketPath, &got); err != nil {
		t.Fatalf("status: %v", err)
	}
	if got.Owner == "" {
		t.Error("status does not say who owns the instance")
	}
}
