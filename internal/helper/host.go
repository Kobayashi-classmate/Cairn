// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// Host manages the host: its name, time zone and clock synchronization,
// swap, reboots, system updates and the system's package sources.
type Host struct {
	StateDir string
	Software *Software // package manager, sources probing
	Root     string
	Log      *slog.Logger
	now      func() time.Time
	// pkgBusy reports the panel's own package work: an operation holding
	// the package manager, or a transaction still running in its unit.
	pkgBusy func() bool
}

const (
	swapFile        = "/swapfile"
	swappinessFile  = "/etc/sysctl.d/60-cairn-swappiness.conf"
	aptAutoFile     = "/etc/apt/apt.conf.d/52cairn-auto-upgrades"
	dnfAutoConf     = "/etc/dnf/automatic.conf"
	dnfAutoTimer    = "dnf-automatic.timer"
	selfPackage     = "cairn" // updated by the panel's own update, never here
	updatesFile     = "updates.json"
	btrfsSuperMagic = 0x9123683E
	xfsSuperMagic   = 0x58465342
)

// RegisterHost adds the host actions.
func RegisterHost(r *Registry, o *Ops, h *Host) {
	Register(r, helperapi.ActionHostStatus, func(ctx context.Context, _ helperapi.Caller, _ helperapi.Empty) (helperapi.HostStatus, error) {
		return h.Status(ctx), nil
	})
	Register(r, helperapi.ActionHostHostname, h.setHostname)
	Register(r, helperapi.ActionHostTimezones, func(ctx context.Context, _ helperapi.Caller, _ helperapi.Empty) (helperapi.Timezones, error) {
		return helperapi.Timezones{Zones: h.timezones(ctx)}, nil
	})
	Register(r, helperapi.ActionHostTimezone, h.setTimezone)
	Register(r, helperapi.ActionHostNTP, h.setNTP)
	Register(r, helperapi.ActionHostSwappiness, h.setSwappiness)
	h.pkgBusy = func() bool {
		busy := o.Busy[LockPkg] // set once the helper has its updater
		return o.Held(LockPkg) || busy != nil && busy()
	}
	Register(r, helperapi.ActionHostReboot, h.reboot)
	RegisterOp(r, o, helperapi.ActionHostSwap, OpSpec[helperapi.SwapRequest]{
		Locks: func(helperapi.SwapRequest) []string { return []string{"swap"} }, Run: h.setSwap})
	pkg := func(any) []string { return []string{LockPkg} }
	RegisterOp(r, o, helperapi.ActionHostUpdatesCheck, OpSpec[helperapi.Empty]{
		Locks: func(q helperapi.Empty) []string { return pkg(q) }, CancelWhileRunning: true, Run: h.checkUpdates})
	RegisterOp(r, o, helperapi.ActionHostUpdatesInstall, OpSpec[helperapi.UpdatesInstallRequest]{
		Locks: func(q helperapi.UpdatesInstallRequest) []string { return pkg(q) }, Run: h.installUpdates})
	RegisterOp(r, o, helperapi.ActionHostUpdatesAuto, OpSpec[helperapi.AutoUpdatesRequest]{
		Locks: func(q helperapi.AutoUpdatesRequest) []string { return pkg(q) }, Run: h.setAutoUpdates})
	registerSysSources(r, o, h)
}

func hostErr(code string, params map[string]any) error {
	return &helperapi.Error{Code: code, Params: params}
}

func (h *Host) path(p string) string { return filepath.Join(h.Root, p) }

func (h *Host) clock() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

func (h *Host) manager() string {
	if h.Software == nil {
		return ""
	}
	return h.Software.Manager
}

// Status describes the host.
func (h *Host) Status(ctx context.Context) helperapi.HostStatus {
	st := helperapi.HostStatus{Hostname: h.hostname(), Timezone: h.timezone(ctx), NTP: h.ntp(ctx), Swap: h.swap(ctx)}
	st.Kernel = h.kernel()
	st.RebootRequired = st.Kernel.Latest != "" && st.Kernel.Latest != st.Kernel.Running || fileExists(h.path("/run/reboot-required"))
	st.AutoUpdates = h.autoUpdates(ctx)
	st.Updates = h.loadUpdates()
	return st
}

