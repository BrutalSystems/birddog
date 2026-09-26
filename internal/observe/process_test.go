package observe

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/BrutalSystems/birddog/internal/config"
	"github.com/BrutalSystems/birddog/internal/policy"
)

func processTarget(pid int, procStart string) config.Target {
	return config.Target{
		ID: "worker-1", Provider: "process",
		Attachment: config.Attachment{Kind: "process", PID: pid, ProcStart: procStart},
	}
}

func TestProcessObserverReportsALiveProcess(t *testing.T) {
	o := &Process{
		SameProcess: func(int, string) bool { return true },
		Descendants: func(int) ([]int, error) { return nil, nil },
	}

	got, err := o.Observe(processTarget(4242, "Mon Sep 21 15:53:11 2026"))
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !got.Live {
		t.Error("Live = false for a running process")
	}
}

// Criterion 7: the pid is in use but by a different process, so the one that
// was registered is gone. That is a verified exit, not an adoption.
func TestProcessObserverTreatsAReplacedPIDAsAnExit(t *testing.T) {
	o := &Process{
		SameProcess: func(int, string) bool { return false },
		Descendants: func(int) ([]int, error) { return nil, nil },
	}

	got, _ := o.Observe(processTarget(4242, "Mon Sep 21 15:53:11 2026"))
	if got.Status != policy.StatusExited || !got.StatusKnown {
		t.Errorf("Status = %q known=%v, want a verified exit", got.Status, got.StatusKnown)
	}
	if got.Live {
		t.Error("Live = true for a process that is gone")
	}
}

// Criterion 5, at the process level: a worker blocked on a twenty-minute
// compile is working. Its own CPU says nothing; the child is the work.
func TestProcessWithRunningChildrenIsRunningATool(t *testing.T) {
	o := &Process{
		SameProcess: func(int, string) bool { return true },
		Descendants: func(int) ([]int, error) { return []int{99, 100}, nil },
	}

	got, _ := o.Observe(processTarget(4242, "start"))
	if got.Status != policy.StatusRunningTool || !got.StatusKnown {
		t.Errorf("Status = %q known=%v, want running_tool while a child is working", got.Status, got.StatusKnown)
	}
}

// With no children and nothing else to go on, birddog does not know what the
// process is doing. Saying "idle" would be a claim; saying nothing is honest.
func TestProcessWithNoChildrenHasNoReadableState(t *testing.T) {
	o := &Process{
		SameProcess: func(int, string) bool { return true },
		Descendants: func(int) ([]int, error) { return nil, nil },
	}

	got, _ := o.Observe(processTarget(4242, "start"))
	if got.StatusKnown {
		t.Error("StatusKnown = true though nothing indicates what the process is doing")
	}
	if !got.Live {
		t.Error("Live = false though the process is running")
	}
}

// A watched file growing is activity, even when the process looks unchanged.
func TestWatchedFileActivityCountsAsActivity(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "build.log")
	touch(t, logPath, "start")

	target := processTarget(4242, "start")
	target.Observations.Logs = []string{logPath}

	o := &Process{
		SameProcess: func(int, string) bool { return true },
		Descendants: func(int) ([]int, error) { return nil, nil },
	}

	first, err := o.Observe(target)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	touch(t, logPath, "start\nprogress")
	second, _ := o.Observe(target)

	if !second.LastActivityAt.After(first.LastActivityAt) {
		t.Errorf("LastActivityAt did not advance when the watched log grew: %v then %v",
			first.LastActivityAt, second.LastActivityAt)
	}
}

// Criterion 9: an unreadable watched path is a gap in coverage, and must not
// look like a quiet worker.
func TestUnreadableWatchedFileDoesNotLookLikeSilence(t *testing.T) {
	target := processTarget(4242, "start")
	target.Observations.Files = []string{filepath.Join(t.TempDir(), "never-existed")}

	o := &Process{
		SameProcess: func(int, string) bool { return true },
		Descendants: func(int) ([]int, error) { return nil, nil },
	}

	got, err := o.Observe(target)
	if err == nil {
		t.Fatal("Observe = nil error though a watched path could not be read")
	}
	if got.Live != true {
		t.Error("Live = false though the process itself is running")
	}
}

