// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// SSH manages the SSH server's login settings, its port and the
// authorized keys of root and the ordinary users.
type SSH struct {
	StateDir string
	Files    *Files    // users and the root access switch
	Firewall *Firewall // keeps the SSH ports open (nil: none)
	Software *Software // installs the SELinux tools when needed
	// Service is the platform's SSH unit: ssh (Debian) or sshd (EL).
	Service    string
	Executable string
	Root       string
	Log        *slog.Logger
	now        func() time.Time

	// watch makes port changes look out for the first connection from
	// another host (set by Resume, in the helper that keeps running).
	watch   bool
	watchMu sync.Mutex
	// watching: the lookout is running.
	watching bool
}

const (
	sshdBinary     = "/usr/sbin/sshd"
	sshDropIn      = "/etc/ssh/sshd_config.d/00-cairn.conf"
	sshMainConfig  = "/etc/ssh/sshd_config"
	sshPortTimer   = "cairn-ssh-port-close"
	sshManagedNote = "# Managed by Cairn. Change these settings in the panel instead of editing this file.\n" +
		"# sshd keeps the first value it reads, and this file is read before the others.\n"
)

// sshState is what the panel set.
type sshState struct {
	// Ports is nil until the panel changes the port.
	Ports     []int                     `json:"ports,omitempty"`
	Password  *bool                     `json:"password,omitempty"`
	RootLogin string                    `json:"rootLogin,omitempty"`
	Pending   *helperapi.SSHPortPending `json:"pending,omitempty"`
}

// RegisterSSH adds the SSH actions.
func RegisterSSH(r *Registry, o *Ops, s *SSH) {
	Register(r, helperapi.ActionSSHStatus, func(ctx context.Context, _ helperapi.Caller, _ helperapi.Empty) (helperapi.SSHStatus, error) {
		return s.Status(ctx), nil
	})
	locks := func(any) []string { return []string{"ssh", "firewall"} }
	RegisterOp(r, o, helperapi.ActionSSHSettings, OpSpec[helperapi.SSHSettingsRequest]{
		Locks: func(q helperapi.SSHSettingsRequest) []string { return locks(q) }, Run: s.settings})
	RegisterOp(r, o, helperapi.ActionSSHPort, OpSpec[helperapi.SSHPortRequest]{
		Locks: func(q helperapi.SSHPortRequest) []string { return append(locks(q), LockPkg) }, Run: s.addPort})
	RegisterOp(r, o, helperapi.ActionSSHPortFinish, OpSpec[helperapi.Empty]{
		Locks: func(q helperapi.Empty) []string { return locks(q) },
		Run: func(ctx context.Context, _ helperapi.Caller, _ helperapi.Empty, log io.Writer) (any, error) {
			return nil, s.FinishPort(ctx, log, false)
		}})
	RegisterOp(r, o, helperapi.ActionSSHPortCancel, OpSpec[helperapi.Empty]{
		Locks: func(q helperapi.Empty) []string { return locks(q) }, Run: s.cancelPort})
	Register(r, helperapi.ActionSSHKeys, func(_ context.Context, _ helperapi.Caller, q helperapi.SSHKeysRequest) (helperapi.SSHKeys, error) {
		return s.keys(q.User)
	})
	Register(r, helperapi.ActionSSHKeyAdd, s.addKey)
	Register(r, helperapi.ActionSSHKeyDelete, s.deleteKey)
	Register(r, helperapi.ActionSSHLogins, s.logins)
}

func sshErr(code string, params map[string]any) error {
	return &helperapi.Error{Code: code, Params: params}
}

func (s *SSH) path(p string) string { return filepath.Join(s.Root, p) }

func (s *SSH) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *SSH) installed() bool { return fileExists(s.path(sshdBinary)) }

// lock keeps SSH changes apart across processes (the port timer runs the
// helper on its own).
func (s *SSH) lock() (func(), error) {
	if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(s.StateDir, "ssh.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

func (s *SSH) load() sshState {
	var st sshState
	if data, err := os.ReadFile(filepath.Join(s.StateDir, "ssh.json")); err == nil {
		_ = json.Unmarshal(data, &st)
	}
	return st
}

func (s *SSH) save(st sshState) error {
	data, _ := json.MarshalIndent(st, "", "  ")
	return writeFileMode(filepath.Join(s.StateDir, "ssh.json"), data, 0o600)
}

// Effective configuration -------------------------------------------------

// sshdConfig is sshd -T: each key with its values in order.
type sshdConfig map[string][]string

func (c sshdConfig) get(k string) string {
	if v := c[k]; len(v) > 0 {
		return v[0]
	}
	return ""
}

func parseSSHDConfig(out string) sshdConfig {
	c := sshdConfig{}
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), " ")
		if ok {
			c[strings.ToLower(k)] = append(c[strings.ToLower(k)], strings.TrimSpace(v))
		}
	}
	return c
}

func (s *SSH) effective(ctx context.Context) (sshdConfig, error) {
	c, err := readSSHDConfig(ctx, s.Root)
	if err != nil {
		return nil, sshErr(helperapi.CodeSSHConfigInvalid, map[string]any{"output": errDetail(err)})
	}
	return c, nil
}

// ports are the Port options in effect: what the drop-in sets. Where sshd
// listens is another matter (see sshDetect).
func (c sshdConfig) ports() []int {
	var ports []int
	for _, v := range c["port"] {
		if n, err := strconv.Atoi(v); err == nil {
			ports = addPorts(ports, n)
		}
	}
	return ports
}

// sshStatePorts are the SSH ports the panel set, from the state kept in
// dir (none until the panel changed the port).
func sshStatePorts(dir string) []int {
	var st sshState
	data, err := os.ReadFile(filepath.Join(dir, "ssh.json")) //nolint:gosec // G304: the helper's own state file.
	if err != nil || json.Unmarshal(data, &st) != nil {
		return nil
	}
	ports := slices.Clone(st.Ports)
	if st.Pending != nil {
		ports = append(append(ports, st.Pending.OldPorts...), st.Pending.NewPort)
	}
	return ports
}

func rootLogin(v string) string {
	switch v {
	case "without-password", "prohibit-password":
		return helperapi.RootLoginKeysOnly
	case "yes", "no":
		return v
	case "forced-commands-only":
		return helperapi.RootLoginNo
	}
	return v
}

func (s *SSH) socketActivated(ctx context.Context) bool {
	if !systemdRunning() {
		return false
	}
	out, _ := queryCommand(ctx, "systemctl", "is-active", "ssh.socket")
	return strings.TrimSpace(out) == "active"
}