// Host name ---------------------------------------------------------------

func (h *Host) hostname() string {
	if data, err := os.ReadFile(h.path("/etc/hostname")); err == nil {
		if s := strings.TrimSpace(string(data)); s != "" {
			return s
		}
	}
	name, _ := os.Hostname()
	return name
}

func (h *Host) setHostname(ctx context.Context, _ helperapi.Caller, q helperapi.HostnameRequest) (helperapi.Empty, error) {
	old := h.hostname()
	if systemdRunning() {
		if _, err := queryCommand(ctx, "hostnamectl", "set-hostname", q.Hostname); err != nil {
			return helperapi.Empty{}, hostErr(helperapi.CodeHostHostnameInvalid, map[string]any{"detail": errDetail(err)})
		}
	} else if err := writeFileMode(h.path("/etc/hostname"), []byte(q.Hostname+"\n"), 0o644); err != nil { //nolint:gosec // G306: world-readable by design.
		return helperapi.Empty{}, err
	}
	// Debian maps the host name to 127.0.1.1; keep sudo and others able to
	// resolve it. Other lines of /etc/hosts stay untouched.
	if old != "" && old != q.Hostname {
		_ = h.renameInHosts(old, q.Hostname)
	}
	return helperapi.Empty{}, nil
}

func (h *Host) renameInHosts(old, name string) error {
	p := h.path("/etc/hosts")
	data, err := os.ReadFile(p) //nolint:gosec // G304: a fixed path under the test prefix.
	if err != nil {
		return err
	}
	shortOld, _, _ := strings.Cut(old, ".")
	shortNew, _, _ := strings.Cut(name, ".")
	lines := strings.SplitAfter(string(data), "\n")
	changed := false
	for i, l := range lines {
		f := strings.Fields(l)
		if len(f) < 2 || f[0] != "127.0.1.1" {
			continue
		}
		for j := 1; j < len(f); j++ {
			switch f[j] {
			case old:
				f[j], changed = name, true
			case shortOld:
				f[j], changed = shortNew, true
			}
		}
		nl := ""
		if strings.HasSuffix(l, "\n") {
			nl = "\n"
		}
		lines[i] = f[0] + "\t" + strings.Join(f[1:], " ") + nl
	}
	if !changed {
		return nil
	}
	return writeFileMode(p, []byte(strings.Join(lines, "")), 0o644) //nolint:gosec // G306: /etc/hosts is world-readable.
}

// Time --------------------------------------------------------------------

func timedateShow(ctx context.Context, props ...string) map[string]string {
	out := map[string]string{}
	if !systemdRunning() {
		return out
	}
	args := []string{"show"}
	for _, p := range props {
		args = append(args, "--property="+p)
	}
	text, err := queryCommand(ctx, "timedatectl", args...)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(text, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			out[k] = v
		}
	}
	return out
}

func (h *Host) timezone(ctx context.Context) string {
	if tz := timedateShow(ctx, "Timezone")["Timezone"]; tz != "" {
		return tz
	}
	if link, err := os.Readlink(h.path("/etc/localtime")); err == nil {
		if _, tz, ok := strings.Cut(link, "zoneinfo/"); ok {
			return tz
		}
	}
	return "UTC"
}

func (h *Host) timezones(ctx context.Context) []string {
	zones := []string{}
	if !systemdRunning() {
		return zones
	}
	text, err := queryCommand(ctx, "timedatectl", "list-timezones")
	if err != nil {
		return zones
	}
	zones = append(zones, strings.Fields(text)...)
	return zones
}

func (h *Host) setTimezone(ctx context.Context, _ helperapi.Caller, q helperapi.TimezoneRequest) (helperapi.Empty, error) {
	if !slices.Contains(h.timezones(ctx), q.Timezone) {
		return helperapi.Empty{}, hostErr(helperapi.CodeHostTimezoneInvalid, map[string]any{"timezone": q.Timezone})
	}
	if _, err := queryCommand(ctx, "timedatectl", "set-timezone", q.Timezone); err != nil {
		return helperapi.Empty{}, hostErr(helperapi.CodeHostTimezoneInvalid, map[string]any{"detail": errDetail(err)})
	}
	return helperapi.Empty{}, nil
}

