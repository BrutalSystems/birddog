package claude

import (
	"net"
	"time"
)

// probeTimeout bounds a single connect probe. A listing probes every record,
// so this is the per-session cost of a discovery call.
const probeTimeout = 250 * time.Millisecond

// ProbeSocket reports whether a unix socket accepts a connection.
//
// This is the liveness test the staleness model rests on: a registry record
// can outlive the process that wrote it, but a dead session's socket refuses.
// The probe connects and closes without writing, so a live session sees a
// connection carrying no bytes and is not disturbed.
func ProbeSocket(path string) bool {
	if path == "" {
		return false
	}
	c, err := net.DialTimeout("unix", path, probeTimeout)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}
