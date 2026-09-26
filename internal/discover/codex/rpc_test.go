package codex

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// fakePeer runs a scripted app-server over pipes: it reads one request per
// line and replies with whatever handler returns. Returns the conn under test.
func fakePeer(t *testing.T, handler func(req map[string]any) string) *rpcConn {
	t.Helper()
	reqR, reqW := io.Pipe()   // conn writes requests here
	respR, respW := io.Pipe() // conn reads responses from here

	go func() {
		defer respW.Close()
		sc := bufio.NewScanner(reqR)
		for sc.Scan() {
			var req map[string]any
			if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
				continue
			}
			if reply := handler(req); reply != "" {
				if _, err := io.WriteString(respW, reply+"\n"); err != nil {
					return
				}
			}
		}
	}()

	c := newRPCConn(reqW, respR)
	t.Cleanup(func() { c.Close(); reqW.Close() })
	return c
}

// echoResult replies to every request with the same result object.
func echoResult(result string) func(map[string]any) string {
	return func(req map[string]any) string {
		return `{"id":` + jsonNum(req["id"]) + `,"result":` + result + `}`
	}
}

func jsonNum(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestCallReturnsTheResultForItsRequest(t *testing.T) {
	c := fakePeer(t, echoResult(`{"data":[]}`))

	got, err := c.Call("thread/list", map[string]any{"limit": 50})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if string(got) != `{"data":[]}` {
		t.Errorf("result = %s", got)
	}
}

func TestCallSendsMethodAndParams(t *testing.T) {
	var seen map[string]any
	c := fakePeer(t, func(req map[string]any) string {
		seen = req
		return `{"id":` + jsonNum(req["id"]) + `,"result":{}}`
	})

	if _, err := c.Call("thread/list", map[string]any{"limit": 50}); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if seen["method"] != "thread/list" {
		t.Errorf("method = %v", seen["method"])
	}
	params, _ := seen["params"].(map[string]any)
	if params["limit"] != float64(50) {
		t.Errorf("params = %v", seen["params"])
	}
}

// The app-server streams notifications — messages with no id — alongside
// responses. Treating one as a reply would return the wrong thing, or hang.
func TestCallIgnoresNotificationsWhileWaiting(t *testing.T) {
	c := fakePeer(t, func(req map[string]any) string {
		id := jsonNum(req["id"])
		return `{"method":"thread/status/changed","params":{"threadId":"x"}}` + "\n" +
			`{"method":"item/started","params":{}}` + "\n" +
			`{"id":` + id + `,"result":{"data":[{"id":"real"}]}}`
	})

	got, err := c.Call("thread/list", nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !strings.Contains(string(got), "real") {
		t.Errorf("result = %s, want the response not a notification", got)
	}
}

// Responses may arrive out of order; each call must get its own.
func TestCallMatchesResponsesByID(t *testing.T) {
	c := fakePeer(t, func(req map[string]any) string {
		id := jsonNum(req["id"])
		// Reply to a request that was never made, then the real one.
		return `{"id":999,"result":{"wrong":true}}` + "\n" +
			`{"id":` + id + `,"result":{"right":true}}`
	})

	got, err := c.Call("thread/list", nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !strings.Contains(string(got), "right") {
		t.Errorf("result = %s, want the response matching this request's id", got)
	}
}

func TestCallSurfacesRPCErrors(t *testing.T) {
	c := fakePeer(t, func(req map[string]any) string {
		return `{"id":` + jsonNum(req["id"]) + `,"error":{"code":-32600,"message":"nope"}}`
	})

	_, err := c.Call("thread/list", nil)
	if err == nil {
		t.Fatal("Call = nil error, want the RPC error surfaced")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("error = %v, want it to carry the server's message", err)
	}
}

func TestCallFailsWhenTheServerGoesAway(t *testing.T) {
	c := fakePeer(t, func(map[string]any) string { return "" }) // never replies

	// Closing the response side ends the wait rather than hanging forever.
	c.Close()
	if _, err := c.Call("thread/list", nil); err == nil {
		t.Error("Call = nil error, want a failure once the server is gone")
	}
}

func TestCallGivesEachRequestADistinctID(t *testing.T) {
	var ids []any
	c := fakePeer(t, func(req map[string]any) string {
		ids = append(ids, req["id"])
		return `{"id":` + jsonNum(req["id"]) + `,"result":{}}`
	})

	for range 3 {
		if _, err := c.Call("thread/list", nil); err != nil {
			t.Fatalf("Call: %v", err)
		}
	}
	if len(ids) != 3 {
		t.Fatalf("saw %d requests, want 3", len(ids))
	}
	if ids[0] == ids[1] || ids[1] == ids[2] || ids[0] == ids[2] {
		t.Errorf("ids not distinct: %v", ids)
	}
}
