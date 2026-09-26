// Package provider is the registry of what birddog can observe.
//
// It is a leaf on purpose. internal/config validates a watch list against it
// and internal/cli/doctor describes it, and neither may import the adapters
// themselves: internal/observe imports config, so a registry carrying
// constructors here would close an import cycle. Constructors are registered
// separately inside internal/observe, from the same init that registers the
// spec here, and a test asserts the two never diverge.
package provider

import "sort"

// Spec is everything birddog can say about a provider without observing one.
type Spec struct {
	// Name is the provider token a watch list names.
	Name string

	// Command is the executable a reader would run to start one.
	Command string

	// The capability prose reported by doctor, one line each.
	AttachToRunningSession string
	RuntimeState           string
	ToolEvents             string
	InputRequests          string
	Notes                  string

	// The machine-readable support levels beside that prose. Plain strings
	// holding a policy.Support* value; the constants are not imported here
	// because this package must stay a leaf.
	AttachToRunningSessionSupport string
	RuntimeStateSupport           string
	ToolEventsSupport             string
	InputRequestsSupport          string

	// Internal reports a provider that exists for tests and smoke runs
	// rather than for an operator. doctor leaves these out of its table;
	// config still accepts them, because the smoke test names them.
	Internal bool
}

var specs = map[string]Spec{}

// Register adds a provider. It panics on a duplicate, which can only be a
// programming error at startup.
func Register(s Spec) {
	if _, exists := specs[s.Name]; exists {
		panic("provider: " + s.Name + " registered twice")
	}
	specs[s.Name] = s
}

// Known reports whether a watch list may name this provider.
func Known(name string) bool {
	_, ok := specs[name]
	return ok
}

// Names lists every registered provider, sorted.
func Names() []string {
	out := make([]string, 0, len(specs))
	for n := range specs {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Specs lists every registration, sorted by name.
func Specs() []Spec {
	out := make([]Spec, 0, len(specs))
	for _, n := range Names() {
		out = append(out, specs[n])
	}
	return out
}
