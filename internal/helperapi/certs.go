// SPDX-License-Identifier: AGPL-3.0-only

package helperapi

import (
	"net/url"
	"regexp"
	"strings"
)

// Certificate actions. cert.issue, cert.import, cert.remove and panel.cert
// are operations; cert.status and dns.test are synchronous.
const (
	ActionCertIssue  = "cert.issue"
	ActionCertImport = "cert.import"
	ActionCertRemove = "cert.remove"
	ActionCertStatus = "cert.status"
	ActionPanelCert  = "panel.cert"
	ActionDNSTest    = "dns.test"
)

// Certificate error codes.
const (
	CodeCertNotFound     = "cert.not_found"
	CodeCertInUse        = "cert.in_use"      // used by sites or the panel
	CodeCertInvalid      = "cert.invalid"     // an uploaded certificate or key does not parse or match
	CodeCertExpired      = "cert.expired"     // an uploaded certificate has expired
	CodeCertACME         = "cert.acme_failed" // the CA refused or validation failed
	CodeCertAccount      = "cert.account_failed"
	CodeCertHTTPNoServer = "cert.http01_unavailable" // nginx not running and port 80 taken
	CodeCertCoverage     = "cert.domains_not_covered"
	CodeDNSCredentials   = "dns.credentials_invalid"
	CodeDNSRequest       = "dns.request_failed"
)

// ACME challenge types.
const (
	ChallengeHTTP01 = "http-01"
	ChallengeDNS01  = "dns-01"
)

// Known ACME CAs and their directories. "custom" takes a directory URL.
var ACMEDirectories = map[string]string{
	"letsencrypt":         "https://acme-v02.api.letsencrypt.org/directory",
	"letsencrypt-staging": "https://acme-staging-v02.api.letsencrypt.org/directory",
	"zerossl":             "https://acme.zerossl.com/v2/DV90",
	"google":              "https://dv.acme-v02.api.pki.goog/directory",
}

// CAs that only accept accounts with external account binding.
var ACMENeedsEAB = map[string]bool{"zerossl": true, "google": true}

// Key types of issued certificates.
var CertKeyTypes = []string{"ec256", "ec384", "rsa2048", "rsa4096"}

// MaxCertDomains caps the names of one certificate (Let's Encrypt's limit).
const MaxCertDomains = 100

var (
	certIDPattern = regexp.MustCompile(`^c[0-9a-f]{15}$`)
	emailPattern  = regexp.MustCompile(`^[^\s@]{1,64}@[^\s@]{1,190}$`)
	eabPattern    = regexp.MustCompile(`^[A-Za-z0-9_=+/-]{1,512}$`)
)

// ValidCertID reports whether s names a certificate.
func ValidCertID(s string) bool { return certIDPattern.MatchString(s) }

// ACMECA is the CA to issue from.
type ACMECA struct {
	// Name is a key of ACMEDirectories or "custom".
	Name string `json:"name"`
	// Directory is the ACME directory of a custom CA (https).
	Directory string `json:"directory,omitempty"`
	// RootPEM adds a trusted root for a custom CA's own HTTPS (internal CAs).
	RootPEM string `json:"rootPem,omitempty"`
	// EAB binds the account to one at the CA.
	EABKid  string `json:"eabKid,omitempty"`
	EABHMAC string `json:"eabHmac,omitempty"`
	// Email is the account contact (optional).
	Email string `json:"email,omitempty"`
}

// DirectoryURL returns the CA's ACME directory.
func (c ACMECA) DirectoryURL() string {
	if c.Name == "custom" {
		return c.Directory
	}
	return ACMEDirectories[c.Name]
}

func (c ACMECA) validate() error {
	switch {
	case c.Name == "custom":
		u, err := url.Parse(c.Directory)
		if err != nil || u.Scheme != "https" || u.Host == "" || len(c.Directory) > 512 {
			return invalid("ca.directory", "https_url")
		}
		if len(c.RootPEM) > 64<<10 || (c.RootPEM != "" && !strings.Contains(c.RootPEM, "-----BEGIN CERTIFICATE-----")) {
			return invalid("ca.rootPem", "pem")
		}
	case ACMEDirectories[c.Name] != "":
		if c.Directory != "" || c.RootPEM != "" {
			return invalid("ca", "custom_fields")
		}
	default:
		return invalid("ca.name", "oneof")
	}
	if (c.EABKid == "") != (c.EABHMAC == "") || (c.EABKid != "" && (!eabPattern.MatchString(c.EABKid) || !eabPattern.MatchString(c.EABHMAC))) {
		return invalid("ca.eab", "pair")
	}
	if ACMENeedsEAB[c.Name] && c.EABKid == "" {
		return invalid("ca.eab", "required")
	}
	if c.Email != "" && !emailPattern.MatchString(c.Email) {
		return invalid("ca.email", "email")
	}
	return nil
}

// DNSCredentials are a DNS provider and its credential fields (see package
// dnsprov).
type DNSCredentials struct {
	Provider string            `json:"provider"`
	Fields   map[string]string `json:"fields"`
}

// CertIssueRequest issues (or renews) a certificate through ACME.
type CertIssueRequest struct {
	ID        string          `json:"id"`
	Domains   []string        `json:"domains"`
	CA        ACMECA          `json:"ca"`
	Challenge string          `json:"challenge"`
	DNS       *DNSCredentials `json:"dns,omitempty"`
	KeyType   string          `json:"keyType"`
}

