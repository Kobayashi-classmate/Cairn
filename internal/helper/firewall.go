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
// The SSH ports (read with sshd -T) and the panel port are always
// allowed. A change from the panel is rolled back after 60 seconds unless
// it is confirmed; the rollback runs in a timer of its own, so it happens
// even if the helper stops.
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
}

// fwPending is a change waiting for confirmation, with the configuration
// to go back to.
type fwPending struct {
	helperapi.FirewallPending
	Previous fwConfig `json:"previous"`
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
	fwCommandLimit = 2 * time.Minute
)

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

// sshPorts reads the ports sshd listens on; when sshd is installed but its
// configuration cannot be read, port 22 stays open to be safe.
var sshPorts = func(ctx context.Context) []int {
	if _, err := os.Stat("/usr/sbin/sshd"); err != nil {
		return nil
	}
	out, err := queryCommand(ctx, "/usr/sbin/sshd", "-T")
	if err != nil {
		return []int{22}
	}
	ports := parseSSHDPorts(out)
	if len(ports) == 0 {
		return []int{22}
	}
	return ports
}

func parseSSHDPorts(out string) []int {
	var ports []int
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok || k != "port" {
			continue
		}
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 && n < 65536 && !slices.Contains(ports, n) {
			ports = append(ports, n)
		}
	}
	slices.Sort(ports)
	return ports
}

func portStrings(ports []int) []string {
	out := make([]string, len(ports))
	for i, p := range ports {
		out[i] = strconv.Itoa(p)
	}
	return out
}

// protected returns the rules that are always there.
func (f *Firewall) protected(ctx context.Context) []helperapi.FirewallRule {
	var rules []helperapi.FirewallRule
	if ports := sshPorts(ctx); len(ports) > 0 {
		rules = append(rules, helperapi.FirewallRule{ID: helperapi.OwnerSSH, Action: helperapi.FirewallAllow, Protocol: helperapi.ProtoTCP,
			Ports: portStrings(ports), Owner: helperapi.OwnerSSH})
	}
	if f.PanelPort > 0 {
		rules = append(rules, helperapi.FirewallRule{ID: helperapi.OwnerPanel, Action: helperapi.FirewallAllow, Protocol: helperapi.ProtoTCP,
			Ports: []string{strconv.Itoa(f.PanelPort)}, Owner: helperapi.OwnerPanel})
	}
	return rules
}

