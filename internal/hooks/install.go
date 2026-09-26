package hooks

import (
	"fmt"
	"sort"
)

// Installing hooks edits a settings file an operator owns and may have
// configured heavily. Everything here works on the parsed document in place,
// appends rather than replaces, and can be undone exactly — because the worst
// outcome of installing monitoring would be destroying somebody's own hooks.

// InstalledEvents are the lifecycle events birddog asks to be told about.
//
// Deliberately few. Each one costs a process spawn on Claude's critical path,
// so birddog subscribes only to what it cannot learn from the session
// registry: tool boundaries, permission decisions, and turn ends.
var InstalledEvents = []string{
	"PreToolUse",
	"PostToolUse",
	"PermissionRequest",
	"PermissionDenied",
	"Stop",
	"SubagentStop",
	"SessionEnd",
}

// marker identifies birddog's own handlers, so uninstall can remove exactly
// them and nothing else.
const marker = " hook"

// Status describes what is currently installed.
type Status_ struct {
	Installed  bool
	Partial    bool
	BinaryPath string
	Events     []string
	Missing    []string
}

// Install adds birddog's hook handlers to a parsed settings document.
//
// It reports whether anything changed, so a caller can avoid rewriting a file
// that is already correct.
func Install(settings map[string]any, binary string) (bool, error) {
	if binary == "" {
		return false, fmt.Errorf("no birddog binary path to install")
	}
	command := binary + marker

	hooks, err := hooksMap(settings)
	if err != nil {
		return false, err
	}

	// Asked and answered before touching anything: a document that already
	// says exactly this needs no rewrite, and reporting a change would make
	// the command rewrite a file every time it ran.
	if matchesInstall(hooks, command) {
		settings["hooks"] = hooks
		return false, nil
	}

	for _, event := range InstalledEvents {
		groups, _ := hooks[event].([]any)

		// Drop any birddog handler already there, whatever path it names, so
		// a moved or rebuilt binary is re-pointed rather than duplicated.
		kept, _ := withoutBirddog(groups)

		kept = append(kept, map[string]any{
			"hooks": []any{map[string]any{"type": "command", "command": command}},
		})
		hooks[event] = kept
	}

	settings["hooks"] = hooks
	return true, nil
}

// Uninstall removes birddog's handlers and leaves everything else alone.
func Uninstall(settings map[string]any) (bool, error) {
	hooks, err := hooksMap(settings)
	if err != nil {
		return false, err
	}

	changed := false
	for event, raw := range hooks {
		groups, ok := raw.([]any)
		if !ok {
			continue
		}
		kept, removed := withoutBirddog(groups)
		if !removed {
			continue
		}
		changed = true
		if len(kept) == 0 {
			// An empty list is clutter that reads like configuration.
			delete(hooks, event)
			continue
		}
		hooks[event] = kept
	}

	if len(hooks) == 0 {
		delete(settings, "hooks")
	}
	return changed, nil
}

// Status reports what is installed, distinguishing a complete install from a
// partial one — which is worse than none, because it looks like coverage.
func Status(settings map[string]any) Status_ {
	st := Status_{}
	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		st.Missing = append([]string(nil), InstalledEvents...)
		return st
	}

	for _, event := range InstalledEvents {
		groups, _ := hooks[event].([]any)
		if path, found := birddogCommand(groups); found {
			st.Events = append(st.Events, event)
			st.BinaryPath = path
			continue
		}
		st.Missing = append(st.Missing, event)
	}

	sort.Strings(st.Events)
	switch {
	case len(st.Missing) == 0:
		st.Installed = true
	case len(st.Events) > 0:
		st.Partial = true
	}
	return st
}

func hooksMap(settings map[string]any) (map[string]any, error) {
	raw, present := settings["hooks"]
	if !present {
		return map[string]any{}, nil
	}
	hooks, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf(`settings "hooks" is not an object; refusing to rewrite it`)
	}
	return hooks, nil
}

// withoutBirddog filters out birddog's own handlers, and any matcher group
// left with none.
func withoutBirddog(groups []any) (kept []any, removed bool) {
	for _, raw := range groups {
		group, ok := raw.(map[string]any)
		if !ok {
			kept = append(kept, raw)
			continue
		}
		handlers, ok := group["hooks"].([]any)
		if !ok {
			kept = append(kept, raw)
			continue
		}

		var keptHandlers []any
		for _, h := range handlers {
			if isBirddog(h) {
				removed = true
				continue
			}
			keptHandlers = append(keptHandlers, h)
		}
		if len(keptHandlers) == 0 {
			continue // the group existed only for birddog
		}
		group["hooks"] = keptHandlers
		kept = append(kept, group)
	}
	return kept, removed
}

// isBirddog recognises a handler this package installed, by the command it
// runs rather than by where the binary happens to live.
func isBirddog(handler any) bool {
	h, ok := handler.(map[string]any)
	if !ok {
		return false
	}
	command, ok := h["command"].(string)
	if !ok {
		return false
	}
	return len(command) > len(marker) && command[len(command)-len(marker):] == marker &&
		endsWithBinary(command[:len(command)-len(marker)])
}

// endsWithBinary checks the command's program is birddog, so an unrelated
// hook whose command happens to end in " hook" is left alone.
func endsWithBinary(path string) bool {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[i+1:] == "birddog"
		}
	}
	return path == "birddog"
}

func birddogCommand(groups []any) (string, bool) {
	for _, raw := range groups {
		group, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		handlers, _ := group["hooks"].([]any)
		for _, h := range handlers {
			if !isBirddog(h) {
				continue
			}
			command := h.(map[string]any)["command"].(string)
			return command[:len(command)-len(marker)], true
		}
	}
	return "", false
}

// matchesInstall reports whether the document already contains exactly this
// install and nothing stale.
func matchesInstall(hooks map[string]any, command string) bool {
	for _, event := range InstalledEvents {
		groups, _ := hooks[event].([]any)
		found := false
		for _, raw := range groups {
			group, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			handlers, _ := group["hooks"].([]any)
			for _, h := range handlers {
				m, ok := h.(map[string]any)
				if ok && m["command"] == command {
					found = true
				}
			}
		}
		if !found {
			return false
		}
	}
	return true
}
