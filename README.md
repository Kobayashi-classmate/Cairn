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

## License

[GNU Affero General Public License v3.0](LICENSE) (`AGPL-3.0-only`).

Every source file starts with:

```
// SPDX-License-Identifier: AGPL-3.0-only
```
