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

const pkgTimeout = 30 * time.Minute

// pkgEnv is what the package manager gets on top of the fixed base
// environment, wherever it runs.
var pkgEnv = []string{"DEBIAN_FRONTEND=noninteractive"}

// pkgManagers are the commands runPackageManager runs.
var pkgManagers = map[string]bool{"apt-get": true, "dnf": true}

// runCommand runs a command for an operation, streaming its output into the
// operation log. Tests replace it; they must never run a package manager.
// The command runs in the helper's own unit and inherits its sandbox: the
// package manager goes through runPackageManager instead.
var runCommand = func(ctx context.Context, log io.Writer, name string, args ...string) error {
	_, _ = fmt.Fprintf(log, "$ %s %s\n", name, strings.Join(args, " "))
	_, err := execx.Run(ctx, execx.Cmd{
		Name: name, Args: args, Timeout: pkgTimeout, Output: log, MaxOutput: 64 << 10,
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
var runPackageManager = func(ctx context.Context, log io.Writer, name string, args ...string) error {
	if !pkgManagers[name] {
		return fmt.Errorf("%s is not a package manager", name)
	}
	if !systemdRunning() {
		return runCommand(ctx, log, name, args...)
	}
	return runInPkgUnit(ctx, log, name, args)
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
