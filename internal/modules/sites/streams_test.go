// SPDX-License-Identifier: AGPL-3.0-only

package sites

import (
	"net"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/authz"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

func errCode(err error) string {
	if err == nil {
		return ""
	}
	if ae, ok := err.(*api.Error); ok {
		return ae.Code
	}
	return "error: " + err.Error()
}

// Without the firewall permission a stream may not reach this host, by any
// way of writing its address, nor a metadata service; other hosts of a
// private network stay allowed.
func TestStreamUpstreamsWithoutFirewallPermission(t *testing.T) {
	old := hostAddrs
	hostAddrs = func() ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("192.168.1.10"), netip.MustParseAddr("2001:db8:1::10")}, nil
	}
	t.Cleanup(func() { hostAddrs = old })
	r := httptest.NewRequest("POST", "/api/v1/streams", nil) // no principal: no firewall permission
	for u, want := range map[string]string{
		"127.0.0.1:3306": CodeStreamLocalUpstream, "127.1.2.3:3306": CodeStreamLocalUpstream, "0.0.0.0:22": CodeStreamLocalUpstream,
		"0.1.2.3:22": CodeStreamLocalUpstream, "[::1]:22": CodeStreamLocalUpstream, "[::]:22": CodeStreamLocalUpstream,
		"[::ffff:127.0.0.1]:22": CodeStreamLocalUpstream, "[64:ff9b::7f00:1]:22": CodeStreamLocalUpstream, "[2002:7f00:1::]:22": CodeStreamLocalUpstream,
		// This host's own addresses, as they are and inside other forms.
		"192.168.1.10:3306": CodeStreamLocalUpstream, "[::ffff:192.168.1.10]:3306": CodeStreamLocalUpstream,
		"[64:ff9b::c0a8:10a]:3306": CodeStreamLocalUpstream, "[2001:db8:1::10]:5432": CodeStreamLocalUpstream,
		// Metadata services and link-local addresses.
		"169.254.169.254:80": CodeStreamMetadataUpstream, "100.100.100.200:80": CodeStreamMetadataUpstream,
		"[fd00:ec2::254]:80": CodeStreamMetadataUpstream, "[fe80::1]:22": CodeStreamMetadataUpstream,
		"[64:ff9b::a9fe:a9fe]:80": CodeStreamMetadataUpstream,
		// Names and other ways of writing an address.
		"localhost:22": CodeStreamUpstreamName, "db.example.com:3306": CodeStreamUpstreamName, "[::ffff:7f00:1]:22": CodeStreamUpstreamName,
		"[2001:db8:0::1]:6379": CodeStreamUpstreamName,
		// Other hosts.
		"192.168.1.11:3306": "", "10.0.0.5:3306": "", "172.17.0.1:8080": "", "[fd00::5]:3306": "", "203.0.113.7:25": "",
	} {
		if err := (helperapi.StreamSpec{Code: "tcp-4407", Protocol: "tcp", Port: 4407, Upstreams: []string{u}}).Validate(); err != nil {
			t.Errorf("%s: Validate refuses: %v", u, err)
			continue
		}
		if got := errCode(checkUpstreams(r, []string{u})); got != want {
			t.Errorf("%s: %q, want %q", u, got, want)
		}
	}

	// With the firewall permission (the firewall module's, administrators
	// only), anything Validate takes.
	authz.Define(firewallManage)
	admin := r.WithContext(api.WithPrincipal(r.Context(), &api.Principal{UserID: 1, Username: "admin", Role: string(authz.Admin)}))
	operator := r.WithContext(api.WithPrincipal(r.Context(), &api.Principal{UserID: 2, Username: "oper", Role: string(authz.Operator)}))
	if !anyUpstream(admin) || checkUpstreams(admin, []string{"127.0.0.1:3306", "192.168.1.10:3306", "169.254.169.254:80", "db.example.com:3306"}) != nil {
		t.Error("an administrator may not forward to this host")
	}
	if anyUpstream(r) || anyUpstream(operator) || errCode(checkUpstreams(operator, []string{"192.168.1.10:3306"})) != CodeStreamLocalUpstream {
		t.Error("an operator may forward to this host")
	}
}

// The addresses of the interfaces of the machine the test runs on count
// as this host.
func TestStreamUpstreamsOfThisHostsInterfaces(t *testing.T) {
	r := httptest.NewRequest("POST", "/api/v1/streams", nil)
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok || ipn.IP.IsLoopback() || ipn.IP.IsLinkLocalUnicast() {
			continue
		}
		u := net.JoinHostPort(ipn.IP.String(), "3306")
		if got := errCode(checkUpstreams(r, []string{u})); got != CodeStreamLocalUpstream {
			t.Errorf("own interface address %s accepted as a stream upstream without the firewall permission: %q", u, got)
		}
		checked++
	}
	t.Logf("%d interface addresses checked", checked)
}
