//go:build darwin

package observe

import (
	"os"
	"syscall"
)

// inodeOf identifies the file behind a name, so a log rotated by replacement
// is seen as a new file rather than as the same one having gone quiet.
func inodeOf(info os.FileInfo) uint64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return st.Ino
	}
	return 0
}
