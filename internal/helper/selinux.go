// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// SELinux (EL): sites keep the policy's own types instead of switching
// anything off.
//   - /srv/www is httpd_sys_content_t, so nginx and PHP-FPM (both httpd_t)
//     read the sites; a site's tmp/ and its writable directories are
//     httpd_sys_rw_content_t. The ACME webroot is readable by nginx too.
//   - A port type is system-wide and decides which service may bind the
//     port, so Cairn never changes a type a port already has: a stream
//     forwarding to another machine's 3306 must not take this machine's
//     3306 away from MariaDB.
//   - A port nginx listens on (a stream's own port) needs http_port_t. A
//     port without a type of its own gets it; one that belongs to another
//     service is refused.
//   - A port nginx only connects to (an app's port, a proxy's or a
//     stream's upstream port) keeps its type. Without a type of its own it
//     gets http_port_t, which takes nothing from anyone; with one (3306 is
//     mysqld_port_t) the policy module cairn_connect lets httpd_t connect
//     to ports of that type. That is narrower than the
//     httpd_can_network_connect boolean, which lets nginx and every PHP
//     site connect to any port. UDP has no connect permission, so UDP
//     upstreams need nothing.
//   - httpd_graceful_shutdown lets httpd_t connect to http_port_t at all.
//
// The label changes go through "semanage import" in one transaction; what
// Cairn added is recorded so that only its own rules are ever removed.

const (
	seStateFile = "/var/lib/cairn-helper/selinux.json"
	sePortType  = "http_port_t"
	seRWType    = "httpd_sys_rw_content_t"
	seContent   = "httpd_sys_content_t"
	seSemanage  = "/usr/sbin/semanage"
	seSemodule  = "/usr/sbin/semodule"
	// seModule is the policy module with the connect permissions.
	seModule = "cairn_connect"
	// seConnectBool: see seConnectBoolean.
	seConnectBool = "httpd_graceful_shutdown"
	seRestorecon  = "/usr/sbin/restorecon"
	// seStateVersion 1: ports nginx only connects to are no longer
	// relabeled (seUpgrade repairs what earlier versions did).
	seStateVersion = 1
)

var seMu sync.Mutex

// seTypeName is what a type written into the policy module must look like.
var seTypeName = regexp.MustCompile(`^[a-z][a-z0-9_]*_t$`)

// sePort is one port Cairn needs (by owner) or labeled (Labeled).
type sePort struct {
	Proto string `json:"proto"`
	Port  int    `json:"port"`
	// Bind: nginx listens on the port; otherwise it only connects to it.
	Bind bool `json:"bind,omitempty"`
	// Modified: an earlier version overrode the port's type with
	// http_port_t (semanage port -m). Orig is the type to put back when the
	// label goes; when it is empty, deleting the local rule brings back the
	// policy's own type.
	Modified bool   `json:"modified,omitempty"`
	Orig     string `json:"orig,omitempty"`
}

// seState records Cairn's SELinux rules: ports by owner ("site:<code>",
// "stream:<code>"), the ports it labeled, the types in the connect module,
// file context rules by site, and whether the shared rules (sites, ACME
// webroot) are in place.
type seState struct {
	Version   int                 `json:"version,omitempty"`
	Base      bool                `json:"base"`
	Ports     map[string][]sePort `json:"ports"`
	Labeled   []sePort            `json:"labeled"`
	Connect   []string            `json:"connect,omitempty"`
	FContexts map[string][]string `json:"fcontexts"`
}

// selinuxOn reports whether SELinux is enforcing or permissive (labels
// matter in both).
var selinuxOn = func(ctx context.Context) bool {
	out, err := queryCommand(ctx, "getenforce")
	if err != nil {
		return false
	}
	v := strings.TrimSpace(out)
	return v == "Enforcing" || v == "Permissive"
}

func (st *Sites) seLoad() seState {
	s := seState{Ports: map[string][]sePort{}, FContexts: map[string][]string{}}
	if data, err := os.ReadFile(st.path(seStateFile)); err == nil {
		_ = json.Unmarshal(data, &s)
	}
	if s.Ports == nil {
		s.Ports = map[string][]sePort{}
	}
	if s.FContexts == nil {
		s.FContexts = map[string][]string{}
	}
	return s
}