func (h *Host) ntp(ctx context.Context) helperapi.HostNTP {
	v := timedateShow(ctx, "NTP", "NTPSynchronized", "CanNTP")
	return helperapi.HostNTP{Available: v["CanNTP"] == "yes", Enabled: v["NTP"] == "yes", Synchronized: v["NTPSynchronized"] == "yes"}
}

func (h *Host) setNTP(ctx context.Context, _ helperapi.Caller, q helperapi.NTPRequest) (helperapi.Empty, error) {
	if !h.ntp(ctx).Available {
		return helperapi.Empty{}, hostErr(helperapi.CodeHostNTPUnavailable, nil)
	}
	if _, err := queryCommand(ctx, "timedatectl", "set-ntp", strconv.FormatBool(q.Enabled)); err != nil {
		return helperapi.Empty{}, hostErr(helperapi.CodeHostNTPUnavailable, map[string]any{"detail": errDetail(err)})
	}
	return helperapi.Empty{}, nil
}

// Swap --------------------------------------------------------------------

func (h *Host) swap(ctx context.Context) helperapi.HostSwap {
	st := helperapi.HostSwap{Devices: []helperapi.SwapDevice{}, Supported: true}
	if data, err := os.ReadFile(h.path("/proc/swaps")); err == nil {
		for _, line := range strings.Split(string(data), "\n")[1:] {
			f := strings.Fields(line)
			if len(f) < 4 {
				continue
			}
			size, _ := strconv.ParseInt(f[2], 10, 64)
			used, _ := strconv.ParseInt(f[3], 10, 64)
			kind := "partition"
			if f[1] == "file" {
				kind = "file"
			}
			st.Devices = append(st.Devices, helperapi.SwapDevice{Path: f[0], Kind: kind, Size: size << 10, Used: used << 10, Managed: f[0] == swapFile})
			st.Total += size << 10
			st.Used += used << 10
		}
	}
	if data, err := os.ReadFile(h.path("/proc/sys/vm/swappiness")); err == nil {
		st.Swappiness, _ = strconv.Atoi(strings.TrimSpace(string(data)))
	}
	switch {
	case !systemdRunning() || h.inContainer(ctx):
		st.Supported, st.Reason = false, "container"
	case h.btrfs() && !fileExists(h.path("/usr/bin/btrfs")) && !fileExists(h.path("/usr/sbin/btrfs")):
		st.Supported, st.Reason = false, "btrfs_old"
	}
	return st
}

func (h *Host) inContainer(ctx context.Context) bool {
	out, err := queryCommand(ctx, "systemd-detect-virt", "--container")
	return err == nil && strings.TrimSpace(out) != "none" && strings.TrimSpace(out) != ""
}

// rootFS is the file system type of / (its statfs magic number).
func (h *Host) rootFS() int64 {
	var fs unix.Statfs_t
	if err := unix.Statfs(h.path("/"), &fs); err != nil {
		return 0
	}
	return int64(fs.Type) //nolint:unconvert // the field's type differs between architectures
}

func (h *Host) btrfs() bool { return h.rootFS() == btrfsSuperMagic }

func (h *Host) swapActive() bool {
	data, err := os.ReadFile(h.path("/proc/swaps"))
	return err == nil && slices.ContainsFunc(strings.Split(string(data), "\n"), func(l string) bool {
		f := strings.Fields(l)
		return len(f) > 0 && f[0] == swapFile
	})
}

// fstabSwap adds (or removes) the managed swap file's line in /etc/fstab.
func (h *Host) fstabSwap(add bool) error {
	p := h.path("/etc/fstab")
	data, err := os.ReadFile(p) //nolint:gosec // G304: a fixed path under the test prefix.
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	var lines []string
	found := false
	for _, l := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		if f := strings.Fields(l); len(f) > 0 && f[0] == swapFile {
			found = true
			if !add {
				continue
			}
		}
		lines = append(lines, l)
	}
	if add == found {
		return nil
	}
	if add {
		lines = append(lines, swapFile+" none swap sw 0 0")
	}
	out := strings.Join(lines, "\n")
	if out != "" {
		out += "\n"
	}
	return writeFileMode(p, []byte(out), 0o644) //nolint:gosec // G306: /etc/fstab is world-readable.
}

