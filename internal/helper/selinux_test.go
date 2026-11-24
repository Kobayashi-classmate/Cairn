// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// fakeSemanage keeps local rules the way semanage does and answers its
// listings; imports are applied line by line.
type fakeSemanage struct {
	policy  string          // "semanage port -l -n" lines of the policy
	ports   map[string]bool // local "type proto port"
	fc      map[string]string
	imports [][]string
	other   []string
	module  string // the CIL of the loaded cairn_connect module
	sshd    string // "sshd -T" output
}

// imported is every line imported so far.
func (f *fakeSemanage) imported() string {
	all := ""
	for _, imp := range f.imports {
		all += strings.Join(imp, "\n") + "\n"
	}
	return all
}

// local is the local rule of a port ("" when there is none).
func (f *fakeSemanage) local(proto string, port int) string {
	for k := range f.ports {
		if fl := strings.Fields(k); fl[1] == proto && fl[2] == strconv.Itoa(port) {
			return fl[0]
		}
	}
	return ""
}

func (f *fakeSemanage) use(t *testing.T) {
	t.Helper()
	oldRun, oldQuery, oldOn := runCommand, queryCommand, selinuxOn
	t.Cleanup(func() { runCommand, queryCommand, selinuxOn = oldRun, oldQuery, oldOn })
	selinuxOn = func(context.Context) bool { return true }
	queryCommand = func(_ context.Context, name string, args ...string) (string, error) {
		cmd := strings.Join(args, " ")
		switch {
		case strings.HasSuffix(name, "semanage") && cmd == "port -l -n":
			out := f.policy
			for k := range f.ports {
				out += k + "\n"
			}
			return out, nil
		case strings.HasSuffix(name, "semanage") && cmd == "port -l -C -n":
			out := ""
			for k := range f.ports {
				out += k + "\n"
			}
			return out, nil
		case strings.HasSuffix(name, "semanage") && cmd == "fcontext -l -C -n":
			out := ""
			for p, typ := range f.fc {
				out += p + "  all files  system_u:object_r:" + typ + ":s0\n"
			}
			return out, nil
		case strings.HasSuffix(name, "semodule") && cmd == "-l":
			out := "apache\nmysql\n"
			if f.module != "" {
				out += seModule + "\n"
			}
			return out, nil
		case strings.HasSuffix(name, "sshd") && cmd == "-T":
			return f.sshd, nil
		}
		return "", nil
	}
	runCommand = func(_ context.Context, _ io.Writer, name string, args ...string) error {
		if strings.HasSuffix(name, "semanage") && len(args) == 3 && args[0] == "import" {
			data, err := os.ReadFile(args[2])
			if err != nil {
				return err
			}
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			f.imports = append(f.imports, lines)
			for _, l := range lines {
				fl := strings.Fields(l)
				switch {
				case fl[0] == "port" && fl[1] == "-d":
					for k := range f.ports {
						if strings.HasSuffix(k, " "+fl[3]+" "+fl[4]) {
							delete(f.ports, k)
						}
					}
				case fl[0] == "port":
					// A port has one local rule: an add or modify replaces it.
					for k := range f.ports {
						if strings.HasSuffix(k, " "+fl[5]+" "+fl[6]) {
							delete(f.ports, k)
						}
					}
					f.ports[fl[3]+" "+fl[5]+" "+fl[6]] = true
				case fl[0] == "fcontext" && fl[1] == "-d":
					delete(f.fc, strings.Trim(fl[2], "'"))
				case fl[0] == "fcontext":
					f.fc[strings.Trim(fl[4], "'")] = fl[3]
				}
			}
			return nil
		}
		if strings.HasSuffix(name, "semodule") && len(args) == 2 {
			switch args[0] {
			case "-i":
				data, err := os.ReadFile(args[1])
				if err != nil {
					return err
				}
				if filepath.Base(args[1]) != seModule+".cil" {
					t.Errorf("module file %s", args[1])
				}
				f.module = string(data)
			case "-r":
				f.module = ""
			}
		}
		f.other = append(f.other, filepath.Base(name)+" "+strings.Join(args, " "))
		return nil
	}
}

