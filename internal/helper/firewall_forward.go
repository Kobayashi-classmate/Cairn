// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"encoding/xml"
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

// Port forwards. Each backend sends connections arriving on a port on to
// another port of this host or of another IPv4 host:
//
//   - nftables: NAT chains in Cairn's own table, plus an input rule that
//     accepts what a forward redirected to this host;
//   - ufw: the NAT chains in a table of their own (inet cairn_forward,
//     loaded at boot by cairn-forward.service), since ufw's before.rules
//     cannot hold NAT rules that survive a reload without duplicating
//     them, and a block in before.rules that lets the forwarded traffic
//     through ufw's own chains;
//   - firewalld: forward-port entries, in Cairn's policy for this host
//     and in a second policy (cairn-forward) for other hosts, since
//     firewalld only allows a target address and masquerading in a
//     policy whose traffic leaves the host.
//
// Forwards to other hosts need the kernel to forward IPv4 packets. Cairn
// switches that on while such a forward exists (keeping it on across
// reboots with a sysctl.d file) and off again only when it was Cairn
// that switched it on and nothing else on the host relies on it.

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

func (w fwForwarding) apply(ctx context.Context, log io.Writer, r fwRender) error {
	if err := w.fwBackend.apply(ctx, log, r); err != nil {
		return err
	}
	return w.f.ipForward(log, remoteForwards(r.Forwards))
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

// nftForwardNAT renders the NAT chains of the forwards: the redirection or
// translation of the destination, and masquerading of what goes on to
// another host (so its replies come back here).
func nftForwardNAT(forwards []helperapi.FirewallForward) string {
	if len(forwards) == 0 {
		return ""
	}
	var pre, post []string
	for _, fw := range forwards {
		for _, proto := range protocols(fw.Protocol) {
			match := proto + " dport " + strconv.Itoa(fw.Port)
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

// ufw -----------------------------------------------------------------------

const (
	ufwForwardFile  = "/etc/cairn-firewall/forward.nft"
	ufwForwardUnit  = "cairn-forward.service"
	ufwForwardTable = "cairn_forward"
	ufwForwardBegin = "# BEGIN Cairn port forwards (managed by Cairn; change them in the panel)"
	ufwForwardEnd   = "# END Cairn port forwards"
	ufwRequiredEnd  = "# End required lines"
)

func renderUFWForwardNAT(forwards []helperapi.FirewallForward) string {
	return "#!/usr/sbin/nft -f\n# Managed by Cairn. Change the port forwards in the panel instead of editing this file.\n" +
		"table inet " + ufwForwardTable + "\ndelete table inet " + ufwForwardTable + "\ntable inet " + ufwForwardTable + " {\n" +
		nftForwardNAT(forwards) + "}\n"
}

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

// setForwardNAT loads the NAT table of the forwards (and keeps it loaded
// at boot), or takes it away when there are none.
func (u *ufwBackend) setForwardNAT(ctx context.Context, log io.Writer, forwards []helperapi.FirewallForward) error {
	p := filepath.Join(u.root, ufwForwardFile)
	nft := filepath.Join(u.root, nftBinary)
	if len(forwards) == 0 {
		if !fileExists(p) {
			return nil
		}
		if systemdRunning() {
			_ = runCommand(ctx, log, "systemctl", "disable", ufwForwardUnit)
		}
		if fileExists(nft) {
			if _, err := queryCommand(ctx, nftBinary, "list", "table", "inet", ufwForwardTable); err == nil {
				if err := runCommand(ctx, log, nftBinary, "delete", "table", "inet", ufwForwardTable); err != nil {
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
	if err := writeFileMode(p, []byte(renderUFWForwardNAT(forwards)), 0o600); err != nil {
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
		return runCommand(ctx, log, "systemctl", "enable", ufwForwardUnit)
	}
	return nil
}

// firewalld -----------------------------------------------------------------

const firewalldForwardPolicyFile = "/etc/firewalld/policies/cairn-forward.xml"

type fwdForwardPort struct {
	Port     string `xml:"port,attr"`
	Protocol string `xml:"protocol,attr"`
	ToPort   string `xml:"to-port,attr"`
	ToAddr   string `xml:"to-addr,attr,omitempty"`
}

type fwdDestination struct {
	Address string `xml:"address,attr"`
}

type fwdForwardXML struct {
	XMLName      xml.Name           `xml:"policy"`
	Priority     int                `xml:"priority,attr"`
	Target       string             `xml:"target,attr"`
	Short        string             `xml:"short"`
	Description  string             `xml:"description"`
	Ingress      fwdZone            `xml:"ingress-zone"`
	Egress       fwdZone            `xml:"egress-zone"`
	ForwardPorts []fwdForwardPort   `xml:"forward-port"`
	Rules        []fwdMasqueradeXML `xml:"rule"`
}

type fwdMasqueradeXML struct {
	Family      string         `xml:"family,attr"`
	Destination fwdDestination `xml:"destination"`
	Masquerade  struct{}       `xml:"masquerade"`
}

// fwdLocalForwards renders the forwards to ports of this host, for Cairn's
// main policy.
func fwdLocalForwards(forwards []helperapi.FirewallForward) []fwdForwardPort {
	var out []fwdForwardPort
	for _, fw := range forwards {
		if fw.Remote() {
			continue
		}
		for _, proto := range protocols(fw.Protocol) {
			out = append(out, fwdForwardPort{Port: strconv.Itoa(fw.Port), Protocol: proto, ToPort: strconv.Itoa(fw.ToPort)})
		}
	}
	return out
}

// renderFirewalldForward renders the policy of the forwards to other
// hosts, or nil when there are none. Masquerading is per target address:
// firewalld does not take a port in the same rule.
func renderFirewalldForward(forwards []helperapi.FirewallForward) ([]byte, error) {
	pol := fwdForwardXML{Priority: -100, Target: "CONTINUE", Short: "Cairn port forwards",
		Description: "Managed by Cairn. Change the port forwards in the panel instead of editing this file.",
		Ingress:     fwdZone{"ANY"}, Egress: fwdZone{"ANY"}}
	seen := map[string]bool{}
	for _, fw := range forwards {
		if !fw.Remote() {
			continue
		}
		for _, proto := range protocols(fw.Protocol) {
			pol.ForwardPorts = append(pol.ForwardPorts, fwdForwardPort{Port: strconv.Itoa(fw.Port), Protocol: proto,
				ToPort: strconv.Itoa(fw.ToPort), ToAddr: fw.ToAddress})
		}
		if !seen[fw.ToAddress] {
			seen[fw.ToAddress] = true
			pol.Rules = append(pol.Rules, fwdMasqueradeXML{Family: "ipv4", Destination: fwdDestination{fw.ToAddress}})
		}
	}
	if len(pol.ForwardPorts) == 0 {
		return nil, nil
	}
	out, err := xml.MarshalIndent(pol, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), append(out, '\n')...), nil
}
