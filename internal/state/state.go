// Package state persists the servers vpncli manages, in a local SQLite file.
//
// There is no Terraform here on purpose: rotation (destroy + reprovision with
// a fresh IP and fresh REALITY keypair) is the core workflow, and a plan/apply
// model fights that. A single table plus the provider API is enough.
package state

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	// Pure-Go SQLite driver: no cgo, so `go build` still produces one static
	// binary that cross-compiles cleanly. Do not swap this for mattn/go-sqlite3.
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

// ErrNotFound is returned when no row matches.
var ErrNotFound = errors.New("server not found in local state")

// Server is one persisted row.
type Server struct {
	ID         int64
	Provider   string
	ProviderID string
	Name       string
	Region     string
	Size       string
	Image      string
	IPv4       string
	Status     string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Store is a handle on the state database.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the database at path and applies the schema.
// Pass ":memory:" for tests.
func Open(path string) (*Store, error) {
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("creating state directory: %w", err)
		}
	}

	// _pragma args are how modernc.org/sqlite takes PRAGMAs. WAL keeps reads
	// (`vpncli list`) from blocking on a provision in another terminal.
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening state database: %w", err)
	}
	// Every connection to ":memory:" gets its own private database, so the
	// pool has to be pinned to one connection or writes vanish between calls.
	if path == ":memory:" {
		db.SetMaxOpenConns(1)
	}

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("opening state database %s: %w", path, err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("applying schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// Insert adds a server and returns it with ID and timestamps populated.
func (s *Store) Insert(ctx context.Context, srv Server) (Server, error) {
	now := time.Now().UTC()
	if srv.CreatedAt.IsZero() {
		srv.CreatedAt = now
	}
	srv.UpdatedAt = now

	res, err := s.db.ExecContext(ctx, `
		INSERT INTO servers
			(provider, provider_id, name, region, size, image, ipv4, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		srv.Provider, srv.ProviderID, srv.Name, srv.Region, srv.Size,
		srv.Image, srv.IPv4, srv.Status, srv.CreatedAt, srv.UpdatedAt)
	if err != nil {
		return Server{}, fmt.Errorf("inserting server: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Server{}, fmt.Errorf("reading inserted id: %w", err)
	}
	srv.ID = id
	return srv, nil
}

// List returns every server, newest first.
func (s *Store) List(ctx context.Context) ([]Server, error) {
	rows, err := s.db.QueryContext(ctx, selectColumns+` FROM servers ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("listing servers: %w", err)
	}
	defer rows.Close()

	var out []Server
	for rows.Next() {
		srv, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, srv)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing servers: %w", err)
	}
	return out, nil
}

// Get returns one server by local id.
func (s *Store) Get(ctx context.Context, id int64) (Server, error) {
	row := s.db.QueryRowContext(ctx, selectColumns+` FROM servers WHERE id = ?`, id)
	srv, err := scanServer(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Server{}, fmt.Errorf("server %d: %w", id, ErrNotFound)
	}
	return srv, err
}

// GetByProviderID returns one server by its provider-side identity, which is
// what `vpncli sync` matches on.
func (s *Store) GetByProviderID(ctx context.Context, providerName, providerID string) (Server, error) {
	row := s.db.QueryRowContext(ctx,
		selectColumns+` FROM servers WHERE provider = ? AND provider_id = ?`,
		providerName, providerID)
	srv, err := scanServer(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Server{}, fmt.Errorf("%s/%s: %w", providerName, providerID, ErrNotFound)
	}
	return srv, err
}

// Update writes the mutable fields (address and lifecycle) of an existing row.
func (s *Store) Update(ctx context.Context, srv Server) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE servers
		   SET ipv4 = ?, status = ?, updated_at = ?
		 WHERE id = ?`,
		srv.IPv4, srv.Status, time.Now().UTC(), srv.ID)
	if err != nil {
		return fmt.Errorf("updating server %d: %w", srv.ID, err)
	}
	return checkAffected(res, srv.ID)
}

// Delete removes a server row by local id.
func (s *Store) Delete(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM servers WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("deleting server %d: %w", id, err)
	}
	return checkAffected(res, id)
}

const selectColumns = `SELECT id, provider, provider_id, name, region, size, image, ipv4, status, created_at, updated_at`

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanServer(sc scanner) (Server, error) {
	var srv Server
	err := sc.Scan(&srv.ID, &srv.Provider, &srv.ProviderID, &srv.Name, &srv.Region,
		&srv.Size, &srv.Image, &srv.IPv4, &srv.Status, &srv.CreatedAt, &srv.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Server{}, err
		}
		return Server{}, fmt.Errorf("scanning server row: %w", err)
	}
	return srv, nil
}

func checkAffected(res sql.Result, id int64) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking affected rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("server %d: %w", id, ErrNotFound)
	}
	return nil
}
