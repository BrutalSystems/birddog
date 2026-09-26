package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Incident is one attention condition, held open for as long as it lasts.
//
// An incident exists so that a condition observed over and over — a worker
// idle for an hour is observed on every poll — is reported once. It is scoped
// to a target *and* run generation: a replacement session going idle is a new
// fact about a new session, not a continuation of the old one's.
type Incident struct {
	ID         int64
	TargetID   string
	Condition  string
	Generation int64

	OpenedAt   time.Time
	LastSeenAt time.Time

	// ResolvedAt is set when fresh evidence ended the condition.
	ResolvedAt *time.Time

	// NotifiedAt records that the orchestrator was told, so an unchanged
	// condition is not announced again on every observation.
	NotifiedAt *time.Time

	// AcknowledgedAt records only that the orchestrator has seen the alert.
	// It resolves nothing and changes nothing about the worker.
	AcknowledgedAt *time.Time
}

const incidentSchema = `
CREATE TABLE IF NOT EXISTS incidents (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	target_id       TEXT    NOT NULL,
	condition       TEXT    NOT NULL,
	generation      INTEGER NOT NULL DEFAULT 0,
	opened_ms       INTEGER NOT NULL,
	last_seen_ms    INTEGER NOT NULL,
	resolved_ms     INTEGER,
	notified_ms     INTEGER,
	acknowledged_ms INTEGER
);
-- At most one open incident per target, condition and generation. The database
-- enforces the deduplication rather than trusting every caller to check first.
CREATE UNIQUE INDEX IF NOT EXISTS incidents_open
	ON incidents (target_id, condition, generation)
	WHERE resolved_ms IS NULL;
`

// OpenOrTouchIncident records an observation of a condition.
//
// It opens an incident the first time, and afterwards only refreshes the
// existing one. The returned flag distinguishes the two, because opening is
// what an orchestrator needs telling about; a repeat is not.
func (s *Store) OpenOrTouchIncident(targetID, condition string, generation int64, at time.Time) (Incident, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return Incident{}, false, fmt.Errorf("incident: %w", err)
	}
	defer tx.Rollback()

	var id int64
	err = tx.QueryRow(
		`SELECT id FROM incidents
		 WHERE target_id = ? AND condition = ? AND generation = ? AND resolved_ms IS NULL`,
		targetID, condition, generation,
	).Scan(&id)

	opened := false
	switch {
	case errors.Is(err, sql.ErrNoRows):
		res, err := tx.Exec(
			`INSERT INTO incidents (target_id, condition, generation, opened_ms, last_seen_ms)
			 VALUES (?, ?, ?, ?, ?)`,
			targetID, condition, generation, at.UnixMilli(), at.UnixMilli(),
		)
		if err != nil {
			return Incident{}, false, fmt.Errorf("open incident: %w", err)
		}
		if id, err = res.LastInsertId(); err != nil {
			return Incident{}, false, fmt.Errorf("open incident: %w", err)
		}
		opened = true

	case err != nil:
		return Incident{}, false, fmt.Errorf("incident: %w", err)

	default:
		// Only freshness moves. Notification and acknowledgement state are
		// deliberately left alone: a repeat observation of an unchanged
		// condition must not cause the orchestrator to be told again.
		if _, err := tx.Exec(
			`UPDATE incidents SET last_seen_ms = ? WHERE id = ?`, at.UnixMilli(), id,
		); err != nil {
			return Incident{}, false, fmt.Errorf("touch incident: %w", err)
		}
	}

	inc, err := readIncident(tx, id)
	if err != nil {
		return Incident{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Incident{}, false, fmt.Errorf("incident: %w", err)
	}
	return inc, opened, nil
}

// ResolveIncident closes an incident. The condition recurring afterwards opens
// a new one, so the orchestrator hears about it again.
func (s *Store) ResolveIncident(id int64, at time.Time) error {
	return s.stamp(id, "resolved_ms", at)
}

// MarkNotified records that the orchestrator was told about this incident.
func (s *Store) MarkNotified(id int64, at time.Time) error {
	return s.stamp(id, "notified_ms", at)
}

// AcknowledgeIncident records that the orchestrator has seen the alert.
//
// It does not resolve the incident, approve anything, or touch the worker in
// any way. A worker waiting on a permission request is still waiting after its
// alert is acknowledged.
func (s *Store) AcknowledgeIncident(id int64, at time.Time) error {
	return s.stamp(id, "acknowledged_ms", at)
}

func (s *Store) stamp(id int64, column string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// column is never caller-supplied: it comes from the methods above.
	res, err := s.db.Exec(`UPDATE incidents SET `+column+` = ? WHERE id = ?`, at.UnixMilli(), id)
	if err != nil {
		return fmt.Errorf("update incident: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update incident: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("incident %d not found", id)
	}
	return nil
}

// OpenIncidents returns a target's unresolved incidents, oldest first.
func (s *Store) OpenIncidents(targetID string) ([]Incident, error) {
	rows, err := s.db.Query(
		`SELECT id, target_id, condition, generation, opened_ms, last_seen_ms,
		        resolved_ms, notified_ms, acknowledged_ms
		 FROM incidents WHERE target_id = ? AND resolved_ms IS NULL ORDER BY opened_ms ASC, id ASC`,
		targetID,
	)
	if err != nil {
		return nil, fmt.Errorf("open incidents: %w", err)
	}
	defer rows.Close()

	var out []Incident
	for rows.Next() {
		inc, err := scanIncident(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, inc)
	}
	return out, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func readIncident(tx *sql.Tx, id int64) (Incident, error) {
	row := tx.QueryRow(
		`SELECT id, target_id, condition, generation, opened_ms, last_seen_ms,
		        resolved_ms, notified_ms, acknowledged_ms
		 FROM incidents WHERE id = ?`, id)
	return scanIncident(row)
}

func scanIncident(sc scanner) (Incident, error) {
	var inc Incident
	var openedMS, lastSeenMS int64
	var resolvedMS, notifiedMS, ackMS sql.NullInt64

	if err := sc.Scan(&inc.ID, &inc.TargetID, &inc.Condition, &inc.Generation,
		&openedMS, &lastSeenMS, &resolvedMS, &notifiedMS, &ackMS); err != nil {
		return Incident{}, fmt.Errorf("read incident: %w", err)
	}

	inc.OpenedAt = time.UnixMilli(openedMS).UTC()
	inc.LastSeenAt = time.UnixMilli(lastSeenMS).UTC()
	inc.ResolvedAt = nullableTime(resolvedMS)
	inc.NotifiedAt = nullableTime(notifiedMS)
	inc.AcknowledgedAt = nullableTime(ackMS)
	return inc, nil
}

func nullableTime(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.UnixMilli(v.Int64).UTC()
	return &t
}
