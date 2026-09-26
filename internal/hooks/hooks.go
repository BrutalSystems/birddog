// Package hooks records what Claude Code's lifecycle hooks report.
//
// Hooks are the only way an outside observer learns that a Claude Code
// session is running a tool or waiting on a permission decision — the session
// registry carries neither. This package is what a hook handler writes and
// what the observer reads.
//
// The representation is deliberately lock-free. Hooks fire as separate short
// processes, concurrently: Claude runs tools in parallel, so two PreToolUse
// handlers can be writing at the same moment. Rather than have them
// coordinate, each event touches only its own marker file, and state is
// derived by listing a directory. Nothing has to read-modify-write, so nothing
// can be lost to a race or stall a hook waiting on a lock.
package hooks

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Store is a directory of per-session hook state.
type Store struct {
	Dir string

	// StaleAfter bounds how long a marker is believed. A hook that reports a
	// tool starting cannot promise one will report it finishing: a session
	// killed mid-tool leaves its marker behind, and without a bound that
	// session reports work in progress forever.
	StaleAfter time.Duration
}

// defaultStaleAfter is generous, because a legitimate tool call can be long —
// a full test suite, a slow build. It only has to be shorter than "forever".
const defaultStaleAfter = 2 * time.Hour

// Tool is a tool call a hook reported starting.
type Tool struct {
	Name      string
	ID        string
	StartedAt time.Time
}

// Permission is a permission request a hook reported, still unanswered.
type Permission struct {
	Tool    string
	ID      string
	AskedAt time.Time
}

// State is what the hooks have reported about one session.
type State struct {
	// Present is false when no hook has ever reported this session — which
	// means hooks are not installed, or the session predates them. That is
	// not the same as a session with nothing outstanding.
	Present bool

	RunningTools       []Tool
	PendingPermissions []Permission

	LastActivityAt  time.Time
	LastTurnEndedAt time.Time
}

// Marker filename prefixes. Each event writes its own file, so concurrent
// hooks never contend.
const (
	toolPrefix = "tool."
	permPrefix = "perm."
	turnMarker = "turn-ended"
	activity   = "activity"
)

// Apply records one hook event.
//
// Unrecognised events are ignored rather than rejected: Claude Code has many
// lifecycle events and gains more, and a handler configured for one birddog
// does not use must not start failing.
func (s *Store) Apply(event map[string]any) error {
	sessionID, err := safeComponent(str(event["session_id"]), "session_id")
	if err != nil {
		return err
	}

	dir := filepath.Join(s.Dir, sessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create hook state directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("restrict hook state directory: %w", err)
	}

	switch str(event["hook_event_name"]) {
	case "PreToolUse":
		id, err := toolID(event)
		if err != nil {
			return err
		}
		// The tool proceeding answers any permission request for it.
		clearPermission(dir, id, str(event["tool_name"]))
		if err := touch(dir, toolPrefix+id, str(event["tool_name"])); err != nil {
			return err
		}

	case "PostToolUse", "PostToolUseFailure":
		id, err := toolID(event)
		if err != nil {
			return err
		}
		remove(dir, toolPrefix+id)
		// A tool that finished was not waiting on anyone, whether or not
		// birddog saw the PreToolUse that would have cleared it.
		clearPermission(dir, id, str(event["tool_name"]))

	case "PermissionRequest":
		id, err := toolID(event)
		if err != nil {
			return err
		}
		if err := touch(dir, permPrefix+id, str(event["tool_name"])); err != nil {
			return err
		}

	case "PermissionDenied":
		id, err := toolID(event)
		if err != nil {
			return err
		}
		// Denied is answered. The session is no longer waiting on a human,
		// whatever it does next.
		remove(dir, permPrefix+id)

	case "Stop", "SubagentStop":
		// A turn ending means nothing is mid-tool. Clearing here is what
		// stops a session killed mid-tool reporting work indefinitely.
		//
		// It says the turn ended and nothing more: birddog never reports a
		// finished turn as finished work.
		clearPrefix(dir, toolPrefix)
		// And nothing is waiting on a human. A turn cannot end while a
		// permission is outstanding, because the turn is what is blocked on
		// it — so anything still recorded here was answered, or abandoned,
		// which is not waiting either. This is the backstop for a request
		// whose identity birddog could not match; see clearPermission.
		clearPrefix(dir, permPrefix)
		if err := touch(dir, turnMarker, ""); err != nil {
			return err
		}

	case "SessionEnd":
		return os.RemoveAll(dir)
	}

	return touch(dir, activity, "")
}

