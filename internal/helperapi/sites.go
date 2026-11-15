// SPDX-License-Identifier: AGPL-3.0-only

package helperapi

import (
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Site actions. site.apply and site.remove are operations; the others are
// synchronous.
const (
	ActionSiteApply  = "site.apply"
	ActionSiteRemove = "site.remove"
	ActionSiteStatus = "site.status"
	ActionSiteLog    = "site.log"
)

// Site types.
const (
	SiteStatic = "static"
	SitePHP    = "php"
	SiteProxy  = "proxy"
)

// Site error codes.
const (
	CodeSiteUnsupported   = "site.unsupported"    // not on this distribution yet
	CodeSiteNoNginx       = "site.nginx_missing"  // nginx is not installed
	CodeSitePHPMissing    = "site.php_missing"    // the PHP line is not installed (or has no FPM)
	CodeSiteDomainTaken   = "site.domain_taken"   // another site serves the domain
	CodeSiteConfigInvalid = "site.config_invalid" // nginx -t or php-fpm -t refused the change; nothing was changed
	CodeSiteReloadFailed  = "site.reload_failed"  // the reload failed; the previous configuration was restored
	CodeSiteNotFound      = "site.not_found"
	CodeSiteRewrite       = "site.rewrite_unknown"
	CodeSiteCertMissing   = "site.cert_missing"  // the site's certificate is not stored
	CodeSiteCertCoverage  = "site.cert_coverage" // the certificate does not cover a domain of the site
)

// MaxSiteDomains caps the domains of one site.
const MaxSiteDomains = 32

var (
	// A site code names the site's directory and users: 2-20 characters.
	siteCodePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,18}[a-z0-9]$`)
	hostLabel       = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	// Relative paths inside a site: plain names separated by slashes.
	relPathPart = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
	indexName   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	proxyPath   = regexp.MustCompile(`^/[A-Za-z0-9._~/%-]{0,255}$`)
)

// ProxySpec is the upstream of a reverse proxy site.
type ProxySpec struct {
	// URL is the upstream, http(s)://host[:port][/path].
	URL string `json:"url"`
	// WebSocket passes connection upgrades through.
	WebSocket bool `json:"webSocket,omitempty"`
	// UpstreamHost sends the upstream's own host name instead of the one
	// the client asked for.
	UpstreamHost bool `json:"upstreamHost,omitempty"`
}

// SiteSpec describes a site completely; site.apply makes the system match
// it. Every field is checked again by the helper.
type SiteSpec struct {
	Code    string   `json:"code"`
	Type    string   `json:"type"`
	Domains []string `json:"domains"`
	// Root is the web root relative to the site's app directory ("" for the
	// app directory itself, "public" for Laravel).
	Root  string   `json:"root,omitempty"`
	Index []string `json:"index,omitempty"`
	// Rewrite names a built-in rewrite rule (static and PHP sites).
	Rewrite string `json:"rewrite,omitempty"`
	// PHP is the PHP line of a PHP site.
	PHP string `json:"php,omitempty"`
	// Writable are directories relative to the app directory that the
	// site's runtime user may write (uploads, caches). Everything else is
	// read-only to it.
	Writable []string   `json:"writable,omitempty"`
	Proxy    *ProxySpec `json:"proxy,omitempty"`
	// TLS serves the site over HTTPS with a stored certificate.
	TLS *SiteTLS `json:"tls,omitempty"`
	// Enabled false keeps the site configured but answers 503.
	Enabled bool `json:"enabled"`
}

// SiteTLS is a site's HTTPS: the certificate (which must cover every
// domain of the site), whether plain HTTP redirects to HTTPS, and HSTS.
type SiteTLS struct {
	Cert     string `json:"cert"`
	Redirect bool   `json:"redirect,omitempty"`
	HSTS     bool   `json:"hsts,omitempty"`
}

func invalid(field, rule string) error {
	return &Error{Code: "validation.invalid", Params: map[string]any{"field": field, "rule": rule}}
}

// ValidSiteCode reports whether s can name a site.
func ValidSiteCode(s string) bool {
	return siteCodePattern.MatchString(s) && !strings.Contains(s, "--")
}

// ValidHost reports whether s is a host name a site may serve: lower-case
// ASCII (punycode for international names), optionally starting with "*."
// for a wildcard, or an IPv4 address.
func ValidHost(s string) bool {
	if len(s) == 0 || len(s) > 253 {
		return false
	}
	if ip := net.ParseIP(s); ip != nil {
		return ip.To4() != nil
	}
	name := strings.TrimPrefix(s, "*.")
	labels := strings.Split(name, ".")
	if len(labels) < 2 && (name != "localhost" || name != s) {
		return false
	}
	for _, l := range labels {
		if !hostLabel.MatchString(l) {
			return false
		}
	}
	// The last label of a name is never all digits.
	_, err := strconv.Atoi(labels[len(labels)-1])
	return err != nil
}

// ValidRelPath reports whether p is a relative path inside a site ("" is
// the site's app directory).
func ValidRelPath(p string) bool {
	if p == "" {
		return true
	}
	if len(p) > 512 || strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/") {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == "." || part == ".." || !relPathPart.MatchString(part) {
			return false
		}
	}
	return true
}

// ValidProxyURL reports whether u is an upstream a proxy site may use.
func ValidProxyURL(u string) bool {
	p, err := url.Parse(u)
	if err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.User != nil || p.RawQuery != "" || p.Fragment != "" || p.Opaque != "" {
		return false
	}
	host := p.Hostname()
	if ip := net.ParseIP(host); ip == nil && !ValidHost(host) && host != "localhost" {
		return false
	}
	if port := p.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return false
		}
	}
	return p.Path == "" || proxyPath.MatchString(p.EscapedPath())
}

// Validate implements the helper's request validation hook: the shape of
// every field. Whether the PHP line is installed or a domain is free is
// checked when the site is applied.
func (s SiteSpec) Validate() error {
	if !ValidSiteCode(s.Code) {
		return invalid("code", "site_code")
	}
	switch s.Type {
	case SiteStatic, SitePHP, SiteProxy:
	default:
		return invalid("type", "oneof")
	}
	if len(s.Domains) == 0 || len(s.Domains) > MaxSiteDomains {
		return invalid("domains", "count")
	}
	seen := map[string]bool{}
	for _, d := range s.Domains {
		if !ValidHost(d) {
			return invalid("domains", "host")
		}
		if seen[d] {
			return invalid("domains", "unique")
		}
		seen[d] = true
	}
	if len(s.Index) > 8 {
		return invalid("index", "count")
	}
	for _, i := range s.Index {
		if !indexName.MatchString(i) || i == "." || i == ".." {
			return invalid("index", "file_name")
		}
	}
	if len(s.Writable) > 16 {
		return invalid("writable", "count")
	}
	for _, w := range s.Writable {
		if w == "" || !ValidRelPath(w) {
			return invalid("writable", "path")
		}
	}
	if s.Rewrite != "" && !softwareIDPattern.MatchString(s.Rewrite) {
		return invalid("rewrite", "id")
	}
	if s.TLS != nil && !ValidCertID(s.TLS.Cert) {
		return invalid("tls.cert", "cert_id")
	}
	switch s.Type {
	case SiteProxy:
		if s.Proxy == nil || !ValidProxyURL(s.Proxy.URL) {
			return invalid("proxy", "url")
		}
		if s.PHP != "" || s.Rewrite != "" || s.Root != "" || len(s.Writable) > 0 || len(s.Index) > 0 {
			return invalid("type", "proxy_fields")
		}
	case SitePHP:
		if !linePattern.MatchString(s.PHP) {
			return invalid("php", "line")
		}
		fallthrough
	default:
		if s.Proxy != nil {
			return invalid("proxy", "unexpected")
		}
		if !ValidRelPath(s.Root) {
			return invalid("root", "path")
		}
		if s.Type == SiteStatic && (s.PHP != "" || len(s.Writable) > 0) {
			return invalid("type", "static_fields")
		}
	}
	return nil
}

// SiteRemoveRequest removes a site's configuration and, with DeleteFiles,
// its directory and users.
type SiteRemoveRequest struct {
	Code        string `json:"code"`
	DeleteFiles bool   `json:"deleteFiles,omitempty"`
}

// Validate implements the helper's request validation hook.
func (r SiteRemoveRequest) Validate() error {
	if !ValidSiteCode(r.Code) {
		return invalid("code", "site_code")
	}
	return nil
}

// SiteResult is the result of site.apply and site.remove.
type SiteResult struct {
	Code string `json:"code"`
	// Dir is the site directory, Owner and Runner its two users.
	Dir    string `json:"dir"`
	Owner  string `json:"owner"`
	Runner string `json:"runner"`
	// Reloaded lists the services reloaded (nginx, php8.4-fpm).
	Reloaded []string `json:"reloaded,omitempty"`
}

// SiteStatus is the result of site.status.
type SiteStatus struct {
	// Supported is false where sites are not available yet (EL until the
	// SELinux verification); Reason says why.
	Supported bool   `json:"supported"`
	Reason    string `json:"reason,omitempty"`
	Nginx     struct {
		Installed bool   `json:"installed"`
		Running   bool   `json:"running"`
		Version   string `json:"version,omitempty"`
	} `json:"nginx"`
	// PHP lists the installed PHP lines whose FPM can host site pools.
	PHP []string `json:"php"`
	// Sites are the sites the helper has applied, by code.
	Sites map[string]SiteState `json:"sites"`
}

// SiteState is what the helper last applied for a site.
type SiteState struct {
	Spec      SiteSpec `json:"spec"`
	AppliedAt int64    `json:"appliedAt"`
}

// Site log kinds.
const (
	SiteLogAccess = "access"
	SiteLogError  = "error"
)

// SiteLogRequest reads a site's nginx log from Offset (-1: the last part).
type SiteLogRequest struct {
	Code   string `json:"code"`
	Kind   string `json:"kind"`
	Offset int64  `json:"offset"`
}

// Validate implements the helper's request validation hook.
func (r SiteLogRequest) Validate() error {
	if !ValidSiteCode(r.Code) {
		return invalid("code", "site_code")
	}
	if r.Kind != SiteLogAccess && r.Kind != SiteLogError {
		return invalid("kind", "oneof")
	}
	if r.Offset < -1 {
		return invalid("offset", "min")
	}
	return nil
}

// SiteLogResponse is a chunk of a site log.
type SiteLogResponse struct {
	Data string `json:"data"`
	// Offset is where the next read continues; Size the file size. A file
	// smaller than the requested offset was rotated: reading restarts at 0.
	Offset int64 `json:"offset"`
	Size   int64 `json:"size"`
}
