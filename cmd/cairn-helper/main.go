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
	"strings"
	"syscall"

	"github.com/Kobayashi-classmate/Cairn/catalog"
	"github.com/Kobayashi-classmate/Cairn/internal/buildinfo"
	"github.com/Kobayashi-classmate/Cairn/internal/config"
	"github.com/Kobayashi-classmate/Cairn/internal/helper"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
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
	if len(os.Args) > 1 && os.Args[1] == "firewall" {
		if err := runFirewall(os.Args[2:], log); err != nil {
			fmt.Fprintln(os.Stderr, "cairn firewall:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "ssh" {
		if err := runSSH(os.Args[2:], log); err != nil {
			fmt.Fprintln(os.Stderr, "cairn ssh:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "task" {
		if err := runTask(os.Args[2:], log); err != nil {
			fmt.Fprintln(os.Stderr, "cairn task:", err)
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
	e, err := newEngines(cfg, manager, log)
	if err != nil {
		return err
	}
	helper.RegisterSoftware(reg, ops, e.software)
	helper.RegisterSites(reg, ops, e.sites)
	helper.RegisterCerts(reg, ops, &helper.Certs{Sites: e.sites, PanelUser: "cairn", DataDir: cfg.Paths.DataDir, Resolvers: cfg.ACME.Resolvers})
	helper.RegisterDBs(reg, ops, e.dbs)
	helper.RegisterFiles(reg, ops, e.files)
	helper.RegisterBackups(reg, ops, e.backups)
	helper.RegisterCron(reg, ops, e.cron)
	helper.RegisterLogs(reg, "")
	helper.RegisterFirewall(reg, ops, e.firewall)
	helper.RegisterSSH(reg, ops, e.ssh)
	helper.RegisterHost(reg, ops, e.host)
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
	// Existing sites get what this version writes into their configuration
	// (after an upgrade), before any request can change them.
	e.sites.StartRefresh(ctx, ops, log)

	srv := &helper.Server{
		Registry: reg,
		// Only the web process account and root may call actions.
		Allowed: func(uid uint32) bool { return uid == webUID || uid == 0 },
		Log:     log,
	}
	log.Info("cairn-helper ready", "version", buildinfo.Version, "actions", len(reg.Actions()), "web_uid", webUID)
	return srv.Serve(ctx, l)
}

// engines are the helper's subsystems, shared by the daemon and by the
// task runner.
type engines struct {
	software *helper.Software
	sites    *helper.Sites
	dbs      *helper.DBs
	files    *helper.Files
	backups  *helper.Backups
	cron     *helper.Cron
	firewall *helper.Firewall
	ssh      *helper.SSH
	host     *helper.Host
}

func newEngines(cfg config.Config, manager string, log *slog.Logger) (*engines, error) {
	software, err := softwareEngine(manager, log)
	if err != nil {
		return nil, err
	}
	software.RootAccess = cfg.Security.RootAccess
	e := &engines{software: software, sites: siteEngine(software), dbs: dbEngine(software, cfg.Paths.DataDir)}
	if _, port, err := net.SplitHostPort(cfg.Server.Listen); err == nil {
		e.sites.PanelPort, _ = strconv.Atoi(port)
	}
	e.files = &helper.Files{DataDir: cfg.Paths.DataDir, PanelUser: "cairn", StateDir: stateDir, RootAccess: cfg.Security.RootAccess, Log: log}
	e.backups = &helper.Backups{Files: e.files, DBs: e.dbs, Log: log}
	e.cron = &helper.Cron{Sites: e.sites, Files: e.files, Backups: e.backups}
	e.firewall = &helper.Firewall{StateDir: stateDir, Software: software, Default: "none", PanelPort: e.sites.PanelPort,
		Executable: helperExecutable(), Log: log}
	e.host = &helper.Host{StateDir: stateDir, Software: software, Log: log}
	e.ssh = &helper.SSH{StateDir: stateDir, Files: e.files, Firewall: e.firewall, Software: software, Service: "ssh",
		Executable: e.firewall.Executable, Log: log}
	if p, _, err := platform.Detect(); err == nil {
		e.firewall.Default = p.Firewall
		if unit := p.Services["ssh"]; unit != "" {
			e.ssh.Service = unit
		}
	}
	e.sites.Firewall, e.dbs.Firewall = e.firewall, e.firewall
	return e, nil
}

// helperExecutable is the path timers run the helper from.
func helperExecutable() string {
	if exe, err := os.Executable(); err == nil {
		return exe
	}
	return "/usr/lib/cairn/cairn-helper"
}

// runSSH is what the SSH port timer runs:
//
//	cairn-helper ssh finish-port   (close the old ports of a port change)
func runSSH(args []string, log *slog.Logger) error {
	if os.Geteuid() != 0 || len(args) != 1 || args[0] != "finish-port" {
		return errors.New("usage (as root): cairn-helper ssh finish-port")
	}
	cfg, err := config.Load(config.DefaultPath)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	_, manager := packageTools(slog.New(slog.DiscardHandler))
	e, err := newEngines(cfg, manager, log)
	if err != nil {
		return err
	}
	return e.ssh.FinishPort(context.Background(), os.Stdout)
}

// runFirewall is the firewall's command line, for the installer and the
// rollback timer:
//
//	cairn-helper firewall plan            (what setup would do, key=value lines)
//	cairn-helper firewall setup           (enable it at install time)
//	cairn-helper firewall rollback <tx>   (undo a change nobody confirmed)
func runFirewall(args []string, log *slog.Logger) error {
	if os.Geteuid() != 0 {
		return errors.New("the firewall commands run as root")
	}
	if len(args) == 0 {
		return errors.New("usage: cairn-helper firewall plan|setup|rollback <tx>")
	}
	cfg, err := config.Load(config.DefaultPath)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	_, manager := packageTools(slog.New(slog.DiscardHandler))
	e, err := newEngines(cfg, manager, log)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	switch {
	case args[0] == "plan" && len(args) == 1:
		plan, err := e.firewall.Plan(ctx)
		if err != nil {
			return err
		}
		active := "no"
		if plan.Active {
			active = "yes"
		}
		fmt.Printf("backend=%s\nactive=%s\ninstall=%s\nports=%s\n", plan.Backend, active,
			strings.Join(plan.Install, " "), strings.Join(plan.Ports, " "))
		return nil
	case args[0] == "setup" && len(args) == 1:
		return e.firewall.Setup(ctx, os.Stdout)
	case args[0] == "rollback" && len(args) == 2 && helperapi.ValidFirewallTxID(args[1]):
		return e.firewall.Rollback(ctx, args[1])
	}
	return errors.New("usage: cairn-helper firewall plan|setup|rollback <tx>")
}

// runTask is what a scheduled task's service runs:
//
//	cairn-helper task start|done <id>   (records the run, as root)
//	cairn-helper task url               (fetches $CAIRN_URL)
//	cairn-helper task backup <id>       (runs the task's backup plan)
func runTask(args []string, log *slog.Logger) error {
	if len(args) == 1 && args[0] == "url" {
		return helper.TaskURL(context.Background(), os.Stdout)
	}
	if len(args) != 2 {
		return errors.New("usage: cairn-helper task start|done|backup <id>")
	}
	if os.Geteuid() != 0 {
		return errors.New("tasks run as root")
	}
	cfg, err := config.Load(config.DefaultPath)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	_, manager := packageTools(slog.New(slog.DiscardHandler))
	e, err := newEngines(cfg, manager, log)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	switch args[0] {
	case "start":
		return e.cron.TaskStart(args[1])
	case "done":
		return e.cron.TaskDone(args[1])
	case "backup":
		return e.cron.TaskBackup(ctx, args[1], os.Stdout)
	}
	return fmt.Errorf("unknown task step %q", args[0])
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

// dbEngine prepares database management: every engine on Debian and
// Ubuntu; on EL MariaDB and Redis/Valkey for now, PostgreSQL and MySQL
// after the P1 acceptance (P1 batch 10).
func dbEngine(software *helper.Software, dataDir string) *helper.DBs {
	d := &helper.DBs{Software: software, DataDir: dataDir, PanelUser: "cairn", Unsupported: "platform"}
	if p, _, err := platform.Detect(); err == nil {
		switch p.Family {
		case "debian":
			d.Unsupported = ""
		case "el":
			d.Unsupported, d.OnlySoftware = "", []string{"mariadb", "redis", "valkey"}
		}
	}
	return d
}

// siteEngine prepares site management for the platform's layout (on EL
// with SELinux labels, P1 batch 10).
func siteEngine(software *helper.Software) *helper.Sites {
	st := &helper.Sites{Software: software, Unsupported: "platform"}
	p, _, err := platform.Detect()
	if err != nil {
		return st
	}
	switch p.Family {
	case "debian", "el":
		st.Unsupported, st.NginxUser, st.Family = "", p.WebUser, p.Family
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
