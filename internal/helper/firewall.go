// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// Firewall keeps Cairn's firewall configuration (its rules, trusted and
// blocked addresses, ping) and renders it into the backend the system
// uses. Only one backend is ever active: a running firewalld or ufw is
// used as it is; without either, the platform's default is enabled
// (firewalld on EL, ufw on Ubuntu, a table of Cairn's own in nftables on
// Debian).
//
// The SSH ports (see sshDetect) and the panel port are always allowed. A
// change from the panel is rolled back after 60 seconds unless it is
// confirmed. The change is written down as pending and its rollback timer
// started before the first rule changes, and the timer is a unit of its
// own: the rollback happens even if the helper stops or the request that
// made the change goes away, and a pending change left behind by a reboot
// is taken over when the system comes up (Recover).
type Firewall struct {
	StateDir string
	// Software installs the backend when it is missing (nil: never).
	Software *Software
	// Default is the platform profile's firewall: none | ufw | firewalld.
	Default    string
	PanelPort  int
	Executable string
	// Root prefixes system paths (tests).
	Root string
	Log  *slog.Logger

	mu     sync.Mutex
	timers map[string]*time.Timer // pending rollbacks without systemd
	now    func() time.Time
	// backends replaces the real backends (tests).
	backends map[string]fwBackend
}

// fwConfig is what Cairn wants the firewall to be.
type fwConfig struct {
	Backend      string                      `json:"backend,omitempty"`
	Enabled      bool                        `json:"enabled"`
	BlockPing    bool                        `json:"blockPing"`
	Rules        []helperapi.FirewallRule    `json:"rules"`
	Addresses    []helperapi.FirewallAddress `json:"addresses"`
	Forwards     []helperapi.FirewallForward `json:"forwards,omitempty"`
	Pending      *fwPending                  `json:"pending,omitempty"`
	LastRollback *helperapi.FirewallRollback `json:"lastRollback,omitempty"`
	// SSHSeen is where SSH was found the last time sshd's configuration
	// could be read: what stays open when it cannot be.
	SSHSeen []int `json:"sshSeen,omitempty"`
	// SSHKeep are ports an administrator was connected through (at install
	// time) that the detection did not find; they stay open until it does.
	SSHKeep []int `json:"sshKeep,omitempty"`
}

// fwPending is a change waiting for confirmation, with the configuration
// to go back to.
type fwPending struct {
	helperapi.FirewallPending
	Previous fwConfig `json:"previous"`
	// Backend is the backend the change is applied to, which is not always
	// the one of Previous ("" in state files written before it was kept).
	Backend string `json:"backend,omitempty"`
	// Applying is set until the rules are in place. Whoever finds it while
	// holding the lock knows the apply was cut short: such a change can be
	// undone, never confirmed.
	Applying bool `json:"applying,omitempty"`
}

// fwRender is what a backend puts in place: every rule (the protected ones
// first), the addresses, ping and the port forwards.
type fwRender struct {
	Rules     []helperapi.FirewallRule
	Addresses []helperapi.FirewallAddress
	BlockPing bool
	Forwards  []helperapi.FirewallForward
}

// fwBackend is one way of enforcing the configuration.
type fwBackend interface {
	installed() bool
	packages() []string
	// active reports whether the backend is enforcing rules on the system.
	active(ctx context.Context) bool
	// loaded reports whether Cairn's rules are in effect.
	loaded(ctx context.Context) bool
	// apply puts the rules in place and switches the backend on.
	apply(ctx context.Context, log io.Writer, r fwRender) error
	disable(ctx context.Context, log io.Writer) error
	// external lists rules on the system that Cairn did not make.
	external(ctx context.Context) []string
}

const (
	fwConfirm      = helperapi.FirewallConfirmSeconds * time.Second
	fwRollbackUnit = "cairn-fw-rollback-"
	// fwCommandLimit bounds starting and stopping a timer; fwApplyLimit a
	// backend's apply or disable (ufw runs one command per rule).
	fwCommandLimit = 2 * time.Minute
	fwApplyLimit   = 5 * time.Minute
	// fwSetupConfirm is how long the install-time setup waits for a person
	// to check a new SSH connection and answer.
	fwSetupConfirm = 5 * time.Minute
)

// FirewallSetupConfirmMinutes is fwSetupConfirm for the installer's prompt.
const FirewallSetupConfirmMinutes = int(fwSetupConfirm / time.Minute)

// fwRollbackRetry is how long the timer waits before it tries a rollback
// that failed again; after the last try the failure stays on record for
// the next start of the helper and for "roll back now".
var fwRollbackRetry = []time.Duration{10 * time.Second, 30 * time.Second}

// fwSleep waits (tests shorten it).
var fwSleep = time.Sleep

// RegisterFirewall adds the firewall actions.
func RegisterFirewall(r *Registry, o *Ops, f *Firewall) {
	Register(r, helperapi.ActionFirewallStatus, func(ctx context.Context, _ helperapi.Caller, _ helperapi.Empty) (helperapi.FirewallStatus, error) {
		return f.Status(ctx), nil
	})
	Register(r, helperapi.ActionFirewallRuleSet, f.setRule)
	Register(r, helperapi.ActionFirewallRuleDelete, f.deleteRule)
	Register(r, helperapi.ActionFirewallAddressSet, f.setAddress)
	Register(r, helperapi.ActionFirewallAddressDelete, f.deleteAddress)
	Register(r, helperapi.ActionFirewallForwardSet, f.setForward)
	Register(r, helperapi.ActionFirewallForwardDelete, f.deleteForward)
	Register(r, helperapi.ActionFirewallPing, func(ctx context.Context, c helperapi.Caller, q helperapi.FirewallPingRequest) (helperapi.FirewallChange, error) {
		return f.change(ctx, c, "ping", func(cfg *fwConfig) error {
			cfg.BlockPing = q.Block
			return nil
		})
	})
	Register(r, helperapi.ActionFirewallConfirm, func(ctx context.Context, _ helperapi.Caller, q helperapi.FirewallRef) (helperapi.Empty, error) {
		return helperapi.Empty{}, f.Confirm(ctx, q.ID)
	})
	Register(r, helperapi.ActionFirewallRollback, func(ctx context.Context, _ helperapi.Caller, q helperapi.FirewallRef) (helperapi.Empty, error) {
		return helperapi.Empty{}, f.RollbackNow(ctx, q.ID)
	})
	locks := func(helperapi.FirewallEnableRequest) []string { return []string{"firewall", LockPkg} }
	RegisterOp(r, o, helperapi.ActionFirewallEnable, OpSpec[helperapi.FirewallEnableRequest]{Locks: locks,
		Run: func(ctx context.Context, c helperapi.Caller, q helperapi.FirewallEnableRequest, log io.Writer) (any, error) {
			p, err := f.Enable(ctx, c, q.Confirm, log)
			return helperapi.FirewallChange{Pending: p}, err
		}})
	RegisterOp(r, o, helperapi.ActionFirewallDisable, OpSpec[helperapi.Empty]{
		Locks: func(helperapi.Empty) []string { return []string{"firewall"} },
		Run: func(ctx context.Context, _ helperapi.Caller, _ helperapi.Empty, log io.Writer) (any, error) {
			return helperapi.FirewallChange{}, f.Disable(ctx, log)
		}})
}

