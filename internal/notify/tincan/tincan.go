// Package tincan delivers birddog's alerts through the tincan CLI.
//
// It exists so birddog can alert an orchestrator that is not a Claude Code
// session. The claude-inbox connector speaks one runtime's inbox socket
// directly; tincan reaches Codex and opencode as well, through one adapter
// each, so an orchestrator in any of the three is reachable without birddog
// implementing three wire formats.
//
// The cost is a dependency on a binary birddog does not ship and cannot
// version-pin: tincan is installed separately, and can be upgraded or
// downgraded under a running birddog. So the connector probes when it is
// built and refuses loudly, rather than discovering it at the first alert.
//
// tincan 2.0.0 or newer is required. `send --help` exiting 0 is what the
// probe reads, and `duplicate_peer_moved` is what lets a duplicate be
// reported honestly; both arrived in that release.
package tincan

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/BrutalSystems/birddog/internal/notify"
)

// Kind is the configuration name for this connector.
const Kind = "tincan"

// defaultTimeout bounds one send.
//
// A connector that can hang stalls the observation pass behind it, which is
// worse than one that fails: birddog stops reporting every other target while
// it waits. tincan had a bug where `send` never exited at all when a codex
// CLI was on PATH; it is fixed, and this bound would have contained it. It is
// kept against any external binary, not that one.
const defaultTimeout = 10 * time.Second

// Runner executes the CLI. Injected so the connector is testable without
// tincan installed.
//
// stderr is returned separately and never merged: the send contract puts the
// result line on stdout and diagnostics on stderr, so merging them would
// break the parse. It is still returned because a refusal quotes it — the
// probe's own answer is its exit code, not anything it writes.
type Runner func(timeout time.Duration, name string, args ...string) (stdout, stderr []byte, exitCode int, err error)

// Connector delivers one alert per invocation of `tincan send`.
type Connector struct {
	// Peer is the orchestrator's tincan name.
	Peer string

	// Binary is the tincan executable, resolved on PATH unless overridden.
	Binary string

	Timeout time.Duration
	Run     Runner
}

// Name identifies the route in delivery records.
func (c *Connector) Name() string { return Kind }

// result is the JSON line `tincan send` prints on exits 0, 1 and 2.
//
// Exit 64 prints none: nothing was attempted, there is no message id, and no
// refusal describes a malformed command line, so a result-shaped line there
// would invite branching on a send that never happened.
type result struct {
	Outcome   string `json:"outcome"`
	Refusal   string `json:"refusal"`
	MessageID string `json:"message_id"`
	PeerState string `json:"peer_state"`
}

// idempotencyKey is stable for one incident across retries, which is what
// lets tincan refuse a second delivery of the same alert.
//
// The condition is deliberately not in the key: it does not change within an
// incident, and including it would only invite a future change to break
// de-duplication silently.
func (c *Connector) idempotencyKey(a notify.Alert) string {
	return a.InstanceID + "-" + strconv.FormatInt(a.IncidentID, 10)
}

// Deliver hands one alert to the CLI and maps its answer onto birddog's.
func (c *Connector) Deliver(a notify.Alert) (notify.Outcome, error) {
	timeout := c.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	binary := c.Binary
	if binary == "" {
		binary = "tincan"
	}

	// The reply route is birddog's own ack command, not a reply to the
	// message: an orchestrator answering down this channel would be answering
	// a one-shot process that has already exited.
	replyVia := fmt.Sprintf("birddog ack --instance %s --incident %d", a.InstanceID, a.IncidentID)

	stdout, stderr, code, err := c.Run(timeout, binary, "send",
		"--to", c.Peer,
		"--from", a.InstanceID,
		"--message", a.Text(),
		"--idempotency-key", c.idempotencyKey(a),
		"--reply-via", replyVia,
	)

	// The process did not return an exit status at all: killed at the
	// timeout, or never started. Whether the send landed is unknown, and
	// resending risks a duplicate the recipient cannot deduplicate.
	if err != nil && code < 0 {
		return notify.Unknown, fmt.Errorf("tincan send did not complete: %w", err)
	}

	if code == exitUsage {
		// birddog built the command line wrongly. Nothing arrived, which is
		// what Failed means; the error is what makes it findable.
		return notify.Failed, fmt.Errorf(
			"tincan send rejected birddog's own arguments (exit %d): %s",
			exitUsage, strings.TrimSpace(string(stderr)))
	}

	var r result
	if json.Unmarshal(stdout, &r) != nil || r.Outcome == "" {
		// Exits 0, 1 and 2 all carry a line. Without one birddog cannot tell
		// what happened, and guessing either way would claim something it
		// did not establish.
		return notify.Unknown, fmt.Errorf(
			"tincan send exited %d with no readable result: %.200q", code, string(stdout))
	}

	return outcomeOf(r, c.Peer)
}

// exitUsage is the code tincan uses for a malformed command line, distinct
// from every send outcome.
const exitUsage = 64

// outcomeOf maps tincan's answer onto birddog's.
//
// tincan's `accepted` and birddog's Delivered make the same claim — the
// destination accepted it — and neither means the session read it. Nothing
// here upgrades that.
func outcomeOf(r result, peer string) (notify.Outcome, error) {
	switch r.Outcome {
	case "accepted":
		return notify.Delivered, nil

	case "failed":
		return notify.Failed, fmt.Errorf("tincan could not deliver: %s", r.Refusal)

	case "rejected":
		switch r.Refusal {
		case "duplicate_send":
			// This alert already went out under the id this names, and from
			// tincan 2.0.0 it means the peer being addressed really does have
			// it: a retry whose name has since moved to a different session
			// is refused as duplicate_peer_moved instead.
			//
			// So it makes exactly the claim `accepted` makes, and is reported
			// the same way. Until that split existed this had to be Unknown,
			// because the two were indistinguishable and recording
			// `delivered` would have claimed a worker was told something it
			// may never have been told. The startup probe is what keeps that
			// true: a tincan that cannot draw the distinction never gets past
			// it.
			return notify.Delivered, nil

		case "duplicate_peer_moved":
			// Same key, but the name it was sent to now belongs to a
			// different session, so the session holding it never saw this
			// alert. Nothing was established. Not Delivered, for the reason
			// above; not Failed either, since something was sent and Failed
			// invites a retry that resolves identically. Unknown stops
			// further attempts and leaves the recovery to the event feed.
			return notify.Unknown, fmt.Errorf(
				"duplicate_peer_moved: this alert was already sent as %s, but the name %q has since "+
					"moved to a different session — so whichever session holds it now has not seen this",
				r.MessageID, peer)

		case "key_reused":
			// birddog's key derivation collided: this key is spent on a
			// different message, and nothing was sent. A birddog bug, not a
			// transport fault, and it must not be silent.
			return notify.Failed, fmt.Errorf(
				"birddog reused an idempotency key already spent on %s (key_reused)", r.MessageID)

		case "peer_unknown", "peer_ambiguous", "peer_unreachable", "peer_changed", "self_send":
			// The recipient could not be resolved, and would resolve the
			// same way next time. Retrying spends the attempt budget on a
			// configuration error and then reports it as a transport fault.
			return notify.NoRecipient, fmt.Errorf("tincan could not reach %q: %s", peer, r.Refusal)

		default:
			// A refusal this build does not know. Reported as uncertain
			// rather than mapped to the nearest familiar value.
			return notify.Unknown, fmt.Errorf("tincan refused with an unrecognised reason: %s", r.Refusal)
		}
	}
	return notify.Unknown, fmt.Errorf("tincan returned an unrecognised outcome: %s", r.Outcome)
}
