package config_test

// Provider names are registered by internal/observe's init. config validates
// against that registry, so a config test binary that never links observe
// would reject every provider name.
import _ "github.com/BrutalSystems/birddog/internal/observe"