func fwErr(code string, params map[string]any) error {
	return &helperapi.Error{Code: code, Params: params}
}

func (f *Firewall) clock() time.Time {
	if f.now != nil {
		return f.now()
	}
	return time.Now()
}

func (f *Firewall) stateDir(parts ...string) string {
	return filepath.Join(append([]string{f.StateDir, "firewall"}, parts...)...)
}

func newHexID(prefix string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)[:15]
}

// lock keeps changes apart, within the helper and across processes (the
// rollback timer and the install-time setup run in processes of their own).
func (f *Firewall) lock() (func(), error) {
	f.mu.Lock()
	if err := os.MkdirAll(f.stateDir(), 0o700); err != nil {
		f.mu.Unlock()
		return nil, err
	}
	fh, err := os.OpenFile(f.stateDir("lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		f.mu.Unlock()
		return nil, err
	}
	if err := syscall.Flock(int(fh.Fd()), syscall.LOCK_EX); err != nil {
		_ = fh.Close()
		f.mu.Unlock()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(fh.Fd()), syscall.LOCK_UN)
		_ = fh.Close()
		f.mu.Unlock()
	}, nil
}

func (f *Firewall) load() (fwConfig, error) {
	var cfg fwConfig
	data, err := os.ReadFile(f.stateDir("state.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	return cfg, json.Unmarshal(data, &cfg)
}

func (f *Firewall) save(cfg fwConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return writeFileMode(f.stateDir("state.json"), data, 0o600)
}

func (cfg fwConfig) clone() fwConfig {
	out := cfg
	out.Pending = nil
	out.Rules = slices.Clone(cfg.Rules)
	for i := range out.Rules {
		out.Rules[i].Ports = slices.Clone(out.Rules[i].Ports)
		out.Rules[i].Sources = slices.Clone(out.Rules[i].Sources)
	}
	out.Addresses = slices.Clone(cfg.Addresses)
	out.Forwards = slices.Clone(cfg.Forwards)
	out.SSHSeen = slices.Clone(cfg.SSHSeen)
	out.SSHKeep = slices.Clone(cfg.SSHKeep)
	return out
}

// Backends ----------------------------------------------------------------

func (f *Firewall) backend(name string) fwBackend {
	var b fwBackend
	switch name {
	case helperapi.FirewallUFW:
		b = &ufwBackend{root: f.Root}
	case helperapi.FirewallFirewalld:
		b = &firewalldBackend{root: f.Root}
	case helperapi.FirewallNftables:
		b = &nftBackend{root: f.Root}
	}
	if fake, ok := f.backends[name]; ok {
		b = fake
	}
	if b == nil {
		return nil
	}
	return fwForwarding{fwBackend: b, f: f}
}

// fwSystem is what runs on the system.
type fwSystem struct {
	backend  string
	conflict string
	active   bool
}

// detect finds the backend to use: a running firewalld or ufw, Cairn's
// nftables table when Cairn enabled it, otherwise the platform default.
func (f *Firewall) detect(ctx context.Context, cfg fwConfig) fwSystem {
	fd := f.backend(helperapi.FirewallFirewalld).active(ctx)
	uf := f.backend(helperapi.FirewallUFW).active(ctx)
	nft := cfg.Enabled && cfg.Backend == helperapi.FirewallNftables
	switch {
	case fd && uf:
		return fwSystem{conflict: "ufw_and_firewalld"}
	case nft && fd:
		return fwSystem{conflict: "nftables_and_firewalld"}
	case nft && uf:
		return fwSystem{conflict: "nftables_and_ufw"}
	case fd:
		return fwSystem{backend: helperapi.FirewallFirewalld, active: true}
	case uf:
		return fwSystem{backend: helperapi.FirewallUFW, active: true}
	case nft:
		return fwSystem{backend: helperapi.FirewallNftables, active: true}
	}
	switch f.Default {
	case helperapi.FirewallUFW, helperapi.FirewallFirewalld:
		return fwSystem{backend: f.Default}
	}
	return fwSystem{backend: helperapi.FirewallNftables}
}

// Protected rules -----------------------------------------------------------

// fwSSH is where SSH stays reachable: the ports of the rule that is always
// there and, when they are partly a guess, why.
type fwSSH struct {
	ports []int
	// unread is not empty when sshd's configuration could not be read.
	unread string
}

// sshView works out the SSH ports to keep open from what the system tells
// (d) and what is on record. While sshd's configuration can be read, they
// are what the system tells now. When it cannot (a file on disk has an
// option this sshd does not know, a host key is missing), the running sshd
// still listens where it did: the ports seen last, the ones the panel set
// and 22 stay open as well. Without any sshd to ask, the ports SSH servers
// were seen listening on stay on record, since such a server may be down
// for a moment just when the rules are applied.
func (f *Firewall) sshView(d sshEndpoints, cfg fwConfig) fwSSH {
	v := fwSSH{ports: addPorts(slices.Clone(d.Ports), cfg.SSHKeep...), unread: d.Unread}
	if d.Installed && d.Unread == "" {
		return v
	}
	v.ports = addPorts(v.ports, cfg.SSHSeen...)
	if d.Installed {
		v.ports = addPorts(v.ports, sshStatePorts(f.StateDir)...)
		v.ports = addPorts(v.ports, 22)
	}
	return v
}

// sshKept reads the SSH ports to keep open without recording anything.
func (f *Firewall) sshKept(ctx context.Context, cfg fwConfig) fwSSH {
	return f.sshView(sshDetect(ctx, f.Root), cfg)
}

// sshRemember is sshKept for a change: what it learns about the SSH ports
// goes into the configuration that will be stored. keep adds ports an
// administrator is connected through.
func (f *Firewall) sshRemember(ctx context.Context, cfg *fwConfig, keep ...int) fwSSH {
	d := sshDetect(ctx, f.Root)
	if d.Installed && d.Unread == "" {
		// The configuration is the authority again: what it lists needs
		// no other record.
		cfg.SSHSeen = slices.Clone(d.Ports)
		cfg.SSHKeep = slices.DeleteFunc(slices.Clone(cfg.SSHKeep), func(p int) bool { return slices.Contains(d.Ports, p) })
	} else {
		cfg.SSHSeen = addPorts(slices.Clone(cfg.SSHSeen), d.Ports...)
	}
	for _, p := range keep {
		if !slices.Contains(d.Ports, p) {
			cfg.SSHKeep = addPorts(slices.Clone(cfg.SSHKeep), p)
		}
	}
	if len(cfg.SSHKeep) == 0 {
		cfg.SSHKeep = nil
	}
	v := f.sshView(d, *cfg)
	if v.unread != "" && f.Log != nil {
		f.Log.Warn("sshd's configuration cannot be read: the SSH ports it listens on now, the ones known before and 22 stay open",
			"ports", v.ports, "err", v.unread)
	}
	return v
}

func portStrings(ports []int) []string {
	out := make([]string, len(ports))
	for i, p := range ports {
		out[i] = strconv.Itoa(p)
	}
	return out
}

// protected returns the rules that are always there.
func (f *Firewall) protected(ssh []int) []helperapi.FirewallRule {
	var rules []helperapi.FirewallRule
	if ports := ssh; len(ports) > 0 {
		rules = append(rules, helperapi.FirewallRule{ID: helperapi.OwnerSSH, Action: helperapi.FirewallAllow, Protocol: helperapi.ProtoTCP,
			Ports: portStrings(ports), Owner: helperapi.OwnerSSH})
	}
	if f.PanelPort > 0 {
		rules = append(rules, helperapi.FirewallRule{ID: helperapi.OwnerPanel, Action: helperapi.FirewallAllow, Protocol: helperapi.ProtoTCP,
			Ports: []string{strconv.Itoa(f.PanelPort)}, Owner: helperapi.OwnerPanel})
	}
	return rules
}

func (f *Firewall) render(cfg fwConfig, ssh []int) fwRender {
	return fwRender{Rules: append(f.protected(ssh), cfg.Rules...), Addresses: cfg.Addresses, BlockPing: cfg.BlockPing, Forwards: cfg.Forwards}
}

// Checks ------------------------------------------------------------------

func coversPort(r helperapi.FirewallRule, port int) bool {
	if r.Protocol == helperapi.ProtoUDP {
		return false
	}
	for _, p := range r.Ports {
		if lo, hi, ok := helperapi.ParsePortRange(p); ok && lo <= port && port <= hi {
			return true
		}
	}
	return false
}

// check refuses configurations that would block the caller's own address
// from the panel or SSH, and trusted addresses overlapping blocked ones:
// backends order rules differently, so the result must not depend on it.
func (f *Firewall) check(cfg fwConfig, remote string, ssh []int) error {
	type entry struct {
		p    netip.Prefix
		text string
	}
	var allow, deny []entry
	seen := map[string]bool{}
	for _, a := range cfg.Addresses {
		p, ok := helperapi.ParseSource(a.Address)
		if !ok {
			return fwErr("validation.invalid", map[string]any{"field": "address", "rule": "cidr"})
		}
		key := a.Action + " " + p.String()
		if seen[key] {
			return fwErr(helperapi.CodeFirewallOverlap, map[string]any{"address": a.Address, "other": a.Address})
		}
		seen[key] = true
		if a.Action == helperapi.FirewallAllow {
			allow = append(allow, entry{p, a.Address})
		} else {
			deny = append(deny, entry{p, a.Address})
		}
	}
	var denyRules []helperapi.FirewallRule
	for _, r := range cfg.Rules {
		if r.Action != helperapi.FirewallDeny {
			continue
		}
		denyRules = append(denyRules, r)
		for _, s := range r.Sources {
			if p, ok := helperapi.ParseSource(s); ok {
				deny = append(deny, entry{p, s})
			}
		}
	}
	for _, a := range allow {
		for _, d := range deny {
			if a.p.Overlaps(d.p) {
				return fwErr(helperapi.CodeFirewallOverlap, map[string]any{"address": a.text, "other": d.text})
			}
		}
	}
	if len(cfg.Rules) > helperapi.FirewallMaxRules || len(cfg.Addresses) > helperapi.FirewallMaxRules {
		return fwErr("validation.invalid", map[string]any{"field": "rules", "rule": "count"})
	}
	if err := checkForwards(cfg.Forwards, append(slices.Clone(ssh), f.PanelPort)); err != nil {
		return err
	}
	ip, err := netip.ParseAddr(remote)
	if err != nil {
		return nil //nolint:nilerr // no caller address (command line): nothing to protect
	}
	ip = ip.Unmap()
	for _, a := range cfg.Addresses {
		if p, ok := helperapi.ParseSource(a.Address); ok && a.Action == helperapi.FirewallDeny && p.Contains(ip) {
			return fwErr(helperapi.CodeFirewallSelfBlock, map[string]any{"address": remote})
		}
	}
	keep := append(portStrings(ssh), strconv.Itoa(f.PanelPort))
	for _, r := range denyRules {
		hit := false
		for _, s := range r.Sources {
			if p, ok := helperapi.ParseSource(s); ok && p.Contains(ip) {
				hit = true
			}
		}
		if !hit {
			continue
		}
		for _, k := range keep {
			if n, _ := strconv.Atoi(k); n > 0 && coversPort(r, n) {
				return fwErr(helperapi.CodeFirewallSelfBlock, map[string]any{"address": remote})
			}
		}
	}
	return nil
}

// Changes -----------------------------------------------------------------

// active reports whether the firewall enforces rules now, and refreshes
// cfg.Enabled from the system for firewalld and ufw (they may have been
// switched on or off outside Cairn).
func (f *Firewall) active(ctx context.Context, cfg *fwConfig, sys fwSystem) bool {
	if sys.backend != helperapi.FirewallNftables {
		cfg.Enabled = sys.active
		if sys.active {
			cfg.Backend = sys.backend
		}
	}
	return cfg.Enabled
}

// detached gives a step of a change a context of its own: the request that
// asked for the change may end (a closed browser tab, a proxy's timeout)
// while rules are half applied, and neither the apply nor what puts the
// old rules back may stop there. Every step gets a fresh time limit, so
// that undoing still works after an apply ran into its own.
func detached(ctx context.Context, limit time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), limit)
}

