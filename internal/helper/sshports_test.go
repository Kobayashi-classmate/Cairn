// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// procHex writes an address the way /proc/net/tcp does: every 32-bit word
// in host byte order.
func procHex(addr string) string {
	raw := netip.MustParseAddr(addr).AsSlice()
	var b strings.Builder
	for i := 0; i < len(raw); i += 4 {
		fmt.Fprintf(&b, "%02X%02X%02X%02X", raw[i+3], raw[i+2], raw[i+1], raw[i])
	}
	return b.String()
}

// fakeSocket is a line of /proc/net/tcp or udp; state 0x0A listens, 0x01
// is an established connection.
type fakeSocket struct {
	local, remote string // "0.0.0.0:22"
	state         int
	inode         uint64
}

func (s fakeSocket) line() string {
	l, r := netip.MustParseAddrPort(s.local), netip.MustParseAddrPort(s.remote)
	return fmt.Sprintf("   0: %s:%04X %s:%04X %02X 00000000:00000000 00:00000000 00000000     0        0 %d 1 0000000000000000 100 0 0 10 0",
		procHex(l.Addr().String()), l.Port(), procHex(r.Addr().String()), r.Port(), s.state, s.inode)
}

// fakeProcess is a process of the fake /proc with the sockets it holds.
type fakeProcess struct {
	pid, parent int
	name        string
	sockets     []uint64
}

