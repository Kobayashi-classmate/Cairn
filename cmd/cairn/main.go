// SPDX-License-Identifier: AGPL-3.0-only

// Command cairn is the local administration tool. It must run on the server
// itself; when started as root it drops to the cairn account before touching
// the panel database, so files keep the right owner.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/audit"
	"github.com/Kobayashi-classmate/Cairn/internal/auth"
	"github.com/Kobayashi-classmate/Cairn/internal/buildinfo"
	"github.com/Kobayashi-classmate/Cairn/internal/config"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/i18n"
	"github.com/Kobayashi-classmate/Cairn/internal/store"
	"github.com/Kobayashi-classmate/Cairn/internal/tlsutil"
)

const usage = `Usage: cairn <command> [flags]

Commands:
  version           print the version
  setup-link        print a one-time link for creating the administrator
  reset-password    reset a user's password and end their sessions (API tokens stay valid)
  reset-2fa         turn off a user's two-step sign-in (lost phone and codes)
  reset-panel-cert  serve the panel's self-signed certificate again (browsers refuse the one set)

Run "cairn <command> -h" for the flags of a command.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	var app *cli
	switch cmd {
	case "version", "--version", "-v":
		fmt.Println(buildinfo.Version)
		return
	case "setup-link":
		var hint bool
		app, err = newCLI("setup-link", args, func(f *flag.FlagSet) {
			f.BoolVar(&hint, "hint", false, "only say how to get the link (no link is made); prints nothing once setup is done")
		})
		if err == nil {
			if hint {
				err = app.setupHint()
			} else {
				err = app.setupLink()
			}
		}
	case "reset-password":
		var username string
		app, err = newCLI("reset-password", args, func(f *flag.FlagSet) {
			f.StringVar(&username, "username", "", "user whose password is reset (optional if only one user exists)")
		})
		if err == nil {
			err = app.resetPassword(username)
		}
	case "reset-2fa":
		var username string
		app, err = newCLI("reset-2fa", args, func(f *flag.FlagSet) {
			f.StringVar(&username, "username", "", "user whose two-step sign-in is turned off (optional if only one user exists)")
		})
		if err == nil {
			err = app.reset2FA(username)
		}
	case "reset-panel-cert":
		app, err = newCLI("reset-panel-cert", args, nil)
		if err == nil {
			err = app.resetPanelCert()
		}
	case "help", "-h", "--help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		msg := err.Error()
		if app != nil {
			msg = app.t("cli.error", map[string]any{"Message": msg})
		}
		fmt.Fprintln(os.Stderr, msg)
		os.Exit(1)
	}
}

type cli struct {
	cfg     config.Config
	locale  string
	catalog *i18n.Catalog
	db      *sql.DB
}

func newCLI(name string, args []string, extra func(*flag.FlagSet)) (*cli, error) {
	fset := flag.NewFlagSet("cairn "+name, flag.ExitOnError)
	cfgPath := fset.String("config", config.DefaultPath, "configuration file")
	lang := fset.String("lang", "", "output language (en, zh-CN); default: $LANG, then the panel default")
	if extra != nil {
		extra(fset)
	}
	_ = fset.Parse(args)

	cfg, err := config.Load(*cfgPath)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	catalog, err := i18n.NewCatalog()
	if err != nil {
		return nil, err
	}
	c := &cli{cfg: cfg, catalog: catalog, locale: pickLocale(*lang, cfg.UI.DefaultLocale)}

	if err := dropPrivileges(); err != nil {
		return nil, err
	}
	c.db, err = store.Open(filepath.Join(cfg.Paths.DataDir, "cairn.db"))
	if err != nil {
		return nil, err
	}
	if err := store.Migrate(context.Background(), c.db); err != nil {
		return nil, err
	}
	return c, nil
}

// pickLocale applies the CLI precedence: --lang, then LC_ALL/LC_MESSAGES/LANG,
// then the panel default.
func pickLocale(flagLang, panelDefault string) string {
	for _, v := range []string{flagLang, os.Getenv("LC_ALL"), os.Getenv("LC_MESSAGES"), os.Getenv("LANG")} {
		if l, ok := i18n.Normalize(v); ok {
			return l
		}
	}
	if i18n.IsSupported(panelDefault) {
		return panelDefault
	}
	return i18n.Default
}

// dropPrivileges switches from root to the cairn account. Without root the
// command runs as the invoking user, who then needs access to the data dir.
func dropPrivileges() error {
	if os.Geteuid() != 0 {
		return nil
	}
	u, err := user.Lookup("cairn")
	if err != nil {
		return fmt.Errorf("the cairn system account does not exist: %w", err)
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return err
	}
	if err := syscall.Setgroups([]int{gid}); err != nil {
		return err
	}
	if err := syscall.Setgid(gid); err != nil {
		return err
	}
	return syscall.Setuid(uid)
}

func (c *cli) t(id string, data map[string]any) string { return c.catalog.T(c.locale, id, data) }

// setupHint tells how to get the setup link, for the package's post-install
// script: the link itself would end up in the package manager's logs
// (/var/log/apt/term.log is readable by the adm group) while it is valid.
func (c *cli) setupHint() error {
	n, err := auth.CountUsers(context.Background(), c.db)
	if err != nil || n > 0 {
		return err
	}
	fmt.Println(c.t("cli.setup_link.hint", nil))
	return nil
}

func (c *cli) setupLink() error {
	ctx := context.Background()
	token, err := auth.CreateSetupToken(ctx, c.db, auth.SetupTokenTTL)
	if errors.Is(err, auth.ErrSetupDone) {
		fmt.Println(c.t("cli.setup_link.already_done", nil))
		return nil
	}
	if err != nil {
		return err
	}
	host, port, err := net.SplitHostPort(c.cfg.Server.Listen)
	if err != nil {
		return err
	}
	var hosts []string
	if ip := net.ParseIP(host); ip == nil || ip.IsUnspecified() {
		for _, ip := range tlsutil.LocalIPs(false) {
			hosts = append(hosts, ip.String())
		}
	} else {
		hosts = append(hosts, host)
	}
	if len(hosts) == 0 {
		hosts = []string{"127.0.0.1"}
	}
	fmt.Println(c.t("cli.setup_link.intro", map[string]any{"Minutes": int(auth.SetupTokenTTL.Minutes())}))
	fmt.Println()
	for _, h := range hosts {
		fmt.Printf("  https://%s/setup?token=%s\n", net.JoinHostPort(h, port), token)
	}
	fmt.Println()
	fmt.Println(c.t("cli.setup_link.cert_note", nil))
	return nil
}

// pickUser resolves --username, defaulting to the only user if there is one.
func (c *cli) pickUser(ctx context.Context, username string) (*auth.User, error) {
	if username == "" {
		n, err := auth.CountUsers(ctx, c.db)
		if err != nil {
			return nil, err
		}
		if n != 1 {
			return nil, errors.New(c.t("cli.reset_password.need_username", nil))
		}
		if err := c.db.QueryRowContext(ctx, `SELECT username FROM users LIMIT 1`).Scan(&username); err != nil {
			return nil, err
		}
	}
	u, err := auth.UserByName(ctx, c.db, username)
	if errors.Is(err, auth.ErrNotFound) {
		return nil, errors.New(c.t("cli.reset_password.no_user", map[string]any{"Username": username}))
	}
	return u, err
}

func (c *cli) reset2FA(username string) error {
	ctx := context.Background()
	u, err := c.pickUser(ctx, username)
	if err != nil {
		return err
	}
	if err := auth.DisableTOTP(ctx, c.db, u.ID); err != nil {
		return err
	}
	if err := auth.DeleteUserSessions(ctx, c.db, u.ID); err != nil {
		return err
	}
	if err := audit.Record(ctx, c.db, audit.Entry{UserID: u.ID, Username: u.Username, Action: "auth.reset_2fa", Result: "ok", Source: "cli"}); err != nil {
		return err
	}
	fmt.Println(c.t("cli.reset_2fa.done", map[string]any{"Username": u.Username}))
	fmt.Println(c.t("cli.reset_password.sessions_revoked", nil))
	return nil
}

// resetPanelCert has the helper serve the panel's self-signed certificate
// again, as choosing it on the certificates page does: the way back for a
// panel whose certificate no browser takes. The panel picks it up within
// seconds, without a restart.
func (c *cli) resetPanelCert() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	h := helperapi.NewClient(c.cfg.Paths.HelperSocket)
	caller := helperapi.Caller{Source: "cli"}
	var ref helperapi.OpRef
	if err := h.Call(ctx, helperapi.ActionPanelCert, caller, helperapi.PanelCertRequest{}, &ref); err != nil {
		return err
	}
	for {
		var res helperapi.OpGetResponse
		if err := h.Call(ctx, helperapi.ActionOpGet, caller, helperapi.OpGetRequest{ID: ref.ID}, &res); err != nil {
			return err
		}
		if helperapi.OpDone(res.Op.State) {
			if res.Op.State == helperapi.OpSucceeded {
				break
			}
			if res.Op.Error != nil {
				return res.Op.Error
			}
			return errors.New(string(res.Op.State))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	if err := audit.Record(ctx, c.db, audit.Entry{Action: "cert.panel", Params: map[string]any{"cert": ""}, Result: "ok", Source: "cli"}); err != nil {
		return err
	}
	fmt.Println(c.t("cli.reset_panel_cert.done", nil))
	return nil
}

func (c *cli) resetPassword(username string) error {
	ctx := context.Background()
	u, err := c.pickUser(ctx, username)
	if err != nil {
		return err
	}
	password, err := auth.RandomPassword(20)
	if err != nil {
		return err
	}
	if err := auth.SetPassword(ctx, c.db, u.ID, password); err != nil {
		return err
	}
	if err := auth.DeleteUserSessions(ctx, c.db, u.ID); err != nil {
		return err
	}
	if err := auth.ResetFailures(ctx, c.db, auth.UserKey(u.Username)); err != nil {
		return err
	}
	if err := audit.Record(ctx, c.db, audit.Entry{UserID: u.ID, Username: u.Username, Action: "auth.reset_password", Result: "ok", Source: "cli"}); err != nil {
		return err
	}
	fmt.Println(c.t("cli.reset_password.done", map[string]any{"Username": u.Username, "Password": password}))
	fmt.Println(c.t("cli.reset_password.sessions_revoked", nil))
	fmt.Println(c.t("cli.reset_password.tokens_kept", nil))
	return nil
}
