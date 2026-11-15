// SPDX-License-Identifier: AGPL-3.0-only

// Package dnsprov describes the DNS providers the panel can manage records
// at (for ACME DNS-01 validation now, record management later): which
// credential fields each takes and which of them are secret. It holds no
// provider code, so the web process can use it without the providers'
// dependencies; the root helper builds the providers (libdns).
package dnsprov

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Field is one credential field of a provider.
type Field struct {
	Name     string `json:"name"`
	Secret   bool   `json:"secret,omitempty"`
	Optional bool   `json:"optional,omitempty"`
}

// Provider is a DNS provider.
type Provider struct {
	ID string `json:"id"`
	// Name is the provider's product name (not translated).
	Name     string  `json:"name"`
	Homepage string  `json:"homepage"`
	Fields   []Field `json:"fields"`
}

// Providers, decided with the project owner (2026-11-14): the libdns
// implementations of the design's list, plus RFC 2136 for self-hosted
// servers (BIND, PowerDNS, Knot).
var Providers = []Provider{
	{ID: "cloudflare", Name: "Cloudflare", Homepage: "https://www.cloudflare.com/",
		Fields: []Field{{Name: "api_token", Secret: true}, {Name: "zone_token", Secret: true, Optional: true}}},
	{ID: "tencentcloud", Name: "Tencent Cloud DNSPod", Homepage: "https://cloud.tencent.com/product/cns",
		Fields: []Field{{Name: "secret_id"}, {Name: "secret_key", Secret: true}}},
	{ID: "alidns", Name: "Alibaba Cloud DNS", Homepage: "https://www.alibabacloud.com/product/dns",
		Fields: []Field{{Name: "access_key_id"}, {Name: "access_key_secret", Secret: true}, {Name: "region_id", Optional: true}}},
	{ID: "huaweicloud", Name: "Huawei Cloud DNS", Homepage: "https://www.huaweicloud.com/product/dns.html",
		Fields: []Field{{Name: "access_key_id"}, {Name: "secret_access_key", Secret: true}, {Name: "region_id", Optional: true}}},
	{ID: "route53", Name: "AWS Route 53", Homepage: "https://aws.amazon.com/route53/",
		Fields: []Field{{Name: "access_key_id"}, {Name: "secret_access_key", Secret: true}, {Name: "region", Optional: true}}},
	{ID: "godaddy", Name: "GoDaddy", Homepage: "https://www.godaddy.com/",
		Fields: []Field{{Name: "api_key"}, {Name: "api_secret", Secret: true}}},
	{ID: "volcengine", Name: "Volcengine DNS", Homepage: "https://www.volcengine.com/product/dns",
		Fields: []Field{{Name: "access_key_id"}, {Name: "access_key_secret", Secret: true}, {Name: "region_id", Optional: true}}},
	{ID: "westcn", Name: "West.cn", Homepage: "https://www.west.cn/",
		Fields: []Field{{Name: "username"}, {Name: "api_password", Secret: true}}},
	{ID: "rfc2136", Name: "RFC 2136 (BIND, PowerDNS, Knot)", Homepage: "https://www.rfc-editor.org/rfc/rfc2136",
		Fields: []Field{{Name: "server"}, {Name: "key_name"}, {Name: "key", Secret: true}, {Name: "key_alg", Optional: true}}},
}

// Find returns the provider with id.
func Find(id string) (Provider, bool) {
	for _, p := range Providers {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

// Field values: printable, no spaces, bounded. Credentials of the listed
// providers never need more.
var fieldValue = regexp.MustCompile(`^[\x21-\x7e]{1,512}$`)

// ErrUnknownProvider and the other errors name what is wrong; callers map
// them to API errors.
type FieldError struct {
	Field string
	Rule  string
}

func (e *FieldError) Error() string { return fmt.Sprintf("dns credentials: %s %s", e.Field, e.Rule) }

// Validate checks credentials for a provider: every required field set,
// no unknown ones, values printable.
func Validate(provider string, fields map[string]string) error {
	p, ok := Find(provider)
	if !ok {
		return &FieldError{Field: "provider", Rule: "oneof"}
	}
	for name, v := range fields {
		if !slices.ContainsFunc(p.Fields, func(f Field) bool { return f.Name == name }) {
			return &FieldError{Field: name, Rule: "unexpected"}
		}
		if v != "" && !fieldValue.MatchString(v) {
			return &FieldError{Field: name, Rule: "printable"}
		}
	}
	for _, f := range p.Fields {
		if !f.Optional && strings.TrimSpace(fields[f.Name]) == "" {
			return &FieldError{Field: f.Name, Rule: "required"}
		}
	}
	return nil
}
