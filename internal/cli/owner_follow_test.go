package cli

import (
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
