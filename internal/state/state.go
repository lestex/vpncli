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
	"io/fs"
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

	// Credentials is what the bootstrap generated. Zero until it has run.
	Credentials Credentials
	// SSHHostKey is the key this server presented the first time we connected,
	// in authorized_keys form. Empty until then.
	SSHHostKey string
	// BootstrappedAt is when the server was configured. Zero means it is not.
	BootstrappedAt time.Time
}

// Bootstrapped reports whether this server has been configured.
func (s Server) Bootstrapped() bool { return !s.BootstrappedAt.IsZero() }

// Credentials is everything a client needs to reach one server. It is
// generated during the bootstrap and stored nowhere else: the provider does
// not know it, and it is not in config.yaml, which describes the next server
// rather than any particular one.
type Credentials struct {
	// UUID identifies the single VLESS client this server accepts.
	UUID string
	// PrivateKey stays on the server; PublicKey is what a client presents.
	// Both are base64url, the form Xray reads and writes.
	PrivateKey string
	PublicKey  string
	// ShortID is the pre-shared hex string a client sends with the handshake.
	ShortID string
	// Dest and ServerName are the camouflage this server was configured with,
	// which a client has to match exactly.
	Dest       string
	ServerName string
}

// Complete reports whether these credentials can build a client config.
func (c Credentials) Complete() bool {
	return c.UUID != "" && c.PublicKey != "" && c.ShortID != "" && c.ServerName != ""
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
	// (`vpncli server list`) from blocking on a provision in another terminal.
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
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	if err := restrict(path); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// restrict makes the database readable only by its owner.
//
// It holds every server's REALITY private key, so it is as sensitive as an SSH
// key and gets the same permissions. It is done on every open rather than only
// at creation: a database made by an earlier version, or copied off a backup,
// arrives with whatever mode it had.
func restrict(path string) error {
	if path == ":memory:" {
		return nil
	}

	// WAL means the data lives in three files, and the other two are recreated
	// by SQLite as it pleases. A file that is not there yet is not a problem;
	// it will be created by a connection whose umask this process controls.
	for _, name := range []string{path, path + "-wal", path + "-shm"} {
		err := os.Chmod(name, 0o600)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("securing %s: %w", name, err)
		}
	}
	return nil
}

// addedColumns are the columns that arrived after the first release. The
// schema file only creates a table that is missing, so a database made by an
// older version keeps its original shape and has to be widened here.
var addedColumns = []struct{ name, definition string }{
	{"uuid", "TEXT NOT NULL DEFAULT ''"},
	{"reality_private_key", "TEXT NOT NULL DEFAULT ''"},
	{"reality_public_key", "TEXT NOT NULL DEFAULT ''"},
	{"reality_short_id", "TEXT NOT NULL DEFAULT ''"},
	{"reality_dest", "TEXT NOT NULL DEFAULT ''"},
	{"reality_server_name", "TEXT NOT NULL DEFAULT ''"},
	{"ssh_host_key", "TEXT NOT NULL DEFAULT ''"},
	{"bootstrapped_at", "TIMESTAMP"},
}

// migrate adds whatever the servers table is missing. Every column added here
// has a default, so an existing row stays valid without being rewritten.
func migrate(db *sql.DB) error {
	rows, err := db.Query(`SELECT name FROM pragma_table_info('servers')`)
	if err != nil {
		return fmt.Errorf("reading the servers table: %w", err)
	}
	defer rows.Close()

	have := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return fmt.Errorf("reading the servers table: %w", err)
		}
		have[name] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("reading the servers table: %w", err)
	}

	for _, column := range addedColumns {
		if have[column.name] {
			continue
		}
		if _, err := db.Exec(`ALTER TABLE servers ADD COLUMN ` + column.name + ` ` + column.definition); err != nil {
			return fmt.Errorf("adding column %s: %w", column.name, err)
		}
	}
	return nil
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

// SaveHostKey records the SSH host key a server presented, in authorized_keys
// form. It is written on the first connection and checked on every later one,
// so a server that starts answering with a different key is a question rather
// than a shrug.
func (s *Store) SaveHostKey(ctx context.Context, id int64, hostKey string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE servers
		   SET ssh_host_key = ?, updated_at = ?
		 WHERE id = ?`,
		hostKey, time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("recording the host key of server %d: %w", id, err)
	}
	return checkAffected(res, id)
}

// SaveBootstrap records what a finished bootstrap produced, and marks the
// server configured.
//
// It is written once, at the end. A bootstrap that failed halfway leaves the
// row unmarked and its credentials empty, which is exactly right: re-running
// it generates fresh material and replaces whatever reached the server, so
// there is never a half-written set of keys to reconcile.
func (s *Store) SaveBootstrap(ctx context.Context, id int64, c Credentials) error {
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `
		UPDATE servers
		   SET uuid = ?, reality_private_key = ?, reality_public_key = ?,
		       reality_short_id = ?, reality_dest = ?, reality_server_name = ?,
		       bootstrapped_at = ?, updated_at = ?
		 WHERE id = ?`,
		c.UUID, c.PrivateKey, c.PublicKey, c.ShortID, c.Dest, c.ServerName, now, now, id)
	if err != nil {
		return fmt.Errorf("recording the bootstrap of server %d: %w", id, err)
	}
	return checkAffected(res, id)
}

// Delete removes a server row by local id.
func (s *Store) Delete(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM servers WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("deleting server %d: %w", id, err)
	}
	return checkAffected(res, id)
}

const selectColumns = `SELECT id, provider, provider_id, name, region, size, image, ipv4, status, created_at, updated_at,
       uuid, reality_private_key, reality_public_key, reality_short_id, reality_dest, reality_server_name,
       ssh_host_key, bootstrapped_at`

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanServer(sc scanner) (Server, error) {
	var srv Server
	// A server that has never been bootstrapped has no timestamp, so this one
	// column is nullable where the rest carry defaults.
	var bootstrappedAt sql.NullTime

	err := sc.Scan(&srv.ID, &srv.Provider, &srv.ProviderID, &srv.Name, &srv.Region,
		&srv.Size, &srv.Image, &srv.IPv4, &srv.Status, &srv.CreatedAt, &srv.UpdatedAt,
		&srv.Credentials.UUID, &srv.Credentials.PrivateKey, &srv.Credentials.PublicKey,
		&srv.Credentials.ShortID, &srv.Credentials.Dest, &srv.Credentials.ServerName,
		&srv.SSHHostKey, &bootstrappedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Server{}, err
		}
		return Server{}, fmt.Errorf("scanning server row: %w", err)
	}
	srv.BootstrappedAt = bootstrappedAt.Time
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