func (f *Firewall) render(ctx context.Context, cfg fwConfig) fwRender {
	return fwRender{Rules: append(f.protected(ctx), cfg.Rules...), Addresses: cfg.Addresses, BlockPing: cfg.BlockPing, Forwards: cfg.Forwards}
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
func (f *Firewall) check(ctx context.Context, cfg fwConfig, remote string) error {
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
	if err := checkForwards(cfg.Forwards, append(sshPorts(ctx), f.PanelPort)); err != nil {
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
	keep := append(portStrings(sshPorts(ctx)), strconv.Itoa(f.PanelPort))
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

// change applies a change to the configuration. While the firewall is on,
// the change takes effect at once and waits for confirmation; while it is
// off, it is only stored.
func (f *Firewall) change(ctx context.Context, c helperapi.Caller, kind string, mutate func(*fwConfig) error) (helperapi.FirewallChange, error) {
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
	if err := f.check(ctx, cfg, c.Remote); err != nil {
		return helperapi.FirewallChange{}, err
	}
	if !on {
		return helperapi.FirewallChange{}, f.save(cfg)
	}
	var out bytes.Buffer
	if err := f.backend(sys.backend).apply(ctx, &out, f.render(ctx, cfg)); err != nil {
		f.restore(ctx, prev, sys.backend)
		return helperapi.FirewallChange{}, applyErr(err, out.String())
	}
	p, err := f.hold(ctx, &cfg, prev, kind)
	if err != nil {
		return helperapi.FirewallChange{}, err
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

// restore puts the previous configuration back after a failed change.
func (f *Firewall) restore(ctx context.Context, prev fwConfig, backend string) {
	b := f.backend(backend)
	var err error
	if prev.Enabled {
		err = b.apply(ctx, io.Discard, f.render(ctx, prev))
	} else {
		err = b.disable(ctx, io.Discard)
	}
	if err != nil && f.Log != nil {
		f.Log.Error("restoring the previous firewall configuration failed", "err", err)
	}
}

// hold stores the new configuration with a pending confirmation and
// schedules its rollback.
func (f *Firewall) hold(ctx context.Context, cfg *fwConfig, prev fwConfig, kind string) (*helperapi.FirewallPending, error) {
	p := helperapi.FirewallPending{ID: newHexID("x"), Expires: f.clock().Add(fwConfirm).Unix(), Kind: kind}
	cfg.Pending = &fwPending{FirewallPending: p, Previous: prev}
	if err := f.save(*cfg); err != nil {
		return nil, err
	}
	if err := f.schedule(ctx, p.ID); err != nil {
		// Without a rollback timer the change cannot stay: undo it now.
		f.restore(ctx, prev, cfg.Backend)
		prev.Pending = nil
		_ = f.save(prev)
		return nil, err
	}
	return &p, nil
}

// schedule starts the rollback timer of a pending change.
func (f *Firewall) schedule(ctx context.Context, tx string) error {
	if !systemdRunning() {
		if f.timers == nil {
			f.timers = map[string]*time.Timer{}
		}
		f.timers[tx] = time.AfterFunc(fwConfirm, func() {
			if err := f.Rollback(context.Background(), tx); err != nil && f.Log != nil {
				f.Log.Error("firewall rollback failed", "tx", tx, "err", err)
			}
		})
		return nil
	}
	_, err := queryCommand(ctx, "systemd-run", "--unit="+fwRollbackUnit+tx, "--description=Cairn firewall rollback",
		"--on-active="+strconv.Itoa(helperapi.FirewallConfirmSeconds), "--timer-property=AccuracySec=1s",
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
		_, _ = queryCommand(ctx, "systemctl", "stop", fwRollbackUnit+tx+".timer")
	}
}

// Confirm keeps a pending change.
func (f *Firewall) Confirm(ctx context.Context, tx string) error {
	unlock, err := f.lock()
	if err != nil {
		return err
	}
	defer unlock()
	cfg, err := f.load()
	if err != nil {
		return err
	}
	if cfg.Pending == nil || cfg.Pending.ID != tx {
		return fwErr(helperapi.CodeFirewallNoPending, map[string]any{"id": tx})
	}
	f.unschedule(ctx, tx)
	cfg.Pending = nil
	return f.save(cfg)
}

// Rollback undoes a pending change that was not confirmed in time.
func (f *Firewall) Rollback(ctx context.Context, tx string) error {
	return f.rollback(ctx, tx, false)
}

// RollbackNow undoes a pending change at once, on request: the user saw
// that it does not work and does not want to wait for the timer.
func (f *Firewall) RollbackNow(ctx context.Context, tx string) error {
	return f.rollback(ctx, tx, true)
}

func (f *Firewall) rollback(ctx context.Context, tx string, requested bool) error {
	unlock, err := f.lock()
	if err != nil {
		return err
	}
	defer unlock()
	cfg, err := f.load()
	if err != nil {
		return err
	}
	if cfg.Pending == nil || cfg.Pending.ID != tx {
		if requested {
			return fwErr(helperapi.CodeFirewallNoPending, map[string]any{"id": tx})
		}
		return nil // confirmed meanwhile
	}
	if requested {
		f.unschedule(ctx, tx)
	} else {
		delete(f.timers, tx)
	}
	prev := cfg.Pending.Previous
	backend := cfg.Backend
	if prev.Backend != "" {
		backend = prev.Backend
	}
	b := f.backend(backend)
	if prev.Enabled {
		err = b.apply(ctx, io.Discard, f.render(ctx, prev))
	} else {
		err = b.disable(ctx, io.Discard)
	}
	prev.Pending = nil
	prev.LastRollback = &helperapi.FirewallRollback{ID: tx, At: f.clock().Unix(), Kind: cfg.Pending.Kind, Requested: requested}
	if serr := f.save(prev); serr != nil {
		return errors.Join(err, serr)
	}
	if f.Log != nil {
		if requested {
			f.Log.Info("firewall change rolled back on request", "tx", tx, "kind", prev.LastRollback.Kind)
		} else {
			f.Log.Warn("firewall change rolled back: it was not confirmed in time", "tx", tx, "kind", prev.LastRollback.Kind)
		}
	}
	return err
}

// Enable switches the firewall on (installing the backend when needed)
// with Cairn's rules; confirm asks for the rollback window.
func (f *Firewall) Enable(ctx context.Context, c helperapi.Caller, confirm bool, log io.Writer) (*helperapi.FirewallPending, error) {
	// Only a root caller (the install-time setup) goes without the
	// rollback window; the web process always gets it, whatever it asks.
	if !c.PeerRoot {
		confirm = true
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
	cfg.Enabled, cfg.Backend = true, sys.backend
	if err := f.check(ctx, cfg, c.Remote); err != nil {
		return nil, err
	}
	r := f.render(ctx, cfg)
	_, _ = fmt.Fprintf(log, "Enabling the firewall (%s): incoming connections are denied except:\n", sys.backend)
	for _, rule := range r.Rules {
		if rule.Action == helperapi.FirewallAllow && len(rule.Sources) == 0 {
			_, _ = fmt.Fprintf(log, "  %s %s\n", strings.Join(rule.Ports, ","), rule.Protocol)
		}
	}
	if err := b.apply(ctx, log, r); err != nil {
		f.restore(ctx, prev, sys.backend)
		return nil, applyErr(err, "")
	}
	if !confirm {
		return nil, f.save(cfg)
	}
	return f.hold(ctx, &cfg, prev, "enable")
}

// Disable switches the firewall off.
func (f *Firewall) Disable(ctx context.Context, log io.Writer) error {
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
	if err := f.backend(sys.backend).disable(ctx, log); err != nil {
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
	if err := f.backend(sys.backend).apply(ctx, &out, f.render(ctx, cfg)); err != nil {
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
	st := helperapi.FirewallStatus{Systemd: systemdRunning(), BlockPing: cfg.BlockPing, PanelPort: f.PanelPort,
		SSHPorts: sshPorts(ctx), Rules: []helperapi.FirewallRule{}, Addresses: []helperapi.FirewallAddress{}, External: []string{},
		LastRollback: cfg.LastRollback}
	if st.SSHPorts == nil {
		st.SSHPorts = []int{}
	}
	sys := f.detect(ctx, cfg)
	st.Conflict = sys.conflict
	st.Rules = append(append(st.Rules, f.protected(ctx)...), cfg.Rules...)
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
		st.Loaded = b.loaded(ctx)
		if ext := b.external(ctx); ext != nil {
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
}

// webPorts are opened at install time for the sites the panel serves.
var webPorts = []string{"80", "443"}

// Plan describes the install-time setup.
func (f *Firewall) Plan(ctx context.Context) (FirewallPlan, error) {
	cfg, err := f.load()
	if err != nil {
		return FirewallPlan{}, err
	}
	sys := f.detect(ctx, cfg)
	if sys.conflict != "" {
		return FirewallPlan{}, fwErr(helperapi.CodeFirewallConflict, map[string]any{"conflict": sys.conflict})
	}
	plan := FirewallPlan{Backend: sys.backend, Active: sys.active}
	if b := f.backend(sys.backend); !b.installed() {
		plan.Install = b.packages()
	}
	for _, r := range f.protected(ctx) {
		for _, p := range r.Ports {
			plan.Ports = append(plan.Ports, p+"/tcp")
		}
	}
	for _, p := range webPorts {
		plan.Ports = append(plan.Ports, p+"/tcp")
	}
	return plan, nil
}

// Setup enables the firewall at install time: the protected ports plus
// 80 and 443, no confirmation. A firewall Cairn already manages is left
// as it is.
func (f *Firewall) Setup(ctx context.Context, log io.Writer) error {
	cfg, err := f.load()
	if err != nil {
		return err
	}
	if len(cfg.Rules) == 0 {
		cfg.Rules = append(cfg.Rules, helperapi.FirewallRule{ID: newHexID("r"), Action: helperapi.FirewallAllow,
			Protocol: helperapi.ProtoTCP, Ports: webPorts, Note: "web"})
		f.mu.Lock()
		err := f.save(cfg)
		f.mu.Unlock()
		if err != nil {
			return err
		}
	}
	_, err = f.Enable(ctx, helperapi.Caller{Source: "cli", PeerRoot: true}, false, log)
	return err
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
	_, _ = fmt.Fprintf(log, "Updating the firewall (%s) for the SSH ports.\n", sys.backend)
	var out bytes.Buffer
	if err := f.backend(sys.backend).apply(ctx, &out, f.render(ctx, cfg)); err != nil {
		return applyErr(err, out.String())
	}
	return nil
}
