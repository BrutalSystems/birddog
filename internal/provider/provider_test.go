package provider

import "testing"

func TestRegisterMakesProviderKnown(t *testing.T) {
	reset(t)
	Register(Spec{Name: "demo"})
	if !Known("demo") {
		t.Error("Known(demo) = false, want true")
	}
	if Known("nope") {
		t.Error("Known(nope) = true, want false")
	}
}

func TestSpecsAreSortedByName(t *testing.T) {
	reset(t)
	Register(Spec{Name: "zebra"})
	Register(Spec{Name: "alpha"})
	got := Names()
	if len(got) != 2 || got[0] != "alpha" || got[1] != "zebra" {
		t.Errorf("Names() = %v, want [alpha zebra]", got)
	}
}

func TestRegisterPanicsOnDuplicate(t *testing.T) {
	reset(t)
	Register(Spec{Name: "dup"})
	defer func() {
		if recover() == nil {
			t.Error("second Register did not panic")
		}
	}()
	Register(Spec{Name: "dup"})
}
