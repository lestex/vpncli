// Package config handles vpncli's on-disk layout and the config file the
// `vpncli init` wizard writes.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// AppName is the directory name used under the config and data roots.
const AppName = "vpncli"

// Config is the persisted user configuration. Every field is optional: the
// wizard fills it in over several steps (provider and region in v0.5.0, size
// and image in v0.6.0, REALITY settings in v0.7.0) and writes what it has so
// far, so a partially complete file is normal and must not be an error.
type Config struct {
	// Provider is the provider slug, e.g. "digitalocean".
	Provider string `yaml:"provider,omitempty"`
	// Region, Size and Image are provider-specific slugs.
	Region string `yaml:"region,omitempty"`
	Size   string `yaml:"size,omitempty"`
	Image  string `yaml:"image,omitempty"`

	// SSHKeyPath is the private key used to bootstrap the server.
	SSHKeyPath string `yaml:"ssh_key_path,omitempty"`

	// Reality holds the camouflage settings for the VLESS+REALITY server.
	Reality Reality `yaml:"reality,omitempty"`
}

// Reality is the REALITY camouflage configuration.
//
// Note there is deliberately no domain or CDN setting here. REALITY connects
// direct to the server's IP; fronting it with a CDN breaks the handshake trick,
// and a stable hostname would defeat the point of rotating IPs.
type Reality struct {
	// Dest is the real TLS 1.3 site the server impersonates, host:port.
	// Best picks are themselves CDN-fronted, so our traffic blends into a
	// large legitimate pool.
	Dest string `yaml:"dest,omitempty"`
	// ServerNames is the SNI list clients may present; it must match Dest.
	ServerNames []string `yaml:"server_names,omitempty"`
}

// Path returns the config file location: $XDG_CONFIG_HOME/vpncli/config.yaml,
// falling back to ~/.config/vpncli/config.yaml.
func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.yaml"), nil
}

// Dir returns the config directory, creating nothing.
func Dir() (string, error) {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, AppName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating home directory: %w", err)
	}
	return filepath.Join(home, ".config", AppName), nil
}

// DataDir returns the directory for mutable state (the SQLite database):
// $XDG_DATA_HOME/vpncli, falling back to ~/.local/share/vpncli.
func DataDir() (string, error) {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, AppName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating home directory: %w", err)
	}
	return filepath.Join(home, ".local", "share", AppName), nil
}

// DatabasePath returns the state database location.
func DatabasePath() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "state.db"), nil
}

// Load reads the config file. A missing file yields a zero Config and no
// error, since not having run the wizard yet is a normal state.
func Load() (*Config, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	return LoadFrom(path)
}

// LoadFrom reads a config file from an explicit path.
func LoadFrom(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &Config{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config %s: %w", path, err)
	}
	return &cfg, nil
}

// Save writes the config to the default path, creating the directory. The file
// is written 0600 and the directory 0700: no secrets live here today, but
// v0.8.0 adds REALITY key material and the permissions should already be right.
func (c *Config) Save() error {
	path, err := Path()
	if err != nil {
		return err
	}
	return c.SaveTo(path)
}

// SaveTo writes the config to an explicit path.
func (c *Config) SaveTo(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}

	// Write to a temp file and rename, so an interrupted save cannot leave a
	// truncated config behind.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.yaml")
	if err != nil {
		return fmt.Errorf("creating temp config: %w", err)
	}
	defer os.Remove(tmp.Name())

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("setting config permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("saving config to %s: %w", path, err)
	}
	return nil
}
