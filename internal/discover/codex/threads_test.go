package codex

import (
	"encoding/json"
	"errors"
	"testing"
)

// fakeCaller returns canned JSON-RPC results, so thread parsing is tested
// without spawning a codex app-server.
type fakeCaller struct {
	method string
	params map[string]any
	result string
	err    error
}

func (f *fakeCaller) Call(method string, params any) (json.RawMessage, error) {
	f.method = method
	b, _ := json.Marshal(params)
	_ = json.Unmarshal(b, &f.params)
	if f.err != nil {
		return nil, f.err
	}
	return json.RawMessage(f.result), nil
}

func TestListThreadsParsesThreadRecords(t *testing.T) {
	c := &fakeCaller{result: `{"data":[
		{"id":"01a0c3ae","name":"auth-refactor","cwd":"/work/api","status":"idle","canAcceptDirectInput":true},
		{"id":"01a0c46d","name":"billing","cwd":"/work/billing","status":"running","canAcceptDirectInput":false}
	]}`}

	got, err := ListThreads(c)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d threads, want 2", len(got))
	}
	if got[0].ID != "01a0c3ae" || got[0].Name != "auth-refactor" || got[0].CWD != "/work/api" {
		t.Errorf("first thread = %+v", got[0])
	}
	if got[0].Status != "idle" {
		t.Errorf("Status = %q, want idle", got[0].Status)
	}
	if !got[0].CanAcceptDirectInput {
		t.Error("CanAcceptDirectInput = false, want true")
	}
}

func TestListThreadsReadsTheStateDBWithoutLoadingThreads(t *testing.T) {
	c := &fakeCaller{result: `{"data":[]}`}
	if _, err := ListThreads(c); err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if c.method != "thread/list" {
		t.Errorf("method = %q, want thread/list", c.method)
	}
	// thread/loaded/list only sees threads loaded in the calling process, so
	// the state DB is the only source that reports other sessions.
	if v, ok := c.params["useStateDbOnly"].(bool); !ok || !v {
		t.Errorf("params = %v, want useStateDbOnly true", c.params)
	}
}

// Codex reports status either as a bare string or as a tagged object.
func TestListThreadsAcceptsTaggedStatusObject(t *testing.T) {
	c := &fakeCaller{result: `{"data":[{"id":"x","status":{"type":"running"}}]}`}
	got, err := ListThreads(c)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if got[0].Status != "running" {
		t.Errorf("Status = %q, want running", got[0].Status)
	}
}

func TestListThreadsLeavesStatusEmptyWhenAbsent(t *testing.T) {
	c := &fakeCaller{result: `{"data":[{"id":"x"}]}`}
	got, err := ListThreads(c)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if got[0].Status != "" {
		t.Errorf("Status = %q, want empty when the record carries none", got[0].Status)
	}
}

func TestListThreadsReturnsEmptyForNoData(t *testing.T) {
	c := &fakeCaller{result: `{}`}
	got, err := ListThreads(c)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d threads, want 0", len(got))
	}
}

func TestListThreadsPropagatesCallFailure(t *testing.T) {
	c := &fakeCaller{err: errors.New("app-server gone")}
	if _, err := ListThreads(c); err == nil {
		t.Error("ListThreads = nil error, want the call failure surfaced")
	}
}
