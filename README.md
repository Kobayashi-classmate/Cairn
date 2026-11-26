# Cairn

English | [简体中文](README.zh-CN.md)

An open-source control panel for Linux servers.

> A *cairn* is a pile of stones raised by passing hikers, one stone at a time. Each one stands on its own, and together they mark the trail.
> Cairn works the same way: every server runs its own complete, independent panel. Linking the panels to manage them together is planned.

**Status: not released yet.** The features of the first two milestones (P0, the platform, and P1, web hosting) are implemented, and the interface has been rebuilt. No version has been released; a full acceptance test on real machines is being prepared. Until then, build the packages yourself (see [Installation](#installation)) and use them on test servers only.

## Available now

- **Distributions**: Debian 12 and 13, Ubuntu 22.04, 24.04 and 26.04, and Rocky Linux, AlmaLinux and RHEL 9 and 10, on amd64 and arm64. These are tested in containers and virtual machines (Rocky Linux and AlmaLinux stand in for RHEL); the arm64 packages are built but have not yet run on a machine. On Enterprise Linux, SELinux stays in enforcing mode.
- **Web sites**: static sites; PHP sites, with several PHP versions side by side and a PHP-FPM pool and two system accounts for each site; reverse proxies; applications on Node.js, Python, Go, Java or .NET, run as systemd services; and TCP or UDP proxies on nginx's stream module.
- **TLS certificates**: from ACME (Let's Encrypt, ZeroSSL, Google Trust Services or your own CA; HTTP-01, or DNS-01 through Cloudflare, DNSPod, Alibaba Cloud, Huawei Cloud, Route 53, GoDaddy, Volcengine, West.cn or RFC 2136) or uploaded, for sites and for the panel itself, and renewed automatically.
- **Software**: nginx, PHP and its extensions, MariaDB, MySQL, PostgreSQL, Redis, Valkey, Node.js, Python, Go, Java (Temurin), .NET, and unar for 7z and RAR archives. Packages come from the distribution's own repositories and from the official upstream repositories (nginx.org, MariaDB, MySQL, PostgreSQL's PGDG, Redis); PHP versions the distribution lacks come from sury, the ondrej PPA or Remi. A version that several of them have uses the distribution's packages unless you choose another source when installing; the recommended version is upstream's newest long-term support one. Versions this server cannot install are listed with the reason. Language runtimes are the official builds, checked against the checksums in each project's official release metadata. Before installing, the panel checks which download sources have the exact version and how fast they are (for runtimes you pick the source), and the exact version is then pinned. Services can be started, stopped and reloaded, and their configuration files edited, in the panel.
- **Databases**: MariaDB or MySQL, PostgreSQL, and Redis or Valkey: databases, users and grants, remote access, dumps and restores (a restore runs as the database's own user), and a key browser for Redis. On Enterprise Linux, only MariaDB and Redis or Valkey for now.
- **Files and terminal**: a file manager with resumable uploads, an editor, archives, a trash and downloads from a URL; a web terminal with saved commands and a choice of colour schemes.
- **Scheduled tasks, logs and backups**: tasks on systemd timers (shell scripts, URLs, backups); the system journal, site logs and SSH sign-ins; backups of sites, databases and folders to local disk or S3-compatible storage, optionally encrypted with age, kept by daily, weekly and monthly rules.
- **Firewall and SSH**: port rules, trusted and blocked addresses, blocking ping, and port forwards on nftables, firewalld or ufw. A change rolls back after 60 seconds unless it is confirmed over a new connection. SSH settings and authorized keys; a change of the SSH port keeps the old port open until the new one has been reached from outside.
- **Host**: host name, time zone, swap, system updates, automatic security updates and the system's package mirrors.
- **Accounts and API**: two-step sign-in (TOTP, with recovery codes), three roles (administrator, operator, auditor), API tokens with an OpenAPI description, an audit log, and notifications by email or webhook.
- **The panel itself**: signed online updates with rollback, and official messages from the project; fetching these can be switched off. English and Simplified Chinese; light, dark and system colour schemes; usable with the keyboard and on phones.

## Planned

Designed, not implemented:

- Docker, an app store and WordPress management; versions whose upstream support has ended, run in containers.
- AI: models connected only through APIs (cloud providers, or inference engines you run on this server or another one), an AI gateway, an ops assistant and an MCP server. Cairn will not ship a model service of its own.
- Monitoring with alerts, more notification channels, a security center, a WAF, log analysis, a mail server, FTP and SFTP accounts, DNS record management and migration tools.
- Node management: one panel managing others, while every server stays manageable at its own address.
- More distributions (the installer already accepts Enterprise Linux 8 and CentOS Stream 9 and 10, but they are not tested; openEuler and others later), loong64, and more languages.
- Signing in with WebAuthn.

## Installation

No version has been released, so there is nothing to download yet. To try Cairn now, build the packages on a machine with Docker (every build step runs in a container):

```sh
make build package                # amd64; add ARCH=arm64 for arm64
```

then copy the package for your system from `dist/` to a test server and install it:

```sh
sudo apt-get install ./cairn_*_amd64.deb      # Debian, Ubuntu
sudo dnf install ./cairn-*.x86_64.rpm         # Rocky Linux, AlmaLinux, RHEL
sudo cairn setup-link                         # prints the one-time link that creates the first administrator
```

The panel listens on HTTPS on a port picked at installation. Installing the package by hand leaves the firewall alone; the panel's Firewall page switches it on.

Releases will come with an installer script, `install.sh`. It checks the release's signed checksum list, installs the package with apt-get or dnf without adding a package repository (`--with-repo` adds Cairn's signed repository instead), asks for the panel's default language (`--lang en|zh-CN`), and switches the firewall on after showing which ports stay open and which listening ones close (`--no-firewall` skips this). At a terminal it asks first and switches the firewall off again unless you confirm from a new SSH connection that you can still log in. Later versions are installed through the panel's online update.

## Security

- The web process, `cairn-web`, runs as the unprivileged user `cairn`. Work that needs root goes through `cairn-helper`, which listens only on a local socket and accepts a fixed set of actions with checked parameters.
- Programs are started with argument lists. The only shell code is a fixed wrapper inside the helper and what you write yourself: an application's start command and a scheduled task's script.
- A browser session needs both its cookie and a key that only the panel's own pages hold. Terminals and downloads use one-time tickets, and websites on the server never receive the panel's cookies.
- Changes to the server and to accounts are recorded in the audit log.
- Firewall changes roll back unless confirmed, and an SSH port change keeps the old port until the new one is reached from outside, so a mistake does not lock you out.
- No telemetry: nothing is reported, updates are never forced, and there is no account to bind. The panel only fetches signed public data (official messages, the release index) from the project, and that can be switched off.

### What `root_access = "never"` does

Root on the server can set this in `/etc/cairn/config.toml`; if the file already has a `[security]` table, change `root_access` there rather than adding a second one. The panel cannot change it. Restart `cairn-helper` afterwards.

```toml
[security]
root_access = "never"
```

It turns off full-disk access in the file manager, root terminals, scheduled tasks that run as root and changes to root's SSH keys, and makes the configuration files of services read-only in the panel.

It does **not** keep root out of reach on every server. An administrator of the panel, or an attacker who controls the web process, can still open a terminal, add an SSH key or create a scheduled task as an ordinary login user, and a user who can use `sudo` can become root. Many cloud images create a default user with passwordless `sudo`. On such a server, take passwordless `sudo` away from login users if you rely on this setting.

## Uninstall

Removing the package never touches the websites: their files, accounts, units (`cairn-app-*`) and server blocks, the databases, the software installed from the panel, the files the sites use (`/etc/nginx/cairn`, which the `nginx.conf` of nginx.org's nginx includes, `/var/lib/cairn-acme`, `/opt/cairn/runtimes`) and the rules in firewalld or ufw all stay, and the sites keep running. Certificates are no longer renewed. The `cairn` and `cairn-unpack` system accounts stay as well, and so do the package repositories the panel added for software: the `cairn-*` files in `/etc/apt/sources.list.d`, `/etc/apt/keyrings` and `/etc/apt/preferences.d`, or `cairn-*.repo` in `/etc/yum.repos.d` with its key `RPM-GPG-KEY-cairn-*` in `/etc/pki/rpm-gpg`.

| What | `apt remove cairn` | `apt purge cairn` | `dnf remove cairn` |
|---|---|---|---|
| Programs and the panel's service units | removed | removed | removed |
| Configuration (`/etc/cairn`) | kept | removed, except app sites' environment files in `/etc/cairn/apps` | kept |
| Panel data (`/var/lib/cairn`: database, keys, uploads) | kept | removed | kept |
| Local backups and database dumps (`/var/lib/cairn/backups`) | kept | **kept** (removed with `CAIRN_PURGE_BACKUPS=1`) | kept |
| Helper state (`/var/lib/cairn-helper`: backup plans with their bucket settings, certificates, update packages) | kept | removed | kept |
| Scheduled tasks (`/etc/cairn-cron`, `cairn-cron-*` units) | kept, stopped | removed | kept, stopped |
| Cairn's firewall (nftables tables `cairn` and `cairn_forward`, `/etc/cairn-firewall`) | tables stay loaded until reboot, files kept | removed | as with `apt remove` |
| Terminal colours (`/etc/profile.d/cairn-term.sh`, which acts only in the panel's terminal) | kept | removed | removed (kept as `.rpmsave` if you changed it) |

- Installed again after `apt remove` or `dnf remove`, Cairn carries on with the kept data and turns the scheduled tasks and the firewall back on.
- After `apt purge`, a new installation starts a new panel; the kept backups are in the Files page's backups root and the database dumps on the Databases page. Backup plans and their bucket settings are gone, but the copies in buckets are not. Encrypted backups need the private key you kept when you made the plan: Cairn never stores it.
- To purge the backups too: `sudo CAIRN_PURGE_BACKUPS=1 apt purge cairn` (after `sudo`, which drops other environment variables). If the package is already purged: `sudo rm -rf /var/lib/cairn`.
- RPM has no purge step. To delete what `dnf remove` keeps, as `apt purge` would:

```sh
sudo rm -rf /etc/cairn-cron /etc/systemd/system/cairn-cron-*.service /etc/systemd/system/cairn-cron-*.timer
sudo systemctl daemon-reload
sudo nft delete table inet cairn; sudo nft delete table inet cairn_forward
sudo rm -rf /var/lib/cairn-helper /var/lib/cairn-unpack /etc/cairn-firewall /etc/sysctl.d/60-cairn-forward.conf
# Keeps app sites' environment files (/etc/cairn/apps) and the backups;
# add "sudo rm -rf /var/lib/cairn" to delete the backups too.
sudo find /etc/cairn -mindepth 1 -maxdepth 1 ! -name apps -exec rm -rf {} +
sudo find /var/lib/cairn -mindepth 1 -maxdepth 1 ! -name backups -exec rm -rf {} +
```

## License

[GNU Affero General Public License v3.0](LICENSE) (`AGPL-3.0-only`).

The project's own source files start with an SPDX line, written in each language's comment syntax:

```
// SPDX-License-Identifier: AGPL-3.0-only
```

Third-party code keeps its own license header. The interface components in `web/src/components/ui` are based on shadcn-vue and carry `SPDX-License-Identifier: MIT`; see [their license](web/src/components/ui/LICENSE).

## Tech stack

Go (three statically linked programs: `cairn-web`, `cairn-helper` and the `cairn` command) · SQLite · Vue 3 + TypeScript + Tailwind CSS + Reka UI (shadcn-vue) · systemd · deb and rpm packages
