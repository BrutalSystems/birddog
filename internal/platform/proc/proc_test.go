package proc

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// StartTime is birddog's process-identity primitive: a PID alone is not an
// identity because PIDs are reused, so every process reference is (pid,
// start time). See docs/handoff.md, acceptance criterion 7.

func TestStartTimeReturnsValueForLiveProcess(t *testing.T) {
	got, err := StartTime(os.Getpid())
	if err != nil {
		t.Fatalf("StartTime(self): %v", err)
	}
	if got == "" {
		t.Error("StartTime(self) = empty, want a start time")
	}
}

func TestStartTimeIsStableAcrossCalls(t *testing.T) {
	first, err := StartTime(os.Getpid())
	if err != nil {
		t.Fatalf("StartTime: %v", err)
	}
	second, err := StartTime(os.Getpid())
	if err != nil {
		t.Fatalf("StartTime: %v", err)
	}
	if first != second {
		t.Errorf("StartTime not stable: %q then %q", first, second)
	}
}

func TestStartTimeErrorsForUnusedPID(t *testing.T) {
	// PIDs are capped well below this on macOS, so nothing can be running here.
	if _, err := StartTime(4194303); err == nil {
		t.Error("StartTime(unused pid) = nil error, want an error")
	}
}

// The Claude Code registry stores procStart in `ps -o lstart` format, so
// StartTime must produce a string comparable to it.
func TestStartTimeMatchesRegistryFormat(t *testing.T) {
	got, err := StartTime(os.Getpid())
	if err != nil {
		t.Fatalf("StartTime: %v", err)
	}
	// e.g. "Thu Sep 17 22:11:09 2026" — five space-separated fields.
	if fields := len(splitFields(got)); fields != 5 {
		t.Errorf("StartTime = %q (%d fields), want ps lstart format with 5", got, fields)
	}
}

func splitFields(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ' ' {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func TestSameProcessTrueWhenStartTimeMatches(t *testing.T) {
	pid := os.Getpid()
	start, err := StartTime(pid)
	if err != nil {
		t.Fatalf("StartTime: %v", err)
	}
	if !SameProcess(pid, start) {
		t.Error("SameProcess = false for this process's own start time")
	}
}

// The case criterion 7 exists for: the PID is alive, but it is a different
// process than the one recorded. It must not be silently adopted.
func TestSameProcessFalseWhenStartTimeDiffers(t *testing.T) {
	if SameProcess(os.Getpid(), "Thu Jan  1 00:00:00 1970") {
		t.Error("SameProcess = true for a reused PID, want false")
	}
}

func TestSameProcessFalseForUnusedPID(t *testing.T) {
	if SameProcess(4194303, "Thu Jan  1 00:00:00 1970") {
		t.Error("SameProcess = true for an unused PID, want false")
	}
}

// Claude Code records procStart in UTC, but `ps -o lstart` prints in the
// caller's local zone. Comparing the two directly made every live session look
// like a PID-reuse victim on any machine not set to UTC. StartTime must
// therefore not vary with the caller's timezone.
func TestStartTimeIsIndependentOfCallerTimezone(t *testing.T) {
	t.Setenv("TZ", "UTC")
	utc, err := StartTime(os.Getpid())
	if err != nil {
		t.Fatalf("StartTime under TZ=UTC: %v", err)
	}

	t.Setenv("TZ", "Asia/Tokyo")
	tokyo, err := StartTime(os.Getpid())
	if err != nil {
		t.Fatalf("StartTime under TZ=Asia/Tokyo: %v", err)
	}

	if utc != tokyo {
		t.Errorf("StartTime varies with caller timezone: %q under UTC, %q under Asia/Tokyo", utc, tokyo)
	}
}

func TestSameProcessTrueRegardlessOfCallerTimezone(t *testing.T) {
	t.Setenv("TZ", "UTC")
	start, err := StartTime(os.Getpid())
	if err != nil {
		t.Fatalf("StartTime: %v", err)
	}

	t.Setenv("TZ", "America/New_York")
	if !SameProcess(os.Getpid(), start) {
		t.Error("SameProcess = false when the caller's timezone changed, want true")
	}
}

// A worker is often waiting on a child — a build, a test run — and that child
// is the work. Descendants are how that becomes visible.
func TestDescendantsFindsAChildProcess(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	got, err := Descendants(os.Getpid())
	if err != nil {
		t.Fatalf("Descendants: %v", err)
	}
	if !containsPID(got, cmd.Process.Pid) {
		t.Errorf("Descendants = %v, want it to include child %d", got, cmd.Process.Pid)
	}
}

// Grandchildren count too: a test runner that spawns workers is still work.
func TestDescendantsIsTransitive(t *testing.T) {
	cmd := exec.Command("sh", "-c", "sleep 30 & wait")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	// Give the shell a moment to fork its own child.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got, err := Descendants(os.Getpid())
		if err != nil {
			t.Fatalf("Descendants: %v", err)
		}
		if len(got) >= 2 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("Descendants never reported the grandchild")
}

func TestDescendantsOfAProcessWithNoneIsEmpty(t *testing.T) {
	got, err := Descendants(4194303) // nothing is running here
	if err != nil {
		t.Fatalf("Descendants: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Descendants = %v, want none", got)
	}
}

func containsPID(pids []int, want int) bool {
	for _, p := range pids {
		if p == want {
			return true
		}
	}
	return false
}