// Read derives a session's state from its markers.
func (s *Store) Read(sessionID string) (State, error) {
	clean, err := safeComponent(sessionID, "session_id")
	if err != nil {
		return State{}, err
	}

	dir := filepath.Join(s.Dir, clean)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		// No hook has reported this session. Saying "nothing outstanding"
		// would turn an absence of hooks into evidence about the session.
		return State{}, nil
	}
	if err != nil {
		return State{}, fmt.Errorf("read hook state: %w", err)
	}

	stale := s.StaleAfter
	if stale <= 0 {
		stale = defaultStaleAfter
	}
	cutoff := time.Now().Add(-stale)

	state := State{Present: true}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		when := info.ModTime()
		if when.After(state.LastActivityAt) {
			state.LastActivityAt = when
		}

		name := entry.Name()
		switch {
		case name == turnMarker:
			state.LastTurnEndedAt = when

		case strings.HasPrefix(name, toolPrefix):
			if when.Before(cutoff) {
				continue // a marker this old is not evidence of a live tool
			}
			state.RunningTools = append(state.RunningTools, Tool{
				Name: readLabel(dir, name), ID: strings.TrimPrefix(name, toolPrefix), StartedAt: when,
			})

		case strings.HasPrefix(name, permPrefix):
			if when.Before(cutoff) {
				continue
			}
			state.PendingPermissions = append(state.PendingPermissions, Permission{
				Tool: readLabel(dir, name), ID: strings.TrimPrefix(name, permPrefix), AskedAt: when,
			})
		}
	}
	return state, nil
}

// Prune removes state for sessions that are no longer running.
func (s *Store) Prune(live func(sessionID string) bool) error {
	entries, err := os.ReadDir(s.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read hook state: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() && !live(entry.Name()) {
			_ = os.RemoveAll(filepath.Join(s.Dir, entry.Name()))
		}
	}
	return nil
}

// toolID reads the identifier a tool event is keyed on.
func toolID(event map[string]any) (string, error) {
	id := str(event["tool_use_id"])
	if id == "" {
		// Fall back to the tool name, so an event without an id still tracks
		// something rather than being dropped.
		id = str(event["tool_name"])
	}
	return safeComponent(id, "tool_use_id")
}

// clearPermission removes a permission request by every identity it may have
// been recorded under.
//
// toolID falls back to the tool name when an event carries no tool_use_id, so
// a request and the tool call that answers it can be recorded under different
// keys: observed on Claude Code 2.1.274, where PermissionRequest for
// AskUserQuestion carries no id and the PreToolUse that follows carries a real
// one. Removing only the id left the request outstanding, and the session
// reported waiting on a human who had already answered until the marker aged
// out — up to StaleAfter, two hours by default.
func clearPermission(dir, id, toolName string) {
	remove(dir, permPrefix+id)
	if toolName == "" || toolName == id {
		return
	}
	if name, err := safeComponent(toolName, "tool_name"); err == nil {
		remove(dir, permPrefix+name)
	}
}

// safeComponent refuses anything that could name a file outside the store.
func safeComponent(v, what string) (string, error) {
	switch {
	case v == "":
		return "", fmt.Errorf("%s is missing", what)
	case strings.ContainsAny(v, `/\`):
		return "", fmt.Errorf("%s %q contains a path separator", what, v)
	case v == "." || v == "..":
		return "", fmt.Errorf("%s %q is not a name", what, v)
	}
	return v, nil
}

// touch creates or refreshes a marker, optionally carrying a short label.
func touch(dir, name, label string) error {
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(label), 0o600); err != nil {
		return fmt.Errorf("write hook marker: %w", err)
	}
	return nil
}

func remove(dir, name string) {
	_ = os.Remove(filepath.Join(dir, name))
}

func clearPrefix(dir, prefix string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), prefix) {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}

func readLabel(dir, name string) string {
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func str(v any) string {
	s, _ := v.(string)
	return s
}