// change applies a change to the configuration. While the firewall is on,
// the change takes effect at once and waits for confirmation; while it is
// off, it is only stored.
func (f *Firewall) change(ctx context.Context, c helperapi.Caller, kind string, mutate func(*fwConfig) error) (helperapi.FirewallChange, error) {
	ctx = context.WithoutCancel(ctx)
	unlock, err := f.lock()
	if err != nil {
		return helperapi.FirewallChange{}, err
	}
	defer unlock()
	cfg, err := f.load()
	if err != nil {
		return helperapi.FirewallChange{}, err
	}
	if cfg.Pending != nil {
		return helperapi.FirewallChange{}, fwErr(helperapi.CodeFirewallPending, map[string]any{"id": cfg.Pending.ID})
	}
	sys := f.detect(ctx, cfg)
	if sys.conflict != "" {
		return helperapi.FirewallChange{}, fwErr(helperapi.CodeFirewallConflict, map[string]any{"conflict": sys.conflict})
	}
	on := f.active(ctx, &cfg, sys)
	prev := cfg.clone()
	if err := mutate(&cfg); err != nil {
		return helperapi.FirewallChange{}, err
	}
	ssh := f.sshRemember(ctx, &cfg)
	if err := f.check(cfg, c.Remote, ssh.ports); err != nil {
		return helperapi.FirewallChange{}, err
	}
	if !on {
		return helperapi.FirewallChange{}, f.save(cfg)
	}
	var out bytes.Buffer
	p, err := f.commit(ctx, &out, &cfg, prev, sys.backend, kind, ssh.ports, fwConfirm)
	if err != nil {
		return helperapi.FirewallChange{}, applyErr(err, out.String())
	}
	return helperapi.FirewallChange{Pending: p}, nil
}

