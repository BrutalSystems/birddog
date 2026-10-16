package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/BrutalSystems/birddog/internal/discover/claude"
	"github.com/BrutalSystems/birddog/internal/platform/where"
)

func sessions() []claude.Session {
	return []claude.Session{
		{
			SessionID: "bbbb", PID: 2, Name: "zeta", CWD: "/w/z", Status: "active",
			ProcStart: "Thu Sep 17 22:11:09 2026", Kind: "interactive", Entrypoint: "cli",
			Version: "2.1.267", StatusUpdatedAt: time.UnixMilli(1789683614652), Live: true,
		},
		{
			SessionID: "aaaa", PID: 1, Name: "alpha", CWD: "/w/a", Status: "active",
			ProcStart: "Thu Sep 17 21:00:00 2026", Kind: "interactive", Entrypoint: "cli",
			Version: "2.1.267", StatusUpdatedAt: time.UnixMilli(1789683000000), Live: false,
		},
	}
}

func TestRenderJSONCarriesTheFieldsAnOrchestratorNeedsToRegisterATarget(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderJSON(&buf, FromClaude(sessions())); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}

	var got struct {
		Sessions []map[string]any `json:"sessions"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	if len(got.Sessions) != 2 {
		t.Fatalf("got %d sessions, want 2", len(got.Sessions))
	}

	// Registering a target needs an unambiguous session identity, the process
	// identity that guards against PID reuse, and the provider it belongs to.
	for _, key := range []string{"provider", "session_id", "pid", "proc_start", "cwd", "name", "status", "live", "status_is_current"} {
		if _, ok := got.Sessions[0][key]; !ok {
			t.Errorf("missing key %q in output", key)
		}
	}
	if got.Sessions[0]["provider"] != "claude" {
		t.Errorf("provider = %v, want claude", got.Sessions[0]["provider"])
	}
}

func TestRenderJSONSortsByNameForStableOutput(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderJSON(&buf, FromClaude(sessions())); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	var got struct {
		Sessions []map[string]any `json:"sessions"`
	}
	_ = json.Unmarshal(buf.Bytes(), &got)

	if got.Sessions[0]["name"] != "alpha" || got.Sessions[1]["name"] != "zeta" {
		t.Errorf("order = %v, %v; want alpha, zeta", got.Sessions[0]["name"], got.Sessions[1]["name"])
	}
}

// A dead session's status must never be presented as a current fact.
func TestRenderJSONMarksDeadSessionStatusAsNotCurrent(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderJSON(&buf, FromClaude(sessions())); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	var got struct {
		Sessions []map[string]any `json:"sessions"`
	}
	_ = json.Unmarshal(buf.Bytes(), &got)

	alpha := got.Sessions[0] // not live
	if alpha["live"] != false {
		t.Errorf("live = %v, want false", alpha["live"])
	}
	if alpha["status_is_current"] != false {
		t.Errorf("status_is_current = %v, want false for a dead session", alpha["status_is_current"])
	}
	if alpha["status"] != "active" {
		t.Errorf("status = %v, want the last-known value preserved", alpha["status"])
	}
}

// Nothing in the registry's credential material may reach the output.
func TestRenderJSONEmitsNoTokenMaterial(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderJSON(&buf, FromClaude(sessions())); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	for _, forbidden := range []string{"peerToken", "peer_token", "token", ".key"} {
		if strings.Contains(buf.String(), forbidden) {
			t.Errorf("output contains %q:\n%s", forbidden, buf.String())
		}
	}
}

func TestRenderTextMarksStaleSessions(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderText(&buf, FromClaude(sessions())); err != nil {
		t.Fatalf("RenderText: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "alpha") || !strings.Contains(out, "zeta") {
		t.Fatalf("missing session names:\n%s", out)
	}
	if !strings.Contains(out, "stale") {
		t.Errorf("dead session not marked stale:\n%s", out)
	}
}

func TestRenderTextSaysSoWhenNothingIsFound(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderText(&buf, nil); err != nil {
		t.Fatalf("RenderText: %v", err)
	}
	if !strings.Contains(buf.String(), "No") {
		t.Errorf("empty listing should say so plainly, got:\n%s", buf.String())
	}
}

// An empty listing is still valid JSON with an empty array, never null — a
// consumer must not have to special-case it.
func TestRenderJSONEmitsEmptyArrayNotNull(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderJSON(&buf, nil); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	if strings.Contains(buf.String(), "null") {
		t.Errorf("empty listing rendered null, want []:\n%s", buf.String())
	}
}

func fakeLocations(locs map[int]where.Location) func([]int) (map[int]where.Location, error) {
	return func([]int) (map[int]where.Location, error) { return locs, nil }
}

// Only a live session has a place to be found; a dead one's pid may already
// belong to someone else.
func TestLocateFillsOnlyLiveRows(t *testing.T) {
	rows := FromClaude(sessions())
	var asked []int
	Locate(rows, func(pids []int) (map[int]where.Location, error) {
		asked = pids
		return map[int]where.Location{
			1: {TTY: "/dev/ttys001"},
			2: {TTY: "/dev/ttys002", TerminalApp: "Ghostty"},
		}, nil
	})
	if len(asked) != 1 || asked[0] != 2 {
		t.Errorf("resolved pids = %v, want only the live one [2]", asked)
	}
	for _, r := range rows {
		switch r.PID {
		case 1:
			if r.Location != nil {
				t.Errorf("dead session got a location: %+v", r.Location)
			}
		case 2:
			if r.Location == nil || r.Location.TerminalApp != "Ghostty" {
				t.Errorf("live session location = %+v", r.Location)
			}
		}
	}
}

// Locating is best-effort: a failure leaves locations absent, never an error.
func TestLocateSwallowsAResolveFailure(t *testing.T) {
	rows := FromClaude(sessions())
	Locate(rows, func([]int) (map[int]where.Location, error) { return nil, errors.New("ps failed") })
	for _, r := range rows {
		if r.Location != nil {
			t.Errorf("location set despite a failed resolve: %+v", r.Location)
		}
	}
}

func TestRenderJSONCarriesTheTmuxLocation(t *testing.T) {
	rows := FromClaude(sessions())
	Locate(rows, fakeLocations(map[int]where.Location{2: {
		TTY: "/dev/ttys005",
		Tmux: &where.Tmux{
			Socket: "/private/tmp/tmux-501/muster", SocketName: "muster",
			Session: "muster-bafad08e-66ea", Window: "0", Pane: "%38",
			Target: "muster-bafad08e-66ea:0.0",
		},
	}}))
	var buf bytes.Buffer
	if err := RenderJSON(&buf, rows); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Sessions []struct {
			Name     string          `json:"name"`
			Location *where.Location `json:"location"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	alpha, zeta := got.Sessions[0], got.Sessions[1]
	if alpha.Location != nil {
		t.Errorf("dead session has a location: %+v", alpha.Location)
	}
	if zeta.Location == nil || zeta.Location.Tmux == nil || zeta.Location.Tmux.Target != "muster-bafad08e-66ea:0.0" {
		t.Errorf("zeta location = %+v", zeta.Location)
	}
	if !strings.Contains(buf.String(), `"socket": "/private/tmp/tmux-501/muster"`) {
		t.Errorf("socket path missing:\n%s", buf.String())
	}
}

