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
	"os"
	"os/signal"
	"os/user"
	"strconv"
	"syscall"

	"github.com/Kobayashi-classmate/Cairn/internal/buildinfo"
	"github.com/Kobayashi-classmate/Cairn/internal/config"
	"github.com/Kobayashi-classmate/Cairn/internal/helper"
)

func main() {
	cfgPath := flag.String("config", config.DefaultPath, "configuration file")
	account := flag.String("user", "cairn", "account of the unprivileged web process")
	flag.Parse()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(*cfgPath, *account, log); err != nil {
		log.Error("cairn-helper stopped", "err", err)
		os.Exit(1)
	}
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
