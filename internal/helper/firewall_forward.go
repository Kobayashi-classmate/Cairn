// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// Port forwards. Connections arriving on a port of this host go on to
// another port of this host or of another IPv4 host. Every backend uses the
// same NAT chains of Cairn's own (nftForwardNAT):
//
//   - nftables: in Cairn's table, plus an input rule that accepts what a
//     forward redirected to this host;
//   - ufw: in a table of their own (inet cairn_forward, loaded at boot by
//     cairn-forward.service), since ufw's before.rules cannot hold NAT
//     rules that survive a reload without duplicating them, plus a block
//     in before.rules that lets the forwarded traffic through ufw's chains;
//   - firewalld: in that same table. firewalld's own forward-port entries
//     translate routed traffic as well, and firewalld accepts translated
//     connections before any zone or policy, blocked addresses included;
//     it does let through what another table translated.
//
// Only connections addressed to this host are forwarded: what containers
// send out and what the host routes keeps its destination. Connections
// from blocked addresses, and from the sources of a deny rule for the
// port (or, for a forward to this host, for its target port, which they
// could not reach directly), are not forwarded either: they meet the
// firewall's rules for the port they were sent to.
//
// Forwards to other hosts need the kernel to forward IPv4 packets. Cairn
// switches that on while such a forward exists (keeping it on across
// reboots with a sysctl.d file) and off again only when it was Cairn
// that switched it on and nothing else on the host relies on it. While
// it is on because of Cairn, the nftables backend drops what else the host
// would route (nftForwardFilter); ufw and firewalld drop it by default.

const (
	ipForwardProc = "/proc/sys/net/ipv4/ip_forward"
	ipForwardConf = "/etc/sysctl.d/60-cairn-forward.conf"
	// ipForwardMark records that Cairn switched forwarding on.
	ipForwardMark = "ip-forward-on"
)

// Changes -------------------------------------------------------------------

func (f *Firewall) setForward(ctx context.Context, c helperapi.Caller, q helperapi.FirewallForward) (helperapi.FirewallChange, error) {
	var saved helperapi.FirewallForward
	kind := "forward.add"
	if q.ID != "" {
		kind = "forward.update"
	}
	ch, err := f.change(ctx, c, kind, func(cfg *fwConfig) error {
		if q.ID == "" {
			q.ID = newHexID("f")
			cfg.Forwards = append(cfg.Forwards, q)
			saved = q
			return nil
		}
		i := slices.IndexFunc(cfg.Forwards, func(x helperapi.FirewallForward) bool { return x.ID == q.ID })
		if i < 0 {
			return fwErr(helperapi.CodeFirewallNotFound, map[string]any{"id": q.ID})
		}
		cfg.Forwards[i] = q
		saved = q
		return nil
	})
	if err != nil {
		return ch, err
	}
	ch.Forward = &saved
	return ch, nil
}

func (f *Firewall) deleteForward(ctx context.Context, c helperapi.Caller, q helperapi.FirewallRef) (helperapi.FirewallChange, error) {
	return f.change(ctx, c, "forward.delete", func(cfg *fwConfig) error {
		i := slices.IndexFunc(cfg.Forwards, func(x helperapi.FirewallForward) bool { return x.ID == q.ID })
		if i < 0 {
			return fwErr(helperapi.CodeFirewallNotFound, map[string]any{"id": q.ID})
		}
		cfg.Forwards = slices.Delete(cfg.Forwards, i, i+1)
		return nil
	})
}

// sharesProtocol reports whether two protocol choices overlap.
func sharesProtocol(a, b string) bool {
	return a == b || a == helperapi.ProtoBoth || b == helperapi.ProtoBoth
}

// checkForwards refuses forwards that would take the SSH or panel port
// away from sshd or the panel, and two forwards on the same port.
func checkForwards(forwards []helperapi.FirewallForward, keep []int) error {
	if len(forwards) > helperapi.FirewallMaxForwards {
		return fwErr("validation.invalid", map[string]any{"field": "forwards", "rule": "count"})
	}
	for i, fw := range forwards {
		if fw.Protocol != helperapi.ProtoUDP && slices.Contains(keep, fw.Port) {
			return fwErr(helperapi.CodeFirewallProtected, map[string]any{"port": fw.Port})
		}
		for _, other := range forwards[:i] {
			if other.Port == fw.Port && sharesProtocol(other.Protocol, fw.Protocol) {
				return fwErr(helperapi.CodeFirewallForwardTaken, map[string]any{"port": fw.Port})
			}
		}
	}
	return nil
}

