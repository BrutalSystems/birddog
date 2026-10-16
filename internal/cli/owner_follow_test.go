package cli

import (
	"testing"

	"github.com/BrutalSystems/birddog/internal/discover/claude"
)

// An owner that runs /clear keeps its process and gets a new session id. Its
// instance must not reap itself for that.
func TestOwnerIsFollowedAcrossANewSessionID(t *testing.T) {
	o := &ownerTrack{sessionID: "s1"}
	if !o.alive([]claude.Session{{SessionID: "s1", PID: 7, ProcStart: "P", Live: true}}) {
		t.Fatal("owner present and live reported gone")
	}
	if !o.alive([]claude.Session{{SessionID: "s2", PID: 7, ProcStart: "P", Live: true}}) {
		t.Fatal("owner cleared in the same process reported gone")
	}
	if o.sessionID != "s2" {
		t.Errorf("tracked session = %q, want s2", o.sessionID)
	}
}

func TestOwnerIsNotFollowedOntoAnotherProcess(t *testing.T) {
	o := &ownerTrack{sessionID: "s1"}
	o.alive([]claude.Session{{SessionID: "s1", PID: 7, ProcStart: "P", Live: true}})
	if o.alive([]claude.Session{{SessionID: "s2", PID: 7, ProcStart: "Q", Live: true}}) {
		t.Fatal("a reused pid kept the owner alive")
	}
	fresh := &ownerTrack{sessionID: "s1"}
	if fresh.alive([]claude.Session{{SessionID: "s2", PID: 7, ProcStart: "P", Live: true}}) {
		t.Fatal("followed with no process identity ever observed")
	}
}
