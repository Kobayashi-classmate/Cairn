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
	// SiteApp is a project: a process the panel runs (a systemd unit as the
	// site's runner) behind nginx.
	SiteApp = "app"
)

// Site error codes.
const (
	CodeSiteUnsupported   = "site.unsupported"    // not on this distribution yet
	CodeSiteSELinux       = "site.selinux"        // an SELinux label could not be set
	CodeSiteNoNginx       = "site.nginx_missing"  // nginx is not installed
	CodeSitePHPMissing    = "site.php_missing"    // the PHP line is not installed (or has no FPM)
	CodeSitePHPUnknown    = "site.php_unknown"    // the catalog does not know the site's current PHP line here, so its pool cannot be found; nothing was changed
	CodeSiteDomainTaken   = "site.domain_taken"   // another site serves the domain
	CodeSiteConfigInvalid = "site.config_invalid" // nginx -t or php-fpm -t refused the change; nothing was changed
	CodeSiteReloadFailed  = "site.reload_failed"  // the reload failed; the previous configuration was restored
	CodeSiteNotFound      = "site.not_found"
	CodeSiteRewrite       = "site.rewrite_unknown"
	CodeSiteCertMissing   = "site.cert_missing"  // the site's certificate is not stored
	CodeSiteCertCoverage  = "site.cert_coverage" // the certificate does not cover a domain of the site
	// CodeSiteUserConflict: a user of the site's names exists but does not
	// belong to the site's group (made for something else).
	CodeSiteUserConflict = "site.user_conflict"
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
	proxyPath   = regexp.MustCompile(`^/[A-Za-z0-9._~/%+,=@!*()-]{0,255}$`)
	// An upstream URL as nginx gets it: already escaped, with nothing
	// nginx's configuration syntax gives a meaning to (spaces, quotes,
	// braces, semicolons, backslashes, $ variables, ? and #).
	proxyURLChars = regexp.MustCompile(`^[A-Za-z0-9._~/%:\[\]+,=@!*()-]+$`)
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
	// App is the process of an app site.
	App *AppSpec `json:"app,omitempty"`
	// TLS serves the site over HTTPS with a stored certificate.
	TLS *SiteTLS `json:"tls,omitempty"`
	// AllowSymlinks lets the site's links cross its own two users (static
	// and PHP sites). nginx serves a link only when the link and the file
	// it points at have the same owner, so that one site's code cannot link
	// another site's files into its web root. With this on, each apply
	// gives the site's links (of root, the owner or the runner) that lead
	// to a file of the owner or the runner that file's owner, which serves
	// Laravel's public/storage made by root or pointing into directories
	// the runner created; links to anything else stay refused.
	AllowSymlinks bool `json:"allowSymlinks,omitempty"`
	// Enabled false keeps the site configured but answers 503.
	Enabled bool `json:"enabled"`
}

// App runtimes (catalog software IDs); "" runs the command with the
// system's tools only (a binary in the site).
var AppRuntimes = []string{"nodejs", "python", "go", "java", "dotnet"}

// App ports the panel hands out (it may also be set by hand, from 1024).
const (
	AppPortMin = 20000
	AppPortMax = 29999
)

// EnvVar is one environment variable of an app.
type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// AppSpec is how an app site's process runs.
type AppSpec struct {
	// Runtime and Line pick an installed runtime, put first on PATH (with
	// JAVA_HOME or DOTNET_ROOT where they apply).
	Runtime string `json:"runtime,omitempty"`
	Line    string `json:"line,omitempty"`
	// Command is one shell command line, run by /bin/sh in WorkDir as the
	// site's runner; it must listen on 127.0.0.1:$PORT.
	Command string `json:"command"`
	// WorkDir is relative to the app directory.
	WorkDir string   `json:"workDir,omitempty"`
	Port    int      `json:"port"`
	Env     []EnvVar `json:"env,omitempty"`
	// MemoryMB and CPUPercent limit the process (0: no limit); CPUPercent
	// is of one CPU (200 = two CPUs).
	MemoryMB   int `json:"memoryMb,omitempty"`
	CPUPercent int `json:"cpuPercent,omitempty"`
	// HealthPath is checked over HTTP by the panel; failing checks restart
	// the app. Empty: no check.
	HealthPath string `json:"healthPath,omitempty"`
	// WebSocket passes connection upgrades through nginx.
	WebSocket bool `json:"webSocket,omitempty"`
}