// Status describes the SSH server.
func (s *SSH) Status(ctx context.Context) helperapi.SSHStatus {
	st := helperapi.SSHStatus{Service: s.Service, Ports: []int{}, ListenAddresses: []string{}, Users: []helperapi.SSHUser{},
		Installed: s.installed()}
	state := s.load()
	st.Pending = state.Pending
	if s.Files != nil {
		st.RootKeys = s.Files.access().RootTerminal
	}
	if s.Firewall != nil {
		st.Firewall = s.Firewall.ActiveBackend(ctx)
	}
	for _, u := range s.users() {
		keys, _ := s.keys(u.Name)
		u.Keys = len(keys.Keys)
		st.Users = append(st.Users, u)
	}
	if !st.Installed {
		return st
	}
	st.SocketActivated = s.socketActivated(ctx)
	// The ports are read where the firewall reads them, so that this page
	// and the firewall's always-open rule cannot disagree; they are known
	// even while sshd's configuration cannot be read.
	if ports := sshDetect(ctx, s.Root).Ports; len(ports) > 0 {
		st.Ports = ports
	}
	c, err := s.effective(ctx)
	if err != nil {
		return st
	}
	for _, a := range c["listenaddress"] {
		if host, _, err := net.SplitHostPort(a); err == nil && !slices.Contains(st.ListenAddresses, host) {
			st.ListenAddresses = append(st.ListenAddresses, host)
		}
	}
	st.PasswordAuthentication = c.get("passwordauthentication") == "yes"
	st.PubkeyAuthentication = c.get("pubkeyauthentication") == "yes"
	st.PermitRootLogin = rootLogin(c.get("permitrootlogin"))
	return st
}

// Applying ----------------------------------------------------------------

func renderSSHDropIn(st sshState) []byte {
	var b strings.Builder
	b.WriteString(sshManagedNote)
	for _, p := range st.Ports {
		fmt.Fprintf(&b, "Port %d\n", p)
	}
	if st.Password != nil {
		v := "no"
		if *st.Password {
			v = "yes"
		}
		fmt.Fprintf(&b, "PasswordAuthentication %s\n", v)
		if !*st.Password {
			// keyboard-interactive asks for the password through PAM.
			b.WriteString("KbdInteractiveAuthentication no\n")
		}
	}
	if st.RootLogin != "" {
		fmt.Fprintf(&b, "PermitRootLogin %s\n", st.RootLogin)
	}
	return []byte(b.String())
}

var portLine = regexp.MustCompile(`(?mi)^[ \t]*Port[ \t]+[0-9]+[ \t]*$`)

// otherPortFiles finds active Port lines outside the drop-in: sshd adds
// every Port it reads, so they would keep listening.
func (s *SSH) otherPortFiles() []string {
	files := []string{sshMainConfig}
	if m, _ := filepath.Glob(s.path("/etc/ssh/sshd_config.d/*.conf")); len(m) > 0 {
		for _, f := range m {
			if rel := strings.TrimPrefix(f, s.Root); rel != sshDropIn {
				files = append(files, rel)
			}
		}
	}
	var out []string
	for _, f := range files {
		if data, err := os.ReadFile(s.path(f)); err == nil && portLine.Match(data) { //nolint:gosec // G304: sshd's own files.
			out = append(out, f)
		}
	}
	return out
}

// commentPorts comments out the Port lines of a file, keeping a copy.
func (s *SSH) commentPorts(f string, log io.Writer) error {
	p := s.path(f)
	data, err := os.ReadFile(p) //nolint:gosec // G304: sshd's own files.
	if err != nil {
		return err
	}
	info, err := os.Stat(p)
	if err != nil {
		return err
	}
	backup := filepath.Join(s.StateDir, "ssh-backup", strings.ReplaceAll(strings.TrimPrefix(f, "/"), "/", "_")+"."+strconv.FormatInt(s.clock().Unix(), 10))
	if err := os.MkdirAll(filepath.Dir(backup), 0o700); err != nil {
		return err
	}
	if err := writeFileMode(backup, data, 0o600); err != nil {
		return err
	}
	out := portLine.ReplaceAllFunc(data, func(l []byte) []byte {
		return append([]byte("# Moved to "+sshDropIn+" by Cairn: "), bytes.TrimSpace(l)...)
	})
	_, _ = fmt.Fprintf(log, "Commenting out the Port lines of %s (a copy is in %s).\n", f, backup)
	return writeFileMode(p, out, info.Mode().Perm())
}

// apply writes the drop-in, checks it, keeps the firewall and SELinux in
// step and reloads sshd. On failure the previous drop-in comes back.
func (s *SSH) apply(ctx context.Context, log io.Writer, st sshState, opened []int) error {
	if !s.installed() {
		return sshErr(helperapi.CodeSSHNotInstalled, nil)
	}
	before, err := s.effective(ctx)
	if err != nil {
		return err
	}
	p := s.path(sshDropIn)
	old, oldErr := os.ReadFile(p) //nolint:gosec // G304: a fixed path.
	put := func() {
		if oldErr == nil {
			_ = writeFileMode(p, old, 0o644)
		} else {
			_ = os.Remove(p)
		}
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil { //nolint:gosec // G301: sshd's include directory.
		return err
	}
	if !s.included() {
		return sshErr(helperapi.CodeSSHConflict, map[string]any{"key": "Include", "file": sshMainConfig})
	}
	if err := writeFileMode(p, renderSSHDropIn(st), 0o644); err != nil { //nolint:gosec // G306: sshd config is world-readable.
		return err
	}
	_, _ = fmt.Fprintf(log, "Writing %s\n", sshDropIn)
	var out bytes.Buffer
	if err := runCommand(ctx, io.MultiWriter(log, &out), s.path(sshdBinary), "-t"); err != nil {
		put()
		return sshErr(helperapi.CodeSSHConfigInvalid, map[string]any{"output": tailString(strings.TrimSpace(out.String()), 600)})
	}
	after, err := s.effective(ctx)
	if err != nil {
		put()
		return err
	}
	if st.Ports != nil && !slices.Equal(after.ports(), st.Ports) {
		for _, f := range s.otherPortFiles() {
			if err := s.commentPorts(f, log); err != nil {
				put()
				return err
			}
		}
		if after, err = s.effective(ctx); err != nil {
			put()
			return err
		}
		if !slices.Equal(after.ports(), st.Ports) {
			put()
			return sshErr(helperapi.CodeSSHConflict, map[string]any{"key": "Port", "file": sshMainConfig})
		}
	}
	if st.Password != nil && (after.get("passwordauthentication") == "yes") != *st.Password {
		put()
		return sshErr(helperapi.CodeSSHConflict, map[string]any{"key": "PasswordAuthentication", "file": s.definedIn("PasswordAuthentication")})
	}
	if st.RootLogin != "" && rootLogin(after.get("permitrootlogin")) != st.RootLogin {
		put()
		return sshErr(helperapi.CodeSSHConflict, map[string]any{"key": "PermitRootLogin", "file": s.definedIn("PermitRootLogin")})
	}
	for _, port := range opened {
		if err := s.labelPort(ctx, log, port); err != nil {
			put()
			return err
		}
	}
	portsChanged := !slices.Equal(before.ports(), after.ports())
	if portsChanged && s.Firewall != nil {
		// The SSH ports are protected rules: opening the new one before
		// sshd listens on it, closing the old one after.
		if err := s.Firewall.Refresh(ctx, log); err != nil {
			// The backend may have stopped halfway (ufw deletes the old
			// rule before it adds the new one): with the previous drop-in
			// back, the firewall gets the previous SSH ports again.
			put()
			if rerr := s.Firewall.Refresh(ctx, io.Discard); rerr != nil {
				_, _ = fmt.Fprintf(log, "Warning: the firewall could not be put back for the SSH ports: %v\n", rerr)
			}
			return err
		}
	}
	if err := s.reload(ctx, log, portsChanged); err != nil {
		put()
		_ = s.reload(ctx, io.Discard, portsChanged)
		if portsChanged && s.Firewall != nil {
			_ = s.Firewall.Refresh(ctx, io.Discard)
		}
		return err
	}
	for _, port := range opened {
		if err := s.listening(ctx, after, port); err != nil {
			put()
			_ = s.reload(ctx, io.Discard, portsChanged)
			if s.Firewall != nil {
				_ = s.Firewall.Refresh(ctx, io.Discard)
			}
			return err
		}
	}
	if portsChanged && s.Firewall != nil {
		// The firewall keeps open what an SSH server listens on as well as
		// what its configuration says, and when the rules were made above
		// sshd still held the ports that are going. Now that it has
		// reloaded and let go of them, the rules are made once more, or
		// those ports would stay open until the next change.
		want := after.listenPorts()
		for i := 0; i < 20; i++ {
			if !slices.ContainsFunc(sshListeners(s.Root), func(p int) bool { return !slices.Contains(want, p) }) {
				break
			}
			time.Sleep(sshProbePause)
		}
		if err := s.Firewall.Refresh(ctx, log); err != nil {
			_, _ = fmt.Fprintf(log, "Warning: the firewall could not be brought up to date with the SSH ports: %v\n", err)
		}
	}
	return nil
}

// included reports whether sshd_config reads sshd_config.d (Debian 12+,
// Ubuntu 22.04+ and EL 9+ do, at the top).
func (s *SSH) included() bool {
	data, err := os.ReadFile(s.path(sshMainConfig))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && strings.EqualFold(f[0], "Include") && strings.Contains(line, "sshd_config.d/") {
			return true
		}
		if len(f) > 0 && strings.EqualFold(f[0], "Match") {
			return false
		}
	}
	return false
}

