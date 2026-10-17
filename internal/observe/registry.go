package observe

import (
	"fmt"

	"github.com/BrutalSystems/birddog/internal/monitor"
)

// Deps is everything any adapter needs to be built. Supplied by the CLI,
// which is the only layer that knows where things live on disk.
type Deps struct {
	ClaudeRegistryDir string
	HookStateDir      string
	CodexLockDir      string
	OpencodeRecords   string

	// ProjectsDirs is every config profile's transcripts directory. A list,
	// not one path: a registry directory may aggregate several profiles by
	// symlink while their transcripts stay where they were written.
	ProjectsDirs []string
}

type constructor func(Deps) (monitor.Observer, error)

var constructors = map[string]constructor{}

func register(name string, c constructor) {
	if _, exists := constructors[name]; exists {
		panic("observe: constructor for " + name + " registered twice")
	}
	constructors[name] = c
}

// Build constructs every registered observer.
func Build(d Deps) (map[string]monitor.Observer, error) {
	out := make(map[string]monitor.Observer, len(constructors))
	for name, c := range constructors {
		o, err := c(d)
		if err != nil {
			return nil, fmt.Errorf("build %s observer: %w", name, err)
		}
		out[name] = o
	}
	return out, nil
}
