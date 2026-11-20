package main

import (
	"database/sql"
	"fmt"
)

func openDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`
		PRAGMA busy_timeout = 5000;
		PRAGMA foreign_keys = ON;
		PRAGMA journal_mode = WAL;
		CREATE TABLE IF NOT EXISTS users (
			id INTEGER PRIMARY KEY, issuer TEXT NOT NULL, subject TEXT NOT NULL,
			name TEXT NOT NULL, immich_key BLOB, UNIQUE (issuer, subject)
		);
		CREATE TABLE IF NOT EXISTS sessions (
			hash BLOB PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			expires INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS sessions_expiry ON sessions(expires);
		CREATE TABLE IF NOT EXISTS device_keys (
			id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			name TEXT NOT NULL, hash BLOB NOT NULL UNIQUE, last_used INTEGER, revoked INTEGER
		);
		CREATE INDEX IF NOT EXISTS device_keys_owner ON device_keys(user_id);
		CREATE TABLE IF NOT EXISTS points (
			id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			stream TEXT NOT NULL, recorded_at INTEGER NOT NULL, lat REAL NOT NULL, lon REAL NOT NULL,
			edited_lat REAL, edited_lon REAL, revision INTEGER NOT NULL DEFAULT 0,
			estimated INTEGER NOT NULL DEFAULT 0, break_before INTEGER NOT NULL DEFAULT 0,
			CHECK (lat BETWEEN -90 AND 90 AND lon BETWEEN -180 AND 180)
		);
		CREATE INDEX IF NOT EXISTS points_range ON points(user_id, recorded_at, id);
		CREATE INDEX IF NOT EXISTS points_stream ON points(user_id, stream, recorded_at, id);
		CREATE UNIQUE INDEX IF NOT EXISTS original_fix ON points(user_id, recorded_at, lat, lon) WHERE estimated = 0;
	`)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize SQLite: %w", err)
	}
	return db, nil
}
