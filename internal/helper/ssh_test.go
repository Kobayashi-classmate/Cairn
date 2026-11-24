// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// fakeSSHD answers sshd -T from the files under root the way sshd reads
// them: the drop-ins first (sorted), then sshd_config; the first value of
// a key wins, every Port adds a port.
type fakeSSHD struct {
	root string
	cmds []string
}

func (f *fakeSSHD) effective() string {
	files, _ := filepath.Glob(filepath.Join(f.root, "etc/ssh/sshd_config.d/*.conf"))
	files = append(files, filepath.Join(f.root, "etc/ssh/sshd_config"))
	seen := map[string]bool{}
	var out []string
	var ports []string
	for _, p := range files {
		data, _ := os.ReadFile(p)
		for _, line := range strings.Split(string(data), "\n") {
			fl := strings.Fields(line)
			if len(fl) < 2 || strings.HasPrefix(fl[0], "#") || strings.EqualFold(fl[0], "Include") {
				continue
			}
			k := strings.ToLower(fl[0])
			if k == "port" {
				ports = append(ports, fl[1])
				continue
			}
			if !seen[k] {
				seen[k] = true
				out = append(out, k+" "+fl[1])
			}
		}
	}
	if len(ports) == 0 {
		ports = []string{"22"}
	}
	for _, p := range ports {
		out = append(out, "port "+p)
	}
	for _, d := range []string{"passwordauthentication yes", "permitrootlogin without-password", "pubkeyauthentication yes"} {
		if k := strings.Fields(d)[0]; !seen[k] {
			out = append(out, d)
		}
	}
	// Without a ListenAddress of its own, sshd -T lists the wildcard
	// address with every port.
	for _, p := range ports {
		out = append(out, "listenaddress 0.0.0.0:"+p)
	}
	return strings.Join(out, "\n") + "\n"
}

func (f *fakeSSHD) use(t *testing.T) {
	t.Helper()
	oldRun, oldQuery, oldSystemd, oldDial, oldDetect := runCommand, queryCommand, systemdRunning, sshdDial, sshDetect
	t.Cleanup(func() {
		runCommand, queryCommand, systemdRunning, sshdDial, sshDetect = oldRun, oldQuery, oldSystemd, oldDial, oldDetect
	})
	systemdRunning = func() bool { return true }
	sshdDial = func(context.Context, string) error { return nil }
	// The firewall of a test has no root of its own: it reads this one's.
	sshDetect = func(ctx context.Context, _ string) sshEndpoints { return detectSSH(ctx, f.root) }
	runCommand = func(_ context.Context, _ io.Writer, name string, args ...string) error {
		f.cmds = append(f.cmds, filepath.Base(name)+" "+strings.Join(args, " "))
		return nil
	}
	queryCommand = func(_ context.Context, name string, args ...string) (string, error) {
		if strings.HasSuffix(name, "sshd") && len(args) == 1 && args[0] == "-T" {
			return f.effective(), nil
		}
		if name == "getenforce" {
			return "", os.ErrNotExist
		}
		return "", nil
	}
}

func newTestSSH(t *testing.T) (*SSH, *fakeSSHD) {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"etc/ssh/sshd_config.d", "usr/sbin", "root", "home/deploy", "proc/net"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p, data string) {
		if err := os.WriteFile(filepath.Join(root, p), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("usr/sbin/sshd", "")
	write("etc/ssh/sshd_config", "Include /etc/ssh/sshd_config.d/*.conf\n#Port 22\nPasswordAuthentication yes\n")
	write("etc/ssh/sshd_config.d/50-cloud-init.conf", "PasswordAuthentication no\n")
	write("etc/passwd", "root:x:0:0:root:/root:/bin/bash\ndeploy:x:1000:1000::/home/deploy:/bin/bash\n"+
		"cw-shop:x:1001:1001::/srv/www/shop:/bin/bash\nnobody:x:65534:65534::/:/usr/sbin/nologin\n")
	write("proc/net/tcp", "  sl  local_address rem_address   st\n   0: 00000000:1F90 00000000:0000 0A\n")
	files := &Files{Root: root, StateDir: t.TempDir()}
	f := &fakeSSHD{root: root}
	f.use(t)
	return &SSH{StateDir: t.TempDir(), Files: files, Service: "ssh", Root: root, Executable: "/usr/lib/cairn/cairn-helper"}, f
}

func newKey(t *testing.T, comment string) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sp, _ := ssh.NewPublicKey(pub)
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sp))) + " " + comment
}

