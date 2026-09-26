package hooks

import (
	"encoding/json"
	"strings"
	"testing"
)

func parse(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("parse: %v", err)
	}
	return m
}

func render(t *testing.T, m map[string]any) string {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return string(b)
}

func TestInstallAddsBirddogHooks(t *testing.T) {
	settings := parse(t, `{}`)
	changed, err := Install(settings, "/usr/local/bin/birddog")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !changed {
		t.Error("changed = false on a fresh install")
	}
	out := render(t, settings)
	for _, event := range InstalledEvents {
		if !strings.Contains(out, event) {
			t.Errorf("settings missing %q:\n%s", event, out)
		}
	}
}

// The single most important property: an operator's existing configuration
// must survive untouched. Overwriting somebody's hooks to install monitoring
// would be the worst thing this command could do.
func TestInstallPreservesEverythingElse(t *testing.T) {
	settings := parse(t, `{
		"theme": "dark",
		"permissions": {"allow": ["Bash(ls:*)"]},
		"hooks": {
			"PreToolUse": [
				{"matcher": "Bash", "hooks": [{"type": "command", "command": "/theirs/guard.sh"}]}
			],
			"SessionStart": [
				{"hooks": [{"type": "command", "command": "/theirs/setup.sh"}]}
			]
		}
	}`)

	if _, err := Install(settings, "/usr/local/bin/birddog"); err != nil {
		t.Fatalf("Install: %v", err)
	}

	out := render(t, settings)
	for _, keep := range []string{"dark", "Bash(ls:*)", "/theirs/guard.sh", "/theirs/setup.sh", "SessionStart"} {
		if !strings.Contains(out, keep) {
			t.Errorf("install destroyed %q:\n%s", keep, out)
		}
	}
}

// Their PreToolUse hook and ours must both run, in their original order.
func TestInstallAppendsRatherThanReplacing(t *testing.T) {
	settings := parse(t, `{"hooks": {"PreToolUse": [
		{"matcher": "Bash", "hooks": [{"type": "command", "command": "/theirs/guard.sh"}]}
	]}}`)

	if _, err := Install(settings, "/usr/local/bin/birddog"); err != nil {
		t.Fatalf("Install: %v", err)
	}

	groups, _ := settings["hooks"].(map[string]any)["PreToolUse"].([]any)
	if len(groups) != 2 {
		t.Fatalf("PreToolUse has %d groups, want theirs plus ours", len(groups))
	}
	first := render(t, groups[0].(map[string]any))
	if !strings.Contains(first, "/theirs/guard.sh") {
		t.Errorf("their hook is no longer first: %s", first)
	}
}

func TestInstallIsIdempotent(t *testing.T) {
	settings := parse(t, `{}`)
	if _, err := Install(settings, "/usr/local/bin/birddog"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	before := render(t, settings)

	changed, err := Install(settings, "/usr/local/bin/birddog")
	if err != nil {
		t.Fatalf("second Install: %v", err)
	}
	if changed {
		t.Error("changed = true on a second install")
	}
	if render(t, settings) != before {
		t.Error("a second install altered the settings")
	}
}

// A moved or rebuilt binary has to be re-pointed, not duplicated.
func TestInstallUpdatesAChangedBinaryPath(t *testing.T) {
	settings := parse(t, `{}`)
	_, _ = Install(settings, "/old/birddog")

	changed, err := Install(settings, "/new/birddog")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !changed {
		t.Error("changed = false though the binary moved")
	}
	out := render(t, settings)
	if strings.Contains(out, "/old/birddog") {
		t.Errorf("the old path is still configured:\n%s", out)
	}
	if strings.Count(out, "/new/birddog") != len(InstalledEvents) {
		t.Errorf("expected one entry per event:\n%s", out)
	}
}

func TestUninstallRemovesOnlyBirddogHooks(t *testing.T) {
	settings := parse(t, `{"hooks": {"PreToolUse": [
		{"matcher": "Bash", "hooks": [{"type": "command", "command": "/theirs/guard.sh"}]}
	]}}`)
	_, _ = Install(settings, "/usr/local/bin/birddog")

	changed, err := Uninstall(settings)
	if err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if !changed {
		t.Error("changed = false though birddog was installed")
	}

	out := render(t, settings)
	if strings.Contains(out, "birddog") {
		t.Errorf("birddog hooks remain:\n%s", out)
	}
	if !strings.Contains(out, "/theirs/guard.sh") {
		t.Errorf("uninstall removed somebody else's hook:\n%s", out)
	}
}

func TestUninstallOnACleanConfigChangesNothing(t *testing.T) {
	settings := parse(t, `{"theme": "dark"}`)
	changed, err := Uninstall(settings)
	if err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if changed {
		t.Error("changed = true with nothing installed")
	}
}

// An empty event list left behind is clutter that looks like configuration.
func TestUninstallTidiesEmptiedEvents(t *testing.T) {
	settings := parse(t, `{}`)
	_, _ = Install(settings, "/usr/local/bin/birddog")
	_, _ = Uninstall(settings)

	hooks, ok := settings["hooks"].(map[string]any)
	if ok && len(hooks) != 0 {
		t.Errorf("empty event lists left behind: %v", hooks)
	}
}

func TestStatusReportsWhatIsInstalled(t *testing.T) {
	settings := parse(t, `{}`)
	if st := Status(settings); st.Installed {
		t.Error("Installed = true before installing")
	}

	_, _ = Install(settings, "/usr/local/bin/birddog")
	st := Status(settings)
	if !st.Installed {
		t.Error("Installed = false after installing")
	}
	if st.BinaryPath != "/usr/local/bin/birddog" {
		t.Errorf("BinaryPath = %q", st.BinaryPath)
	}
	if len(st.Events) != len(InstalledEvents) {
		t.Errorf("Events = %v", st.Events)
	}
}

// A partial install — some events present, some not — must be reported as
// partial rather than as installed.
func TestStatusReportsAPartialInstall(t *testing.T) {
	settings := parse(t, `{}`)
	_, _ = Install(settings, "/usr/local/bin/birddog")

	hooks := settings["hooks"].(map[string]any)
	delete(hooks, InstalledEvents[0])

	st := Status(settings)
	if st.Installed {
		t.Error("Installed = true for a partial install")
	}
	if !st.Partial {
		t.Error("Partial = false for a partial install")
	}
}

// Every configured handler must run birddog's own hook command, and nothing
// else. A hook that could block is the one thing this must never install.
func TestInstalledHandlersOnlyRunTheHookCommand(t *testing.T) {
	settings := parse(t, `{}`)
	_, _ = Install(settings, "/usr/local/bin/birddog")

	hooks := settings["hooks"].(map[string]any)
	for event, raw := range hooks {
		for _, group := range raw.([]any) {
			for _, h := range group.(map[string]any)["hooks"].([]any) {
				handler := h.(map[string]any)
				if handler["type"] != "command" {
					t.Errorf("%s: handler type = %v", event, handler["type"])
				}
				if got := handler["command"].(string); got != "/usr/local/bin/birddog hook" {
					t.Errorf("%s: command = %q", event, got)
				}
			}
		}
	}
}