// definedIn names the first file that sets key, for the error message.
func (s *SSH) definedIn(key string) string {
	files := []string{sshMainConfig}
	m, _ := filepath.Glob(s.path("/etc/ssh/sshd_config.d/*.conf"))
	sort.Strings(m)
	for _, f := range m {
		files = append(files, strings.TrimPrefix(f, s.Root))
	}
	re := regexp.MustCompile(`(?mi)^[ \t]*` + key + `[ \t]`)
	for _, f := range files {
		if f == sshDropIn {
			continue
		}
		if data, err := os.ReadFile(s.path(f)); err == nil && re.Match(data) { //nolint:gosec // G304: sshd's own files.
			return f
		}
	}
	return sshMainConfig
}

// reload makes sshd read its configuration. On Ubuntu's socket activation
// the ports belong to ssh.socket, generated from sshd_config.
func (s *SSH) reload(ctx context.Context, log io.Writer, portsChanged bool) error {
	if !systemdRunning() {
		return nil
	}
	if portsChanged && s.socketActivated(ctx) {
		if err := runCommand(ctx, log, "systemctl", "daemon-reload"); err != nil {
			return err
		}
		return runCommand(ctx, log, "systemctl", "restart", "ssh.socket")
	}
	// sshd re-executes itself on reload and binds the new ports.
	return runCommand(ctx, log, "systemctl", "try-reload-or-restart", s.Service)
}

// sshdDial checks that sshd answers on a port.
var sshdDial = func(ctx context.Context, addr string) error {
	d := net.Dialer{Timeout: 3 * time.Second}
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil && line == "" {
		return err
	}
	if !strings.HasPrefix(line, "SSH-") {
		return errors.New("not an SSH server")
	}
	return nil
}

// listening waits until sshd answers on the new port, on any of its
// listen addresses.
func (s *SSH) listening(ctx context.Context, c sshdConfig, port int) error {
	var hosts []string
	for _, a := range c["listenaddress"] {
		h, _, err := net.SplitHostPort(a)
		if err != nil {
			continue
		}
		switch h {
		case "0.0.0.0":
			h = "127.0.0.1"
		case "::":
			h = "::1"
		}
		if !slices.Contains(hosts, h) {
			hosts = append(hosts, h)
		}
	}
	if len(hosts) == 0 {
		hosts = []string{"127.0.0.1"}
	}
	var err error
	for i := 0; i < 10; i++ {
		for _, h := range hosts {
			if err = sshdDial(ctx, net.JoinHostPort(h, strconv.Itoa(port))); err == nil {
				return nil
			}
		}
		time.Sleep(sshProbePause)
	}
	return sshErr(helperapi.CodeSSHNotListening, map[string]any{"port": port, "detail": err.Error()})
}

// sshProbePause is the wait between two tries to reach sshd on a new port;
// sshWatchEvery how often a pending port change looks for a connection
// from another host. Tests shorten both.
var (
	sshProbePause = 500 * time.Millisecond
	sshWatchEvery = 3 * time.Second
)

// labelPort lets sshd bind a port under SELinux.
func (s *SSH) labelPort(ctx context.Context, log io.Writer, port int) error {
	mode, err := queryCommand(ctx, "getenforce")
	if err != nil || strings.TrimSpace(mode) == "Disabled" {
		return nil //nolint:nilerr // no SELinux
	}
	if !fileExists(s.path("/usr/sbin/semanage")) {
		if s.Software == nil || s.Software.Manager != "dnf" {
			return sshErr(helperapi.CodeSSHSELinuxPort, map[string]any{"port": port, "type": "semanage_missing"})
		}
		if err := s.Software.ensureLibraries(ctx, log, []string{"policycoreutils-python-utils"}); err != nil {
			return err
		}
	}
	out, err := queryCommand(ctx, "/usr/sbin/semanage", "port", "-l", "-n")
	if err != nil {
		return sshErr(helperapi.CodeSSHSELinuxPort, map[string]any{"port": port, "type": "semanage_failed"})
	}
	if t := selinuxPortType(out, port); t == "ssh_port_t" {
		return nil
	} else if t != "" {
		return sshErr(helperapi.CodeSSHSELinuxPort, map[string]any{"port": port, "type": t})
	}
	return runCommand(ctx, log, "/usr/sbin/semanage", "port", "-a", "-t", "ssh_port_t", "-p", "tcp", strconv.Itoa(port))
}

