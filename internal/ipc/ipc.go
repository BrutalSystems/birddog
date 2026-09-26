// Package ipc is birddog's private local control channel.
//
// One JSON request and one JSON response per connection, over a unix socket
// owned by the operator. Nothing is exposed on the network: an instance is
// reachable only by someone who can already read the socket.
//
// The transport is deliberately behind this small interface so a Windows named
// pipe can replace it without the commands or the daemon noticing.
package ipc

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

// Stable error codes. An orchestrator branches on these; the message is for a
// human and may be reworded.
const (
	CodeNotRunning    = "not_running"
	CodeUnknownMethod = "unknown_method"
	CodeBadRequest    = "bad_request"
	CodeNotFound      = "not_found"
	CodeCursorStale   = "cursor_stale"
	CodeStoreReplaced = "store_replaced"
	// CodeMachineMismatch: the position belongs to a different machine, or to
	// one this instance cannot confirm it is.
	CodeMachineMismatch = "machine_mismatch"
	CodeInternal        = "internal"
)

// Error is a coded failure.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }

// Handler answers one request.
type Handler func(method string, params json.RawMessage) (any, error)

type request struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

type response struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

// maxRequest bounds one request. Control messages are small; anything larger
// is a mistake rather than a message.
const maxRequest = 4 << 20

// Server listens for control connections.
type Server struct {
	ln   net.Listener
	path string

	closeOnce sync.Once
	wg        sync.WaitGroup
}

// Listen starts a control server on a unix socket.
//
// A leftover socket file from a crashed daemon is replaced rather than treated
// as a conflict: the instance lock decides whether an instance is already
// running, not the presence of a file.
func Listen(path string, h Handler) (*Server, error) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("clear stale socket: %w", err)
	}

	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, fmt.Errorf("restrict socket: %w", err)
	}

	s := &Server{ln: ln, path: path}
	s.wg.Add(1)
	go s.accept(h)
	return s, nil
}

func (s *Server) accept(h Handler) {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return // listener closed
		}
		// Each connection is served independently, so a long poll holds up
		// nothing else against this instance.
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			serveConn(conn, h)
		}()
	}
}

func serveConn(conn net.Conn, h Handler) {
	defer conn.Close()

	line, err := bufio.NewReaderSize(conn, 64<<10).ReadBytes('\n')
	if err != nil {
		// A client that connected and hung up is ordinary, not an error the
		// server should care about.
		if !errors.Is(err, io.EOF) || len(line) == 0 {
			return
		}
	}
	if len(line) > maxRequest {
		writeResponse(conn, response{Error: &Error{Code: CodeBadRequest, Message: "request too large"}})
		return
	}

	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		writeResponse(conn, response{Error: &Error{Code: CodeBadRequest, Message: err.Error()}})
		return
	}

	result, err := h(req.Method, req.Params)
	if err != nil {
		writeResponse(conn, response{Error: asError(err)})
		return
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		writeResponse(conn, response{Error: &Error{Code: CodeInternal, Message: err.Error()}})
		return
	}
	writeResponse(conn, response{Result: encoded})
}

// asError keeps a coded error's code, and gives an uncategorised one a code
// rather than letting it escape uncategorised.
func asError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return &Error{Code: CodeInternal, Message: err.Error()}
}

func writeResponse(w io.Writer, r response) {
	b, err := json.Marshal(r)
	if err != nil {
		b, _ = json.Marshal(response{Error: &Error{Code: CodeInternal, Message: "response could not be encoded"}})
	}
	_, _ = w.Write(append(b, '\n'))
}

// Close stops the server and removes its socket.
func (s *Server) Close() error {
	var err error
	s.closeOnce.Do(func() {
		err = s.ln.Close()
		_ = os.Remove(s.path)
	})
	return err
}

// Path returns the socket the server is listening on.
func (s *Server) Path() string { return s.path }

func dial(path string) (net.Conn, error) {
	conn, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return nil, &Error{
			Code:    CodeNotRunning,
			Message: fmt.Sprintf("no instance is listening on %s", path),
		}
	}
	return conn, nil
}

// Call sends one request and decodes the result.
//
// There is no deadline on the response: a long poll is a supported shape, and
// bounding it here would cut off exactly the request that means to wait.
func Call(path, method string, params, result any) error {
	conn, err := dial(path)
	if err != nil {
		return err
	}
	defer conn.Close()

	req := request{Method: method}
	if params != nil {
		encoded, err := json.Marshal(params)
		if err != nil {
			return &Error{Code: CodeBadRequest, Message: err.Error()}
		}
		req.Params = encoded
	}
	line, err := json.Marshal(req)
	if err != nil {
		return &Error{Code: CodeBadRequest, Message: err.Error()}
	}
	if _, err := conn.Write(append(line, '\n')); err != nil {
		return &Error{Code: CodeNotRunning, Message: err.Error()}
	}

	respLine, err := bufio.NewReaderSize(conn, 4<<20).ReadBytes('\n')
	if err != nil && len(respLine) == 0 {
		return &Error{Code: CodeNotRunning, Message: "instance closed the connection without replying"}
	}

	var resp response
	if err := json.Unmarshal(respLine, &resp); err != nil {
		return &Error{Code: CodeInternal, Message: fmt.Sprintf("unreadable response: %v", err)}
	}
	if resp.Error != nil {
		return resp.Error
	}
	if result != nil && len(resp.Result) > 0 {
		if err := json.Unmarshal(resp.Result, result); err != nil {
			return &Error{Code: CodeInternal, Message: fmt.Sprintf("unreadable result: %v", err)}
		}
	}
	return nil
}