func applyErr(err error, out string) error {
	if he, ok := helperapi.AsError(err); ok {
		return he
	}
	detail := strings.TrimSpace(out)
	if detail == "" {
		detail = err.Error()
	}
	return fwErr(helperapi.CodeFirewallApplyFailed, map[string]any{"detail": tailString(detail, 600)})
}

// commit applies cfg on backend as a change that waits for confirmation.
// The pending change is stored and its rollback timer started before the
// first rule changes: whatever cuts the apply short (the helper stopping,
// the machine going down), the state says what to go back to and
// something is there to do it. window is how long the change then waits.
func (f *Firewall) commit(ctx context.Context, log io.Writer, cfg *fwConfig, prev fwConfig, backend, kind string, ssh []int,
	window time.Duration) (*helperapi.FirewallPending, error) {
	b := f.backend(backend)
	if b == nil {
		return nil, fwErr(helperapi.CodeFirewallBackendMissing, map[string]any{"backend": backend})
	}
	tx := newHexID("x")
	cfg.Pending = &fwPending{FirewallPending: helperapi.FirewallPending{ID: tx, Expires: f.clock().Add(window).Unix(), Kind: kind},
		Previous: prev, Backend: backend, Applying: true}
	if err := f.save(*cfg); err != nil {
		return nil, err
	}
	// Nothing has changed on the system yet: without a timer, nothing will.
	if err := f.schedule(ctx, tx, window); err != nil {
		return nil, errors.Join(err, f.save(f.previous(*cfg)))
	}
	actx, cancel := detached(ctx, fwApplyLimit)
	err := b.apply(actx, log, f.render(*cfg, ssh))
	cancel()
	if err == nil {
		// The change waits from now on: a connection that confirms it must
		// be younger than the rules.
		cfg.Pending.Applying = false
		cfg.Pending.Expires = f.clock().Add(window).Unix()
		err = f.save(*cfg)
	}
	if err != nil {
		if uerr := f.undo(ctx, *cfg, ssh); uerr != nil {
			// The pending change and its timer stay: the rollback is tried
			// again when the time is up.
			f.undoFailed(cfg, uerr)
			return nil, err
		}
		if serr := f.save(f.previous(*cfg)); serr != nil {
			return nil, errors.Join(err, serr)
		}
		f.unschedule(ctx, tx)
		return nil, err
	}
	p := cfg.Pending.FirewallPending
	return &p, nil
}

// previous is the configuration to store when the pending change of cfg is
// gone: the one before it, with what was learned about the SSH ports since.
func (f *Firewall) previous(cfg fwConfig) fwConfig {
	prev := cfg.Pending.Previous
	prev.Pending = nil
	prev.SSHSeen, prev.SSHKeep = cfg.SSHSeen, cfg.SSHKeep
	return prev
}

// undo puts the system back to what it was before the pending change of
// cfg: on the backend the change was applied to, which is not always the
// one in use before (switching the firewall on picks the platform's when
// the earlier one no longer runs).
func (f *Firewall) undo(ctx context.Context, cfg fwConfig, ssh []int) error {
	prev := cfg.Pending.Previous
	applied := cfg.Pending.Backend
	if applied == "" {
		// A state file from before the backend was recorded.
		if applied = prev.Backend; applied == "" {
			applied = cfg.Backend
		}
	}
	before := prev.Backend
	if before == "" {
		before = applied
	}
	ab, bb := f.backend(applied), f.backend(before)
	if ab == nil || bb == nil {
		return fwErr(helperapi.CodeFirewallBackendMissing, map[string]any{"backend": applied})
	}
	ctx, cancel := detached(ctx, fwApplyLimit)
	defer cancel()
	if !prev.Enabled || before != applied {
		if err := ab.disable(ctx, io.Discard); err != nil {
			return err
		}
	}
	if prev.Enabled {
		return bb.apply(ctx, io.Discard, f.render(prev, ssh))
	}
	return nil
}

// undoFailed records that the pending change of cfg could not be undone:
// its rules are (partly) in force, and the state must say so.
func (f *Firewall) undoFailed(cfg *fwConfig, err error) {
	failed := &helperapi.FirewallRollbackFailure{At: f.clock().Unix(), Attempts: 1, Detail: tailString(errDetail(err), 400)}
	if old := cfg.Pending.RollbackFailed; old != nil {
		failed.Attempts = old.Attempts + 1
	}
	cfg.Pending.RollbackFailed = failed
	serr := f.save(*cfg)
	if f.Log != nil {
		f.Log.Error("undoing a firewall change failed: its rules are still in force", "tx", cfg.Pending.ID, "kind", cfg.Pending.Kind,
			"attempts", failed.Attempts, "err", err, "save_err", serr)
	}
}

// schedule starts the rollback timer of a pending change.
func (f *Firewall) schedule(ctx context.Context, tx string, delay time.Duration) error {
	if !systemdRunning() {
		if f.timers == nil {
			f.timers = map[string]*time.Timer{}
		}
		f.timers[tx] = time.AfterFunc(delay, func() {
			if err := f.Rollback(context.Background(), tx); err != nil && f.Log != nil {
				f.Log.Error("firewall rollback failed", "tx", tx, "err", err)
			}
		})
		return nil
	}
	ctx, cancel := detached(ctx, fwCommandLimit)
	defer cancel()
	seconds := max(int((delay+time.Second-1)/time.Second), 1)
	_, err := queryCommand(ctx, "systemd-run", "--unit="+fwRollbackUnit+tx, "--description=Cairn firewall rollback",
		"--on-active="+strconv.Itoa(seconds), "--timer-property=AccuracySec=1s",
		"--property=CollectMode=inactive-or-failed", "--property=Type=oneshot", f.Executable, "firewall", "rollback", tx)
	if err != nil {
		return fmt.Errorf("start the rollback timer: %w", err)
	}
	return nil
}

func (f *Firewall) unschedule(ctx context.Context, tx string) {
	if t, ok := f.timers[tx]; ok {
		t.Stop()
		delete(f.timers, tx)
	}
	if systemdRunning() {
		ctx, cancel := detached(ctx, fwCommandLimit)
		defer cancel()
		_, _ = queryCommand(ctx, "systemctl", "stop", fwRollbackUnit+tx+".timer")
	}
}