func remoteForwards(forwards []helperapi.FirewallForward) bool {
	return slices.ContainsFunc(forwards, helperapi.FirewallForward.Remote)
}

// IP forwarding --------------------------------------------------------------

// fwForwarding wraps a backend: once its rules are in place (or gone), the
// kernel's IPv4 forwarding follows the forwards to other hosts.
type fwForwarding struct {
	fwBackend
	f *Firewall
}

// withForwarding completes a render with who owns IPv4 forwarding. It is
// known before the rules go in: forwarding that Cairn is about to switch
// on is filtered from the first packet.
func (w fwForwarding) withForwarding(r fwRender) fwRender {
	r.OwnForwarding = remoteForwards(r.Forwards) && w.f.ownsIPForward()
	return r
}

func (w fwForwarding) apply(ctx context.Context, log io.Writer, r fwRender) error {
	if err := w.fwBackend.apply(ctx, log, w.withForwarding(r)); err != nil {
		return err
	}
	return w.f.ipForward(log, remoteForwards(r.Forwards))
}

// current asks the backend whether its rules are r's; one that cannot tell
// counts as current, and is left alone.
func (w fwForwarding) current(ctx context.Context, r fwRender) bool {
	c, ok := w.fwBackend.(fwCurrent)
	return !ok || c.current(ctx, w.withForwarding(r))
}

func (w fwForwarding) disable(ctx context.Context, log io.Writer) error {
	if err := w.fwBackend.disable(ctx, log); err != nil {
		return err
	}
	return w.f.ipForward(log, false)
}

// ipForwardOn reads whether the kernel forwards IPv4 packets.
func (f *Firewall) ipForwardOn() bool {
	data, err := os.ReadFile(filepath.Join(f.Root, ipForwardProc)) //nolint:gosec // G304: a fixed path.
	return err == nil && strings.TrimSpace(string(data)) == "1"
}

// ownsIPForward reports whether IPv4 forwarding is on because of Cairn, or
// off so that Cairn would switch it on. Forwarding that something else
// switched on is left as it is, with whatever filters that brought.
func (f *Firewall) ownsIPForward() bool {
	return fileExists(f.stateDir(ipForwardMark)) || !f.ipForwardOn()
}

// othersForward reports whether something else on the host routes packets
// and would break without forwarding (container and VM bridges).
func (f *Firewall) othersForward() bool {
	for _, p := range []string{"/sys/class/net/docker0", "/sys/class/net/podman0", "/sys/class/net/virbr0", "/sys/class/net/lxdbr0", "/sys/class/net/cni0"} {
		if fileExists(filepath.Join(f.Root, p)) {
			return true
		}
	}
	return false
}

// ipForward switches IPv4 forwarding on when need is true, and back off
// when it is not needed any more and Cairn was the one that switched it on.
func (f *Firewall) ipForward(log io.Writer, need bool) error {
	conf := filepath.Join(f.Root, ipForwardConf)
	proc := filepath.Join(f.Root, ipForwardProc)
	mark := f.stateDir(ipForwardMark)
	if need {
		if !fileExists(conf) {
			if err := os.MkdirAll(filepath.Dir(conf), 0o755); err != nil { //nolint:gosec // G301: /etc/sysctl.d is world-readable.
				return err
			}
			body := "# Managed by Cairn: port forwards to other hosts need IPv4 forwarding.\nnet.ipv4.ip_forward = 1\n"
			if err := writeFileMode(conf, []byte(body), 0o644); err != nil {
				return err
			}
		}
		if f.ipForwardOn() {
			return nil
		}
		if err := os.WriteFile(proc, []byte("1\n"), 0o644); err != nil { //nolint:gosec // G306: a kernel setting.
			return fmt.Errorf("switching on IPv4 forwarding: %w", err)
		}
		_, _ = fmt.Fprintln(log, "IPv4 forwarding switched on")
		return writeFileMode(mark, nil, 0o600)
	}
	if err := os.Remove(conf); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if !fileExists(mark) {
		return nil
	}
	if f.ipForwardOn() && !f.othersForward() {
		if err := os.WriteFile(proc, []byte("0\n"), 0o644); err != nil { //nolint:gosec // G306: a kernel setting.
			return fmt.Errorf("switching off IPv4 forwarding: %w", err)
		}
		_, _ = fmt.Fprintln(log, "IPv4 forwarding switched off")
	}
	return os.Remove(mark)
}

