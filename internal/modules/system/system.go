// SPDX-License-Identifier: AGPL-3.0-only

// Package system serves the overview page: host information, live resource
// usage and the recent history; and the host's name and addresses alone,
// for the server card of every page.
package system

import (
	"net"
	"net/http"
	"net/netip"

	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/authz"
	"github.com/Kobayashi-classmate/Cairn/internal/metrics"
	"github.com/Kobayashi-classmate/Cairn/internal/module"
)

// Module implements module.Module.
type Module struct {
	Metrics *metrics.Collector
}

// ID implements module.Module.
func (m *Module) ID() string { return "system" }

// Info implements module.Describer.
func (m *Module) Info() module.Info {
	return module.Info{Nav: []module.NavItem{
		{Key: "overview", LabelKey: "common.nav.overview", Icon: "speedometer", Route: "overview", Permission: authz.SystemRead, Order: 10},
	}}
}

// Routes implements module.Module.
func (m *Module) Routes(r *api.Router) {
	r.Handle("GET /api/v1/system/overview", func(w http.ResponseWriter, _ *http.Request) error {
		return api.WriteJSON(w, http.StatusOK, m.Metrics.Overview())
	}, api.Require(authz.SystemRead), api.Doc("system", "Host information, live usage and recent history"), api.Returns(200, metrics.Overview{}))
	r.Handle("GET /api/v1/system/host", func(w http.ResponseWriter, _ *http.Request) error {
		h := m.Metrics.Host()
		return api.WriteJSON(w, http.StatusOK, HostView{Hostname: h.Hostname, OS: h.OS, Addresses: primaryAddresses()})
	}, api.Require(authz.SystemRead), api.Doc("system", "Host name, operating system and the addresses this server reaches the internet from"),
		api.Returns(200, HostView{}))
}

// HostView is the server as the sidebar shows it.
type HostView struct {
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	// Addresses are the source addresses of the default IPv4 and IPv6
	// routes, IPv4 first; empty without such routes. Behind NAT the public
	// address differs and is not known here.
	Addresses []string `json:"addresses"`
}

// routeProbes are public addresses whose routes give the primary source
// addresses. Connecting a UDP socket only looks up the route: nothing is
// sent.
var routeProbes = []string{"192.0.2.1:9", "[2001:db8::1]:9"}

func primaryAddresses() []string {
	out := []string{}
	for _, probe := range routeProbes {
		c, err := net.Dial("udp", probe)
		if err != nil {
			continue
		}
		a, ok := c.LocalAddr().(*net.UDPAddr)
		_ = c.Close()
		if !ok {
			continue
		}
		ip, ok := netip.AddrFromSlice(a.IP)
		if !ok {
			continue
		}
		ip = ip.Unmap()
		if ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() {
			continue
		}
		out = append(out, ip.String())
	}
	return out
}