func (st *Sites) seSave(s seState) error {
	data, _ := json.MarshalIndent(s, "", "  ")
	return writePrivate(st.path(seStateFile), data)
}

// seTools installs semanage when it is missing.
func (st *Sites) seTools(ctx context.Context, log io.Writer) error {
	if fileExists(st.path(seSemanage)) {
		return nil
	}
	if st.Software == nil || st.Software.Manager != "dnf" {
		return siteErr(helperapi.CodeSiteSELinux, map[string]any{"detail": "semanage is missing"})
	}
	_, _ = fmt.Fprintf(log, "Installing the SELinux management tools.\n")
	return st.Software.ensureLibraries(ctx, log, []string{"policycoreutils-python-utils"})
}

// seLocal reads the local (non-policy) rules: file context patterns and
// "proto/port" keys, so that an add never repeats a rule and a delete
// never misses one (semanage import stops at the first failing line).
func (st *Sites) seLocal(ctx context.Context) (fcontexts, ports map[string]bool) {
	fcontexts, ports = map[string]bool{}, map[string]bool{}
	if out, err := queryCommand(ctx, seSemanage, "fcontext", "-l", "-C", "-n"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			if f := strings.Fields(line); len(f) > 0 {
				fcontexts[f[0]] = true
			}
		}
	}
	if out, err := queryCommand(ctx, seSemanage, "port", "-l", "-C", "-n"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			f := strings.Fields(strings.ReplaceAll(line, ",", " "))
			if len(f) < 3 {
				continue
			}
			for _, p := range f[2:] {
				ports[f[1]+"/"+p] = true
			}
		}
	}
	return fcontexts, ports
}

// seFilter drops the lines that would fail or do something else than
// meant: adds of rules already there (semanage turns a port add on a port
// it knows into a silent modify), deletes and modifies of rules that are
// not.
func (st *Sites) seFilter(ctx context.Context, lines []string) []string {
	fc, ports := st.seLocal(ctx)
	var out []string
	for _, l := range lines {
		f := strings.Fields(l)
		switch {
		case len(f) >= 3 && f[0] == "fcontext":
			pattern := strings.Trim(f[len(f)-1], "'")
			if (f[1] == "-a") == fc[pattern] {
				continue
			}
		case len(f) >= 5 && f[0] == "port" && f[1] == "-d":
			if !ports[f[3]+"/"+f[4]] {
				continue
			}
		case len(f) >= 7 && f[0] == "port" && f[1] == "-a":
			if ports[f[5]+"/"+f[6]] {
				continue
			}
		case len(f) >= 7 && f[0] == "port" && f[1] == "-m":
			if !ports[f[5]+"/"+f[6]] {
				continue
			}
		}
		out = append(out, l)
	}
	return out
}

// seImport runs semanage commands in one transaction.
func (st *Sites) seImport(ctx context.Context, log io.Writer, lines []string) error {
	lines = st.seFilter(ctx, lines)
	if len(lines) == 0 {
		return nil
	}
	f, err := os.CreateTemp("", "cairn-semanage-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err := f.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	for _, l := range lines {
		_, _ = fmt.Fprintf(log, "SELinux: %s\n", l)
	}
	if err := runCommand(ctx, io.Discard, seSemanage, "import", "-f", f.Name()); err != nil {
		return siteErr(helperapi.CodeSiteSELinux, map[string]any{"detail": errDetail(err)})
	}
	return nil
}

// sePortTypes are the port types as semanage lists them, by "proto/port".
type sePortTypes struct {
	// local are the local rules ("semanage port -l -C"), any type.
	local map[string]string
	// policy are the policy's own types, without the generic range types
	// and without what a local rule overrides them with.
	policy map[string]string
}

// effective is the type the port has now: a local rule overrides the
// policy's. "" means only a generic range type covers the port.
func (t sePortTypes) effective(k string) string {
	if v, ok := t.local[k]; ok {
		return v
	}
	return t.policy[k]
}

