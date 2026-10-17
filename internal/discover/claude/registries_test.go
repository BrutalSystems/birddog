package claude

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func writeSession(t *testing.T, dir string, pid int, sessionID, procStart string, updated int64) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"pid": %d, "sessionId": %q, "procStart": %q, "messagingSocketPath": "/s/%d", "updatedAt": %d}`,
		pid, sessionID, procStart, pid, updated)
	path := filepath.Join(dir, fmt.Sprintf("%d.json", pid))
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func everyoneLive() (func(string) bool, func(int, string) bool) {
	return func(string) bool { return true }, func(int, string) bool { return true }
}

func TestRegistriesAreTheDefinedProfilesAndNothingElse(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude-work"))
	t.Setenv(ConfigDirsEnv, "~/.claude-home:"+filepath.Join(home, ".claude-work")+": :relative/dir")

	// A backup and a link that a pattern would have matched.
	_ = os.MkdirAll(filepath.Join(home, ".claude-backup", "sessions"), 0o700)
	_ = os.MkdirAll(filepath.Join(home, ".claude-home"), 0o700)
	_ = os.Symlink(filepath.Join(home, ".claude-home"), filepath.Join(home, ".claude-link"))

	got := Registries()
	want := []string{
		filepath.Join(home, ".claude", "sessions"), // listed though it does not exist yet
		filepath.Join(home, ".claude-work", "sessions"),
		filepath.Join(home, ".claude-home", "sessions"),
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("Registries() = %v, want %v", got, want)
	}
}

func TestRegistriesSkipALinkedProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	_ = os.MkdirAll(filepath.Join(home, ".claude-real", "sessions"), 0o700)
	_ = os.Symlink(filepath.Join(home, ".claude-real"), filepath.Join(home, ".claude-alias"))
	t.Setenv(ConfigDirsEnv, filepath.Join(home, ".claude-alias"))

	for _, dir := range Registries() {
		if filepath.Base(filepath.Dir(dir)) == ".claude-alias" {
			t.Errorf("Registries() includes %s, reached through a link", dir)
		}
	}
}

// A daemon runs for days. A registry created after it started is read.
func TestListSeesARegistryCreatedLater(t *testing.T) {
	root := t.TempDir()
	later := filepath.Join(root, "later", "sessions")
	probe, same := everyoneLive()
	p := Params{Registries: func() []string { return []string{later} }, Probe: probe, SameProcess: same}

	if got, err := List(p); err != nil || len(got) != 0 {
		t.Fatalf("List before the registry exists = %v, %v; want nothing and no error", got, err)
	}
	writeSession(t, later, 10, "s", "P", 1)
	if got, err := List(p); err != nil || len(got) != 1 {
		t.Errorf("List after it was created = %v, %v; want the session", got, err)
	}
}

// One process is one session, identified by pid and start time. The live
// record wins over a newer stale one, and a reused pid is a different process.
func TestListDeduplicatesByProcessAndPrefersTheLiveRecord(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	writeSession(t, a, 10, "live", "P", 1)
	stale := `{"pid": 10, "sessionId": "stale-copy", "procStart": "P", "messagingSocketPath": "/dead", "updatedAt": 99}`
	if err := os.WriteFile(filepath.Join(b, "10.json"), []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	writeSession(t, a, 20, "first-run", "OLD", 1)
	writeSession(t, b, 20, "pid-reused", "NEW", 1)

	got, err := List(Params{
		Registries: func() []string { return []string{a, b} },
		// The copy's socket does not answer; it is newer, and not live.
		Probe:       func(socket string) bool { return socket != "/dead" },
		SameProcess: func(int, string) bool { return true },
	})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, s := range got {
		ids[s.SessionID] = true
	}
	if len(got) != 3 || !ids["live"] || ids["stale-copy"] || !ids["first-run"] || !ids["pid-reused"] {
		t.Errorf("sessions = %v, want live, first-run and pid-reused", ids)
	}
}

// A record reached through a link belongs to the directory it lives in, where
// its credential file is; and the real record is preferred to the link.
func TestALinkedRecordBelongsToItsRealDirectory(t *testing.T) {
	real, mirror := t.TempDir(), t.TempDir()
	path := writeSession(t, real, 10, "s", "P", 1)
	if err := os.Symlink(path, filepath.Join(mirror, "10.json")); err != nil {
		t.Fatal(err)
	}
	probe, same := everyoneLive()

	only, err := List(Params{Registries: func() []string { return []string{mirror} }, Probe: probe, SameProcess: same})
	if err != nil || len(only) != 1 {
		t.Fatalf("List = %v, %v", only, err)
	}
	if want, _ := filepath.EvalSymlinks(real); only[0].Dir != want {
		t.Errorf("Dir = %q, want the real directory %q", only[0].Dir, want)
	}

	both, _ := List(Params{Registries: func() []string { return []string{mirror, real} }, Probe: probe, SameProcess: same})
	if len(both) != 1 || both[0].linked {
		t.Errorf("sessions = %+v, want one, read from the real record", both)
	}
}

// A registry that exists and cannot be read makes the listing partial, not
// empty and not complete.
func TestAnUnreadableRegistryMakesTheListingPartial(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads everything")
	}
	good, bad := t.TempDir(), t.TempDir()
	writeSession(t, good, 10, "s", "P", 1)
	if err := os.Chmod(bad, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(bad, 0o700) })
	probe, same := everyoneLive()

	got, err := List(Params{Registries: func() []string { return []string{good, bad} }, Probe: probe, SameProcess: same})
	if !IsPartial(err) || len(got) != 1 {
		t.Errorf("List = %v, %v; want the readable session and a partial error", got, err)
	}
	if _, err := List(Params{Registries: func() []string { return []string{bad} }, Probe: probe, SameProcess: same}); err == nil {
		t.Error("List of only an unreadable registry = nil error")
	}
}

// A record in a readable registry that cannot itself be read is a session the
// listing cannot rule out, so the listing is partial. One that vanished
// between the directory listing and the read — or a link to a record that is
// gone — describes nothing and is skipped.
func TestAnUnreadableRecordMakesTheListingPartial(t *testing.T) {
	probe, same := everyoneLive()
	for name, setup := range map[string]func(t *testing.T, dir string){
		"bad json": func(t *testing.T, dir string) {
			_ = os.WriteFile(filepath.Join(dir, "20.json"), []byte(`{"pid": 20, "sessionId": `), 0o600)
		},
		"unreadable file": func(t *testing.T, dir string) {
			if os.Getuid() == 0 {
				t.Skip("root reads everything")
			}
			path := writeSession(t, dir, 20, "owner", "P", 1)
			_ = os.Chmod(path, 0)
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeSession(t, dir, 10, "other", "P", 1)
			setup(t, dir)
			for _, p := range []Params{
				{RegistryDir: dir, Probe: probe, SameProcess: same},
				{Registries: func() []string { return []string{dir} }, Probe: probe, SameProcess: same},
			} {
				got, err := List(p)
				if !IsPartial(err) || len(got) != 1 {
					t.Errorf("List = %v, %v; want the readable session and a partial error", got, err)
				}
			}
		})
	}

	// A link whose record is missing is skipped only when the pid in its
	// name is not running. A link to a missing record for a running pid may
	// be a session behind a renamed or unmounted directory, so the listing
	// is partial.
	for name, alive := range map[string]bool{"dangling link, pid not running": false, "dangling link, pid running": true} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeSession(t, dir, 10, "other", "P", 1)
			_ = os.Symlink(filepath.Join(dir, "elsewhere", "30.json"), filepath.Join(dir, "30.json"))
			got, err := List(Params{RegistryDir: dir, Probe: probe, SameProcess: same,
				PIDAlive: func(pid int) bool { return pid == 30 && alive }})
			if len(got) != 1 {
				t.Errorf("sessions = %v, want the readable one", got)
			}
			if IsPartial(err) != alive {
				t.Errorf("List error = %v; want partial %v", err, alive)
			}
		})
	}
}

// The race the review named: a link resolved, then its record removed before
// the read. With the link itself still there and its pid running, that is not
// evidence of an ended session.
func TestVanishedNeedsEvidence(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "30.json")
	_ = os.Symlink(filepath.Join(dir, "gone.json"), link)

	if gone, _ := vanished(link, true, "30.json", func(int) bool { return true }); gone {
		t.Error("a link that still exists, for a running pid, was taken as gone")
	}
	if gone, _ := vanished(link, true, "30.json", func(int) bool { return false }); !gone {
		t.Error("a link for a pid nothing holds was not taken as gone")
	}
	_ = os.Remove(link)
	if gone, _ := vanished(link, true, "30.json", func(int) bool { return true }); !gone {
		t.Error("an entry removed during the scan was not taken as gone")
	}
}
