package notify

import (
	"errors"
	"strings"
	"testing"
)

func resetRegistry(t *testing.T) {
	t.Helper()
	saved := make(map[string]Factory, len(factories))
	for k, v := range factories {
		saved[k] = v
	}
	t.Cleanup(func() { factories = saved })
}

func TestBuildReturnsNoneByDefault(t *testing.T) {
	n, err := Build(Options{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if n.Name() != "none" {
		t.Errorf("Name = %q, want none", n.Name())
	}
}

func TestBuildReturnsARegisteredConnector(t *testing.T) {
	resetRegistry(t)
	Register("test-kind", func(Options) (Notifier, error) { return &Fake{}, nil })

	n, err := Build(Options{Kind: "test-kind"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if n.Name() != "fake" {
		t.Errorf("Name = %q", n.Name())
	}
}

// An unknown kind is refused at startup. Silently falling back to no delivery
// would leave an operator believing alerts are being sent when they are not.
func TestBuildRefusesAnUnknownKind(t *testing.T) {
	resetRegistry(t)
	Register("known", func(Options) (Notifier, error) { return &Fake{}, nil })

	_, err := Build(Options{Kind: "invented"})
	if err == nil {
		t.Fatal("Build = nil error for an unknown kind")
	}
	if !strings.Contains(err.Error(), "known") {
		t.Errorf("error = %v, want it to name the kinds that do exist", err)
	}
}

func TestBuildSurfacesAConnectorsOwnError(t *testing.T) {
	resetRegistry(t)
	Register("broken", func(Options) (Notifier, error) { return nil, errors.New("needs a session_id") })

	_, err := Build(Options{Kind: "broken"})
	if err == nil || !strings.Contains(err.Error(), "session_id") {
		t.Errorf("error = %v, want the connector's own explanation", err)
	}
}

func TestOptionsArePassedToTheConnector(t *testing.T) {
	resetRegistry(t)
	var seen Options
	Register("capturing", func(o Options) (Notifier, error) {
		seen = o
		return &Fake{}, nil
	})

	if _, err := Build(Options{Kind: "capturing", Settings: map[string]string{"session_id": "abc"}}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if seen.Settings["session_id"] != "abc" {
		t.Errorf("settings = %v", seen.Settings)
	}
}

func TestKindsListsWhatCanBeConfigured(t *testing.T) {
	resetRegistry(t)
	Register("b-kind", func(Options) (Notifier, error) { return &Fake{}, nil })
	Register("a-kind", func(Options) (Notifier, error) { return &Fake{}, nil })

	got := Kinds()
	if len(got) < 3 { // the two above plus "none"
		t.Fatalf("Kinds = %v, want at least none and the two registered", got)
	}
	// Sorted, so help text and error messages read the same way twice.
	for i := 1; i < len(got); i++ {
		if got[i-1] > got[i] {
			t.Errorf("Kinds not sorted: %v", got)
			break
		}
	}
}

func TestNoneIsAlwaysAvailable(t *testing.T) {
	resetRegistry(t)
	factories = map[string]Factory{}

	n, err := Build(Options{Kind: "none"})
	if err != nil {
		t.Fatalf("Build: %v, want none always available", err)
	}
	if n.Name() != "none" {
		t.Errorf("Name = %q", n.Name())
	}
}