// scheduled reports whether the rollback of a pending change is on its
// way: its timer waits, or the timer's service is at work.
func (f *Firewall) scheduled(ctx context.Context, tx string) bool {
	if _, ok := f.timers[tx]; ok {
		return true
	}
	if !systemdRunning() {
		return false
	}
	ctx, cancel := detached(ctx, fwCommandLimit)
	defer cancel()
	// is-active exits non-zero unless every unit is active; its output has
	// a line per unit all the same.
	out, _ := queryCommand(ctx, "systemctl", "is-active", fwRollbackUnit+tx+".timer", fwRollbackUnit+tx+".service")
	for _, state := range strings.Fields(out) {
		if state == "active" || state == "activating" {
			return true
		}
	}
	return false
}

// Confirm keeps a pending change.
func (f *Firewall) Confirm(ctx context.Context, tx string) error {
	ctx = context.WithoutCancel(ctx)
	unlock, err := f.lock()
	if err != nil {
		return err
	}
	defer unlock()
	cfg, err := f.load()
	if err != nil {
		return err
	}
	// A change whose apply was cut short is not in place as a whole, and
	// one whose rollback failed may be half undone: neither can be kept.
	if cfg.Pending == nil || cfg.Pending.ID != tx || cfg.Pending.Applying {
		return fwErr(helperapi.CodeFirewallNoPending, map[string]any{"id": tx})
	}
	if cfg.Pending.RollbackFailed != nil {
		return fwErr(helperapi.CodeFirewallRollbackFailed, map[string]any{"id": tx, "detail": cfg.Pending.RollbackFailed.Detail})
	}
	cfg.Pending = nil
	// The state first: a timer that still fires finds nothing to undo,
	// while a stopped timer with the change still pending would leave it
	// in force unconfirmed.
	if err := f.save(cfg); err != nil {
		return err
	}
	f.unschedule(ctx, tx)
	return nil
}

// Rollback undoes a pending change that was not confirmed in time. It is
// what the change's timer runs: a rollback that fails is tried again a few
// times, and stays on record when it keeps failing.
func (f *Firewall) Rollback(ctx context.Context, tx string) error {
	ctx = context.WithoutCancel(ctx)
	mayWait := true
	for try := 0; ; {
		wait, err := f.rollback(ctx, tx, false, mayWait)
		if wait > 0 {
			// Waited for once only: afterwards the change goes, whatever
			// the clock says by then.
			mayWait = false
			fwSleep(wait)
			continue
		}
		if err == nil || try >= len(fwRollbackRetry) {
			return err
		}
		fwSleep(fwRollbackRetry[try])
		try++
	}
}

// RollbackNow undoes a pending change at once, on request: the user saw
// that it does not work and does not want to wait for the timer.
func (f *Firewall) RollbackNow(ctx context.Context, tx string) error {
	_, err := f.rollback(context.WithoutCancel(ctx), tx, true, false)
	return err
}

// rollback undoes the pending change tx. The timer of a change is started
// before its rules are applied, so it fires that much before the change
// expires: with mayWait, rollback then reports how long is left instead of
// undoing anything. The change counts as rolled back only once the old
// rules are in place again; until then it stays pending, its failure on
// record.
func (f *Firewall) rollback(ctx context.Context, tx string, requested, mayWait bool) (time.Duration, error) {
	unlock, err := f.lock()
	if err != nil {
		return 0, err
	}
	defer unlock()
	cfg, err := f.load()
	if err != nil {
		return 0, err
	}
	if cfg.Pending == nil || cfg.Pending.ID != tx {
		if requested {
			return 0, fwErr(helperapi.CodeFirewallNoPending, map[string]any{"id": tx})
		}
		return 0, nil // confirmed meanwhile
	}
	if !requested {
		delete(f.timers, tx)
		if left := time.Unix(cfg.Pending.Expires, 0).Sub(f.clock()); mayWait && !cfg.Pending.Applying && left > 0 {
			return min(left, fwSetupConfirm), nil
		}
	}
	kind := cfg.Pending.Kind
	ssh := f.sshRemember(ctx, &cfg)
	if err := f.undo(ctx, cfg, ssh.ports); err != nil {
		f.undoFailed(&cfg, err)
		return 0, fwErr(helperapi.CodeFirewallRollbackFailed, map[string]any{"id": tx, "detail": cfg.Pending.RollbackFailed.Detail})
	}
	prev := f.previous(cfg)
	prev.LastRollback = &helperapi.FirewallRollback{ID: tx, At: f.clock().Unix(), Kind: kind, Requested: requested}
	// Should this fail, the change stays pending with the old rules in
	// place: the next try puts them in place once more and stores it.
	if err := f.save(prev); err != nil {
		return 0, err
	}
	if requested {
		f.unschedule(ctx, tx)
	}
	if f.Log != nil {
		if requested {
			f.Log.Info("firewall change rolled back on request", "tx", tx, "kind", kind)
		} else {
			f.Log.Warn("firewall change rolled back: it was not confirmed in time", "tx", tx, "kind", kind)
		}
	}
	return 0, nil
}

// Recover takes over a pending change whose rollback timer is gone: the
// machine was restarted before the change was confirmed (the timer is a
// transient unit, the rules are on disk and loaded at boot), or the helper
// stopped in the middle of applying it. A change that has expired, or was
// never applied as a whole, is rolled back at once; one that still has
// time gets a timer for what is left of it. It runs when the helper starts
// and once at boot (cairn-firewall-recover.service).
func (f *Firewall) Recover(ctx context.Context) error {
	ctx = context.WithoutCancel(ctx)
	unlock, err := f.lock()
	if err != nil {
		return err
	}
	cfg, err := f.load()
	if err != nil || cfg.Pending == nil {
		unlock()
		return err
	}
	tx := cfg.Pending.ID
	left := time.Unix(cfg.Pending.Expires, 0).Sub(f.clock())
	if !cfg.Pending.Applying && left > 0 {
		if !f.scheduled(ctx, tx) {
			// Never longer than a change waits: the clock may have been
			// set since the change was made.
			err = f.schedule(ctx, tx, min(left, fwSetupConfirm))
			if f.Log != nil {
				f.Log.Warn("a firewall change was left pending without its rollback timer; the timer is started again",
					"tx", tx, "kind", cfg.Pending.Kind, "seconds", int(left/time.Second), "err", err)
			}
		}
		unlock()
		return err
	}
	if f.Log != nil {
		f.Log.Warn("a firewall change was left pending past its time; rolling it back", "tx", tx, "kind", cfg.Pending.Kind,
			"interrupted", cfg.Pending.Applying)
	}
	unlock()
	return f.Rollback(ctx, tx)
}

// fwEnable is how the firewall is switched on.
type fwEnable struct {
	// confirm asks for the rollback window; window is its length (the
	// usual one when zero).
	confirm bool
	window  time.Duration
	// seedWeb opens 80 and 443 when there are no rules yet (install time).
	seedWeb bool
	// keepSSH are ports of SSH connections that must stay possible.
	keepSSH []int
}

