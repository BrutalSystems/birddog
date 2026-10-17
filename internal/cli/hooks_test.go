package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BrutalSystems/birddog/internal/policy"
)

// The warning is only worth anything if it reaches the operator at the moment
// they choose the path. Checking FragilePath in isolation would pass with the
// call site removed.
func TestInstallWarnsAboutAVersionManagedPath(t *testing.T) {
	var out bytes.Buffer
	settings := filepath.Join(t.TempDir(), "settings.json")

	err := runHooksInstall([]string{
		"--dry-run",
		"--binary", "/Users/x/.asdf/installs/nodejs/24.16.0/bin/birddog",
		"--settings", settings,
	}, &out)
	if err != nil {
		t.Fatalf("hooks install: %v", err)
	}

	if !contains(out.String(), "may not keep working") {
		t.Errorf("install said nothing about a version-managed path:\n%s", out.String())
	}
}

// And must stay silent for a path that does not move, or it is noise and gets
// ignored when it matters.
func TestInstallDoesNotWarnAboutAStablePath(t *testing.T) {
	var out bytes.Buffer
	settings := filepath.Join(t.TempDir(), "settings.json")

	err := runHooksInstall([]string{
		"--dry-run",
		"--binary", "/usr/local/bin/birddog",
		"--settings", settings,
	}, &out)
	if err != nil {
		t.Fatalf("hooks install: %v", err)
	}

	if contains(out.String(), "may not keep working") {
		t.Errorf("install warned about a stable path:\n%s", out.String())
	}
}

// A dry run must not write anything, including while warning about what it
// would have written.
func TestAWarnedDryRunStillWritesNothing(t *testing.T) {
	var out bytes.Buffer
	settings := filepath.Join(t.TempDir(), "settings.json")

	if err := runHooksInstall([]string{
		"--dry-run",
		"--binary", "/Users/x/.nvm/versions/node/v22.11.0/bin/birddog",
		"--settings", settings,
	}, &out); err != nil {
		t.Fatalf("hooks install: %v", err)
	}

	if _, err := readSettings(settings); err != nil {
		t.Fatalf("readSettings: %v", err)
	}
	settingsWritten, err := filepath.Glob(settings + "*")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(settingsWritten) != 0 {
		t.Errorf("dry run wrote %v", settingsWritten)
	}
}

// installJSON runs `hooks install` with --json and decodes what it reported.
func installJSON(t *testing.T, args ...string) HooksResult {
	t.Helper()
	var out bytes.Buffer
	if err := runHooksInstall(append(args, "--json"), &out); err != nil {
		t.Fatalf("hooks install %v: %v", args, err)
	}
	var result HooksResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("decode %s: %v", out.String(), err)
	}
	return result
}

// executableBinary makes a file that CheckHandler accepts as a handler.
func executableBinary(t *testing.T) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "birddog")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe
}

// The operator runs several Claude Code profiles, each reading its own
// settings.json from CLAUDE_CONFIG_DIR. Installing into ~/.claude for a
// session that reads another file reports success and observes nothing.
func TestSettingsPathFollowsClaudeConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "~/.claude-work")

	result := installJSON(t, "--dry-run", "--binary", executableBinary(t))

	want := filepath.Join(home, ".claude-work", "settings.json")
	if result.SettingsPath != want {
		t.Errorf("settings_path = %q, want %q", result.SettingsPath, want)
	}
	if result.SettingsSource != "CLAUDE_CONFIG_DIR" {
		t.Errorf("settings_source = %q, want CLAUDE_CONFIG_DIR", result.SettingsSource)
	}
}

// Set but empty is the same as unset: an empty directory name would resolve
// to ./settings.json, wherever the command happened to run.
func TestEmptyClaudeConfigDirFallsBackToHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	path, source, err := resolveSettingsPath("", "")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".claude", "settings.json"); path != want || source != "default" {
		t.Errorf("got %q from %q, want %q from default", path, source, want)
	}
}

// --config-dir names a profile without exporting anything, which is what a
// loop over every profile needs. It outranks the environment.
func TestConfigDirFlagNamesThatProfilesSettings(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	dir := t.TempDir()

	result := installJSON(t, "--dry-run", "--binary", executableBinary(t), "--config-dir", dir)

	if want := filepath.Join(dir, "settings.json"); result.SettingsPath != want {
		t.Errorf("settings_path = %q, want %q", result.SettingsPath, want)
	}
	if result.SettingsSource != "config_dir_flag" {
		t.Errorf("settings_source = %q, want config_dir_flag", result.SettingsSource)
	}
}

func TestSettingsFlagReportsItsSource(t *testing.T) {
	settings := filepath.Join(t.TempDir(), "settings.json")
	result := installJSON(t, "--dry-run", "--binary", executableBinary(t), "--settings", settings)
	if result.SettingsPath != settings || result.SettingsSource != "flag" {
		t.Errorf("got %q from %q, want %q from flag", result.SettingsPath, result.SettingsSource, settings)
	}
}