func TestWhereTextIsShortForEachKindOfLocation(t *testing.T) {
	cases := []struct {
		loc  *where.Location
		want string
	}{
		{nil, "—"},
		{&where.Location{TTY: "/dev/ttys005"}, "ttys005"},
		{&where.Location{TTY: "/dev/ttys005", TerminalApp: "Ghostty"}, "Ghostty ttys005"},
		{&where.Location{TTY: "/dev/ttys005", Tmux: &where.Tmux{
			SocketName: "muster", Session: "muster-bafad08e-66ea-4330", Window: "0", Pane: "%38",
		}}, "tmux muster:muster-bafad08e…:0.%38"},
		{&where.Location{Tmux: &where.Tmux{
			Socket: "/srv/tmux.sock", Session: "work", Window: "2", Pane: "%9",
		}}, "tmux /srv/tmux.sock:work:2.%9"},
	}
	for _, c := range cases {
		if got := whereText(c.loc); got != c.want {
			t.Errorf("whereText(%+v) = %q, want %q", c.loc, got, c.want)
		}
	}
}

func TestRenderTextShowsAWhereColumn(t *testing.T) {
	rows := FromClaude(sessions())
	Locate(rows, fakeLocations(map[int]where.Location{2: {TTY: "/dev/ttys002", TerminalApp: "Ghostty"}}))
	var buf bytes.Buffer
	if err := RenderText(&buf, rows); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "WHERE") || !strings.Contains(out, "Ghostty ttys002") || !strings.Contains(out, "—") {
		t.Errorf("WHERE column missing or incomplete:\n%s", out)
	}
}