func (h *Host) setSwap(ctx context.Context, _ helperapi.Caller, q helperapi.SwapRequest, log io.Writer) (any, error) {
	st := h.swap(ctx)
	if !st.Supported {
		return nil, hostErr(helperapi.CodeHostSwapUnsupported, map[string]any{"reason": st.Reason})
	}
	p := h.path(swapFile)
	oldMB := 0 // the managed swap file's size, made again if the new one fails
	if info, err := os.Lstat(p); err == nil {
		if !info.Mode().IsRegular() {
			return nil, hostErr(helperapi.CodeHostSwapNotManaged, map[string]any{"path": swapFile})
		}
		oldMB = int(info.Size() >> 20)
	}
	// Space first: nothing is turned off for a swap file that cannot fit
	// (the old file's space counts, it goes first).
	if q.SizeMB > 0 {
		var sfs unix.Statfs_t
		if err := unix.Statfs(h.path("/"), &sfs); err == nil {
			free := int64(sfs.Bavail)*int64(sfs.Bsize)>>20 + int64(oldMB) //nolint:gosec // G115: block counts fit.
			if need := int64(q.SizeMB) + 512; free < need {
				return nil, hostErr(helperapi.CodeHostSwapNoSpace, map[string]any{"needMb": need, "freeMb": free})
			}
		}
	}
	wasActive := h.swapActive()
	if wasActive {
		_, _ = fmt.Fprintf(log, "Turning off %s (its pages move back to memory).\n", swapFile)
		if err := runCommand(ctx, log, "swapoff", swapFile); err != nil {
			return nil, err
		}
	}
	if q.SizeMB == 0 {
		if err := h.fstabSwap(false); err != nil {
			return nil, err
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		_, _ = fmt.Fprintf(log, "%s removed.\n", swapFile)
		return nil, nil
	}
	_ = os.Remove(p)
	_, _ = fmt.Fprintf(log, "Creating a %d MiB swap file at %s.\n", q.SizeMB, swapFile)
	if err := h.makeSwap(ctx, log, q.SizeMB); err != nil {
		_ = os.Remove(p)
		// The system had swap before: it gets the previous file back
		// rather than none and a boot-time line for a missing file.
		if wasActive && oldMB > 0 {
			_, _ = fmt.Fprintf(log, "Making the previous %d MiB swap file again.\n", oldMB)
			if rerr := h.makeSwap(ctx, log, oldMB); rerr == nil {
				return nil, err
			}
			_ = os.Remove(p)
		}
		if ferr := h.fstabSwap(false); ferr != nil {
			_, _ = fmt.Fprintf(log, "Warning: %s still names the swap file: %v\n", "/etc/fstab", ferr)
		}
		return nil, err
	}
	return nil, h.fstabSwap(true)
}

// makeSwap creates the managed swap file of sizeMB and turns it on.
func (h *Host) makeSwap(ctx context.Context, log io.Writer, sizeMB int) error {
	p := h.path(swapFile)
	size := strconv.Itoa(sizeMB)
	var err error
	dd := func() error {
		return runCommand(ctx, log, "dd", "if=/dev/zero", "of="+swapFile, "bs=1M", "count="+size, "status=none")
	}
	switch h.rootFS() {
	case btrfsSuperMagic:
		err = runCommand(ctx, log, "btrfs", "filesystem", "mkswapfile", "--size", size+"m", swapFile)
	case xfsSuperMagic:
		// XFS swap files must have no unwritten extents: write them out.
		err = dd()
	default:
		if err = runCommand(ctx, log, "fallocate", "-l", size+"M", swapFile); err != nil {
			err = dd()
		}
	}
	if err == nil {
		// Swapped-out memory must not be readable by other users.
		err = os.Chmod(p, 0o600)
	}
	if err == nil && !h.btrfs() {
		err = runCommand(ctx, log, "mkswap", swapFile)
	}
	if err == nil && fileExists(h.path("/usr/sbin/restorecon")) {
		// SELinux: the boot-time swapon needs the file labeled swapfile_t.
		_ = runCommand(ctx, log, "/usr/sbin/restorecon", swapFile)
	}
	if err == nil {
		err = runCommand(ctx, log, "swapon", swapFile)
	}
	return err
}

func (h *Host) setSwappiness(_ context.Context, _ helperapi.Caller, q helperapi.SwappinessRequest) (helperapi.Empty, error) {
	conf := fmt.Sprintf("# Managed by Cairn. Change it in the panel.\nvm.swappiness = %d\n", q.Value)
	if err := writeFileMode(h.path(swappinessFile), []byte(conf), 0o644); err != nil { //nolint:gosec // G306: sysctl files are world-readable.
		return helperapi.Empty{}, err
	}
	return helperapi.Empty{}, os.WriteFile(h.path("/proc/sys/vm/swappiness"), []byte(strconv.Itoa(q.Value)), 0o644) //nolint:gosec // G306: a sysctl.
}

// Kernel and reboot ---------------------------------------------------------

// versionCompare compares version strings naturally (digit runs as
// numbers): "6.12.9" < "6.12.10".
func versionCompare(a, b string) int {
	for a != "" && b != "" {
		da, db := leadingDigits(a), leadingDigits(b)
		if da != "" && db != "" {
			na, _ := strconv.ParseUint(da, 10, 64)
			nb, _ := strconv.ParseUint(db, 10, 64)
			if na != nb {
				if na < nb {
					return -1
				}
				return 1
			}
			a, b = a[len(da):], b[len(db):]
			continue
		}
		if a[0] != b[0] {
			if a[0] < b[0] {
				return -1
			}
			return 1
		}
		a, b = a[1:], b[1:]
	}
	return len(a) - len(b)
}

func leadingDigits(s string) string {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return s[:i]
}

func (h *Host) kernel() helperapi.HostKernel {
	var k helperapi.HostKernel
	var u unix.Utsname
	if err := unix.Uname(&u); err == nil {
		k.Running = unix.ByteSliceToString(u.Release[:])
	}
	matches, _ := filepath.Glob(h.path("/boot/vmlinuz-*"))
	for _, m := range matches {
		v := strings.TrimPrefix(filepath.Base(m), "vmlinuz-")
		if strings.Contains(v, "rescue") {
			continue
		}
		if k.Latest == "" || versionCompare(v, k.Latest) > 0 {
			k.Latest = v
		}
	}
	return k
}

func (h *Host) reboot(ctx context.Context, _ helperapi.Caller, _ helperapi.Empty) (helperapi.RebootResult, error) {
	if !systemdRunning() {
		return helperapi.RebootResult{}, hostErr(helperapi.CodeHostRebootFailed, map[string]any{"detail": "systemd"})
	}
	// A reboot in the middle of a transaction leaves packages half
	// configured, and the next boot may need them (a kernel, an initramfs).
	if h.pkgBusy != nil && h.pkgBusy() || pkgLocked(h.Root, h.manager()) {
		return helperapi.RebootResult{}, hostErr(helperapi.CodeHostRebootBusy, nil)
	}
	at := h.clock().Add(5 * time.Second)
	// A timer of its own, so the answer reaches the panel first.
	if _, err := queryCommand(ctx, "systemd-run", "--unit=cairn-reboot", "--description=Cairn: reboot", "--on-active=5",
		"/usr/bin/systemctl", "reboot"); err != nil {
		return helperapi.RebootResult{}, hostErr(helperapi.CodeHostRebootFailed, map[string]any{"detail": errDetail(err)})
	}
	if h.Log != nil {
		h.Log.Warn("reboot requested from the panel")
	}
	return helperapi.RebootResult{At: at.Unix()}, nil
}

// pkgLockFiles are the locks the package manager holds while it changes
// packages, whoever runs it: apt's and dpkg's, rpm's (EL 10 keeps its
// database in /usr/lib/sysimage).
var pkgLockFiles = map[string][]string{
	"apt": {"/var/lib/dpkg/lock-frontend", "/var/lib/dpkg/lock"},
	"dnf": {"/var/lib/rpm/.rpm.lock", "/usr/lib/sysimage/rpm/.rpm.lock"},
}

// pkgLocked reports whether a process holds a lock of the package manager:
// apt, dpkg or rpm at work, started by the panel or not (unattended
// upgrades, an administrator). An open file description lock conflicts
// with the record locks they take, this process's own included.
func pkgLocked(root, manager string) bool {
	for _, p := range pkgLockFiles[manager] {
		f, err := os.OpenFile(filepath.Join(root, p), os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0) //nolint:gosec // G304: fixed system paths.
		if err != nil {
			continue
		}
		lk := unix.Flock_t{Type: unix.F_WRLCK, Whence: io.SeekStart}
		err = unix.FcntlFlock(f.Fd(), unix.F_OFD_GETLK, &lk)
		_ = f.Close()
		if err == nil && lk.Type != unix.F_UNLCK {
			return true
		}
	}
	return false
}

// Updates -----------------------------------------------------------------

func (h *Host) loadUpdates() helperapi.UpdateList {
	l := helperapi.UpdateList{Packages: []helperapi.PackageUpdate{}, Sources: []helperapi.SourceWarning{}}
	if data, err := os.ReadFile(filepath.Join(h.StateDir, updatesFile)); err == nil {
		_ = json.Unmarshal(data, &l)
	}
	if l.Packages == nil {
		l.Packages = []helperapi.PackageUpdate{}
	}
	if l.Sources == nil {
		l.Sources = []helperapi.SourceWarning{}
	}
	return l
}

func (h *Host) saveUpdates(l helperapi.UpdateList) error {
	data, _ := json.MarshalIndent(l, "", "  ")
	return writeFileMode(filepath.Join(h.StateDir, updatesFile), data, 0o600)
}

// aptSimulation is an apt-get -s line: "Inst openssl [3.5.1-1] (3.5.4-1
// Debian-Security:13/stable-security [amd64])".
var aptSimulation = regexp.MustCompile(`^Inst (\S+) (?:\[([^\]]*)\] )?\((\S+) ([^\[)]*)`)

func parseAptSimulation(out string) []helperapi.PackageUpdate {
	list := []helperapi.PackageUpdate{}
	for _, line := range strings.Split(out, "\n") {
		m := aptSimulation.FindStringSubmatch(line)
		if m == nil || m[1] == selfPackage || m[2] == "" {
			continue
		}
		origin := strings.ToLower(m[4])
		list = append(list, helperapi.PackageUpdate{Name: m[1], From: m[2], To: m[3], Security: strings.Contains(origin, "security")})
	}
	return list
}

// parseDNFUpgrades reads "dnf list --upgrades": "name.arch  version  repo".
func parseDNFUpgrades(out string) []helperapi.PackageUpdate {
	list := []helperapi.PackageUpdate{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) != 3 || !strings.Contains(f[0], ".") || strings.HasSuffix(f[0], ":") {
			continue
		}
		name := f[0][:strings.LastIndex(f[0], ".")]
		if name == selfPackage {
			continue
		}
		list = append(list, helperapi.PackageUpdate{Name: name, To: f[1]})
	}
	return list
}