func newSESites(t *testing.T) (*Sites, *fakeSemanage) {
	t.Helper()
	root := t.TempDir()
	for _, p := range []string{seSemanage, seRestorecon} {
		writeSystemFile(t, root, p, "")
	}
	if err := os.MkdirAll(filepath.Join(root, "srv/www/shop/app"), 0o750); err != nil {
		t.Fatal(err)
	}
	f := &fakeSemanage{ports: map[string]bool{}, fc: map[string]string{},
		policy: "http_port_t tcp 80, 81, 443, 488, 8008, 8009, 8443, 9000\nntop_port_t tcp 3000-3001\nunreserved_port_t tcp 1024-32767\n" +
			"mysqld_port_t tcp 1186, 3306, 63132-63164\nssh_port_t tcp 22\ndns_port_t udp 53, 853\nunreserved_port_t udp 1024-32767\n"}
	f.use(t)
	return &Sites{Root: root, Family: "el"}, f
}

func TestSELinuxSiteLabels(t *testing.T) {
	st, f := newSESites(t)
	ctx := context.Background()
	spec := helperapi.SiteSpec{Code: "shop", Type: helperapi.SitePHP, Writable: []string{"uploads", "var/cache"}}
	if err := st.seSite(ctx, io.Discard, spec); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]string{
		"/srv/www(/.*)?":                    seContent,
		"/var/lib/cairn-acme(/.*)?":         seContent,
		"/srv/www/shop/tmp(/.*)?":           seRWType,
		"/srv/www/shop/app/uploads(/.*)?":   seRWType,
		"/srv/www/shop/app/var/cache(/.*)?": seRWType,
	} {
		if f.fc[p] != want {
			t.Errorf("%s: %q", p, f.fc[p])
		}
	}
	if !strings.Contains(strings.Join(f.other, "\n"), "restorecon -RF /srv/www/shop") {
		t.Fatalf("no relabel: %v", f.other)
	}
	// A writable directory dropped loses its rule; the shared rules are
	// not added twice.
	spec.Writable = []string{"uploads"}
	if err := st.seSite(ctx, io.Discard, spec); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.fc["/srv/www/shop/app/var/cache(/.*)?"]; ok {
		t.Fatal("dropped directory still writable")
	}
	if last := strings.Join(f.imports[len(f.imports)-1], "\n"); strings.Contains(last, "/srv/www(/.*)?") {
		t.Fatalf("shared rule repeated:\n%s", last)
	}
	st.seForget(ctx, io.Discard, "shop")
	for p := range f.fc {
		if strings.Contains(p, "/shop/") {
			t.Errorf("left %s", p)
		}
	}
}

