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
	machine                  TEXT NOT NULL DEFAULT '',
	reason_ms                INTEGER NOT NULL DEFAULT 0
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
	} else if e.Reason != "" {
		// An observation that asserts nothing keeps the last known status,
		// which is history from now on. Why it is history is current, and is
		// what reason is for: without this, status went on reporting the
		// reason of the last good observation — usually none — through an
		// outage, and none at all for a target that had never been seen.
		stateApplied, err = applyReason(tx, e)
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

	// The reason is taken from this observation unless a reason-only one
	// newer than it, in the same run, has already said something later.
	// Without that, a delayed success could clear the reason of an outage
	// that began after it.
	if _, err := tx.Exec(
		`INSERT INTO target_state (target_id, session_id, generation, status, observed_ms, source, input_request_visibility, reason, machine, reason_ms)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(target_id) DO UPDATE SET
			session_id  = excluded.session_id,
			status      = excluded.status,
			observed_ms = excluded.observed_ms,
			source      = excluded.source,
			input_request_visibility = excluded.input_request_visibility,
			reason      = CASE WHEN excluded.generation > target_state.generation OR excluded.reason_ms > target_state.reason_ms
			                   THEN excluded.reason ELSE target_state.reason END,
			reason_ms   = CASE WHEN excluded.generation > target_state.generation OR excluded.reason_ms > target_state.reason_ms
			                   THEN excluded.reason_ms ELSE target_state.reason_ms END,
			generation  = excluded.generation,
			machine     = excluded.machine`,
		e.TargetID, e.SessionID, e.Generation, e.Status, e.At.UnixMilli(), e.Source, visibility, e.Reason, e.Machine, e.At.UnixMilli(),
	); err != nil {
		return false, fmt.Errorf("write target state: %w", err)
	}
	return true, nil
}

// applyReason records why a target is not currently observed, from an
// observation that asserts no status, under the same ordering applyState
// uses: a straggler from a replaced run, or one overtaken within the same run
// by anything recorded later, changes nothing.
//
// A target with no state yet gets a row carrying only the reason. No status
// is invented for it: status stays empty, and its observed time zero, so
// status reports the reason and nothing it did not observe — and the first
// real observation is never overtaken by it.
func applyReason(tx *sql.Tx, e Event) (bool, error) {
	at := e.At.UnixMilli()
	var curGen, curObserved, curReason int64
	err := tx.QueryRow(
		`SELECT generation, observed_ms, reason_ms FROM target_state WHERE target_id = ?`, e.TargetID,
	).Scan(&curGen, &curObserved, &curReason)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.Exec(
			`INSERT INTO target_state (target_id, session_id, generation, status, observed_ms, source, input_request_visibility, reason, machine, reason_ms)
			 VALUES (?, '', ?, '', 0, ?, 'unavailable', ?, ?, ?)`,
			e.TargetID, e.Generation, e.Source, e.Reason, e.Machine, at,
		); err != nil {
			return false, fmt.Errorf("record reason: %w", err)
		}
		return true, nil
	case err != nil:
		return false, fmt.Errorf("read target state: %w", err)
	case e.Generation < curGen:
		return false, nil // a run that has since been replaced
	case at <= curReason:
		return false, nil // a reason recorded later is already there
	case e.Generation == curGen && at <= curObserved:
		return false, nil // overtaken by a later observation of this run
	}

	if _, err := tx.Exec(
		`UPDATE target_state SET reason = ?, reason_ms = ? WHERE target_id = ?`,
		e.Reason, at, e.TargetID,
	); err != nil {
		return false, fmt.Errorf("record reason: %w", err)
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
	// Zero is what a row holding only a reason stores: nothing was observed.
	// Read as the zero time, not as the Unix epoch, which every reader would
	// otherwise print as an observation made in 1970.
	if observedMS != 0 {
		r.ObservedAt = time.UnixMilli(observedMS).UTC()
	}
	return r, true, nil
}
