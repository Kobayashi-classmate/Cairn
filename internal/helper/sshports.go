// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Where SSH connections arrive. The firewall keeps these ports open whatever
// its rules say and the SSH page shows them, so both ask here. The "port"
// lines of sshd -T alone are not enough:
//
//   - "ListenAddress host:port" makes sshd listen on that port only, while
//     sshd -T still prints the default "port 22";
//   - with socket activation the ports are the socket unit's, which a
//     drop-in for the unit changes without sshd's configuration knowing;
//   - sshd -T fails as soon as one file on disk has an option this sshd
//     does not know, while the running sshd keeps its ports;
//   - the SSH server may not be /usr/sbin/sshd at all.
//
// So the ports are the union of what the configuration says, what the
// socket units listen on and what SSH servers hold open right now.

// sshEndpoints is what the system tells about its SSH server.
type sshEndpoints struct {
	// Ports is every port found, sorted.
	Ports []int
	// Installed reports whether there is an sshd to read a configuration from.
	Installed bool
	// Unread says why sshd's configuration could not be read ("" when it
	// was, or when there is no sshd).
	Unread string
}

// sshDetect finds the SSH ports; root prefixes system paths (tests, which
// may also replace it).
var sshDetect = detectSSH

// sshServerNames are the programs that listen for SSH connections;
// sshSessionNames those that hold an SSH connection once it is made.
var (
	sshServerNames  = []string{"sshd", "dropbear"}
	sshSessionNames = []string{"sshd", "sshd-session", "dropbear", "tinysshd"}
)

// sshSocketUnits are the socket units an SSH server is started from
// (ssh.socket on Debian and Ubuntu, sshd.socket elsewhere).
var sshSocketUnits = []string{"ssh.socket", "sshd.socket"}

func addPorts(ports []int, more ...int) []int {
	for _, p := range more {
		if p > 0 && p < 65536 && !slices.Contains(ports, p) {
			ports = append(ports, p)
		}
	}
	slices.Sort(ports)
	return ports
}

func detectSSH(ctx context.Context, root string) sshEndpoints {
	var e sshEndpoints
	if fileExists(filepath.Join(root, sshdBinary)) {
		e.Installed = true
		if c, err := readSSHDConfig(ctx, root); err != nil {
			e.Unread = errDetail(err)
		} else {
			e.Ports = addPorts(e.Ports, c.listenPorts()...)
		}
	}
	e.Ports = addPorts(e.Ports, sshListeners(root)...)
	e.Ports = addPorts(e.Ports, sshSocketPorts(ctx)...)
	return e
}

// readSSHDConfig runs sshd -T, which needs sshd's privilege separation
// directory: it is on a tmpfs and missing until sshd has run once.
func readSSHDConfig(ctx context.Context, root string) (sshdConfig, error) {
	_ = os.MkdirAll(filepath.Join(root, "/run/sshd"), 0o755) //nolint:gosec // G301: sshd's privilege separation directory.
	out, err := queryCommand(ctx, filepath.Join(root, sshdBinary), "-T")
	if err != nil {
		return nil, err
	}
	return parseSSHDConfig(out), nil
}

// listenPorts are the ports of the configuration: every Port, and the port
// of every ListenAddress (sshd -T prints them with the port they get).
func (c sshdConfig) listenPorts() []int {
	ports := c.ports()
	for _, a := range c["listenaddress"] {
		if _, p, err := net.SplitHostPort(a); err == nil {
			if n, err := strconv.Atoi(p); err == nil {
				ports = addPorts(ports, n)
			}
		}
	}
	return ports
}

// parseSSHDPorts reads the ports of sshd -T's output.
func parseSSHDPorts(out string) []int {
	return parseSSHDConfig(out).listenPorts()
}

// sshSocketPorts reads the ports of the active SSH socket units.
func sshSocketPorts(ctx context.Context) []int {
	if !systemdRunning() {
		return nil
	}
	args := append([]string{"show", "--property=ActiveState", "--property=Listen"}, sshSocketUnits...)
	out, err := queryCommand(ctx, "systemctl", args...)
	if err != nil {
		return nil
	}
	return parseSocketListen(out)
}

