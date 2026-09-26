package codex

import (
	"encoding/json"
	"fmt"
	"os/exec"
)

// version identifies birddog to the app-server.
const version = "0.1.0"

// Client is a connection to a Codex app-server child process.
//
// The app-server on codex-cli 0.155.1 does not serve external observers
// through any socket, so reading shared thread state means running one. That
// is a cost, not a design preference: one long-lived child per connection.
//
// The child is birddog's own. It does not attach to, resume, or otherwise
// touch the sessions it reports on.
type Client struct {
	cmd  *exec.Cmd
	conn *rpcConn
}

// Call satisfies Caller.
func (c *Client) Call(method string, params any) (json.RawMessage, error) {
	return c.conn.Call(method, params)
}

// Connect spawns a Codex app-server and completes the handshake.
func Connect() (*Client, error) {
	// argv-based, never a shell.
	cmd := exec.Command("codex", "app-server", "--listen", "stdio://")

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("codex app-server stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("codex app-server stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start codex app-server: %w", err)
	}

	client := &Client{cmd: cmd, conn: newRPCConn(stdin, stdout)}
	if err := initialize(client); err != nil {
		client.Close()
		return nil, err
	}
	return client, nil
}

// Close shuts down the app-server child.
func (c *Client) Close() error {
	if c.conn != nil {
		c.conn.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		_ = c.cmd.Wait()
	}
	return nil
}

// initialize performs the app-server handshake.
//
// It deliberately does not request experimentalApi. That capability gates the
// thread/queue/* family — the way to put input into another session — and
// birddog only observes. Asking for it would acquire an authority the product
// boundary forbids, whether or not it were ever used.
func initialize(c Caller) error {
	_, err := c.Call("initialize", map[string]any{
		"clientInfo": map[string]any{
			"name":    "birddog",
			"title":   "birddog",
			"version": version,
		},
		"capabilities": map[string]any{
			"experimentalApi":    false,
			"requestAttestation": false,
		},
	})
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	return nil
}