// genericPortTypes cover whole ranges; a port of theirs can still get a
// type of its own.
var genericPortTypes = []string{"unreserved_port_t", "reserved_port_t", "hi_reserved_port_t", "ephemeral_port_t"}

// selinuxPortType finds the type a TCP port is labeled with in
// "semanage port -l" ("http_port_t  tcp  80, 81, 8008-8009"), ignoring the
// generic range types.
func selinuxPortType(list string, port int) string {
	found := ""
	for _, line := range strings.Split(list, "\n") {
		f := strings.Fields(strings.ReplaceAll(line, ",", " "))
		if len(f) < 3 || f[1] != "tcp" || slices.Contains(genericPortTypes, f[0]) {
			continue
		}
		for _, p := range f[2:] {
			lo, hi, ok := helperapi.ParsePortRange(p)
			if ok && lo <= port && port <= hi {
				if f[0] == "ssh_port_t" {
					return f[0]
				}
				found = f[0]
			}
		}
	}
	return found
}

// Settings ----------------------------------------------------------------

func (s *SSH) settings(ctx context.Context, _ helperapi.Caller, q helperapi.SSHSettingsRequest, log io.Writer) (any, error) {
	unlock, err := s.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	// Without sshd there is nothing to lock anyone out of: apply says so.
	if s.installed() {
		c, err := s.effective(ctx)
		if err != nil {
			return nil, err
		}
		if err := s.leavesAWayIn(c, sshWay{password: q.PasswordAuthentication, rootLogin: q.PermitRootLogin}); err != nil {
			return nil, err
		}
	}
	st := s.load()
	st.Password, st.RootLogin = &q.PasswordAuthentication, q.PermitRootLogin
	if err := s.apply(ctx, log, st, nil); err != nil {
		return nil, err
	}
	return nil, s.save(st)
}

// keepsAWayIn refuses a key deletion after which nobody could log in
// (kept are the lines user's file keeps), under the settings sshd has now.
func (s *SSH) keepsAWayIn(ctx context.Context, user string, kept []string) error {
	if !s.installed() {
		return nil
	}
	c, err := s.effective(ctx)
	if err != nil {
		return nil //nolint:nilerr // sshd's configuration unknown: the key goes as asked
	}
	return s.leavesAWayIn(c, sshWay{password: passwordsOn(c), rootLogin: rootLogin(c.get("permitrootlogin")), keysOf: user, kept: kept})
}

// Who can still log in ----------------------------------------------------
//
// A change of the login settings, or a key that goes, must leave someone a
// way in: on many servers root is the only account, and nobody can then
// undo the change over SSH. Whether a way in is left is worked out from
// what sshd -T says and from the account files. sshd may know ways the
// panel cannot see (accounts from a directory service, AuthenticationMethods
// asking for several methods at once): those count as not there, so the
// panel errs on refusing.

// sshWay is how sshd lets users in after a change: whether it takes
// passwords, how root may log in, and the keys file of one user as it will
// be (the lines in kept; every other file stays as it is).
type sshWay struct {
	password  bool
	rootLogin string
	keysOf    string
	kept      []string
}

// leavesAWayIn refuses a change after which nobody could log in.
func (s *SSH) leavesAWayIn(c sshdConfig, w sshWay) error {
	if s.loginLeft(c, w) {
		return nil
	}
	var params map[string]any
	if w.keysOf != "" {
		params = map[string]any{"user": w.keysOf}
	}
	if !w.password {
		return sshErr(helperapi.CodeSSHNoKeys, params)
	}
	return sshErr(helperapi.CodeSSHNoLogin, params)
}

// passwordsOn reports whether sshd takes passwords: as such, or asked for
// through PAM by keyboard-interactive.
func passwordsOn(c sshdConfig) bool {
	if c.get("passwordauthentication") == "yes" {
		return true
	}
	kbd := c.get("kbdinteractiveauthentication")
	if kbd == "" {
		kbd = c.get("challengeresponseauthentication") // OpenSSH before 8.7
	}
	return kbd == "yes" && c.get("usepam") == "yes"
}

// loginLeft reports whether some account could log in over SSH the way w
// says: allowed in by sshd, not expired, and with a password sshd takes or
// a key sshd reads.
func (s *SSH) loginLeft(c sshdConfig, w sshWay) bool {
	shadow := s.shadow()
	now := s.clock()
	var groups map[string][]string
	if len(c["allowgroups"]) > 0 || len(c["denygroups"]) > 0 {
		groups = s.groups()
	}
	for _, u := range s.users() {
		password := w.password
		if u.Root {
			switch w.rootLogin {
			case helperapi.RootLoginNo:
				continue
			case helperapi.RootLoginKeysOnly:
				password = false
			}
		}
		account, err := s.account(u.Name)
		if err != nil {
			continue
		}
		sh := shadow[u.Name]
		if sh.expired(now) || !sshdAdmits(c, u.Name, groupsOf(groups, u.Name, account.gid)) {
			continue
		}
		if password && sh.password {
			return true
		}
		if s.keyLeft(c, account, w, now) {
			return true
		}
	}
	return false
}

// keyLeft reports whether a user could log in with a key: sshd takes keys
// and reads the file the panel keeps them in, and a key there lets the user
// in. Keys from elsewhere (AuthorizedKeysCommand, certificates of a trusted
// CA) the panel does not see; when sshd takes them, every user is assumed
// to have one.
func (s *SSH) keyLeft(c sshdConfig, u passwdEntry, w sshWay, now time.Time) bool {
	if c.get("pubkeyauthentication") == "no" {
		return false
	}
	for _, k := range []string{"authorizedkeyscommand", "trustedusercakeys"} {
		if v := c.get(k); v != "" && v != "none" {
			return true
		}
	}
	if !keysFileRead(c, u) {
		return false
	}
	lines := w.kept
	if u.name != w.keysOf {
		data, err := s.readKeys(u)
		if err != nil {
			return false
		}
		lines = strings.Split(string(data), "\n")
	}
	return slices.ContainsFunc(lines, func(l string) bool { return loginKey(l, now) })
}

// keysFileRead reports whether sshd reads ~/.ssh/authorized_keys, the file
// the panel keeps a user's keys in (AuthorizedKeysFile: %h, %u and %% are
// expanded, a relative path is in the home).
func keysFileRead(c sshdConfig, u passwdEntry) bool {
	files := []string{".ssh/authorized_keys"} // sshd's default
	if v, ok := c["authorizedkeysfile"]; ok {
		files = nil
		for _, line := range v {
			files = append(files, strings.Fields(line)...)
		}
	}
	want := filepath.Join(u.home, ".ssh/authorized_keys")
	expand := strings.NewReplacer("%%", "%", "%h", u.home, "%u", u.name)
	for _, f := range files {
		if f == "none" {
			continue
		}
		f = expand.Replace(f)
		if !filepath.IsAbs(f) {
			f = filepath.Join(u.home, f)
		}
		if filepath.Clean(f) == want {
			return true
		}
	}
	return false
}

