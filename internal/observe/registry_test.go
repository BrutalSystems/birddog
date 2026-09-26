package observe

import (
	"testing"

	"github.com/BrutalSystems/birddog/internal/observe/transcript"
	"github.com/BrutalSystems/birddog/internal/provider"
)

// The two registries are populated from the same init and must never
// diverge: a provider with a spec and no constructor is nameable in a watch
// list and observes nothing, which is the half-added state the split exists
// to prevent.
func TestEveryRegisteredProviderHasAConstructor(t *testing.T) {
	for _, name := range provider.Names() {
		if _, ok := constructors[name]; !ok {
			t.Errorf("provider %q has a spec but no constructor", name)
		}
	}
	for name := range constructors {
		if !provider.Known(name) {
			t.Errorf("provider %q has a constructor but no spec", name)
		}
	}
}

func TestClaudeObserverIsWrappedForTranscriptReading(t *testing.T) {
	obs, err := Build(Deps{
		ClaudeRegistryDir: t.TempDir(),
		HookStateDir:      t.TempDir(),
		CodexLockDir:      t.TempDir(),
		OpencodeRecords:   t.TempDir(),
		ProjectsDirs:      []string{t.TempDir()},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, ok := obs["claude"].(*transcript.Watcher); !ok {
		t.Errorf("claude observer is %T, want *transcript.Watcher", obs["claude"])
	}
}