// Enable switches the firewall on (installing the backend when needed)
// with Cairn's rules; confirm asks for the rollback window.
func (f *Firewall) Enable(ctx context.Context, c helperapi.Caller, confirm bool, log io.Writer) (*helperapi.FirewallPending, error) {
	return f.enable(ctx, c, fwEnable{confirm: confirm}, log)
}

func (f *Firewall) enable(ctx context.Context, c helperapi.Caller, o fwEnable, log io.Writer) (*helperapi.FirewallPending, error) {
	// Only a root caller (the install-time setup) goes without the
	// rollback window; the web process always gets it, whatever it asks.
	if !c.PeerRoot {
		o.confirm, o.window, o.seedWeb, o.keepSSH = true, 0, false, nil
	}
	if o.window == 0 {
		o.window = fwConfirm
	}
	unlock, err := f.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	cfg, err := f.load()
	if err != nil {
		return nil, err
	}
	if cfg.Pending != nil {
		return nil, fwErr(helperapi.CodeFirewallPending, map[string]any{"id": cfg.Pending.ID})
	}
	sys := f.detect(ctx, cfg)
	if sys.conflict != "" {
		return nil, fwErr(helperapi.CodeFirewallConflict, map[string]any{"conflict": sys.conflict})
	}
	f.active(ctx, &cfg, sys)
	prev := cfg.clone()
	b := f.backend(sys.backend)
	if !b.installed() {
		if f.Software == nil || f.Software.Manager == "" {
			return nil, fwErr(helperapi.CodeFirewallBackendMissing, map[string]any{"backend": sys.backend})
		}
		_, _ = fmt.Fprintf(log, "Installing %s from the system repositories.\n", strings.Join(b.packages(), ", "))
		if err := f.Software.ensureLibraries(ctx, log, b.packages()); err != nil {
			return nil, fwErr(helperapi.CodeFirewallBackendMissing, map[string]any{"backend": sys.backend, "detail": errDetail(err)})
		}
	}
	// From here on nothing follows the caller's context any more.
	ctx = context.WithoutCancel(ctx)
	if o.seedWeb && len(cfg.Rules) == 0 {
		cfg.Rules = append(cfg.Rules, helperapi.FirewallRule{ID: newHexID("r"), Action: helperapi.FirewallAllow,
			Protocol: helperapi.ProtoTCP, Ports: webPorts, Note: "web"})
	}
	ssh := f.sshRemember(ctx, &cfg, o.keepSSH...)
	cfg.Enabled, cfg.Backend = true, sys.backend
	if err := f.check(cfg, c.Remote, ssh.ports); err != nil {
		return nil, err
	}
	r := f.render(cfg, ssh.ports)
	_, _ = fmt.Fprintf(log, "Enabling the firewall (%s): incoming connections are denied except:\n", sys.backend)
	for _, rule := range r.Rules {
		if rule.Action == helperapi.FirewallAllow && len(rule.Sources) == 0 {
			_, _ = fmt.Fprintf(log, "  %s %s\n", strings.Join(rule.Ports, ","), rule.Protocol)
		}
	}
	switch {
	case ssh.unread != "":
		_, _ = fmt.Fprintf(log, "Warning: sshd's configuration cannot be read (%s). The SSH ports above are the ones sshd listens on now, "+
			"the ones known before and 22.\n", tailString(ssh.unread, 200))
	case len(ssh.ports) == 0:
		_, _ = fmt.Fprintln(log, "Warning: no SSH server was found on this system: no port stays open for SSH.")
	}
	p, err := f.commit(ctx, log, &cfg, prev, sys.backend, "enable", ssh.ports, o.window)
	if err != nil {
		return nil, applyErr(err, "")
	}
	if o.confirm {
		return p, nil
	}
	// No window was asked for: the change is kept at once. Until here it
	// went the same way, so that an apply cut short is still undone.
	cfg.Pending = nil
	if err := f.save(cfg); err != nil {
		return nil, err
	}
	f.unschedule(ctx, p.ID)
	return nil, nil
}

// Disable switches the firewall off.
func (f *Firewall) Disable(ctx context.Context, log io.Writer) error {
	ctx = context.WithoutCancel(ctx)
	unlock, err := f.lock()
	if err != nil {
		return err
	}
	defer unlock()
	cfg, err := f.load()
	if err != nil {
		return err
	}
	if cfg.Pending != nil {
		return fwErr(helperapi.CodeFirewallPending, map[string]any{"id": cfg.Pending.ID})
	}
	sys := f.detect(ctx, cfg)
	if sys.conflict != "" {
		return fwErr(helperapi.CodeFirewallConflict, map[string]any{"conflict": sys.conflict})
	}
	_, _ = fmt.Fprintf(log, "Disabling the firewall (%s).\n", sys.backend)
	actx, cancel := detached(ctx, fwApplyLimit)
	defer cancel()
	if err := f.backend(sys.backend).disable(actx, log); err != nil {
		return applyErr(err, "")
	}
	cfg.Enabled = false
	return f.save(cfg)
}

// SetManaged opens ports for another feature (a stream proxy, a database's
// remote access) under owner; nil ports closes them. It needs no
// confirmation: it only ever allows. Sources the user set on the rule are
// kept.
func (f *Firewall) SetManaged(ctx context.Context, log io.Writer, owner, protocol string, ports []string) error {
	ctx = context.WithoutCancel(ctx)
	unlock, err := f.lock()
	if err != nil {
		return err
	}
	defer unlock()
	cfg, err := f.load()
	if err != nil {
		return err
	}
	set := func(c *fwConfig) bool {
		i := slices.IndexFunc(c.Rules, func(r helperapi.FirewallRule) bool { return r.Owner == owner })
		switch {
		case ports == nil && i < 0:
			return false
		case ports == nil:
			c.Rules = slices.Delete(c.Rules, i, i+1)
		case i < 0:
			c.Rules = append(c.Rules, helperapi.FirewallRule{ID: newHexID("r"), Action: helperapi.FirewallAllow, Protocol: protocol,
				Ports: ports, Owner: owner})
		default:
			if c.Rules[i].Protocol == protocol && slices.Equal(c.Rules[i].Ports, ports) {
				return false
			}
			c.Rules[i].Protocol, c.Rules[i].Ports = protocol, ports
		}
		return true
	}
	if !set(&cfg) {
		return nil
	}
	if cfg.Pending != nil {
		set(&cfg.Pending.Previous)
	}
	sys := f.detect(ctx, cfg)
	on := sys.conflict == "" && f.active(ctx, &cfg, sys)
	var ssh fwSSH
	if on {
		ssh = f.sshRemember(ctx, &cfg)
	}
	if err := f.save(cfg); err != nil {
		return err
	}
	if !on {
		return nil
	}
	if ports == nil {
		_, _ = fmt.Fprintf(log, "Closing the firewall for %s.\n", owner)
	} else {
		_, _ = fmt.Fprintf(log, "Opening %s/%s in the firewall for %s.\n", strings.Join(ports, ","), protocol, owner)
	}
	var out bytes.Buffer
	actx, cancel := detached(ctx, fwApplyLimit)
	defer cancel()
	if err := f.backend(sys.backend).apply(actx, &out, f.render(cfg, ssh.ports)); err != nil {
		return applyErr(err, out.String())
	}
	return nil
}