// The same watcher must persist between passes, or every poll is a baseline
// and nothing is ever seen to change.
func TestProcessObserverKeepsItsWatchersBetweenPasses(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "a.log")
	touch(t, logPath, "one")

	target := processTarget(4242, "start")
	target.Observations.Logs = []string{logPath}

	o := &Process{
		SameProcess: func(int, string) bool { return true },
		Descendants: func(int) ([]int, error) { return nil, nil },
	}
	_, _ = o.Observe(target)

	if err := os.WriteFile(logPath, []byte("one\ntwo"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, ok := o.watchers[target.ID]; !ok {
		t.Fatal("no watcher retained for the target")
	}
}

func TestProcessObserverRequiresAProcessIdentity(t *testing.T) {
	o := &Process{}
	if _, err := o.Observe(processTarget(0, "")); err == nil {
		t.Error("Observe = nil error with no pid, want it required")
	}
}

// Review Focus 3. pollFiles returns an empty FileActivity when a target names no
// files or logs, and the adapter reads as though it always has a signal.
func TestProcessWatchingNothingReportsUnavailableResolution(t *testing.T) {
	o := &Process{
		SameProcess: func(int, string) bool { return true },
		Descendants: func(int) ([]int, error) { return nil, nil },
	}

	got, err := o.Observe(processTarget(4242, "Mon Sep 21 15:53:11 2026"))
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if got.ActivityResolution != policy.ResolutionUnavailable {
		t.Errorf("ActivityResolution = %q, want unavailable: the target names nothing to watch",
			got.ActivityResolution)
	}
}

// Review Focus 3b, and spec D-2's stability claim. A watched path that does not
// exist yet — a build log before the build starts — leaves LastChangeAt zero
// while the target is still being watched at activity resolution. A zero-check
// would flip the resolution the moment the file appeared, when nothing about
// what is watching had changed.
func TestProcessWatchingAPathThatDoesNotExistYetStillReportsActivity(t *testing.T) {
	target := processTarget(4242, "Mon Sep 21 15:53:11 2026")
	target.Observations.Logs = []string{filepath.Join(t.TempDir(), "build.log")}

	o := &Process{
		SameProcess: func(int, string) bool { return true },
		Descendants: func(int) ([]int, error) { return nil, nil },
	}

	// The error is expected and not fatal: a path that cannot be read is a gap
	// in coverage, which process.go reports alongside a fully usable sighting
	// rather than instead of one — "it is never allowed to read as silence".
	got, err := o.Observe(target)
	if err == nil {
		t.Error("Observe returned no error, want the coverage gap reported")
	}
	if !got.Live {
		t.Fatalf("Live = false, want a usable sighting alongside the gap: %+v", got.Observation)
	}
	if !got.LastActivityAt.IsZero() {
		t.Errorf("LastActivityAt = %v, want zero for a path that does not exist", got.LastActivityAt)
	}
	if got.ActivityResolution != policy.ResolutionActivity {
		t.Errorf("ActivityResolution = %q, want activity even with no timestamp yet",
			got.ActivityResolution)
	}
}

func TestProcessVerifiedExitReportsUnavailableResolution(t *testing.T) {
	o := &Process{
		SameProcess: func(int, string) bool { return false },
		Descendants: func(int) ([]int, error) { return nil, nil },
	}

	got, err := o.Observe(processTarget(4242, "Mon Sep 21 15:53:11 2026"))
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if got.Status != policy.StatusExited {
		t.Fatalf("Status = %q, want exited", got.Status)
	}
	if got.ActivityResolution != policy.ResolutionUnavailable {
		t.Errorf("ActivityResolution = %q, want unavailable: nothing watches a process that has gone",
			got.ActivityResolution)
	}
}
