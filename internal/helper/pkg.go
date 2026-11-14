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

// runCommand runs a command for an operation, streaming its output into the
// operation log. Tests replace it; they must never run a package manager.
var runCommand = func(ctx context.Context, log io.Writer, name string, args ...string) error {
	_, _ = fmt.Fprintf(log, "$ %s %s\n", name, strings.Join(args, " "))
	_, err := execx.Run(ctx, execx.Cmd{
		Name: name, Args: args, Timeout: pkgTimeout, Output: log, MaxOutput: 64 << 10,
		Env: []string{"DEBIAN_FRONTEND=noninteractive"},
	})
	return err
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
				return nil, runCommand(ctx, log, "apt-get", "update")
			case "dnf":
				return nil, runCommand(ctx, log, "dnf", "makecache", "--assumeyes")
			}
			return nil, &helperapi.Error{Code: CodePkgUnsupported}
		},
	})
}
