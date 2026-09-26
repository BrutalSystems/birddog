package codex

import (
	"encoding/json"
	"fmt"
)

// Caller performs one JSON-RPC call against a Codex app-server.
type Caller interface {
	Call(method string, params any) (json.RawMessage, error)
}

// Thread is one Codex thread as the shared state database describes it.
//
// Metadata only: whether the thread is live comes from its writer lock, not
// from appearing in this listing. Threads that ended long ago are listed too.
type Thread struct {
	ID                   string
	Name                 string
	CWD                  string
	Status               string
	Ephemeral            bool
	CanAcceptDirectInput bool
}

// threadListLimit bounds one listing. Codex pages; birddog is not trying to
// enumerate history, only to describe what is running now.
const threadListLimit = 50

// ListThreads reads thread metadata from the shared state database.
//
// useStateDbOnly is not optional: thread/loaded/list, and thread/list without
// it, report only threads loaded in the *calling* process — which for an
// external observer means nothing at all.
//
// This is a read. It does not resume a conversation, start a turn, or load a
// thread into this process.
func ListThreads(c Caller) ([]Thread, error) {
	raw, err := c.Call("thread/list", map[string]any{
		"limit":          threadListLimit,
		"useStateDbOnly": true,
	})
	if err != nil {
		return nil, fmt.Errorf("thread/list: %w", err)
	}

	var res struct {
		Data []struct {
			ID                   string          `json:"id"`
			Name                 string          `json:"name"`
			CWD                  string          `json:"cwd"`
			Status               json.RawMessage `json:"status"`
			Ephemeral            bool            `json:"ephemeral"`
			CanAcceptDirectInput bool            `json:"canAcceptDirectInput"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("thread/list result: %w", err)
	}

	out := make([]Thread, 0, len(res.Data))
	for _, d := range res.Data {
		out = append(out, Thread{
			ID:                   d.ID,
			Name:                 d.Name,
			CWD:                  d.CWD,
			Status:               statusTag(d.Status),
			Ephemeral:            d.Ephemeral,
			CanAcceptDirectInput: d.CanAcceptDirectInput,
		})
	}
	return out, nil
}

// statusTag reads a status reported either as a bare string or as a tagged
// object. An unrecognised shape yields "" rather than a guess.
func statusTag(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var tagged struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &tagged); err == nil {
		return tagged.Type
	}
	return ""
}
