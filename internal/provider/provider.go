// Package provider defines the VPS provider abstraction. Every supported
// cloud (DigitalOcean first, Hetzner/Vultr/Linode later) implements
// VPSProvider so the rest of vpncli never learns which one it is talking to.
package provider

import (
	"context"
	"errors"
	"time"
)

// Status is the normalized lifecycle state of an instance. Providers report
// their own vocabulary ("new", "active", "running", "provisioning"); each
// implementation maps into these values so callers see one set.
type Status string

// The normalized lifecycle states. StatusUnknown is the zero value and means
// the provider reported something we do not have a mapping for.
const (
	StatusUnknown      Status = "unknown"
	StatusProvisioning Status = "provisioning"
	StatusActive       Status = "active"
	StatusStopped      Status = "stopped"
	StatusDeleting     Status = "deleting"
	StatusError        Status = "error"
)

// VPSInstance is a server as the provider currently sees it. This is the live
// view; the persisted view lives in internal/state.
type VPSInstance struct {
	// ID is the provider-side identifier (DigitalOcean droplet ID, Hetzner
	// server ID, ...). It is a string because providers disagree on the type.
	ID        string
	Name      string
	Provider  string
	Region    string
	Size      string
	Image     string
	IPv4      string
	Status    Status
	CreatedAt time.Time
}

// CreateOptions is the provider-independent request to stand up one server.
type CreateOptions struct {
	Name   string
	Region string
	Size   string
	Image  string

	// SSHKeyIDs are provider-side identifiers of keys to install for root.
	// Bootstrap (v0.8.0) connects over SSH, so at least one is required.
	SSHKeyIDs []string

	// Tags are applied provider-side where supported, so `vpncli sync` can
	// tell our servers apart from anything else in the account.
	Tags []string
}

// Region is a datacenter location offered by a provider.
type Region struct {
	Slug      string
	Name      string
	Available bool
}

// Size is an instance type. PriceMonthly is in USD.
type Size struct {
	Slug         string
	VCPUs        int
	MemoryMB     int
	DiskGB       int
	PriceMonthly float64
	Available    bool
	// Regions lists region slugs where this size can be created.
	Regions []string
}

// Image is a bootable OS image.
type Image struct {
	Slug         string
	Name         string
	Distribution string
}

// VPSProvider is the full contract. Each provider normalizes its own SDK's
// quirks behind these methods - notably WaitReady, where Hetzner has native
// async waiters while DigitalOcean, Vultr and Linode need manual polling. That
// difference must never leak into this interface.
type VPSProvider interface {
	// Name is the stable slug used in config and state ("digitalocean").
	Name() string

	ListInstances(ctx context.Context) ([]VPSInstance, error)
	GetInstance(ctx context.Context, id string) (VPSInstance, error)
	CreateInstance(ctx context.Context, opts CreateOptions) (VPSInstance, error)
	DeleteInstance(ctx context.Context, id string) error

	// WaitReady blocks until the instance is active with a routable public
	// IPv4 and is accepting TCP connections on port 22, or until ctx is done.
	WaitReady(ctx context.Context, id string) (VPSInstance, error)

	// Catalog lookups, used by the `vpncli init` wizard.
	ListRegions(ctx context.Context) ([]Region, error)
	ListSizes(ctx context.Context) ([]Size, error)
	ListImages(ctx context.Context) ([]Image, error)
}

// ErrNotFound is returned by GetInstance and DeleteInstance when the provider
// has no such instance. `vpncli sync` treats it as "this row is stale".
var ErrNotFound = errors.New("instance not found")
