// Package store keeps forwarding rules in SQLite.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lsongdev/forward-go/forward"
	_ "modernc.org/sqlite" // registers the "sqlite" driver for Open
)

var (
	// ErrNotFound reports a rule that does not exist.
	ErrNotFound = errors.New("rule not found")
	// ErrConflict reports a duplicate name, or a second rule on the same
	// protocol and listen address.
	ErrConflict = errors.New("conflict")
)

// SQLite stores rules. Its tables are prefixed "forward_", so the database
// can be shared with other modules.
type SQLite struct {
	db    *sql.DB
	owned bool // opened by Open, so Close closes it
}

// Open opens (creating if needed) a database file of its own at path.
func Open(ctx context.Context, path string) (*SQLite, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s, err := New(ctx, db)
	if err != nil {
		db.Close()
		return nil, err
	}
	s.owned = true
	return s, nil
}

// New migrates the forward_ tables in db, which the caller owns and may
// share with other modules.
func New(ctx context.Context, db *sql.DB) (*SQLite, error) {
	s := &SQLite{db: db}
	if err := s.migrate(ctx); err != nil {
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	return s, nil
}

// Close closes the database if Open opened it.
func (s *SQLite) Close() error {
	if s.owned {
		return s.db.Close()
	}
	return nil
}

// migrations are applied in order; forward_schema records how many have
// run. Append new migrations, never edit old ones.
var migrations = []string{`
CREATE TABLE forward_rules (
	id         INTEGER PRIMARY KEY,
	name       TEXT NOT NULL UNIQUE,
	protocol   TEXT NOT NULL CHECK (protocol IN ('tcp', 'udp')),
	listen     TEXT NOT NULL,
	target     TEXT NOT NULL,
	enabled    INTEGER NOT NULL,
	created_at INTEGER NOT NULL, -- unix nanoseconds
	updated_at INTEGER NOT NULL,
	UNIQUE (protocol, listen)
)`}

func (s *SQLite) migrate(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS forward_schema (version INTEGER NOT NULL)`); err != nil {
		return err
	}
	var version int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM forward_schema`).Scan(&version); err != nil {
		return err
	}
	if version > len(migrations) {
		return fmt.Errorf("schema version %d is newer than this program (%d)", version, len(migrations))
	}
	if version == len(migrations) {
		return nil
	}
	for i := version; i < len(migrations); i++ {
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM forward_schema`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO forward_schema (version) VALUES (?)`, len(migrations)); err != nil {
		return err
	}
	return tx.Commit()
}

const columns = `id, name, protocol, listen, target, enabled, created_at, updated_at`

func scan(row interface{ Scan(...any) error }) (forward.Rule, error) {
	var r forward.Rule
	var created, updated int64
	err := row.Scan(&r.ID, &r.Name, &r.Protocol, &r.Listen, &r.Target, &r.Enabled, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	r.CreatedAt, r.UpdatedAt = time.Unix(0, created), time.Unix(0, updated)
	return r, err
}

// List returns every rule by name.
func (s *SQLite) List(ctx context.Context) ([]forward.Rule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM forward_rules ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rules := []forward.Rule{}
	for rows.Next() {
		r, err := scan(rows)
		if err != nil {
			return nil, err
		}
		rules = append(rules, r)
	}
	return rules, rows.Err()
}

// Get returns one rule.
func (s *SQLite) Get(ctx context.Context, id int64) (forward.Rule, error) {
	return scan(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM forward_rules WHERE id = ?`, id))
}

// Create inserts r and sets its ID and timestamps.
func (s *SQLite) Create(ctx context.Context, r *forward.Rule) error {
	now := time.Now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO forward_rules (name, protocol, listen, target, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, r.Name, r.Protocol, r.Listen, r.Target, r.Enabled, now.UnixNano(), now.UnixNano())
	if err != nil {
		return conflict(err)
	}
	r.ID, err = res.LastInsertId()
	r.CreatedAt, r.UpdatedAt = now, now
	return err
}

// Update replaces r's fields, keeping its creation time.
func (s *SQLite) Update(ctx context.Context, r *forward.Rule) error {
	now := time.Now()
	res, err := s.db.ExecContext(ctx, `UPDATE forward_rules SET name = ?, protocol = ?, listen = ?, target = ?, enabled = ?, updated_at = ?
		WHERE id = ?`, r.Name, r.Protocol, r.Listen, r.Target, r.Enabled, now.UnixNano(), r.ID)
	if err != nil {
		return conflict(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	r.UpdatedAt = now
	return nil
}

// Delete removes a rule; removing a missing rule succeeds.
func (s *SQLite) Delete(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM forward_rules WHERE id = ?`, id)
	return err
}

func conflict(err error) error {
	if strings.Contains(err.Error(), "UNIQUE constraint failed") {
		if strings.Contains(err.Error(), "forward_rules.name") {
			return fmt.Errorf("%w: a rule with this name exists", ErrConflict)
		}
		return fmt.Errorf("%w: another rule already listens on this address", ErrConflict)
	}
	return err
}
