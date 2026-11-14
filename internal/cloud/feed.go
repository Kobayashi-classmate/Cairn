// SPDX-License-Identifier: AGPL-3.0-only

package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/semver"
)

// Feed file names at the base URL.
const (
	ReleasesFile      = "releases.json"
	AnnouncementsFile = "announcements.json"
	maxFeedSize       = 2 << 20
)

// ErrRollback means the server returned data older than data already seen.
var ErrRollback = errors.New("cloud: feed is older than the last one seen")

// Package is one installable file of a release.
type Package struct {
	Format string `json:"format"` // deb | rpm
	Arch   string `json:"arch"`   // Go architecture: amd64, arm64, loong64
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Release is a published version.
type Release struct {
	Version     string            `json:"version"`
	Channel     string            `json:"channel"` // stable | beta
	PublishedAt int64             `json:"publishedAt"`
	Security    bool              `json:"security"`
	Notes       map[string]string `json:"notes"`
	Packages    []Package         `json:"packages"`
}

// Releases is the payload of releases.json.
type Releases struct {
	Type        string    `json:"type"` // "cairn.releases"
	GeneratedAt int64     `json:"generatedAt"`
	Releases    []Release `json:"releases"`
}

// Announcement is an official message.
type Announcement struct {
	ID          string            `json:"id"`
	Level       string            `json:"level"`    // info | warning | critical
	Category    string            `json:"category"` // release | security | maintenance | general
	PublishedAt int64             `json:"publishedAt"`
	ExpiresAt   int64             `json:"expiresAt"` // 0 = no expiry
	Versions    string            `json:"versions"`  // semver constraint; empty = all
	Families    []string          `json:"families"`  // debian | el; empty = all
	Title       map[string]string `json:"title"`
	Body        map[string]string `json:"body"`
	Link        string            `json:"link"`
}

// Announcements is the payload of announcements.json.
type Announcements struct {
	Type        string         `json:"type"` // "cairn.announcements"
	GeneratedAt int64          `json:"generatedAt"`
	Items       []Announcement `json:"items"`
}

// Client downloads and verifies feed files.
type Client struct {
	BaseURL string
	Keys    Keyring
	HTTP    *http.Client
}

// NewHTTPClient returns the client used for cloud downloads: TLS verified by
// default, bounded time, redirects only to http(s).
func NewHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "https" && req.URL.Scheme != "http" {
				return fmt.Errorf("redirect to %s refused", req.URL.Scheme)
			}
			return nil
		},
	}
}

// Get downloads base/name and base/name.sig and returns the verified bytes.
func (c *Client) Get(ctx context.Context, name string) ([]byte, error) {
	if len(c.Keys) == 0 {
		return nil, ErrNoTrustedKey
	}
	data, err := c.download(ctx, name, maxFeedSize)
	if err != nil {
		return nil, err
	}
	sig, err := c.download(ctx, name+".sig", 4096)
	if err != nil {
		return nil, err
	}
	if err := c.Keys.Verify(data, sig); err != nil {
		return nil, err
	}
	return data, nil
}

func (c *Client) download(ctx context.Context, name string, limit int64) ([]byte, error) {
	u, err := url.JoinPath(c.BaseURL, name)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	// No version or platform: filtering happens locally.
	req.Header.Set("User-Agent", "Cairn")
	hc := c.HTTP
	if hc == nil {
		hc = NewHTTPClient(30 * time.Second)
	}
	res, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cloud: get %s: %w", name, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cloud: get %s: HTTP %d", name, res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("cloud: read %s: %w", name, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("cloud: %s is larger than %d bytes", name, limit)
	}
	return data, nil
}

// FetchReleases downloads releases.json; notOlderThan rejects rollbacks.
func (c *Client) FetchReleases(ctx context.Context, notOlderThan int64) (*Releases, error) {
	data, err := c.Get(ctx, ReleasesFile)
	if err != nil {
		return nil, err
	}
	var r Releases
	if err := json.Unmarshal(data, &r); err != nil || r.Type != "cairn.releases" {
		return nil, fmt.Errorf("%w: not a release index", ErrSignature)
	}
	if r.GeneratedAt < notOlderThan {
		return nil, ErrRollback
	}
	return &r, nil
}

// FetchAnnouncements downloads announcements.json; notOlderThan rejects rollbacks.
func (c *Client) FetchAnnouncements(ctx context.Context, notOlderThan int64) (*Announcements, error) {
	data, err := c.Get(ctx, AnnouncementsFile)
	if err != nil {
		return nil, err
	}
	var a Announcements
	if err := json.Unmarshal(data, &a); err != nil || a.Type != "cairn.announcements" {
		return nil, fmt.Errorf("%w: not an announcement feed", ErrSignature)
	}
	if a.GeneratedAt < notOlderThan {
		return nil, ErrRollback
	}
	return &a, nil
}

// channelIncludes reports whether a subscriber of channel receives r:
// the beta channel receives stable releases too.
func channelIncludes(channel, r string) bool {
	return r == "stable" || (channel == "beta" && r == "beta")
}

// Latest returns the newest release above current for the channel that has
// a package for format/arch, or nil.
func (r *Releases) Latest(current semver.Version, channel, format, arch string) (*Release, *Package) {
	var best *Release
	var bestPkg *Package
	var bestV semver.Version
	for i := range r.Releases {
		rel := &r.Releases[i]
		v, err := semver.Parse(rel.Version)
		if err != nil || !channelIncludes(channel, rel.Channel) || semver.Compare(v, current) <= 0 {
			continue
		}
		pkg := rel.Package(format, arch)
		if pkg == nil {
			continue
		}
		if best == nil || semver.Compare(v, bestV) > 0 {
			best, bestPkg, bestV = rel, pkg, v
		}
	}
	return best, bestPkg
}

// Find returns a specific version, if listed.
func (r *Releases) Find(version string) *Release {
	for i := range r.Releases {
		if r.Releases[i].Version == version {
			return &r.Releases[i]
		}
	}
	return nil
}

// Package returns the file for format/arch, if any. The URL scheme is not a
// security boundary: the SHA-256 in the signed index authenticates the file.
func (rel *Release) Package(format, arch string) *Package {
	for i := range rel.Packages {
		p := &rel.Packages[i]
		if p.Format == format && p.Arch == arch && p.URL != "" && len(p.SHA256) == 64 {
			return p
		}
	}
	return nil
}

// Applicable returns the announcements that concern this installation now:
// not expired, matching the version constraint and the distribution family.
// Development builds (current == nil) only see unconstrained messages. Links
// other than https are dropped.
func (a *Announcements) Applicable(current *semver.Version, family string, now time.Time) []Announcement {
	var out []Announcement
	for _, it := range a.Items {
		if it.ID == "" || (it.ExpiresAt > 0 && now.Unix() > it.ExpiresAt) {
			continue
		}
		if it.Versions != "" {
			if current == nil {
				continue
			}
			if ok, err := semver.Satisfies(*current, it.Versions); err != nil || !ok {
				continue
			}
		}
		if len(it.Families) > 0 && !slices.Contains(it.Families, family) {
			continue
		}
		if !strings.HasPrefix(it.Link, "https://") {
			it.Link = ""
		}
		out = append(out, it)
	}
	return out
}
