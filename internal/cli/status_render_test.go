package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/BrutalSystems/birddog/internal/daemon"
	"github.com/BrutalSystems/birddog/internal/platform/where"
)

func TestStatusTextShowsPolicyAndWhere(t *testing.T) {
	until := time.Date(2026, 10, 16, 20, 0, 0, 0, time.UTC)
	var out bytes.Buffer
	err := renderStatus(&out, daemon.StatusResult{
		InstanceID: "bd-1", Name: "cos-masters",
		Targets: []daemon.TargetStatus{
			{
				ID: "worker-1", Provider: "claude", Status: "idle", StatusIsCurrent: true,
				Location: &where.Location{TTY: "/dev/ttys005", Tmux: &where.Tmux{
					SocketName: "muster", Session: "muster-1", Window: "0", Pane: "%38",
				}},
				Policy: &daemon.PolicyView{
					AlertOn: []string{"exit", "quiet"}, QuietAfterSeconds: 300, IdleGraceSeconds: 30,
					NoProgressAfterSeconds: 3600,
					ExpectedQuietUntil:     &until, ExpectedQuietReason: "integration suite", ExpectedQuietActive: true,
				},
			},
			{ID: "worker-2", Provider: "claude", Policy: &daemon.PolicyView{QuietAfterSeconds: 300}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"WHERE", "tmux muster:muster-1:0.%38",
		"ALERT ON", "exit,quiet", "5m0s", "30s", "1h0m0s",
		"until 2026-10-16T20:00:00Z — integration suite",
		// A target alerting on nothing is said outright, and no_progress is
		// off rather than zero.
		"nothing", "off",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("status text lacks %q:\n%s", want, text)
		}
	}
}

func TestStatusTextMarksALapsedOverride(t *testing.T) {
	until := time.Date(2026, 10, 16, 20, 0, 0, 0, time.UTC)
	got := expectedQuietText(daemon.PolicyView{ExpectedQuietUntil: &until})
	if !strings.Contains(got, "(lapsed)") {
		t.Errorf("expectedQuietText = %q, want a lapsed override marked", got)
	}
}

func TestSyncTextListsEachChange(t *testing.T) {
	var out bytes.Buffer
	res := daemon.WatchSyncResult{
		Source: "/ledger.json", Changed: true,
		Added: []string{"w2"}, Removed: []string{"w3"}, Unchanged: []string{"w1"},
		NotApplied: []string{"notification: takes effect when the instance is resumed"},
	}
	res.ConfigRevision, res.Targets = 4, 2
	if err := renderSync(&out, res); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"Synced from /ledger.json", "revision 4", "added     w2", "removed   w3", "unchanged w1", "not applied: notification", "not touched"} {
		if !strings.Contains(text, want) {
			t.Errorf("sync text lacks %q:\n%s", want, text)
		}
	}
}

// An instance from before 2.1 sends no policy. Reading zeroes in its place
// would report a target that alerts on nothing.
func TestStatusTextSaysPolicyIsUnknownForAnOlderInstance(t *testing.T) {
	var out bytes.Buffer
	if err := renderStatus(&out, daemon.StatusResult{
		InstanceID: "bd-1", Name: "cos-masters",
		Targets: []daemon.TargetStatus{{ID: "worker-1", Provider: "claude", Status: "idle", StatusIsCurrent: true}},
	}); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "unknown (instance predates 2.1)") {
		t.Errorf("status text lacks the unknown-policy line:\n%s", text)
	}
	if strings.Contains(text, "nothing") || strings.Contains(text, "0s") {
		t.Errorf("status text invents a policy for an older instance:\n%s", text)
	}
}
