// Package claudeinbox delivers birddog's alerts into a Claude Code session's
// inbox.
//
// This is the route for an orchestrator that is itself a Claude Code session —
// the usual case, and the one tincan explicitly declines, since it reaches
// Codex and opencode only. So the delivery path is implemented here rather
// than borrowed.
//
// Written against the wire format of Claude Code 2.1.267. It is not a
// published interface, so it may change; the connector fails loudly rather
// than quietly when it does.
//
// birddog only ever sends alerts, only to the orchestrator that configured it,
// and only queued for the next turn boundary. It never reaches a watched
// worker: nothing here takes a target's identity as a destination.
package claudeinbox

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/BrutalSystems/birddog/internal/notify"
)

// Kind is the configuration name for this connector.
const Kind = "claude-inbox"

// defaultReceiptWindow is how long to wait for the session to acknowledge.
// Long enough for a local socket round trip, short enough that an observation
// pass is never held up by a recipient that is not answering.
const defaultReceiptWindow = 500 * time.Millisecond

// Recipient is the orchestrator session an instance reports to.
type Recipient struct {
	SocketPath string
	PeerToken  string
	ProcStart  string
	PIDDomain  string
}

// Connector delivers alerts to one Claude Code session.
type Connector struct {
	// Resolve finds the recipient afresh for each delivery. A session's pid,
	// socket and token all change when it restarts, so resolving once at
	// startup would deliver to a session that no longer exists.
	Resolve func() (Recipient, error)

	ReceiptWindow time.Duration
}

// Name identifies the route in delivery records.
func (c *Connector) Name() string { return Kind }

// authFrame authenticates birddog to the session.
type authFrame struct {
	Type      string `json:"type"`
	PeerToken string `json:"peerToken"`
	ProcStart string `json:"procStart,omitempty"`
	PIDDomain string `json:"pidDomain,omitempty"`
}

// userFrame carries the alert. priority "next" queues it for the turn
// boundary, which is what makes delivery non-disruptive.
type userFrame struct {
	Type     string        `json:"type"`
	Message  userFrameBody `json:"message"`
	Priority string        `json:"priority"`
	MsgID    string        `json:"msg_id"`
}

type userFrameBody struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// statusFrame is the session acknowledging a message.
type statusFrame struct {
	Type      string `json:"type"`
	OrigMsgID string `json:"orig_msg_id"`
	Status    string `json:"status"`
	Detail    string `json:"detail"`
}

// Deliver sends one alert.
//
// The three outcomes are decided by what the attempt actually established, not
// by what is convenient:
//
//   - Failed when the session could not be reached or resolved. Nothing was
//     sent, so a retry cannot duplicate.
//   - Delivered when the message went out and the connection stayed healthy.
//     This route has no positive acknowledgement — the session replies only to
//     report a hold — so silence is how it accepts, and a hold counts too: the
//     message was received, and its human may release it.
//   - Unknown when the write went out and the connection then broke before the
//     bytes could have been read. Whether it arrived is unknowable, which is
//     the case criterion 18 exists for: neither resent nor rerouted.
//
// Treating ordinary silence as uncertain was the first design here, and live
// delivery to a real session disproved it: the alert arrived and nothing came
// back, so every successful delivery would have been recorded "unknown"
// forever — never marked notified, and never retried either.
func (c *Connector) Deliver(a notify.Alert) (notify.Outcome, error) {
	recipient, err := c.Resolve()
	if err != nil {
		// Nobody to deliver to, rather than a delivery that failed. The next
		// attempt resolves identically, so retrying would spend the budget on
		// a configuration error and then report it as a transport fault.
		return notify.NoRecipient, fmt.Errorf("resolve orchestrator session: %w", err)
	}

	window := c.ReceiptWindow
	if window <= 0 {
		window = defaultReceiptWindow
	}

	conn, err := net.DialTimeout("unix", recipient.SocketPath, 2*time.Second)
	if err != nil {
		// Refused or absent: the session is gone. Definitely not delivered.
		return notify.Failed, fmt.Errorf("orchestrator session unreachable: %w", err)
	}
	defer conn.Close()

	// The session closes an idle connection, so connect only when the text is
	// ready and write immediately.
	if recipient.PeerToken != "" {
		if err := writeFrame(conn, authFrame{
			Type:      "auth",
			PeerToken: recipient.PeerToken,
			ProcStart: recipient.ProcStart,
			PIDDomain: recipient.PIDDomain,
		}); err != nil {
			return notify.Failed, fmt.Errorf("authenticate to orchestrator session: %w", err)
		}
	}

	msgID := messageID(a)
	if err := writeFrame(conn, userFrame{
		Type:     "user",
		Message:  userFrameBody{Role: "user", Content: a.Text()},
		Priority: "next",
		MsgID:    msgID,
	}); err != nil {
		// The write failed, so nothing arrived.
		return notify.Failed, fmt.Errorf("send alert: %w", err)
	}

	// Settle before closing. A write to a unix socket only buffers the bytes,
	// so hanging up immediately could drop them before the session reads.
	return settle(conn, window), nil
}

// settle holds the connection open briefly after the write, and reports what
// that established.
//
// A read deadline expiring is the ordinary case: the session accepted the
// message and has nothing to say about it. A connection that breaks instead
// means the bytes may never have been read.
func settle(conn net.Conn, window time.Duration) notify.Outcome {
	if err := conn.SetReadDeadline(time.Now().Add(window)); err != nil {
		return notify.Unknown
	}

	reader := bufio.NewReader(conn)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				// The window elapsed with the connection healthy: accepted.
				return notify.Delivered
			}
			// EOF or a read error: the session went away mid-delivery, and
			// whether it read the message first is unknowable.
			return notify.Unknown
		}

		var frame statusFrame
		if json.Unmarshal(line, &frame) != nil {
			continue
		}
		// A hold is receipt, not refusal: the message arrived and its human
		// may release it.
		if frame.Type == "peer_message_status" {
			return notify.Delivered
		}
	}
}

func writeFrame(conn net.Conn, frame any) error {
	line, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	if err := conn.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return err
	}
	_, err = conn.Write(append(line, '\n'))
	return err
}

// messageID is stable per incident, so a destination able to deduplicate has
// what it needs, and a retry of the same alert is recognisable as one.
func messageID(a notify.Alert) string {
	return fmt.Sprintf("birddog_%s_%d", a.InstanceID, a.IncidentID)
}

// errNoSession reports that the configured orchestrator could not be found.
var errNoSession = errors.New("session not found in the Claude Code registry")
