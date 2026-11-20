package main

import (
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
)

func sqliteDSN(path string, readOnly bool) string {
	query := "_pragma=busy_timeout(5000)&_pragma=temp_store(MEMORY)"
	if readOnly {
		query = "mode=ro&_pragma=query_only(1)&" + query
	} else {
		query = "_txlock=immediate&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&" + query
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	slashed := filepath.ToSlash(path)
	if !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed
	}
	u := url.URL{Scheme: "file", Path: slashed, RawQuery: query}
	return u.String()
}

func openDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", sqliteDSN(path, false))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS users (
			id INTEGER PRIMARY KEY, issuer TEXT NOT NULL, subject TEXT NOT NULL,
			name TEXT NOT NULL, immich_key BLOB, UNIQUE (issuer, subject)
		);
		CREATE TABLE IF NOT EXISTS sessions (
			hash BLOB PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			expires INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS sessions_expiry ON sessions(expires);
		CREATE TABLE IF NOT EXISTS refresh_tokens (
			hash BLOB PRIMARY KEY, family_id BLOB NOT NULL,
			user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			session_hash BLOB, expires INTEGER NOT NULL, revoked INTEGER
		);
		CREATE INDEX IF NOT EXISTS refresh_tokens_expiry ON refresh_tokens(expires);
		CREATE INDEX IF NOT EXISTS refresh_tokens_family ON refresh_tokens(family_id);
		CREATE INDEX IF NOT EXISTS refresh_tokens_user ON refresh_tokens(user_id);
		CREATE TABLE IF NOT EXISTS device_keys (
			id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			name TEXT NOT NULL, hash BLOB NOT NULL UNIQUE, last_used INTEGER, revoked INTEGER
		);
		CREATE INDEX IF NOT EXISTS device_keys_owner ON device_keys(user_id);
		CREATE TABLE IF NOT EXISTS points (
			id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			stream TEXT NOT NULL, recorded_at INTEGER NOT NULL, lat REAL NOT NULL, lon REAL NOT NULL,
			edited_lat REAL, edited_lon REAL, edited_time INTEGER, revision INTEGER NOT NULL DEFAULT 0,
			estimated INTEGER NOT NULL DEFAULT 0, break_before INTEGER NOT NULL DEFAULT 0,
			CHECK (lat BETWEEN -90 AND 90 AND lon BETWEEN -180 AND 180)
		);
		CREATE INDEX IF NOT EXISTS points_range ON points(user_id, recorded_at, id);
		CREATE INDEX IF NOT EXISTS points_stream ON points(user_id, stream, recorded_at, id);
		CREATE UNIQUE INDEX IF NOT EXISTS original_fix ON points(user_id, recorded_at, lat, lon) WHERE estimated = 0;
		CREATE TABLE IF NOT EXISTS upload_history (
			id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			device_id INTEGER REFERENCES device_keys(id) ON DELETE SET NULL,
			recorded_at INTEGER NOT NULL, received_at INTEGER NOT NULL,
			lat REAL NOT NULL, lon REAL NOT NULL, duplicate INTEGER NOT NULL DEFAULT 0
		);
		CREATE INDEX IF NOT EXISTS upload_history_user ON upload_history(user_id, id DESC);
	`)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize SQLite: %w", err)
	}
	if err := ensureColumn(db, "points", "edited_time", "INTEGER"); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate SQLite: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS points_edited_time ON points(user_id, edited_time, id) WHERE edited_time IS NOT NULL`); err != nil {
		db.Close()
		return nil, fmt.Errorf("index edited_time: %w", err)
	}
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err == nil && version < 2 {
		_, _ = db.Exec("PRAGMA user_version = 2")
	}
	return db, nil
}

func ensureColumn(db *sql.DB, table, column, definition string) error {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.Exec("ALTER TABLE " + table + " ADD COLUMN " + column + " " + definition)
	return err
}

func openReadDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", sqliteDSN(path, true))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(max(4, runtime.NumCPU()))
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open read-only SQLite: %w", err)
	}
	return db, nil
}
