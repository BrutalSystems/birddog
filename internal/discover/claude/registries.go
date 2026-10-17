package claude

import (
	"errors"
	"fmt"
	iofs "io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ConfigDirsEnv names further Claude Code profile directories whose session
// registries birddog reads, as a list separated by the OS path-list separator
// (":" on macOS): `BIRDDOG_CLAUDE_CONFIG_DIRS=~/.claude-work:~/.claude-home`.
//
// Claude Code writes a session's registry record under $CLAUDE_CONFIG_DIR, so
// a profile's sessions are in that profile's directory and nowhere else. Which
// directories are profiles is said, not guessed: a pattern such as
// ~/.claude*/ also matches backups, copies and unrelated directories, and a
// copy of a registry would then be read as live sessions, with its stale
// credential files beside them.
const ConfigDirsEnv = "BIRDDOG_CLAUDE_CONFIG_DIRS"

// Registries returns the session registry directories to read, worked out
// afresh on every call: ~/.claude/sessions, always, then $CLAUDE_CONFIG_DIR's,
// then each of BIRDDOG_CLAUDE_CONFIG_DIRS's.
//
// Afresh, because a daemon runs for days. A registry that does not exist yet —
// a profile not used since boot, or a machine on which Claude Code has never
// run — is created later, and a list captured at startup would never see it.
// ~/.claude/sessions is listed whether or not it exists for the same reason:
// it is the path that will be there.
//
// A directory reached through a symbolic link is skipped. A link to another
// profile's registry would read the same records twice under a path that is
// not where they live; a link to a copy would read records nothing is
// writing.
func Registries() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	candidates := []string{filepath.Join(home, ".claude")}
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		candidates = append(candidates, dir)
	}
	for _, dir := range filepath.SplitList(os.Getenv(ConfigDirsEnv)) {
		if dir = strings.TrimSpace(dir); dir != "" {
			candidates = append(candidates, dir)
		}
	}

	seen := map[string]bool{}
	var out []string
	for _, dir := range candidates {
		dir = expandHome(dir, home)
		if !filepath.IsAbs(dir) {
			continue
		}
		registry := filepath.Clean(filepath.Join(dir, "sessions"))
		if seen[registry] || isSymlink(registry) || isSymlink(filepath.Clean(dir)) {
			continue
		}
		seen[registry] = true
		out = append(out, registry)
	}
	return out
}

func expandHome(path, home string) string {
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:])
	}
	return path
}

func isSymlink(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode()&os.ModeSymlink != 0
}

// PartialError reports that some registries could not be read although others
// could. The sessions returned beside it are real, and incomplete: a session
// missing from them may be in a registry that could not be read, so its
// absence is not evidence that it has gone.
type PartialError struct {
	Unreadable map[string]error
}

func (e *PartialError) Error() string {
	var parts []string
	for dir, err := range e.Unreadable {
		parts = append(parts, fmt.Sprintf("%s: %v", dir, err))
	}
	return "some session registries could not be read: " + strings.Join(parts, "; ")
}

// IsPartial reports whether err says a listing is incomplete.
func IsPartial(err error) bool {
	var p *PartialError
	return errors.As(err, &p)
}

// listMany reads several registries as one.
//
// A registry that does not exist holds no sessions, which is an answer. One
// that exists and cannot be read is not, and is reported as a PartialError
// beside whatever the others held — or as the error itself when none could be
// read at all.
//
// One process is one session. A process recorded in more than one registry is
// identified by pid and start time together, never pid alone, and the record
// chosen is the live one, then the one that is not a link, then the newest:
// liveness is the fact birddog acts on, and a timestamp is only a tiebreak.
func listMany(p Params, dirs []string) ([]Session, error) {
	type key struct {
		pid   int
		start string
	}
	var out []Session
	index := map[key]int{}
	unreadable := map[string]error{}
	read := 0

	for _, dir := range dirs {
		ss, err := listDir(p, dir)
		if err != nil {
			if errors.Is(err, iofs.ErrNotExist) {
				continue
			}
			unreadable[dir] = err
			continue
		}
		read++
		for _, s := range ss {
			k := key{s.PID, s.ProcStart}
			i, dup := index[k]
			if !dup || s.PID == 0 {
				index[k] = len(out)
				out = append(out, s)
				continue
			}
			if better(s, out[i]) {
				out[i] = s
			}
		}
	}

	switch {
	case len(unreadable) == 0:
		return out, nil
	case read == 0:
		return nil, &PartialError{Unreadable: unreadable}
	default:
		return out, &PartialError{Unreadable: unreadable}
	}
}

// better reports whether a is the record to keep over b for the same process.
func better(a, b Session) bool {
	if a.Live != b.Live {
		return a.Live
	}
	if a.linked != b.linked {
		return !a.linked
	}
	return a.UpdatedAt.After(b.UpdatedAt)
}
