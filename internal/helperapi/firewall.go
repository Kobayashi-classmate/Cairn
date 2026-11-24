// SPDX-License-Identifier: AGPL-3.0-only

package helperapi

import (
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The firewall: Cairn keeps its own rules (port rules, trusted and blocked
// addresses, ping, port forwards) and renders them into the backend the
// system uses: firewalld (policies of its own), ufw (rules tagged with a
// comment) or nftables (a table of its own). The SSH ports and the panel port are
// always allowed. A change from the panel is rolled back after 60 seconds
// unless it is confirmed over a new connection.
const (
	ActionFirewallStatus        = "firewall.status"         // synchronous
	ActionFirewallRuleSet       = "firewall.rule.set"       // synchronous: add (no id) or change a rule
	ActionFirewallRuleDelete    = "firewall.rule.delete"    // synchronous
	ActionFirewallAddressSet    = "firewall.address.set"    // synchronous: add an address
	ActionFirewallAddressDelete = "firewall.address.delete" // synchronous
	ActionFirewallPing          = "firewall.ping"           // synchronous
	ActionFirewallForwardSet    = "firewall.forward.set"    // synchronous: add (no id) or change a port forward
	ActionFirewallForwardDelete = "firewall.forward.delete" // synchronous
	ActionFirewallConfirm       = "firewall.confirm"        // synchronous: keep a pending change
	ActionFirewallRollback      = "firewall.rollback"       // synchronous: undo a pending change now
	ActionFirewallEnable        = "firewall.enable"         // operation: may install the backend
	ActionFirewallDisable       = "firewall.disable"        // operation
)

// Backends.
const (
	FirewallNftables  = "nftables"
	FirewallUFW       = "ufw"
	FirewallFirewalld = "firewalld"
)

// Rule owners besides the user ("").
const (
	OwnerSSH   = "ssh"   // protected: the SSH ports
	OwnerPanel = "panel" // protected: the panel port
	// OwnerStreamPrefix and OwnerDBPrefix mark rules opened by another
	// feature: "stream:<code>", "db:<engine>".
	OwnerStreamPrefix = "stream:"
	OwnerDBPrefix     = "db:"
)

// Firewall error codes.
const (
	CodeFirewallPending        = "firewall.pending"
	CodeFirewallNoPending      = "firewall.no_pending"
	CodeFirewallConfirmRetry   = "firewall.confirm_retry"
	CodeFirewallSelfBlock      = "firewall.self_block"
	CodeFirewallOverlap        = "firewall.overlap"
	CodeFirewallProtected      = "firewall.protected"
	CodeFirewallManaged        = "firewall.managed"
	CodeFirewallNotEnabled     = "firewall.not_enabled"
	CodeFirewallBackendMissing = "firewall.backend_missing"
	CodeFirewallConflict       = "firewall.conflict"
	CodeFirewallApplyFailed    = "firewall.apply_failed"
	CodeFirewallNotFound       = "firewall.not_found"
	CodeFirewallUnsupported    = "firewall.unsupported"
	CodeFirewallDenyNeedsSrc   = "firewall.deny_needs_source"
	// CodeFirewallForwardTaken: another forward already listens on the port.
	CodeFirewallForwardTaken = "firewall.forward_taken"
	// CodeFirewallRollbackFailed: a change could not be undone; its rules
	// are still in force and it stays pending.
	CodeFirewallRollbackFailed = "firewall.rollback_failed"
)

// Limits.
const (
	FirewallMaxPorts   = 15
	FirewallMaxSources = 20
	FirewallMaxRules   = 500
	// FirewallMaxForwards is how many port forwards Cairn keeps.
	FirewallMaxForwards = 100
	// FirewallConfirmSeconds is how long a change waits for confirmation.
	FirewallConfirmSeconds = 60
)

// Rule actions and protocols.
const (
	FirewallAllow = "allow"
	FirewallDeny  = "deny"
	ProtoTCP      = "tcp"
	ProtoUDP      = "udp"
	ProtoBoth     = "both"
)

var (
	firewallRuleID    = regexp.MustCompile(`^r[0-9a-f]{15}$`)
	firewallAddressID = regexp.MustCompile(`^a[0-9a-f]{15}$`)
	firewallTxID      = regexp.MustCompile(`^x[0-9a-f]{15}$`)
	firewallForwardID = regexp.MustCompile(`^f[0-9a-f]{15}$`)
	portRange         = regexp.MustCompile(`^([0-9]{1,5})(?:-([0-9]{1,5}))?$`)
)

// FirewallRule opens (allow) or closes (deny) ports, for everyone or for
// some sources. A deny rule needs sources: inbound traffic is denied
// unless allowed anyway.
type FirewallRule struct {
	ID       string   `json:"id"`
	Action   string   `json:"action"`
	Protocol string   `json:"protocol"`
	Ports    []string `json:"ports"`
	Sources  []string `json:"sources,omitempty"`
	Note     string   `json:"note,omitempty"`
	Owner    string   `json:"owner,omitempty"`
}

// FirewallAddress trusts (allow: every port) or blocks (deny: every port)
// an address or network.
type FirewallAddress struct {
	ID      string `json:"id"`
	Address string `json:"address"`
	Action  string `json:"action"`
	Note    string `json:"note,omitempty"`
}

// FirewallForward sends connections arriving on a port to another port of
// this host (no ToAddress) or to a port of another IPv4 host. Forwarding to
// another host needs IP forwarding, which Cairn switches on while such a
// forward exists, and is masqueraded so that replies come back through this
// host. Forwarding to IPv6 hosts is not offered: switching on IPv6
// forwarding stops a host from configuring its own addresses from router
// advertisements.
type FirewallForward struct {
	ID        string `json:"id"`
	Protocol  string `json:"protocol"`
	Port      int    `json:"port"`
	ToAddress string `json:"toAddress,omitempty"`
	ToPort    int    `json:"toPort"`
	Note      string `json:"note,omitempty"`
}

// Remote reports whether the forward goes to another host.
func (f FirewallForward) Remote() bool { return f.ToAddress != "" }

// FirewallPending is a change waiting for confirmation.
type FirewallPending struct {
	ID      string `json:"id"`
	Expires int64  `json:"expires"`
	Kind    string `json:"kind"`
	// RollbackFailed is set once undoing the change failed: its time is
	// up (or it was given up) and its rules are still in force.
	RollbackFailed *FirewallRollbackFailure `json:"rollbackFailed,omitempty"`
}

// FirewallRollbackFailure records the last failed try to undo a change.
type FirewallRollbackFailure struct {
	At       int64  `json:"at"`
	Attempts int    `json:"attempts"`
	Detail   string `json:"detail"`
}

// FirewallRollback records the last change that was rolled back: not
// confirmed in time, or undone on request. A rollback that failed is not
// recorded here but in the change, which stays pending.
type FirewallRollback struct {
	ID        string `json:"id"`
	At        int64  `json:"at"`
	Kind      string `json:"kind"`
	Requested bool   `json:"requested,omitempty"`
}

// FirewallStatus describes the firewall.
type FirewallStatus struct {
	Backend   string `json:"backend"`
	Installed bool   `json:"installed"`
	Enabled   bool   `json:"enabled"`
	Loaded    bool   `json:"loaded"`
	Conflict  string `json:"conflict"`
	Systemd   bool   `json:"systemd"`
	BlockPing bool   `json:"blockPing"`
	SSHPorts  []int  `json:"sshPorts"`
	// SSHUnread reports that sshd's configuration could not be read:
	// SSHPorts are then the ports sshd listens on now, the ones known
	// before and 22.
	SSHUnread bool              `json:"sshUnread"`
	PanelPort int               `json:"panelPort"`
	Rules     []FirewallRule    `json:"rules"`
	Addresses []FirewallAddress `json:"addresses"`
	Forwards  []FirewallForward `json:"forwards"`
	// IPForward reports whether the kernel forwards IPv4 packets, which
	// forwards to other hosts need.
	IPForward    bool              `json:"ipForward"`
	External     []string          `json:"external"`
	Pending      *FirewallPending  `json:"pending"`
	LastRollback *FirewallRollback `json:"lastRollback"`
}

// FirewallChange is the answer to a change: the pending confirmation (nil
// when the change needed none, e.g. while the firewall is off) and the
// rule or address with its id.
type FirewallChange struct {
	Pending *FirewallPending `json:"pending"`
	Rule    *FirewallRule    `json:"rule,omitempty"`
	Address *FirewallAddress `json:"address,omitempty"`
	Forward *FirewallForward `json:"forward,omitempty"`
}

// FirewallRef names a rule, an address or a pending change.
type FirewallRef struct {
	ID string `json:"id"`
}

// Validate implements the helper's request validation hook.
func (r FirewallRef) Validate() error {
	if !firewallRuleID.MatchString(r.ID) && !firewallAddressID.MatchString(r.ID) && !firewallTxID.MatchString(r.ID) &&
		!firewallForwardID.MatchString(r.ID) {
		return invalid("id", "firewall_id")
	}
	return nil
}

// FirewallPingRequest blocks or allows ping (ICMP echo requests).
type FirewallPingRequest struct {
	Block bool `json:"block"`
}

// FirewallEnableRequest switches the firewall on; Confirm asks for the
// rollback window (false only from the command line at install time).
type FirewallEnableRequest struct {
	Confirm bool `json:"confirm"`
}

// ValidFirewallRuleID reports whether s names a rule.
func ValidFirewallRuleID(s string) bool { return firewallRuleID.MatchString(s) }

// ValidFirewallAddressID reports whether s names an address entry.
func ValidFirewallAddressID(s string) bool { return firewallAddressID.MatchString(s) }

// ValidFirewallForwardID reports whether s names a port forward.
func ValidFirewallForwardID(s string) bool { return firewallForwardID.MatchString(s) }

// ValidFirewallTxID reports whether s names a pending change.
func ValidFirewallTxID(s string) bool { return firewallTxID.MatchString(s) }

// ParsePortRange reads "80" or "8000-8100".
func ParsePortRange(s string) (lo, hi int, ok bool) {
	m := portRange.FindStringSubmatch(s)
	if m == nil {
		return 0, 0, false
	}
	lo, _ = strconv.Atoi(m[1])
	hi = lo
	if m[2] != "" {
		hi, _ = strconv.Atoi(m[2])
	}
	if lo < 1 || hi > 65535 || hi < lo {
		return 0, 0, false
	}
	return lo, hi, true
}

// ParseSource reads an address or a network as a prefix; a bare address
// becomes a /32 or /128. The prefix is masked (10.1.2.3/8 -> 10.0.0.0/8).
func ParseSource(s string) (netip.Prefix, bool) {
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil || p.Addr().Zone() != "" {
			return netip.Prefix{}, false
		}
		p = p.Masked()
		if p.Addr().Is4In6() {
			return netip.Prefix{}, false
		}
		return p, true
	}
	a, err := netip.ParseAddr(s)
	if err != nil || a.Zone() != "" || a.Is4In6() {
		return netip.Prefix{}, false
	}
	return netip.PrefixFrom(a, a.BitLen()), true
}

// CanonicalSource writes a source the way Cairn stores it: an address
// without a prefix length when it is a single host.
func CanonicalSource(p netip.Prefix) string {
	if p.IsSingleIP() {
		return p.Addr().String()
	}
	return p.String()
}

func validNote(s string) bool {
	return utf8.RuneCountInString(s) <= 64 && !strings.ContainsAny(s, "\x00\r\n\"'\\`")
}

// Validate checks a rule's shape. It does not check ownership or
// overlaps; the helper does.
func (r FirewallRule) Validate() error {
	if r.ID != "" && !firewallRuleID.MatchString(r.ID) {
		return invalid("id", "firewall_id")
	}
	if r.Action != FirewallAllow && r.Action != FirewallDeny {
		return invalid("action", "oneof")
	}
	if r.Protocol != ProtoTCP && r.Protocol != ProtoUDP && r.Protocol != ProtoBoth {
		return invalid("protocol", "oneof")
	}
	if len(r.Ports) == 0 || len(r.Ports) > FirewallMaxPorts {
		return invalid("ports", "count")
	}
	for _, p := range r.Ports {
		if _, _, ok := ParsePortRange(p); !ok {
			return invalid("ports", "port")
		}
	}
	if len(r.Sources) > FirewallMaxSources {
		return invalid("sources", "count")
	}
	for _, s := range r.Sources {
		if _, ok := ParseSource(s); !ok {
			return invalid("sources", "cidr")
		}
	}
	if r.Action == FirewallDeny && len(r.Sources) == 0 {
		return &Error{Code: CodeFirewallDenyNeedsSrc}
	}
	if !validNote(r.Note) {
		return invalid("note", "max")
	}
	if r.Owner != "" && r.Owner != OwnerSSH && r.Owner != OwnerPanel &&
		!strings.HasPrefix(r.Owner, OwnerStreamPrefix) && !strings.HasPrefix(r.Owner, OwnerDBPrefix) {
		return invalid("owner", "oneof")
	}
	return nil
}

// Validate checks an address entry.
func (a FirewallAddress) Validate() error {
	if a.ID != "" && !firewallAddressID.MatchString(a.ID) {
		return invalid("id", "firewall_id")
	}
	if a.Action != FirewallAllow && a.Action != FirewallDeny {
		return invalid("action", "oneof")
	}
	if _, ok := ParseSource(a.Address); !ok {
		return invalid("address", "cidr")
	}
	if !validNote(a.Note) {
		return invalid("note", "max")
	}
	return nil
}

// ForwardTarget reads a forward's target: an IPv4 address of another host
// (not loopback, unspecified, multicast or the broadcast address).
func ForwardTarget(s string) (netip.Addr, bool) {
	a, err := netip.ParseAddr(s)
	if err != nil || !a.Is4() {
		return netip.Addr{}, false
	}
	if a.IsLoopback() || a.IsUnspecified() || a.IsMulticast() || a == netip.AddrFrom4([4]byte{255, 255, 255, 255}) {
		return netip.Addr{}, false
	}
	return a, true
}

// Validate checks a port forward. Whether its port is free is checked by
// the helper.
func (f FirewallForward) Validate() error {
	if f.ID != "" && !firewallForwardID.MatchString(f.ID) {
		return invalid("id", "firewall_id")
	}
	if f.Protocol != ProtoTCP && f.Protocol != ProtoUDP && f.Protocol != ProtoBoth {
		return invalid("protocol", "oneof")
	}
	if f.Port < 1 || f.Port > 65535 {
		return invalid("port", "port")
	}
	if f.ToPort < 1 || f.ToPort > 65535 {
		return invalid("toPort", "port")
	}
	if f.ToAddress != "" {
		if _, ok := ForwardTarget(f.ToAddress); !ok {
			return invalid("toAddress", "forward_target")
		}
	} else if f.ToPort == f.Port {
		return invalid("toPort", "same_port")
	}
	if !validNote(f.Note) {
		return invalid("note", "max")
	}
	return nil
}