// nevraName takes the name out of "openssl-1:3.0.7-28.el9.x86_64".
func nevraName(s string) string {
	if i := strings.LastIndex(s, "."); i > 0 {
		s = s[:i]
	}
	for range 2 {
		if i := strings.LastIndex(s, "-"); i > 0 {
			s = s[:i]
		}
	}
	return s
}

// pendingUpdates lists the updates from the current package indexes.
func (h *Host) pendingUpdates(ctx context.Context, cached bool) ([]helperapi.PackageUpdate, error) {
	switch h.manager() {
	case "apt":
		out, err := queryCommand(ctx, "apt-get", "-s", "--with-new-pkgs", "upgrade")
		if err != nil {
			return nil, err
		}
		return parseAptSimulation(out), nil
	case "dnf":
		args := []string{"-q", "list", "--upgrades"}
		if cached {
			args = append([]string{"-C"}, args...)
		}
		out, err := queryCommand(ctx, "dnf", args...)
		if err != nil {
			return nil, err
		}
		list := parseDNFUpgrades(out)
		if len(list) == 0 {
			return list, nil
		}
		secArgs := []string{"-q", "updateinfo", "list", "--security"}
		if cached {
			secArgs = append([]string{"-C"}, secArgs...)
		}
		security := map[string]bool{}
		if sec, err := queryCommand(ctx, "dnf", secArgs...); err == nil {
			for _, line := range strings.Split(sec, "\n") {
				if f := strings.Fields(line); len(f) >= 3 {
					security[nevraName(f[len(f)-1])] = true
				}
			}
		}
		names := make([]string, len(list))
		for i, p := range list {
			names[i] = p.Name
		}
		from := map[string]string{}
		if out, err := queryCommand(ctx, "rpm", append([]string{"-q", "--qf", "%{NAME} %{VERSION}-%{RELEASE}\n"}, names...)...); err == nil || out != "" {
			for _, line := range strings.Split(out, "\n") {
				if f := strings.Fields(line); len(f) == 2 {
					from[f[0]] = f[1]
				}
			}
		}
		for i := range list {
			list[i].From, list[i].Security = from[list[i].Name], security[list[i].Name]
		}
		return list, nil
	}
	return nil, hostErr(helperapi.CodeHostUpdatesUnavailable, nil)
}

