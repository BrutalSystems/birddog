package cli

import (
	"testing"
	"time"

	"github.com/BrutalSystems/birddog/internal/instance"
)

func rec(id string, stopped bool, running bool) instance.Record {
	r := instance.Record{ID: id, Name: "n", PID: 1, StartedAt: time.Now().UTC(), Running: running}
	if stopped {
		at := time.Now().UTC()
		r.StoppedAt = &at
	}
	return r
}

// An instance that is still running is never touched: prune tidies what is
// over, and deciding on its own that a live watch is unwanted would be the
// one thing it must not do.
func TestPruneLeavesRunningInstancesAlone(t *testing.T) {
	plan := planPrune([]instance.Record{rec("bd-live", false, true)})

	if len(plan.Remove) != 0 {
		t.Errorf("Remove = %v, want a running instance left alone", plan.Remove)
	}
}

func TestPruneRemovesTheRecordOfAStoppedInstance(t *testing.T) {
	plan := planPrune([]instance.Record{rec("bd-stopped", true, false)})

	if len(plan.Remove) != 1 || plan.Remove[0].ID != "bd-stopped" {
		t.Errorf("Remove = %+v, want the stopped instance", plan.Remove)
	}
}

// A daemon that crashed leaves a record saying it is running, with a pid that
// is gone. That is exactly the stray this command exists for.
func TestPruneRemovesARecordWhoseProcessDied(t *testing.T) {
	crashed := rec("bd-crashed", false, false) // never marked stopped, not running
	plan := planPrune([]instance.Record{crashed})

	if len(plan.Remove) != 1 {
		t.Fatalf("Remove = %+v, want the crashed instance", plan.Remove)
	}
	if plan.Remove[0].Reason == "" {
		t.Error("no reason recorded; an operator should be told why it went")
	}
}

func TestPruneDistinguishesTheTwoReasons(t *testing.T) {
	plan := planPrune([]instance.Record{
		rec("bd-stopped", true, false),
		rec("bd-crashed", false, false),
	})
	if len(plan.Remove) != 2 {
		t.Fatalf("Remove = %+v", plan.Remove)
	}
	reasons := map[string]string{}
	for _, r := range plan.Remove {
		reasons[r.ID] = r.Reason
	}
	if reasons["bd-stopped"] == reasons["bd-crashed"] {
		t.Errorf("both reported as %q; a clean stop and a crash are different", reasons["bd-stopped"])
	}
}

func TestPruneOnACleanMachineRemovesNothing(t *testing.T) {
	plan := planPrune(nil)
	if len(plan.Remove) != 0 {
		t.Errorf("Remove = %v, want nothing", plan.Remove)
	}
}

// The database outlives the record on purpose: a stopped instance can be
// resumed, and prune removing its history would make that a lie.
func TestPruneNeverTouchesTheEventHistory(t *testing.T) {
	plan := planPrune([]instance.Record{rec("bd-stopped", true, false)})
	for _, r := range plan.Remove {
		if r.RemovesHistory {
			t.Error("prune proposed removing an instance's recorded history")
		}
	}
}

// `list --json` is what an orchestrator reads, and whether an instance is
// running is the first thing it needs. The field is deliberately never stored
// — a stored answer goes stale the moment it is written — but leaving it out
// of the output too made the JSON strictly less useful than the human text,
// which showed it.
func TestListJSONSaysWhetherEachInstanceIsRunning(t *testing.T) {
	rows := listRows([]instance.Record{
		rec("bd-live", false, true),
		rec("bd-stopped", true, false),
	})

	if len(rows) != 2 {
		t.Fatalf("got %d rows", len(rows))
	}
	if !rows[0].Running {
		t.Error("a running instance is not reported as running")
	}
	if rows[1].Running {
		t.Error("a stopped instance is reported as running")
	}
}

func TestListJSONCarriesTheOwner(t *testing.T) {
	r := rec("bd-owned", false, true)
	r.Owner = "session-abc"

	rows := listRows([]instance.Record{r})
	if rows[0].Owner != "session-abc" {
		t.Errorf("Owner = %q, want it carried through", rows[0].Owner)
	}
}