func TestSSHSettingsAndPort(t *testing.T) {
	s, f := newTestSSH(t)
	ctx := context.Background()
	st := s.Status(ctx)
	if !st.Installed || st.PasswordAuthentication || st.PermitRootLogin != "prohibit-password" || len(st.Ports) != 1 {
		t.Fatalf("status %+v", st)
	}
	if len(st.Users) != 2 || st.Users[0].Name != "root" || st.Users[1].Name != "deploy" {
		t.Fatalf("users %+v", st.Users)
	}

	// Passwords cannot be switched off while nobody has a key.
	if _, err := s.settings(ctx, helperapi.Caller{}, helperapi.SSHSettingsRequest{PermitRootLogin: "no"}, io.Discard); !hasCode(err, helperapi.CodeSSHNoKeys) {
		t.Fatalf("no keys: %v", err)
	}
	// The drop-in wins over cloud-init's file.
	if _, err := s.settings(ctx, helperapi.Caller{}, helperapi.SSHSettingsRequest{PasswordAuthentication: true, PermitRootLogin: "no"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if st := s.Status(ctx); !st.PasswordAuthentication || st.PermitRootLogin != "no" {
		t.Fatalf("after settings %+v", st)
	}
	drop, _ := os.ReadFile(filepath.Join(s.Root, sshDropIn))
	if !strings.Contains(string(drop), "PasswordAuthentication yes\nPermitRootLogin no\n") {
		t.Fatalf("drop-in:\n%s", drop)
	}
	if !strings.Contains(strings.Join(f.cmds, "\n"), "systemctl try-reload-or-restart ssh") {
		t.Fatalf("not reloaded: %v", f.cmds)
	}

	// A port in use elsewhere is refused (8080 listens in /proc/net/tcp).
	if _, err := s.addPort(ctx, helperapi.Caller{}, helperapi.SSHPortRequest{Port: 8080, CloseAfterMinutes: 30}, io.Discard); !hasCode(err, helperapi.CodeSSHPortInUse) {
		t.Fatalf("port in use: %v", err)
	}
	// A Port line in sshd_config would keep listening: it is commented out.
	main := filepath.Join(s.Root, sshMainConfig)
	_ = os.WriteFile(main, []byte("Include /etc/ssh/sshd_config.d/*.conf\nPort 22\n"), 0o600)
	res, err := s.addPort(ctx, helperapi.Caller{}, helperapi.SSHPortRequest{Port: 2222, CloseAfterMinutes: 30}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	p := res.(*helperapi.SSHPortPending)
	if p.NewPort != 2222 || len(p.OldPorts) != 1 || p.OldPorts[0] != 22 {
		t.Fatalf("pending %+v", p)
	}
	if st := s.Status(ctx); len(st.Ports) != 2 || st.Pending == nil {
		t.Fatalf("both ports %+v", st)
	}
	if !regexp.MustCompile(`systemd-run --unit=cairn-ssh-port-close .*--on-active=1800 .* ssh finish-port`).MatchString(strings.Join(f.cmds, "\n")) {
		t.Fatalf("no timer: %v", f.cmds)
	}
	if _, err := s.addPort(ctx, helperapi.Caller{}, helperapi.SSHPortRequest{Port: 2223, CloseAfterMinutes: 30}, io.Discard); !hasCode(err, helperapi.CodeSSHPending) {
		t.Fatalf("second change: %v", err)
	}
	if err := s.FinishPort(ctx, io.Discard, false); err != nil {
		t.Fatal(err)
	}
	if st := s.Status(ctx); len(st.Ports) != 1 || st.Ports[0] != 2222 || st.Pending != nil {
		t.Fatalf("finished %+v", st)
	}
	// Closing port 22 needs the Port line of sshd_config gone.
	if data, _ := os.ReadFile(main); !strings.Contains(string(data), "# Moved to /etc/ssh/sshd_config.d/00-cairn.conf by Cairn: Port 22") {
		t.Fatalf("main config:\n%s", data)
	}
	// Cancel goes back to the old port.
	if _, err := s.addPort(ctx, helperapi.Caller{}, helperapi.SSHPortRequest{Port: 2022, CloseAfterMinutes: 5}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err := s.cancelPort(ctx, helperapi.Caller{}, helperapi.Empty{}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if st := s.Status(ctx); len(st.Ports) != 1 || st.Ports[0] != 2222 {
		t.Fatalf("cancelled %+v", st)
	}
	if err := s.FinishPort(ctx, io.Discard, false); !hasCode(err, helperapi.CodeSSHNoPending) {
		t.Fatalf("finish without change: %v", err)
	}
}

func TestSSHKeys(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("writing keys for another user needs root (make test runs as root in a container)")
	}
	s, _ := newTestSSH(t)
	ctx := context.Background()
	key := newKey(t, "me@laptop")
	k, err := s.addKey(ctx, helperapi.Caller{}, helperapi.SSHKeyAddRequest{User: "deploy", Key: key})
	if err != nil || k.Type != "ssh-ed25519" || k.Comment != "me@laptop" || !strings.HasPrefix(k.Fingerprint, "SHA256:") {
		t.Fatalf("%+v %v", k, err)
	}
	if _, err := s.addKey(ctx, helperapi.Caller{}, helperapi.SSHKeyAddRequest{User: "deploy", Key: key}); !hasCode(err, helperapi.CodeSSHKeyExists) {
		t.Fatalf("duplicate: %v", err)
	}
	for _, bad := range []string{`from="1.2.3.4" ` + key, "ssh-ed25519 notbase64", key + "\nssh-rsa AAAA"} {
		if _, err := s.addKey(ctx, helperapi.Caller{}, helperapi.SSHKeyAddRequest{User: "deploy", Key: bad}); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	info, err := os.Stat(filepath.Join(s.Root, "home/deploy/.ssh/authorized_keys"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("file %v %v", info, err)
	}
	// Other lines stay when a key goes.
	p := filepath.Join(s.Root, "home/deploy/.ssh/authorized_keys")
	data, _ := os.ReadFile(p)
	_ = os.WriteFile(p, append([]byte("# my note\n"), data...), 0o600)
	// With password logins off (cloud-init's file), the last key of the
	// only user who has one stays: nobody could log in.
	if _, err := s.deleteKey(ctx, helperapi.Caller{}, helperapi.SSHKeyDeleteRequest{User: "deploy", Fingerprint: k.Fingerprint}); !hasCode(err, helperapi.CodeSSHNoKeys) {
		t.Fatalf("last key deleted with passwords off: %v", err)
	}
	if err := os.WriteFile(filepath.Join(s.Root, sshDropIn), []byte("PasswordAuthentication yes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.deleteKey(ctx, helperapi.Caller{}, helperapi.SSHKeyDeleteRequest{User: "deploy", Fingerprint: k.Fingerprint}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(p); string(data) != "# my note\n" {
		t.Fatalf("left %q", data)
	}
	// Site owners and unknown users are not managed here; root needs the
	// root access switch.
	for _, u := range []string{"cw-shop", "nobody", "ghost"} {
		if _, err := s.keys(u); !hasCode(err, helperapi.CodeSSHUserNotFound) {
			t.Errorf("%s: %v", u, err)
		}
	}
	if _, err := s.addKey(ctx, helperapi.Caller{}, helperapi.SSHKeyAddRequest{User: "root", Key: key}); !hasCode(err, helperapi.CodeSSHRootDisabled) {
		t.Fatalf("root: %v", err)
	}
	// A file the user links into .ssh is never written through: not under
	// the name the helper once used for its temporary file, not as the
	// keys file itself.
	outside := filepath.Join(s.Root, "etc", "shadow")
	if err := os.WriteFile(outside, []byte("root:secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".authorized_keys.cairn", "authorized_keys"} {
		planted := filepath.Join(s.Root, "home/deploy/.ssh", name)
		_ = os.Remove(planted)
		if err := os.Link(outside, planted); err != nil {
			t.Fatal(err)
		}
	}
	other := newKey(t, "other@host")
	if _, err := s.addKey(ctx, helperapi.Caller{}, helperapi.SSHKeyAddRequest{User: "deploy", Key: other}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(outside); string(data) != "root:secret\n" {
		t.Fatalf("written through a hard link: %q", data)
	}
	if info, err := os.Lstat(p); err != nil || !info.Mode().IsRegular() || info.Sys().(*syscall.Stat_t).Nlink != 1 {
		t.Fatalf("keys file: %v %v", info, err)
	}
	// Changes at the same time all land.
	_ = os.Remove(p)
	var wg sync.WaitGroup
	for range 8 {
		key := newKey(t, "k")
		wg.Go(func() {
			if _, err := s.addKey(ctx, helperapi.Caller{}, helperapi.SSHKeyAddRequest{User: "deploy", Key: key}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if got, _ := s.keys("deploy"); len(got.Keys) != 8 {
		t.Fatalf("%d of 8 keys kept", len(got.Keys))
	}
	if entries, _ := os.ReadDir(filepath.Join(s.Root, "home/deploy/.ssh")); len(entries) != 2 { // the keys and the planted name
		t.Fatalf("temporary files left: %v", entries)
	}

	// A symbolic link out of the home is refused.
	_ = os.RemoveAll(filepath.Join(s.Root, "home/deploy/.ssh"))
	if err := os.Symlink("/etc", filepath.Join(s.Root, "home/deploy/.ssh")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.addKey(ctx, helperapi.Caller{}, helperapi.SSHKeyAddRequest{User: "deploy", Key: key}); err == nil {
		t.Fatal("followed a link out of the home")
	}
}

func TestParseLogin(t *testing.T) {
	for msg, want := range map[string]helperapi.SSHLogin{
		"Accepted publickey for root from 203.0.113.9 port 51234 ssh2: ED25519 SHA256:x": {User: "root", IP: "203.0.113.9", Method: "publickey", Result: "accepted"},
		"Accepted keyboard-interactive/pam for deploy from 2001:db8::1 port 2 ssh2":      {User: "deploy", IP: "2001:db8::1", Method: "keyboard-interactive", Result: "accepted"},
		"Failed password for root from 198.51.100.1 port 22 ssh2":                        {User: "root", IP: "198.51.100.1", Method: "password", Result: "failed"},
		"Failed password for invalid user admin from 198.51.100.1 port 22 ssh2":          {User: "admin", IP: "198.51.100.1", Method: "password", Result: "invalid_user"},
		"Invalid user oracle from 198.51.100.2 port 4444":                                {User: "oracle", IP: "198.51.100.2", Result: "invalid_user"},
	} {
		got, ok := parseLogin(msg)
		if !ok || got != want {
			t.Errorf("%s: %+v", msg, got)
		}
	}
	if _, ok := parseLogin("Connection closed by 1.2.3.4 port 22"); ok {
		t.Error("matched a non-login line")
	}
	if got := usecArg("1700000000123456", 1); got != "@1700000000.123457" {
		t.Error(got)
	}
}

func TestSELinuxPortType(t *testing.T) {
	list := "http_port_t                    tcp      80, 81, 443, 488, 8008, 8009, 8443, 9000\nssh_port_t                     tcp      22\nunreserved_port_t              tcp      61000-61001, 1024-32767\n"
	for port, want := range map[int]string{22: "ssh_port_t", 8443: "http_port_t", 2222: "", 50000: ""} {
		if got := selinuxPortType(list, port); got != want {
			t.Errorf("%d: %q", port, got)
		}
	}
}

func TestSSHNoLockout(t *testing.T) {
	s, _ := newTestSSH(t)
	ctx := context.Background()
	// Only root has a key: switching passwords off is fine while root may
	// still log in with it, not when root login goes off too.
	if err := os.MkdirAll(filepath.Join(s.Root, "root/.ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Root, "root/.ssh/authorized_keys"), []byte(newKey(t, "root@laptop")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.settings(ctx, helperapi.Caller{}, helperapi.SSHSettingsRequest{PermitRootLogin: helperapi.RootLoginNo}, io.Discard); !hasCode(err, helperapi.CodeSSHNoKeys) {
		t.Fatalf("root login off with root's key only: %v", err)
	}
	if _, err := s.settings(ctx, helperapi.Caller{}, helperapi.SSHSettingsRequest{PermitRootLogin: helperapi.RootLoginKeysOnly}, io.Discard); err != nil {
		t.Fatal(err)
	}
}

func TestSSHPortAndFirewall(t *testing.T) {
	s, _ := newTestSSH(t)
	ctx := context.Background()
	ufw := &fakeBackend{on: true}
	fw := &Firewall{StateDir: t.TempDir(), PanelPort: 28443, Default: helperapi.FirewallUFW,
		backends: map[string]fwBackend{helperapi.FirewallUFW: ufw, helperapi.FirewallFirewalld: &fakeBackend{}, helperapi.FirewallNftables: &fakeBackend{}}}
	s.Firewall = fw
	// A port a forward listens on is taken: the forward would get the
	// connections.
	if err := fw.save(fwConfig{Enabled: true, Backend: helperapi.FirewallUFW,
		Forwards: []helperapi.FirewallForward{{ID: "f0123456789abcde", Protocol: helperapi.ProtoTCP, Port: 2200, ToPort: 80}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.addPort(ctx, helperapi.Caller{}, helperapi.SSHPortRequest{Port: 2200, CloseAfterMinutes: 30}, io.Discard); !hasCode(err, helperapi.CodeSSHPortInUse) {
		t.Fatalf("forwarded port: %v", err)
	}
	// The firewall fails halfway through opening the new port: it gets
	// the old SSH port back, and the drop-in is as before.
	ufw.failNext = 1
	if _, err := s.addPort(ctx, helperapi.Caller{}, helperapi.SSHPortRequest{Port: 2222, CloseAfterMinutes: 30}, io.Discard); err == nil {
		t.Fatal("port change despite a failed firewall")
	}
	if len(ufw.applied) == 0 {
		t.Fatal("the firewall was not put back")
	}
	if r := ufw.last(); r.Rules[0].Owner != helperapi.OwnerSSH || strings.Join(r.Rules[0].Ports, ",") != "22" {
		t.Fatalf("firewall after the failure: %+v", r.Rules)
	}
	if st := s.Status(ctx); len(st.Ports) != 1 || st.Ports[0] != 22 || st.Pending != nil {
		t.Fatalf("ssh after the failure: %+v", st)
	}

	// The firewall keeps open what sshd listens on, and when its rules are
	// made for a port that goes, sshd has not reloaded yet and still holds
	// it. Once it has, the rules are made again and the port is closed.
	if _, err := s.addPort(ctx, helperapi.Caller{}, helperapi.SSHPortRequest{Port: 2222, CloseAfterMinutes: 30}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if r := ufw.last(); strings.Join(r.Rules[0].Ports, ",") != "22,2222" {
		t.Fatalf("firewall with the second port: %+v", r.Rules)
	}
	applied, detect := len(ufw.applied), sshDetect
	sshDetect = func(ctx context.Context, root string) sshEndpoints {
		e := detect(ctx, root)
		if len(ufw.applied) == applied { // not reloaded yet
			e.Ports = addPorts(e.Ports, 2222)
		}
		return e
	}
	if _, err := s.cancelPort(ctx, helperapi.Caller{}, helperapi.Empty{}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if r := ufw.last(); strings.Join(r.Rules[0].Ports, ",") != "22" {
		t.Fatalf("firewall after the port was given up: %+v", r.Rules)
	}
}

func TestSSHKeysFileIsReadSafely(t *testing.T) {
	s, _ := newTestSSH(t)
	dir := filepath.Join(s.Root, "home/deploy/.ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "authorized_keys")
	// A pipe there does not hang the read.
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := s.keys("deploy"); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a pipe was read as keys")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reading a pipe hangs")
	}
	// Nor is a huge file read whole.
	_ = os.Remove(p)
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(64 << 20); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if _, err := s.keys("deploy"); err == nil {
		t.Fatal("a 64 MiB file was read")
	}
}
