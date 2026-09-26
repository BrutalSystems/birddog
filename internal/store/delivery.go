package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Delivery is what is known about telling the orchestrator about an incident.
type Delivery struct {
	IncidentID int64
	Transport  string
	Outcome    string
	Attempts   int
	LastAt     time.Time
}

// Outcome values, matching notify.Outcome.String().
const (
	OutcomeDelivered   = "delivered"
	OutcomeFailed      = "failed"
	OutcomeUnknown     = "delivery_unknown"
	OutcomeSuppressed  = "suppressed"
	OutcomeNoRecipient = "no_recipient"
)

const deliverySchema = `
CREATE TABLE IF NOT EXISTS deliveries (
	incident_id INTEGER PRIMARY KEY,
	transport   TEXT    NOT NULL,
	outcome     TEXT    NOT NULL,
	attempts    INTEGER NOT NULL DEFAULT 0,
	last_ms     INTEGER NOT NULL
);
`

// RecordDelivery stores the result of a delivery attempt.
//
// Attempts accumulate, and the state is durable: without it, every open
// incident would be announced again the moment birddog restarted.
func (s *Store) RecordDelivery(incidentID int64, transport, outcome string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(
		`INSERT INTO deliveries (incident_id, transport, outcome, attempts, last_ms)
		 VALUES (?, ?, ?, 1, ?)
		 ON CONFLICT(incident_id) DO UPDATE SET
			transport = excluded.transport,
			outcome   = excluded.outcome,
			attempts  = deliveries.attempts + 1,
			last_ms   = excluded.last_ms`,
		incidentID, transport, outcome, at.UnixMilli(),
	)
	if err != nil {
		return fmt.Errorf("record delivery: %w", err)
	}
	return nil
}

// DeliveryStatus reports what is known about an incident's delivery.
func (s *Store) DeliveryStatus(incidentID int64) (Delivery, bool, error) {
	var d Delivery
	var lastMS int64
	err := s.db.QueryRow(
		`SELECT incident_id, transport, outcome, attempts, last_ms FROM deliveries WHERE incident_id = ?`,
		incidentID,
	).Scan(&d.IncidentID, &d.Transport, &d.Outcome, &d.Attempts, &lastMS)

	if errors.Is(err, sql.ErrNoRows) {
		return Delivery{}, false, nil
	}
	if err != nil {
		return Delivery{}, false, fmt.Errorf("delivery status: %w", err)
	}
	d.LastAt = time.UnixMilli(lastMS).UTC()
	return d, true, nil
}

// ShouldAttemptDelivery decides whether to tell the orchestrator about an
// incident now.
//
// Three answers, and the middle one is the point.
//
// Delivered: no. Re-announcing an unchanged condition is the alert storm the
// incident model exists to prevent.
//
// Uncertain: no. The attempt returned without establishing anything — an
// asynchronous call returning promptly is not an acknowledgement. Resending
// risks a duplicate the destination cannot deduplicate, and trying a different
// transport risks the same alert arriving twice by two routes. Exactly-once
// delivery cannot be promised without the destination's help, so birddog stops
// and leaves the event feed as the recovery path.
//
// Failed: yes, up to the bound. Nothing arrived, so a retry cannot duplicate.
func (s *Store) ShouldAttemptDelivery(incidentID int64, maxAttempts int) (bool, error) {
	d, ok, err := s.DeliveryStatus(incidentID)
	if err != nil {
		return false, err
	}
	if !ok {
		return true, nil
	}

	switch d.Outcome {
	case OutcomeFailed:
		return d.Attempts < maxAttempts, nil
	default:
		// Delivered, uncertain, suppressed or no recipient: nothing to gain
		// and something to lose by trying again. No recipient in particular
		// resolves the same way every time, so a retry only spends the budget
		// and makes a configuration error look like a transport fault.
		return false, nil
	}
}
