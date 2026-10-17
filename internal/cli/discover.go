// Package cli renders birddog's command output.
//
// Rendering is kept separate from observation so both output forms are
// fixture-testable without a live session anywhere.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"unicode/utf8"

	"github.com/BrutalSystems/birddog/internal/discover/claude"
	"github.com/BrutalSystems/birddog/internal/discover/codex"
	"github.com/BrutalSystems/birddog/internal/platform/where"
)

// Visibility describes what birddog can see of a signal, as distinct from what
// it saw. "not_observed" means looked-for and absent; "unavailable" means no
// route to look at all. Collapsing the two would let silence masquerade as
// evidence — see docs/handoff.md, acceptance criterion 14.
const (
	VisibilityObserved    = "observed"
	VisibilityNotObserved = "not_observed"
	VisibilityUnavailable = "unavailable"
)

// statusUnavailable stands where a status could not be read at all, so an
// unreadable session cannot be mistaken for one that simply has no status.
const statusUnavailable = "unavailable"

// Row is one discovered session, in the shape an orchestrator needs to
// register it as a target: an unambiguous session identity, the process
// identity that guards against PID reuse, and an explicit statement of what
// may be read as current and what could not be seen at all.
type Row struct {
	Provider        string `json:"provider"`
	SessionID       string `json:"session_id"`
	PID             int    `json:"pid"`
	ProcStart       string `json:"proc_start,omitempty"`
	Name            string `json:"name"`
	CWD             string `json:"cwd"`
	Kind            string `json:"kind,omitempty"`
	Entrypoint      string `json:"entrypoint,omitempty"`
	Version         string `json:"version,omitempty"`
	Status          string `json:"status"`
	StatusUpdatedAt string `json:"status_updated_at,omitempty"`
	Live            bool   `json:"live"`
	StatusIsCurrent bool   `json:"status_is_current"`

	// InputRequestVisibility says whether a permission or input wait could be
	// observed for this session — never whether one exists.
	InputRequestVisibility string `json:"input_request_visibility"`

	// Location says where the session can be focused or attached. Absent
	// when the session is not live or nothing could be found: locating is
	// best-effort and never fails a listing.
	Location *where.Location `json:"location,omitempty"`
}

// Locate fills in Location for every live row with a pid, resolving them all
// in one pass. A resolve error leaves every location absent rather than
// failing discovery.
func Locate(rows []Row, resolve func(pids []int) (map[int]where.Location, error)) {
	var pids []int
	for _, r := range rows {
		if r.Live && r.PID > 0 {
			pids = append(pids, r.PID)
		}
	}
	if len(pids) == 0 {
		return
	}
	locs, err := resolve(pids)
	if err != nil {
		return
	}
	for i := range rows {
		if !rows[i].Live || rows[i].PID <= 0 {
			continue
		}
		if loc, ok := locs[rows[i].PID]; ok && !loc.Empty() {
			l := loc
			rows[i].Location = &l
		}
	}
}

// whereSessionWidth caps a tmux session name in the WHERE column. muster's
// names carry a full UUID; its first eight hex digits are what tells them
// apart at a glance, and the JSON carries the whole name.
const whereSessionWidth = 15

// whereText renders a location compactly for the WHERE column.
func whereText(l *where.Location) string {
	if l == nil {
		return "—"
	}
	if t := l.Tmux; t != nil {
		server := t.SocketName
		if server == "" {
			server = t.Socket
		}
		return fmt.Sprintf("tmux %s:%s:%s.%s", server, truncate(t.Session, whereSessionWidth), t.Window, t.Pane)
	}
	tty := strings.TrimPrefix(l.TTY, "/dev/")
	switch {
	case l.TerminalApp != "" && tty != "":
		return l.TerminalApp + " " + tty
	case tty != "":
		return tty
	case l.TerminalApp != "":
		return l.TerminalApp
	}
	return "—"
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// FromClaude converts Claude Code sessions to rows.
//
// Only the fields named here are read, so the peer tokens stored alongside the
// session records cannot reach any output.
func FromClaude(ss []claude.Session) []Row {
	rows := make([]Row, 0, len(ss))
	for _, s := range ss {
		rows = append(rows, Row{
			Provider:        "claude",
			SessionID:       s.SessionID,
			PID:             s.PID,
			ProcStart:       s.ProcStart,
			Name:            s.Name,
			CWD:             s.CWD,
			Kind:            s.Kind,
			Entrypoint:      s.Entrypoint,
			Version:         s.Version,
			Status:          s.Status,
			StatusUpdatedAt: s.StatusUpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
			Live:            s.Live,
			StatusIsCurrent: s.StatusIsCurrent(),
			// The registry carries no permission-request signal; that comes
			// from hooks, which discovery does not install or require.
			InputRequestVisibility: VisibilityUnavailable,
		})
	}
	return rows
}

// FromCodex converts live Codex threads to rows.
func FromCodex(ss []codex.Session) []Row {
	rows := make([]Row, 0, len(ss))
	for _, s := range ss {
		// An unknown state is reported as unknown. Codex's state database
		// reports `notLoaded` for threads owned by another process, which is
		// birddog's own view rather than the session's; borrowing it would
		// invent an observation.
		status := s.Status
		if !s.StatusKnown {
			status = statusUnavailable
		}
		rows = append(rows, Row{
			Provider:  "codex",
			SessionID: s.ThreadID,
			// The lock holder is both the process identity and the evidence
			// that the thread is live.
			PID:    s.HolderPID,
			Name:   s.Name,
			CWD:    s.CWD,
			Status: status,
			Live:   s.Live,
			// A status describes a live thread only when it was actually
			// observed for that thread.
			StatusIsCurrent: s.Live && s.StatusKnown,
			// Codex exposes no input-wait signal to an external observer.
			InputRequestVisibility: VisibilityUnavailable,
		})
	}
	return rows
}

func sortRows(rows []Row) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Name != rows[j].Name {
			return rows[i].Name < rows[j].Name
		}
		return rows[i].SessionID < rows[j].SessionID
	})
}

// RenderJSON writes the machine-readable listing.
func RenderJSON(w io.Writer, rows []Row) error {
	sorted := append([]Row(nil), rows...)
	sortRows(sorted)
	if sorted == nil {
		sorted = []Row{}
	}

	out := struct {
		Sessions []Row `json:"sessions"`
	}{Sessions: sorted}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// RenderText writes the human-readable listing.
func RenderText(w io.Writer, rows []Row) error {
	if len(rows) == 0 {
		_, err := fmt.Fprintln(w, "No attachable sessions found.")
		return err
	}
	sorted := append([]Row(nil), rows...)
	sortRows(sorted)

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tPROVIDER\tSTATUS\tPID\tWHERE\tSESSION\tCWD")
	for _, r := range sorted {
		status := r.Status
		if !r.StatusIsCurrent && status != statusUnavailable {
			// Never present a dead session's last state as a current fact.
			status += " (stale)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\t%s\n",
			r.Name, r.Provider, status, r.PID, whereText(r.Location), r.SessionID, r.CWD)
	}
	return tw.Flush()
}
