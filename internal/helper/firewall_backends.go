// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// splitSources groups sources by address family; no sources means
// anywhere (one group with nil).
func splitSources(sources []string) (v4, v6 []string) {
	for _, s := range sources {
		p, ok := helperapi.ParseSource(s)
		if !ok {
			continue
		}
		if p.Addr().Is4() {
			v4 = append(v4, helperapi.CanonicalSource(p))
		} else {
			v6 = append(v6, helperapi.CanonicalSource(p))
		}
	}
	return v4, v6
}

func protocols(p string) []string {
	if p == helperapi.ProtoBoth {
		return []string{helperapi.ProtoTCP, helperapi.ProtoUDP}
	}
	return []string{p}
}

func fileExists(p string) bool {
	_, err := os.Stat(p) //nolint:gosec // G703: callers pass fixed system paths (under a test prefix).
	return err == nil
}

// nftables ------------------------------------------------------------------

const (
	nftRulesFile = "/etc/cairn-firewall/cairn.nft"
	nftUnit      = "cairn-firewall.service"
	nftBinary    = "/usr/sbin/nft"
)

// nftBackend keeps every rule in a table of Cairn's own (inet cairn),
// replaced atomically and loaded at boot by cairn-firewall.service.
type nftBackend struct{ root string }

func (n *nftBackend) installed() bool    { return fileExists(filepath.Join(n.root, nftBinary)) }
func (n *nftBackend) packages() []string { return []string{"nftables"} }

func (n *nftBackend) active(ctx context.Context) bool { return n.loaded(ctx) }

func (n *nftBackend) loaded(ctx context.Context) bool {
	if !n.installed() {
		return false
	}
	_, err := queryCommand(ctx, nftBinary, "list", "table", "inet", "cairn")
	return err == nil
}

func (n *nftBackend) external(context.Context) []string { return nil }

func nftSet(name, typ string, elems []string) string {
	s := "\tset " + name + " {\n\t\ttype " + typ + "\n\t\tflags interval\n\t\tauto-merge\n"
	if len(elems) > 0 {
		s += "\t\telements = { " + strings.Join(elems, ", ") + " }\n"
	}
	return s + "\t}\n"
}

func nftPorts(ports []string) string {
	out := make([]string, len(ports))
	for i, p := range ports {
		lo, hi, _ := helperapi.ParsePortRange(p)
		out[i] = strconv.Itoa(lo)
		if hi != lo {
			out[i] += "-" + strconv.Itoa(hi)
		}
	}
	return "{ " + strings.Join(out, ", ") + " }"
}

// nftRuleLines renders one port rule: a line per address family.
func nftRuleLines(r helperapi.FirewallRule) []string {
	verdict := "accept"
	if r.Action == helperapi.FirewallDeny {
		verdict = "drop"
	}
	match := "tcp dport " + nftPorts(r.Ports)
	switch r.Protocol {
	case helperapi.ProtoUDP:
		match = "udp dport " + nftPorts(r.Ports)
	case helperapi.ProtoBoth:
		// Protocol numbers, not names: names need /etc/protocols, which
		// minimal systems may lack (6 = tcp, 17 = udp).
		match = "meta l4proto { 6, 17 } th dport " + nftPorts(r.Ports)
	}
	comment := ` comment "cairn:` + r.ID + `"`
	if len(r.Sources) == 0 {
		return []string{match + " " + verdict + comment}
	}
	v4, v6 := splitSources(r.Sources)
	var lines []string
	if len(v4) > 0 {
		lines = append(lines, "ip saddr { "+strings.Join(v4, ", ")+" } "+match+" "+verdict+comment)
	}
	if len(v6) > 0 {
		lines = append(lines, "ip6 saddr { "+strings.Join(v6, ", ")+" } "+match+" "+verdict+comment)
	}
	return lines
}

