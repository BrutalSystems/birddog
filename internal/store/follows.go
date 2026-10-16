package store

import (
	"fmt"
	"time"
)

// A follow records that a target's session changed its id while staying the
// same process — Claude Code mints a new session id on /clear without
// restarting — and that birddog moved the target to the new id.
//
// It is durable because the configuration is not rewritten: a file naming the
// original id stays as it was written, and a restarted instance, or a sync
// from that file, would otherwise go looking for a session id that no longer
// exists. Replaying the recorded follows is what lets either one land on the
// session the target has actually been following.

const followSchema = `
CREATE TABLE IF NOT EXISTS follows (
	target_id       TEXT    NOT NULL,
	from_session_id TEXT    NOT NULL,
	to_session_id   TEXT    NOT NULL,
	pid             INTEGER NOT NULL DEFAULT 0,
	proc_start      TEXT    NOT NULL DEFAULT '',
	at_ms           INTEGER NOT NULL,
	PRIMARY KEY (target_id, from_session_id)
);
`

// Follow is one recorded move of a target from one session id to another.
type Follow struct {
	TargetID      string
	FromSessionID string
	ToSessionID   string
	PID           int
	ProcStart     string
	At            time.Time
}

// RecordFollow appends the event announcing a follow and records the follow
// itself, in one transaction, so the feed and the replayable record cannot
// disagree about whether it happened.
//
// The event carries no status and so changes no target state: the next
// observation, made of the new session, does that.
func (s *Store) RecordFollow(f Follow, e Event) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("record follow: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		`INSERT INTO events (instance_id, target_id, type, source, at_ms, session_id, generation, config_rev, status, reason, machine, evidence)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.InstanceID, e.TargetID, e.Type, e.Source, e.At.UnixMilli(),
		e.SessionID, e.Generation, e.ConfigRev, e.Status, e.Reason, e.Machine, []byte(e.Evidence),
	)
	if err != nil {
		return 0, fmt.Errorf("record follow: %w", err)
	}
	seq, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("record follow: %w", err)
	}

	// Replacing on conflict: a session that went A -> B, was configured back
	// to A, and then moved A -> C has followed to C, and only the latest
	// answer for a given starting id is worth replaying.
	if _, err := tx.Exec(
		`INSERT INTO follows (target_id, from_session_id, to_session_id, pid, proc_start, at_ms)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(target_id, from_session_id) DO UPDATE SET
		   to_session_id = excluded.to_session_id, pid = excluded.pid,
		   proc_start = excluded.proc_start, at_ms = excluded.at_ms`,
		f.TargetID, f.FromSessionID, f.ToSessionID, f.PID, f.ProcStart, f.At.UnixMilli(),
	); err != nil {
		return 0, fmt.Errorf("record follow: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("record follow: %w", err)
	}
	return seq, nil
}

// Follows returns every recorded follow, oldest first.
func (s *Store) Follows() ([]Follow, error) {
	rows, err := s.db.Query(
		`SELECT target_id, from_session_id, to_session_id, pid, proc_start, at_ms
		 FROM follows ORDER BY at_ms, rowid`)
	if err != nil {
		return nil, fmt.Errorf("read follows: %w", err)
	}
	defer rows.Close()

	var out []Follow
	for rows.Next() {
		var f Follow
		var at int64
		if err := rows.Scan(&f.TargetID, &f.FromSessionID, &f.ToSessionID, &f.PID, &f.ProcStart, &at); err != nil {
			return nil, fmt.Errorf("read follows: %w", err)
		}
		f.At = time.UnixMilli(at).UTC()
		out = append(out, f)
	}
	return out, rows.Err()
}