// Rules and addresses -----------------------------------------------------

func normalizeSources(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if p, ok := helperapi.ParseSource(strings.TrimSpace(s)); ok {
			if c := helperapi.CanonicalSource(p); !slices.Contains(out, c) {
				out = append(out, c)
			}
		}
	}
	return out
}

func (f *Firewall) setRule(ctx context.Context, c helperapi.Caller, q helperapi.FirewallRule) (helperapi.FirewallChange, error) {
	q.Sources = normalizeSources(q.Sources)
	var saved helperapi.FirewallRule
	kind := "rule.add"
	if q.ID != "" {
		kind = "rule.update"
	}
	ch, err := f.change(ctx, c, kind, func(cfg *fwConfig) error {
		if q.ID == "" {
			if q.Owner != "" {
				return fwErr(helperapi.CodeFirewallManaged, nil)
			}
			q.ID = newHexID("r")
			cfg.Rules = append(cfg.Rules, q)
			saved = q
			return nil
		}
		i := slices.IndexFunc(cfg.Rules, func(r helperapi.FirewallRule) bool { return r.ID == q.ID })
		if i < 0 {
			return fwErr(helperapi.CodeFirewallNotFound, map[string]any{"id": q.ID})
		}
		if cur := cfg.Rules[i]; cur.Owner != "" {
			// Another feature's rule: only who may reach it can change.
			if len(q.Sources) == 0 && len(cur.Sources) == 0 {
				saved = cur
				return nil
			}
			cur.Sources = q.Sources
			cfg.Rules[i] = cur
			saved = cur
			return nil
		}
		q.Owner = ""
		cfg.Rules[i] = q
		saved = q
		return nil
	})
	if err != nil {
		return ch, err
	}
	ch.Rule = &saved
	return ch, nil
}

func (f *Firewall) deleteRule(ctx context.Context, c helperapi.Caller, q helperapi.FirewallRef) (helperapi.FirewallChange, error) {
	return f.change(ctx, c, "rule.delete", func(cfg *fwConfig) error {
		i := slices.IndexFunc(cfg.Rules, func(r helperapi.FirewallRule) bool { return r.ID == q.ID })
		if i < 0 {
			return fwErr(helperapi.CodeFirewallNotFound, map[string]any{"id": q.ID})
		}
		if cfg.Rules[i].Owner != "" {
			return fwErr(helperapi.CodeFirewallManaged, map[string]any{"owner": cfg.Rules[i].Owner})
		}
		cfg.Rules = slices.Delete(cfg.Rules, i, i+1)
		return nil
	})
}

func (f *Firewall) setAddress(ctx context.Context, c helperapi.Caller, q helperapi.FirewallAddress) (helperapi.FirewallChange, error) {
	p, _ := helperapi.ParseSource(strings.TrimSpace(q.Address))
	q.Address = helperapi.CanonicalSource(p)
	q.ID = newHexID("a")
	ch, err := f.change(ctx, c, "address.add", func(cfg *fwConfig) error {
		cfg.Addresses = append(cfg.Addresses, q)
		return nil
	})
	if err != nil {
		return ch, err
	}
	ch.Address = &q
	return ch, nil
}

func (f *Firewall) deleteAddress(ctx context.Context, c helperapi.Caller, q helperapi.FirewallRef) (helperapi.FirewallChange, error) {
	return f.change(ctx, c, "address.delete", func(cfg *fwConfig) error {
		i := slices.IndexFunc(cfg.Addresses, func(a helperapi.FirewallAddress) bool { return a.ID == q.ID })
		if i < 0 {
			return fwErr(helperapi.CodeFirewallNotFound, map[string]any{"id": q.ID})
		}
		cfg.Addresses = slices.Delete(cfg.Addresses, i, i+1)
		return nil
	})
}

// Status ------------------------------------------------------------------

// Status describes the firewall.
func (f *Firewall) Status(ctx context.Context) helperapi.FirewallStatus {
	f.mu.Lock()
	cfg, err := f.load()
	f.mu.Unlock()
	if err != nil && f.Log != nil {
		f.Log.Warn("reading the firewall state failed", "err", err)
	}
	ssh := f.sshKept(ctx, cfg)
	st := helperapi.FirewallStatus{Systemd: systemdRunning(), BlockPing: cfg.BlockPing, PanelPort: f.PanelPort,
		SSHPorts: ssh.ports, SSHUnread: ssh.unread != "", Rules: []helperapi.FirewallRule{}, Addresses: []helperapi.FirewallAddress{},
		External: []string{}, LastRollback: cfg.LastRollback}
	if st.SSHPorts == nil {
		st.SSHPorts = []int{}
	}
	sys := f.detect(ctx, cfg)
	st.Conflict = sys.conflict
	st.Rules = append(append(st.Rules, f.protected(ssh.ports)...), cfg.Rules...)
	st.Addresses = append(st.Addresses, cfg.Addresses...)
	st.Forwards = append([]helperapi.FirewallForward{}, cfg.Forwards...)
	st.IPForward = f.ipForwardOn()
	if cfg.Pending != nil {
		p := cfg.Pending.FirewallPending
		st.Pending = &p
	}
	if sys.conflict != "" {
		return st
	}
	b := f.backend(sys.backend)
	st.Backend = sys.backend
	st.Installed = b.installed()
	st.Enabled = f.active(ctx, &cfg, sys)
	if st.Installed && st.Enabled {
		// Two questions to the backend's own tool, which may take a second
		// each to answer (firewall-cmd): asked side by side, since the
		// overview waits for this status with a time limit.
		var ext []string
		done := make(chan struct{})
		go func() {
			defer close(done)
			ext = b.external(ctx)
		}()
		st.Loaded = b.loaded(ctx)
		<-done
		if ext != nil {
			st.External = ext
		}
	}
	return st
}

// Install-time setup --------------------------------------------------------

// FirewallPlan is what setting up the firewall at install time would do.
type FirewallPlan struct {
	Backend string
	Active  bool
	Install []string
	Ports   []string // "22/tcp"
	// SSH are the ports that stay open for SSH. With SSHUnread they are
	// partly a guess: sshd's configuration could not be read.
	SSH       []int
	SSHUnread bool
	// Session are the server ports of the SSH connections the plan was
	// asked from; they are among SSH whatever the detection found.
	Session []int
	// Closing lists what other hosts can connect to now and will not be
	// able to any more ("25/tcp(master)"); empty when a firewall already
	// runs, which closed what it closed before.
	Closing []string
	// Unattended reports whether the firewall may be switched on with
	// nobody there to check that SSH still works: the SSH connection in
	// use is known, or the SSH ports were found for certain. Reason says
	// what is missing otherwise (ssh_unread, ssh_unknown).
	Unattended bool
	Reason     string
}

