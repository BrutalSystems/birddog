package provider

import "testing"

// reset empties the registry for one test and restores it afterwards.
func reset(t *testing.T) {
	t.Helper()
	saved := specs
	specs = map[string]Spec{}
	t.Cleanup(func() { specs = saved })
}