// sePortList calls fn for every "proto/port" of a "semanage port -l -n"
// listing, with its type; ranges wider than 4096 ports are left out.
func sePortList(out string, fn func(typ, key string)) {
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(strings.ReplaceAll(line, ",", " "))
		if len(f) < 3 || (f[1] != "tcp" && f[1] != "udp") {
			continue
		}
		for _, p := range f[2:] {
			lo, hi, ok := helperapi.ParsePortRange(p)
			if !ok || hi-lo > 4096 {
				continue
			}
			for n := lo; n <= hi; n++ {
				fn(f[0], f[1]+"/"+strconv.Itoa(n))
			}
		}
	}
}

// sePortTypes reads the TCP and UDP port types, local and the policy's.
func (st *Sites) sePortTypes(ctx context.Context) (sePortTypes, error) {
	t := sePortTypes{local: map[string]string{}, policy: map[string]string{}}
	local, err := queryCommand(ctx, seSemanage, "port", "-l", "-C", "-n")
	if err != nil {
		return t, siteErr(helperapi.CodeSiteSELinux, map[string]any{"detail": errDetail(err)})
	}
	sePortList(local, func(typ, key string) { t.local[key] = typ })
	all, err := queryCommand(ctx, seSemanage, "port", "-l", "-n")
	if err != nil {
		return t, siteErr(helperapi.CodeSiteSELinux, map[string]any{"detail": errDetail(err)})
	}
	sePortList(all, func(typ, key string) {
		// The full listing merges the local rules in.
		if slices.Contains(genericPortTypes, typ) || t.local[key] == typ {
			return
		}
		if t.policy[key] != sePortType {
			t.policy[key] = typ
		}
	})
	return t, nil
}

func portKey(p sePort) string { return p.Proto + "/" + strconv.Itoa(p.Port) }

// seRefuse refuses the ports an owner asks for that SELinux reserves for
// another service: nginx cannot listen on them without taking them from
// that service. A local rule with a generic type is refused for connecting
// too, since allowing it would open a whole range.
func seRefuse(types sePortTypes, ports []sePort) error {
	for _, p := range ports {
		t := types.effective(portKey(p))
		switch {
		case t == "" || t == sePortType:
		case p.Bind:
			return siteErr(helperapi.CodeStreamSELinuxPort, map[string]any{"port": p.Port, "protocol": p.Proto, "type": t})
		case p.Proto == helperapi.StreamTCP && (slices.Contains(genericPortTypes, t) || !seTypeName.MatchString(t)):
			return siteErr(helperapi.CodeSiteSELinux, map[string]any{
				"detail": fmt.Sprintf("port %s/%d has the local type %s, which nginx is not allowed to connect to", p.Proto, p.Port, t)})
		}
	}
	return nil
}

// seSetPorts gives owner exactly ports (nil releases them) and brings the
// labels and the connect module to what all owners need.
func (st *Sites) seSetPorts(ctx context.Context, log io.Writer, owner string, ports []sePort) error {
	if !selinuxOn(ctx) {
		return nil
	}
	seMu.Lock()
	defer seMu.Unlock()
	if err := st.seTools(ctx, log); err != nil {
		return err
	}
	s := st.seLoad()
	if s.Version < seStateVersion {
		if err := st.seUpgrade(ctx, log, &s); err != nil {
			return err
		}
	}
	types, err := st.sePortTypes(ctx)
	if err != nil {
		return err
	}
	if err := seRefuse(types, ports); err != nil {
		return err
	}
	if len(ports) == 0 {
		delete(s.Ports, owner)
	} else {
		s.Ports[owner] = ports
	}
	return st.seApplyPorts(ctx, log, s, types)
}

// seNeeded merges what every owner needs, by "proto/port"; a port binds
// when any owner listens on it.
func seNeeded(s seState) map[string]sePort {
	needed := map[string]sePort{}
	for _, ps := range s.Ports {
		for _, p := range ps {
			k := portKey(p)
			q, ok := needed[k]
			if !ok {
				q = sePort{Proto: p.Proto, Port: p.Port}
			}
			q.Bind = q.Bind || p.Bind
			needed[k] = q
		}
	}
	return needed
}

