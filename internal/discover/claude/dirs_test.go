package claude

import (
	"os"
	"path/filepath"
	"testing"
)

func writeRegistry(t *testing.T, dir string, pid int, sessionID string, updated int64) {
	t.Helper()
	body := `{"pid": ` + itoaN(pid) + `, "sessionId": "` + sessionID + `", "procStart": "P", "updatedAt": ` + itoaN(int(updated)) + `}`
	if err := os.WriteFile(filepath.Join(dir, itoaN(pid)+".json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func itoaN(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for ; n > 0; n /= 10 {
		s = string(rune('0'+n%10)) + s
	}
	return s
}

// Each profile keeps its own registry. A session recorded in only one of them
// is still a session, and one recorded in two is one session.
func TestListReadsEveryProfilesRegistry(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	writeRegistry(t, a, 10, "only-in-a", 1)
	writeRegistry(t, b, 20, "only-in-b", 1)
	writeRegistry(t, a, 30, "old", 1)
	writeRegistry(t, b, 30, "new", 2)

	got, err := List(Params{
		RegistryDirs: []string{a, b, filepath.Join(a, "missing")},
		Probe:        func(string) bool { return true },
		SameProcess:  func(int, string) bool { return true },
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	ids := map[string]string{}
	for _, s := range got {
		ids[s.SessionID] = s.Dir
	}
	if len(got) != 3 || ids["only-in-a"] != a || ids["only-in-b"] != b || ids["new"] != b {
		t.Errorf("sessions = %v, want one per process with the newest record", ids)
	}
}

func TestListFailsOnlyWhenNoRegistryIsReadable(t *testing.T) {
	if _, err := List(Params{RegistryDirs: []string{filepath.Join(t.TempDir(), "nope")}}); err == nil {
		t.Error("List = nil error with no readable registry")
	}
}
