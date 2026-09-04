package state

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func sampleServer() Server {
	return Server{
		Provider:   "digitalocean",
		ProviderID: "123456",
		Name:       "vpncli-fra1-a1b2",
		Region:     "fra1",
		Size:       "s-1vcpu-1gb",
		Image:      "ubuntu-24-04-x64",
		IPv4:       "203.0.113.10",
		Status:     "active",
	}
}

func TestOpenCreatesDatabaseFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	// Opening the same file again must be a no-op, not a schema conflict.
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopening existing database: %v", err)
	}
	s2.Close()
}

func TestInsertAndGet(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	in := sampleServer()
	got, err := s.Insert(ctx, in)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if got.ID == 0 {
		t.Error("Insert did not populate ID")
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Error("Insert did not populate timestamps")
	}

	fetched, err := s.Get(ctx, got.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if fetched.ProviderID != in.ProviderID || fetched.Region != in.Region || fetched.IPv4 != in.IPv4 {
		t.Errorf("round trip mismatch: got %+v, want %+v", fetched, in)
	}
	if !fetched.CreatedAt.Equal(got.CreatedAt) {
		t.Errorf("CreatedAt not preserved: got %v, want %v", fetched.CreatedAt, got.CreatedAt)
	}
}

func TestGetByProviderID(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	inserted, err := s.Insert(ctx, sampleServer())
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	got, err := s.GetByProviderID(ctx, "digitalocean", "123456")
	if err != nil {
		t.Fatalf("GetByProviderID: %v", err)
	}
	if got.ID != inserted.ID {
		t.Errorf("got id %d, want %d", got.ID, inserted.ID)
	}

	// A matching provider id under a different provider must not collide.
	if _, err := s.GetByProviderID(ctx, "hetzner", "123456"); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-provider lookup: got %v, want ErrNotFound", err)
	}
}

func TestDuplicateProviderIDRejected(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if _, err := s.Insert(ctx, sampleServer()); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if _, err := s.Insert(ctx, sampleServer()); err == nil {
		t.Error("inserting a duplicate (provider, provider_id) should fail")
	}
}

func TestList(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if got, err := s.List(ctx); err != nil || len(got) != 0 {
		t.Fatalf("empty List: got %v, %v; want empty, nil", got, err)
	}

	base := time.Now().UTC().Add(-time.Hour)
	for i, providerID := range []string{"1", "2", "3"} {
		srv := sampleServer()
		srv.ProviderID = providerID
		srv.CreatedAt = base.Add(time.Duration(i) * time.Minute)
		if _, err := s.Insert(ctx, srv); err != nil {
			t.Fatalf("Insert %s: %v", providerID, err)
		}
	}

	got, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d servers, want 3", len(got))
	}
	// Newest first.
	if got[0].ProviderID != "3" || got[2].ProviderID != "1" {
		t.Errorf("wrong order: got %s, %s, %s; want 3, 2, 1",
			got[0].ProviderID, got[1].ProviderID, got[2].ProviderID)
	}
}

