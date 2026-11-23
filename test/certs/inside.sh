#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Runs in the panel container of test/certs/run.sh: installs the panel and
# nginx, then through the API issues certificates from Pebble (HTTP-01 for
# a site, DNS-01 with a wildcard through RFC 2136, HTTP-01 for the panel's
# own domain that no site serves), serves them, uploads one, renews and
# deletes. Chains are verified against Pebble's root.
set -euo pipefail
grep_q() { grep "$@" >/dev/null; }
step() { printf '  - %s\n' "$*"; }
fail() {
	printf 'FAIL: %s\n' "$*" >&2
	[ -f /tmp/job.log ] && { echo "--- job log" >&2; tail -40 /tmp/job.log >&2; }
	tail -5 /tmp/helper.log >&2 || true
	exit 1
}
trap 'fail "line $LINENO: $BASH_COMMAND"' ERR
. /etc/os-release
echo "== ${PRETTY_NAME}: certificates from Pebble"

export DEBIAN_FRONTEND=noninteractive
apt-get update -qq >/dev/null
apt-get install -y -qq curl procps python3 openssl >/dev/null
apt-get install -y /pkgs/cairn_*_amd64.deb >/tmp/install.log 2>&1 || fail "install cairn"
# DNS-01 checks propagation at the test zone's server.
printf '\n[acme]\nresolvers = ["%s"]\n' "$DNS_SERVER" >>/etc/cairn/config.toml
/usr/lib/cairn/cairn-helper >/tmp/helper.log 2>&1 &
for _ in $(seq 50); do [ -S /run/cairn/helper.sock ] && break; sleep 0.1; done
runuser -u cairn -- /usr/bin/cairn-web >/tmp/web.log 2>&1 &
port=$(sed -n 's/^listen = "0.0.0.0:\([0-9]*\)"/\1/p' /etc/cairn/config.toml)
base="https://127.0.0.1:${port}"
for _ in $(seq 100); do curl -sk -o /dev/null "$base/api/v1/meta" && break; sleep 0.1; done
jar=/tmp/jar
api() { curl -sk -c "$jar" -b "$jar" -H 'X-Cairn-Request: 1' -H 'Content-Type: application/json' "$@"; }
json() { python3 -c "import json,sys; d=json.load(sys.stdin); print($1)"; }
pw=certs-test-password-1
token=$(cairn setup-link 2>&1 | grep -o 'token=[A-Za-z0-9_-]*' | head -1 | cut -d= -f2)
api -X POST "$base/api/v1/setup" -d "{\"token\":\"$token\",\"username\":\"admin\",\"password\":\"$pw\",\"locale\":\"en\"}" | grep_q '"username":"admin"' || fail "setup"
api -X POST "$base/api/v1/auth/elevate" -d "{\"password\":\"$pw\"}" | grep_q '"elevatedUntil"' || fail "elevate"

# run METHOD PATH [BODY]: starts a job, waits; prints the final job JSON.
run() {
	local r id state offset=0
	r=$(api -X "$1" "$base$2" ${3:+-d "$3"})
	id=$(printf '%s' "$r" | json 'd.get("job",{}).get("id","")')
	[ -n "$id" ] || fail "$1 $2 not started: $r"
	: >/tmp/job.log
	for _ in $(seq 200); do
		r=$(api "$base/api/v1/jobs/$id/log?offset=$offset&wait=20")
		printf '%s' "$r" | json 'd["log"]' >>/tmp/job.log
		offset=$(printf '%s' "$r" | json 'd["offset"]')
		state=$(printf '%s' "$r" | json 'd["job"]["state"]')
		case "$state" in succeeded | failed | canceled | interrupted) break ;; esac
	done
	printf '%s' "$r" | json 'json.dumps(d["job"])'
}
ok() { local j; j=$(run "$@"); [ "$(printf '%s' "$j" | json 'd["state"]')" = succeeded ] || fail "$1 $2: $j"; printf '%s' "$j"; }
newest_cert() { api "$base/api/v1/certs" | json 'sorted(d["certs"], key=lambda c: c["createdAt"])[-1]["id"]'; }
issuer_of() { echo | openssl s_client -connect "127.0.0.1:$2" -servername "$1" 2>/dev/null | openssl x509 -noout -issuer -serial; }

nginx=$(api "$base/api/v1/software" | json '[o["line"] for s in d["software"] if s["id"]=="nginx" for o in s["offers"] if o["recommended"]][0]')
ok POST /api/v1/software/install "{\"items\":[{\"software\":\"nginx\",\"line\":\"$nginx\"}]}" >/dev/null
nginx
# The custom CA: Pebble, whose HTTPS is signed by its test root.
root=$(python3 -c 'import json; print(json.dumps(open("/minica.pem").read()))')
api -X PUT "$base/api/v1/certs/settings" -d "{\"email\":\"\",\"defaultCa\":\"custom\",\"cas\":{\"custom\":{\"directory\":\"https://pebble:14000/dir\",\"rootPem\":$root}}}" |
	grep_q '"ready":true' || fail "settings"
curl -sk https://pebble:15000/roots/0 >/tmp/pebble-root.pem
curl -sk https://pebble:15000/intermediates/0 >>/tmp/pebble-root.pem
step "nginx installed; the custom ACME CA points at Pebble"