func TestSELinuxPorts(t *testing.T) {
	st, f := newSESites(t)
	ctx := context.Background()
	// An app on 3000 (ntop's), a proxy to 8443 (already http_port_t), a
	// stream on 9002 to 3000.
	if err := st.seSetPorts(ctx, io.Discard, "site:app", sitePorts(helperapi.SiteSpec{Type: helperapi.SiteApp, App: &helperapi.AppSpec{Port: 3000}})); err != nil {
		t.Fatal(err)
	}
	if err := st.seSetPorts(ctx, io.Discard, "site:px", sitePorts(helperapi.SiteSpec{Type: helperapi.SiteProxy, Proxy: &helperapi.ProxySpec{URL: "https://10.0.0.5:8443/"}})); err != nil {
		t.Fatal(err)
	}
	if err := st.seSetPorts(ctx, io.Discard, "stream:s", streamPorts(helperapi.StreamSpec{Protocol: "tcp", Port: 9002, Upstreams: []string{"127.0.0.1:3000"}})); err != nil {
		t.Fatal(err)
	}
	all := f.imported()
	if !strings.Contains(all, "port -a -t http_port_t -p tcp 9002") {
		t.Errorf("stream port not labeled:\n%s", all)
	}
	// 3000 keeps ntop's type; nginx connects to it through the module.
	if strings.Contains(all, "3000") || strings.Contains(all, "port -m") {
		t.Errorf("relabeled a port with a type of its own:\n%s", all)
	}
	if strings.Contains(all, "8443") {
		t.Errorf("relabeled a port of the policy's own:\n%s", all)
	}
	if !strings.Contains(f.module, "(allow httpd_t ntop_port_t (tcp_socket (name_connect)))") {
		t.Fatalf("module: %q", f.module)
	}
	if !strings.Contains(strings.Join(f.other, "\n"), "setsebool -P "+seConnectBool+" on") {
		t.Errorf("boolean not set: %v", f.other)
	}
	// The connect permission stays while the stream needs it; it goes
	// with the last user, and so do the labels.
	if err := st.seSetPorts(ctx, io.Discard, "site:app", nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.module, "ntop_port_t") {
		t.Fatal("3000 released while the stream uses it")
	}
	if err := st.seSetPorts(ctx, io.Discard, "site:px", nil); err != nil {
		t.Fatal(err)
	}
	if err := st.seSetPorts(ctx, io.Discard, "stream:s", nil); err != nil {
		t.Fatal(err)
	}
	if len(f.ports) != 0 || f.module != "" {
		t.Fatalf("left: %v %q", f.ports, f.module)
	}
	if s := st.seLoad(); len(s.Labeled) != 0 || len(s.Connect) != 0 || len(s.Ports) != 0 {
		t.Fatalf("state left: %+v", s)
	}
	// Without SELinux nothing runs.
	selinuxOn = func(context.Context) bool { return false }
	n, m := len(f.imports), len(f.other)
	_ = st.seSetPorts(ctx, io.Discard, "site:x", []sePort{{Proto: "tcp", Port: 4000}})
	_ = st.seSetPorts(ctx, io.Discard, "stream:y", streamPorts(helperapi.StreamSpec{Protocol: "tcp", Port: 3306, Upstreams: []string{"10.0.0.2:22"}}))
	if len(f.imports) != n || len(f.other) != m {
		t.Fatal("semanage ran without SELinux")
	}
}