func TestUpdate(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	srv := sampleServer()
	srv.IPv4 = ""
	srv.Status = "provisioning"
	inserted, err := s.Insert(ctx, srv)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	inserted.IPv4 = "198.51.100.7"
	inserted.Status = "active"
	if err := s.Update(ctx, inserted); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := s.Get(ctx, inserted.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.IPv4 != "198.51.100.7" || got.Status != "active" {
		t.Errorf("update not applied: got ipv4=%q status=%q", got.IPv4, got.Status)
	}
	if !got.UpdatedAt.After(got.CreatedAt) && !got.UpdatedAt.Equal(got.CreatedAt) {
		t.Errorf("UpdatedAt %v is before CreatedAt %v", got.UpdatedAt, got.CreatedAt)
	}
}

func TestDelete(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	inserted, err := s.Insert(ctx, sampleServer())
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := s.Delete(ctx, inserted.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(ctx, inserted.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("after delete, Get returned %v; want ErrNotFound", err)
	}
}

func TestMissingRowsReportNotFound(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	tests := []struct {
		name string
		call func() error
	}{
		{"Get", func() error { _, err := s.Get(ctx, 999); return err }},
		{"Update", func() error { return s.Update(ctx, Server{ID: 999}) }},
		{"Delete", func() error { return s.Delete(ctx, 999) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(); !errors.Is(err, ErrNotFound) {
				t.Errorf("got %v, want ErrNotFound", err)
			}
		})
	}
}

func sampleCredentials() Credentials {
	return Credentials{
		UUID:       "6f4a1e2c-1f1a-4c7b-9a3d-5d2f8b0c1e77",
		PrivateKey: "cJfBGaGmB6cQpRnLxTt6qkZKmsxk4nB1FvJ8mQnZ3F4",
		PublicKey:  "5H1lQeVXqQ0m8VbXZ1qkqzZ0k3sPq7dFf2C6bJmWnQ8",
		ShortID:    "1a2b3c4d",
		Dest:       "www.apple.com:443",
		ServerName: "www.apple.com",
	}
}

// The bootstrap is the only place these come from, and losing them means a
// server nobody can connect to but which still bills.
func TestSaveBootstrap(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	seeded, err := s.Insert(ctx, sampleServer())
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if seeded.Bootstrapped() {
		t.Error("a freshly created server reports itself bootstrapped")
	}

	want := sampleCredentials()
	if err := s.SaveBootstrap(ctx, seeded.ID, want); err != nil {
		t.Fatalf("SaveBootstrap: %v", err)
	}

	got, err := s.Get(ctx, seeded.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Credentials != want {
		t.Errorf("credentials = %+v, want %+v", got.Credentials, want)
	}
	if !got.Bootstrapped() {
		t.Error("a bootstrapped server does not say so")
	}
	if !got.Credentials.Complete() {
		t.Errorf("%+v is not complete enough to build a client config", got.Credentials)
	}
}

func TestSaveHostKey(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	seeded, err := s.Insert(ctx, sampleServer())
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	const hostKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleExampleExampleExampleExampleEx"
	if err := s.SaveHostKey(ctx, seeded.ID, hostKey); err != nil {
		t.Fatalf("SaveHostKey: %v", err)
	}

	got, err := s.Get(ctx, seeded.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.SSHHostKey != hostKey {
		t.Errorf("host key = %q, want %q", got.SSHHostKey, hostKey)
	}
}

func TestSaveOnMissingRows(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.SaveBootstrap(ctx, 42, sampleCredentials()); !errors.Is(err, ErrNotFound) {
		t.Errorf("SaveBootstrap on a missing row = %v, want ErrNotFound", err)
	}
	if err := s.SaveHostKey(ctx, 42, "ssh-ed25519 AAAA"); !errors.Is(err, ErrNotFound) {
		t.Errorf("SaveHostKey on a missing row = %v, want ErrNotFound", err)
	}
}

// A database written by an older version has none of the bootstrap columns.
// Opening it has to widen the table rather than fail, or an upgrade would
// orphan whatever servers were already running.
func TestOpenMigratesAnOlderDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")

	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("opening a bare database: %v", err)
	}
	_, err = old.Exec(`
		CREATE TABLE servers (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			provider    TEXT NOT NULL,
			provider_id TEXT NOT NULL,
			name        TEXT NOT NULL,
			region      TEXT NOT NULL,
			size        TEXT NOT NULL,
			image       TEXT NOT NULL,
			ipv4        TEXT NOT NULL DEFAULT '',
			status      TEXT NOT NULL DEFAULT 'unknown',
			created_at  TIMESTAMP NOT NULL,
			updated_at  TIMESTAMP NOT NULL,
			UNIQUE (provider, provider_id)
		);
		INSERT INTO servers (provider, provider_id, name, region, size, image, ipv4, status, created_at, updated_at)
		VALUES ('digitalocean', '1001', 'vpncli-fra1-a1b2', 'fra1', 's-1vcpu-1gb', 'ubuntu-24-04-x64', '203.0.113.10', 'active', datetime('now'), datetime('now'));`)
	if err != nil {
		t.Fatalf("seeding an old database: %v", err)
	}
	if err := old.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("opening an old database: %v", err)
	}
	defer s.Close()

	servers, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(servers) != 1 || servers[0].Name != "vpncli-fra1-a1b2" {
		t.Fatalf("got %+v, want the row that was already there", servers)
	}
	if servers[0].Bootstrapped() {
		t.Error("a row from before the bootstrap existed claims to be bootstrapped")
	}

	// And the widened table has to be writable, not just readable.
	if err := s.SaveBootstrap(ctx, servers[0].ID, sampleCredentials()); err != nil {
		t.Fatalf("SaveBootstrap after migrating: %v", err)
	}
}