// Two flags naming two files is a mistake, not a precedence question, and on
// every subcommand it is a usage error.
func TestSettingsAndConfigDirAreMutuallyExclusive(t *testing.T) {
	for _, sub := range []string{"install", "status", "uninstall"} {
		var stdout, stderr bytes.Buffer
		code := Main([]string{"hooks", sub,
			"--settings", filepath.Join(t.TempDir(), "settings.json"),
			"--config-dir", t.TempDir(),
		}, &stdout, &stderr)
		if code != ExitUsage {
			t.Errorf("hooks %s: exit %d, want %d", sub, code, ExitUsage)
		}
		if !contains(stderr.String(), "--config-dir") {
			t.Errorf("hooks %s: stderr does not say why: %q", sub, stderr.String())
		}
	}
}

// The settings file is the operator's, and much of it is not birddog's. An
// install followed by an uninstall must leave every other key exactly as it
// was — including a large integer, which decoding as float64 would turn into
// 1e+22, and a command whose && would come back HTML-escaped.
func TestInstallAndUninstallPreserveUnrelatedKeys(t *testing.T) {
	settings := filepath.Join(t.TempDir(), "settings.json")
	original := `{
  "cleanupPeriodDays": 12345678901234567890123,
  "ratio": 0.1,
  "env": {"FOO": "bar", "nested": {"deep": [1, 2, {"three": 3}]}},
  "hooks": {
    "PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "a && b > /dev/null"}]}]
  }
}
`
	if err := os.WriteFile(settings, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	want, err := readSettings(settings)
	if err != nil {
		t.Fatal(err)
	}

	if r := installJSON(t, "--binary", executableBinary(t), "--settings", settings); !r.Changed {
		t.Fatalf("install reported no change")
	}
	var out bytes.Buffer
	if err := runHooksUninstall([]string{"--settings", settings}, &out); err != nil {
		t.Fatalf("hooks uninstall: %v", err)
	}

	raw, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	for _, literal := range []string{"12345678901234567890123", "0.1", "a && b > /dev/null"} {
		if !strings.Contains(string(raw), literal) {
			t.Errorf("%q did not survive install and uninstall:\n%s", literal, raw)
		}
	}
	got, err := readSettings(settings)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("settings changed across install and uninstall:\n got %v\nwant %v", got, want)
	}
}

// Safe to run twice: the second install finds the file already correct, says
// so, and touches nothing — no rewrite and no new backup over the first one.
func TestSecondInstallWritesNothing(t *testing.T) {
	settings := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(settings, []byte(`{"model": "opus"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	exe := executableBinary(t)

	first := installJSON(t, "--binary", exe, "--settings", settings)
	if !first.Changed || first.BackupPath != settings+".birddog-backup" {
		t.Fatalf("first install: changed=%v backup=%q", first.Changed, first.BackupPath)
	}
	if err := os.Remove(first.BackupPath); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}

	second := installJSON(t, "--binary", exe, "--settings", settings)
	if second.Changed || second.BackupPath != "" {
		t.Errorf("second install: changed=%v backup=%q, want no change", second.Changed, second.BackupPath)
	}
	after, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("second install rewrote the file:\n%s\n---\n%s", before, after)
	}
	if _, err := os.Stat(first.BackupPath); !os.IsNotExist(err) {
		t.Errorf("second install wrote a backup")
	}
}

// Install used to report handler_runnable: false for every binary, because it
// never checked. A binary that exists and is executable is runnable.
func TestInstallDryRunReportsARunnableHandler(t *testing.T) {
	settings := filepath.Join(t.TempDir(), "settings.json")
	result := installJSON(t, "--dry-run", "--binary", executableBinary(t), "--settings", settings)
	if !result.HandlerRunnable || result.HandlerProblem != "" {
		t.Errorf("handler_runnable=%v problem=%q, want runnable", result.HandlerRunnable, result.HandlerProblem)
	}
}

// And one that is not there is reported, with the reason, in both outputs —
// but installed anyway: the operator may be about to put it in place.
func TestInstallDryRunReportsAMissingHandler(t *testing.T) {
	settings := filepath.Join(t.TempDir(), "settings.json")
	missing := filepath.Join(t.TempDir(), "gone", "birddog")

	result := installJSON(t, "--dry-run", "--binary", missing, "--settings", settings)
	if result.HandlerRunnable {
		t.Errorf("handler_runnable = true for a missing binary")
	}
	if !contains(result.HandlerProblem, missing) {
		t.Errorf("handler_problem does not name the binary: %q", result.HandlerProblem)
	}
	if !result.Changed {
		t.Errorf("install refused a missing binary; it should warn and go ahead")
	}

	var out bytes.Buffer
	if err := runHooksInstall([]string{"--dry-run", "--binary", missing, "--settings", settings}, &out); err != nil {
		t.Fatalf("hooks install: %v", err)
	}
	if !contains(out.String(), "cannot run") || !contains(out.String(), missing) {
		t.Errorf("text output does not say the handler cannot run:\n%s", out.String())
	}
}

// doctor must read the settings install wrote. Otherwise a profile with hooks
// installed is reported as having none, and the two commands disagree.
func TestDoctorReadsTheProfilesSettings(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("BIRDDOG_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())

	installJSON(t, "--binary", executableBinary(t))

	for _, r := range instrumentationReports() {
		if r.Mechanism != "claude_hooks" {
			continue
		}
		if r.State != policy.InstrumentInstalled {
			t.Errorf("claude_hooks state = %q, want %q: %s", r.State, policy.InstrumentInstalled, r.Detail)
		}
		return
	}
	t.Fatal("no claude_hooks report")
}
