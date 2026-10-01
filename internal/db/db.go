// Package db owns the SQLite database: connection, schema migration and
// default seed data. All queries are executed via the sqlc-generated
// package internal/sqlc/sqlite/db.
package db

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"fmt"

	_ "modernc.org/sqlite"

	"htmx-golang-excercise/internal/env"
	"htmx-golang-excercise/internal/passwords"
	sqlcemb "htmx-golang-excercise/internal/sqlc/sqlite"
	sqlite "htmx-golang-excercise/internal/sqlc/sqlite/db"
)

// IDs of the seed rows created by Open. Used by callers that don't yet
// have real multi-user support.
const (
	DefaultUserID        = 1
	DefaultServerGroupID = 1
)

// NewSessionToken returns a fresh random session token (base64url-encoded).
// Session tokens are stored in the user table; the cookie only carries the
// signed token, so deleting/recreating the database invalidates every
// existing session.
func NewSessionToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// Open opens (creating if needed) the SQLite database at path, applies the
// schema, seeds the default user/server group, and returns a handle plus
// sqlc queries bound to it.
func Open(path string) (*sql.DB, *sqlite.Queries, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)", path)
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, nil, err
	}

	// SQLite is single-writer; avoid SQLITE_BUSY under concurrent requests.
	database.SetMaxOpenConns(1)

	if err := database.Ping(); err != nil {
		database.Close()
		return nil, nil, err
	}

	if _, err := database.Exec(sqlcemb.Schema); err != nil {
		database.Close()
		return nil, nil, fmt.Errorf("apply schema: %w", err)
	}

	if err := migrate(database); err != nil {
		database.Close()
		return nil, nil, fmt.Errorf("migrate: %w", err)
	}

	if err := seedDefaults(database); err != nil {
		database.Close()
		return nil, nil, fmt.Errorf("seed defaults: %w", err)
	}

	return database, sqlite.New(database), nil
}

// migrate applies additive changes to tables that already exist. The base
// schema uses CREATE TABLE IF NOT EXISTS, which leaves pre-existing tables
// untouched, so columns added later are applied here idempotently.
func migrate(database *sql.DB) error {
	rows, err := database.Query(`SELECT name FROM pragma_table_info('query_history') WHERE name = 'tab_id'`)
	if err != nil {
		return err
	}
	exists := rows.Next()
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if !exists {
		if _, err := database.Exec(`ALTER TABLE query_history ADD COLUMN tab_id TEXT`); err != nil {
			return err
		}
	}

	rows2, err := database.Query(`SELECT name FROM pragma_table_info('query_history') WHERE name = 'rows_affected'`)
	if err != nil {
		return err
	}
	exists2 := rows2.Next()
	rows2.Close()
	if err := rows2.Err(); err != nil {
		return err
	}
	if !exists2 {
		if _, err := database.Exec(`ALTER TABLE query_history ADD COLUMN rows_affected INTEGER`); err != nil {
			return err
		}
	}

	rows3, err := database.Query(`SELECT name FROM pragma_table_info('user') WHERE name = 'masterpass'`)
	if err != nil {
		return err
	}
	dropMaster := rows3.Next()
	rows3.Close()
	if err := rows3.Err(); err != nil {
		return err
	}
	if dropMaster {
		if _, err := database.Exec(`ALTER TABLE "user" DROP COLUMN masterpass`); err != nil {
			return err
		}
	}

	rows4, err := database.Query(`SELECT name FROM pragma_table_info('user') WHERE name = 'session_token'`)
	if err != nil {
		return err
	}
	hasToken := rows4.Next()
	rows4.Close()
	if err := rows4.Err(); err != nil {
		return err
	}
	if !hasToken {
		if _, err := database.Exec(`ALTER TABLE "user" ADD COLUMN session_token TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}

	// file_path records where a script tab was last saved so a refresh can
	// detect the file being deleted and re-flag the tab as unsaved.
	rows5, err := database.Query(`SELECT name FROM pragma_table_info('workspace_tabs') WHERE name = 'file_path'`)
	if err != nil {
		return err
	}
	hasFilePath := rows5.Next()
	rows5.Close()
	if err := rows5.Err(); err != nil {
		return err
	}
	if !hasFilePath {
		if _, err := database.Exec(`ALTER TABLE workspace_tabs ADD COLUMN file_path TEXT DEFAULT ''`); err != nil {
			return err
		}
	}

	// scratch_text persists each script tab's Scratch Pad across refreshes.
	rows7, err := database.Query(`SELECT name FROM pragma_table_info('workspace_tabs') WHERE name = 'scratch_text'`)
	if err != nil {
		return err
	}
	hasScratch := rows7.Next()
	rows7.Close()
	if err := rows7.Err(); err != nil {
		return err
	}
	if !hasScratch {
		if _, err := database.Exec(`ALTER TABLE workspace_tabs ADD COLUMN scratch_text TEXT DEFAULT ''`); err != nil {
			return err
		}
	}

	// disconnected tracks servers the user deliberately disconnected from
	// so that state survives across application restarts.
	rows6, err := database.Query(`SELECT name FROM pragma_table_info('server') WHERE name = 'disconnected'`)
	if err != nil {
		return err
	}
	hasDisconnected := rows6.Next()
	rows6.Close()
	if err := rows6.Err(); err != nil {
		return err
	}
	if !hasDisconnected {
		if _, err := database.Exec(`ALTER TABLE server ADD COLUMN disconnected BOOLEAN NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}

	return nil
}

// seedDefaults creates (or refreshes) the demo user and its "Servers" group
// used until real user management exists. Credentials come from the .env
// file (PGHTMX_ADMIN_DEFAULT_EMAIL / PGHTMX_ADMIN_DEFAULT_PASSWORD) and the
// password is stored as a PBKDF2 hash, never in plain text.
func seedDefaults(database *sql.DB) error {
	email := env.Get("PGHTMX_ADMIN_DEFAULT_EMAIL", "admin@admin.com")
	password := env.Get("PGHTMX_ADMIN_DEFAULT_PASSWORD", "secret")

	hash, err := passwords.Hash(password)
	if err != nil {
		return fmt.Errorf("hash default password: %w", err)
	}

	// Issued as separate statements: the modernc sqlite driver binds the same
	// argument list to every statement in a multi-statement Exec, so sharing
	// one call here would set servergroup.user_id to the email string.
	if _, err := database.Exec(`
		INSERT INTO "user" (id, email, password, active)
		VALUES (?, ?, ?, 1)
		ON CONFLICT(id) DO UPDATE SET
			email = excluded.email,
			password = excluded.password,
			active = 1;
	`, DefaultUserID, email, hash); err != nil {
		return err
	}

	// A recreated database gets a brand-new session token, which revokes any
	// session cookie issued against the previous database. On an ordinary
	// restart the token is kept so sessions survive.
	token, err := NewSessionToken()
	if err != nil {
		return fmt.Errorf("generate session token: %w", err)
	}
	if _, err := database.Exec(`
		UPDATE "user" SET session_token = ?
		WHERE id = ? AND session_token = '';
	`, token, DefaultUserID); err != nil {
		return err
	}

	_, err = database.Exec(`
		INSERT OR IGNORE INTO servergroup (id, user_id, name)
		VALUES (?, ?, 'Servers');
	`, DefaultServerGroupID, DefaultUserID)
	return err
}
