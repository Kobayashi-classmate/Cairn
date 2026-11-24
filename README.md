# Cairn

English | [简体中文](README.zh-CN.md)

An open-source control panel for Linux servers.

> A *cairn* is a pile of stones raised by passing hikers, one stone at a time. Each one stands on its own, and together they mark the trail.
> Cairn works the same way: every server runs its own complete, independent panel, and the panels can be linked to manage them together.

**Status: design phase** (draft v0.2). There is no runnable code yet.

## Highlights (design goals)

- **Multi-distro**: Tier 1 support for Debian 12/13, Ubuntu 22.04/24.04/26.04 and Rocky/Alma/RHEL 9/10; Tier 2 support for EL 8, openEuler and several Chinese distributions. Runs on amd64, arm64 and loong64.
- **Full-featured**: 30 modules that can be enabled as needed, including websites (PHP/Java/Node/Go/Python/.NET, reverse proxy, layer-4 proxy), SSL, DNS, databases, files, terminal, scheduled tasks, Docker, app store, backups, monitoring and alerts, security center, WAF, mail server, FTP, multi-user access and node management.
- **Secure by default**: the web process runs as an unprivileged user. Root operations go only through a local helper process that accepts a fixed set of actions. No shell command strings are ever assembled. Every operation is audited.
- **Ready out of the box**: nothing is compiled from source, so a common web stack installs in about a minute. Download sources can be picked automatically by speed test or chosen by hand; either way, exactly the version you selected is installed.
- **AI infrastructure**: every model is connected through an API, whether it is a cloud provider or an inference engine you deploy yourself on this server or another machine. Cairn provides an AI gateway, an ops assistant and an MCP server. The panel does not ship a model service of its own.
- **Multiple panels working together**: each server's panel is fully usable on its own. Node management only adds one more place to manage servers from; if the primary panel goes down, every node can still be managed at its own address.
- **Multilingual**: English and Simplified Chinese at first, with more languages to follow. You choose the default language during installation and can switch at any time in the panel.
- **No telemetry**: no data reporting, no forced updates, no account binding.

## Tech stack

Go (statically linked, single binary) · SQLite · Vue 3 + TypeScript + Tailwind CSS + Reka UI (shadcn-vue) · systemd · ships as deb and rpm packages

## Uninstall

Removing the package never touches the websites: their files, accounts, units (`cairn-app-*`) and server blocks, the databases, the software installed from the panel, the files the sites use (`/etc/nginx/cairn`, `/var/lib/cairn-acme`, `/opt/cairn/runtimes`) and the rules in firewalld or ufw all stay, and the sites keep running. Certificates are no longer renewed. The `cairn` and `cairn-unpack` system accounts stay as well.

| What | `apt remove cairn` | `apt purge cairn` | `dnf remove cairn` |
|---|---|---|---|
| Programs and the panel's service units | removed | removed | removed |
| Configuration (`/etc/cairn`) | kept | removed, except app sites' environment files in `/etc/cairn/apps` | kept |
| Panel data (`/var/lib/cairn`: database, keys, uploads) | kept | removed | kept |
| Local backups and database dumps (`/var/lib/cairn/backups`) | kept | **kept** (removed with `CAIRN_PURGE_BACKUPS=1`) | kept |
| Helper state (`/var/lib/cairn-helper`: backup plans with their bucket settings, certificates, update packages) | kept | removed | kept |
| Scheduled tasks (`/etc/cairn-cron`, `cairn-cron-*` units) | kept, stopped | removed | kept, stopped |
| Cairn's firewall (nftables tables `cairn` and `cairn_forward`, `/etc/cairn-firewall`) | tables stay loaded until reboot, files kept | removed | as with `apt remove` |

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

Every source file starts with:

```
// SPDX-License-Identifier: AGPL-3.0-only
```