// seApplyPorts labels the ports that need http_port_t and have no type,
// releases Cairn's labels nothing needs any more (putting back what an
// earlier version overrode), loads the connect module for the other types
// and saves the state.
func (st *Sites) seApplyPorts(ctx context.Context, log io.Writer, s seState, types sePortTypes) error {
	needed := seNeeded(s)
	var lines []string
	var kept []sePort
	for _, p := range s.Labeled {
		if _, ok := needed[portKey(p)]; ok {
			kept = append(kept, p)
			continue
		}
		lines = append(lines, seReleaseLine(log, p, types))
	}
	keys := make([]string, 0, len(needed))
	for k := range needed {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var connect []string
	for _, k := range keys {
		p := needed[k]
		if slices.ContainsFunc(kept, func(q sePort) bool { return portKey(q) == k }) {
			continue
		}
		if !p.Bind && p.Proto != helperapi.StreamTCP {
			continue // UDP: nothing checks a connect
		}
		switch t := types.effective(k); {
		case t == sePortType:
			// The policy's own (80, 443, 8443...) or someone else's label.
		case t == "":
			lines = append(lines, fmt.Sprintf("port -a -t %s -p %s %d", sePortType, p.Proto, p.Port))
			kept = append(kept, sePort{Proto: p.Proto, Port: p.Port})
		case p.Bind || slices.Contains(genericPortTypes, t) || !seTypeName.MatchString(t):
			// Accepted when its owner was set (seRefuse); the type changed
			// since. It is not taken over.
			_, _ = fmt.Fprintf(log, "Warning: SELinux: port %s is of type %s now; nginx is not given it.\n", k, t)
		default:
			if !slices.Contains(connect, t) {
				connect = append(connect, t)
			}
		}
	}
	if err := st.seImport(ctx, log, lines); err != nil {
		return err
	}
	s.Labeled = kept
	if err := st.seSave(s); err != nil {
		return err
	}
	sort.Strings(connect)
	if !slices.Equal(connect, s.Connect) {
		if err := st.seConnectModule(ctx, log, connect); err != nil {
			return err
		}
		s.Connect = connect
		if err := st.seSave(s); err != nil {
			return err
		}
	}
	if len(needed) > 0 {
		return st.seConnectBoolean(ctx, log)
	}
	return nil
}

// seReleaseLine removes a label of Cairn's. A port an earlier version
// overrode gets its type back: Orig when known, otherwise the policy's
// (deleting the local rule brings it back). When neither is known, the
// port's earlier type is lost; the log says how to set it again.
func seReleaseLine(log io.Writer, p sePort, types sePortTypes) string {
	pol := types.policy[portKey(p)]
	if p.Modified && p.Orig != "" && p.Orig != pol {
		return fmt.Sprintf("port -m -t %s -p %s %d", p.Orig, p.Proto, p.Port)
	}
	if p.Modified && pol == "" {
		_, _ = fmt.Fprintf(log, "Warning: SELinux: an earlier version replaced the type of port %s/%d with %s, and the type it had before is not known. "+
			"If a service uses this port, give it its type again: semanage port -a -t <type> -p %s %d\n", p.Proto, p.Port, sePortType, p.Proto, p.Port)
	}
	return fmt.Sprintf("port -d -p %s %d", p.Proto, p.Port)
}

// seConnectModule loads the policy module that lets httpd_t connect to
// ports of types (or removes it when there are none). The module is CIL,
// which semodule (policycoreutils) compiles itself.
func (st *Sites) seConnectModule(ctx context.Context, log io.Writer, types []string) error {
	if len(types) == 0 {
		out, err := queryCommand(ctx, seSemodule, "-l")
		if err != nil {
			return siteErr(helperapi.CodeSiteSELinux, map[string]any{"detail": errDetail(err)})
		}
		if !slices.ContainsFunc(strings.Split(out, "\n"), func(l string) bool {
			f := strings.Fields(l)
			return len(f) > 0 && f[0] == seModule
		}) {
			return nil
		}
		_, _ = fmt.Fprintf(log, "SELinux: removing the policy module %s.\n", seModule)
		if err := runCommand(ctx, log, seSemodule, "-r", seModule); err != nil {
			return siteErr(helperapi.CodeSiteSELinux, map[string]any{"detail": errDetail(err)})
		}
		return nil
	}
	var b strings.Builder
	b.WriteString("; Written by cairn-helper: nginx (httpd_t) may connect to the upstream ports of these types.\n")
	for _, t := range types {
		if !seTypeName.MatchString(t) {
			return siteErr(helperapi.CodeSiteSELinux, map[string]any{"detail": "invalid port type " + strconv.Quote(t)})
		}
		fmt.Fprintf(&b, "(allow httpd_t %s (tcp_socket (name_connect)))\n", t)
	}
	dir, err := os.MkdirTemp("", "cairn-semodule-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	// semodule names the module after the file.
	file := filepath.Join(dir, seModule+".cil")
	if err := os.WriteFile(file, []byte(b.String()), 0o600); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(log, "SELinux: loading the policy module %s (nginx may connect to ports of type %s).\n", seModule, strings.Join(types, ", "))
	if err := runCommand(ctx, log, seSemodule, "-i", file); err != nil {
		return siteErr(helperapi.CodeSiteSELinux, map[string]any{"detail": errDetail(err)})
	}
	return nil
}

// seUpgrade repairs what versions before seStateVersion 1 did: they
// labeled every upstream port http_port_t, overriding the type of ports
// that had one (semanage port -m), so that a stream to another machine's
// 3306 kept this machine's MariaDB from starting again. The owners'
// records get Bind (a stream's own port) and lose UDP upstreams. A port
// that was overridden only for connecting gets its type back: the policy's
// by deleting the local rule, ssh_port_t when sshd listens on it (the type
// was a local rule, most likely the SSH page's); otherwise its earlier
// type is unknown and the log says so. A stream's own port keeps the label
// while the stream exists, since nginx listens on it, and gets the
// policy's type back when it goes. The labels and the connect module are
// brought up to date by the caller (seApplyPorts).
func (st *Sites) seUpgrade(ctx context.Context, log io.Writer, s *seState) error {
	streams, err := st.streamStates()
	if err != nil {
		return err
	}
	for owner, ps := range s.Ports {
		code, isStream := strings.CutPrefix(owner, "stream:")
		spec, ok := streams[code]
		var out []sePort
		for _, p := range ps {
			p.Bind = isStream && ok && spec.Enabled && p.Proto == spec.Protocol && p.Port == spec.Port
			if !p.Bind && p.Proto != helperapi.StreamTCP {
				continue
			}
			out = append(out, p)
		}
		if len(out) == 0 {
			delete(s.Ports, owner)
		} else {
			s.Ports[owner] = out
		}
	}
	types, err := st.sePortTypes(ctx)
	if err != nil {
		return err
	}
	needed := seNeeded(*s)
	var sshd []int
	if fileExists(st.path(sshdBinary)) {
		if out, err := queryCommand(ctx, st.path(sshdBinary), "-T"); err == nil {
			sshd = parseSSHDConfig(out).ports()
		}
	}
	var lines []string
	var labeled []sePort
	for _, p := range s.Labeled {
		k := portKey(p)
		if !p.Modified {
			labeled = append(labeled, p)
			continue
		}
		if types.local[k] != sePortType {
			// Someone set the type again by hand: the rule is not Cairn's.
			continue
		}
		pol := types.policy[k]
		n, isNeeded := needed[k]
		switch {
		case isNeeded && n.Bind:
			p.Orig = pol
			_, _ = fmt.Fprintf(log, "Warning: SELinux: port %s keeps the type %s an earlier version gave it in place of %s, since a layer-4 proxy listens on it; "+
				"it gets its type back when the proxy is removed or moved to another port.\n", k, sePortType, cmp.Or(pol, "its earlier type"))
			labeled = append(labeled, p)
		case pol != "":
			_, _ = fmt.Fprintf(log, "SELinux: giving port %s its type %s back (an earlier version had replaced it with %s).\n", k, pol, sePortType)
			lines = append(lines, fmt.Sprintf("port -d -p %s %d", p.Proto, p.Port))
		case p.Proto == helperapi.StreamTCP && slices.Contains(sshd, p.Port):
			_, _ = fmt.Fprintf(log, "SELinux: giving port %s the type ssh_port_t back: sshd listens on it (an earlier version had replaced it with %s).\n", k, sePortType)
			lines = append(lines, fmt.Sprintf("port -m -t ssh_port_t -p %s %d", p.Proto, p.Port))
		case isNeeded:
			_, _ = fmt.Fprintf(log, "Warning: SELinux: an earlier version replaced the type of port %s with %s, and the type it had before is not known. "+
				"If a service of this machine uses the port, give it its type again: semanage port -m -t <type> -p %s %d\n", k, sePortType, p.Proto, p.Port)
			labeled = append(labeled, p)
		default:
			// Released by seApplyPorts, which says the same.
			labeled = append(labeled, p)
		}
	}
	if err := st.seImport(ctx, log, lines); err != nil {
		return err
	}
	s.Labeled = labeled
	s.Version = seStateVersion
	return nil
}

// seMigrate runs seUpgrade once after an upgrade, when the helper starts,
// so that a system an earlier version broke is repaired without anyone
// changing a site or stream. The log goes to logger.
func (st *Sites) seMigrate(ctx context.Context, logger *slog.Logger) {
	if st.Unsupported != "" || !fileExists(st.path(seStateFile)) || !fileExists(st.path(seSemanage)) || !selinuxOn(ctx) {
		return
	}
	var b bytes.Buffer
	err := func() error {
		seMu.Lock()
		defer seMu.Unlock()
		s := st.seLoad()
		if s.Version >= seStateVersion {
			return nil
		}
		if err := st.seUpgrade(ctx, &b, &s); err != nil {
			return err
		}
		types, err := st.sePortTypes(ctx)
		if err != nil {
			return err
		}
		return st.seApplyPorts(ctx, &b, s, types)
	}()
	for _, l := range strings.Split(strings.TrimSpace(b.String()), "\n") {
		switch {
		case l == "":
		case strings.HasPrefix(l, "Warning: "):
			logger.Warn(strings.TrimPrefix(l, "Warning: "))
		default:
			logger.Info(l)
		}
	}
	if err != nil {
		logger.Warn("SELinux: repairing the port labels of an earlier version failed; the next site or stream change tries again", "err", err)
	}
}

// seConnectBoolean lets httpd_t connect to http_port_t ports. Labels alone
// are not enough: EL ships every boolean that allows httpd_t to connect
// off. httpd_graceful_shutdown is the narrowest of them: it allows exactly
// http_port_t (80, 443, 8443... and the ports labeled here), where
// httpd_can_network_relay adds other services' ports and
// httpd_can_network_connect every port. PHP-FPM shares httpd_t, so PHP may
// reach web servers on those ports too.
func (st *Sites) seConnectBoolean(ctx context.Context, log io.Writer) error {
	out, err := queryCommand(ctx, "getsebool", seConnectBool)
	if err == nil && strings.Contains(out, "--> on") {
		return nil
	}
	_, _ = fmt.Fprintf(log, "SELinux: switching %s on (nginx may connect to http_port_t ports).\n", seConnectBool)
	if err := runCommand(ctx, log, "setsebool", "-P", seConnectBool, "on"); err != nil {
		return siteErr(helperapi.CodeSiteSELinux, map[string]any{"detail": errDetail(err)})
	}
	return nil
}

// seSite labels a site's directories: the shared rules once, then its
// tmp/ and writable directories.
func (st *Sites) seSite(ctx context.Context, log io.Writer, spec helperapi.SiteSpec) error {
	if !selinuxOn(ctx) {
		return nil
	}
	seMu.Lock()
	defer seMu.Unlock()
	if err := st.seTools(ctx, log); err != nil {
		return err
	}
	s := st.seLoad()
	var lines []string
	if !s.Base {
		lines = append(lines,
			fmt.Sprintf("fcontext -a -t %s '%s(/.*)?'", seContent, sitesDir),
			fmt.Sprintf("fcontext -a -t %s '%s(/.*)?'", seContent, acmeWebroot))
	}
	base := siteDirOf(spec.Code)
	want := []string{base + "/tmp(/.*)?"}
	for _, w := range spec.Writable {
		want = append(want, base+"/app/"+w+"(/.*)?")
	}
	have := s.FContexts[spec.Code]
	for _, r := range have {
		if !slices.Contains(want, r) {
			lines = append(lines, fmt.Sprintf("fcontext -d '%s'", r))
		}
	}
	for _, r := range want {
		if !slices.Contains(have, r) {
			lines = append(lines, fmt.Sprintf("fcontext -a -t %s '%s'", seRWType, r))
		}
	}
	if err := st.seImport(ctx, log, lines); err != nil {
		return err
	}
	s.Base = true
	s.FContexts[spec.Code] = want
	if err := st.seSave(s); err != nil {
		return err
	}
	for _, p := range []string{base, acmeWebroot} {
		if _, err := os.Stat(st.path(p)); err == nil {
			if err := runCommand(ctx, log, seRestorecon, "-RF", p); err != nil {
				return siteErr(helperapi.CodeSiteSELinux, map[string]any{"detail": errDetail(err)})
			}
		}
	}
	return nil
}

// seForget removes a deleted site's rules.
func (st *Sites) seForget(ctx context.Context, log io.Writer, code string) {
	if !selinuxOn(ctx) {
		return
	}
	seMu.Lock()
	s := st.seLoad()
	var lines []string
	for _, r := range s.FContexts[code] {
		lines = append(lines, fmt.Sprintf("fcontext -d '%s'", r))
	}
	delete(s.FContexts, code)
	err := st.seImport(ctx, log, lines)
	if err == nil {
		err = st.seSave(s)
	}
	seMu.Unlock()
	if err != nil {
		_, _ = fmt.Fprintf(log, "Warning: the SELinux rules of %s could not be removed: %v\n", code, err)
	}
	if err := st.seSetPorts(ctx, log, "site:"+code, nil); err != nil {
		_, _ = fmt.Fprintf(log, "Warning: the SELinux port labels of %s could not be removed: %v\n", code, err)
	}
}

// sitePorts are the ports nginx connects to for a site: an app's port, a
// proxy's upstream port (80 and 443 are the policy's own already). nginx
// listens on none of them.
func sitePorts(spec helperapi.SiteSpec) []sePort {
	var out []sePort
	if spec.Type == helperapi.SiteApp && spec.App != nil && spec.App.Port > 0 {
		out = append(out, sePort{Proto: "tcp", Port: spec.App.Port})
	}
	if spec.Proxy != nil {
		if u, err := url.Parse(spec.Proxy.URL); err == nil {
			if n, err := strconv.Atoi(u.Port()); err == nil && n > 0 {
				out = append(out, sePort{Proto: "tcp", Port: n})
			}
		}
	}
	return out
}

// streamPorts are a stream's listening port and, for TCP, its upstreams'
// ports (UDP has no connect permission to give).
func streamPorts(spec helperapi.StreamSpec) []sePort {
	out := []sePort{{Proto: spec.Protocol, Port: spec.Port, Bind: true}}
	if spec.Protocol != helperapi.StreamTCP {
		return out
	}
	for _, u := range spec.Upstreams {
		if _, p, err := net.SplitHostPort(u); err == nil {
			if n, err := strconv.Atoi(p); err == nil && !slices.ContainsFunc(out, func(q sePort) bool { return q.Port == n }) {
				out = append(out, sePort{Proto: spec.Protocol, Port: n})
			}
		}
	}
	return out
}
