package codex

import (
	"encoding/json"
	"os/exec"
	"testing"
)

// birddog only ever reads. The experimentalApi capability gates Codex's
// thread/queue/* family — the way to put input into somebody else's session —
// so an observer must not ask for it. Requesting it would hand birddog an
// authority its product boundary says it must not have.
func TestInitializeDoesNotRequestWriteCapabilities(t *testing.T) {
	var seen map[string]any
	c := fakePeer(t, func(req map[string]any) string {
		if req["method"] == "initialize" {
			seen, _ = req["params"].(map[string]any)
		}
		return `{"id":` + jsonNum(req["id"]) + `,"result":{}}`
	})

	if err := initialize(c); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	caps, _ := seen["capabilities"].(map[string]any)
	if v, ok := caps["experimentalApi"].(bool); ok && v {
		t.Errorf("initialize requested experimentalApi: %v — an observer must not ask for write capability", caps)
	}
}

func TestInitializeIdentifiesBirddog(t *testing.T) {
	var seen map[string]any
	c := fakePeer(t, func(req map[string]any) string {
		if req["method"] == "initialize" {
			seen, _ = req["params"].(map[string]any)
		}
		return `{"id":` + jsonNum(req["id"]) + `,"result":{}}`
	})

	if err := initialize(c); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	info, _ := seen["clientInfo"].(map[string]any)
	if info["name"] != "birddog" {
		t.Errorf("clientInfo = %v, want name birddog so the peer can attribute the connection", info)
	}
}

func TestInitializeSurfacesRejection(t *testing.T) {
	c := fakePeer(t, func(req map[string]any) string {
		return `{"id":` + jsonNum(req["id"]) + `,"error":{"code":-32600,"message":"unsupported client"}}`
	})
	if err := initialize(c); err == nil {
		t.Error("initialize = nil error, want the rejection surfaced")
	}
}

// Integration: spawn the installed codex and read real threads. Skipped when
// codex is not on PATH, so the suite stays runnable anywhere.
func TestConnectAgainstInstalledCodex(t *testing.T) {
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex not installed")
	}

	client, err := Connect()
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer client.Close()

	threads, err := ListThreads(client)
	if err != nil {
		t.Fatalf("ListThreads against installed codex: %v", err)
	}
	for _, th := range threads {
		if th.ID == "" {
			b, _ := json.Marshal(th)
			t.Errorf("thread with no id: %s", b)
		}
	}
	t.Logf("installed codex reported %d threads", len(threads))
}
