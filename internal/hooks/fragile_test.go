package hooks

import "testing"

// hooks install writes whatever binary it is running from. Under a version
// manager that is a path with a version in it, so a birddog upgrade is fine
// and a *node* upgrade silently breaks every hook — the handler is still
// registered, still named, and no longer there.
//
// birddog cannot pick a better path on the operator's behalf: resolving the
// name on PATH would happily choose a stale binary that happens to come
// first, which is exactly how this goes wrong. So it says what it is about to
// write and why that may not last.
func TestAVersionManagerPathIsReportedAsFragile(t *testing.T) {
	fragile := []string{
		"/Users/x/.asdf/installs/nodejs/24.16.0/bin/birddog",
		"/Users/x/.nvm/versions/node/v22.11.0/bin/birddog",
		"/Users/x/.volta/tools/image/node/20.1.0/bin/birddog",
		"/Users/x/.fnm/node-versions/v21.0.0/installation/bin/birddog",
		"/Users/x/.nodenv/versions/22.0.0/bin/birddog",
		"/Users/x/project/node_modules/.bin/birddog",
	}
	for _, path := range fragile {
		if FragilePath(path) == "" {
			t.Errorf("FragilePath(%q) = \"\", want it flagged", path)
		}
	}
}

// A stable location must stay quiet, or the warning becomes noise and is
// ignored when it matters.
func TestAStablePathIsNotReportedAsFragile(t *testing.T) {
	stable := []string{
		"/usr/local/bin/birddog",
		"/opt/homebrew/bin/birddog",
		"/Users/x/.local/bin/birddog",
		"/Users/x/.asdf/shims/birddog",
		"/Users/x/go/bin/birddog",
	}
	for _, path := range stable {
		if got := FragilePath(path); got != "" {
			t.Errorf("FragilePath(%q) = %q, want no warning", path, got)
		}
	}
}

// A shim is the stable half of the same tool, and flagging it would push an
// operator away from the one path that survives both kinds of upgrade.
func TestAShimIsNotConfusedWithAVersionedInstall(t *testing.T) {
	if got := FragilePath("/Users/x/.asdf/shims/birddog"); got != "" {
		t.Errorf("FragilePath on an asdf shim = %q, want no warning", got)
	}
}

// The message has to name the thing an operator can act on.
func TestTheWarningNamesTheEscapeHatch(t *testing.T) {
	got := FragilePath("/Users/x/.asdf/installs/nodejs/24.16.0/bin/birddog")
	if !containsStr(got, "--binary") {
		t.Errorf("warning = %q, want it to mention --binary", got)
	}
}

func containsStr(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
