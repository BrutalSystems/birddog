package notify

import (
	"fmt"
	"sort"
	"strings"
)

// Connectors register themselves here, so adding a route to a new runtime is a
// new package and one import rather than a change to the monitor, the daemon
// or the commands.
//
// Every connector answers the same three questions, and the second is the one
// that decides the design: who is the recipient, what did the attempt actually
// establish, and can delivery reach a busy orchestrator without interrupting
// it. A connector that cannot answer the third non-disruptively should not
// exist — polling is always available and never interrupts anyone.

// Options configure one connector.
type Options struct {
	// Kind selects the connector. Empty or "none" means no delivery.
	Kind string

	// Settings are connector-specific. Each connector documents its own keys
	// and must refuse ones it does not understand.
	Settings map[string]string
}

// Factory builds a connector from its options.
type Factory func(Options) (Notifier, error)

// factories holds the registered connectors. Registration happens in package
// init, so importing a connector is what makes it configurable.
var factories = map[string]Factory{}

// Register adds a connector. It panics on a duplicate, which can only be a
// programming error at startup.
func Register(kind string, f Factory) {
	if _, exists := factories[kind]; exists {
		panic("notify: connector " + kind + " registered twice")
	}
	factories[kind] = f
}

// Kinds lists what can be configured, sorted.
func Kinds() []string {
	kinds := make([]string, 0, len(factories)+1)
	kinds = append(kinds, "none")
	for k := range factories {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	return kinds
}

// Build constructs the configured connector.
//
// An unknown kind is refused rather than quietly falling back to no delivery:
// an operator who configured a route is entitled to be told it does not exist,
// instead of believing alerts are being sent when they are not.
func Build(o Options) (Notifier, error) {
	if o.Kind == "" || o.Kind == "none" {
		return None{}, nil
	}
	factory, ok := factories[o.Kind]
	if !ok {
		return nil, fmt.Errorf("unknown notification kind %q; available: %s",
			o.Kind, strings.Join(Kinds(), ", "))
	}
	n, err := factory(o)
	if err != nil {
		return nil, fmt.Errorf("notification %q: %w", o.Kind, err)
	}
	return n, nil
}
