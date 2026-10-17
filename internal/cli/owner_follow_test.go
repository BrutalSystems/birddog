package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/BrutalSystems/birddog/internal/discover/claude"
)

// An owner that runs /clear keeps its process and gets a new session id. Its
// instance must not reap itself for that.
func TestOwnerIsFollowedAcrossANewSessionID(t *testing.T) {
	o := &ownerTrack{claude.Tracker{SessionID: "s1"}}
	if !o.alive([]claude.Session{{SessionID: "s1", PID: 7, ProcStart: "P", Live: true}}, false) {
		t.Fatal("owner present and live reported gone")
	}
	if !o.alive([]claude.Session{{SessionID: "s2", PID: 7, ProcStart: "P", Live: true}}, false) {
		t.Fatal("owner cleared in the same process reported gone")
	}
	if o.SessionID != "s2" {
		t.Errorf("tracked session = %q, want s2", o.SessionID)
	}
}

func TestOwnerIsNotFollowedOntoAnotherProcess(t *testing.T) {
	o := &ownerTrack{claude.Tracker{SessionID: "s1"}}
	o.alive([]claude.Session{{SessionID: "s1", PID: 7, ProcStart: "P", Live: true}}, false)
	if o.alive([]claude.Session{{SessionID: "s2", PID: 7, ProcStart: "Q", Live: true}}, false) {
		t.Fatal("a reused pid kept the owner alive")
	}
	fresh := &ownerTrack{claude.Tracker{SessionID: "s1"}}
	if fresh.alive([]claude.Session{{SessionID: "s2", PID: 7, ProcStart: "P", Live: true}}, false) {
		t.Fatal("followed with no process identity ever observed")
	}
}

// A listing missing a registry that could not be read says nothing about a
// session that may be in it. Three such passes must not reap the instance.
func TestOwnerMissingFromAPartialListingIsNotGone(t *testing.T) {
	o := &ownerTrack{claude.Tracker{SessionID: "s1"}}
	if !o.alive(nil, true) {
		t.Error("owner reported gone from a partial listing")
	}
	if o.alive(nil, false) {
		t.Error("owner reported present when a complete listing lacks it")
	}
}

// An owner whose registry record is reached through a link that no longer
// resolves, for a pid that is running, may be there. Its instance must not be
// reaped for it. Exercised through the owner check itself, with real process
// liveness: the pid is this test's own.
func TestAnOwnerBehindADanglingLinkIsNotReaped(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, fmt.Sprintf("%d.json", os.Getpid()))
	if err := os.Symlink(filepath.Join(dir, "moved", "record.json"), link); err != nil {
		t.Fatal(err)
	}
	owner := ownerWatchIn("owner-session", claude.Params{RegistryDir: dir})
	for i := 0; i < 3; i++ {
		if !owner.Alive() {
			t.Fatalf("pass %d: owner counted absent from an incomplete listing", i+1)
		}
	}

	// With the link gone, the owner is genuinely not there.
	_ = os.Remove(link)
	if owner.Alive() {
		t.Error("owner counted present with nothing in the registry")
	}
}
