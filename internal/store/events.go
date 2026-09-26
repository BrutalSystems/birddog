// Package store is birddog's durable record of what it observed.
//
// Events are append-only and addressed by a monotonic cursor, so an
// orchestrator can read a snapshot, follow from that exact point, and resume
// after a restart without a gap. The store never interprets an event; it only
// keeps it.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// ErrCursorStale reports that replay from a cursor cannot be honest, because
// events after it have been pruned. Returning later events instead would be
// indistinguishable from a read that simply had nothing in between.
var ErrCursorStale = errors.New("cursor is older than the retained history")

// Event is one observation. It carries what was seen, what saw it, and enough
// identity to survive a session being replaced underneath it.
type Event struct {
	// Seq is the cursor. Assigned on append; never reused.
	Seq int64

	InstanceID string
	TargetID   string
	Type       string

	// Source names what produced the observation, so a conclusion's evidence
	// scope stays attached to it.
	Source string

	// At is the wall-clock time of the observation. Live timers must use
	// monotonic elapsed time instead; this is for the durable record.
	At time.Time

	// SessionID, Generation and ConfigRev let a late event be recognised as
	// belonging to a session or configuration that has since been replaced.
	SessionID  string
	Generation int64
	ConfigRev  int64

	// Status is the runtime state this event asserts, empty when it asserts
	// none. Only an event carrying one can change a target's current state.
	Status string

	// Reason says why this observation is not a current, readable answer.
	// Empty when it is one.
	Reason string

	// Machine is the identity of the machine this observation was made on,
	// empty in local mode. Supplied by the daemon: the store records what it
	// is handed and never discovers it, so a relabelled machine changes what
	// later events say and nothing about what earlier ones already said.
	Machine string

	// InputRequestVisibility says whether a permission wait could be observed
	// for this session at all. Empty means unavailable: birddog never reports
	// an absence of observation as an absence of requests.
	InputRequestVisibility string

	// Evidence is a small payload, not a transcript.
	Evidence json.RawMessage
}

// Store is a birddog instance's event log.
type Store struct {
	db *sql.DB

	// id identifies this store's history, so a cursor from a replaced store
	// is refused rather than answered with an empty page.
	id string

	// mu serialises appends so cursor order matches append order.
	mu sync.Mutex
}

const schema = `
CREATE TABLE IF NOT EXISTS events (
	seq         INTEGER PRIMARY KEY AUTOINCREMENT,
	instance_id TEXT    NOT NULL,
	target_id   TEXT    NOT NULL,
	type        TEXT    NOT NULL,
	source      TEXT    NOT NULL,
	at_ms       INTEGER NOT NULL,
	session_id  TEXT    NOT NULL DEFAULT '',
	generation  INTEGER NOT NULL DEFAULT 0,
	config_rev  INTEGER NOT NULL DEFAULT 0,
	status      TEXT    NOT NULL DEFAULT '',
	reason      TEXT    NOT NULL DEFAULT '',
	machine     TEXT    NOT NULL DEFAULT '',
	evidence    BLOB
);
CREATE INDEX IF NOT EXISTS events_target ON events (target_id, seq);

CREATE TABLE IF NOT EXISTS meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
`

// migrate brings a database created by an earlier build up to date.
//
// CREATE TABLE IF NOT EXISTS does nothing to a table that already exists, so a
// column added later has to be added explicitly. Each step is written to be
// safe to re-run.
func migrate(db *sql.DB) error {
	// Added when the event feed began carrying the observed status.
	steps := []string{
		// Added when the event feed began carrying the observed status.
		`ALTER TABLE events ADD COLUMN status TEXT NOT NULL DEFAULT ''`,
		// Added when the opencode plugin made input-request visibility real
		// for one provider, so it stopped being a constant.
		`ALTER TABLE target_state ADD COLUMN input_request_visibility TEXT NOT NULL DEFAULT 'unavailable'`,
		// Added when an indeterminate answer started saying why it was
		// indeterminate.
		`ALTER TABLE events ADD COLUMN reason TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE target_state ADD COLUMN reason TEXT NOT NULL DEFAULT ''`,
		// Added when a cursor became meaningful across machines, so an event
		// says where it was observed. Existing rows default to empty rather
		// than being back-filled: they were observed before there was an
		// answer, and inventing one would be a claim nobody made.
		`ALTER TABLE events ADD COLUMN machine TEXT NOT NULL DEFAULT ''`,
		// The state half of the same answer. Target state outlives the
		// process that wrote it, so the machine belongs beside it rather
		// than being supplied by whoever reads it later.
		`ALTER TABLE target_state ADD COLUMN machine TEXT NOT NULL DEFAULT ''`,
	}
	for _, step := range steps {
		if _, err := db.Exec(step); err != nil {
			if !strings.Contains(err.Error(), "duplicate column name") {
				return fmt.Errorf("migrate store: %w", err)
			}
		}
	}
	return nil
}

// retentionFloorKey records the highest sequence number that has been pruned.
// A cursor at or above it can still be replayed in full.
const retentionFloorKey = "retention_floor"

