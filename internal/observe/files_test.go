package observe

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func touch(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestWatcherReportsTheNewestChangeAmongItsFiles(t *testing.T) {
	dir := t.TempDir()
	older, newer := filepath.Join(dir, "a.log"), filepath.Join(dir, "b.log")
	touch(t, older, "one")
	touch(t, newer, "two")

	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(older, past, past); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	w := NewWatcher([]string{older, newer})
	got := w.Poll()
	if got.LastChangeAt.Before(time.Now().Add(-time.Minute)) {
		t.Errorf("LastChangeAt = %v, want the newer file's time", got.LastChangeAt)
	}
}

// Growth is activity. A build writing to its log is work happening, even
// though nothing about the agent process changed.
func TestWatcherSeesAFileGrow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "build.log")
	touch(t, path, "start")

	w := NewWatcher([]string{path})
	first := w.Poll()

	touch(t, path, "start\nmore output")
	second := w.Poll()

	if !second.Changed {
		t.Error("Changed = false after the file grew")
	}
	if !second.LastChangeAt.After(first.LastChangeAt) && second.Size == first.Size {
		t.Errorf("growth not noticed: %+v then %+v", first, second)
	}
}

// Criterion 9: truncation must read as a change, not as nothing happening.
// A log rotated in place shrinks, and its timestamp may not move much.
func TestWatcherNoticesTruncation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rotating.log")
	touch(t, path, "a long line of previous output")

	w := NewWatcher([]string{path})
	_ = w.Poll()

	touch(t, path, "") // rotated in place
	got := w.Poll()

	if !got.Changed {
		t.Error("Changed = false after truncation — a rotated log looked like silence")
	}
	if got.Size != 0 {
		t.Errorf("Size = %d, want 0 after truncation", got.Size)
	}
}

// Criterion 9: replacing the file, as logrotate does, must also read as change.
func TestWatcherNoticesReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rotating.log")
	touch(t, path, "first generation")

	w := NewWatcher([]string{path})
	_ = w.Poll()

	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	touch(t, path, "second generation")

	if got := w.Poll(); !got.Changed {
		t.Error("Changed = false after the file was replaced")
	}
}

// Criterion 9: an unreadable path is reported as unavailable, never as quiet.
// Treating "I cannot look" as "nothing happened" is the error the whole
// evidence model exists to avoid.
func TestWatcherReportsAnInaccessiblePathAsUnavailable(t *testing.T) {
	w := NewWatcher([]string{filepath.Join(t.TempDir(), "never-existed.log")})

	got := w.Poll()
	if got.Available {
		t.Error("Available = true for a path that cannot be read")
	}
	if got.Changed {
		t.Error("Changed = true for a path that cannot be read")
	}
}

func TestWatcherIsAvailableWhenAnyWatchedPathCanBeRead(t *testing.T) {
	dir := t.TempDir()
	readable := filepath.Join(dir, "a.log")
	touch(t, readable, "x")

	w := NewWatcher([]string{readable, filepath.Join(dir, "missing.log")})
	got := w.Poll()
	if !got.Available {
		t.Error("Available = false though one watched path is readable")
	}
	if len(got.Unavailable) != 1 {
		t.Errorf("Unavailable = %v, want the missing path named", got.Unavailable)
	}
}

// Criterion 9: paths containing spaces must behave like any other.
func TestWatcherHandlesPathsContainingSpaces(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a directory with spaces")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "a log with spaces.log")
	touch(t, path, "one")

	w := NewWatcher([]string{path})
	if got := w.Poll(); !got.Available {
		t.Fatalf("Available = false for a path with spaces: %+v", got)
	}

	touch(t, path, "one\ntwo")
	if got := w.Poll(); !got.Changed {
		t.Error("Changed = false after a path with spaces grew")
	}
}

// A file that has not changed is not a change. Otherwise every poll would look
// like activity and nothing would ever be reported quiet.
func TestUnchangedFileIsNotAChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "still.log")
	touch(t, path, "unchanging")

	w := NewWatcher([]string{path})
	_ = w.Poll()

	if got := w.Poll(); got.Changed {
		t.Error("Changed = true though nothing was written")
	}
}

func TestWatcherWithNoPathsIsSimplyUnused(t *testing.T) {
	got := NewWatcher(nil).Poll()
	if got.Changed {
		t.Error("Changed = true with nothing to watch")
	}
	if got.Available {
		t.Error("Available = true with nothing to watch")
	}
}
