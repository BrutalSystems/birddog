package daemon

import "github.com/BrutalSystems/birddog/internal/ipc"

// callStatus keeps the status round trip in one place for tests.
func callStatus(socket string, out *StatusResult) error {
	return ipc.Call(socket, "status", nil, out)
}