// renderNft writes the table. Order: loopback and established traffic,
// blocked addresses, trusted addresses, deny rules, ICMP (needed for IPv6
// neighbor discovery and path MTU), DHCPv6 replies, allow rules; anything
// else is dropped.
func renderNft(r fwRender) string {
	var allow4, allow6, deny4, deny6 []string
	for _, a := range r.Addresses {
		v4, v6 := splitSources([]string{a.Address})
		if a.Action == helperapi.FirewallAllow {
			allow4, allow6 = append(allow4, v4...), append(allow6, v6...)
		} else {
			deny4, deny6 = append(deny4, v4...), append(deny6, v6...)
		}
	}
	var b strings.Builder
	b.WriteString("#!/usr/sbin/nft -f\n# Managed by Cairn. Change the firewall in the panel instead of editing this file.\n")
	b.WriteString("table inet cairn\ndelete table inet cairn\ntable inet cairn {\n")
	b.WriteString(nftSet("allow4", "ipv4_addr", allow4))
	b.WriteString(nftSet("allow6", "ipv6_addr", allow6))
	b.WriteString(nftSet("deny4", "ipv4_addr", deny4))
	b.WriteString(nftSet("deny6", "ipv6_addr", deny6))
	b.WriteString("\tchain input {\n\t\ttype filter hook input priority filter - 10; policy drop;\n")
	lines := []string{
		`iifname "lo" accept`,
		"ct state established,related accept",
		"ct state invalid drop",
		"ip saddr @deny4 drop",
		"ip6 saddr @deny6 drop",
		"ip saddr @allow4 accept",
		"ip6 saddr @allow6 accept",
	}
	for _, rule := range r.Rules {
		if rule.Action == helperapi.FirewallDeny {
			lines = append(lines, nftRuleLines(rule)...)
		}
	}
	if r.BlockPing {
		lines = append(lines, "icmp type echo-request drop", "icmpv6 type echo-request drop")
	}
	// 1 = icmp, 58 = ipv6-icmp (numbers: see nftRuleLines).
	lines = append(lines, "meta l4proto 1 accept", "meta l4proto 58 accept", "ip6 saddr fe80::/10 udp dport 546 accept")
	for _, rule := range r.Rules {
		if rule.Action == helperapi.FirewallAllow {
			lines = append(lines, nftRuleLines(rule)...)
		}
	}
	for _, l := range lines {
		b.WriteString("\t\t" + l + "\n")
	}
	b.WriteString("\t}\n}\n")
	return b.String()
}

