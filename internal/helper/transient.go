// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/execx"
)

// unitRun is a command run in a transient service of its own
// (systemd-run --wait --pipe --collect): standard input and output are
// the helper's pipes, and nothing the command starts outlives it, since
// systemd stops the unit's whole control group when the command ends.
type unitRun struct {
	// Cmd is the command: Name, Args, Dir, Stdin, Stdout, Output, Timeout
	// and MaxOutput apply. Env goes into the unit's environment, which
	// every local user can read (systemctl show): never secrets. RunAs is
	// ignored (see DynamicUser and User).
	Cmd execx.Cmd
	// DynamicUser runs the command as a user systemd allocates for this
	// run only; otherwise as User (a system account), or root when empty.
	DynamicUser bool
	User        string
	// Credentials are files the command reads (secrets), by name: they
	// reach only the unit (LoadCredential=, a directory only its user can
	// read), never its properties or environment. CredentialEnv sets
	// environment variables to their paths (variable -> credential name).
	Credentials   map[string][]byte
	CredentialEnv map[string]string
	// Properties are more unit properties ("ReadWritePaths=/srv/x").
	Properties []string
}

// unitCredDir keeps the credential files while their unit starts
// (root-only; tests replace it).
var unitCredDir = "/run/cairn-helper/credentials" //nolint:gosec // G101: a directory, not a credential.

var (
	unitCredName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	unitEnvName  = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,63}$`)
)

// unitBase confines every transient run: no new privileges, nothing of
// the system writable unless a property says so, private /tmp and /dev.
var unitBase = []string{"NoNewPrivileges=yes", "PrivateTmp=yes", "PrivateDevices=yes", "ProtectSystem=strict",
	"ProtectKernelTunables=yes", "ProtectKernelModules=yes", "ProtectControlGroups=yes", "RestrictSUIDSGID=yes"}

// runInUnit runs u and returns the command's result; the exit status is
// the command's (systemd-run --wait passes it on).
func runInUnit(ctx context.Context, u unitRun) (execx.Result, error) {
	unit := "cairn-run-" + newHexID("")
	timeout := u.Cmd.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	args := []string{"--quiet", "--wait", "--pipe", "--collect", "--unit=" + unit,
		// A backstop: the unit stops even if systemd-run is killed.
		fmt.Sprintf("--property=RuntimeMaxSec=%d", int((timeout+time.Minute)/time.Second))}
	props := append([]string{}, unitBase...)
	switch {
	case u.DynamicUser:
		props = append(props, "DynamicUser=yes")
	case u.User != "":
		props = append(props, "User="+u.User)
	}
	props = append(props, u.Properties...)
	if len(u.Credentials) > 0 {
		if err := os.MkdirAll(unitCredDir, 0o700); err != nil {
			return execx.Result{}, err
		}
		names := make([]string, 0, len(u.Credentials))
		for n := range u.Credentials {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			if !unitCredName.MatchString(n) {
				return execx.Result{}, fmt.Errorf("credential name %q", n)
			}
			p := filepath.Join(unitCredDir, unit+"."+n)
			if err := os.WriteFile(p, u.Credentials[n], 0o600); err != nil {
				return execx.Result{}, err
			}
			defer func() { _ = os.Remove(p) }()
			props = append(props, "LoadCredential="+n+":"+p)
		}
	}
	for _, p := range props {
		args = append(args, "--property="+p)
	}
	for _, e := range u.Cmd.Env {
		args = append(args, "--setenv="+e)
	}
	if u.Cmd.Dir != "" {
		args = append(args, "--working-directory="+u.Cmd.Dir)
	}
	args = append(args, "--")
	if len(u.CredentialEnv) > 0 {
		// The credentials' directory is known only inside the unit.
		vars := make([]string, 0, len(u.CredentialEnv))
		for v := range u.CredentialEnv {
			vars = append(vars, v)
		}
		sort.Strings(vars)
		var script strings.Builder
		for _, v := range vars {
			n := u.CredentialEnv[v]
			if !unitEnvName.MatchString(v) || !unitCredName.MatchString(n) || u.Credentials[n] == nil {
				return execx.Result{}, fmt.Errorf("credential variable %s=%s", v, n)
			}
			fmt.Fprintf(&script, "export %s=\"$CREDENTIALS_DIRECTORY/%s\"; ", v, n)
		}
		script.WriteString(`exec "$@"`)
		args = append(args, "/bin/sh", "-c", script.String(), "sh")
	}
	args = append(args, u.Cmd.Name)
	args = append(args, u.Cmd.Args...)
	res, err := dbRun(ctx, execx.Cmd{Name: "systemd-run", Args: args, Stdin: u.Cmd.Stdin, Stdout: u.Cmd.Stdout, Output: u.Cmd.Output,
		Timeout: timeout, MaxOutput: u.Cmd.MaxOutput})
	if err != nil {
		// Killing systemd-run (timeout, cancel) does not stop the unit.
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		_, _ = dbRun(sctx, execx.Cmd{Name: "systemctl", Args: []string{"stop", unit + ".service"}, Timeout: 30 * time.Second})
		cancel()
	}
	return res, err
}