// Validate implements the helper's request validation hook.
func (r CertIssueRequest) Validate() error {
	if !ValidCertID(r.ID) {
		return invalid("id", "cert_id")
	}
	if len(r.Domains) == 0 || len(r.Domains) > MaxCertDomains {
		return invalid("domains", "count")
	}
	seen := map[string]bool{}
	for _, d := range r.Domains {
		if !ValidHost(d) || strings.Count(d, "*") > 0 && !strings.HasPrefix(d, "*.") || isIPv4(d) || d == "localhost" {
			return invalid("domains", "host")
		}
		if strings.HasPrefix(d, "*.") && r.Challenge != ChallengeDNS01 {
			return invalid("domains", "wildcard_needs_dns")
		}
		if seen[d] {
			return invalid("domains", "unique")
		}
		seen[d] = true
	}
	if err := r.CA.validate(); err != nil {
		return err
	}
	switch r.Challenge {
	case ChallengeHTTP01:
		if r.DNS != nil {
			return invalid("dns", "unexpected")
		}
	case ChallengeDNS01:
		if r.DNS == nil || r.DNS.Provider == "" {
			return invalid("dns", "required")
		}
	default:
		return invalid("challenge", "oneof")
	}
	if !containsString(CertKeyTypes, r.KeyType) {
		return invalid("keyType", "oneof")
	}
	return nil
}

func isIPv4(s string) bool {
	return strings.Trim(s, "0123456789.") == "" && strings.Count(s, ".") == 3
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// CertImportRequest stores an uploaded certificate (PEM chain, leaf first)
// and its private key.
type CertImportRequest struct {
	ID      string `json:"id"`
	CertPEM string `json:"certPem"`
	KeyPEM  string `json:"keyPem"`
}

// Validate implements the helper's request validation hook.
func (r CertImportRequest) Validate() error {
	if !ValidCertID(r.ID) {
		return invalid("id", "cert_id")
	}
	if len(r.CertPEM) == 0 || len(r.CertPEM) > 256<<10 {
		return invalid("certPem", "size")
	}
	if len(r.KeyPEM) == 0 || len(r.KeyPEM) > 64<<10 {
		return invalid("keyPem", "size")
	}
	return nil
}

// CertRef names a certificate.
type CertRef struct {
	ID string `json:"id"`
}

// Validate implements the helper's request validation hook.
func (r CertRef) Validate() error {
	if !ValidCertID(r.ID) {
		return invalid("id", "cert_id")
	}
	return nil
}

// PanelCertRequest makes a certificate the panel's own; ID "" goes back
// to the self-signed certificate.
type PanelCertRequest struct {
	ID string `json:"id"`
}

// Validate implements the helper's request validation hook.
func (r PanelCertRequest) Validate() error {
	if r.ID != "" && !ValidCertID(r.ID) {
		return invalid("id", "cert_id")
	}
	return nil
}

// ChainCert is one certificate of a chain.
type ChainCert struct {
	Subject  string `json:"subject"`
	Issuer   string `json:"issuer"`
	NotAfter int64  `json:"notAfter"`
}

// CertInfo describes a stored certificate (never its key).
type CertInfo struct {
	ID          string      `json:"id"`
	Source      string      `json:"source"` // acme | upload
	Domains     []string    `json:"domains"`
	NotBefore   int64       `json:"notBefore"`
	NotAfter    int64       `json:"notAfter"`
	Issuer      string      `json:"issuer"`
	Serial      string      `json:"serial"`
	Fingerprint string      `json:"fingerprint"` // SHA-256 of the leaf
	KeyType     string      `json:"keyType"`
	Chain       []ChainCert `json:"chain"`
	UpdatedAt   int64       `json:"updatedAt"`
	// Sites lists the sites serving it, Panel whether the panel does.
	Sites []string `json:"sites,omitempty"`
	Panel bool     `json:"panel,omitempty"`
}

// CertStatus is the result of cert.status.
type CertStatus struct {
	Certs map[string]CertInfo `json:"certs"`
	// Panel is the certificate the panel serves ("" for the self-signed one).
	Panel string `json:"panel"`
}

// DNSTestRequest checks DNS credentials by listing a zone's records.
type DNSTestRequest struct {
	DNS  DNSCredentials `json:"dns"`
	Zone string         `json:"zone"`
}

// Validate implements the helper's request validation hook.
func (r DNSTestRequest) Validate() error {
	if !ValidZone(r.Zone) {
		return invalid("zone", "host")
	}
	if r.DNS.Provider == "" {
		return invalid("dns", "required")
	}
	return nil
}

// ValidZone reports whether s names a DNS zone: one or more host name
// labels (internal zones such as "lan" have a single one), with or
// without the final dot.
func ValidZone(s string) bool {
	s = strings.TrimSuffix(s, ".")
	if s == "" || len(s) > 253 {
		return false
	}
	for _, l := range strings.Split(s, ".") {
		if !hostLabel.MatchString(l) {
			return false
		}
	}
	return true
}

// DNSTestResult is the result of dns.test.
type DNSTestResult struct {
	Records int `json:"records"`
}
