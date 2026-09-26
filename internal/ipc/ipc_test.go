package ipc

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// shortSocketPath keeps within the 104-byte sun_path limit; t.TempDir() alone
// exceeds it on macOS.
func shortSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "bdipc")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "s.sock")
}

func serve(t *testing.T, h Handler) string {
	t.Helper()
	path := shortSocketPath(t)
	srv, err := Listen(path, h)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { srv.Close() })
	return path
}

func TestCallRoundTripsAResult(t *testing.T) {
	path := serve(t, func(method string, params json.RawMessage) (any, error) {
		if method != "status" {
			return nil, &Error{Code: CodeUnknownMethod, Message: method}
		}
		return map[string]any{"name": "checkout-refactor"}, nil
	})

	var got struct {
		Name string `json:"name"`
	}
	if err := Call(path, "status", nil, &got); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if got.Name != "checkout-refactor" {
		t.Errorf("Name = %q", got.Name)
	}
}

func TestCallPassesParametersThrough(t *testing.T) {
	var seen string
	path := serve(t, func(method string, params json.RawMessage) (any, error) {
		var p struct {
			Target string `json:"target"`
		}
		_ = json.Unmarshal(params, &p)
		seen = p.Target
		return map[string]any{}, nil
	})

	if err := Call(path, "ack", map[string]any{"target": "worker-1"}, &struct{}{}); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if seen != "worker-1" {
		t.Errorf("target = %q, want worker-1", seen)
	}
}

// Errors need stable codes so an orchestrator can branch on them without
// parsing prose.
func TestHandlerErrorsCarryAStableCode(t *testing.T) {
	path := serve(t, func(string, json.RawMessage) (any, error) {
		return nil, &Error{Code: CodeCursorStale, Message: "cursor 3 is older than the retained history"}
	})

	err := Call(path, "events", nil, &struct{}{})
	if err == nil {
		t.Fatal("Call = nil error, want the handler's error")
	}
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("error = %v, want an *Error", err)
	}
	if e.Code != CodeCursorStale {
		t.Errorf("Code = %q, want %q", e.Code, CodeCursorStale)
	}
	if !strings.Contains(e.Message, "retained history") {
		t.Errorf("Message = %q, want the readable explanation preserved", e.Message)
	}
}

// A handler returning a plain error must still produce a coded response rather
// than leaking an uncategorised failure.
func TestPlainHandlerErrorGetsAnInternalCode(t *testing.T) {
	path := serve(t, func(string, json.RawMessage) (any, error) {
		return nil, errors.New("something broke")
	})

	err := Call(path, "status", nil, &struct{}{})
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("error = %v, want an *Error", err)
	}
	if e.Code != CodeInternal {
		t.Errorf("Code = %q, want %q", e.Code, CodeInternal)
	}
}

// The socket carries an operator's session state; nobody else on the machine
// needs to reach it.
func TestSocketIsOwnerOnly(t *testing.T) {
	path := serve(t, func(string, json.RawMessage) (any, error) { return struct{}{}, nil })

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("permissions = %04o, want 0600", perm)
	}
}

// An instance that is not running must say so plainly, not time out.
func TestCallToAMissingSocketFailsClearly(t *testing.T) {
	err := Call(filepath.Join(t.TempDir(), "absent.sock"), "status", nil, &struct{}{})
	if err == nil {
		t.Fatal("Call = nil error against a missing socket")
	}
	var e *Error
	if errors.As(err, &e) && e.Code != CodeNotRunning {
		t.Errorf("Code = %q, want %q", e.Code, CodeNotRunning)
	}
}

func TestClosingTheServerRemovesItsSocket(t *testing.T) {
	path := shortSocketPath(t)
	srv, err := Listen(path, func(string, json.RawMessage) (any, error) { return struct{}{}, nil })
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("socket still present after Close: %v", err)
	}
}

// A stale socket from a crashed daemon must not prevent a restart. The lock,
// not the socket file, is what decides whether an instance is already running.
func TestListenReplacesAStaleSocketFile(t *testing.T) {
	path := shortSocketPath(t)
	if err := os.WriteFile(path, []byte("not a socket"), 0o600); err != nil {
		t.Fatalf("write stale file: %v", err)
	}

	srv, err := Listen(path, func(string, json.RawMessage) (any, error) { return struct{}{}, nil })
	if err != nil {
		t.Fatalf("Listen over a stale socket file: %v", err)
	}
	defer srv.Close()

	if err := Call(path, "status", nil, &struct{}{}); err != nil {
		t.Errorf("Call after replacing a stale socket: %v", err)
	}
}

// Long polling: `events --wait` holds a request open, and must not block any
// other command against the same instance.
func TestSlowRequestDoesNotBlockOthers(t *testing.T) {
	release := make(chan struct{})
	path := serve(t, func(method string, _ json.RawMessage) (any, error) {
		if method == "wait" {
			<-release
		}
		return map[string]any{"method": method}, nil
	})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = Call(path, "wait", nil, &struct{}{})
	}()

	// The quick call must complete while the slow one is still outstanding.
	done := make(chan error, 1)
	go func() {
		done <- Call(path, "status", nil, &struct{}{})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("concurrent Call: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Error("a slow request blocked an unrelated one")
	}

	close(release)
	wg.Wait()
}

func TestServerSurvivesAClientDisconnectingMidRequest(t *testing.T) {
	path := serve(t, func(string, json.RawMessage) (any, error) { return map[string]any{"ok": true}, nil })

	// Connect and hang up without sending anything.
	c, err := dial(path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	c.Close()

	if err := Call(path, "status", nil, &struct{}{}); err != nil {
		t.Errorf("server did not survive an abandoned connection: %v", err)
	}
}