// loginKey reports whether an authorized_keys line lets its user log in: a
// key not bound to a forced command (which runs instead of the user's
// shell), and not past its expiry-time.
func loginKey(line string, now time.Time) bool {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return false
	}
	_, _, options, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return false
	}
	for _, o := range options {
		name, value, _ := strings.Cut(o, "=")
		switch strings.ToLower(name) {
		case "command":
			return false
		case "expiry-time":
			// sshd ignores a line whose time it cannot read.
			if until, ok := keyExpiry(strings.Trim(value, `"`)); !ok || !now.Before(until) {
				return false
			}
		}
	}
	return true
}

// keyExpiry reads an expiry-time: YYYYMMDD[HHMM[SS]], in local time unless
// it ends in Z.
func keyExpiry(v string) (time.Time, bool) {
	loc := time.Local
	if s, ok := strings.CutSuffix(v, "Z"); ok {
		v, loc = s, time.UTC
	}
	var layout string
	switch len(v) {
	case 8:
		layout = "20060102"
	case 12:
		layout = "200601021504"
	case 14:
		layout = "20060102150405"
	default:
		return time.Time{}, false
	}
	t, err := time.ParseInLocation(layout, v, loc)
	return t, err == nil
}

// shadowEntry is what /etc/shadow says about an account's logins.
type shadowEntry struct {
	// password: a password hash sshd can check; not a locked one ("!..."),
	// none ("*"), or an empty one, which sshd refuses unless
	// PermitEmptyPasswords is on.
	password bool
	// expireDay is the day (since 1970) from which the account is expired,
	// for every way of logging in; -1 when it does not expire.
	expireDay int64
}

func (e shadowEntry) expired(now time.Time) bool {
	return e.expireDay >= 0 && now.Unix()/86400 >= e.expireDay
}

// shadow reads /etc/shadow; an account without an entry has no password
// and does not expire.
func (s *SSH) shadow() map[string]shadowEntry {
	out := map[string]shadowEntry{}
	data, err := os.ReadFile(s.path("/etc/shadow")) //nolint:gosec // G304: the system's shadow file.
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Split(line, ":")
		if len(f) < 2 || f[0] == "" {
			continue
		}
		e := shadowEntry{expireDay: -1}
		e.password = f[1] != "" && !strings.HasPrefix(f[1], "!") && !strings.HasPrefix(f[1], "*")
		if len(f) > 7 && f[7] != "" {
			if n, err := strconv.ParseInt(f[7], 10, 64); err == nil {
				e.expireDay = n
			}
		}
		out[f[0]] = e
	}
	return out
}

// groups reads /etc/group: the members of each group, by group name, and
// "#<gid>" naming each group's name.
func (s *SSH) groups() map[string][]string {
	out := map[string][]string{}
	data, err := os.ReadFile(s.path("/etc/group")) //nolint:gosec // G304: the system's group file.
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Split(line, ":")
		if len(f) != 4 || f[0] == "" {
			continue
		}
		out["#"+f[2]] = []string{f[0]}
		out[f[0]] = strings.Split(f[3], ",")
	}
	return out
}

// groupsOf names the groups of a user: the primary one and those listing
// the user as a member.
func groupsOf(groups map[string][]string, user string, gid int) []string {
	out := groups["#"+strconv.Itoa(gid)]
	out = slices.Clone(out)
	for name, members := range groups {
		if !strings.HasPrefix(name, "#") && slices.Contains(members, user) && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}

// sshdAdmits reports whether DenyUsers, AllowUsers, DenyGroups and
// AllowGroups let a user in, from some address: the host part of a
// "user@host" pattern limits where from, so it denies nobody everywhere and
// allows the user from somewhere.
func sshdAdmits(c sshdConfig, user string, groups []string) bool {
	matches := func(key string, names []string, hosts bool) (set, hit bool) {
		for _, line := range c[key] {
			for _, p := range strings.Fields(line) {
				set = true
				p, host, _ := strings.Cut(p, "@")
				if host != "" && !hosts {
					continue
				}
				for _, n := range names {
					if sshdPattern(p, n) {
						hit = true
					}
				}
			}
		}
		return set, hit
	}
	if _, hit := matches("denyusers", []string{user}, false); hit {
		return false
	}
	if set, hit := matches("allowusers", []string{user}, true); set && !hit {
		return false
	}
	if _, hit := matches("denygroups", groups, false); hit {
		return false
	}
	if set, hit := matches("allowgroups", groups, false); set && !hit {
		return false
	}
	return true
}

// sshdPattern matches a name against an sshd pattern: * stands for any run
// of characters, ? for one.
func sshdPattern(pattern, name string) bool {
	var b strings.Builder
	b.WriteString("^")
	for _, r := range pattern {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	ok, err := regexp.MatchString(b.String(), name)
	return err == nil && ok
}

// Ports -------------------------------------------------------------------

// listeningPorts reads the TCP ports in LISTEN state from /proc.
func (s *SSH) listeningPorts() map[int]bool { return procPorts(s.Root, false) }

// procPorts reads the local ports of the sockets in /proc/net under root
// (prefix; "" for /): TCP ports in LISTEN state, or the ports UDP sockets
// are bound to (in any state: a connected one holds its port too).
func procPorts(root string, udp bool) map[int]bool {
	files := []string{"/proc/net/tcp", "/proc/net/tcp6"}
	if udp {
		files = []string{"/proc/net/udp", "/proc/net/udp6"}
	}
	out := map[int]bool{}
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(root, f)) //nolint:gosec // G304: fixed /proc files.
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n")[1:] {
			fl := strings.Fields(line)
			if len(fl) < 4 || !udp && fl[3] != "0A" { // 0A: TCP_LISTEN
				continue
			}
			if i := strings.LastIndex(fl[1], ":"); i >= 0 {
				if n, err := strconv.ParseInt(fl[1][i+1:], 16, 32); err == nil {
					out[int(n)] = true
				}
			}
		}
	}
	return out
}

