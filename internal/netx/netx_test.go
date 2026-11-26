// SPDX-License-Identifier: AGPL-3.0-only

package netx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"syscall"
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

// An IPv6 literal with a zone is the same address for every guard: the
// zone only picks the interface, and Linux connects "::" with any zone to
// "::1".
func TestClassificationIgnoresZones(t *testing.T) {
	for _, c := range []struct {
		ip                     string
		public, local, metadat bool
	}{
		{"::%lo", false, true, false},
		{"::%x", false, true, false},
		{"::1%lo", false, true, false},
		{"::ffff:127.0.0.1%lo", false, true, false},
		{"64:ff9b::7f00:1%x", false, true, false},
		{"64:ff9b::a9fe:a9fe%x", false, false, true},
		{"2002:7f00:1::%x", false, true, false},
		{"fd00:ec2::254%x", false, false, true},
		{"fe80::1%eth0", false, false, true},
		{"fc00::1%x", false, false, false},
		{"fec0::1%x", false, false, false},
		// A public address stays public, whatever interface is named.
		{"2606:4700:4700::1111%eth0", true, false, false},
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
		if got := LocalOrMetadata(ip); got != (c.local || c.metadat) {
			t.Errorf("LocalOrMetadata(%s) = %v", c.ip, got)
		}
	}
}

// localForms are URL hosts that all point at this host, the internal
// network or a metadata service.
var localForms = []string{
	"[::1]",
	"[::%25lo]",
	"[::%25x]",
	"[::1%25lo]",
	"[fe80::1%25eth0]",
	"[::ffff:127.0.0.1]",
	"[::ffff:127.0.0.1%25lo]",
	"0.0.0.0",
	"[::]",
	"2130706433",
	"0x7f000001",
	"0x7f.1",
	"localhost",
	"localhost.",
	"127.0.0.1.",
	"[64:ff9b::7f00:1]",
	"[64:ff9b::7f00:1%25x]",
	"[64:ff9b::a9fe:a9fe%25x]",
	"[2002:7f00:1::%25x]",
	"169.254.169.254",
	"[fd00:ec2::254]",
	"[fd00:ec2::254%25x]",
	"[fc00::1%25x]",
	"[fec0::1%25x]",
}

// Every form, dialed through a dialer whose Control records the address
// and then runs DialControl(Internal), is refused at Control. The recorder
// never lets a connection through, so nothing is contacted.
func TestDialControlRefusesLocalForms(t *testing.T) {
	guard := DialControl(Internal)
	for _, h := range localForms {
		u, err := url.Parse("http://" + h + ":9/")
		if err != nil {
			continue // a form the URL parser refuses never gets this far
		}
		var seen string
		var verdict error
		stop := errors.New("recorded")
		d := &net.Dialer{Timeout: 3 * time.Second, Control: func(network, address string, c syscall.RawConn) error {
			if seen == "" {
				seen, verdict = address, guard(network, address, c)
			}
			return stop
		}}
		_, _ = d.DialContext(context.Background(), "tcp", u.Host)
		if seen == "" {
			continue // never resolved to an address (an unknown interface)
		}
		var denied *DeniedError
		if !errors.As(verdict, &denied) {
			t.Errorf("%s: Control saw %s and let it through (%v)", h, seen, verdict)
		}
	}
}

// A server listening on [::1] is not reached through the unspecified
// address with a zone, the way a download from a URL would try it; the
// loopback address itself is refused as before.
func TestZoneLiteralDoesNotReachLoopback(t *testing.T) {
	ln, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skipf("no IPv6 loopback here: %v", err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "from the loopback")
	}), ReadHeaderTimeout: time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port

	dialer := &net.Dialer{Timeout: 3 * time.Second, Control: DialControl(Internal)}
	tr := &http.Transport{DialContext: dialer.DialContext}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	for _, host := range []string{"[::1]", "[::]", "[::%25lo]", "[::%25x]", "[::1%25lo]"} {
		res, err := client.Get(fmt.Sprintf("http://%s:%d/", host, port))
		if err == nil {
			body, _ := io.ReadAll(res.Body)
			_ = res.Body.Close()
			t.Errorf("%s reached the [::1] listener: %q", host, body)
			continue
		}
		var denied *DeniedError
		if !errors.As(err, &denied) {
			t.Logf("%s: refused before Control: %v", host, err)
		}
	}
}