func writeProcNet(t *testing.T, root, name string, sockets ...fakeSocket) {
	t.Helper()
	v4 := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
	v6 := v4
	for _, s := range sockets {
		if netip.MustParseAddrPort(s.local).Addr().Is4() {
			v4 += s.line() + "\n"
		} else {
			v6 += s.line() + "\n"
		}
	}
	dir := filepath.Join(root, "proc/net")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	for file, data := range map[string]string{name: v4, name + "6": v6} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func writeProcesses(t *testing.T, root string, procs ...fakeProcess) {
	t.Helper()
	for _, p := range procs {
		dir := filepath.Join(root, "proc", strconv.Itoa(p.pid))
		if err := os.MkdirAll(filepath.Join(dir, "fd"), 0o750); err != nil {
			t.Fatal(err)
		}
		stat := fmt.Sprintf("%d (%s) S %d %d %d 0 -1 4194560 100 0 0 0 0 0 0 0 20 0 1 0 100 1000 100\n", p.pid, p.name, p.parent, p.pid, p.pid)
		if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o600); err != nil {
			t.Fatal(err)
		}
		_ = os.Symlink("/dev/null", filepath.Join(dir, "fd", "0"))
		for i, inode := range p.sockets {
			if err := os.Symlink(fmt.Sprintf("socket:[%d]", inode), filepath.Join(dir, "fd", strconv.Itoa(i+3))); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// sshdStub makes sshd -T under root answer with out (or fail with err),
// and "systemctl show" of the socket units answer with sockets.
type sshdStub struct {
	out     string
	err     error
	sockets string
	ran     []string
}

func (s *sshdStub) use(t *testing.T, root string, systemd bool) {
	t.Helper()
	oldQuery, oldSystemd := queryCommand, systemdRunning
	t.Cleanup(func() { queryCommand, systemdRunning = oldQuery, oldSystemd })
	systemdRunning = func() bool { return systemd }
	if s.out != "" || s.err != nil {
		if err := os.MkdirAll(filepath.Join(root, "usr/sbin"), 0o750); err != nil {
			t.Fatal(err)
		}
		// A stand-in: only its presence is looked at, sshd -T is answered below.
		if err := os.WriteFile(filepath.Join(root, sshdBinary), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	queryCommand = func(_ context.Context, name string, args ...string) (string, error) {
		s.ran = append(s.ran, name+" "+strings.Join(args, " "))
		switch {
		case name == filepath.Join(root, sshdBinary):
			if _, err := os.Stat(filepath.Join(root, "run/sshd")); err != nil {
				return "", errors.New("Missing privilege separation directory: /run/sshd")
			}
			return s.out, s.err
		case name == "systemctl" && args[0] == "show":
			return s.sockets, nil
		}
		return "", nil
	}
}

func TestProcAddr(t *testing.T) {
	for in, want := range map[string]string{
		"0100007F:0016":                         "127.0.0.1:22",
		"00000000:1F90":                         "0.0.0.0:8080",
		"0A00020F:08AE":                         "15.2.0.10:2222",
		"00000000000000000000000001000000:0016": "[::1]:22",
		"00000000000000000000000000000000:01BB": "[::]:443",
		"0000000000000000FFFF00000100007F:1F90": "127.0.0.1:8080",
		"B80D0120000000000000000001000000:0016": "[2001:db8::1]:22",
	} {
		got, ok := procAddr(in)
		if !ok || got.String() != want {
			t.Errorf("%s: %v %v, want %s", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "0100007F", "0100007:0016", "zz00007F:0016", "0100007F:zz"} {
		if _, ok := procAddr(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
	if got := procHex("10.0.2.15"); got != "0F02000A" {
		t.Fatalf("procHex: %s", got)
	}
}

// What sshd -T printed in containers of Debian 13 (OpenSSH 10.0p2) and
// Ubuntu 24.04 (9.6p1) with only "ListenAddress 0.0.0.0:2222" set, while
// sshd listened on 2222 alone: the default port, and the address with its
// own.
const sshdListenAddressOnly = "port 22\naddressfamily any\nlistenaddress 0.0.0.0:2222\n"

func TestSSHDListenAddressPort(t *testing.T) {
	if got := parseSSHDPorts(sshdListenAddressOnly); !slices.Contains(got, 2222) {
		t.Fatalf("the port of ListenAddress 0.0.0.0:2222 is missing: %v", got)
	}
	// The usual output: every address with every Port.
	out := "port 2222\nport 22\nlistenaddress [::]:22\nlistenaddress 0.0.0.0:22\nlistenaddress [::]:2222\nlistenaddress 0.0.0.0:2222\n"
	if got := parseSSHDPorts(out); !slices.Equal(got, []int{22, 2222}) {
		t.Fatalf("%v", got)
	}
	// The Port options alone are what the SSH module compares its drop-in with.
	if got := parseSSHDConfig(sshdListenAddressOnly).ports(); !slices.Equal(got, []int{22}) {
		t.Fatalf("port options: %v", got)
	}
}

func TestParseSocketListen(t *testing.T) {
	// "systemctl show ssh.socket sshd.socket -p ActiveState -p Listen": a
	// block per unit, the properties in no fixed order.
	out := "Listen=0.0.0.0:2223 (Stream)\nListen=[::]:2223 (Stream)\nActiveState=active\n\nActiveState=inactive\nListen=[::]:22 (Stream)\n"
	if got := parseSocketListen(out); !slices.Equal(got, []int{2223}) {
		t.Fatalf("%v", got)
	}
	for _, none := range []string{"", "ActiveState=inactive\n\nActiveState=inactive\n", "ActiveState=active\nListen=/run/sshd.sock (Stream)\nListen=[::]:22 (Datagram)\n"} {
		if got := parseSocketListen(none); len(got) != 0 {
			t.Errorf("%q: %v", none, got)
		}
	}
	if got := parseSocketListen("ActiveState=active\nListen=22 (Stream)\n"); !slices.Equal(got, []int{22}) {
		t.Fatalf("bare port: %v", got)
	}
}

func TestDetectSSH(t *testing.T) {
	ctx := context.Background()
	t.Run("ListenAddress with a port", func(t *testing.T) {
		root := t.TempDir()
		(&sshdStub{out: sshdListenAddressOnly}).use(t, root, false)
		if d := detectSSH(ctx, root); !slices.Equal(d.Ports, []int{22, 2222}) || !d.Installed || d.Unread != "" {
			t.Fatalf("%+v", d)
		}
	})
	t.Run("configuration unreadable, sshd still running", func(t *testing.T) {
		// Measured on Debian 13 and Ubuntu 24.04: sshd -T exits 255 as soon
		// as a file on disk has an option it does not know, while the
		// running sshd keeps listening where it did.
		root := t.TempDir()
		(&sshdStub{err: errors.New("/etc/ssh/sshd_config.d/x.conf: line 1: Bad configuration option: Bogus")}).use(t, root, false)
		writeProcNet(t, root, "tcp",
			fakeSocket{local: "0.0.0.0:2222", remote: "0.0.0.0:0", state: tcpListen, inode: 5},
			fakeSocket{local: "[::]:2222", remote: "[::]:0", state: tcpListen, inode: 6},
			fakeSocket{local: "0.0.0.0:80", remote: "0.0.0.0:0", state: tcpListen, inode: 7})
		writeProcesses(t, root, fakeProcess{pid: 700, parent: 1, name: "sshd", sockets: []uint64{5, 6}},
			fakeProcess{pid: 800, parent: 1, name: "nginx", sockets: []uint64{7}})
		d := detectSSH(ctx, root)
		if !slices.Equal(d.Ports, []int{2222}) || !d.Installed || !strings.Contains(d.Unread, "Bad configuration option") {
			t.Fatalf("%+v", d)
		}
	})
	t.Run("socket activation with the port set on the unit", func(t *testing.T) {
		// Ubuntu 24.04 after "systemctl edit ssh.socket" set ListenStream
		// to 2223: sshd -T still says 2222, the socket is on 2223.
		root := t.TempDir()
		stub := &sshdStub{out: "port 2222\nlistenaddress [::]:2222\nlistenaddress 0.0.0.0:2222\n",
			sockets: "ActiveState=active\nListen=[::]:2223 (Stream)\n\nActiveState=inactive\n"}
		stub.use(t, root, true)
		if d := detectSSH(ctx, root); !slices.Equal(d.Ports, []int{2222, 2223}) {
			t.Fatalf("%+v", d)
		}
		if !slices.Contains(stub.ran, "systemctl show --property=ActiveState --property=Listen ssh.socket sshd.socket") {
			t.Fatalf("ran: %q", stub.ran)
		}
	})
	t.Run("another SSH server", func(t *testing.T) {
		root := t.TempDir()
		(&sshdStub{}).use(t, root, false)
		writeProcNet(t, root, "tcp", fakeSocket{local: "0.0.0.0:22", remote: "0.0.0.0:0", state: tcpListen, inode: 9})
		writeProcesses(t, root, fakeProcess{pid: 321, parent: 1, name: "dropbear", sockets: []uint64{9}})
		if d := detectSSH(ctx, root); !slices.Equal(d.Ports, []int{22}) || d.Installed || d.Unread != "" {
			t.Fatalf("%+v", d)
		}
	})
	t.Run("no SSH server", func(t *testing.T) {
		root := t.TempDir()
		(&sshdStub{}).use(t, root, true)
		if d := detectSSH(ctx, root); len(d.Ports) != 0 || d.Installed || d.Unread != "" {
			t.Fatalf("%+v", d)
		}
	})
	t.Run("sshd -T needs /run/sshd", func(t *testing.T) {
		root := t.TempDir()
		(&sshdStub{out: "port 22\n"}).use(t, root, false)
		if d := detectSSH(ctx, root); !slices.Equal(d.Ports, []int{22}) || d.Unread != "" {
			t.Fatalf("%+v", d)
		}
	})
}

// With sshd's configuration unreadable, the firewall keeps the SSH ports
// it knew open instead of falling back to 22 alone, and says so.
func TestFirewallKeepsSSHPortsWhenSSHDCannotBeRead(t *testing.T) {
	f, ufw, _ := newDebianFirewall(t)
	f.Root = t.TempDir()
	sshDetect = detectSSH
	stub := &sshdStub{out: "port 2222\nlistenaddress 0.0.0.0:2222\n"}
	stub.use(t, f.Root, false)
	ctx := context.Background()
	sshRule := func() string {
		r := ufw.last().Rules[0]
		if r.Owner != helperapi.OwnerSSH {
			t.Fatalf("first rule: %+v", r)
		}
		return strings.Join(r.Ports, ",")
	}
	if err := f.Refresh(ctx, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := sshRule(); got != "2222" {
		t.Fatalf("configuration readable: %s", got)
	}
	if st := f.Status(ctx); st.SSHUnread || !slices.Equal(st.SSHPorts, []int{2222}) {
		t.Fatalf("status: %+v", st)
	}
	stub.out, stub.err = "", errors.New("exit status 255")
	if err := f.Refresh(ctx, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := sshRule(); got != "22,2222" {
		t.Fatalf("configuration unreadable: SSH rule %s; the port sshd runs on must stay open", got)
	}
	st := f.Status(ctx)
	if !st.SSHUnread || !slices.Equal(st.SSHPorts, []int{22, 2222}) || !slices.Equal(st.Rules[0].Ports, []string{"22", "2222"}) {
		t.Fatalf("status: %+v", st)
	}
	// A change made now cannot take the real SSH port for a forward.
	if _, err := f.setForward(ctx, fwAdmin, helperapi.FirewallForward{Protocol: "tcp", Port: 2222, ToPort: 8080}); !hasCode(err, helperapi.CodeFirewallProtected) {
		t.Fatalf("forward on the SSH port: %v", err)
	}
	// Readable again: the guess goes.
	stub.out, stub.err = "port 2222\nlistenaddress 0.0.0.0:2222\n", nil
	if err := f.Refresh(ctx, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := sshRule(); got != "2222" {
		t.Fatalf("configuration readable again: %s", got)
	}

	// Never read at all (the firewall is new): the ports the panel set
	// for SSH count as well.
	g, ufw2, _ := newDebianFirewall(t)
	g.Root = f.Root
	sshDetect = detectSSH
	stub.out, stub.err = "", errors.New("exit status 255")
	if err := os.WriteFile(filepath.Join(g.StateDir, "ssh.json"), []byte(`{"ports":[2200]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := g.Refresh(ctx, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(ufw2.last().Rules[0].Ports, ","); got != "22,2200" {
		t.Fatalf("panel-set port: %s", got)
	}
}

func TestSSHSessionPorts(t *testing.T) {
	root := t.TempDir()
	writeProcNet(t, root, "tcp",
		fakeSocket{local: "0.0.0.0:2222", remote: "0.0.0.0:0", state: tcpListen, inode: 3},
		fakeSocket{local: "192.0.2.10:2222", remote: "203.0.113.9:51234", state: tcpEstablished, inode: 9},
		fakeSocket{local: "192.0.2.10:443", remote: "203.0.113.9:51235", state: tcpEstablished, inode: 10})
	writeProcesses(t, root,
		fakeProcess{pid: 60, parent: 1, name: "sshd", sockets: []uint64{3}},
		fakeProcess{pid: 70, parent: 60, name: "sshd-session", sockets: []uint64{9}},
		fakeProcess{pid: 71, parent: 70, name: "sshd-session", sockets: []uint64{9}},
		fakeProcess{pid: 80, parent: 71, name: "bash"},
		fakeProcess{pid: 90, parent: 80, name: "sudo"},
		fakeProcess{pid: 95, parent: 90, name: "sh (install)", sockets: []uint64{10}},
		fakeProcess{pid: 100, parent: 95, name: "cairn-helper"},
		fakeProcess{pid: 200, parent: 1, name: "cron"})
	if got := sshSessionPorts(root, 100); !slices.Equal(got, []int{2222}) {
		t.Fatalf("from the SSH session: %v", got)
	}
	if got := sshSessionPorts(root, 200); len(got) != 0 {
		t.Fatalf("outside an SSH session: %v", got)
	}
	if got := sshSessionPorts(root, 4242); len(got) != 0 {
		t.Fatalf("unknown process: %v", got)
	}
}

func TestExposedListeners(t *testing.T) {
	root := t.TempDir()
	writeProcNet(t, root, "tcp",
		fakeSocket{local: "0.0.0.0:25", remote: "0.0.0.0:0", state: tcpListen, inode: 11},
		fakeSocket{local: "127.0.0.1:3306", remote: "0.0.0.0:0", state: tcpListen, inode: 12},
		fakeSocket{local: "[::1]:6379", remote: "[::]:0", state: tcpListen, inode: 13},
		fakeSocket{local: "[::]:8080", remote: "[::]:0", state: tcpListen, inode: 14},
		fakeSocket{local: "192.0.2.10:8080", remote: "203.0.113.9:50000", state: tcpEstablished, inode: 15})
	writeProcNet(t, root, "udp",
		fakeSocket{local: "0.0.0.0:53", remote: "0.0.0.0:0", state: 7, inode: 21},
		fakeSocket{local: "0.0.0.0:68", remote: "0.0.0.0:0", state: 7, inode: 22},
		fakeSocket{local: "192.0.2.10:123", remote: "198.51.100.1:123", state: 1, inode: 23},
		fakeSocket{local: "0.0.0.0:40000", remote: "0.0.0.0:0", state: 7, inode: 24},
		fakeSocket{local: "127.0.0.1:323", remote: "0.0.0.0:0", state: 7, inode: 25})
	writeProcesses(t, root,
		fakeProcess{pid: 400, parent: 1, name: "master", sockets: []uint64{11}},
		fakeProcess{pid: 410, parent: 1, name: "node", sockets: []uint64{14, 15}},
		fakeProcess{pid: 420, parent: 1, name: "named", sockets: []uint64{21}})
	got := exposedListeners(root)
	want := []listener{{Port: 25, Process: "master"}, {Port: 8080, Process: "node"}, {Port: 53, UDP: true, Process: "named"}}
	if !slices.Equal(got, want) {
		t.Fatalf("%+v", got)
	}
}

func TestReachedFromOutside(t *testing.T) {
	old := localAddrs
	t.Cleanup(func() { localAddrs = old })
	localAddrs = func() []netip.Addr {
		return []netip.Addr{netip.MustParseAddr("192.0.2.10"), netip.MustParseAddr("2001:db8::10")}
	}
	for _, c := range []struct {
		name string
		s    fakeSocket
		want string
	}{
		{"over loopback", fakeSocket{local: "127.0.0.1:2222", remote: "127.0.0.1:40000", state: tcpEstablished}, ""},
		{"over loopback (IPv6)", fakeSocket{local: "[::1]:2222", remote: "[::1]:40000", state: tcpEstablished}, ""},
		{"from the host's own address", fakeSocket{local: "192.0.2.10:2222", remote: "192.0.2.10:40000", state: tcpEstablished}, ""},
		{"from the host's own IPv6 address", fakeSocket{local: "[2001:db8::10]:2222", remote: "[2001:db8::10]:40000", state: tcpEstablished}, ""},
		{"listening only", fakeSocket{local: "0.0.0.0:2222", remote: "0.0.0.0:0", state: tcpListen}, ""},
		{"a handshake that never finished", fakeSocket{local: "192.0.2.10:2222", remote: "203.0.113.9:40000", state: 3}, ""},
		{"another port", fakeSocket{local: "192.0.2.10:22", remote: "203.0.113.9:40000", state: tcpEstablished}, ""},
		{"from another host", fakeSocket{local: "192.0.2.10:2222", remote: "203.0.113.9:40000", state: tcpEstablished}, "203.0.113.9"},
		{"from another host (IPv6)", fakeSocket{local: "[2001:db8::10]:2222", remote: "[2001:db8::99]:40000", state: tcpEstablished}, "2001:db8::99"},
	} {
		root := t.TempDir()
		writeProcNet(t, root, "tcp", c.s)
		if got := reachedFromOutside(root, 2222); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}