func (s *SSH) addPort(ctx context.Context, _ helperapi.Caller, q helperapi.SSHPortRequest, log io.Writer) (any, error) {
	unlock, err := s.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	st := s.load()
	if st.Pending != nil {
		return nil, sshErr(helperapi.CodeSSHPending, nil)
	}
	c, err := s.effective(ctx)
	if err != nil {
		return nil, err
	}
	current := c.ports()
	if slices.Contains(current, q.Port) {
		return nil, sshErr(helperapi.CodeSSHPortInvalid, map[string]any{"port": q.Port, "reason": "current"})
	}
	if s.Firewall != nil && q.Port == s.Firewall.PanelPort || s.listeningPorts()[q.Port] {
		return nil, sshErr(helperapi.CodeSSHPortInUse, map[string]any{"port": q.Port})
	}
	// A port forward takes the connections before sshd sees them.
	if s.Firewall != nil && s.Firewall.forwardsTCP(q.Port) {
		return nil, sshErr(helperapi.CodeSSHPortInUse, map[string]any{"port": q.Port, "forward": true})
	}
	next := st
	next.Ports = slices.Sorted(slices.Values(append(slices.Clone(current), q.Port)))
	closeAt := s.clock().Add(time.Duration(q.CloseAfterMinutes) * time.Minute)
	next.Pending = &helperapi.SSHPortPending{OldPorts: current, NewPort: q.Port, CloseAt: closeAt.Unix()}
	_, _ = fmt.Fprintf(log, "Adding SSH port %d next to %v; the old ports close at %s.\n", q.Port, current, closeAt.UTC().Format(time.RFC3339))
	if err := s.apply(ctx, log, next, []int{q.Port}); err != nil {
		return nil, err
	}
	if err := s.save(next); err != nil {
		return nil, err
	}
	if systemdRunning() {
		_ = runCommand(ctx, io.Discard, "systemctl", "stop", sshPortTimer+".timer")
		if err := runCommand(ctx, log, "systemd-run", "--unit="+sshPortTimer, "--description=Cairn: close the old SSH ports",
			"--on-active="+strconv.Itoa(q.CloseAfterMinutes*60), "--timer-property=AccuracySec=1s",
			"--property=CollectMode=inactive-or-failed", "--property=Type=oneshot", s.Executable, "ssh", "finish-port"); err != nil {
			_, _ = fmt.Fprintf(log, "Warning: the timer that closes the old ports could not start; close them on the SSH page.\n")
		}
	}
	_, _ = fmt.Fprintf(log, "When the time is up, the old ports close only if a connection from another host has reached port %d by then.\n", q.Port)
	s.watchPort()
	return next.Pending, nil
}

// FinishPort closes the old ports of a port change. Asked for in the panel,
// it only needs sshd to answer on the new port: the person asking says the
// port works. Unattended (the change's timer) it also needs a connection
// from another host to have reached the new port since it was opened.
// That sshd answers on this host says nothing about a security group or a
// firewall in front of it, and closing the port that works on a guess is
// how an administrator gets locked out. Without that proof both ports stay
// open and the change stays pending, for a person to finish or cancel.
func (s *SSH) FinishPort(ctx context.Context, log io.Writer, unattended bool) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	st := s.load()
	if st.Pending == nil {
		return sshErr(helperapi.CodeSSHNoPending, nil)
	}
	port := st.Pending.NewPort
	c, err := s.effective(ctx)
	if err == nil {
		err = s.listening(ctx, c, port)
	}
	if err != nil {
		_, _ = fmt.Fprintf(log, "sshd does not answer on port %d: the old ports %v stay open.\n", port, st.Pending.OldPorts)
		return s.holdPort(st, unattended, helperapi.SSHHeldNotListening, err)
	}
	if unattended && st.Pending.ReachedAt == 0 && reachedFromOutside(s.Root, port) == "" {
		_, _ = fmt.Fprintf(log, "No connection from another host has reached port %d: the old ports %v stay open. "+
			"Log in through port %d, then close the old ports on the SSH page.\n", port, st.Pending.OldPorts, port)
		return s.holdPort(st, true, helperapi.SSHHeldUnreached,
			sshErr(helperapi.CodeSSHPortUnreached, map[string]any{"port": port, "old": st.Pending.OldPorts}))
	}
	next := st
	next.Ports = []int{st.Pending.NewPort}
	next.Pending = nil
	_, _ = fmt.Fprintf(log, "Closing the old SSH ports %v; SSH stays on %d.\n", st.Pending.OldPorts, st.Pending.NewPort)
	if err := s.apply(ctx, log, next, nil); err != nil {
		return err
	}
	s.stopTimer(ctx)
	return s.save(next)
}

// holdPort records why the timer left the old ports open (its one run is
// over: from now on the change waits for a person) and returns cause.
func (s *SSH) holdPort(st sshState, unattended bool, reason string, cause error) error {
	if !unattended {
		return cause
	}
	pending := *st.Pending
	pending.Held = reason
	st.Pending = &pending
	if err := s.save(st); err != nil {
		return errors.Join(cause, err)
	}
	if s.Log != nil {
		s.Log.Warn("the old SSH ports were not closed when the time of the port change ran out", "new", pending.NewPort,
			"old", pending.OldPorts, "reason", reason)
	}
	return cause
}

// Resume takes up a port change that was pending when the helper started,
// and makes later ones look out for a connection from another host too.
func (s *SSH) Resume() {
	s.watch = true
	s.watchPort()
}

// watchPort looks out for the first connection from another host to the
// new port of the pending port change and records when it came: what lets
// the timer close the old ports. A connection may be short (someone logs
// in to check and leaves), so the state at the moment the timer runs is
// not enough.
func (s *SSH) watchPort() {
	if !s.watch {
		return
	}
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	if s.watching || s.load().Pending == nil {
		return
	}
	s.watching = true
	go func() {
		for {
			// Deciding to stop and saying so are one step: a change made
			// meanwhile either finds this lookout or starts its own.
			s.watchMu.Lock()
			p := s.load().Pending
			if p == nil || p.ReachedAt != 0 {
				s.watching = false
				s.watchMu.Unlock()
				return
			}
			s.watchMu.Unlock()
			if peer := reachedFromOutside(s.Root, p.NewPort); peer != "" {
				s.reached(p.NewPort, peer)
			}
			time.Sleep(sshWatchEvery)
		}
	}()
}

// reached records that a connection from another host arrived on the new
// port of the pending change.
func (s *SSH) reached(port int, peer string) {
	unlock, err := s.lock()
	if err != nil {
		return
	}
	defer unlock()
	st := s.load()
	if st.Pending == nil || st.Pending.NewPort != port || st.Pending.ReachedAt != 0 {
		return
	}
	pending := *st.Pending
	pending.ReachedAt = s.clock().Unix()
	st.Pending = &pending
	if err := s.save(st); err == nil && s.Log != nil {
		s.Log.Info("a connection from another host reached the new SSH port", "port", port, "peer", peer)
	}
}

func (s *SSH) cancelPort(ctx context.Context, _ helperapi.Caller, _ helperapi.Empty, log io.Writer) (any, error) {
	unlock, err := s.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	st := s.load()
	if st.Pending == nil {
		return nil, sshErr(helperapi.CodeSSHNoPending, nil)
	}
	next := st
	next.Ports = st.Pending.OldPorts
	next.Pending = nil
	_, _ = fmt.Fprintf(log, "Keeping SSH on %v; port %d closes.\n", st.Pending.OldPorts, st.Pending.NewPort)
	if err := s.apply(ctx, log, next, nil); err != nil {
		return nil, err
	}
	s.stopTimer(ctx)
	return nil, s.save(next)
}

