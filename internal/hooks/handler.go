package hooks

import (
	"fmt"
	"os"
)

// HandlerState is whether an installed hook handler can actually run.
//
// Registration and runnability are different facts, and birddog reported only
// the first. A handler entry whose binary has been moved or removed leaves
// every hook firing and failing, while `hooks status` calls the installation
// complete because all the entries are present — coverage that looks complete
// while none of it works.
//
// This is the same discipline the adapters already follow about watched
// sessions: a record is not evidence that the thing it describes is there.
// birddog applies it to its own installation here.
type HandlerState struct {
	// Path is the handler the settings file names. Empty when none is
	// installed.
	Path string

	// Runnable is true when something is installed and can be executed.
	// False when nothing is installed, so read it with Path.
	Runnable bool

	// Problem describes what is wrong, for an operator to act on. Empty when
	// the handler is runnable, and empty when none is installed — an absent
	// installation is a choice, not a fault.
	Problem string
}

// CheckHandler reports whether the handler at path can be run.
//
// It stats rather than executes. Running the handler to find out would mean
// birddog invoking itself on every status check, and a hook handler is
// deliberately a short-lived process with side effects on its own state
// directory.
//
// Stat follows symlinks, which is the case that matters: a link left pointing
// at a removed binary is exactly how this fails in practice, and it is
// indistinguishable from a working install by inspecting the settings file
// alone.
func CheckHandler(path string) HandlerState {
	if path == "" {
		// Nothing installed. Not a fault; `hooks status` says so separately.
		return HandlerState{}
	}

	info, err := os.Stat(path)
	switch {
	case os.IsNotExist(err):
		return HandlerState{
			Path:    path,
			Problem: fmt.Sprintf("%s is registered as the hook handler but is not there — every hook will fail until it is restored or `birddog hooks install` is re-run", path),
		}
	case err != nil:
		return HandlerState{
			Path:    path,
			Problem: fmt.Sprintf("%s could not be checked: %v", path, err),
		}
	case info.IsDir():
		return HandlerState{
			Path:    path,
			Problem: fmt.Sprintf("%s is a directory, not the birddog binary", path),
		}
	case info.Mode().Perm()&0o111 == 0:
		return HandlerState{
			Path:    path,
			Problem: fmt.Sprintf("%s is not executable, so every hook will fail", path),
		}
	}

	return HandlerState{Path: path, Runnable: true}
}
