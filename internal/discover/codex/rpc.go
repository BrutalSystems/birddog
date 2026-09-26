package codex

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// rpcConn speaks the Codex app-server's line protocol: JSON-RPC 2.0 semantics
// with the "jsonrpc" member omitted, one message per line.
//
// A single reader goroutine owns the response stream, because the server
// interleaves notifications (messages with no id) with replies. Calls register
// a waiter by id and take only their own response.
type rpcConn struct {
	w  io.WriteCloser
	r  io.ReadCloser
	mu sync.Mutex

	nextID  int
	waiters map[int]chan rpcMessage

	closeOnce sync.Once
	done      chan struct{}
	readErr   error
}

type rpcMessage struct {
	ID     *int            `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("codex app-server: %s (%d)", e.Message, e.Code) }

// maxLine bounds one protocol line. A thread listing is metadata, not a
// transcript, so anything larger is a malformed stream rather than real data.
const maxLine = 8 << 20

func newRPCConn(w io.WriteCloser, r io.ReadCloser) *rpcConn {
	c := &rpcConn{
		w:       w,
		r:       r,
		nextID:  1,
		waiters: make(map[int]chan rpcMessage),
		done:    make(chan struct{}),
	}
	go c.readLoop()
	return c
}

func (c *rpcConn) readLoop() {
	defer close(c.done)

	sc := bufio.NewScanner(c.r)
	sc.Buffer(make([]byte, 0, 64<<10), maxLine)
	for sc.Scan() {
		var m rpcMessage
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			continue // not a protocol message; the server also logs to stdout
		}
		if m.ID == nil {
			continue // a notification: no call is waiting on it
		}

		c.mu.Lock()
		ch, ok := c.waiters[*m.ID]
		delete(c.waiters, *m.ID)
		c.mu.Unlock()

		if ok {
			ch <- m
		}
	}
	c.readErr = sc.Err()
}

// Call sends one request and waits for the response carrying its id.
func (c *rpcConn) Call(method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	id := c.nextID
	c.nextID++
	ch := make(chan rpcMessage, 1)
	c.waiters[id] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.waiters, id)
		c.mu.Unlock()
	}()

	req := map[string]any{"id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	line, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", method, err)
	}
	if _, err := c.w.Write(append(line, '\n')); err != nil {
		return nil, fmt.Errorf("send %s: %w", method, err)
	}

	select {
	case m := <-ch:
		if m.Error != nil {
			return nil, m.Error
		}
		return m.Result, nil
	case <-c.done:
		// The stream ended before a reply arrived: the server exited, or the
		// connection was closed. Either way this call has no answer.
		if c.readErr != nil {
			return nil, fmt.Errorf("%s: %w", method, c.readErr)
		}
		return nil, fmt.Errorf("%s: %w", method, errors.New("app-server closed the connection"))
	}
}

func (c *rpcConn) Close() error {
	c.closeOnce.Do(func() {
		_ = c.r.Close()
		_ = c.w.Close()
	})
	return nil
}
