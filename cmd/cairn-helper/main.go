// SPDX-License-Identifier: AGPL-3.0-only

// Command cairn-helper is the root helper. It is started by systemd socket
// activation on /run/cairn/helper.sock and executes a fixed table of typed
// actions for cairn-web (and for root). It never listens on the network.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"

	"github.com/Kobayashi-classmate/Cairn/catalog"
	"github.com/Kobayashi-classmate/Cairn/internal/buildinfo"
	"github.com/Kobayashi-classmate/Cairn/internal/config"
	"github.com/Kobayashi-classmate/Cairn/internal/helper"
	"github.com/Kobayashi-classmate/Cairn/internal/platform"
	"github.com/Kobayashi-classmate/Cairn/internal/sources"
)

// stateDir holds the helper's own state (update packages and status).
const stateDir = "/var/lib/cairn-helper"

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if len(os.Args) > 1 && os.Args[1] == "run-update" {
		if err := runUpdate(os.Args[2:], log); err != nil {
			os.Exit(1)
		}
		return
	}
	cfgPath := flag.String("config", config.DefaultPath, "configuration file")
	account := flag.String("user", "cairn", "account of the unprivileged web process")
	flag.Parse()
	if err := run(*cfgPath, *account, log); err != nil {
		log.Error("cairn-helper stopped", "err", err)
		os.Exit(1)
	}
}

// runUpdate is the body of the transient systemd unit started for an update.
// It runs outside cairn-helper.service so restarting the service during the
// package upgrade does not interrupt it.
func runUpdate(args []string, log *slog.Logger) error {
	set := flag.NewFlagSet("run-update", flag.ExitOnError)
	dir := set.String("state-dir", stateDir, "helper state directory")
	file := set.String("file", "", "verified package file to install")
	version := set.String("version", "", "version being installed")
	previous := set.String("previous", "", "package file of the running version, for rollback")
	_ = set.Parse(args)
	if os.Geteuid() != 0 || *file == "" {
		return errors.New("run-update must run as root with --file")
	}
	return helper.RunUpdate(*dir, *file, *version, *previous, log)
}

func run(cfgPath, account string, log *slog.Logger) error {
	if os.Geteuid() != 0 {
		return errors.New("cairn-helper must run as root")
	}
	cfg, err := config.Load(cfgPath)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	u, err := user.Lookup(account)
	if err != nil {
		return fmt.Errorf("look up account %s: %w", account, err)
	}
	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return err
	}
	webUID := uint32(uid)

	reg := helper.NewRegistry()
	helper.RegisterCore(reg, helper.Deps{ConfigPath: cfgPath, ConfigGID: gid, Log: log})

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	ops, err := helper.NewOps(filepath.Join(stateDir, "ops"), log)
	if err != nil {
		return err
	}
	helper.RegisterOpActions(reg, ops)
	format, manager := packageTools(log)
	helper.RegisterPkg(reg, ops, manager)
	software, err := softwareEngine(manager, log)
	if err != nil {
		return err
	}
	helper.RegisterSoftware(reg, ops, software)
	sites := siteEngine(software)
	if _, port, err := net.SplitHostPort(cfg.Server.Listen); err == nil {
		sites.PanelPort, _ = strconv.Atoi(port)
	}
	helper.RegisterSites(reg, ops, sites)
	helper.RegisterCerts(reg, ops, &helper.Certs{Sites: sites, PanelUser: "cairn", Resolvers: cfg.ACME.Resolvers})
	helper.RegisterDBs(reg, ops, dbEngine(software, cfg.Paths.DataDir))
	helper.RegisterFiles(reg, ops, &helper.Files{DataDir: cfg.Paths.DataDir, PanelUser: "cairn", StateDir: stateDir,
		RootAccess: cfg.Security.RootAccess, Log: log})
	updater := &helper.Updater{
		ConfigPath: cfgPath, StateDir: stateDir, Version: buildinfo.Version,
		Format: format, Arch: runtime.GOARCH, Executable: exe, Log: log, Ops: ops,
	}
	ops.Busy = map[string]func() bool{helper.LockPkg: updater.Running}
	helper.RegisterUpdate(reg, updater)

	l, err := helper.Listen(cfg.Paths.HelperSocket, gid)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv := &helper.Server{
		Registry: reg,
		// Only the web process account and root may call actions.
		Allowed: func(uid uint32) bool { return uid == webUID || uid == 0 },
		Log:     log,
	}
	log.Info("cairn-helper ready", "version", buildinfo.Version, "actions", len(reg.Actions()), "web_uid", webUID)
	return srv.Serve(ctx, l)
}

// softwareEngine prepares catalog installs for the running platform; on an
// unsupported platform every request answers pkg.not_available.
func softwareEngine(manager string, log *slog.Logger) (*helper.Software, error) {
	cat, err := catalog.Load()
	if err != nil {
		return nil, err
	}
	prober := &sources.Prober{}
	s := &helper.Software{Catalog: cat, Manager: manager, Arch: runtime.GOARCH, Log: log, Sources: prober,
		Runtimes: &helper.Runtimes{Dir: "/opt/cairn/runtimes", Arch: runtime.GOARCH, Mirrors: &cat.Mirrors, Prober: prober}}
	p, osr, err := platform.Detect()
	if err != nil {
		return s, nil //nolint:nilerr // unsupported platforms install nothing; already logged by packageTools
	}
	s.OS = osr
	if s.Chain, err = platform.Chain(p.Name); err != nil {
		log.Warn("platform profile chain unavailable; software installation disabled", "err", err)
		s.Manager = ""
	}
	return s, nil
}

// dbEngine prepares database management, Debian and Ubuntu only for now
// (P1 batch 6).
func dbEngine(software *helper.Software, dataDir string) *helper.DBs {
	d := &helper.DBs{Software: software, DataDir: dataDir, PanelUser: "cairn", Unsupported: "platform"}
	if p, _, err := platform.Detect(); err == nil && p.Family == "debian" {
		d.Unsupported = ""
	}
	return d
}

// siteEngine prepares site management. Sites need the Debian layout for
// now; on EL they wait for the SELinux verification (P1 batch 3).
func siteEngine(software *helper.Software) *helper.Sites {
	st := &helper.Sites{Software: software, Unsupported: "platform"}
	p, _, err := platform.Detect()
	if err != nil {
		return st
	}
	switch p.Family {
	case "debian":
		st.Unsupported, st.NginxUser = "", p.WebUser
	case "el":
		st.Unsupported = "selinux_pending"
	}
	return st
}

// packageTools returns the package format (for updates) and the package
// manager (for package operations) of the platform; both are empty when the
// platform is not supported, which disables those features.
func packageTools(log *slog.Logger) (format, manager string) {
	p, _, err := platform.Detect()
	if err != nil {
		log.Warn("platform detection failed; online update and package operations disabled", "err", err)
		return "", ""
	}
	switch p.Family {
	case "debian":
		format = "deb"
	case "el":
		format = "rpm"
	}
	return format, p.PackageManager
}