func (s *SSH) stopTimer(ctx context.Context) {
	if systemdRunning() {
		_, _ = queryCommand(ctx, "systemctl", "stop", sshPortTimer+".timer")
	}
}

// Users and keys ----------------------------------------------------------

// users are root and the ordinary login users (not site owners).
func (s *SSH) users() []helperapi.SSHUser {
	out := []helperapi.SSHUser{{Name: "root", UID: 0, Root: true}}
	if s.Files == nil {
		return out
	}
	var users []helperapi.SSHUser
	for _, e := range s.Files.passwd() {
		if e.uid >= 1000 && e.uid < 60000 && loginShell(e.shell) && !strings.HasPrefix(e.name, "cw-") {
			users = append(users, helperapi.SSHUser{Name: e.name, UID: e.uid})
		}
	}
	sort.Slice(users, func(i, j int) bool { return users[i].Name < users[j].Name })
	return append(out, users...)
}

// account finds a managed user's passwd entry.
func (s *SSH) account(name string) (passwdEntry, error) {
	if name == "root" {
		return passwdEntry{name: "root", home: "/root"}, nil
	}
	if s.Files != nil && slices.ContainsFunc(s.users(), func(u helperapi.SSHUser) bool { return u.Name == name }) {
		for _, e := range s.Files.passwd() {
			if e.name == name {
				return e, nil
			}
		}
	}
	return passwdEntry{}, sshErr(helperapi.CodeSSHUserNotFound, map[string]any{"user": name})
}

func (s *SSH) readKeys(u passwdEntry) ([]byte, error) {
	root, err := os.OpenRoot(s.path(u.home))
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	// The user owns the file: a pipe or a huge sparse file there must not
	// hang the helper (it reads under the SSH lock) or fill its memory.
	data, err := readRegular(root, ".ssh/authorized_keys", maxAuthorizedKeys)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return data, err
}

// maxAuthorizedKeys caps the authorized_keys the panel reads and writes.
const maxAuthorizedKeys = 1 << 20

func parseKeyLine(line string) (helperapi.SSHKey, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return helperapi.SSHKey{}, false
	}
	pub, comment, options, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return helperapi.SSHKey{}, false
	}
	return helperapi.SSHKey{Type: pub.Type(), Fingerprint: ssh.FingerprintSHA256(pub), Comment: comment, Options: strings.Join(options, ",")}, true
}

func (s *SSH) keys(user string) (helperapi.SSHKeys, error) {
	out := helperapi.SSHKeys{Keys: []helperapi.SSHKey{}}
	u, err := s.account(user)
	if err != nil {
		return out, err
	}
	data, err := s.readKeys(u)
	if err != nil {
		return out, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if k, ok := parseKeyLine(line); ok {
			out.Keys = append(out.Keys, k)
		}
	}
	return out, nil
}

// writeKeys replaces a user's authorized_keys inside their home (symbolic
// links out of it are refused), owned by the user, mode 0600.
func (s *SSH) writeKeys(u passwdEntry, data []byte) error {
	root, err := os.OpenRoot(s.path(u.home))
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err := root.Mkdir(".ssh", 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	if err := root.Lchown(".ssh", u.uid, u.gid); err != nil {
		return err
	}
	// The user owns .ssh: a new file of a random name (O_EXCL never opens
	// what is there, a link or a hard link the user made), owned and
	// filled through its descriptor, then renamed into place.
	return writeOwned(root, ".ssh/authorized_keys", data, 0o600, u.uid, u.gid)
}

func (s *SSH) rootKeysAllowed(user string) error {
	if user == "root" && (s.Files == nil || !s.Files.access().RootTerminal) {
		return sshErr(helperapi.CodeSSHRootDisabled, nil)
	}
	return nil
}

var allowedKeyTypes = []string{ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521,
	ssh.KeyAlgoSKED25519, ssh.KeyAlgoSKECDSA256, ssh.KeyAlgoRSA}

func (s *SSH) addKey(_ context.Context, _ helperapi.Caller, q helperapi.SSHKeyAddRequest) (helperapi.SSHKey, error) {
	if err := s.rootKeysAllowed(q.User); err != nil {
		return helperapi.SSHKey{}, err
	}
	// Read, change, write: one change at a time, and none while the
	// settings check that a key is left.
	unlock, err := s.lock()
	if err != nil {
		return helperapi.SSHKey{}, err
	}
	defer unlock()
	u, err := s.account(q.User)
	if err != nil {
		return helperapi.SSHKey{}, err
	}
	pub, comment, options, rest, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(q.Key)))
	if err != nil || len(options) > 0 || len(bytes.TrimSpace(rest)) > 0 || !slices.Contains(allowedKeyTypes, pub.Type()) {
		return helperapi.SSHKey{}, sshErr(helperapi.CodeSSHKeyInvalid, nil)
	}
	if ck, ok := pub.(ssh.CryptoPublicKey); ok && pub.Type() == ssh.KeyAlgoRSA {
		if k, ok := ck.CryptoPublicKey().(interface{ Size() int }); ok && k.Size()*8 < 2048 {
			return helperapi.SSHKey{}, sshErr(helperapi.CodeSSHKeyInvalid, map[string]any{"reason": "rsa_short"})
		}
	}
	key := helperapi.SSHKey{Type: pub.Type(), Fingerprint: ssh.FingerprintSHA256(pub), Comment: strings.TrimSpace(comment)}
	have, err := s.keys(q.User)
	if err != nil {
		return key, err
	}
	if slices.ContainsFunc(have.Keys, func(k helperapi.SSHKey) bool { return k.Fingerprint == key.Fingerprint }) {
		return key, sshErr(helperapi.CodeSSHKeyExists, map[string]any{"fingerprint": key.Fingerprint})
	}
	data, err := s.readKeys(u)
	if err != nil {
		return key, err
	}
	if len(data) > 0 && !bytes.HasSuffix(data, []byte("\n")) {
		data = append(data, '\n')
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
	if key.Comment != "" {
		line += " " + strings.Map(func(r rune) rune {
			if r < 0x20 {
				return -1
			}
			return r
		}, key.Comment)
	}
	data = append(data, []byte(line+"\n")...)
	if len(data) > maxAuthorizedKeys {
		return key, sshErr(helperapi.CodeSSHKeyInvalid, map[string]any{"reason": "too_many"})
	}
	return key, s.writeKeys(u, data)
}

