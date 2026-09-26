package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// TargetStateRecord is what birddog currently believes about one target, and
// the identity of the observation that established it.
//
// Generation and ObservedAt are not decoration: they are what lets a delayed
// observation be recognised as describing a session that has since been
// replaced.
type TargetStateRecord struct {
	TargetID   string
	SessionID  string
	Generation int64
	Status     string
	Reason     string
	ObservedAt time.Time
	Source     string

	// InputRequestVisibility is observed, not_observed or unavailable. It
	// never says a worker is unblocked.
	InputRequestVisibility string

	// Machine is the machine this state was observed on, empty in local mode.
	// Stored rather than taken from whoever is reading, because this record
	// outlives the process that wrote it: a daemon resumed under a different
	// identity must not relabel a session it never observed.
	Machine string
}

const stateSchema = `
CREATE TABLE IF NOT EXISTS target_state (
	target_id   TEXT PRIMARY KEY,
	session_id  TEXT    NOT NULL DEFAULT '',
	generation  INTEGER NOT NULL DEFAULT 0,
	status      TEXT    NOT NULL,
	observed_ms INTEGER NOT NULL,
	source      TEXT    NOT NULL DEFAULT '',
	input_request_visibility TEXT NOT NULL DEFAULT 'unavailable',
	reason                   TEXT NOT NULL DEFAULT '',
	machine                  TEXT NOT NULL DEFAULT ''
);
`

// Record appends an event and, when it asserts a status, applies it as the
// target's current state.
//
// Both happen in one transaction, so the log and the derived state cannot
// disagree about what was accepted.
//
// The returned flag says whether the state was updated. False is an ordinary
// outcome, not an error: the event may assert no status, or may belong to a
// session generation that has since been replaced. Either way the event is
// still recorded — refusing to let it change state is not a reason to lose the
// fact that it arrived.
func (s *Store) Record(e Event) (seq int64, stateApplied bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return 0, false, fmt.Errorf("record: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		`INSERT INTO events (instance_id, target_id, type, source, at_ms, session_id, generation, config_rev, status, reason, machine, evidence)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.InstanceID, e.TargetID, e.Type, e.Source, e.At.UnixMilli(),
		e.SessionID, e.Generation, e.ConfigRev, e.Status, e.Reason, e.Machine, []byte(e.Evidence),
	)
	if err != nil {
		return 0, false, fmt.Errorf("record: %w", err)
	}
	seq, err = res.LastInsertId()
	if err != nil {
		return 0, false, fmt.Errorf("record: %w", err)
	}

	if e.Status != "" {
		stateApplied, err = applyState(tx, e)
		if err != nil {
			return 0, false, err
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, false, fmt.Errorf("record: %w", err)
	}
	return seq, stateApplied, nil
}

// applyState writes the observation as current state unless something newer is
// already there.
//
// An observation supersedes the stored one when it comes from a later session
// generation, or from the same generation at a later time. Anything else is a
// straggler: it describes a moment that has already been overtaken, or a
// session that no longer exists.
func applyState(tx *sql.Tx, e Event) (bool, error) {
	var curGen int64
	var curObserved int64
	err := tx.QueryRow(
		`SELECT generation, observed_ms FROM target_state WHERE target_id = ?`, e.TargetID,
	).Scan(&curGen, &curObserved)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		// Nothing known about this target yet.
	case err != nil:
		return false, fmt.Errorf("read target state: %w", err)
	case e.Generation < curGen:
		return false, nil // a session that has since been replaced
	case e.Generation == curGen && e.At.UnixMilli() <= curObserved:
		return false, nil // overtaken within the same session
	}

	visibility := e.InputRequestVisibility
	if visibility == "" {
		// Never an empty claim: not knowing is itself the report.
		visibility = "unavailable"
	}

	if _, err := tx.Exec(
		`INSERT INTO target_state (target_id, session_id, generation, status, observed_ms, source, input_request_visibility, reason, machine)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(target_id) DO UPDATE SET
			session_id  = excluded.session_id,
			generation  = excluded.generation,
			status      = excluded.status,
			observed_ms = excluded.observed_ms,
			source      = excluded.source,
			input_request_visibility = excluded.input_request_visibility,
			reason      = excluded.reason,
			machine     = excluded.machine`,
		e.TargetID, e.SessionID, e.Generation, e.Status, e.At.UnixMilli(), e.Source, visibility, e.Reason, e.Machine,
	); err != nil {
		return false, fmt.Errorf("write target state: %w", err)
	}
	return true, nil
}

// TargetState returns what birddog currently believes about a target.
//
// The second return is false when nothing has been observed. It is never a
// guess: an unobserved target has no state, rather than a default one.
func (s *Store) TargetState(targetID string) (TargetStateRecord, bool, error) {
	var r TargetStateRecord
	var observedMS int64
	err := s.db.QueryRow(
		`SELECT target_id, session_id, generation, status, observed_ms, source, input_request_visibility, reason, machine
		 FROM target_state WHERE target_id = ?`, targetID,
	).Scan(&r.TargetID, &r.SessionID, &r.Generation, &r.Status, &observedMS, &r.Source, &r.InputRequestVisibility, &r.Reason, &r.Machine)

	if errors.Is(err, sql.ErrNoRows) {
		return TargetStateRecord{}, false, nil
	}
	if err != nil {
		return TargetStateRecord{}, false, fmt.Errorf("target state: %w", err)
	}
	r.ObservedAt = time.UnixMilli(observedMS).UTC()
	return r, true, nil
}
