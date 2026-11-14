// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"log/slog"
	"os"

	"github.com/Kobayashi-classmate/Cairn/internal/buildinfo"
	"github.com/Kobayashi-classmate/Cairn/internal/config"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// Deps are the resources core actions need.
type Deps struct {
	ConfigPath string
	// ConfigGID is the group that may read the config file (the cairn group).
	ConfigGID int
	Log       *slog.Logger
}

// RegisterCore registers the actions every installation provides.
func RegisterCore(r *Registry, d Deps) {
	Register(r, helperapi.ActionPing, func(context.Context, helperapi.Caller, helperapi.Empty) (helperapi.PingResponse, error) {
		return helperapi.PingResponse{Version: buildinfo.Version, PID: os.Getpid()}, nil
	})

	Register(r, helperapi.ActionSetDefaultLocale, func(_ context.Context, c helperapi.Caller, req helperapi.SetDefaultLocaleRequest) (helperapi.Empty, error) {
		cfg, err := config.Load(d.ConfigPath)
		if err != nil {
			return helperapi.Empty{}, err
		}
		old := cfg.UI.DefaultLocale
		cfg.UI.DefaultLocale = req.Locale
		if err := config.Save(d.ConfigPath, cfg, 0o640, 0, d.ConfigGID); err != nil {
			return helperapi.Empty{}, err
		}
		d.Log.Info("default locale changed", "from", old, "to", req.Locale, "user", c.Username)
		return helperapi.Empty{}, nil
	})

	Register(r, helperapi.ActionSetCloud, func(_ context.Context, c helperapi.Caller, req helperapi.SetCloudRequest) (helperapi.Empty, error) {
		cfg, err := config.Load(d.ConfigPath)
		if err != nil {
			return helperapi.Empty{}, err
		}
		cfg.Cloud.Enabled, cfg.Cloud.BaseURL, cfg.Cloud.Channel = req.Enabled, req.BaseURL, req.Channel
		if err := config.Save(d.ConfigPath, cfg, 0o640, 0, d.ConfigGID); err != nil {
			return helperapi.Empty{}, err
		}
		d.Log.Info("cloud settings changed", "enabled", req.Enabled, "base_url", req.BaseURL, "channel", req.Channel, "user", c.Username)
		return helperapi.Empty{}, nil
	})
}
