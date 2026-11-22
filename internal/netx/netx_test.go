// SPDX-License-Identifier: AGPL-3.0-only

package netx

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

func TestClassification(t *testing.T) {
	for _, c := range []struct {
		ip                     string
		public, local, metadat bool
	}{
		{"8.8.8.8", true, false, false},
		{"2606:4700:4700::1111", true, false, false},
		{"127.0.0.1", false, true, false},
		{"127.8.9.10", false, true, false},
		{"::1", false, true, false},
		{"::ffff:127.0.0.1", false, true, false},
		{"0.0.0.0", false, true, false},
		{"::", false, true, false},
		{"10.1.2.3", false, false, false},
		{"172.16.0.1", false, false, false},
		{"192.168.1.1", false, false, false},
		{"100.64.0.1", false, false, false},
		{"100.100.100.200", false, false, true},
		{"169.254.169.254", false, false, true},
		{"fe80::1", false, false, true},
		{"fd00:ec2::254", false, false, true},
		{"fc00::1", false, false, false},
		{"224.0.0.1", false, false, true}, // link-local multicast
		{"239.1.2.3", false, false, false},
		{"ff02::1", false, false, true},
		{"255.255.255.255", false, false, false},
		{"64:ff9b::7f00:1", false, true, false},    // NAT64 of 127.0.0.1
		{"64:ff9b::a9fe:a9fe", false, false, true}, // NAT64 of 169.254.169.254
		{"64:ff9b::808:808", true, false, false},   // NAT64 of 8.8.8.8
		{"2002:7f00:1::", false, true, false},      // 6to4 of 127.0.0.1
	} {
		ip := netip.MustParseAddr(c.ip)
		if got := IsPublic(ip); got != c.public {
			t.Errorf("IsPublic(%s) = %v", c.ip, got)
		}
		if got := IsLocal(ip); got != c.local {
			t.Errorf("IsLocal(%s) = %v", c.ip, got)
		}
		if got := IsMetadata(ip); got != c.metadat {
			t.Errorf("IsMetadata(%s) = %v", c.ip, got)
		}
	}
}

type staticResolver map[string][]netip.Addr

func (s staticResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if ips, ok := s[host]; ok {
		return ips, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

func TestCheckResolvesNames(t *testing.T) {
	r := staticResolver{"rebind.example": {netip.MustParseAddr("192.168.1.9"), netip.MustParseAddr("127.0.0.1")}}
	ctx := context.Background()
	if ip, bad := Check(ctx, r, "rebind.example", IsLocal); !bad || ip.String() != "127.0.0.1" {
		t.Fatalf("name resolving to loopback: %v %v", ip, bad)
	}
	for _, h := range []string{"localhost", "db.localhost", "[::1]", "127.0.0.1"} {
		if _, bad := Check(ctx, r, h, IsLocal); !bad {
			t.Errorf("%s passed", h)
		}
	}
	if _, bad := Check(ctx, r, "unknown.example", IsLocal); bad {
		t.Fatal("an unresolvable name is left to the connection")
	}
	if _, bad := Check(ctx, r, "10.0.0.5", LocalOrMetadata); bad {
		t.Fatal("private networks are allowed for storage")
	}
}

func TestDialControlRefusesAfterResolution(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	dialer := &net.Dialer{Timeout: 5 * time.Second, Control: DialControl(Internal)}
	tr := &http.Transport{DialContext: dialer.DialContext}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr}
	res, err := client.Get(srv.URL)
	if err == nil {
		_ = res.Body.Close()
		t.Fatal("loopback server reached")
	}
	var denied *DeniedError
	if !errors.As(err, &denied) || !denied.Addr.IsLoopback() {
		t.Fatalf("want a DeniedError, got %v", err)
	}
}