// nftables ------------------------------------------------------------------

func forwardComment(fw helperapi.FirewallForward) string {
	return ` comment "cairn:` + fw.ID + `"`
}

// portsCover reports whether a rule's ports include port.
func portsCover(ports []string, port int) bool {
	for _, p := range ports {
		if lo, hi, ok := helperapi.ParsePortRange(p); ok && lo <= port && port <= hi {
			return true
		}
	}
	return false
}

// nftForwardDenied renders, for each deny rule, the forwards its sources
// may not use: those on a port the rule denies and, for a forward to this
// host, those whose target port it denies. Their connections are left
// untranslated (return), so they meet the rules of the port they were
// sent to.
func nftForwardDenied(r fwRender) []string {
	var lines []string
	for _, rule := range r.Rules {
		if rule.Action != helperapi.FirewallDeny {
			continue
		}
		for _, proto := range protocols(rule.Protocol) {
			var ports []string
			for _, fw := range r.Forwards {
				if !slices.Contains(protocols(fw.Protocol), proto) {
					continue
				}
				if portsCover(rule.Ports, fw.Port) || (!fw.Remote() && portsCover(rule.Ports, fw.ToPort)) {
					ports = append(ports, strconv.Itoa(fw.Port))
				}
			}
			if len(ports) == 0 {
				continue
			}
			match := proto + " dport { " + strings.Join(ports, ", ") + " } return" + ` comment "cairn:` + rule.ID + `"`
			if len(rule.Sources) == 0 {
				lines = append(lines, match)
				continue
			}
			v4, v6 := splitSources(rule.Sources)
			if len(v4) > 0 {
				lines = append(lines, "ip saddr { "+strings.Join(v4, ", ")+" } "+match)
			}
			if len(v6) > 0 {
				lines = append(lines, "ip6 saddr { "+strings.Join(v6, ", ")+" } "+match)
			}
		}
	}
	return lines
}

// nftForwardNAT renders the NAT chains of the forwards: the redirection or
// translation of the destination, and masquerading of what goes on to
// another host (so its replies come back here). The table holding them
// has the blocked addresses in the sets deny4 and deny6. A return leaves a
// connection untranslated; other tables' NAT chains still see it.
func nftForwardNAT(r fwRender) string {
	if len(r.Forwards) == 0 {
		return ""
	}
	pre := append([]string{"ip saddr @deny4 return", "ip6 saddr @deny6 return"}, nftForwardDenied(r)...)
	var post []string
	for _, fw := range r.Forwards {
		for _, proto := range protocols(fw.Protocol) {
			// Addressed to this host: what it routes (a container's
			// connection to another host on this port) keeps its destination.
			match := "fib daddr type local " + proto + " dport " + strconv.Itoa(fw.Port)
			if !fw.Remote() {
				// Both address families: the connection stays on this host.
				pre = append(pre, match+" redirect to :"+strconv.Itoa(fw.ToPort)+forwardComment(fw))
				continue
			}
			pre = append(pre, "meta nfproto ipv4 "+match+" dnat ip to "+fw.ToAddress+":"+strconv.Itoa(fw.ToPort)+forwardComment(fw))
			post = append(post, "ip daddr "+fw.ToAddress+" "+proto+" dport "+strconv.Itoa(fw.ToPort)+" ct status dnat masquerade"+forwardComment(fw))
		}
	}
	var b strings.Builder
	b.WriteString("\tchain forward_prerouting {\n\t\ttype nat hook prerouting priority dstnat; policy accept;\n")
	for _, l := range pre {
		b.WriteString("\t\t" + l + "\n")
	}
	b.WriteString("\t}\n")
	if len(post) > 0 {
		b.WriteString("\tchain forward_postrouting {\n\t\ttype nat hook postrouting priority srcnat; policy accept;\n")
		for _, l := range post {
			b.WriteString("\t\t" + l + "\n")
		}
		b.WriteString("\t}\n")
	}
	return b.String()
}