// parseSocketListen reads "systemctl show" of several units (one block of
// lines each): the ports of "Listen=[::]:22 (Stream)" lines in the blocks
// of active units.
func parseSocketListen(out string) []int {
	var ports []int
	for _, block := range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n\n") {
		var found []int
		active := false
		for _, line := range strings.Split(block, "\n") {
			k, v, _ := strings.Cut(strings.TrimSpace(line), "=")
			switch k {
			case "ActiveState":
				active = v == "active"
			case "Listen":
				addr, kind, _ := strings.Cut(v, " (")
				if !strings.HasPrefix(kind, "Stream") {
					continue
				}
				if _, p, err := net.SplitHostPort(addr); err == nil {
					addr = p
				}
				if n, err := strconv.Atoi(addr); err == nil {
					found = append(found, n)
				}
			}
		}
		if active {
			ports = addPorts(ports, found...)
		}
	}
	return ports
}

// /proc ---------------------------------------------------------------------

const tcpListen = 0x0A // TCP_LISTEN in /proc/net/tcp

// tcpEstablished is TCP_ESTABLISHED.
const tcpEstablished = 0x01

// procSocket is a line of /proc/net/tcp, tcp6, udp or udp6.
type procSocket struct {
	local, remote netip.AddrPort
	state         int
	inode         uint64
}

// procAddr reads "0100007F:0016": the address as the kernel prints it (each
// 32-bit word in host byte order, which is little-endian on every
// architecture Cairn is built for) and the port.
func procAddr(s string) (netip.AddrPort, bool) {
	a, p, ok := strings.Cut(s, ":")
	if !ok || len(a) != 8 && len(a) != 32 {
		return netip.AddrPort{}, false
	}
	raw, err := hex.DecodeString(a)
	if err != nil {
		return netip.AddrPort{}, false
	}
	for i := 0; i < len(raw); i += 4 {
		binary.LittleEndian.PutUint32(raw[i:], binary.BigEndian.Uint32(raw[i:]))
	}
	port, err := strconv.ParseUint(p, 16, 16)
	if err != nil {
		return netip.AddrPort{}, false
	}
	addr, _ := netip.AddrFromSlice(raw)
	return netip.AddrPortFrom(addr.Unmap(), uint16(port)), true
}

// procSockets reads the TCP (or UDP) sockets of /proc/net under root.
func procSockets(root string, udp bool) []procSocket {
	files := []string{"/proc/net/tcp", "/proc/net/tcp6"}
	if udp {
		files = []string{"/proc/net/udp", "/proc/net/udp6"}
	}
	var out []procSocket
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(root, f)) //nolint:gosec // G304: fixed /proc files.
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n")[1:] {
			fl := strings.Fields(line)
			if len(fl) < 4 {
				continue
			}
			var s procSocket
			var ok bool
			if s.local, ok = procAddr(fl[1]); !ok {
				continue
			}
			s.remote, _ = procAddr(fl[2])
			if st, err := strconv.ParseInt(fl[3], 16, 32); err == nil {
				s.state = int(st)
			}
			if len(fl) > 9 {
				s.inode, _ = strconv.ParseUint(fl[9], 10, 64)
			}
			out = append(out, s)
		}
	}
	return out
}

// procPIDs lists the processes of /proc under root.
func procPIDs(root string) []string {
	entries, err := os.ReadDir(filepath.Join(root, "/proc"))
	if err != nil {
		return nil
	}
	var pids []string
	for _, e := range entries {
		if n := e.Name(); e.IsDir() && n[0] >= '1' && n[0] <= '9' {
			pids = append(pids, n)
		}
	}
	return pids
}

// procStat reads a process's name and its parent from /proc/<pid>/stat
// ("12 (name) S 1 ..."; the name may hold spaces and parentheses).
func procStat(root, pid string) (comm, parent string) {
	data, err := os.ReadFile(filepath.Join(root, "/proc", pid, "stat")) //nolint:gosec // G304: a /proc file of a numbered process.
	if err != nil {
		return "", ""
	}
	s := string(data)
	open, shut := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if open < 0 || shut < open {
		return "", ""
	}
	comm = s[open+1 : shut]
	if rest := strings.Fields(s[shut+1:]); len(rest) > 1 {
		parent = rest[1]
	}
	return comm, parent
}

