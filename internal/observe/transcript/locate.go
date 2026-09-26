// Package transcript reads a Claude Code session's conversation transcript.
//
// The session registry reports a session as idle whether it has finished its
// turn or is blocked on a question, and those are not alike. The transcript is
// what separates them. It is read and never written: this package sends
// nothing to any session.
package transcript

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Locate returns the transcript path for a session, or "" when there is none.
//
// A transcript lives under its own config profile, at
// projects/<cwd-slug>/<sessionID>.jsonl. Every candidate profile is searched,
// because the session registry a birddog instance reads may aggregate several
// of them by symlink while the transcripts stay where they were written.
//
// The slug is not reconstructed: it is derived from a working directory that
// may have moved, so the session id — which cannot — is matched instead.
func Locate(projectsDirs []string, sessionID string) (string, error) {
	if sessionID == "" {
		return "", fmt.Errorf("transcript: empty session id")
	}
	// A session id reaches here from a config file. Anything that could
	// escape the projects directory or widen the glob is refused rather
	// than sanitised, because a sanitised id would match the wrong session.
	if strings.ContainsAny(sessionID, `/\*?[]`) || strings.Contains(sessionID, "..") {
		return "", fmt.Errorf("transcript: session id %q is not a bare identifier", sessionID)
	}

	var hits []string
	for _, dir := range projectsDirs {
		// A directory that does not exist yields no matches and no error:
		// a profile with no transcripts is ordinary, not a fault.
		found, err := filepath.Glob(filepath.Join(dir, "*", sessionID+".jsonl"))
		if err != nil {
			return "", fmt.Errorf("transcript: search %s: %w", dir, err)
		}
		hits = append(hits, found...)
	}

	switch len(hits) {
	case 0:
		return "", nil
	case 1:
		return hits[0], nil
	default:
		// Two directories holding the same session id. Picking one would
		// report another session's conversation as this one's.
		return "", fmt.Errorf("transcript: session %s matches %d transcripts", sessionID, len(hits))
	}
}

// DefaultProjectsDirs lists every Claude Code config profile's projects
// directory under home.
//
// Supplied by the caller rather than discovered here, so this package stays a
// pure function of the directories it is given and the CLI keeps the one job
// of knowing where things live on disk.
func DefaultProjectsDirs(home string) []string {
	dirs, err := filepath.Glob(filepath.Join(home, ".claude*", "projects"))
	if err != nil {
		return nil
	}
	return dirs
}