// fwVirtualKinds are the kinds of interface whose traffic the forward
// chain lets through: the bridges, veth pairs and overlays that container
// and VM networks route through (Docker, Podman, libvirt, LXC, Kubernetes).
// They filter their own traffic, and may have been set up after Cairn
// switched forwarding on, relying on it. VPN tunnels (tun, wireguard) are
// not among them: the networks behind one stay out of reach of the hosts
// next to this one.
var fwVirtualKinds = []string{"bridge", "veth", "vxlan", "geneve", "openvswitch"}

// nftForwardFilter renders the forward chain while IPv4 forwarding is on
// because of Cairn: the forwarded connections and their replies pass, and
// so does the traffic of container and VM networks; any other IPv4 packet
// the host would route is dropped, so that forwarding switched on for the
// port forwards does not make the host a router for its neighbors.
//
// The forward chains of other tables (Docker's iptables rules, say) still
// see what this one accepts, and a drop here is final for them as well:
// hence no drop policy, and nothing dropped but IPv4 between interfaces of
// other kinds.
func nftForwardFilter(r fwRender) string {
	if !r.OwnForwarding {
		return ""
	}
	kinds := `{ "` + strings.Join(fwVirtualKinds, `", "`) + `" }`
	return "\tchain forward {\n\t\ttype filter hook forward priority filter - 10; policy accept;\n" +
		"\t\tct state established,related accept\n" +
		"\t\tct status dnat accept\n" +
		"\t\tmeta iifkind " + kinds + " accept\n" +
		"\t\tmeta oifkind " + kinds + " accept\n" +
		"\t\tmeta nfproto ipv4 drop\n" +
		"\t}\n"
}

// nftForwardInput lets in what a forward redirected to this host: after
// the redirection the packet's port is the target port, which stays
// closed to direct connections.
func nftForwardInput(forwards []helperapi.FirewallForward) []string {
	var lines []string
	for _, fw := range forwards {
		if fw.Remote() {
			continue
		}
		for _, proto := range protocols(fw.Protocol) {
			lines = append(lines, "ct status dnat "+proto+" dport "+strconv.Itoa(fw.ToPort)+" accept"+forwardComment(fw))
		}
	}
	return lines
}

// The NAT table of ufw and firewalld -----------------------------------------

const (
	forwardTableFile = "/etc/cairn-firewall/forward.nft"
	forwardTableUnit = "cairn-forward.service"
	forwardTable     = "cairn_forward"
)

// renderForwardTable renders the table of the forwards for ufw and
// firewalld, with its own copy of the blocked addresses.
func renderForwardTable(r fwRender) string {
	_, _, deny4, deny6 := addressSets(r.Addresses)
	return "#!/usr/sbin/nft -f\n# Managed by Cairn. Change the port forwards in the panel instead of editing this file.\n" +
		"table inet " + forwardTable + "\ndelete table inet " + forwardTable + "\ntable inet " + forwardTable + " {\n" +
		nftSet("deny4", "ipv4_addr", deny4) + nftSet("deny6", "ipv6_addr", deny6) +
		nftForwardNAT(r) + "}\n"
}

// forwardTableCurrent reports whether the file of the forwards' table holds
// what r renders to (no file without forwards).
func forwardTableCurrent(root string, r fwRender) bool {
	data, err := os.ReadFile(filepath.Join(root, forwardTableFile)) //nolint:gosec // G304: a fixed path.
	if len(r.Forwards) == 0 {
		return errors.Is(err, fs.ErrNotExist)
	}
	return err == nil && string(data) == renderForwardTable(r)
}