func (h *Host) checkUpdates(ctx context.Context, _ helperapi.Caller, _ helperapi.Empty, log io.Writer) (any, error) {
	switch h.manager() {
	case "apt":
		if err := runPackageManager(ctx, log, "apt-get", "update"); err != nil {
			return nil, err
		}
	case "dnf":
		if err := runPackageManager(ctx, log, "dnf", "makecache", "--assumeyes"); err != nil {
			return nil, err
		}
	default:
		return nil, hostErr(helperapi.CodeHostUpdatesUnavailable, nil)
	}
	list, err := h.pendingUpdates(ctx, true)
	if err != nil {
		return nil, err
	}
	l := helperapi.UpdateList{CheckedAt: h.clock().Unix(), Packages: list, Sources: []helperapi.SourceWarning{}}
	security := 0
	for _, p := range list {
		if p.Security {
			security++
		}
	}
	_, _ = fmt.Fprintf(log, "%d updates, %d of them security updates.\n", len(list), security)
	// The same check as at install time: is the source in use behind?
	_, _ = fmt.Fprintf(log, "Comparing the system sources in use with the official ones.\n")
	for _, sl := range h.probeSystemSources(ctx) {
		if sl.Warning == helperapi.WarnInUseBehind || sl.Warning == helperapi.WarnInUseUnusable {
			l.Sources = append(l.Sources, helperapi.SourceWarning{Role: sl.Role, Warning: sl.Warning})
			_, _ = fmt.Fprintf(log, "Warning: the %s source in use: %s.\n", sl.Role, sl.Warning)
		}
	}
	return l, h.saveUpdates(l)
}

