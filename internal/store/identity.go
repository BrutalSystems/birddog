package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
)

// storeIDKey holds the identity of this store's history.
const storeIDKey = "store_id"

// ErrStoreReplaced reports that a cursor belongs to a history this store did
// not produce.
//
// The sibling of ErrCursorStale, for the case it cannot cover. A stale cursor
// is one whose history expired: the events existed here and were pruned, and
// the retention floor records how far back the record still reaches. This is a
// cursor whose history was *replaced* — a different database stands where the
// old one did, because birddog was reinstalled, its state directory cleared,
// or the instance recreated.
//
// Nothing in a bare sequence number distinguishes the two, and the failure is
// silent rather than loud: a fresh store has no retention floor, so the
// staleness check passes, and its sequences start at 1, so a cursor from the
// old history is above everything and matches no rows. The honest-looking
// answer is an empty page — there is nothing new — which to a consumer is
// indistinguishable from a watched session that has gone quiet.
//
// Manufacturing silence is the one thing birddog must not do, so the identity
// is carried and compared instead of trusting the number alone.
var ErrStoreReplaced = errors.New("cursor belongs to a store that no longer exists")

// ID returns this store's identity, minted when the database was created.
//
// Stable for the life of the database and across every reopen: a restart must
// not look like a replacement.
func (s *Store) ID() string { return s.id }

// loadOrMintID reads the store's identity, creating one if this is a new
// database.
//
// Minted rather than derived. A path, an instance id or a machine name would
// all be reused by a replacement standing in the same place, which is exactly
// the case that has to be distinguishable.
func loadOrMintID(db *sql.DB) (string, error) {
	var id string
	err := db.QueryRow(`SELECT value FROM meta WHERE key = ?`, storeIDKey).Scan(&id)
	switch {
	case err == nil:
		return id, nil
	case !errors.Is(err, sql.ErrNoRows):
		return "", fmt.Errorf("read store identity: %w", err)
	}

	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("mint store identity: %w", err)
	}
	id = hex.EncodeToString(b)

	// DO NOTHING rather than overwrite: if another process minted one between
	// the read and this write, its value is the identity and ours is not.
	if _, err := db.Exec(
		`INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO NOTHING`,
		storeIDKey, id,
	); err != nil {
		return "", fmt.Errorf("record store identity: %w", err)
	}
	if err := db.QueryRow(`SELECT value FROM meta WHERE key = ?`, storeIDKey).Scan(&id); err != nil {
		return "", fmt.Errorf("confirm store identity: %w", err)
	}
	return id, nil
}

// CheckIdentity reports whether a cursor said to come from store id can be
// honoured here.
//
// An empty id asks nothing: a caller that carries no identity gets the
// behaviour it had before there was one. A database that predates identities
// has one minted on first open, so an old cursor presented with no identity is
// still answered rather than refused.
func (s *Store) CheckIdentity(id string) error {
	if id == "" || id == s.id {
		return nil
	}
	return fmt.Errorf("cursor is from store %s, this is store %s: %w", id, s.id, ErrStoreReplaced)
}
