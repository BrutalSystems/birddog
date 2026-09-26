package cli

import (
	"bytes"
	"path/filepath"
	"testing"
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
