package instrument

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BrutalSystems/birddog/internal/policy"
)

// defaultStaleAfter matches the bound the opencode observer applies, so doctor
// and the adapter agree about what counts as still publishing.
const defaultStaleAfter = 90 * time.Second

// logTailBytes bounds how much of the plugin log is read.
//
// Only the last start and the last publish failure matter, and both are at the
// end. The log is append-only and the plugin bounds it loosely, so reading it
// whole would make a diagnostic file's size doctor's problem.
const logTailBytes = 64 * 1024

// Report says whether the birddog opencode plugin is in place here.
//
// Two traces, answering different questions. The records say what is
// publishing right now; the log says whether the plugin has ever loaded, which
// is the only way to tell an absent plugin from a machine where no opencode
// session happens to be running.
func (o Opencode) Report() Report {
	r := Report{Mechanism: "opencode_plugin", Provider: "opencode"}

	fresh, seen, recordVersion := o.records()
	r.PublishingSessions = fresh

	loaded, logVersion, failure := o.log()

	// A publishing session is the version running now. The log's last start
	// may be old and name a version since replaced, so it is the fallback
	// rather than the answer.
	r.Version = recordVersion
	if r.Version == "" {
		r.Version = logVersion
	}

	switch {
	case failure != "":
		r.State = policy.InstrumentBroken
		r.Problem = fmt.Sprintf("the plugin loaded but its last attempt to publish failed: %s", failure)
		r.Detail = "the opencode plugin is installed and cannot write its records, " +
			"so sessions it is attached to cannot be observed"

	case loaded || seen > 0:
		r.State = policy.InstrumentInstalled
		switch {
		case fresh > 0:
			r.Detail = fmt.Sprintf("the opencode plugin is publishing %s", plural(fresh, "session"))
		default:
			r.Detail = "the opencode plugin is installed and publishing nothing, " +
				"which usually means no opencode session is running"
		}

	default:
		// The honest floor. Nothing has been written and nothing has been
		// logged, and those two absences have the same two explanations.
		r.State = policy.InstrumentUnknown
		r.Detail = "no opencode session is publishing and the plugin has left no trace, " +
			"so it is either not installed or has not run since it was"
	}

	return r
}

// records counts what the plugin has written.
//
// fresh is what is publishing now; seen is every record present, which is
// weaker evidence but still proof the plugin ran at some point.
func (o Opencode) records() (fresh, seen int, version string) {
	entries, err := os.ReadDir(o.RecordsDir)
	if err != nil {
		return 0, 0, ""
	}

	stale := o.StaleAfter
	if stale <= 0 {
		stale = defaultStaleAfter
	}
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		seen++

		data, err := os.ReadFile(filepath.Join(o.RecordsDir, entry.Name()))
		if err != nil {
			continue
		}
		var rec struct {
			UpdatedAt     string `json:"updated_at"`
			PluginVersion string `json:"plugin_version"`
		}
		if err := json.Unmarshal(data, &rec); err != nil {
			continue
		}

		updated, err := time.Parse(time.RFC3339Nano, rec.UpdatedAt)
		if err != nil || now().Sub(updated) > stale {
			continue
		}
		fresh++
		if version == "" {
			version = rec.PluginVersion
		}
	}
	return fresh, seen, version
}

// log reads the plugin's own account of itself.
//
// Only the last of the two events that describe the installation is
// consulted. A publish failure followed by a start means the plugin came back;
// a start followed by a failure means it is broken now.
//
// Other `.failed` lines are deliberately ignored. A session that could not be
// resolved is a hiccup in one session, not an installation that cannot work,
// and reporting it as broken would cry wolf about a plugin doing its job.
func (o Opencode) log() (loaded bool, version, failure string) {
	for _, line := range tail(o.LogPath, logTailBytes) {
		switch {
		case strings.Contains(line, "event=started"):
			loaded, failure = true, ""
			if v := field(line, "version="); v != "" {
				version = v
			}
		case strings.Contains(line, "event=publish.failed"):
			failure = after(line, "detail=")
		}
	}
	return loaded, version, failure
}

// tail returns the whole lines in the last n bytes of a file.
//
// The first line read may have been cut in half by the offset, so it is
// dropped rather than parsed — a truncated line is not evidence of anything.
func tail(path string, n int64) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil
	}

	truncated := false
	if info.Size() > n {
		if _, err := f.Seek(info.Size()-n, io.SeekStart); err != nil {
			return nil
		}
		truncated = true
	}

	data, err := io.ReadAll(f)
	if err != nil {
		return nil
	}

	lines := strings.Split(string(data), "\n")
	if truncated && len(lines) > 0 {
		lines = lines[1:]
	}
	return lines
}

// field reads a key=value token, which the plugin writes space-separated.
func field(line, key string) string {
	v := after(line, key)
	if i := strings.IndexByte(v, ' '); i >= 0 {
		return v[:i]
	}
	return v
}

// after reads everything following key, for values that may contain spaces.
func after(line, key string) string {
	i := strings.Index(line, key)
	if i < 0 {
		return ""
	}
	return strings.TrimSpace(line[i+len(key):])
}

func plural(n int, what string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, what)
	}
	return fmt.Sprintf("%d %ss", n, what)
}
