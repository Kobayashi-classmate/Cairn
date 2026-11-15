// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-acme/lego/v4/challenge/dns01"
	"github.com/libdns/alidns"
	"github.com/libdns/cloudflare"
	"github.com/libdns/godaddy"
	"github.com/libdns/huaweicloud"
	"github.com/libdns/libdns"
	"github.com/libdns/rfc2136"
	"github.com/libdns/route53"
	"github.com/libdns/tencentcloud"
	"github.com/libdns/volcengine"
	"github.com/libdns/westcn"

	"github.com/Kobayashi-classmate/Cairn/internal/dnsprov"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// dnsProvider is what the panel needs from a libdns provider.
type dnsProvider interface {
	libdns.RecordGetter
	libdns.RecordAppender
	libdns.RecordDeleter
}

// newDNSProvider builds a provider from checked credentials. It never
// falls back to ambient credentials (environment, instance metadata): the
// fields are all it uses.
func newDNSProvider(c helperapi.DNSCredentials) (dnsProvider, error) {
	if err := dnsprov.Validate(c.Provider, c.Fields); err != nil {
		return nil, pkgErr(helperapi.CodeDNSCredentials, map[string]any{"reason": err.Error()})
	}
	f := c.Fields
	switch c.Provider {
	case "cloudflare":
		return &cloudflare.Provider{APIToken: f["api_token"], ZoneToken: f["zone_token"]}, nil
	case "tencentcloud":
		return &tencentcloud.Provider{SecretId: f["secret_id"], SecretKey: f["secret_key"]}, nil
	case "alidns":
		return &alidns.Provider{CredentialInfo: alidns.CredentialInfo{AccessKeyID: f["access_key_id"], AccessKeySecret: f["access_key_secret"], RegionID: f["region_id"]}}, nil
	case "huaweicloud":
		return &huaweicloud.Provider{AccessKeyId: f["access_key_id"], SecretAccessKey: f["secret_access_key"], RegionId: f["region_id"]}, nil
	case "route53":
		return &route53.Provider{AccessKeyId: f["access_key_id"], SecretAccessKey: f["secret_access_key"], Region: f["region"]}, nil
	case "godaddy":
		return &godaddy.Provider{APIToken: f["api_key"] + ":" + f["api_secret"]}, nil
	case "volcengine":
		return &volcengine.Provider{CredentialInfo: volcengine.CredentialInfo{AccessKeyID: f["access_key_id"], AccessKeySecret: f["access_key_secret"], RegionID: f["region_id"]}}, nil
	case "westcn":
		return &westcn.Provider{Username: f["username"], APIPassword: f["api_password"]}, nil
	case "rfc2136":
		alg := f["key_alg"]
		if alg == "" {
			alg = "hmac-sha256"
		}
		return &rfc2136.Provider{Server: f["server"], KeyName: f["key_name"], Key: f["key"], KeyAlg: alg}, nil
	}
	return nil, pkgErr(helperapi.CodeDNSCredentials, map[string]any{"reason": "unknown provider " + c.Provider})
}

// dnsChallenge solves ACME DNS-01 challenges with a libdns provider: a TXT
// record at _acme-challenge (after CNAMEs), removed again afterwards.
type dnsChallenge struct {
	p         dnsProvider
	resolvers []string
	log       func(format string, a ...any)
	// findZone replaces the DNS lookup of the zone (tests).
	findZone func(fqdn string) (string, error)

	mu    sync.Mutex
	added map[string]added // by domain and token
}

type added struct {
	zone    string
	records []libdns.Record
}

const dnsCallTimeout = 2 * time.Minute

func (d *dnsChallenge) zoneOf(fqdn string) (string, error) {
	if d.findZone != nil {
		return d.findZone(fqdn)
	}
	if len(d.resolvers) > 0 {
		return dns01.FindZoneByFqdnCustom(fqdn, d.resolvers)
	}
	return dns01.FindZoneByFqdn(fqdn)
}

// Present implements challenge.Provider.
func (d *dnsChallenge) Present(domain, token, keyAuth string) error {
	// The record of a wildcard name is that of its base name.
	info := dns01.GetChallengeInfo(strings.TrimPrefix(domain, "*."), keyAuth)
	zone, err := d.zoneOf(info.EffectiveFQDN)
	if err != nil {
		return fmt.Errorf("finding the DNS zone of %s: %w", info.EffectiveFQDN, err)
	}
	name := libdns.RelativeName(info.EffectiveFQDN, zone)
	d.log("Adding TXT %s in zone %s\n", name, zone)
	ctx, cancel := context.WithTimeout(context.Background(), dnsCallTimeout)
	defer cancel()
	recs, err := d.p.AppendRecords(ctx, zone, []libdns.Record{libdns.TXT{Name: name, TTL: 120 * time.Second, Text: info.Value}})
	if err != nil {
		return fmt.Errorf("adding the TXT record: %w", err)
	}
	d.mu.Lock()
	if d.added == nil {
		d.added = map[string]added{}
	}
	d.added[domain+" "+token] = added{zone: zone, records: recs}
	d.mu.Unlock()
	return nil
}

// CleanUp implements challenge.Provider.
func (d *dnsChallenge) CleanUp(domain, token, _ string) error {
	d.mu.Lock()
	a, ok := d.added[domain+" "+token]
	delete(d.added, domain+" "+token)
	d.mu.Unlock()
	if !ok || len(a.records) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), dnsCallTimeout)
	defer cancel()
	if _, err := d.p.DeleteRecords(ctx, a.zone, a.records); err != nil {
		d.log("Could not remove the TXT record in %s: %v\n", a.zone, err)
	}
	return nil
}

// Timeout implements challenge.ProviderTimeout: DNS changes may take a few
// minutes to reach every authoritative server.
func (d *dnsChallenge) Timeout() (timeout, interval time.Duration) {
	return 10 * time.Minute, 10 * time.Second
}

// testDNS checks credentials by listing the records of a zone.
func testDNS(ctx context.Context, req helperapi.DNSTestRequest) (helperapi.DNSTestResult, error) {
	p, err := newDNSProvider(req.DNS)
	if err != nil {
		return helperapi.DNSTestResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	recs, err := p.GetRecords(ctx, strings.TrimSuffix(req.Zone, ".")+".")
	if err != nil {
		return helperapi.DNSTestResult{}, pkgErr(helperapi.CodeDNSRequest, map[string]any{"reason": tailString(err.Error(), 300)})
	}
	return helperapi.DNSTestResult{Records: len(recs)}, nil
}