func (h *Host) installUpdates(ctx context.Context, _ helperapi.Caller, q helperapi.UpdatesInstallRequest, log io.Writer) (any, error) {
	switch h.manager() {
	case "apt":
		list, err := h.pendingUpdates(ctx, true)
		if err != nil {
			return nil, err
		}
		var names []string
		for _, p := range list {
			if !q.SecurityOnly || p.Security {
				names = append(names, p.Name)
			}
		}
		if len(names) == 0 {
			_, _ = fmt.Fprintf(log, "Nothing to install.\n")
			break
		}
		args := append([]string{"install", "-y", "--only-upgrade", "-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold"}, names...)
		if err := runPackageManager(ctx, log, "apt-get", args...); err != nil {
			return nil, err
		}
	case "dnf":
		args := []string{"upgrade", "-y", "--exclude=" + selfPackage}
		if q.SecurityOnly {
			args = append(args, "--security")
		}
		if err := runPackageManager(ctx, log, "dnf", args...); err != nil {
			return nil, err
		}
	default:
		return nil, hostErr(helperapi.CodeHostUpdatesUnavailable, nil)
	}
	l := h.loadUpdates()
	if list, err := h.pendingUpdates(ctx, true); err == nil {
		l.Packages = list
	}
	l.CheckedAt = h.clock().Unix()
	return l, h.saveUpdates(l)
}

// Automatic updates ---------------------------------------------------------

var aptConfigValue = regexp.MustCompile(`'([^']*)'`)

