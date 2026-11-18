// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"io"
	"os"
	"path/filepath"
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
					f.ports[fl[3]+" "+fl[5]+" "+fl[6]] = true
				case fl[0] == "fcontext" && fl[1] == "-d":
					delete(f.fc, strings.Trim(fl[2], "'"))
				case fl[0] == "fcontext":
					f.fc[strings.Trim(fl[4], "'")] = fl[3]
				}
			}
			return nil
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
		policy: "http_port_t tcp 80, 81, 443, 488, 8008, 8009, 8443, 9000\nntop_port_t tcp 3000-3001\nunreserved_port_t tcp 1024-32767\n"}
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
	all := ""
	for _, imp := range f.imports {
		all += strings.Join(imp, "\n") + "\n"
	}
	for _, want := range []string{"port -m -t http_port_t -p tcp 3000", "port -a -t http_port_t -p tcp 9002"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in\n%s", want, all)
		}
	}
	if strings.Contains(all, "8443") {
		t.Errorf("relabeled a port of the policy's own:\n%s", all)
	}
	// 3000 stays while the stream needs it; it goes with the last user.
	if err := st.seSetPorts(ctx, io.Discard, "site:app", nil); err != nil {
		t.Fatal(err)
	}
	if !f.ports["http_port_t tcp 3000"] {
		t.Fatal("3000 released while the stream uses it")
	}
	if err := st.seSetPorts(ctx, io.Discard, "stream:s", nil); err != nil {
		t.Fatal(err)
	}
	if len(f.ports) != 0 {
		t.Fatalf("labels left: %v", f.ports)
	}
	// Without SELinux nothing runs.
	selinuxOn = func(context.Context) bool { return false }
	n := len(f.imports)
	_ = st.seSetPorts(ctx, io.Discard, "site:x", []sePort{{Proto: "tcp", Port: 4000}})
	if len(f.imports) != n {
		t.Fatal("semanage ran without SELinux")
	}
}
