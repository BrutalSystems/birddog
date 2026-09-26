package daemon

import (
	"encoding/json"
	"time"
)

// These types are the control channel's contract. They are shared by the
// daemon and the commands, so the JSON an orchestrator reads is defined once.

// StatusResult is a snapshot of an instance.
type StatusResult struct {
	InstanceID string         `json:"instance_id"`
	Name       string         `json:"name"`
	PID        int            `json:"pid"`
	Targets    []TargetStatus `json:"targets"`

	// Cursor is the newest event position at the time of the snapshot, so a
	// consumer can follow from here with no gap. Store identifies the history
	// it belongs to: a cursor is only meaningful within one store, and a
	// consumer that keeps one should keep both.
	Cursor int64  `json:"cursor"`
	Store  string `json:"store,omitempty"`

	// Machine identifies the machine this instance observes from. A cursor is
	// a position in one store on one machine, and a consumer holding feeds
	// from several hosts needs both to compare them. Absent in local mode.
	Machine string `json:"machine,omitempty"`

	ObservedAt time.Time `json:"observed_at"`

	// ConfigRevision identifies the watch list these targets were read under.
	ConfigRevision int64 `json:"config_revision"`

	// Owner is the session this instance was started for, when one was named.
	// The instance stops when that session is gone; empty means it outlives
	// everything and waits to be stopped explicitly.
	Owner string `json:"owner,omitempty"`
}

// TargetStatus is what is known about one watched session.
type TargetStatus struct {
	ID       string            `json:"id"`
	Provider string            `json:"provider"`
	Labels   map[string]string `json:"labels,omitempty"`

	SessionID string `json:"session_id,omitempty"`

	// Machine qualifies the session reference. A session id plus a process
	// start time identifies a session on its own machine and nowhere else.
	// A sibling field, never concatenated into the id.
	Machine string `json:"machine,omitempty"`

	Generation int64  `json:"generation,omitempty"`
	Status     string `json:"status,omitempty"`
	Source     string `json:"source,omitempty"`

	// StatusIsCurrent says whether Status may be read as a present fact.
	// When false it is the last thing observed, and it is stale.
	StatusIsCurrent bool       `json:"status_is_current"`
	ObservedAt      *time.Time `json:"observed_at,omitempty"`

	// InputRequestVisibility is observed, not_observed or unavailable. It
	// never reports that a worker is unblocked.
	InputRequestVisibility string `json:"input_request_visibility"`

	// Reason says why this is not a current, readable answer: contact_lost,
	// source_unreadable, status_unrecognised or provider_limited. Absent when
	// the status is current and readable.
	//
	// provider_limited is the one that will not change by asking again: this
	// cannot be observed from outside for this provider, at any version.
	Reason string `json:"reason,omitempty"`

	// ExpectedQuietUntil, when set, suppresses quiet and idle for this target.
	// It suppresses nothing else, and changes nothing about what is observed.
	ExpectedQuietUntil  *time.Time `json:"expected_quiet_until,omitempty"`
	ExpectedQuietReason string     `json:"expected_quiet_reason,omitempty"`

	Incidents []IncidentView `json:"incidents,omitempty"`
}

// IncidentView is one open attention condition.
type IncidentView struct {
	ID             int64      `json:"id"`
	Condition      string     `json:"condition"`
	Generation     int64      `json:"generation"`
	OpenedAt       time.Time  `json:"opened_at"`
	LastSeenAt     time.Time  `json:"last_seen_at"`
	NotifiedAt     *time.Time `json:"notified_at,omitempty"`
	AcknowledgedAt *time.Time `json:"acknowledged_at,omitempty"`
}

// EventsParams asks for events after a cursor.
type EventsParams struct {
	After int64 `json:"after"`
	Limit int   `json:"limit,omitempty"`

	// Store is the identity the cursor came from. Supplying it turns a cursor
	// into a position in a specific history: if this instance's store has
	// since been replaced, the request fails with store_replaced instead of
	// returning the empty page that would read as silence.
	//
	// Optional. A caller that sends none gets the behaviour it had before
	// identities existed.
	Store string `json:"store,omitempty"`

	// Machine is the machine the cursor came from. Supplying it means this
	// instance refuses the request rather than answering from a history that
	// belongs to a different host. Optional; a caller that sends none gets the
	// behaviour it had before identities existed.
	Machine string `json:"machine,omitempty"`

	// WaitSeconds bounds a long poll. Zero returns immediately.
	WaitSeconds int `json:"wait_seconds,omitempty"`
}

// EventsResult is a page of events and the cursor to continue from.
type EventsResult struct {
	Events []EventView `json:"events"`
	Cursor int64       `json:"cursor"`

	// Store identifies the history these cursors belong to. Carry it back
	// with the cursor; a cursor alone is only meaningful within one store.
	Store string `json:"store,omitempty"`

	// Machine identifies the machine these cursors belong to. Carry it back
	// with the cursor and the store; the three together are a position.
	Machine string `json:"machine,omitempty"`

	// WaitSeconds is the wait actually applied, which is not always the wait
	// asked for: it is bounded by MaxWaitSeconds. WaitClamped says so
	// explicitly, because returning early while reporting nothing is
	// indistinguishable from having waited and seen no events.
	WaitSeconds int  `json:"wait_seconds,omitempty"`
	WaitClamped bool `json:"wait_clamped,omitempty"`
}

// EventView is one recorded observation.
type EventView struct {
	Cursor     int64     `json:"cursor"`
	InstanceID string    `json:"instance_id"`
	TargetID   string    `json:"target_id"`
	Type       string    `json:"type"`
	Source     string    `json:"source"`
	At         time.Time `json:"at"`
	SessionID  string    `json:"session_id,omitempty"`

	// Machine is where this observation was made. It travels with the event,
	// so it is still true once the event leaves the machine that recorded it.
	Machine string `json:"machine,omitempty"`

	Generation int64           `json:"generation,omitempty"`
	Status     string          `json:"status,omitempty"`
	Reason     string          `json:"reason,omitempty"`
	Evidence   json.RawMessage `json:"evidence,omitempty"`
}

// AckParams names the incident the orchestrator has seen.
type AckParams struct {
	IncidentID int64 `json:"incident_id"`

	// IdempotencyKey lets a caller retry safely; see WatchAddParams.
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

// AckResult confirms the acknowledgement was recorded, and says plainly that
// it changed nothing else.
type AckResult struct {
	IncidentID   int64  `json:"incident_id"`
	Acknowledged bool   `json:"acknowledged"`
	Note         string `json:"note"`
}

// StopResult confirms an instance is stopping.
type StopResult struct {
	Stopping bool `json:"stopping"`

	// WatchedSessionsUnaffected is always true, and is stated in the response
	// so the guarantee is visible at the point of use.
	WatchedSessionsUnaffected bool `json:"watched_sessions_unaffected"`
}
