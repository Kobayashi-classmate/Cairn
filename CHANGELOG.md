# Changelog

Everything a Cairn user or administrator can notice is recorded here, newest
first. The release notes on GitHub and in every panel are taken from this
file. Each version lists its changes under the sections Added, Changed,
Removed, Fixed, Security and Upgrade notes, in that order. Versions follow
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- Installation with one script on Debian 12/13, Ubuntu 22.04/24.04/26.04 and
  Rocky Linux, AlmaLinux and RHEL 9/10, from signed deb and rpm packages.
- Accounts with two-factor authentication, roles, API tokens and an audit
  log of every operation.
- Notifications, official messages and signed online updates with rollback.
- A software catalog that installs from the system's repositories first and
  from official upstream repositories otherwise, with download sources
  probed at install time.
- Web sites: static, PHP, reverse proxy, application sites and layer-4
  proxies.
- TLS certificates from ACME (HTTP-01 and DNS-01) or uploaded, for sites and
  for the panel itself.
- Database management for MariaDB, PostgreSQL and Redis or Valkey.
- A file manager and a web terminal.
- Scheduled tasks, a log browser and backups to local or S3 storage.
- Firewall management on nftables, firewalld or ufw, with automatic rollback
  of a change that locks the administrator out.
- SSH settings and authorized keys, and host settings: host name, time,
  swap, system updates and package sources.
- Support for SELinux in enforcing mode on Enterprise Linux.
- English and Simplified Chinese interfaces.
- Light, dark and system colour schemes, saved with each account; a menu
  grouped into apps, operations, security and system, with "Search or jump
  to" (Ctrl K / ⌘K).
- An overview of the server: resources, sites, services and whether they
  run, recent tasks and security checks, with notices for system updates,
  expiring certificates and failed backups.
- Sites and certificates in tables with tabs by type; each site shows the
  days left on its certificate and its last backup, and opens its settings
  in a side panel. Long tasks are followed in a corner of the screen.
