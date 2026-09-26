// internal/observe/transcript/tail.go
package transcript

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// DefaultWindow is how much of the end of a transcript is read.
//
// Transcripts grow without bound — 32 to 2786 records across five live
// sessions when this was measured — and both signals live at the end, because
// a pending question is by definition the last thing the session said. The
// window is bounded in bytes rather than records because one record carrying
// a large tool result can be megabytes on its own.
// Measured across 485 real transcripts on one machine: the distance from EOF
// back to the last message record reached 201,598 bytes, because Claude Code
// writes a large `attachment` record after a message and one of those was
// 633,467 bytes on its own. A window that does not reach a message record
// cannot report "no question" — see Tail's second return value.
const DefaultWindow = int64(1 << 20)

// Part is one element of a message's content.
type Part struct {
	Type      string `json:"type"`
	Text      string `json:"text"`
	Name      string `json:"name"`
	ID        string `json:"id"`
	ToolUseID string `json:"tool_use_id"`

	// Input is a tool_use's arguments, decoded lazily by the caller that
	// understands the tool.
	Input json.RawMessage `json:"input"`
}

// Record is one transcript line birddog cares about: an assistant or user
// message. Every other record type is dropped by Tail.
type Record struct {
	Type        string
	Role        string
	IsSidechain bool
	Parts       []Part
}

// line is the wire shape of a transcript record.
type line struct {
	Type        string `json:"type"`
	IsSidechain bool   `json:"isSidechain"`
	Message     struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// Tail reads the last window bytes of a transcript and returns the message
// records found there, oldest first.
//
// The second return value reports whether the window reached the start of the
// file. It matters because the records between the last message and EOF are
// unbounded: a window that covered only those saw nothing, which is not the
// same as having looked and found no question, and a caller must be able to
// tell the two apart.
//
// A window that begins mid-record discards that partial first line, and a
// transcript being appended to as it is read ends in a partial last line which
// is discarded the same way. Neither is an error: reading a file another
// process is writing is the normal case, not a fault.
func Tail(path string, window int64) ([]Record, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, fmt.Errorf("transcript: open %s: %w", path, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, false, fmt.Errorf("transcript: stat %s: %w", path, err)
	}

	start := int64(0)
	truncated := false
	if info.Size() > window {
		start = info.Size() - window
		truncated = true
	}
	// One byte of context before the window, which is what says whether the
	// window opens on a record boundary or in the middle of one. Without it a
	// window that happens to land exactly on a boundary discards a record it
	// read in full.
	from := start
	if truncated {
		from--
	}
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return nil, false, fmt.Errorf("transcript: seek %s: %w", path, err)
	}
	buf, err := io.ReadAll(f)
	if err != nil {
		return nil, false, fmt.Errorf("transcript: read %s: %w", path, err)
	}

	if truncated && len(buf) > 0 {
		aligned := buf[0] == '\n'
		buf = buf[1:]
		if !aligned {
			// The window opened inside a record; its remains are the first
			// line and are not a record.
			if i := bytes.IndexByte(buf, '\n'); i >= 0 {
				buf = buf[i+1:]
			} else {
				buf = nil
			}
		}
	}

	lines := bytes.Split(buf, []byte("\n"))

	out := make([]Record, 0, len(lines))
	for _, raw := range lines {
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		var l line
		if json.Unmarshal(raw, &l) != nil {
			// A record birddog cannot read is not a record it guesses at.
			//
			// This is also what discards a trailing fragment from a session
			// writing as we read: truncating a JSON object cannot leave
			// valid JSON, because the closing brace goes with the tail.
			// Deciding by parse rather than by "is there a newline after it"
			// keeps a complete last line that simply has none.
			continue
		}
		if l.Type != "assistant" && l.Type != "user" {
			continue
		}
		var parts []Part
		if len(l.Message.Content) > 0 {
			if json.Unmarshal(l.Message.Content, &parts) != nil {
				// Content is a bare string on some records.
				var s string
				if json.Unmarshal(l.Message.Content, &s) == nil {
					parts = []Part{{Type: "text", Text: s}}
				}
			}
		}
		out = append(out, Record{
			Type: l.Type, Role: l.Message.Role,
			IsSidechain: l.IsSidechain, Parts: parts,
		})
	}
	return out, !truncated, nil
}
