package hooks

import (
	"os"
	"path/filepath"
	"testing"
)

// The case that prompted this: the handler path in settings.json pointed at a
// binary that had been removed. Every hook fired and every one failed, and
// nothing birddog reported said so — `hooks status` called the installation
// complete, because the entries were all present.
//
// A registered handler that cannot run is worse than an absent one: coverage
// looks complete while none of it works.
func TestADanglingHandlerIsReportedAsUnrunnable(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "gone")
	link := filepath.Join(dir, "birddog")
	if err := os.WriteFile(target, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := os.Remove(target); err != nil {
		t.Fatalf("remove: %v", err)
	}

	got := CheckHandler(link)
	if got.Runnable {
		t.Error("a dangling symlink was reported as runnable")
	}
	if got.Problem == "" {
		t.Error("no problem was described, so nothing tells an operator what to fix")
	}
}

func TestAMissingHandlerIsReportedAsUnrunnable(t *testing.T) {
	got := CheckHandler(filepath.Join(t.TempDir(), "never-existed"))

	if got.Runnable || got.Problem == "" {
		t.Errorf("CheckHandler on a missing path = %+v, want an explained failure", got)
	}
}

// Present but not executable is its own failure, and a different fix.
func TestANonExecutableHandlerIsReportedSeparately(t *testing.T) {
	path := filepath.Join(t.TempDir(), "birddog")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := CheckHandler(path)
	if got.Runnable {
		t.Error("a non-executable handler was reported as runnable")
	}
	if got.Problem == "" {
		t.Error("no problem was described")
	}
}

func TestAWorkingHandlerReportsNoProblem(t *testing.T) {
	path := filepath.Join(t.TempDir(), "birddog")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := CheckHandler(path)
	if !got.Runnable || got.Problem != "" {
		t.Errorf("CheckHandler on a working handler = %+v, want it clean", got)
	}
}

// Nothing installed is not a fault, and must not be reported as one.
func TestAnAbsentInstallationIsNotAProblem(t *testing.T) {
	got := CheckHandler("")

	if got.Problem != "" {
		t.Errorf("Problem = %q, want none when no handler is installed", got.Problem)
	}
}
