package claude

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

// shortTempDir returns a directory short enough to hold a unix socket path.
//
// macOS caps sockaddr_un.sun_path at 104 bytes, and t.TempDir() returns
// /var/folders/<...>/T/<TestName><digits>/001 — which exceeds that on its own,
// making bind fail with "invalid argument". Same constraint applies to
// birddog's own state directory wherever it binds a socket.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "bd")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// ProbeSocket is the liveness test the whole staleness model rests on, so it
// is exercised against real unix sockets rather than a stub.

func TestProbeSocketTrueForListeningSocket(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "live.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()

	if !ProbeSocket(path) {
		t.Error("ProbeSocket = false, want true for a socket with a listener")
	}
}

func TestProbeSocketFalseAfterListenerClosed(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "dead.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	l.Close()

	if ProbeSocket(path) {
		t.Error("ProbeSocket = true, want false once the listener is gone")
	}
}

func TestProbeSocketFalseForMissingPath(t *testing.T) {
	if ProbeSocket(filepath.Join(shortTempDir(t), "never-existed.sock")) {
		t.Error("ProbeSocket = true, want false for a path with no socket")
	}
}

func TestProbeSocketFalseForEmptyPath(t *testing.T) {
	if ProbeSocket("") {
		t.Error("ProbeSocket = true, want false for an empty path")
	}
}

// Probing must not disturb a live session: the probe connects and closes
// without writing, so the listener sees a connection carrying no bytes.
func TestProbeSocketWritesNothingToTheListener(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "quiet.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()

	read := make(chan int, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			read <- -1
			return
		}
		defer c.Close()
		buf := make([]byte, 64)
		n, _ := c.Read(buf) // returns 0, io.EOF when the prober closes silently
		read <- n
	}()

	if !ProbeSocket(path) {
		t.Fatal("ProbeSocket = false, want true")
	}
	if n := <-read; n != 0 {
		t.Errorf("listener read %d bytes, want 0 — a probe must not send anything", n)
	}
}