func (n *nftBackend) apply(ctx context.Context, log io.Writer, r fwRender) error {
	p := filepath.Join(n.root, nftRulesFile)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	old, oldErr := os.ReadFile(p) //nolint:gosec // G304: a fixed path.
	if err := writeFileMode(p, []byte(renderNft(r)), 0o600); err != nil {
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
	if systemdRunning() {
		out, _ := queryCommand(ctx, "systemctl", "is-active", nftUnit)
		if strings.TrimSpace(out) != "active" {
			// Starting the unit loads the file and keeps it loaded at boot.
			if err := runCommand(ctx, log, "systemctl", "enable", "--now", nftUnit); err != nil {
				put()
				return err
			}
			return nil
		}
	}
	if err := runCommand(ctx, log, nftBinary, "-f", p); err != nil {
		put()
		return err
	}
	return nil
}

func (n *nftBackend) disable(ctx context.Context, log io.Writer) error {
	if systemdRunning() {
		if err := runCommand(ctx, log, "systemctl", "disable", "--now", nftUnit); err != nil {
			return err
		}
	}
	if n.loaded(ctx) {
		return runCommand(ctx, log, nftBinary, "delete", "table", "inet", "cairn")
	}
	return nil
}

// ufw -----------------------------------------------------------------------

const (
	ufwBinary  = "/usr/sbin/ufw"
	ufwTagHead = "cairn:"
)

// ufwBackend adds Cairn's rules with the ufw command, each tagged with a
// comment "cairn:<id>:<hash>" so they can be found again; rules without the
// tag are the user's own and stay. Blocking rules go first (prepend).
type ufwBackend struct{ root string }

func (u *ufwBackend) installed() bool    { return fileExists(filepath.Join(u.root, ufwBinary)) }
func (u *ufwBackend) packages() []string { return []string{"ufw"} }

func (u *ufwBackend) active(ctx context.Context) bool {
	if !u.installed() {
		return false
	}
	out, err := queryCommand(ctx, ufwBinary, "status")
	return err == nil && strings.HasPrefix(strings.TrimSpace(out), "Status: active")
}

func (u *ufwBackend) loaded(ctx context.Context) bool { return u.active(ctx) }

// ufwTuple is one rule as ufw records it in user.rules:
// "### tuple ### allow tcp 22 0.0.0.0/0 any 1.2.3.4 in comment=<hex>".
type ufwTuple struct {
	action, proto, dport, dst, src string
	app                            bool
	comment                        string
	v6                             bool
}

func parseUFWTuples(data []byte, v6 bool) []ufwTuple {
	var out []ufwTuple
	for _, line := range strings.Split(string(data), "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "### tuple ### ")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		t := ufwTuple{v6: v6}
		var plain []string
		for _, fl := range fields {
			if c, ok := strings.CutPrefix(fl, "comment="); ok {
				if b, err := hex.DecodeString(c); err == nil {
					t.comment = string(b)
				}
				continue
			}
			plain = append(plain, fl)
		}
		// action proto dport dst sport src [dapp sapp] direction
		if len(plain) < 6 {
			continue
		}
		t.action, t.proto, t.dport, t.dst, t.src = plain[0], plain[1], plain[2], plain[3], plain[5]
		t.app = len(plain) > 7
		out = append(out, t)
	}
	return out
}

func (u *ufwBackend) tuples() []ufwTuple {
	read := func(name string) []byte {
		data, _ := os.ReadFile(filepath.Join(u.root, "/etc/ufw", name)) //nolint:gosec // G304: fixed names.
		return data
	}
	return append(parseUFWTuples(read("user.rules"), false), parseUFWTuples(read("user6.rules"), true)...)
}

func anyAddr(s string) string {
	if s == "0.0.0.0/0" || s == "::/0" {
		return "any"
	}
	return s
}

// deleteArgs is the "ufw delete" command for a tuple.
func (t ufwTuple) deleteArgs() []string {
	args := []string{"--force", "delete", strings.TrimSuffix(t.action, "_log")}
	if t.proto != "any" {
		args = append(args, "proto", t.proto)
	}
	args = append(args, "from", anyAddr(t.src), "to", anyAddr(t.dst))
	if t.dport != "any" {
		args = append(args, "port", t.dport)
	}
	return args
}

func (t ufwTuple) String() string {
	s := t.action
	if t.proto != "any" {
		s += " " + t.proto
	}
	if t.dport != "any" {
		s += " " + t.dport
	}
	s += " from " + anyAddr(t.src)
	if t.v6 {
		s += " (v6)"
	}
	return s
}

// ufwTag identifies a rule's ufw rules and their content.
func ufwTag(id string, v any) string {
	data, _ := json.Marshal(v)
	sum := sha256.Sum256(data)
	return ufwTagHead + id + ":" + hex.EncodeToString(sum[:4])
}

type ufwCmd struct {
	tag  string
	args []string
}

// ufwPortGroups splits ports into ufw multiport lists of at most 15
// ports (a range counts twice).
func ufwPortGroups(ports []string) []string {
	var groups []string
	var cur []string
	weight := 0
	for _, p := range ports {
		lo, hi, _ := helperapi.ParsePortRange(p)
		s, w := strconv.Itoa(lo), 1
		if hi != lo {
			s, w = s+":"+strconv.Itoa(hi), 2
		}
		if weight+w > 15 {
			groups = append(groups, strings.Join(cur, ","))
			cur, weight = nil, 0
		}
		cur = append(cur, s)
		weight += w
	}
	if len(cur) > 0 {
		groups = append(groups, strings.Join(cur, ","))
	}
	return groups
}

// ufwCommands lists the commands that create the configuration.
func ufwCommands(r fwRender) []ufwCmd {
	var cmds []ufwCmd
	for _, a := range r.Addresses {
		tag := ufwTag(a.ID, a)
		cmds = append(cmds, ufwCmd{tag, []string{"prepend", a.Action, "from", a.Address, "comment", tag}})
	}
	for _, rule := range r.Rules {
		tag := ufwTag(rule.ID, rule)
		verb := []string{rule.Action}
		if rule.Action == helperapi.FirewallDeny {
			verb = []string{"prepend", rule.Action}
		}
		sources := rule.Sources
		if len(sources) == 0 {
			sources = []string{"any"}
		}
		for _, proto := range protocols(rule.Protocol) {
			for _, src := range sources {
				for _, ports := range ufwPortGroups(rule.Ports) {
					args := append(slices.Clone(verb), "proto", proto, "from", src, "to", "any", "port", ports, "comment", tag)
					cmds = append(cmds, ufwCmd{tag, args})
				}
			}
		}
	}
	return cmds
}

var ufwPingLine = regexp.MustCompile(`(?m)^(-A ufw6?-before-input -p icmp(?:v6)? --icmp(?:v6)?-type echo-request -j )(ACCEPT|DROP)$`)

// setPing switches the echo-request lines of before.rules and
// before6.rules; it reports whether a file changed.
func (u *ufwBackend) setPing(block bool) (bool, error) {
	target := "ACCEPT"
	if block {
		target = "DROP"
	}
	changed := false
	for _, name := range []string{"before.rules", "before6.rules"} {
		p := filepath.Join(u.root, "/etc/ufw", name)
		data, err := os.ReadFile(p) //nolint:gosec // G304: fixed names.
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return changed, err
		}
		out := ufwPingLine.ReplaceAll(data, []byte("${1}"+target))
		if bytes.Equal(out, data) {
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

func ufwRun(ctx context.Context, log io.Writer, args ...string) error {
	return runCommand(ctx, log, ufwBinary, args...)
}

func (u *ufwBackend) apply(ctx context.Context, log io.Writer, r fwRender) error {
	want := ufwCommands(r)
	wanted := map[string]bool{}
	for _, c := range want {
		wanted[c.tag] = true
	}
	have := map[string]bool{}
	for _, t := range u.tuples() {
		if !strings.HasPrefix(t.comment, ufwTagHead) {
			continue
		}
		if wanted[t.comment] {
			have[t.comment] = true
			continue
		}
		// Deleting a rule for both families removes its IPv6 twin too;
		// the second delete then finds nothing, which is fine.
		var out bytes.Buffer
		if err := ufwRun(ctx, io.MultiWriter(log, &out), t.deleteArgs()...); err != nil && !strings.Contains(out.String(), "non-existent") {
			return err
		}
	}
	for _, c := range want {
		if have[c.tag] {
			continue
		}
		if err := ufwRun(ctx, log, c.args...); err != nil {
			return err
		}
	}
	pingChanged, err := u.setPing(r.BlockPing)
	if err != nil {
		return err
	}
	if !u.active(ctx) {
		if err := ufwRun(ctx, log, "default", "deny", "incoming"); err != nil {
			return err
		}
		if err := ufwRun(ctx, log, "default", "allow", "outgoing"); err != nil {
			return err
		}
		return ufwRun(ctx, log, "--force", "enable")
	}
	if pingChanged {
		return ufwRun(ctx, log, "reload")
	}
	return nil
}

func (u *ufwBackend) disable(ctx context.Context, log io.Writer) error {
	if !u.installed() {
		return nil
	}
	return ufwRun(ctx, log, "disable")
}

func (u *ufwBackend) external(context.Context) []string {
	out := []string{}
	for _, t := range u.tuples() {
		if !strings.HasPrefix(t.comment, ufwTagHead) {
			out = append(out, t.String())
		}
	}
	return out
}

// firewalld -----------------------------------------------------------------

const (
	firewalldPolicyFile = "/etc/firewalld/policies/cairn.xml"
	firewallCmd         = "/usr/bin/firewall-cmd"
	firewallOfflineCmd  = "/usr/bin/firewall-offline-cmd"
)

// firewalldBackend keeps Cairn's rules in a firewalld policy of its own
// (traffic from any zone to the host), evaluated before the zones; the
// zones and their services stay as they are.
type firewalldBackend struct{ root string }

func (d *firewalldBackend) installed() bool {
	return fileExists(filepath.Join(d.root, firewallCmd)) || fileExists(filepath.Join(d.root, "/usr/sbin/firewalld"))
}
func (d *firewalldBackend) packages() []string { return []string{"firewalld"} }

func (d *firewalldBackend) active(ctx context.Context) bool {
	if !d.installed() {
		return false
	}
	out, err := queryCommand(ctx, firewallCmd, "--state")
	return err == nil && strings.TrimSpace(out) == "running"
}

func (d *firewalldBackend) loaded(ctx context.Context) bool {
	if !d.active(ctx) {
		return false
	}
	_, err := queryCommand(ctx, firewallCmd, "--info-policy=cairn")
	return err == nil
}

type fwdXML struct {
	XMLName     xml.Name     `xml:"policy"`
	Priority    int          `xml:"priority,attr"`
	Target      string       `xml:"target,attr"`
	Short       string       `xml:"short"`
	Description string       `xml:"description"`
	Ingress     fwdZone      `xml:"ingress-zone"`
	Egress      fwdZone      `xml:"egress-zone"`
	ICMPBlocks  []fwdName    `xml:"icmp-block"`
	Ports       []fwdPort    `xml:"port"`
	Rules       []fwdRichXML `xml:"rule"`
}

type fwdZone struct {
	Name string `xml:"name,attr"`
}

type fwdName struct {
	Name string `xml:"name,attr"`
}

type fwdPort struct {
	Port     string `xml:"port,attr"`
	Protocol string `xml:"protocol,attr"`
}

type fwdSource struct {
	Address string `xml:"address,attr"`
}

type fwdRichXML struct {
	Family   string     `xml:"family,attr,omitempty"`
	Priority int        `xml:"priority,attr,omitempty"`
	Source   *fwdSource `xml:"source"`
	Port     *fwdPort   `xml:"port"`
	Accept   *struct{}  `xml:"accept"`
	Drop     *struct{}  `xml:"drop"`
}

// Rich rule priorities: blocked addresses, then trusted ones, then deny
// rules; allow rules with sources sit with the ports.
const (
	fwdPrioDenyAddr  = -300
	fwdPrioAllowAddr = -200
	fwdPrioDenyRule  = -100
)

func fwdPortString(p string) string {
	lo, hi, _ := helperapi.ParsePortRange(p)
	if hi != lo {
		return strconv.Itoa(lo) + "-" + strconv.Itoa(hi)
	}
	return strconv.Itoa(lo)
}

func family(addr string) string {
	if p, ok := helperapi.ParseSource(addr); ok && p.Addr().Is6() {
		return "ipv6"
	}
	return "ipv4"
}

func renderFirewalld(r fwRender) ([]byte, error) {
	pol := fwdXML{Priority: -100, Target: "CONTINUE", Short: "Cairn",
		Description: "Managed by Cairn. Change the firewall in the panel instead of editing this file.",
		Ingress:     fwdZone{"ANY"}, Egress: fwdZone{"HOST"}}
	if r.BlockPing {
		pol.ICMPBlocks = []fwdName{{"echo-request"}}
	}
	for _, a := range r.Addresses {
		rule := fwdRichXML{Family: family(a.Address), Source: &fwdSource{a.Address}}
		if a.Action == helperapi.FirewallAllow {
			rule.Priority, rule.Accept = fwdPrioAllowAddr, &struct{}{}
		} else {
			rule.Priority, rule.Drop = fwdPrioDenyAddr, &struct{}{}
		}
		pol.Rules = append(pol.Rules, rule)
	}
	for _, rule := range r.Rules {
		for _, proto := range protocols(rule.Protocol) {
			for _, p := range rule.Ports {
				port := fwdPort{fwdPortString(p), proto}
				if len(rule.Sources) == 0 {
					pol.Ports = append(pol.Ports, port)
					continue
				}
				for _, s := range normalizeSources(rule.Sources) {
					x := fwdRichXML{Family: family(s), Source: &fwdSource{s}, Port: &port}
					if rule.Action == helperapi.FirewallDeny {
						x.Priority, x.Drop = fwdPrioDenyRule, &struct{}{}
					} else {
						x.Accept = &struct{}{}
					}
					pol.Rules = append(pol.Rules, x)
				}
			}
		}
	}
	// firewalld refuses duplicate entries (the panel port may also be in a
	// user rule).
	seenPort := map[fwdPort]bool{}
	pol.Ports = slices.DeleteFunc(pol.Ports, func(p fwdPort) bool {
		dup := seenPort[p]
		seenPort[p] = true
		return dup
	})
	seenRule := map[string]bool{}
	pol.Rules = slices.DeleteFunc(pol.Rules, func(x fwdRichXML) bool {
		key, _ := xml.Marshal(x)
		dup := seenRule[string(key)]
		seenRule[string(key)] = true
		return dup
	})
	out, err := xml.MarshalIndent(pol, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), append(out, '\n')...), nil
}

var firewalldVersion = regexp.MustCompile(`^([0-9]+)\.([0-9]+)`)

func (d *firewalldBackend) supported(ctx context.Context) error {
	out, err := queryCommand(ctx, firewallCmd, "--version")
	if err != nil {
		return nil //nolint:nilerr // the daemon is not running yet; it is checked again once started
	}
	m := firewalldVersion.FindStringSubmatch(strings.TrimSpace(out))
	if m == nil {
		return nil
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major == 0 && minor < 9 {
		return fwErr(helperapi.CodeFirewallUnsupported, map[string]any{"reason": "firewalld_old", "version": strings.TrimSpace(out)})
	}
	return nil
}

func (d *firewalldBackend) apply(ctx context.Context, log io.Writer, r fwRender) error {
	if err := d.supported(ctx); err != nil {
		return err
	}
	data, err := renderFirewalld(r)
	if err != nil {
		return err
	}
	p := filepath.Join(d.root, firewalldPolicyFile)
	old, oldErr := os.ReadFile(p) //nolint:gosec // G304: a fixed path.
	changed := oldErr != nil || !bytes.Equal(old, data)
	if changed {
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			return err
		}
		if err := writeFileMode(p, data, 0o640); err != nil {
			return err
		}
	}
	put := func() {
		if oldErr == nil {
			_ = writeFileMode(p, old, 0o640)
		} else {
			_ = os.Remove(p)
		}
	}
	running := d.active(ctx)
	check := []string{firewallOfflineCmd, "--check-config"}
	if running {
		check = []string{firewallCmd, "--check-config"}
	}
	if err := runCommand(ctx, log, check[0], check[1:]...); err != nil {
		put()
		return err
	}
	if !running {
		if err := runCommand(ctx, log, "systemctl", "enable", "--now", "firewalld"); err != nil {
			put()
			return err
		}
		return d.supported(ctx)
	}
	if changed || !d.loaded(ctx) {
		if err := runCommand(ctx, log, firewallCmd, "--reload"); err != nil {
			put()
			_ = runCommand(ctx, io.Discard, firewallCmd, "--reload")
			return err
		}
	}
	return nil
}

func (d *firewalldBackend) disable(ctx context.Context, log io.Writer) error {
	if !d.installed() || !systemdRunning() {
		return nil
	}
	return runCommand(ctx, log, "systemctl", "disable", "--now", "firewalld")
}

// external lists the default zone's services and ports.
func (d *firewalldBackend) external(ctx context.Context) []string {
	out, err := queryCommand(ctx, firewallCmd, "--list-all")
	if err != nil {
		return nil
	}
	var zone string
	var list []string
	for i, line := range strings.Split(out, "\n") {
		if i == 0 {
			zone = strings.Fields(line + " ")[0]
			continue
		}
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		v = strings.TrimSpace(v)
		if !ok || v == "" {
			continue
		}
		switch k {
		case "services", "ports", "rich rules", "sources":
			list = append(list, fmt.Sprintf("%s: %s %s", zone, k, v))
		}
	}
	return list
}
