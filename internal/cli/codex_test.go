package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/BrutalSystems/birddog/internal/discover/codex"
)

func decode(t *testing.T, b []byte) []map[string]any {
	t.Helper()
	var got struct {
		Sessions []map[string]any `json:"sessions"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, b)
	}
	return got.Sessions
}

func TestCodexRowsCarryThreadIdentityAndLockHolder(t *testing.T) {
	var buf bytes.Buffer
	rows := FromCodex([]codex.Session{{
		ThreadID: "01a0c3ae", Name: "auth", CWD: "/work/api", Status: "idle",
		HolderPID: 4242, Live: true, MetadataAvailable: true,
	}})
	if err := RenderJSON(&buf, rows); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}

	s := decode(t, buf.Bytes())[0]
	if s["provider"] != "codex" {
		t.Errorf("provider = %v, want codex", s["provider"])
	}
	if s["session_id"] != "01a0c3ae" {
		t.Errorf("session_id = %v, want the thread id", s["session_id"])
	}
	if s["pid"] != float64(4242) {
		t.Errorf("pid = %v, want the lock holder 4242", s["pid"])
	}
}

// Criterion 14: absence of an observed permission request must never read as
// proof that a worker is unblocked. Codex exposes no input-wait signal at all,
// so the answer is "unavailable" — not "none".
func TestCodexReportsInputRequestVisibilityUnavailable(t *testing.T) {
	var buf bytes.Buffer
	rows := FromCodex([]codex.Session{{ThreadID: "t1", Live: true, MetadataAvailable: true}})
	if err := RenderJSON(&buf, rows); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}

	s := decode(t, buf.Bytes())[0]
	if s["input_request_visibility"] != "unavailable" {
		t.Errorf("input_request_visibility = %v, want unavailable", s["input_request_visibility"])
	}
}

// Losing the app-server costs names and statuses. That gap must be visible,
// not rendered as a session that merely has no status.
func TestCodexMarksStatusUnavailableWhenMetadataCouldNotBeRead(t *testing.T) {
	var buf bytes.Buffer
	rows := FromCodex([]codex.Session{{ThreadID: "t1", HolderPID: 7, Live: true, MetadataAvailable: false}})
	if err := RenderJSON(&buf, rows); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}

	s := decode(t, buf.Bytes())[0]
	if s["status"] != "unavailable" {
		t.Errorf("status = %v, want unavailable", s["status"])
	}
	if s["status_is_current"] != false {
		t.Errorf("status_is_current = %v, want false", s["status_is_current"])
	}
	if s["live"] != true {
		t.Errorf("live = %v, want true — the lock still proves the thread runs", s["live"])
	}
}

// Claude Code can observe permission requests, but only through hooks. Until
// those are installed the honest answer is also "unavailable".
func TestClaudeReportsInputRequestVisibilityUnavailableWithoutHooks(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderJSON(&buf, FromClaude(sessions())); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	for _, s := range decode(t, buf.Bytes()) {
		if s["input_request_visibility"] != "unavailable" {
			t.Errorf("input_request_visibility = %v, want unavailable", s["input_request_visibility"])
		}
	}
}

func TestRenderTextGroupsBothProvidersInOneListing(t *testing.T) {
	var buf bytes.Buffer
	rows := append(FromClaude(sessions()), FromCodex([]codex.Session{
		{ThreadID: "01a0c3ae", Name: "auth", CWD: "/work/api", Status: "idle", HolderPID: 1, Live: true, MetadataAvailable: true},
	})...)
	if err := RenderText(&buf, rows); err != nil {
		t.Fatalf("RenderText: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"claude", "codex", "alpha", "auth"} {
		if !strings.Contains(out, want) {
			t.Errorf("listing missing %q:\n%s", want, out)
		}
	}
}

// A live thread whose state birddog cannot see must say so, not borrow a
// plausible-looking status. The thread is still listed: liveness is known even
// when the state is not.
func TestCodexRendersUnknownStatusAsUnavailable(t *testing.T) {
	var buf bytes.Buffer
	rows := FromCodex([]codex.Session{{
		ThreadID: "t1", Name: "auth", CWD: "/work/api",
		HolderPID: 5, Live: true, MetadataAvailable: true, StatusKnown: false,
	}})
	if err := RenderJSON(&buf, rows); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}

	s := decode(t, buf.Bytes())[0]
	if s["status"] != "unavailable" {
		t.Errorf("status = %v, want unavailable", s["status"])
	}
	if s["status_is_current"] != false {
		t.Errorf("status_is_current = %v, want false", s["status_is_current"])
	}
	if s["live"] != true {
		t.Errorf("live = %v, want true — the writer lock still proves it runs", s["live"])
	}
}

func TestCodexRendersKnownStatusAsCurrent(t *testing.T) {
	var buf bytes.Buffer
	rows := FromCodex([]codex.Session{{
		ThreadID: "t1", Status: "active",
		HolderPID: 5, Live: true, MetadataAvailable: true, StatusKnown: true,
	}})
	if err := RenderJSON(&buf, rows); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}

	s := decode(t, buf.Bytes())[0]
	if s["status"] != "active" || s["status_is_current"] != true {
		t.Errorf("status = %v, current = %v; want active/true", s["status"], s["status_is_current"])
	}
}
