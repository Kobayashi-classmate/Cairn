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
- PHP extensions installed and removed from the panel for each PHP version,
  showing which are loaded and which are built in. Removing one never
  removes other packages with it.
- Configuration files of nginx, PHP, MariaDB, MySQL, PostgreSQL, Redis and
  Valkey edited in the panel. Each change is checked before it applies, and
  the previous file comes back if the check fails or the service does not
  start with the new one. The last ten versions are kept. Servers with
  `root_access = "never"` show the files read-only.
- Starting, stopping, restarting and reloading the services of installed
  software.
- Software, databases, backups and scheduled tasks in tables with side
  panels. Installing shows the exact versions and every step before it
  starts. Each installed version keeps its services, versions, extensions,
  configuration files and logs in one place. Databases show their size and
  last backup.
- Saved commands on the Terminal page, kept with each account, to paste
  into the open terminal or run in it.
- The Logs page shows the system journal, the logs of each site and SSH
  sign-ins in tabs. Journal filters stay in the address, and the software
  and site panels link to them.
- Files, the terminal and logs in the new layout: files in a table with
  tabs for site directories, the backup directory and the trash, actions
  for the selected entries, and an editor that asks before unsaved changes
  are lost. The file editor and the terminal follow the light or dark
  scheme.
- Port forwards in the firewall: connections to a port of the server go
  on to another of its ports or to a port of another IPv4 host, on
  nftables, ufw and firewalld, with the same 60-second rollback as the
  other firewall changes. Forwarding to another host switches IPv4
  forwarding on while it is needed.
