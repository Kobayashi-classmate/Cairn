// SPDX-License-Identifier: AGPL-3.0-only

// Package netx classifies network addresses for connections the panel
// makes, or configures, on a user's behalf: a URL to download, an object
// store to test, a proxy upstream. It guards against server-side request
// forgery: a request that reaches this host's own services, the internal
// network or a cloud metadata service instead of the place it claims.
package netx

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"syscall"
	"time"
)

// Resolver looks up host names; *net.Resolver implements it.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Ranges that netip has no predicate for.
var (
	thisNetwork  = netip.MustParsePrefix("0.0.0.0/8")
	sharedCGNAT  = netip.MustParsePrefix("100.64.0.0/10") // includes 100.100.100.200 (Alibaba Cloud metadata)
	ietfProtocol = netip.MustParsePrefix("192.0.0.0/24")
	testNet1     = netip.MustParsePrefix("192.0.2.0/24")
	benchmarking = netip.MustParsePrefix("198.18.0.0/15")
	testNet2     = netip.MustParsePrefix("198.51.100.0/24")
	testNet3     = netip.MustParsePrefix("203.0.113.0/24")
	reserved4    = netip.MustParsePrefix("240.0.0.0/4") // includes 255.255.255.255
	discard6     = netip.MustParsePrefix("100::/64")
	doc6         = netip.MustParsePrefix("2001:db8::/32")
	siteLocal6   = netip.MustParsePrefix("fec0::/10")
	nat64        = netip.MustParsePrefix("64:ff9b::/96")
	nat64Local   = netip.MustParsePrefix("64:ff9b:1::/48")
	sixToFour    = netip.MustParsePrefix("2002::/16")

	// Metadata services reachable outside the link-local ranges.
	alibabaMetadata = netip.MustParseAddr("100.100.100.200")
	awsMetadata6    = netip.MustParseAddr("fd00:ec2::254")
)

// embedded returns the IPv4 address an IPv6 transition address carries
// (NAT64, 6to4), which is where a connection to it really goes.
func embedded(ip netip.Addr) (netip.Addr, bool) {
	b := ip.As16()
	switch {
	case nat64.Contains(ip):
		return netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}), true
	case sixToFour.Contains(ip):
		return netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]}), true
	}
	return netip.Addr{}, false
}

// plain is ip as the guards compare it: an IPv4-mapped address as IPv4,
// and without an IPv6 zone. A zone only names the interface ("[::%eth0]"):
// the address is the same, and Linux connects "::" with any zone to "::1",
// while netip's predicates and prefixes take an address with a zone for
// another one.
func plain(ip netip.Addr) netip.Addr {
	return ip.Unmap().WithZone("")
}

// IsLocal reports whether ip reaches this host itself: a loopback address,
// or the unspecified address (0.0.0.0, ::), which connects to this host too.
func IsLocal(ip netip.Addr) bool {
	ip = plain(ip)
	if v4, ok := embedded(ip); ok {
		ip = v4
	}
	return ip.IsLoopback() || ip.IsUnspecified() || thisNetwork.Contains(ip)
}

// IsHost reports whether ip reaches this host: IsLocal, or one of own, the
// addresses of its network interfaces (HostAddrs), also inside an IPv6
// transition address.
func IsHost(ip netip.Addr, own []netip.Addr) bool {
	if IsLocal(ip) {
		return true
	}
	ip = ip.Unmap().WithZone("")
	candidates := []netip.Addr{ip}
	if v4, ok := embedded(ip); ok {
		candidates = append(candidates, v4)
	}
	for _, a := range own {
		a = a.Unmap().WithZone("")
		for _, c := range candidates {
			if a == c {
				return true
			}
		}
	}
	return false
}

// HostAddrs returns the addresses of this host's network interfaces. They
// change (DHCP, an address added later): a check against them holds for
// the moment it is made.
func HostAddrs() ([]netip.Addr, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	var out []netip.Addr
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if addr, ok := netip.AddrFromSlice(ip); ok {
			out = append(out, addr.Unmap())
		}
	}
	return out, nil
}

