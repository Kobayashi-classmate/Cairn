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
  of a change that locks the administrator out. The rollback holds when
  the browser goes away and when the server restarts before the change is
  confirmed; one that fails is tried again and reported. The installer
  keeps the firewall on only after you confirm from a new SSH connection
  that you can still log in and, with nobody to ask, switches it on only
  when the SSH ports are known for certain.
- SSH settings and authorized keys, and host settings: host name, time,
  swap, system updates and package sources. A change of the SSH port keeps
  the old port open until the new one has been reached from outside.
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
- Port forwards in the firewall: connections addressed to the server on
  one of its ports go on to another of its ports or to a port of another
  IPv4 host, on nftables, ufw and firewalld, with the same 60-second
  rollback as the other firewall changes. Blocked addresses and deny rules
  hold for forwards too, and traffic the server routes for others or its
  containers' own connections are never redirected. Forwarding to another
  host switches IPv4 forwarding on while it is needed, and the server then
  routes nothing else for its neighbors, while container and VM networks
  keep working; forwarding that was already on is left to you.
- The firewall, SSH, host, jobs, users, audit, messages and settings pages
  in the new layout. Firewall and SSH port changes show a banner with
  their steps and the time left, and a firewall change can be rolled back
  at once. Settings are grouped in a side menu with a new Appearance
  page for the colour scheme and language. The sidebar shows the server's
  address.
- The audit log filters by result and time range, and the jobs list by
  status and kind, over the whole history rather than the loaded page.
- The interface works with the keyboard and with screen readers on every
  page, and on tablets and phones; the old component library is gone.
- Roles keep root-level work for administrators: editing the configuration
  files of services, the backup directory and the whole disk in the file
  manager (where reading, downloading and every change need a recent
  re-authentication, as changing backup plans does, so API tokens only
  list names there), terminals and shell tasks as users other than
  site owners and the logs of such tasks, and layer-4 proxies to the
  server itself or to host names. Read-only accounts do not see app
  environment values, task scripts or Redis values.
- Signing in from a browser that signed in before keeps working while
  someone guesses the account's password elsewhere; repeated failures for
  an account otherwise lock it for up to 15 minutes. API tokens cannot
  change the password, sessions, tokens or two-step sign-in, and terminals
  end with the session that opened them or when the account changes.
- Fetching a file from a URL refuses addresses on the server, the local
  network and cloud metadata services; backup storages, URL tasks and ACME
  servers refuse cloud metadata addresses.
- Sites are kept apart: web servers do not follow a site's links to files
  of other owners, so one site cannot serve another's files, and sites on
  the same PHP version cannot read each other's scripts through the shared
  OPcache. The web server cannot read the files of reverse proxy and app
  sites at all. A switch serves the links between a site's own owner and
  runtime user (Laravel's public/storage, for example): the panel hands
  each such link to the right one whenever the site is saved.
- Installing the package by hand prints how to get the one-time setup link
  (`sudo cairn setup-link`) rather than the link itself, which would stay
  in the package manager's logs. The online installer prints the link.
- Database users the panel did not create keep their privileges: the
  panel only sets their password.
- A site, layer-4 proxy or scheduled task whose change was cut off by a
  restart of the panel shows as failed and can be saved again.
- Removing the package keeps the configuration and the data and says where
  they are. `apt purge cairn` deletes them but keeps the local backups and
  database dumps in `/var/lib/cairn/backups`, which may be the only copy;
  `CAIRN_PURGE_BACKUPS=1` deletes those too. The README lists what each
  way of uninstalling leaves behind.
- A version whose upstream support has ended cannot be installed, unless
  the distribution itself maintains it. One that is installed already
  stays on the Software page, marked "End of life", and can still be
  started, configured, used by sites and uninstalled.
- Re-authentication before sensitive operations can be turned off in the
  panel settings; it is on by default. Turning it off asks for the
  password, every change is recorded in the audit log, and API tokens stay
  refused on those operations either way. Completing the first-run setup
  counts as a fresh re-authentication.
- The file manager and directory backups never follow a site's links into
  another site or into the system, also when a link appears while a task
  runs. A folder replaced by a copy, a move or a restore from the trash
  goes to the trash rather than being deleted.
- Restoring a backup checks the whole backup before it writes anything,
  and replaces each file only once its new content is complete. A restore
  into a site's folder gives the files to that site's owner and runtime
  user. Folders with spaces, dots, dashes or non-ASCII characters in their
  path can be backed up like any other, and retention never deletes the
  backup a run has just made.
- A session is more than its cookie. Browsers send a host's cookies to
  every port of it, so the page also holds a key that only the panel's own
  address can read, and nothing accepts the cookie without it: a website
  on the panel's host name cannot use an administrator's session.
  Terminals and downloads use one-time tickets, websites never receive
  the panel's cookies, and the panel warns when a site shares its host
  name. A new tab signs in through a tab that is already open.
