// SPDX-License-Identifier: AGPL-3.0-only

// Command cairn-web is the panel's unprivileged HTTPS server. It holds no
// root privileges; privileged operations go through cairn-helper.
package main

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"flag"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/Kobayashi-classmate/Cairn/catalog"
	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/auth"
	"github.com/Kobayashi-classmate/Cairn/internal/authz"
	"github.com/Kobayashi-classmate/Cairn/internal/buildinfo"
	"github.com/Kobayashi-classmate/Cairn/internal/config"
	"github.com/Kobayashi-classmate/Cairn/internal/events"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/i18n"
	"github.com/Kobayashi-classmate/Cairn/internal/jobs"
	"github.com/Kobayashi-classmate/Cairn/internal/metrics"
	"github.com/Kobayashi-classmate/Cairn/internal/module"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/auditlog"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/backups"
	certsmod "github.com/Kobayashi-classmate/Cairn/internal/modules/certs"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/cloudsvc"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/core"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/cron"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/databases"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/files"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/firewall"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/host"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/jobcenter"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/logs"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/notifications"
	sitesmod "github.com/Kobayashi-classmate/Cairn/internal/modules/sites"
	softwaremod "github.com/Kobayashi-classmate/Cairn/internal/modules/software"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/sshmod"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/system"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/terminal"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/users"
	"github.com/Kobayashi-classmate/Cairn/internal/notify"
	"github.com/Kobayashi-classmate/Cairn/internal/platform"
	"github.com/Kobayashi-classmate/Cairn/internal/secrets"
	"github.com/Kobayashi-classmate/Cairn/internal/store"
	"github.com/Kobayashi-classmate/Cairn/internal/tlsutil"
	"github.com/Kobayashi-classmate/Cairn/internal/ui"
)

func main() {
	cfgPath := flag.String("config", envOr("CAIRN_CONFIG", config.DefaultPath), "configuration file")
	flag.Parse()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(*cfgPath, log); err != nil {
		log.Error("cairn-web stopped", "err", err)
		os.Exit(1)
	}
}

