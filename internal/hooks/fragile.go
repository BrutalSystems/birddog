package hooks

import (
	"fmt"
	"strings"
)

// versionedInstallDirs are path fragments that mean a binary lives inside one
// particular version of a runtime, rather than at a stable name.
//
// The shim or wrapper each of these tools also provides is deliberately not
// listed: a shim is the stable half of the same tool and is the right thing to
// install, so flagging it would push an operator away from the one path that
// survives both kinds of upgrade.
var versionedInstallDirs = []string{
	"/.asdf/installs/",
	"/.nvm/versions/",
	"/.volta/tools/",
	"/.fnm/node-versions/",
	"/.nodenv/versions/",
	"/.n/versions/",
}

// FragilePath reports why an install path is unlikely to keep working, or ""
// when there is nothing to say.
//
// hooks install writes the binary it is running from, and under a version
// manager that is a path with a version number in it. Upgrading birddog is
// then fine — the same path is rewritten — while upgrading *node* moves the
// install tree and leaves every hook entry naming a binary that is no longer
// there. The handler stays registered, stays listed, and silently fails on
// every tool call.
//
// birddog does not pick a different path on the operator's behalf. Resolving
// the name on PATH would choose whatever comes first, which may be an older
// binary someone built by hand — the exact failure this warns about, arrived
// at by a shorter route. Reporting the risk and naming the escape hatch
// leaves the decision where it belongs.
func FragilePath(path string) string {
	for _, dir := range versionedInstallDirs {
		if strings.Contains(path, dir) {
			return fmt.Sprintf(
				"%s is inside a version-managed install directory, so it names one version of a runtime. "+
					"Upgrading birddog keeps working; changing that runtime's version moves the directory and "+
					"leaves every hook naming a binary that is no longer there. "+
					"Consider re-running with --binary and a path that does not move, such as the version "+
					"manager's shim.", path)
		}
	}

	// A project-local install is narrower still: it is gone as soon as the
	// directory is cleaned, reinstalled, or the operator works elsewhere.
	if strings.Contains(path, "/node_modules/.bin/") {
		return fmt.Sprintf(
			"%s is inside a project's node_modules, so it disappears when that directory is cleaned or "+
				"reinstalled, and means nothing outside that project. Consider re-running with --binary and "+
				"a path that does not move.", path)
	}

	return ""
}
