#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Checks, against the distribution's own PostgreSQL, that the statements the
# helper runs as the postgres role resolve to pg_catalog even when a database
# user has planted look-alike objects in a public schema it can write to
# (every database of a cluster created by 14 or earlier, and any database the
# user owns). The planted objects only record that they were called; nothing
# else happens. Run in a throwaway container, for instance:
#
#   docker run --rm -v "$PWD/test/databases:/t:ro" ubuntu:22.04 bash /t/pg-search-path.sh
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq >/dev/null
apt-get install -y -qq --no-install-recommends postgresql >/dev/null
read -r line name port _ < <(pg_lsclusters -h | head -n1)
pg_ctlcluster "$line" "$name" start
# shellcheck disable=SC1091
. /etc/os-release
echo "== ${PRETTY_NAME}, PostgreSQL $line"

fail() { echo "FAIL: $*" >&2; exit 1; }
ok() { echo "ok: $*"; }

# The helper's own way in: psql as the postgres role over the socket, the
# script on standard input, the search path pinned at connection time.
pinned="-c search_path=pg_catalog,pg_temp"
admin() { PGOPTIONS=$pinned runuser -u postgres -- psql -X -q -A -t -F $'\t' -v ON_ERROR_STOP=1 -p "$port" -d "$1" -f -; }
# The same without the pin (what the helper did before).
bare() { runuser -u postgres -- psql -X -q -A -t -F $'\t' -v ON_ERROR_STOP=1 -p "$port" -d "$1" -f -; }
# A database user, over TCP with its password.
user() { PGPASSWORD=user-pass-1 psql -X -q -A -t -v ON_ERROR_STOP=1 -h 127.0.0.1 -p "$port" -U site -d "$1" -f -; }

admin postgres <<'SQL'
CREATE ROLE site LOGIN PASSWORD 'user-pass-1';
CREATE DATABASE site_db OWNER site TEMPLATE template0 ENCODING 'UTF8';
REVOKE CONNECT, TEMPORARY ON DATABASE site_db FROM PUBLIC;
GRANT CONNECT ON DATABASE site_db TO site;
SQL

# In the postgres database, only where PUBLIC may still create in public.
if [ "$(admin postgres <<<"SELECT has_schema_privilege('site', 'public', 'CREATE');")" = t ]; then
	user postgres <<'SQL'
CREATE TABLE public.hits (db text, who text);
GRANT ALL ON public.hits TO PUBLIC;
CREATE FUNCTION public.spy(oid, regrole) RETURNS boolean LANGUAGE plpgsql AS $$
BEGIN
	INSERT INTO public.hits VALUES (current_database(), current_user);
	RETURN $1 OPERATOR(pg_catalog.=) $2::pg_catalog.oid;
END $$;
CREATE OPERATOR public.= (LEFTARG = oid, RIGHTARG = regrole, FUNCTION = public.spy);
SQL
	hits() { admin postgres <<<"SELECT count(*) FROM public.hits;"; }
	old="SELECT 'g', d.datname FROM pg_database d WHERE NOT d.datistemplate AND EXISTS (SELECT 1 FROM aclexplode(d.datacl) a WHERE a.grantee = 'site'::regrole);"
	new="SELECT 'g', d.datname FROM pg_database d WHERE NOT d.datistemplate AND EXISTS (SELECT 1 FROM aclexplode(d.datacl) a WHERE a.grantee OPERATOR(pg_catalog.=) 'site'::pg_catalog.regrole::pg_catalog.oid);"
	# The planted operator is live: the old statement, unpinned, calls it.
	bare postgres <<<"$old" >/dev/null
	[ "$(hits)" -gt 0 ] || fail "control: the planted operator was not picked up"
	ok "control: unpinned, the old grantee lookup calls public.= (oid, regrole)"
	admin postgres <<<"TRUNCATE public.hits;"
	admin postgres <<<"$old" >/dev/null
	[ "$(hits)" = 0 ] || fail "pinned search path: the old statement still calls public.="
	ok "pinned: the old grantee lookup resolves to pg_catalog"
	admin postgres <<<"$new" >/dev/null
	bare postgres <<<"$new" >/dev/null
	[ "$(hits)" = 0 ] || fail "the new grantee lookup calls public.="
	ok "the new grantee lookup is an exact pg_catalog match, pinned or not"
	# It still finds the databases the role has privileges on.
	[ "$(admin postgres <<<"$new")" = $'g\tsite_db' ] || fail "grantee lookup: $(admin postgres <<<"$new")"
	ok "the grantee lookup returns the granted database"
