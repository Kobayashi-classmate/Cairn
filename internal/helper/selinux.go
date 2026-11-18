// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
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
//   - The ports nginx connects to or listens on for the panel (an app's
//     port, a proxy's upstream port, a stream's ports) are labeled
//     http_port_t one by one, and the label goes when nothing uses the
//     port any more (decided for P1 batch 10 instead of the
//     httpd_can_network_connect boolean); httpd_graceful_shutdown lets
//     httpd_t connect to http_port_t at all.
//
// The changes go through "semanage import" in one transaction; what Cairn
// added is recorded so that only its own rules are ever removed.

const (
	seStateFile = "/var/lib/cairn-helper/selinux.json"
	sePortType  = "http_port_t"
	seRWType    = "httpd_sys_rw_content_t"
	seContent   = "httpd_sys_content_t"
	seSemanage  = "/usr/sbin/semanage"
	// seConnectBool: see seConnectBoolean.
	seConnectBool = "httpd_graceful_shutdown"
	seRestorecon  = "/usr/sbin/restorecon"
)

var seMu sync.Mutex

// sePort is one labeled port.
type sePort struct {
	Proto string `json:"proto"`
	Port  int    `json:"port"`
	// Modified means the port had another type that Cairn overrode
	// (semanage port -m); the record is deleted the same way.
	Modified bool `json:"modified,omitempty"`
}

// seState records Cairn's SELinux rules: ports by owner ("site:<code>",
// "stream:<code>"), file context rules by site, and whether the shared
// rules (sites, ACME webroot) are in place.
type seState struct {
	Base      bool                `json:"base"`
	Ports     map[string][]sePort `json:"ports"`
	Labeled   []sePort            `json:"labeled"`
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

// seFilter drops the lines that would fail: adds of rules already there,
// deletes of rules that are not.
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
				l = strings.Replace(l, "port -a", "port -m", 1)
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

// sePortTypes reads every TCP and UDP port type: "proto/port" -> type,
// ignoring the generic range types.
func (st *Sites) sePortTypes(ctx context.Context) (map[string]string, error) {
	out, err := queryCommand(ctx, seSemanage, "port", "-l", "-n")
	if err != nil {
		return nil, siteErr(helperapi.CodeSiteSELinux, map[string]any{"detail": errDetail(err)})
	}
	types := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(strings.ReplaceAll(line, ",", " "))
		if len(f) < 3 || (f[1] != "tcp" && f[1] != "udp") || slices.Contains(genericPortTypes, f[0]) {
			continue
		}
		for _, p := range f[2:] {
			lo, hi, ok := helperapi.ParsePortRange(p)
			if !ok || hi-lo > 4096 {
				continue
			}
			for n := lo; n <= hi; n++ {
				key := f[1] + "/" + strconv.Itoa(n)
				// A local rule and the policy's both list the port; the
				// one Cairn wants wins when either is http_port_t.
				if types[key] != sePortType {
					types[key] = f[0]
				}
			}
		}
	}
	return types, nil
}

func portKey(p sePort) string { return p.Proto + "/" + strconv.Itoa(p.Port) }

// seSetPorts gives owner exactly ports (nil releases them) and labels or
// unlabels the ports no other owner needs.
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
	if len(ports) == 0 {
		delete(s.Ports, owner)
	} else {
		s.Ports[owner] = ports
	}
	needed := map[string]sePort{}
	for _, ps := range s.Ports {
		for _, p := range ps {
			needed[portKey(p)] = p
		}
	}
	types, err := st.sePortTypes(ctx)
	if err != nil {
		return err
	}
	var lines []string
	var kept []sePort
	for _, p := range s.Labeled {
		if _, ok := needed[portKey(p)]; ok {
			kept = append(kept, p)
			continue
		}
		lines = append(lines, fmt.Sprintf("port -d -p %s %d", p.Proto, p.Port))
	}
	keys := make([]string, 0, len(needed))
	for k := range needed {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		p := needed[k]
		if slices.ContainsFunc(kept, func(q sePort) bool { return portKey(q) == k }) {
			continue
		}
		switch types[k] {
		case sePortType:
			continue // the policy's own (80, 443, 8443...)
		case "":
			lines = append(lines, fmt.Sprintf("port -a -t %s -p %s %d", sePortType, p.Proto, p.Port))
		default:
			// Another service's port (3000 is ntop's, 8000 soundd's):
			// this one serves the site now.
			lines = append(lines, fmt.Sprintf("port -m -t %s -p %s %d", sePortType, p.Proto, p.Port))
			p.Modified = true
		}
		kept = append(kept, p)
	}
	if err := st.seImport(ctx, log, lines); err != nil {
		return err
	}
	s.Labeled = kept
	if err := st.seSave(s); err != nil {
		return err
	}
	if len(needed) > 0 {
		return st.seConnectBoolean(ctx, log)
	}
	return nil
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
// proxy's upstream port (80 and 443 are the policy's own already).
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

// streamPorts are a stream's listening port and its upstreams' ports.
func streamPorts(spec helperapi.StreamSpec) []sePort {
	out := []sePort{{Proto: spec.Protocol, Port: spec.Port}}
	for _, u := range spec.Upstreams {
		if _, p, err := net.SplitHostPort(u); err == nil {
			if n, err := strconv.Atoi(p); err == nil && !slices.ContainsFunc(out, func(q sePort) bool { return q.Port == n }) {
				out = append(out, sePort{Proto: spec.Protocol, Port: n})
			}
		}
	}
	return out
}