var (
	envName      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
	healthPathRe = regexp.MustCompile(`^/[A-Za-z0-9._~/%?&=+-]{0,255}$`)
)

// noControl reports whether s has no control characters (newlines would
// break out of a systemd unit line).
func noControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func (a *AppSpec) validate() error {
	if a.Runtime != "" && !containsString(AppRuntimes, a.Runtime) {
		return invalid("app.runtime", "oneof")
	}
	if (a.Runtime == "") != (a.Line == "") || (a.Line != "" && !linePattern.MatchString(a.Line)) {
		return invalid("app.line", "line")
	}
	if strings.TrimSpace(a.Command) == "" || len(a.Command) > 4096 || !noControl(a.Command) {
		return invalid("app.command", "command")
	}
	if !ValidRelPath(a.WorkDir) {
		return invalid("app.workDir", "path")
	}
	if a.Port < 1024 || a.Port > 65535 {
		return invalid("app.port", "port")
	}
	if len(a.Env) > 64 {
		return invalid("app.env", "count")
	}
	seen := map[string]bool{}
	for _, e := range a.Env {
		if !envName.MatchString(e.Name) || seen[e.Name] {
			return invalid("app.env", "name")
		}
		seen[e.Name] = true
		if len(e.Value) > 4096 || !noControl(e.Value) {
			return invalid("app.env", "value")
		}
	}
	if a.MemoryMB < 0 || a.MemoryMB > 1<<20 || (a.MemoryMB > 0 && a.MemoryMB < 16) {
		return invalid("app.memoryMb", "range")
	}
	if a.CPUPercent < 0 || a.CPUPercent > 6400 || (a.CPUPercent > 0 && a.CPUPercent < 5) {
		return invalid("app.cpuPercent", "range")
	}
	if a.HealthPath != "" && !healthPathRe.MatchString(a.HealthPath) {
		return invalid("app.healthPath", "path")
	}
	return nil
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

// ValidSiteCode reports whether s can name a site (or a stream).
func ValidSiteCode(s string) bool {
	return siteCodePattern.MatchString(s) && !strings.Contains(s, "--")
}

// ValidNewSiteCode reports whether a new site may take code s. A site's
// users are cw-<code> and cw-<code>-run, so a new code ending in -run is
// refused: site "x-run" would get the user of site "x"'s runner. Sites
// that already have such a code keep working (the helper refuses to take
// over a user of another site's group anyway).
func ValidNewSiteCode(s string) bool {
	return ValidSiteCode(s) && !strings.HasSuffix(s, "-run")
}

// ErrNewSiteCode is the validation error of a new site's code ending in
// -run.
func ErrNewSiteCode() error { return invalid("code", "site_code_new") }

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
	// The URL goes into nginx's configuration as it is written.
	if !proxyURLChars.MatchString(u) {
		return false
	}
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
	case SiteStatic, SitePHP, SiteProxy, SiteApp:
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
	if s.Type != SiteApp && s.App != nil {
		return invalid("app", "unexpected")
	}
	if s.AllowSymlinks && s.Type != SiteStatic && s.Type != SitePHP {
		return invalid("allowSymlinks", "file_sites")
	}
	switch s.Type {
	case SiteApp:
		if s.App == nil {
			return invalid("app", "required")
		}
		if err := s.App.validate(); err != nil {
			return err
		}
		if s.PHP != "" || s.Rewrite != "" || s.Root != "" || len(s.Index) > 0 || s.Proxy != nil {
			return invalid("type", "app_fields")
		}
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
	// Sites are the sites the helper has applied, by code; Streams the
	// four-layer proxies.
	Sites   map[string]SiteState  `json:"sites"`
	Streams map[string]StreamSpec `json:"streams"`
	// Systemd is false where apps cannot run (containers without it).
	Systemd bool `json:"systemd"`
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

// App actions: synchronous status and log reads, and a restart operation.
const (
	ActionAppStatus  = "app.status"
	ActionAppLog     = "app.log"
	ActionAppRestart = "app.restart"
)

// App error codes.
const (
	CodeAppNoSystemd      = "app.systemd_required" // apps run as systemd units
	CodeAppRuntimeMissing = "app.runtime_missing"  // the runtime line is not installed
	CodeAppPortTaken      = "app.port_taken"       // another site or stream uses the port
	CodeAppNotFound       = "app.not_found"
)

// AppRef names an app site.
type AppRef struct {
	Code string `json:"code"`
}

// Validate implements the helper's request validation hook.
func (r AppRef) Validate() error {
	if !ValidSiteCode(r.Code) {
		return invalid("code", "site_code")
	}
	return nil
}

// AppStatus is what systemd reports about an app's unit.
type AppStatus struct {
	Code        string `json:"code"`
	ActiveState string `json:"activeState"` // active | inactive | failed | activating ...
	SubState    string `json:"subState"`
	MainPID     int    `json:"mainPid,omitempty"`
	Restarts    int    `json:"restarts"`
	MemoryBytes int64  `json:"memoryBytes,omitempty"`
	CPUNanos    int64  `json:"cpuNanos,omitempty"`
	// Since is when the unit entered its active state (unix seconds).
	Since  int64  `json:"since,omitempty"`
	Result string `json:"result,omitempty"` // success | exit-code | signal | oom-kill ...
}

// AppLogRequest reads an app's journal: the last lines, or the lines after
// a cursor returned before.
type AppLogRequest struct {
	Code   string `json:"code"`
	Cursor string `json:"cursor,omitempty"`
}

var journalCursor = regexp.MustCompile(`^[A-Za-z0-9=;_-]{1,512}$`)

// Validate implements the helper's request validation hook.
func (r AppLogRequest) Validate() error {
	if !ValidSiteCode(r.Code) {
		return invalid("code", "site_code")
	}
	if r.Cursor != "" && !journalCursor.MatchString(r.Cursor) {
		return invalid("cursor", "cursor")
	}
	return nil
}

// AppLogResponse is a chunk of an app's journal.
type AppLogResponse struct {
	Lines  []string `json:"lines"`
	Cursor string   `json:"cursor,omitempty"`
}

// Stream (four-layer proxy) actions.
const (
	ActionStreamApply  = "stream.apply"
	ActionStreamRemove = "stream.remove"
)

// Stream error codes.
const (
	CodeStreamModuleMissing = "stream.module_missing" // nginx's stream module is not installed
	CodeStreamPortTaken     = "stream.port_taken"
	CodeStreamNotFound      = "stream.not_found"
)

// Stream protocols.
const (
	StreamTCP = "tcp"
	StreamUDP = "udp"
)

// MaxStreamUpstreams caps the upstreams of a stream.
const MaxStreamUpstreams = 16

// StreamSpec is a four-layer (TCP or UDP) proxy: nginx listens on Port on
// every address and forwards to the upstreams.
type StreamSpec struct {
	Code      string   `json:"code"`
	Protocol  string   `json:"protocol"`
	Port      int      `json:"port"`
	Upstreams []string `json:"upstreams"` // host:port
	// Balance "hash" keeps a client on one upstream; "" is round robin.
	Balance string `json:"balance,omitempty"`
	Enabled bool   `json:"enabled"`
}

// ValidHostPort reports whether s is "host:port" with a host name, an IPv4
// address or a bracketed IPv6 address.
func ValidHostPort(s string) bool {
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return false
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return true
	}
	return ValidHost(host) || host == "localhost"
}

// Validate implements the helper's request validation hook.
func (s StreamSpec) Validate() error {
	if !ValidSiteCode(s.Code) {
		return invalid("code", "site_code")
	}
	if s.Protocol != StreamTCP && s.Protocol != StreamUDP {
		return invalid("protocol", "oneof")
	}
	if s.Port < 1 || s.Port > 65535 || s.Port == 80 || s.Port == 443 || (s.Port >= AppPortMin && s.Port <= AppPortMax) {
		return invalid("port", "port")
	}
	if len(s.Upstreams) == 0 || len(s.Upstreams) > MaxStreamUpstreams {
		return invalid("upstreams", "count")
	}
	seen := map[string]bool{}
	for _, u := range s.Upstreams {
		if !ValidHostPort(u) || seen[u] {
			return invalid("upstreams", "host_port")
		}
		seen[u] = true
	}
	if s.Balance != "" && s.Balance != "hash" {
		return invalid("balance", "oneof")
	}
	return nil
}

// StreamRef names a stream.
type StreamRef struct {
	Code string `json:"code"`
}

// Validate implements the helper's request validation hook.
func (r StreamRef) Validate() error {
	if !ValidSiteCode(r.Code) {
		return invalid("code", "site_code")
	}
	return nil
}
