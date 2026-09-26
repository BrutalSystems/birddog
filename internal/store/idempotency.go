package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Idempotency keys let a caller retry a mutation it did not hear the answer
// to, without applying it twice. Adding a watch twice, or clearing an override
// twice, is not what anyone asked for.

const idempotencySchema = `
CREATE TABLE IF NOT EXISTS idempotency (
	key     TEXT PRIMARY KEY,
	result  BLOB NOT NULL,
	seen_ms INTEGER NOT NULL
);
`

// PriorResult returns what a key's first use produced, if it has been used.
func (s *Store) PriorResult(key string) ([]byte, bool, error) {
	var result []byte
	err := s.db.QueryRow(`SELECT result FROM idempotency WHERE key = ?`, key).Scan(&result)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("idempotency lookup: %w", err)
	}
	return result, true, nil
}

// RememberResult records what a key produced.
//
// The first result wins: a caller retrying is entitled to the answer their
// original request produced, not a later one's.
func (s *Store) RememberResult(key string, result []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(
		`INSERT INTO idempotency (key, result, seen_ms) VALUES (?, ?, ?)
		 ON CONFLICT(key) DO NOTHING`,
		key, result, time.Now().UnixMilli(),
	)
	if err != nil {
		return fmt.Errorf("remember result: %w", err)
	}
	return nil
}
