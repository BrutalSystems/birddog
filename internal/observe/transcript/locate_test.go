// internal/observe/transcript/locate_test.go
package transcript

import (
	"os"
	"path/filepath"
	"testing"
)

// projects builds one projects directory containing the named files.
func projects(t *testing.T, files ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, f := range files {
		p := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestLocateFindsTheTranscript(t *testing.T) {
	dir := projects(t, "-Users-me-work/abc123.jsonl")
	got, err := Locate([]string{dir}, "abc123")
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if filepath.Base(got) != "abc123.jsonl" {
		t.Errorf("Locate = %q, want .../abc123.jsonl", got)
	}
}

// The case that matters on a multi-profile machine: the session's transcript
// is under a different profile than the first directory searched.
func TestLocateSearchesEveryDirectory(t *testing.T) {
	a := projects(t, "-proj/other.jsonl")
	b := projects(t, "-proj/abc123.jsonl")
	got, err := Locate([]string{a, b}, "abc123")
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if filepath.Base(got) != "abc123.jsonl" {
		t.Errorf("Locate = %q, want the transcript in the second directory", got)
	}
}

// A directory that does not exist is not a failure: a profile may have no
// transcripts yet, and the others must still be searched.
func TestLocateSkipsAMissingDirectory(t *testing.T) {
	b := projects(t, "-proj/abc123.jsonl")
	got, err := Locate([]string{filepath.Join(t.TempDir(), "gone"), b}, "abc123")
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if got == "" {
		t.Error("Locate = empty, want the transcript from the directory that exists")
	}
}

func TestLocateReportsNothingWhenAbsent(t *testing.T) {
	dir := projects(t, "-Users-me-work/other.jsonl")
	got, err := Locate([]string{dir}, "abc123")
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if got != "" {
		t.Errorf("Locate = %q, want empty", got)
	}
}

func TestLocateWithNoDirectoriesReportsNothing(t *testing.T) {
	got, err := Locate(nil, "abc123")
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if got != "" {
		t.Errorf("Locate = %q, want empty", got)
	}
}

// Review Focus 5: an ambiguous match is an error, never an arbitrary pick.
func TestLocateRefusesAnAmbiguousMatch(t *testing.T) {
	dir := projects(t, "-Users-me-work/abc123.jsonl", "-Users-me-other/abc123.jsonl")
	if _, err := Locate([]string{dir}, "abc123"); err == nil {
		t.Error("Locate returned no error for two matching transcripts")
	}
}

func TestLocateRefusesAnAmbiguousMatchAcrossDirectories(t *testing.T) {
	a := projects(t, "-proj/abc123.jsonl")
	b := projects(t, "-proj/abc123.jsonl")
	if _, err := Locate([]string{a, b}, "abc123"); err == nil {
		t.Error("Locate returned no error for the same id under two profiles")
	}
}

func TestLocateRejectsASessionIDWithPathCharacters(t *testing.T) {
	dir := projects(t, "-Users-me-work/abc123.jsonl")
	for _, bad := range []string{"../abc123", "*", "a/b"} {
		if _, err := Locate([]string{dir}, bad); err == nil {
			t.Errorf("Locate(%q) returned no error", bad)
		}
	}
}