// setForwardTable loads the NAT table of the forwards (and keeps it loaded
// at boot), or takes it away when there are none.
func setForwardTable(ctx context.Context, log io.Writer, root string, r fwRender) error {
	p := filepath.Join(root, forwardTableFile)
	nft := filepath.Join(root, nftBinary)
	if len(r.Forwards) == 0 {
		if !fileExists(p) {
			return nil
		}
		if systemdRunning() {
			_ = runCommand(ctx, log, "systemctl", "disable", forwardTableUnit)
		}
		if fileExists(nft) {
			if _, err := queryCommand(ctx, nftBinary, "list", "table", "inet", forwardTable); err == nil {
				if err := runCommand(ctx, log, nftBinary, "delete", "table", "inet", forwardTable); err != nil {
					return err
				}
			}
		}
		return os.Remove(p)
	}
	if !fileExists(nft) {
		return fwErr(helperapi.CodeFirewallUnsupported, map[string]any{"reason": "nft_missing"})
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	old, oldErr := os.ReadFile(p) //nolint:gosec // G304: a fixed path.
	if err := writeFileMode(p, []byte(renderForwardTable(r)), 0o600); err != nil {
		return err
	}
	put := func() {
		if oldErr == nil {
			_ = writeFileMode(p, old, 0o600)
		} else {
			_ = os.Remove(p)
		}
	}
	if err := runCommand(ctx, log, nftBinary, "-c", "-f", p); err != nil {
		put()
		return err
	}
	if err := runCommand(ctx, log, nftBinary, "-f", p); err != nil {
		put()
		return err
	}
	if systemdRunning() {
		return runCommand(ctx, log, "systemctl", "enable", forwardTableUnit)
	}
	return nil
}

// ufw -----------------------------------------------------------------------

const (
	ufwForwardBegin = "# BEGIN Cairn port forwards (managed by Cairn; change them in the panel)"
	ufwForwardEnd   = "# END Cairn port forwards"
	ufwRequiredEnd  = "# End required lines"
)

// ufwForwardLines renders the before.rules (v6 false) or before6.rules
// lines that accept forwarded traffic in ufw's chains.
func ufwForwardLines(forwards []helperapi.FirewallForward, v6 bool) []string {
	chain := "ufw-before-input"
	if v6 {
		chain = "ufw6-before-input"
	}
	var lines []string
	for _, fw := range forwards {
		for _, proto := range protocols(fw.Protocol) {
			switch {
			case !fw.Remote():
				lines = append(lines, fmt.Sprintf("-A %s -p %s --dport %d -m conntrack --ctstate DNAT -j ACCEPT", chain, proto, fw.ToPort))
			case !v6:
				lines = append(lines, fmt.Sprintf("-A ufw-before-forward -p %s -d %s --dport %d -m conntrack --ctstate DNAT -j ACCEPT",
					proto, fw.ToAddress, fw.ToPort))
			}
		}
	}
	return lines
}

// withUFWForwardBlock puts Cairn's block of lines into a before.rules file
// right after ufw's required chain declarations (or takes it out when
// there are no lines).
func withUFWForwardBlock(data []byte, lines []string) ([]byte, error) {
	text := string(data)
	if i := strings.Index(text, ufwForwardBegin+"\n"); i >= 0 {
		j := strings.Index(text[i:], ufwForwardEnd+"\n")
		if j < 0 {
			return nil, fwErr(helperapi.CodeFirewallUnsupported, map[string]any{"reason": "ufw_before_rules"})
		}
		text = text[:i] + text[i+j+len(ufwForwardEnd)+1:]
	}
	if len(lines) == 0 {
		return []byte(text), nil
	}
	at := strings.Index(text, ufwRequiredEnd+"\n")
	if at < 0 {
		return nil, fwErr(helperapi.CodeFirewallUnsupported, map[string]any{"reason": "ufw_before_rules"})
	}
	at += len(ufwRequiredEnd) + 1
	block := ufwForwardBegin + "\n" + strings.Join(lines, "\n") + "\n" + ufwForwardEnd + "\n"
	return []byte(text[:at] + block + text[at:]), nil
}

// setForwardRules updates the blocks in before.rules and before6.rules;
// it reports whether a file changed.
func (u *ufwBackend) setForwardRules(forwards []helperapi.FirewallForward) (bool, error) {
	changed := false
	for _, file := range []struct {
		name string
		v6   bool
	}{{"before.rules", false}, {"before6.rules", true}} {
		p := filepath.Join(u.root, "/etc/ufw", file.name)
		data, err := os.ReadFile(p) //nolint:gosec // G304: fixed names.
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return changed, err
		}
		out, err := withUFWForwardBlock(data, ufwForwardLines(forwards, file.v6))
		if err != nil {
			return changed, err
		}
		if string(out) == string(data) {
			continue
		}
		info, err := os.Stat(p)
		if err != nil {
			return changed, err
		}
		if err := writeFileMode(p, out, info.Mode().Perm()); err != nil {
			return changed, err
		}
		changed = true
	}
	return changed, nil
}

// firewalld -----------------------------------------------------------------

// firewalldForwardPolicyFile held the forwards to other hosts while
// firewalld did the forwarding; an apply takes it away.
const firewalldForwardPolicyFile = "/etc/firewalld/policies/cairn-forward.xml"
