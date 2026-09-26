package observe

import (
	"os"
	"time"
)

// Watcher tracks explicitly named files and logs.
//
// It is polling by design: there is no recursive watch, no whole-home scan and
// nothing implicit. Only the paths a target names are looked at, and a path
// that cannot be read is reported as unavailable rather than as silence.
type Watcher struct {
	paths []string
	seen  map[string]fileMark
}

// fileMark is the previous sighting of one file. Size is kept alongside the
// modification time because truncation and in-place rotation can leave the
// timestamp nearly unchanged while the content is gone.
type fileMark struct {
	modTime time.Time
	size    int64
	inode   uint64
	exists  bool
}

// FileActivity is what one poll saw.
type FileActivity struct {
	// Available is false when nothing being watched could be read at all.
	Available bool

	// Changed is true when any watched file grew, shrank or was replaced
	// since the previous poll.
	Changed bool

	LastChangeAt time.Time
	Size         int64

	// Unavailable names the paths that could not be read, so a gap in
	// coverage is visible instead of silently narrowing what is watched.
	Unavailable []string
}

// NewWatcher builds a watcher over explicitly named paths.
func NewWatcher(paths []string) *Watcher {
	return &Watcher{paths: paths, seen: make(map[string]fileMark, len(paths))}
}

// Poll compares every watched path against its previous state.
//
// The first poll establishes a baseline and reports no change: birddog has
// nothing to compare against yet, and inventing a change would be reporting
// activity it did not observe.
func (w *Watcher) Poll() FileActivity {
	var out FileActivity

	for _, path := range w.paths {
		info, err := os.Stat(path)
		if err != nil {
			out.Unavailable = append(out.Unavailable, path)
			// A file that disappeared has changed, if we had seen it before.
			if prev, ok := w.seen[path]; ok && prev.exists {
				out.Changed = true
				w.seen[path] = fileMark{}
			}
			continue
		}

		out.Available = true
		mark := fileMark{
			modTime: info.ModTime(),
			size:    info.Size(),
			inode:   inodeOf(info),
			exists:  true,
		}

		if mark.modTime.After(out.LastChangeAt) {
			out.LastChangeAt = mark.modTime
			out.Size = mark.size
		}

		prev, known := w.seen[path]
		w.seen[path] = mark
		if !known {
			continue // baseline only
		}

		// Any of these is a change: new writes, a truncation that shrank the
		// file, or a replacement that kept the name but changed the file.
		if !mark.modTime.Equal(prev.modTime) || mark.size != prev.size || mark.inode != prev.inode {
			out.Changed = true
		}
	}
	return out
}
