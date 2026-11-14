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
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/auth"
	"github.com/Kobayashi-classmate/Cairn/internal/authz"
	"github.com/Kobayashi-classmate/Cairn/internal/buildinfo"
	"github.com/Kobayashi-classmate/Cairn/internal/config"
	"github.com/Kobayashi-classmate/Cairn/internal/events"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/i18n"
	"github.com/Kobayashi-classmate/Cairn/internal/metrics"
	"github.com/Kobayashi-classmate/Cairn/internal/module"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/auditlog"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/cloudsvc"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/core"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/notifications"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/system"
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
	certFile, keyFile, err := tlsutil.EnsureSelfSigned(filepath.Join(cfg.Paths.DataDir, "tls"))
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

	catalog, err := i18n.NewCatalog()
	if err != nil {
		return err
	}
	bus := &events.Bus{}
	notifyStore := &notify.Store{DB: db, Box: box}
	notifier := notify.New(notifyStore, catalog, log)
	bus.Subscribe(notifier.Handle)
	go notifier.Run(ctx)

	family, format := platformFormat(log)
	cloudMod := &cloudsvc.Module{
		DB: db, Helper: helper, Events: bus, Settings: cloudsvc.NewSettings(cfg.Cloud), Log: log,
		Version: buildinfo.Version, Family: family, Format: format, Arch: runtime.GOARCH,
	}
	go cloudMod.Run(ctx)

	router := api.NewRouter(core.Authenticator(db), log)
	modules := []module.Module{
		&core.Module{DB: db, Settings: core.NewSettings(cfg.UI.DefaultLocale), Helper: helper, Box: box, Events: bus, Log: log},
		&system.Module{Metrics: collector},
		&users.Module{DB: db, Events: bus, Log: log},
		&auditlog.Module{DB: db},
		&notifications.Module{DB: db, Store: notifyStore, Notifier: notifier, Log: log},
		cloudMod,
	}
	for _, m := range modules {
		m.Routes(router)
	}
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
		TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS12},
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
	err = srv.ListenAndServeTLS(certFile, keyFile)
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

// platformFormat returns the distribution family and package format used to
// pick update packages; both are empty on unsupported platforms.
func platformFormat(log *slog.Logger) (family, format string) {
	p, _, err := platform.Detect()
	if err != nil {
		log.Warn("platform detection failed; online update disabled", "err", err)
		return "", ""
	}
	switch p.Family {
	case "debian":
		return p.Family, "deb"
	case "el":
		return p.Family, "rpm"
	}
	return p.Family, ""
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
