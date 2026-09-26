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
	"text/tabwriter"

	"github.com/BrutalSystems/birddog/internal/discover/claude"
	"github.com/BrutalSystems/birddog/internal/discover/codex"
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
	fmt.Fprintln(tw, "NAME\tPROVIDER\tSTATUS\tPID\tSESSION\tCWD")
	for _, r := range sorted {
		status := r.Status
		if !r.StatusIsCurrent && status != statusUnavailable {
			// Never present a dead session's last state as a current fact.
			status += " (stale)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\n",
			r.Name, r.Provider, status, r.PID, r.SessionID, r.CWD)
	}
	return tw.Flush()
}
