package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/BrutalSystems/birddog/internal/discover/claude"
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