func (s *SSH) deleteKey(ctx context.Context, _ helperapi.Caller, q helperapi.SSHKeyDeleteRequest) (helperapi.Empty, error) {
	if err := s.rootKeysAllowed(q.User); err != nil {
		return helperapi.Empty{}, err
	}
	unlock, err := s.lock()
	if err != nil {
		return helperapi.Empty{}, err
	}
	defer unlock()
	u, err := s.account(q.User)
	if err != nil {
		return helperapi.Empty{}, err
	}
	data, err := s.readKeys(u)
	if err != nil {
		return helperapi.Empty{}, err
	}
	var kept []string
	found := false
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		if k, ok := parseKeyLine(line); ok && k.Fingerprint == q.Fingerprint {
			found = true
			continue
		}
		kept = append(kept, line)
	}
	if !found {
		return helperapi.Empty{}, sshErr(helperapi.CodeSSHKeyInvalid, map[string]any{"reason": "not_found"})
	}
	if err := s.keepsAWayIn(ctx, q.User, kept); err != nil {
		return helperapi.Empty{}, err
	}
	out := strings.Join(kept, "\n")
	if out != "" {
		out += "\n"
	}
	return helperapi.Empty{}, s.writeKeys(u, []byte(out))
}

// Logins ------------------------------------------------------------------

// The name of an unknown user is whatever the client sent, spaces and all,
// and sshd writes the address, the port (and "ssh2") after it, last. So a
// name takes everything up to the last " from ": one made to look like an
// address cannot stand in for the real one.
var (
	loginAttempt = regexp.MustCompile(`^(Accepted|Failed) (\S+) for (invalid user )?(.*) from (\S+) port [0-9]+ ssh2(?:: .*)?$`)
	loginInvalid = regexp.MustCompile(`^Invalid user (.*) from (\S+) port [0-9]+$`)
)

func parseLogin(msg string) (helperapi.SSHLogin, bool) {
	var l helperapi.SSHLogin
	if m := loginAttempt.FindStringSubmatch(msg); m != nil {
		l = helperapi.SSHLogin{Method: strings.TrimSuffix(m[2], "/pam"), User: m[4], IP: m[5], Result: helperapi.LoginFailed}
		switch {
		case m[1] == "Accepted":
			l.Result = helperapi.LoginAccepted
		case m[3] != "":
			l.Result = helperapi.LoginInvalidUser
		}
	} else if m := loginInvalid.FindStringSubmatch(msg); m != nil {
		l = helperapi.SSHLogin{User: m[1], IP: m[2], Result: helperapi.LoginInvalidUser}
	} else {
		return l, false
	}
	// sshd writes the client's address, never a name.
	if _, err := netip.ParseAddr(l.IP); err != nil {
		return helperapi.SSHLogin{}, false
	}
	return l, true
}

// sshUnits are the units sshd runs in: its service (ssh on Debian and
// Ubuntu, sshd elsewhere), and the instances a socket with Accept=yes
// starts, one per connection.
var (
	sshUnits    = []string{"ssh.service", "sshd.service", "ssh@*.service", "sshd@*.service"}
	sshUnitName = regexp.MustCompile(`^sshd?(@[^/]+)?\.service$`)
)

// sshdUIDs are the users sshd logs as: root, and the unprivileged user its
// pre-authentication process runs as (OpenSSH 10's sshd-auth; its lines
// normally reach the journal through the privileged process).
func (s *SSH) sshdUIDs() []string {
	uids := []string{"0"}
	if s.Files != nil {
		for _, e := range s.Files.passwd() {
			if e.name == "sshd" {
				uids = append(uids, strconv.Itoa(e.uid))
			}
		}
	}
	return uids
}

// fromSSHD reports whether a journal entry was written by sshd. The
// identifier and the message are whatever the writer says, and any local
// user (a website's code among them) can log as "sshd"; the unit and the
// user are journald's, taken from the writer's control group and
// credentials. The user is checked too: where no PAM session moves a
// logged-in user's processes out of sshd's unit, they would run in it.
func fromSSHD(fields map[string]json.RawMessage, uids []string) bool {
	return sshUnitName.MatchString(journalField(fields["_SYSTEMD_UNIT"])) && slices.Contains(uids, journalField(fields["_UID"]))
}

// usecArg turns a cursor (microseconds) into a journalctl time.
func usecArg(cursor string, delta int64) string {
	n, _ := strconv.ParseInt(cursor, 10, 64)
	n += delta
	return fmt.Sprintf("@%d.%06d", n/1e6, n%1e6)
}

func (s *SSH) logins(ctx context.Context, _ helperapi.Caller, q helperapi.SSHLoginsRequest) (helperapi.SSHLogins, error) {
	out := helperapi.SSHLogins{Entries: []helperapi.SSHLogin{}, Latest: q.After}
	if !hasJournal(s.Root) {
		return out, fileErr(helperapi.CodeLogNoJournal, nil)
	}
	limit := q.Limit
	if limit == 0 {
		limit = 100
	}
	// Each login writes several lines; read more and keep the attempts.
	// Newer ones (After) come oldest first; with more lines than scanned
	// since the cursor, the oldest of them are skipped.
	scan := min(limit*8, 4000)
	// Only sshd's units: lines others write under sshd's name are neither
	// shown nor can they push sshd's own out of the lines read.
	args := []string{"--no-pager", "--output=json", "--lines=" + strconv.Itoa(scan)}
	for _, u := range sshUnits {
		args = append(args, "--unit="+u)
	}
	args = append(args, "SYSLOG_IDENTIFIER=sshd", "SYSLOG_IDENTIFIER=sshd-session", "SYSLOG_IDENTIFIER=sshd-auth")
	if q.After != "" {
		args = append(args, "--since="+usecArg(q.After, 1))
	} else {
		args = append(args, "--reverse")
		if q.Before != "" {
			args = append(args, "--until="+usecArg(q.Before, -1))
		}
	}
	text, err := queryCommand(ctx, "journalctl", args...)
	if err != nil {
		return out, fileErr(helperapi.CodeOpFailed, map[string]any{"detail": errDetail(err)})
	}
	var last int64
	seen, full := 0, false
	uids := s.sshdUIDs()
	for _, line := range strings.Split(text, "\n") {
		var fields map[string]json.RawMessage
		if line == "" || json.Unmarshal([]byte(line), &fields) != nil {
			continue
		}
		if len(out.Entries) >= limit {
			full = true
			break
		}
		seen++
		ts, _ := strconv.ParseInt(journalField(fields["__REALTIME_TIMESTAMP"]), 10, 64)
		last = ts
		if q.After == "" && out.Latest == "" {
			out.Latest = strconv.FormatInt(ts, 10)
		}
		// The query keeps to sshd's units already; each line is checked
		// again, and for its user, which the query cannot ask for (--unit
		// also takes in what root daemons log about a unit).
		if !fromSSHD(fields, uids) {
			continue
		}
		if l, ok := parseLogin(journalField(fields["MESSAGE"])); ok {
			l.Time = ts / 1e6
			out.Entries = append(out.Entries, l)
		}
	}
	switch {
	case q.After != "" && last > 0:
		out.Latest = strconv.FormatInt(last, 10)
	case q.After == "" && last > 0 && (full || seen >= scan):
		out.Next = strconv.FormatInt(last, 10)
	}
	return out, nil
}
