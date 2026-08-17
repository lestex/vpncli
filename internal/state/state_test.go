package state

import (
	"context"
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