// IsMetadata reports whether ip is link-local (where most cloud metadata
// services answer, 169.254.169.254 among them) or a well-known metadata
// address outside those ranges.
func IsMetadata(ip netip.Addr) bool {
	ip = plain(ip)
	if v4, ok := embedded(ip); ok {
		ip = v4
	}
	return ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip == alibabaMetadata || ip == awsMetadata6
}

// IsPublic reports whether ip is a public unicast address. Everything else
// is refused for downloads from URLs: loopback, private (RFC 1918), shared
// (CGNAT, 100.64.0.0/10), link-local, unique-local (fc00::/7),
// unspecified, multicast, and the documentation and reserved ranges.
func IsPublic(ip netip.Addr) bool {
	ip = plain(ip)
	if ip.Is6() {
		if nat64Local.Contains(ip) {
			return false
		}
		if v4, ok := embedded(ip); ok {
			ip = v4
		}
	}
	if !ip.IsValid() || IsLocal(ip) || IsMetadata(ip) || ip.IsPrivate() || ip.IsMulticast() || ip.IsInterfaceLocalMulticast() {
		return false
	}
	for _, p := range []netip.Prefix{sharedCGNAT, ietfProtocol, testNet1, benchmarking, testNet2, testNet3, reserved4, discard6, doc6, siteLocal6} {
		if p.Contains(ip) {
			return false
		}
	}
	return ip.IsGlobalUnicast()
}

// DeniedError reports an address a guard refused.
type DeniedError struct {
	Addr netip.Addr
}

func (e *DeniedError) Error() string {
	return fmt.Sprintf("netx: connecting to %s is not allowed", e.Addr)
}

// DialControl returns a net.Dialer Control function refusing connections to
// addresses deny reports. It sees the address after name resolution, for
// every connection (redirects and retries included), so neither a DNS name
// nor a redirect can lead around it. The address is judged without its
// zone, which the dialer keeps even on an IPv4 address it unmapped
// ("127.0.0.1%lo:80" for "[::ffff:127.0.0.1%lo]").
func DialControl(deny func(netip.Addr) bool) func(network, address string, c syscall.RawConn) error {
	return func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return fmt.Errorf("netx: unexpected dial address %q: %w", address, err)
		}
		host, _, _ = strings.Cut(host, "%")
		addr, err := netip.ParseAddr(host)
		if err != nil {
			return fmt.Errorf("netx: unexpected dial address %q: %w", address, err)
		}
		if ip := plain(addr); deny(ip) {
			return &DeniedError{Addr: ip}
		}
		return nil
	}
}

// resolveTimeout bounds lookups made while answering a request.
const resolveTimeout = 5 * time.Second

// Resolve returns the addresses of host: an IP literal as it is, "localhost"
// and names below it as the loopback addresses (resolvers answer them so),
// other names through r (nil: the system resolver).
func Resolve(ctx context.Context, r Resolver, host string) ([]netip.Addr, error) {
	host = strings.TrimSuffix(strings.Trim(host, "[]"), ".")
	if ip, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{ip.Unmap()}, nil
	}
	if h := strings.ToLower(host); h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return []netip.Addr{netip.IPv6Loopback(), netip.AddrFrom4([4]byte{127, 0, 0, 1})}, nil
	}
	if r == nil {
		r = net.DefaultResolver
	}
	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	ips, err := r.LookupNetIP(ctx, "ip", host)
	for i := range ips {
		ips[i] = ips[i].Unmap()
	}
	return ips, err
}

// Check resolves host and returns the first of its addresses deny refuses,
// if any. A name that does not resolve is not refused: whoever connects to
// it later fails to resolve it as well.
func Check(ctx context.Context, r Resolver, host string, deny func(netip.Addr) bool) (netip.Addr, bool) {
	ips, _ := Resolve(ctx, r, host)
	for _, ip := range ips {
		if deny(ip) {
			return ip, true
		}
	}
	return netip.Addr{}, false
}

// LocalOrMetadata reports whether ip is this host or a metadata service:
// what an object store or similar endpoint may not be, while private
// networks stay allowed (a MinIO on the LAN).
func LocalOrMetadata(ip netip.Addr) bool { return IsLocal(ip) || IsMetadata(ip) }

// Internal reports whether ip is anything but a public address.
func Internal(ip netip.Addr) bool { return !IsPublic(ip) }