// A stream or proxy to another machine's database or SSH port must not
// take the port's type from this machine's own service.
func TestSELinuxUpstreamKeepsItsType(t *testing.T) {
	st, f := newSESites(t)
	ctx := context.Background()
	// The SSH page's port 2222 is a local rule.
	f.ports["ssh_port_t tcp 2222"] = true
	if err := st.seSetPorts(ctx, io.Discard, "stream:tcp-4407", streamPorts(helperapi.StreamSpec{
		Protocol: "tcp", Port: 4407, Upstreams: []string{"10.0.2.2:3306", "10.0.2.3:2222"}})); err != nil {
		t.Fatal(err)
	}
	if err := st.seSetPorts(ctx, io.Discard, "site:px", sitePorts(helperapi.SiteSpec{Type: helperapi.SiteProxy,
		Proxy: &helperapi.ProxySpec{URL: "http://10.0.0.5:22/"}})); err != nil {
		t.Fatal(err)
	}
	// UDP: only the listening port; 53 stays dns_port_t.
	if err := st.seSetPorts(ctx, io.Discard, "stream:udp-5300", streamPorts(helperapi.StreamSpec{
		Protocol: "udp", Port: 5300, Upstreams: []string{"8.8.8.8:53", "10.0.0.9:5301"}})); err != nil {
		t.Fatal(err)
	}
	all := f.imported()
	if strings.Contains(all, "port -m") {
		t.Fatalf("a type was overridden:\n%s", all)
	}
	for _, bad := range []string{" 3306", " 2222", "tcp 22\n", " 53\n", " 5301"} {
		if strings.Contains(all, bad) {
			t.Errorf("upstream port relabeled (%q):\n%s", bad, all)
		}
	}
	if f.local("tcp", 3306) != "" || f.local("tcp", 2222) != "ssh_port_t" || f.local("udp", 53) != "" {
		t.Fatalf("local rules: %v", f.ports)
	}
	if f.local("tcp", 4407) != sePortType || f.local("udp", 5300) != sePortType {
		t.Fatalf("listening ports not labeled: %v", f.ports)
	}
	for _, typ := range []string{"mysqld_port_t", "ssh_port_t"} {
		if !strings.Contains(f.module, "(allow httpd_t "+typ+" (tcp_socket (name_connect)))") {
			t.Errorf("module lacks %s: %q", typ, f.module)
		}
	}
	if strings.Contains(f.module, "dns_port_t") || strings.Contains(f.module, "unreserved") {
		t.Errorf("module: %q", f.module)
	}
	// Removing them leaves the local rules as they were.
	for _, o := range []string{"stream:tcp-4407", "site:px", "stream:udp-5300"} {
		if err := st.seSetPorts(ctx, io.Discard, o, nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.ports) != 1 || !f.ports["ssh_port_t tcp 2222"] || f.module != "" {
		t.Fatalf("left: %v %q", f.ports, f.module)
	}
}

// nginx may not listen on a port SELinux gives another service.
func TestSELinuxListenPortTaken(t *testing.T) {
	st, f := newSESites(t)
	ctx := context.Background()
	f.ports["ssh_port_t tcp 2222"] = true
	for _, spec := range []helperapi.StreamSpec{
		{Protocol: "tcp", Port: 3306, Upstreams: []string{"10.0.0.2:3306"}},
		{Protocol: "tcp", Port: 2222, Upstreams: []string{"10.0.0.2:22"}},
		{Protocol: "udp", Port: 53, Upstreams: []string{"10.0.0.2:53"}},
	} {
		err := st.seSetPorts(ctx, io.Discard, "stream:x", streamPorts(spec))
		var he *helperapi.Error
		if !errors.As(err, &he) || he.Code != helperapi.CodeStreamSELinuxPort || he.Params["port"] != spec.Port {
			t.Fatalf("%d: %v", spec.Port, err)
		}
	}
	if len(f.imports) != 0 || f.module != "" || len(st.seLoad().Ports) != 0 {
		t.Fatalf("changed: %v %q", f.imports, f.module)
	}
	// A port of the policy's http_port_t and a port without a type are fine.
	for _, p := range []int{8443, 9100} {
		if err := st.seSetPorts(ctx, io.Discard, "stream:x", streamPorts(helperapi.StreamSpec{Protocol: "tcp", Port: p})); err != nil {
			t.Fatal(err)
		}
	}
}

// An earlier version overrode upstream ports' types; the upgrade gives
// them back.
func TestSELinuxUpgradeRepairsOverrides(t *testing.T) {
	st, f := newSESites(t)
	ctx := context.Background()
	writeSystemFile(t, st.Root, sshdBinary, "")
	f.sshd = "port 22\nport 2222\n"
	streams := map[string]helperapi.StreamSpec{
		"tcp-4407": {Code: "tcp-4407", Protocol: "tcp", Port: 4407, Upstreams: []string{"10.0.2.2:3306"}, Enabled: true},
		"udp-5300": {Code: "udp-5300", Protocol: "udp", Port: 5300, Upstreams: []string{"8.8.8.8:53"}, Enabled: true},
		"tcp-4422": {Code: "tcp-4422", Protocol: "tcp", Port: 4422, Upstreams: []string{"10.0.2.3:2222"}, Enabled: true},
		"tcp-3000": {Code: "tcp-3000", Protocol: "tcp", Port: 3000, Upstreams: []string{"10.0.2.4:80"}, Enabled: true},
	}
	for code, spec := range streams {
		data, _ := json.Marshal(spec)
		writeSystemFile(t, st.Root, path.Join(streamStateDir, code+".json"), string(data))
	}
	// What the earlier version recorded and did.
	legacy := `{"base": true,
	  "ports": {
	    "stream:tcp-4407": [{"proto": "tcp", "port": 4407}, {"proto": "tcp", "port": 3306}],
	    "stream:udp-5300": [{"proto": "udp", "port": 5300}, {"proto": "udp", "port": 53}],
	    "stream:tcp-4422": [{"proto": "tcp", "port": 4422}, {"proto": "tcp", "port": 2222}],
	    "stream:tcp-3000": [{"proto": "tcp", "port": 3000}],
	    "site:app": [{"proto": "tcp", "port": 7777}]
	  },
	  "labeled": [
	    {"proto": "tcp", "port": 3000, "modified": true},
	    {"proto": "tcp", "port": 2222, "modified": true},
	    {"proto": "tcp", "port": 3306, "modified": true},
	    {"proto": "tcp", "port": 4407},
	    {"proto": "tcp", "port": 4422},
	    {"proto": "tcp", "port": 7777, "modified": true},
	    {"proto": "udp", "port": 53, "modified": true},
	    {"proto": "udp", "port": 5300}
	  ],
	  "fcontexts": {}}`
	writeSystemFile(t, st.Root, seStateFile, legacy)
	for _, k := range []string{"tcp 3000", "tcp 2222", "tcp 3306", "tcp 4407", "tcp 4422", "tcp 7777", "udp 53", "udp 5300"} {
		f.ports[sePortType+" "+k] = true
	}
	var logs bytes.Buffer
	st.seMigrate(ctx, slog.New(slog.NewTextHandler(&logs, nil)))
	t.Log(logs.String())
	for _, c := range []struct {
		proto string
		port  int
		want  string
	}{
		{"tcp", 3306, ""},           // mysqld_port_t of the policy again
		{"udp", 53, ""},             // dns_port_t of the policy again
		{"tcp", 2222, "ssh_port_t"}, // sshd listens on it
		{"tcp", 3000, sePortType},   // a stream listens on it
		{"tcp", 7777, sePortType},   // earlier type unknown
		{"tcp", 4407, sePortType},
		{"tcp", 4422, sePortType},
		{"udp", 5300, sePortType},
	} {
		if got := f.local(c.proto, c.port); got != c.want {
			t.Errorf("%s/%d: %q, want %q", c.proto, c.port, got, c.want)
		}
	}
	if strings.Contains(f.imported(), "-t http_port_t") {
		t.Errorf("labels added:\n%s", f.imported())
	}
	for _, typ := range []string{"mysqld_port_t", "ssh_port_t"} {
		if !strings.Contains(f.module, "httpd_t "+typ+" ") {
			t.Errorf("module lacks %s: %q", typ, f.module)
		}
	}
	for _, want := range []string{"semanage port -m -t <type> -p tcp 7777", "tcp/3000 keeps the type http_port_t", "in place of ntop_port_t"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log lacks %q", want)
		}
	}
	s := st.seLoad()
	if s.Version != seStateVersion || len(s.Ports["stream:udp-5300"]) != 1 || !s.Ports["stream:udp-5300"][0].Bind {
		t.Fatalf("state: %+v", s)
	}
	// Once is enough.
	n := len(f.imports)
	st.seMigrate(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if len(f.imports) != n {
		t.Fatal("upgrade ran twice")
	}
	// The stream on 3000 goes: 3000 is ntop's again.
	if err := st.seSetPorts(ctx, io.Discard, "stream:tcp-3000", nil); err != nil {
		t.Fatal(err)
	}
	if f.local("tcp", 3000) != "" {
		t.Fatalf("3000: %v", f.ports)
	}
}

// A system that never had Cairn's SELinux state is left alone.
func TestSELinuxUpgradeWithoutState(t *testing.T) {
	st, f := newSESites(t)
	st.seMigrate(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if len(f.imports) != 0 || len(f.other) != 0 || fileExists(st.path(seStateFile)) {
		t.Fatalf("changed: %v %v", f.imports, f.other)
	}
}
