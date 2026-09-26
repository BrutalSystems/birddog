// internal/observe/transcript/tail_test.go
package transcript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const asstText = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}`
const sysRec = `{"type":"system"}`

func TestTailReturnsMessageRecordsOldestFirst(t *testing.T) {
	p := write(t,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"first"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"second"}]}}`)
	recs, _, err := Tail(p, DefaultWindow)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2", len(recs))
	}
	if recs[0].Parts[0].Text != "first" || recs[1].Parts[0].Text != "second" {
		t.Errorf("records out of order: %v", recs)
	}
}

// The tail is dense with these; all six sessions sampled had at least one
// between the last message and end of file.
func TestTailSkipsNonMessageRecords(t *testing.T) {
	p := write(t, asstText, sysRec, `{"type":"attachment"}`, `{"type":"atis-latch"}`)
	recs, _, err := Tail(p, DefaultWindow)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if len(recs) != 1 || recs[0].Parts[0].Text != "done" {
		t.Errorf("got %v, want the single assistant record", recs)
	}
}

// Review Focus 1: the session is appending as birddog reads.
func TestTailDiscardsAPartialTrailingLine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	body := asstText + "\n" + `{"type":"assistant","message":{"role":"ass`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	recs, _, err := Tail(p, DefaultWindow)
	if err != nil {
		t.Fatalf("Tail returned an error for a partial line: %v", err)
	}
	if len(recs) != 1 || recs[0].Parts[0].Text != "done" {
		t.Errorf("got %v, want the one complete record", recs)
	}
}

func TestTailSkipsAMalformedLineMidFile(t *testing.T) {
	p := write(t, asstText, `{not json`, asstText)
	recs, _, err := Tail(p, DefaultWindow)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if len(recs) != 2 {
		t.Errorf("got %d records, want 2", len(recs))
	}
}

// Review Focus 2: one record larger than the whole window.
func TestTailReturnsNothingWhenNoCompleteLineFits(t *testing.T) {
	huge := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"` +
		strings.Repeat("x", 4096) + `"}]}}`
	p := write(t, huge)
	recs, _, err := Tail(p, 256)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("got %d records, want 0 — no complete line fits the window", len(recs))
	}
}

func TestTailReadsOnlyTheWindow(t *testing.T) {
	lines := make([]string, 0, 200)
	for i := 0; i < 200; i++ {
		lines = append(lines, asstText)
	}
	p := write(t, lines...)
	recs, _, err := Tail(p, 1024)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if len(recs) == 0 || len(recs) >= 200 {
		t.Errorf("got %d records, want a bounded subset of 200", len(recs))
	}
}

func TestTailOnAMissingFileIsAnError(t *testing.T) {
	if _, _, err := Tail(filepath.Join(t.TempDir(), "nope.jsonl"), DefaultWindow); err == nil {
		t.Error("Tail on a missing file returned no error")
	}
}

// internal/observe/transcript/tail_test.go — append

// The hand-written tests pin the parser; this one pins the format. Every
// record here was captured from a real session and rebuilt from an allowlist
// of keys, so a change in Claude Code's transcript shape fails here rather
// than in production.
func TestTailParsesRealRecordShapes(t *testing.T) {
	recs, _, err := Tail("testdata/real_shape.jsonl", DefaultWindow)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if len(recs) != 3 {
		t.Fatalf("got %d records, want 3", len(recs))
	}

	var sawText, sawAsk, sawResult bool
	for _, r := range recs {
		if r.Role == "" {
			t.Errorf("record of type %q has no role", r.Type)
		}
		for _, p := range r.Parts {
			switch p.Type {
			case "text":
				sawText = sawText || p.Text != ""
			case "tool_use":
				if p.Name == "AskUserQuestion" {
					// The id is what pairing is decided on, and the input
					// is what askText reads. Both must survive the format.
					if p.ID == "" {
						t.Error("AskUserQuestion record has no tool_use id")
					}
					if got := askText(p.Input); got == "" {
						t.Errorf("askText could not read a real AskUserQuestion input: %s", p.Input)
					}
					sawAsk = true
				}
			case "tool_result":
				sawResult = sawResult || p.ToolUseID != ""
			}
		}
	}
	if !sawText || !sawAsk || !sawResult {
		t.Errorf("fixture lost a shape: text=%v ask=%v result=%v", sawText, sawAsk, sawResult)
	}
}

// A window that happens to begin exactly at a record boundary has no partial
// first line, so dropping one discards a record that was read in full. The
// guard has to key on whether the byte before the window is a newline, not on
// whether the file was longer than the window.
func TestTailKeepsARecordThatStartsExactlyAtTheWindow(t *testing.T) {
	first := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"first"}]}}`
	second := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"second"}]}}`
	p := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(p, []byte(first+"\n"+second+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Exactly the second record and its newline.
	recs, _, err := Tail(p, int64(len(second)+1))
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1 — the window is aligned to a record boundary", len(recs))
	}
	if recs[0].Parts[0].Text != "second" {
		t.Errorf("Parts[0].Text = %q, want second", recs[0].Parts[0].Text)
	}
}

// A file whose last line has no terminating newline. Dropping it is right for
// a session writing as we read — that fragment cannot parse — but wrong for a
// complete record, and whether a record is complete is decided by parsing it,
// not by what follows it.
func TestTailKeepsACompleteLastLineWithNoTrailingNewline(t *testing.T) {
	first := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"first"}]}}`
	second := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"second"}]}}`
	p := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(p, []byte(first+"\n"+second), 0o644); err != nil {
		t.Fatal(err)
	}

	recs, _, err := Tail(p, DefaultWindow)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2 — the last line is complete, it just has no newline after it", len(recs))
	}
	if recs[1].Parts[0].Text != "second" {
		t.Errorf("Parts[1].Text = %q, want second", recs[1].Parts[0].Text)
	}
}