func run(cfgPath string, log *slog.Logger) error {
	if os.Geteuid() == 0 && os.Getenv("CAIRN_ALLOW_ROOT") != "1" {
		return errors.New("refusing to run as root: cairn-web must run as the unprivileged cairn user")
	}
	cfg, err := config.Load(cfgPath)
	if errors.Is(err, fs.ErrNotExist) {
		log.Warn("configuration file not found, using defaults", "path", cfgPath)
	} else if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := os.MkdirAll(cfg.Paths.DataDir, 0o700); err != nil {
		return err
	}
	db, err := store.Open(filepath.Join(cfg.Paths.DataDir, "cairn.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	if err := store.Migrate(ctx, db); err != nil {
		return err
	}
	tlsHolder, err := tlsutil.NewHolder(filepath.Join(cfg.Paths.DataDir, "tls"))
	if err != nil {
		return err
	}
	box, err := secrets.LoadOrCreate(cfg.Paths.DataDir)
	if err != nil {
		return err
	}

	collector := metrics.NewCollector(5*time.Second, 720, log)
	go collector.Run(ctx)

	helper := helperapi.NewClient(cfg.Paths.HelperSocket)
	pingHelper(ctx, helper, log)

	messages, err := i18n.NewCatalog()
	if err != nil {
		return err
	}
	software, err := catalog.Load()
	if err != nil {
		return err
	}
	bus := &events.Bus{}
	notifyStore := &notify.Store{DB: db, Box: box}
	notifier := notify.New(notifyStore, messages, log)
	bus.Subscribe(notifier.Handle)
	go notifier.Run(ctx)

	jobManager := &jobs.Manager{DB: db, Dir: filepath.Join(cfg.Paths.DataDir, "jobs"), Helper: helper, Events: bus, Log: log}
	if err := jobManager.Init(ctx); err != nil {
		return err
	}
	go jobManager.RunPruner(ctx)

	plat := detectPlatform(log)
	cloudMod := &cloudsvc.Module{
		DB: db, Helper: helper, Events: bus, Settings: cloudsvc.NewSettings(cfg.Cloud), Log: log,
		Version: buildinfo.Version, Family: plat.family, Format: plat.format, Arch: runtime.GOARCH,
	}
	go cloudMod.Run(ctx)

	sitesMod := &sitesmod.Module{Catalog: software, Jobs: jobManager, Helper: helper, DB: db, Events: bus, Log: log, PanelPort: panelPort(cfg.Server.Listen)}
	go sitesMod.Run(ctx)
	certsMod := &certsmod.Module{Jobs: jobManager, Helper: helper, DB: db, Box: box, Events: bus, TLS: tlsHolder, Log: log}
	go certsMod.Run(ctx)
	filesMod := &files.Module{Jobs: jobManager, Helper: helper, DB: db, Log: log, DataDir: cfg.Paths.DataDir}
	go filesMod.Run(ctx)
	termMod := &terminal.Module{Helper: helper, DB: db, Log: log}
	go termMod.Run(ctx)
	cronMod := &cron.Module{Jobs: jobManager, Helper: helper, DB: db, Events: bus, Log: log}
	go cronMod.Run(ctx)
	backupsMod := &backups.Module{Jobs: jobManager, Helper: helper, DB: db, Box: box, Events: bus, Log: log, DataDir: cfg.Paths.DataDir}
	go backupsMod.Run(ctx)
	firewallMod := &firewall.Module{Jobs: jobManager, Helper: helper, DB: db, Events: bus, Log: log}
	go firewallMod.Run(ctx)
	sshMod := &sshmod.Module{Jobs: jobManager, Helper: helper, DB: db, Events: bus, Log: log}
	go sshMod.Run(ctx)

	router := api.NewRouter(core.Authenticator(db), log)
	registry := module.NewRegistry(db)
	registry.Add(
		&core.Module{DB: db, Settings: core.NewSettings(cfg.UI.DefaultLocale), Helper: helper, Box: box, Events: bus, Log: log, Modules: registry},
		&system.Module{Metrics: collector},
		&softwaremod.Module{Catalog: software, Jobs: jobManager, Helper: helper, DB: db, Log: log, Release: plat.release, Chain: plat.chain, Arch: runtime.GOARCH},
		sitesMod,
		certsMod,
		&databases.Module{Jobs: jobManager, Helper: helper, DB: db, Box: box, Log: log, DataDir: cfg.Paths.DataDir},
		filesMod,
		termMod,
		cronMod,
		backupsMod,
		&logs.Module{Helper: helper, Log: log},
		firewallMod,
		sshMod,
		&host.Module{Jobs: jobManager, Helper: helper, DB: db, Log: log},
		&jobcenter.Module{Jobs: jobManager, DB: db, Log: log},
		&users.Module{DB: db, Events: bus, Log: log},
		&auditlog.Module{DB: db},
		&notifications.Module{DB: db, Store: notifyStore, Notifier: notifier, Log: log},
		cloudMod,
	)
	if err := registry.Load(ctx); err != nil {
		return err
	}
	registry.Mount(router)
	router.Handle("GET /api/v1/openapi.json", router.ServeOpenAPI("Cairn API", buildinfo.Version),
		api.Require(authz.Self), api.Doc("meta", "This OpenAPI document"))

	mux := http.NewServeMux()
	mux.Handle("/api/", router)
	mux.Handle("/", ui.Handler())

	srv := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           api.SecurityHeaders(api.Recover(log, mux)),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ConnContext:       api.ConnContext,
		TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: tlsHolder.GetCertificate},
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	go purgeSessions(ctx, db, log)
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	log.Info("cairn-web listening", "addr", cfg.Server.Listen, "version", buildinfo.Version)
	err = srv.ListenAndServeTLS("", "")
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func pingHelper(ctx context.Context, h *helperapi.Client, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var pong helperapi.PingResponse
	if err := h.Call(ctx, helperapi.ActionPing, helperapi.Caller{Source: "web"}, nil, &pong); err != nil {
		log.Warn("root helper is not reachable; privileged features will fail until it is", "err", err)
		return
	}
	log.Info("root helper reachable", "version", pong.Version)
}

type platformInfo struct {
	release string   // profile name, such as "debian-13"
	chain   []string // profile chain for the software catalog
	family  string   // debian | el
	format  string   // package format for updates: deb | rpm
}

// detectPlatform describes the running system; everything is empty on
// unsupported platforms, which disables updates and software installation.
func detectPlatform(log *slog.Logger) platformInfo {
	p, _, err := platform.Detect()
	if err != nil {
		log.Warn("platform detection failed; online update and software installation disabled", "err", err)
		return platformInfo{}
	}
	info := platformInfo{release: p.Name, family: p.Family}
	switch p.Family {
	case "debian":
		info.format = "deb"
	case "el":
		info.format = "rpm"
	}
	if info.chain, err = platform.Chain(p.Name); err != nil {
		log.Warn("platform profile chain unavailable", "err", err)
		info.release = ""
	}
	return info
}

// purgeSessions deletes expired sessions once an hour.
func purgeSessions(ctx context.Context, db *sql.DB, log *slog.Logger) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := auth.PurgeExpired(ctx, db); err != nil {
				log.Warn("purging expired sessions failed", "err", err)
			}
		}
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// panelPort returns the port of the panel's listen address, or 0.
func panelPort(listen string) int {
	_, p, err := net.SplitHostPort(listen)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(p)
	return n
}
