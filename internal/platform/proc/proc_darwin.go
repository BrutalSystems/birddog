//go:build darwin

// Package proc supplies process identity.
//
// A PID is not an identity: the kernel reuses it, so a live PID may belong to
// a different process than the one recorded. Every reference birddog stores is
// (pid, start time), and a mismatch means the original process is gone — never
// that the replacement should be adopted.
package proc

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// StartTime returns the process's start time in `ps -o lstart` format,
// rendered in UTC.
//
// UTC is not a preference, it is the contract: Claude Code records procStart
// in its session registry in UTC, while `ps -o lstart` prints in the caller's
// local zone. Comparing the two unconverted makes every live session look like
// a PID-reuse victim on any machine not set to UTC — a four-hour error in
// US/Eastern, and a date rollover in Asia/Tokyo.
//
// Returns an error when no process holds the PID.
func StartTime(pid int) (string, error) {
	// argv-based, never a shell: no quoting or path-with-spaces hazard.
	cmd := exec.Command("ps", "-o", "lstart=", "-p", fmt.Sprint(pid))
	// Pin the child's zone rather than the parent's, so the caller's TZ, and
	// any change to it mid-run, cannot shift the result.
	cmd.Env = append(os.Environ(), "TZ=UTC")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("no process with pid %d: %w", pid, err)
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return "", fmt.Errorf("no process with pid %d", pid)
	}
	return s, nil
}

// SameProcess reports whether the process now at pid is the one whose start
// time was recorded. False when the PID is unused, and false when it has been
// reused by a different process.
func SameProcess(pid int, recordedStart string) bool {
	if recordedStart == "" {
		return false
	}
	now, err := StartTime(pid)
	if err != nil {
		return false
	}
	return now == recordedStart
}

// Descendants returns every process descended from pid, transitively.
//
// A worker is often waiting on a child — a build, a test suite — and that child
// is the work. Without this, a session blocked on a twenty-minute compile looks
// exactly like one that has stopped.
func Descendants(pid int) ([]int, error) {
	out, err := exec.Command("ps", "-Ao", "pid=,ppid=").Output()
	if err != nil {
		return nil, fmt.Errorf("list processes: %w", err)
	}

	children := map[int][]int{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		child, err1 := strconv.Atoi(fields[0])
		parent, err2 := strconv.Atoi(fields[1])
		if err1 != nil || err2 != nil {
			continue
		}
		children[parent] = append(children[parent], child)
	}

	var found []int
	seen := map[int]bool{pid: true}
	queue := []int{pid}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, child := range children[current] {
			if seen[child] {
				continue // a cycle cannot happen, but not looping is cheap
			}
			seen[child] = true
			found = append(found, child)
			queue = append(queue, child)
		}
	}
	return found, nil
}