func (h *Host) autoUpdates(ctx context.Context) helperapi.AutoUpdates {
	switch h.manager() {
	case "apt":
		a := helperapi.AutoUpdates{Available: true, Tool: "unattended-upgrades"}
		if len(h.Software.missing(ctx, []string{"unattended-upgrades"})) > 0 {
			return a
		}
		out, err := queryCommand(ctx, "apt-config", "shell", "V", "APT::Periodic::Unattended-Upgrade")
		if m := aptConfigValue.FindStringSubmatch(out); err == nil && m != nil {
			a.Enabled = m[1] != "" && m[1] != "0"
		}
		return a
	case "dnf":
		a := helperapi.AutoUpdates{Available: systemdRunning(), Tool: "dnf-automatic"}
		if a.Available {
			out, _ := queryCommand(ctx, "systemctl", "is-enabled", dnfAutoTimer)
			a.Enabled = strings.TrimSpace(out) == "enabled"
		}
		return a
	}
	return helperapi.AutoUpdates{}
}

// setINI sets key in [section] of an INI file, adding it when missing.
func setINI(data, section, key, value string) string {
	lines := strings.Split(data, "\n")
	in, insertAt := false, -1
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			if in {
				break
			}
			in = t == "["+section+"]"
			if in {
				insertAt = i + 1
			}
			continue
		}
		if !in {
			continue
		}
		if k, _, ok := strings.Cut(t, "="); ok && strings.TrimSpace(k) == key && !strings.HasPrefix(t, "#") {
			lines[i] = key + " = " + value
			return strings.Join(lines, "\n")
		}
	}
	if insertAt < 0 {
		return strings.TrimRight(data, "\n") + "\n\n[" + section + "]\n" + key + " = " + value + "\n"
	}
	lines = slices.Insert(lines, insertAt, key+" = "+value)
	return strings.Join(lines, "\n")
}

func (h *Host) setAutoUpdates(ctx context.Context, _ helperapi.Caller, q helperapi.AutoUpdatesRequest, log io.Writer) (any, error) {
	switch h.manager() {
	case "apt":
		if q.Enabled {
			if missing := h.Software.missing(ctx, []string{"unattended-upgrades"}); len(missing) > 0 {
				if err := h.Software.ensureLibraries(ctx, log, missing); err != nil {
					return nil, err
				}
			}
		}
		v := "0"
		if q.Enabled {
			v = "1"
		}
		conf := fmt.Sprintf("// Managed by Cairn. Switch automatic updates in the panel.\n"+
			"// unattended-upgrades installs what its own configuration allows (the security updates by default).\n"+
			"APT::Periodic::Update-Package-Lists \"%s\";\nAPT::Periodic::Unattended-Upgrade \"%s\";\n", v, v)
		_, _ = fmt.Fprintf(log, "Writing %s\n", aptAutoFile)
		return nil, writeFileMode(h.path(aptAutoFile), []byte(conf), 0o644) //nolint:gosec // G306: apt configuration is world-readable.
	case "dnf":
		if !systemdRunning() {
			return nil, hostErr(helperapi.CodeHostUpdatesUnavailable, nil)
		}
		if !q.Enabled {
			return nil, runCommand(ctx, log, "systemctl", "disable", "--now", dnfAutoTimer)
		}
		if missing := h.Software.missing(ctx, []string{"dnf-automatic"}); len(missing) > 0 {
			if err := h.Software.ensureLibraries(ctx, log, missing); err != nil {
				return nil, err
			}
		}
		p := h.path(dnfAutoConf)
		data, err := os.ReadFile(p) //nolint:gosec // G304: a fixed path under the test prefix.
		if err != nil {
			return nil, err
		}
		out := setINI(setINI(string(data), "commands", "upgrade_type", "security"), "commands", "apply_updates", "yes")
		if out != string(data) {
			_, _ = fmt.Fprintf(log, "Setting %s: security updates, applied automatically.\n", dnfAutoConf)
			if err := writeFileMode(p, []byte(out), 0o644); err != nil { //nolint:gosec // G306: dnf configuration is world-readable.
				return nil, err
			}
		}
		return nil, runCommand(ctx, log, "systemctl", "enable", "--now", dnfAutoTimer)
	}
	return nil, hostErr(helperapi.CodeHostUpdatesUnavailable, nil)
}

// sortByTime sorts backups newest first.
func sortByTime(b []helperapi.SysSourceBackup) {
	sort.Slice(b, func(i, j int) bool { return b[i].At > b[j].At })
}
