package store

import "database/sql"

// openRaw creates a database with a given schema, for testing migrations from
// shapes this build no longer writes.
func openRaw(path, schema string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
