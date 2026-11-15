// SPDX-License-Identifier: AGPL-3.0-only

// Package config loads and saves /etc/cairn/config.toml. The file is owned by
// root and readable by the cairn group; only the root helper writes it.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"

	"github.com/Kobayashi-classmate/Cairn/internal/i18n"
)

// DefaultPath is the location of the configuration file.
const DefaultPath = "/etc/cairn/config.toml"

// Config is the panel configuration.
type Config struct {
	Server Server `toml:"server"`
	UI     UI     `toml:"ui"`
	Paths  Paths  `toml:"paths"`
	Cloud  Cloud  `toml:"cloud"`
	ACME   ACME   `toml:"acme"`
}

// Cloud configures access to the project's signed public data (official
// messages, release information). Nothing is ever uploaded.
type Cloud struct {
	Enabled bool   `toml:"enabled"`
	BaseURL string `toml:"base_url"`
	Channel string `toml:"channel"` // stable | beta
	// TrustedKeys adds ed25519 public keys (base64) to the built-in release
	// keys, for self-hosted mirrors and tests.
	TrustedKeys []string `toml:"trusted_keys"`
}

// DefaultCloudURL serves the signed feed until the Cairn cloud is online.
const DefaultCloudURL = "https://github.com/Kobayashi-classmate/Cairn/releases/download/feed"

// Server configures the HTTPS listener.
type Server struct {
	// Listen is host:port; the installer picks a random port in 20000-60000.
	Listen string `toml:"listen"`
}

// UI holds presentation defaults.
type UI struct {
	// DefaultLocale applies to users without a personal language, to
	// notifications and to the CLI.
	DefaultLocale string `toml:"default_locale"`
}

// Paths holds filesystem locations.
type Paths struct {
	DataDir      string `toml:"data_dir"`
	HelperSocket string `toml:"helper_socket"`
}

// Default returns the built-in defaults.
func Default() Config {
	return Config{
		Server: Server{Listen: "0.0.0.0:28443"},
		UI:     UI{DefaultLocale: i18n.Default},
		Paths:  Paths{DataDir: "/var/lib/cairn", HelperSocket: "/run/cairn/helper.sock"},
		Cloud:  Cloud{Enabled: true, BaseURL: DefaultCloudURL, Channel: "stable"},
	}
}

// ACME holds optional ACME settings of the root helper.
type ACME struct {
	// Resolvers are the DNS servers ("host:port") that DNS-01 validation
	// checks propagation with, for split-horizon or internal DNS: when set,
	// they alone decide that the record is visible (the zone's own servers
	// are not asked). Empty uses the system's resolvers and the zone's
	// authoritative servers.
	Resolvers []string `toml:"resolvers"`
}

// ValidateCloudURL accepts absolute http(s) URLs without credentials.
func ValidateCloudURL(s string) error {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return fmt.Errorf("config: cloud.base_url %q must be an absolute http(s) URL", s)
	}
	return nil
}

// Load reads path over the defaults. A missing file returns the defaults
// together with an error satisfying errors.Is(err, fs.ErrNotExist).
func Load(path string) (Config, error) {
	c := Default()
	data, err := os.ReadFile(path) //nolint:gosec // G304: path comes from a command-line flag set by root or systemd, not from requests.
	if err != nil {
		return c, err
	}
	if err := toml.Unmarshal(data, &c); err != nil {
		return Default(), fmt.Errorf("config: parse %s: %w", path, err)
	}
	return c, c.Validate()
}

// Validate checks values that would otherwise fail later in confusing ways.
func (c Config) Validate() error {
	if _, _, err := net.SplitHostPort(c.Server.Listen); err != nil {
		return fmt.Errorf("config: server.listen %q: %w", c.Server.Listen, err)
	}
	if !i18n.IsSupported(c.UI.DefaultLocale) {
		return fmt.Errorf("config: ui.default_locale %q is not supported", c.UI.DefaultLocale)
	}
	if !filepath.IsAbs(c.Paths.DataDir) || !filepath.IsAbs(c.Paths.HelperSocket) {
		return errors.New("config: paths must be absolute")
	}
	if c.Cloud.Channel != "stable" && c.Cloud.Channel != "beta" {
		return fmt.Errorf("config: cloud.channel %q must be stable or beta", c.Cloud.Channel)
	}
	return ValidateCloudURL(c.Cloud.BaseURL)
}

const header = "# SPDX-License-Identifier: AGPL-3.0-only\n# Cairn configuration. Managed by Cairn; edit with care.\n\n"

// Save writes c to path atomically (temporary file, fsync, rename) with the
// given mode and ownership. Pass uid or gid -1 to leave it unchanged.
func Save(path string, c Config, mode fs.FileMode, uid, gid int) error {
	if err := c.Validate(); err != nil {
		return err
	}
	body, err := toml.Marshal(c)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.toml")
	if err != nil {
		return err
	}
	// Best effort: after a successful rename there is nothing left to remove.
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := writeAndSync(tmp, append([]byte(header), body...), mode, uid, gid); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("config: write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("config: write %s: %w", path, err)
	}
	return os.Rename(tmp.Name(), path)
}

func writeAndSync(f *os.File, data []byte, mode fs.FileMode, uid, gid int) error {
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Chmod(mode); err != nil {
		return err
	}
	if uid >= 0 || gid >= 0 {
		if err := f.Chown(uid, gid); err != nil {
			return err
		}
	}
	return f.Sync()
}
