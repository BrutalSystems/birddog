package codex

import (
	"os/exec"
	"strconv"
	"strings"
)

// LsofHolder reports the pid of a process holding the file open.
//
// This is the liveness test for a Codex thread: the thread store lists threads
// that ended long ago, and a lock file left behind by a crashed process is
// indistinguishable from a live one by its presence alone. Only a holder makes
// a thread live.
//
// When several processes hold the file, the lowest pid is returned — stable
// across calls, which matters because the holder is recorded as evidence.
func LsofHolder(path string) (int, bool) {
	// argv-based, never a shell: lock paths may contain spaces.
	out, err := exec.Command("lsof", "-t", "--", path).Output()
	if err != nil {
		// lsof exits nonzero when nothing holds the file. That is the
		// ordinary "not held" answer, not a failure.
		return 0, false
	}

	lowest := 0
	for _, line := range strings.Fields(string(out)) {
		pid, err := strconv.Atoi(line)
		if err != nil || pid <= 0 {
			continue
		}
		if lowest == 0 || pid < lowest {
			lowest = pid
		}
	}
	if lowest == 0 {
		return 0, false
	}
	return lowest, true
}

// HolderCWD reports the working directory of a running process.
//
// A Codex thread that has not taken a turn yet holds a writer lock but has no
// state-database record, so its holder's working directory is the only thing
// that names it.
func HolderCWD(pid int) (string, bool) {
	out, err := exec.Command("lsof", "-a", "-d", "cwd", "-p", strconv.Itoa(pid), "-Fn").Output()
	if err != nil {
		return "", false
	}
	// -F emits one field per line, each prefixed by its type; "n" is the name.
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "n") {
			if cwd := strings.TrimSpace(line[1:]); cwd != "" {
				return cwd, true
			}
		}
	}
	return "", false
}
