package daemon

// config validates against the registry internal/observe's init populates.
// A daemon test binary that never links observe sees an empty registry and
// rejects every provider name, including the fake one these tests watch.
import _ "github.com/BrutalSystems/birddog/internal/observe"