// procSocketInodes lists the sockets a process holds open.
func procSocketInodes(root, pid string) []uint64 {
	dir := filepath.Join(root, "/proc", pid, "fd")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var inodes []uint64
	for _, e := range entries {
		link, err := os.Readlink(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		if s, ok := strings.CutPrefix(link, "socket:["); ok {
			if n, err := strconv.ParseUint(strings.TrimSuffix(s, "]"), 10, 64); err == nil {
				inodes = append(inodes, n)
			}
		}
	}
	return inodes
}

// sshListeners are the ports SSH servers listen on right now, whatever
// their configuration on disk says and wherever they are installed.
func sshListeners(root string) []int {
	listening := map[uint64]int{}
	for _, s := range procSockets(root, false) {
		if s.state == tcpListen && s.inode != 0 {
			listening[s.inode] = int(s.local.Port())
		}
	}
	if len(listening) == 0 {
		return nil
	}
	var ports []int
	for _, pid := range procPIDs(root) {
		if comm, _ := procStat(root, pid); !slices.Contains(sshServerNames, comm) {
			continue
		}
		for _, inode := range procSocketInodes(root, pid) {
			if p, ok := listening[inode]; ok {
				ports = addPorts(ports, p)
			}
		}
	}
	return ports
}

// sshSessionPorts finds the SSH connections the process pid was started
// from (the installer run in an SSH session): the server ports of the
// connections its ancestors hold. sudo drops SSH_CONNECTION from the
// environment, so the process tree is the only place that always tells.
func sshSessionPorts(root string, pid int) []int {
	connected := map[uint64]int{}
	for _, s := range procSockets(root, false) {
		if s.state != tcpListen && s.inode != 0 {
			connected[s.inode] = int(s.local.Port())
		}
	}
	var ports []int
	at := strconv.Itoa(pid)
	for range 64 {
		if at == "" || at == "0" {
			break
		}
		comm, parent := procStat(root, at)
		if slices.Contains(sshSessionNames, comm) {
			for _, inode := range procSocketInodes(root, at) {
				if p, ok := connected[inode]; ok {
					ports = addPorts(ports, p)
				}
			}
		}
		at = parent
	}
	return ports
}

// localAddrs are the addresses of this host's interfaces. Replaceable in tests.
var localAddrs = func() []netip.Addr {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []netip.Addr
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok {
			if ip, ok := netip.AddrFromSlice(n.IP); ok {
				out = append(out, ip.Unmap())
			}
		}
	}
	return out
}

// reachedFromOutside reports the peer of an established connection to a
// local TCP port that came from another host ("" when there is none). A
// connection from this host itself, over loopback or to one of its own
// addresses, does not pass any firewall in front of the host and proves
// nothing about them.
func reachedFromOutside(root string, port int) string {
	var own []netip.Addr
	for _, s := range procSockets(root, false) {
		if s.state != tcpEstablished || int(s.local.Port()) != port {
			continue
		}
		peer := s.remote.Addr()
		if !peer.IsValid() || peer.IsUnspecified() || peer.IsLoopback() {
			continue
		}
		if own == nil {
			own = append(localAddrs(), netip.Addr{})
		}
		if !slices.Contains(own, peer) {
			return peer.String()
		}
	}
	return ""
}

// listener is a port something on this host takes connections on from
// other hosts.
type listener struct {
	Port    int
	UDP     bool
	Process string
}

// dhcpClientPorts are bound by the DHCP clients, which every backend keeps
// working on its own.
var dhcpClientPorts = []int{68, 546}

// exposedListeners lists the TCP ports in LISTEN state and the UDP ports
// servers are bound to, on addresses other hosts can reach, with the
// program that holds each.
func exposedListeners(root string) []listener {
	type key struct {
		port int
		udp  bool
	}
	found := map[key]uint64{}
	collect := func(udp bool) {
		for _, s := range procSockets(root, udp) {
			port := int(s.local.Port())
			switch {
			case s.local.Addr().IsLoopback() || port == 0:
				continue
			case !udp && s.state != tcpListen:
				continue
			// A connected UDP socket is a client's; so is one on an
			// ephemeral port.
			case udp && (s.remote.Port() != 0 || port >= 32768 || slices.Contains(dhcpClientPorts, port)):
				continue
			}
			if _, ok := found[key{port, udp}]; !ok || s.inode != 0 {
				found[key{port, udp}] = s.inode
			}
		}
	}
	collect(false)
	collect(true)
	if len(found) == 0 {
		return nil
	}
	owner := map[uint64]string{}
	for _, pid := range procPIDs(root) {
		inodes := procSocketInodes(root, pid)
		if len(inodes) == 0 {
			continue
		}
		comm, _ := procStat(root, pid)
		for _, inode := range inodes {
			if _, ok := owner[inode]; !ok {
				owner[inode] = comm
			}
		}
	}
	out := make([]listener, 0, len(found))
	for k, inode := range found {
		out = append(out, listener{Port: k.port, UDP: k.udp, Process: owner[inode]})
	}
	slices.SortFunc(out, func(a, b listener) int {
		if a.UDP != b.UDP {
			if a.UDP {
				return 1
			}
			return -1
		}
		return a.Port - b.Port
	})
	return out
}