else
	ok "PostgreSQL $line: PUBLIC cannot create in postgres.public (nothing to plant there)"
fi

# In a database the user owns, on any version: the owner can set the
# database's search path and create in public.
user site_db <<'SQL'
CREATE TABLE public.hits (who text);
GRANT ALL ON public.hits TO PUBLIC;
CREATE FUNCTION public.now() RETURNS timestamptz LANGUAGE plpgsql AS $$
BEGIN
	INSERT INTO public.hits VALUES (current_user);
	RETURN pg_catalog.now();
END $$;
SQL
PGPASSWORD=user-pass-1 psql -X -q -h 127.0.0.1 -p "$port" -U site -d site_db -c 'ALTER DATABASE site_db SET search_path = public, pg_catalog'
uhits() { admin site_db <<<"SELECT count(*) FROM public.hits;"; }
bare site_db <<<"SELECT now();" >/dev/null
[ "$(uhits)" -gt 0 ] || fail "control: the database's search path did not apply"
ok "control: unpinned, a superuser session in the user's database calls public.now()"
admin site_db <<<"TRUNCATE public.hits;"
[ "$(admin site_db <<<"SHOW search_path;")" = "pg_catalog,pg_temp" ] || fail "search_path: $(admin site_db <<<"SHOW search_path;")"
admin site_db <<<"SELECT now();" >/dev/null
[ "$(uhits)" = 0 ] || fail "pinned search path: public.now() was called"
ok "pinned: the connection option overrides the database's search path; now() is pg_catalog's"
# pg_dump the way the helper runs it.
PGOPTIONS=$pinned runuser -u postgres -- "/usr/lib/postgresql/$line/bin/pg_dump" -Fc --no-owner --no-privileges -p "$port" -d site_db >/dev/null
[ "$(uhits)" = 0 ] || fail "pg_dump called public.now()"
ok "pg_dump with the pinned options runs and calls nothing planted"

# The helper's other statements still work pinned, in the user's database
# too (grants, reassigning) and on names that need quoting.
admin postgres <<'SQL'
CREATE ROLE reader LOGIN PASSWORD 'reader-pass-1';
CREATE DATABASE "x_db" TEMPLATE template0 ENCODING 'UTF8';
REVOKE CONNECT, TEMPORARY ON DATABASE "x_db" FROM PUBLIC;
SQL
admin x_db <<<"REVOKE CREATE ON SCHEMA public FROM PUBLIC; ALTER SCHEMA public OWNER TO \"site\";"
admin site_db <<'SQL'
GRANT CONNECT ON DATABASE "site_db" TO "reader";
GRANT USAGE ON SCHEMA public TO "reader";
GRANT SELECT ON ALL TABLES IN SCHEMA public TO "reader";
ALTER DEFAULT PRIVILEGES FOR ROLE "site" IN SCHEMA public GRANT SELECT ON TABLES TO "reader";
REASSIGN OWNED BY "reader" TO "site";
DROP OWNED BY "reader";
SQL
admin postgres <<'SQL' >/dev/null
SELECT 'v', current_setting('server_version');
SELECT 'd', d.datname, pg_database_size(d.oid), pg_get_userbyid(d.datdba) FROM pg_database d WHERE NOT d.datistemplate ORDER BY 2;
SELECT pg_reload_conf();
DROP DATABASE "x_db" WITH (FORCE);
DROP ROLE "reader";
SQL
ok "create, grant, reassign, status, reload and drop statements run pinned"
echo "== passed"
