package cli

import (
	"bytes"
	"testing"

	"github.com/BrutalSystems/birddog/internal/version"
)

// The release smoke test string-matches this line, so a reworded banner has to
// fail a test rather than a release. tincan shipped a version three releases
// stale behind a check that only asked whether anything was printed.
func TestVersionReportsTheCompiledVersion(t *testing.T) {
	for _, arg := range []string{"--version", "version"} {
		var stdout, stderr bytes.Buffer

		code := Main([]string{arg}, &stdout, &stderr)

		if code != ExitOK {
			t.Fatalf("%s: exit %d, want %d (stderr: %q)", arg, code, ExitOK, stderr.String())
		}
		want := "birddog " + version.Version + "\n"
		if got := stdout.String(); got != want {
			t.Errorf("%s: stdout %q, want %q", arg, got, want)
		}
		if stderr.Len() != 0 {
			t.Errorf("%s: wrote %q to stderr, want nothing", arg, stderr.String())
		}
	}
}
