// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/execx"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// CodePkgUnsupported means the system has no supported package manager.
const CodePkgUnsupported = "pkg.unsupported"

// pkgTimeout is the time limit of a command of an operation, and of a
// package manager command that only refreshes the indexes: either can stop
// at any point without harm.
const pkgTimeout = 30 * time.Minute

// pkgTransactionTimeout is the time limit of a package manager transaction
// (installing, upgrading or removing packages), long enough that only one
// that hangs reaches it: dpkg or rpm killed half-way leaves packages
// unpacked but not configured, and every later package operation fails
// until that is repaired (repairInterrupted). A slow download only takes
// long; a stalled one ends on apt's and dnf's own time limits.
const pkgTransactionTimeout = 24 * time.Hour

// pkgEnv is what the package manager gets on top of the fixed base
// environment, wherever it runs.
var pkgEnv = []string{"DEBIAN_FRONTEND=noninteractive"}

// pkgManagers are the commands runPackageManager runs: dpkg only to finish
// a transaction that was interrupted (repairInterrupted).
var pkgManagers = map[string]bool{"apt-get": true, "dnf": true, "dpkg": true}

// pkgSubcommand is the command a package manager command line runs
// ("install"), past the options before it ("-o Dir::Etc::SourceList=...").
func pkgSubcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-o" || a == "-c": // apt's option and its value
			i++
		case strings.HasPrefix(a, "-"):
		default:
			return a
		}
	}
	return ""
}

// pkgRefresh reports a package manager command that only refreshes the
// indexes.
func pkgRefresh(name string, args []string) bool {
	sub := pkgSubcommand(args)
	return name == "apt-get" && sub == "update" || name == "dnf" && sub == "makecache"
}

// commandTimeout is the time limit of a command of an operation.
func commandTimeout(name string, args []string) time.Duration {
	if pkgManagers[name] && !pkgRefresh(name, args) {
		return pkgTransactionTimeout
	}
	return pkgTimeout
}

// runCommand runs a command for an operation, streaming its output into the
// operation log. Tests replace it; they must never run a package manager.
// The command runs in the helper's own unit and inherits its sandbox: the
// package manager goes through runPackageManager instead.
var runCommand = func(ctx context.Context, log io.Writer, name string, args ...string) error {
	_, _ = fmt.Fprintf(log, "$ %s %s\n", name, strings.Join(args, " "))
	_, err := execx.Run(ctx, execx.Cmd{
		Name: name, Args: args, Timeout: commandTimeout(name, args), Output: log, MaxOutput: 64 << 10,
		Env: pkgEnv,
	})
	return err
}

// runPackageManager runs apt-get or dnf for an operation, whenever it changes
// something: a transaction (install, upgrade, remove) or a refresh of the
// indexes. Under systemd that happens in a unit of its own (pkgunit.go) and
// never in the helper's, whose sandbox the package manager and every
// maintainer script would inherit: with /usr/lib/modules hidden, a kernel
// package cannot be unpacked and a rebuilt initramfs has no modules in it.
// Without systemd (containers) the helper has no sandbox and runs the
// command itself. Tests replace it; they must never run a package manager.
//
// A transaction first finishes one an earlier run left interrupted.
var runPackageManager = func(ctx context.Context, log io.Writer, name string, args ...string) error {
	if !pkgManagers[name] {
		return fmt.Errorf("%s is not a package manager", name)
	}
	if !pkgRefresh(name, args) {
		if err := repairInterrupted(ctx, log, name); err != nil {
			return err
		}
	}
	return runPkgCommand(ctx, log, name, args)
}

// runPkgCommand runs a package manager command where runPackageManager
// says, as it is.
func runPkgCommand(ctx context.Context, log io.Writer, name string, args []string) error {
	if !systemdRunning() {
		return runCommand(ctx, log, name, args...)
	}
	return runInPkgUnit(ctx, log, name, args)
}

// pkgLockRoot prefixes the paths of the package manager's lock files
// ("" means /; tests).
var pkgLockRoot = ""

// interruptedPkg looks for what an interrupted transaction left: what the
// package manager says about it ("" when nothing) and the command that
// finishes it. A transaction another process runs right now is not
// interrupted: its packages look the same until it ends, and it holds the
// locks. Tests replace it.
var interruptedPkg = func(ctx context.Context, manager string) (string, []string) {
	switch manager {
	case "apt-get":
		// The packages left unpacked, half-installed or half-configured
		// (dpkg exits 0 either way).
		if out, _ := queryCommand(ctx, "dpkg", "--audit"); strings.TrimSpace(out) != "" && !pkgLocked(pkgLockRoot, "apt") {
			return strings.TrimSpace(out), []string{"dpkg", "--configure", "-a"}
		}
	case "dnf":
		// "foo-1.2-1.el9.x86_64 is a duplicate with foo-1.1-3.el9.x86_64";
		// packages installed more than once on purpose (kernels) are not.
		if out, _ := queryCommand(ctx, "dnf", "-q", "check", "--duplicates"); strings.Contains(out, "is a duplicate with") && !pkgLocked(pkgLockRoot, "dnf") {
			return strings.TrimSpace(out), []string{"dnf", "remove", "--duplicates", "-y"}
		}
	}
	return "", nil
}

// repairInterrupted finishes what a package manager transaction left
// undone when it was killed or the machine went down half-way: otherwise
// apt refuses every transaction ("dpkg was interrupted, you must manually
// run 'dpkg --configure -a'"), and on dnf the packages of an unfinished
// upgrade stay installed twice. What it finds and does goes to the log.
func repairInterrupted(ctx context.Context, log io.Writer, manager string) error {
	found, repair := interruptedPkg(ctx, manager)
	if found == "" {
		return nil
	}
	_, _ = fmt.Fprintf(log, "An earlier package manager transaction did not finish:\n%s\nFinishing it first.\n", tailString(found, 2000))
	if err := runPkgCommand(ctx, log, repair[0], repair[1:]); err != nil {
		return &helperapi.Error{Code: helperapi.CodePkgRepairFailed, Params: map[string]any{"command": strings.Join(repair, " "), "detail": tailString(err.Error(), 300)}}
	}
	return nil
}

// RegisterPkg adds the package manager operations. manager is the platform
// profile's package manager (apt or dnf).
func RegisterPkg(r *Registry, o *Ops, manager string) {
	RegisterOp(r, o, helperapi.ActionPkgRefresh, OpSpec[helperapi.Empty]{
		Locks: func(helperapi.Empty) []string { return []string{LockPkg} },
		// Refreshing indexes can stop at any point without harm.
		CancelWhileRunning: true,
		Run: func(ctx context.Context, _ helperapi.Caller, _ helperapi.Empty, log io.Writer) (any, error) {
			switch manager {
			case "apt":
				return nil, runPackageManager(ctx, log, "apt-get", "update")
			case "dnf":
				return nil, runPackageManager(ctx, log, "dnf", "makecache", "--assumeyes")
			}
			return nil, &helperapi.Error{Code: CodePkgUnsupported}
		},
	})
}
