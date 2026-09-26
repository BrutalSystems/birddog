package store

import (
	"database/sql"
	"fmt"
	"time"
)

// statusExited and conditionExit mirror policy.StatusExited and
// config.ConditionExit.
//
// The store imports neither package — the dependency runs the other way — and
// delivery.go already mirrors notify.Outcome values the same way. A change to
// either value has to be made here too, which is the cost of the layering.
//
// Unlike the other mirrors in the tree, drift here is unrecoverable rather than
// cosmetic: the held query below would match nothing, the hold would silently
// stop existing, and retention would drop the uncollected terminal outcome it
// exists to protect. TestTheMirroredExitConstantsHaveNotDrifted in
// retention_test.go compares these against the originals, which a test file may
// import without giving the store a production dependency.
const (
	statusExited  = "exited"
	conditionExit = "exit"
)

// RetainFrom returns the lowest sequence number that must be kept, ready to
// hand to PruneBefore.
//
// cutoff is the age boundary: events before it are candidates for dropping.
// hold is how much longer an uncollected terminal outcome survives past that
// boundary, so an exit is still held while its time is after cutoff-hold.
//
// That subtraction is done on millisecond timestamps and assumes hold is sane.
// config.maxRetentionSeconds is what makes it so: the millisecond subtraction
// itself cannot overflow int64 for any Duration, bounded or not. What the
// ceiling actually guards is one step earlier, in config, where a
// seconds-to-Duration multiplication can wrap for a large enough seconds
// value — and a hold that has wrapped negative would prune the outcome it was
// meant to hold. The bound lives at that input rather than here because a
// caller that built a Retention by hand is not the case worth defending
// against — a config file is.
//
// The result is a single contiguous floor, which is what makes ErrCursorStale
// answerable: a history with holes in it could not tell a consumer what it
// lost. A held terminal outcome therefore retains everything after it as well,
// including unrelated events, until its hold elapses.
func (s *Store) RetainFrom(cutoff time.Time, hold time.Duration) (int64, error) {
	cutoffMS := cutoff.UnixMilli()

	// Read first, before either query below. If a new event lands between
	// this read and the fallback branch returning, its seq is greater than
	// latest and it survives the prune; reading LatestCursor after the two
	// queries below would instead let PruneBefore(latest+1) delete an event
	// recorded in the gap, which — if it is an uncollected exit — is exactly
	// the unrecoverable loss this whole design exists to prevent.
	latest, err := s.LatestCursor()
	if err != nil {
		return 0, err
	}

	// Everything at or after the cutoff is inside the window. An event exactly
	// at the boundary is retained: the boundary belongs to the history a
	// consumer was promised, not to the part being dropped.
	var byAge sql.NullInt64
	if err := s.db.QueryRow(
		`SELECT MIN(seq) FROM events WHERE at_ms >= ?`, cutoffMS,
	).Scan(&byAge); err != nil {
		return 0, fmt.Errorf("retain from (age): %w", err)
	}

	// A terminal outcome nobody has acknowledged outlives the window by hold.
	//
	// The join is LEFT because a missing incident is not consent: a policy that
	// does not alert on exit has nothing to acknowledge, so its terminal
	// outcome is uncollected and held. Where several incidents match one run,
	// any unacknowledged one holds the event, which errs toward keeping it.
	var held sql.NullInt64
	if err := s.db.QueryRow(
		`SELECT MIN(e.seq) FROM events e
		 LEFT JOIN incidents i
		        ON i.target_id  = e.target_id
		       AND i.generation = e.generation
		       AND i.condition  = ?
		  WHERE e.status = ?
		    AND e.at_ms  <  ?
		    AND e.at_ms  >  ?
		    AND i.acknowledged_ms IS NULL`,
		conditionExit, statusExited, cutoffMS, cutoffMS-hold.Milliseconds(),
	).Scan(&held); err != nil {
		return 0, fmt.Errorf("retain from (held): %w", err)
	}

	switch {
	case held.Valid && byAge.Valid:
		return min(held.Int64, byAge.Int64), nil
	case held.Valid:
		return held.Int64, nil
	case byAge.Valid:
		return byAge.Int64, nil
	}

	// Nothing is inside the window and nothing is held, so the whole history is
	// droppable. One past the newest sequence read at the top of this call is
	// the floor that says so, and on an empty store that is 1, which prunes
	// nothing.
	return latest + 1, nil
}