// FirewallSetup is how the install-time setup switches the firewall on.
type FirewallSetup struct {
	// SSHPorts are server ports of SSH connections that must stay
	// possible: the installer's own (the fourth field of SSH_CONNECTION).
	SSHPorts []int
	// Confirm keeps the rollback timer running until Confirm is called,
	// for a person who checks a new SSH connection first.
	Confirm bool
}

// webPorts are opened at install time for the sites the panel serves.
var webPorts = []string{"80", "443"}

// sessionPorts are the SSH ports this process was reached through: the
// ones it was told, and the ones its ancestors hold connections on.
func (f *Firewall) sessionPorts(told []int) []int {
	return addPorts(sshSessionPorts(f.Root, os.Getpid()), told...)
}

// opens reports whether a rule lets everyone connect to a port.
func opens(r helperapi.FirewallRule, port int, udp bool) bool {
	if r.Action != helperapi.FirewallAllow || len(r.Sources) > 0 || r.Protocol == helperapi.ProtoUDP && !udp || r.Protocol == helperapi.ProtoTCP && udp {
		return false
	}
	for _, p := range r.Ports {
		if lo, hi, ok := helperapi.ParsePortRange(p); ok && lo <= port && port <= hi {
			return true
		}
	}
	return false
}

// Plan describes the install-time setup; sshPorts are the SSH ports the
// installer knows it is connected through.
func (f *Firewall) Plan(ctx context.Context, sshPorts []int) (FirewallPlan, error) {
	cfg, err := f.load()
	if err != nil {
		return FirewallPlan{}, err
	}
	sys := f.detect(ctx, cfg)
	if sys.conflict != "" {
		return FirewallPlan{}, fwErr(helperapi.CodeFirewallConflict, map[string]any{"conflict": sys.conflict})
	}
	plan := FirewallPlan{Backend: sys.backend, Active: sys.active, Session: f.sessionPorts(sshPorts)}
	if b := f.backend(sys.backend); !b.installed() {
		plan.Install = b.packages()
	}
	// What Setup would do, without storing anything.
	d := sshDetect(ctx, f.Root)
	for _, p := range plan.Session {
		if !slices.Contains(d.Ports, p) {
			cfg.SSHKeep = addPorts(slices.Clone(cfg.SSHKeep), p)
		}
	}
	ssh := f.sshView(d, cfg)
	plan.SSH, plan.SSHUnread = ssh.ports, ssh.unread != ""
	rules := f.protected(ssh.ports)
	if len(cfg.Rules) == 0 {
		rules = append(rules, helperapi.FirewallRule{Action: helperapi.FirewallAllow, Protocol: helperapi.ProtoTCP, Ports: webPorts})
	}
	rules = append(rules, cfg.Rules...)
	for _, r := range rules {
		if r.Action != helperapi.FirewallAllow || len(r.Sources) > 0 {
			continue
		}
		for _, proto := range protocols(r.Protocol) {
			for _, p := range r.Ports {
				plan.Ports = append(plan.Ports, p+"/"+proto)
			}
		}
	}
	if !sys.active {
		for _, l := range exposedListeners(f.Root) {
			if slices.ContainsFunc(rules, func(r helperapi.FirewallRule) bool { return opens(r, l.Port, l.UDP) }) {
				continue
			}
			item := strconv.Itoa(l.Port) + "/" + helperapi.ProtoTCP
			if l.UDP {
				item = strconv.Itoa(l.Port) + "/" + helperapi.ProtoUDP
			}
			if name := plainName(l.Process); name != "" {
				item += "(" + name + ")"
			}
			plan.Closing = append(plan.Closing, item)
		}
	}
	switch {
	case len(plan.Session) > 0:
		plan.Unattended = true
	case ssh.unread != "":
		plan.Reason = "ssh_unread"
	case len(ssh.ports) == 0:
		plan.Reason = "ssh_unknown"
	default:
		plan.Unattended = true
	}
	return plan, nil
}

// plainName keeps the characters of a process name that are safe to print
// in a line a shell script takes apart.
func plainName(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-', r == '+':
			return r
		}
		return '_'
	}, s)
}

// Setup enables the firewall at install time: the protected ports plus 80
// and 443 (rules Cairn already has are left as they are). The SSH
// connections the installer runs in stay possible whatever the detection
// of the SSH ports finds. Without o.Confirm the firewall stays on at once
// (nil is returned); with it, the pending change is returned and rolled
// back unless Confirm is called in time.
func (f *Firewall) Setup(ctx context.Context, log io.Writer, o FirewallSetup) (*helperapi.FirewallPending, error) {
	e := fwEnable{confirm: o.Confirm, seedWeb: true, keepSSH: f.sessionPorts(o.SSHPorts)}
	if o.Confirm {
		e.window = fwSetupConfirm
	}
	return f.enable(ctx, helperapi.Caller{Source: "cli", PeerRoot: true}, e, log)
}

// forwardsTCP reports whether a port forward takes TCP connections on
// port (while the firewall is on; one configured now would be on later).
func (f *Firewall) forwardsTCP(port int) bool {
	f.mu.Lock()
	cfg, _ := f.load()
	f.mu.Unlock()
	return slices.ContainsFunc(cfg.Forwards, func(fw helperapi.FirewallForward) bool {
		return fw.Port == port && fw.Protocol != helperapi.ProtoUDP
	})
}

// ActiveBackend names the backend enforcing rules now ("" when off).
func (f *Firewall) ActiveBackend(ctx context.Context) string {
	f.mu.Lock()
	cfg, _ := f.load()
	f.mu.Unlock()
	sys := f.detect(ctx, cfg)
	if sys.conflict != "" || !f.active(ctx, &cfg, sys) {
		return ""
	}
	return sys.backend
}

// Refresh re-applies the configuration while the firewall is on, after
// the protected ports changed (the SSH port).
func (f *Firewall) Refresh(ctx context.Context, log io.Writer) error {
	ctx = context.WithoutCancel(ctx)
	unlock, err := f.lock()
	if err != nil {
		return err
	}
	defer unlock()
	cfg, err := f.load()
	if err != nil {
		return err
	}
	sys := f.detect(ctx, cfg)
	if sys.conflict != "" || !f.active(ctx, &cfg, sys) {
		return nil
	}
	seen, keep := cfg.SSHSeen, cfg.SSHKeep
	ssh := f.sshRemember(ctx, &cfg)
	_, _ = fmt.Fprintf(log, "Updating the firewall (%s) for the SSH ports.\n", sys.backend)
	var out bytes.Buffer
	actx, cancel := detached(ctx, fwApplyLimit)
	defer cancel()
	if err := f.backend(sys.backend).apply(actx, &out, f.render(cfg, ssh.ports)); err != nil {
		return applyErr(err, out.String())
	}
	if slices.Equal(seen, cfg.SSHSeen) && slices.Equal(keep, cfg.SSHKeep) {
		return nil
	}
	return f.save(cfg)
}