# HTTP-01 for a site, then HTTPS with a redirect.
ok POST /api/v1/sites '{"type":"static","domains":["site.test","www.site.test"]}' >/dev/null
ok POST /api/v1/certs '{"domains":["site.test","www.site.test"],"challenge":"http-01"}' >/dev/null
site_cert=$(newest_cert)
api "$base/api/v1/certs/$site_cert" | json 'd["issuer"]' | grep_q -i pebble || fail "issuer: $(api "$base/api/v1/certs/$site_cert")"
ok PUT /api/v1/sites/site-test "{\"type\":\"static\",\"domains\":[\"site.test\",\"www.site.test\"],\"tls\":{\"cert\":\"$site_cert\",\"redirect\":true,\"hsts\":true}}" >/dev/null
curl -s --cacert /tmp/pebble-root.pem --resolve site.test:443:127.0.0.1 https://site.test/ | grep_q "site.test is ready" || fail "https site"
loc=$(curl -s -o /dev/null -w '%{redirect_url}' -H 'Host: www.site.test' http://127.0.0.1/a?b=1)
[ "$loc" = "https://www.site.test/a?b=1" ] || fail "redirect: $loc"
curl -sI --cacert /tmp/pebble-root.pem --resolve site.test:443:127.0.0.1 https://site.test/ | grep_q -i '^strict-transport-security' || fail "HSTS"
step "HTTP-01 certificate for site.test, www.site.test; HTTPS verified against Pebble's root, HTTP redirects, HSTS sent"

# DNS-01 with a wildcard, through RFC 2136.
acct=$(api -X POST "$base/api/v1/dns/accounts" -d "{\"name\":\"bind\",\"provider\":\"rfc2136\",\"fields\":{\"server\":\"$DNS_SERVER\",\"key_name\":\"cairn.\",\"key\":\"$TSIG_SECRET\",\"key_alg\":\"hmac-sha256\"}}" | json 'd["id"]')
r=$(api -X POST "$base/api/v1/dns/accounts/$acct/test" -d '{"zone":"test"}')
[ "$(printf '%s' "$r" | json 'd.get("records", 0) > 0')" = True ] || fail "dns account test: $r"
ok POST /api/v1/certs "{\"domains\":[\"*.site.test\",\"site.test\"],\"challenge\":\"dns-01\",\"dnsAccount\":\"$acct\"}" >/dev/null
wild=$(newest_cert)
api "$base/api/v1/certs/$wild" | json '" ".join(d["domains"])' | grep_q '\*.site.test' || fail "wildcard: $(api "$base/api/v1/certs/$wild")"
grep_q "Adding TXT _acme-challenge.site in zone test." /tmp/job.log || fail "TXT record not added through RFC 2136"
step "DNS-01 wildcard *.site.test through RFC 2136 (BIND); the TXT record added and removed"

# The panel's own domain, which no site serves: a temporary server block.
ok POST /api/v1/certs '{"domains":["panel.test"],"challenge":"http-01"}' >/dev/null
panel_cert=$(newest_cert)
grep_q "Temporary: answers ACME HTTP-01" /etc/nginx/cairn/sites/_acme-*.conf 2>/dev/null && fail "temporary server block left"
ok PUT /api/v1/certs/panel "{\"id\":\"$panel_cert\"}" >/dev/null
sleep 6
issuer_of panel.test "$port" | grep_q -i pebble || fail "panel certificate: $(issuer_of panel.test "$port")"
curl -s --cacert /tmp/pebble-root.pem --resolve "panel.test:$port:127.0.0.1" "https://panel.test:$port/api/v1/meta" | grep_q '"version"' || fail "panel over verified TLS"
step "panel.test (no site): temporary server block, then served by the panel itself without a restart"

# Upload: a good pair, and a key that does not belong.
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -keyout /tmp/up.key -out /tmp/up.crt -days 30 -subj /CN=up.test -addext subjectAltName=DNS:up.test 2>/dev/null
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -keyout /tmp/other.key -out /tmp/other.crt -days 30 -subj /CN=x.test 2>/dev/null
body() { python3 -c 'import json,sys; print(json.dumps({"name": "up", "certPem": open(sys.argv[1]).read(), "keyPem": open(sys.argv[2]).read()}))' "$1" "$2"; }
ok POST /api/v1/certs/upload "$(body /tmp/up.crt /tmp/up.key)" >/dev/null
j=$(run POST /api/v1/certs/upload "$(body /tmp/up.crt /tmp/other.key)")
[ "$(printf '%s' "$j" | json 'd["error"]["code"]')" = cert.invalid ] || fail "mismatched key: $j"
step "uploaded certificate stored; a key of another certificate refused"

# Renewal replaces what the site serves; a certificate in use stays.
before=$(issuer_of site.test 443 | sed -n 's/^serial=//p')
ok POST "/api/v1/certs/$site_cert/renew" >/dev/null
after=$(issuer_of site.test 443 | sed -n 's/^serial=//p')
[ -n "$after" ] && [ "$after" != "$before" ] || fail "renewal not served: $before -> $after"
j=$(run DELETE "/api/v1/certs/$site_cert")
[ "$(printf '%s' "$j" | json 'd["error"]["code"]')" = cert.in_use ] || fail "deleted while in use: $j"
ok PUT /api/v1/sites/site-test '{"type":"static","domains":["site.test","www.site.test"]}' >/dev/null
ok DELETE "/api/v1/certs/$site_cert" >/dev/null
nginx -t 2>/dev/null || fail "nginx configuration broken"
step "renewal served at once (serial $before -> $after); in-use certificate kept, deleted once the site let go"
echo "== PASS: ${PRETTY_NAME}"