// Open opens or creates an instance's store.
//
// The database is kept owner-only: it records what an operator's sessions were
// doing, which is nobody else's business on a shared machine.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create state directory: %w", err)
		}
	}

	// WAL keeps a reader (an orchestrator polling events) from blocking the
	// writer (the monitor recording them).
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	if _, err := db.Exec(schema + stateSchema + incidentSchema + deliverySchema + idempotencySchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialise store: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil && !os.IsNotExist(err) {
		db.Close()
		return nil, fmt.Errorf("restrict store permissions: %w", err)
	}

	id, err := loadOrMintID(db)
	if err != nil {
		db.Close()
		return nil, err
	}

	return &Store{db: db, id: id}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Append records one event and returns its cursor.
//
// It is Record without the state update, for an event that asserts no runtime
// state. There is one write path underneath.
//
// Nothing in the daemon uses it today: observations assert a status and go
// through Record, and a delivery outcome is kept in the deliveries table
// rather than the feed. It stays because the distinction it draws is real and
// the feed is where a non-observation event would belong.
func (s *Store) Append(e Event) (int64, error) {
	e.Status = ""
	seq, _, err := s.Record(e)
	return seq, err
}

// After returns up to limit events following cursor, oldest first.
//
// A cursor that has fallen behind the retained history returns ErrCursorStale
// rather than the events that happen to remain: a consumer must be told it
// lost something, not handed a plausible-looking result.
func (s *Store) After(cursor int64, limit int) ([]Event, error) {
	floor, err := s.retentionFloor()
	if err != nil {
		return nil, err
	}
	if cursor < floor {
		return nil, fmt.Errorf("cursor %d: %w (retained from %d)", cursor, ErrCursorStale, floor)
	}

	rows, err := s.db.Query(
		`SELECT seq, instance_id, target_id, type, source, at_ms, session_id, generation, config_rev, status, reason, machine, evidence
		 FROM events WHERE seq > ? ORDER BY seq ASC LIMIT ?`,
		cursor, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("read events: %w", err)
	}
	defer rows.Close()

	var out []Event
	for rows.Next() {
		var e Event
		var atMS int64
		var evidence []byte
		if err := rows.Scan(&e.Seq, &e.InstanceID, &e.TargetID, &e.Type, &e.Source,
			&atMS, &e.SessionID, &e.Generation, &e.ConfigRev, &e.Status, &e.Reason, &e.Machine, &evidence); err != nil {
			return nil, fmt.Errorf("read events: %w", err)
		}
		e.At = time.UnixMilli(atMS).UTC()
		if len(evidence) > 0 {
			e.Evidence = json.RawMessage(evidence)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// LatestCursor returns the newest cursor: the highest sequence stored, or the
// retention floor when retention has emptied the log.
//
// Paired with a status snapshot, it lets a consumer start following without a
// gap between what the snapshot showed and what the feed reports next. The
// floor is what keeps that true once everything has been pruned. MAX(seq) alone
// is 0 on an empty table, and After refuses any cursor below the floor — so
// `status` would hand out a cursor `events` permanently rejects, and the
// documented recovery of taking a fresh cursor from `status` would loop. An
// instance whose targets have all exited never records again, so nothing would
// ever break the loop. A cursor at the floor replays completely, by
// PruneBefore's own reasoning, which makes it the honest "follow from here".
func (s *Store) LatestCursor() (int64, error) {
	floor, err := s.retentionFloor()
	if err != nil {
		return 0, err
	}
	var seq sql.NullInt64
	if err := s.db.QueryRow(`SELECT MAX(seq) FROM events`).Scan(&seq); err != nil {
		return 0, fmt.Errorf("latest cursor: %w", err)
	}
	if !seq.Valid || seq.Int64 < floor {
		return floor, nil
	}
	return seq.Int64, nil
}

// PruneBefore drops events with a sequence below seq and records how far the
// retained history now reaches, so later replays can detect the gap.
func (s *Store) PruneBefore(seq int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("prune: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM events WHERE seq < ?`, seq); err != nil {
		return fmt.Errorf("prune: %w", err)
	}
	// Everything up to seq-1 is now gone; a cursor at seq-1 still replays
	// completely, because only events after it are being asked for.
	//
	// The floor only ever rises. A lower seq than a previous call's says
	// nothing about what that call already deleted, and writing it would
	// un-stale cursors that must still be refused — PruneBefore(1) in
	// particular would write floor 0, which retentionFloor cannot tell from
	// "never pruned". The invariant is here rather than in the caller so that
	// no caller can lose it.
	if _, err := tx.Exec(
		`INSERT INTO meta (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET
			value = MAX(CAST(meta.value AS INTEGER), CAST(excluded.value AS INTEGER))`,
		retentionFloorKey, fmt.Sprint(seq-1),
	); err != nil {
		return fmt.Errorf("prune: %w", err)
	}
	return tx.Commit()
}

func (s *Store) retentionFloor() (int64, error) {
	var v sql.NullString
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, retentionFloorKey).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("retention floor: %w", err)
	}
	var floor int64
	if _, err := fmt.Sscan(v.String, &floor); err != nil {
		return 0, fmt.Errorf("retention floor: %w", err)
	}
	return floor, nil
}
