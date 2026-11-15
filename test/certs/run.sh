#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Issues certificates end to end against Let's Encrypt's test CA (Pebble),
# never a real one: a throwaway Docker network with an authoritative BIND
# for the zone "test." (dynamic updates with a TSIG key, for RFC 2136
# DNS-01), Pebble resolving through it, and a Debian 13 container with the
# panel and nginx (test/certs/inside.sh). Build the packages first: make go
# package.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
ls dist/cairn_*_amd64.deb >/dev/null
PEBBLE_IMAGE=${PEBBLE_IMAGE:-ghcr.io/letsencrypt/pebble:latest}
work=$(mktemp -d)
net=cairn-acme-$$
cleanup() {
	docker rm -f "$net-dns" "$net-pebble" "$net-panel" >/dev/null 2>&1 || true
	docker network rm "$net" >/dev/null 2>&1 || true
	rm -rf "$work"
}
trap cleanup EXIT

# A private subnet nobody else uses; static addresses inside it.
for _ in $(seq 20); do
	sub=172.$((20 + RANDOM % 10)).$((RANDOM % 250)).
	docker network create --subnet "${sub}0/24" "$net" >/dev/null 2>&1 && break
	sub=""
done
[ -n "$sub" ] || { echo "no free subnet" >&2; exit 1; }
dns=${sub}2 pebble=${sub}3 panel=${sub}4
secret=$(head -c 32 /dev/urandom | base64)

mkdir -p "$work/bind" "$work/pebble"
cat >"$work/bind/named.conf" <<EOF
options {
    directory "/var/cache/bind";
    listen-on { any; };
    listen-on-v6 { none; };
    allow-query { any; };
    recursion no;
    dnssec-validation no;
};
key "cairn." {
    algorithm hmac-sha256;
    secret "$secret";
};
zone "test." {
    type primary;
    file "/var/lib/bind/test.zone";
    allow-update { key "cairn."; };
    # Listing the zone (the panel's account test) is a zone transfer.
    allow-transfer { key "cairn."; };
};
EOF
cat >"$work/bind/test.zone" <<EOF
\$TTL 60
@         IN SOA ns.test. admin.test. 1 60 60 600 60
@         IN NS  ns.test.
ns        IN A   $dns
site      IN A   $panel
www.site  IN A   $panel
panel     IN A   $panel
EOF
cat >"$work/bind/start.sh" <<'EOF'
set -e
apt-get update -qq >/dev/null
apt-get install -y -qq bind9 bind9-dnsutils >/dev/null
cp /cfg/named.conf /etc/bind/named.conf
cp /cfg/test.zone /var/lib/bind/test.zone
chown bind:bind /var/lib/bind/test.zone
exec named -g -u bind -c /etc/bind/named.conf
EOF
cat >"$work/pebble/config.json" <<EOF
{"pebble": {"listenAddress": "0.0.0.0:14000", "managementListenAddress": "0.0.0.0:15000",
  "certificate": "test/certs/localhost/cert.pem", "privateKey": "test/certs/localhost/key.pem",
  "httpPort": 80, "tlsPort": 443, "ocspResponderURL": "", "externalAccountBindingRequired": false,
  "retryAfter": {"authz": 1, "order": 1}, "keyAlgorithm": "ecdsa"}}
EOF
cid=$(docker create "$PEBBLE_IMAGE")
docker cp "$cid:/test/certs/pebble.minica.pem" "$work/pebble/minica.pem" >/dev/null
docker rm "$cid" >/dev/null

echo "== DNS (BIND, zone test.) at $dns, Pebble at $pebble, panel at $panel"
docker run -d --name "$net-dns" --network "$net" --ip "$dns" -v "$work/bind:/cfg:ro" debian:trixie bash /cfg/start.sh >/dev/null
for i in $(seq 120); do
	[ "$(docker exec "$net-dns" dig +short @127.0.0.1 ns.test 2>/dev/null)" = "$dns" ] && break
	[ "$i" = 120 ] && { docker logs "$net-dns" | tail -20; echo "BIND did not start" >&2; exit 1; }
	sleep 2
done
docker run -d --name "$net-pebble" --network "$net" --ip "$pebble" --network-alias pebble \
	-e PEBBLE_VA_NOSLEEP=1 -e PEBBLE_WFE_NONCEREJECT=0 -v "$work/pebble/config.json:/config.json:ro" \
	"$PEBBLE_IMAGE" -config /config.json -dnsserver "$dns:53" >/dev/null

docker run --rm --name "$net-panel" --network "$net" --ip "$panel" \
	-v "$PWD/dist:/pkgs:ro" -v "$PWD/test/certs:/t:ro" -v "$work/pebble/minica.pem:/minica.pem:ro" \
	-e DNS_SERVER="$dns:53" -e TSIG_SECRET="$secret" debian:trixie bash /t/inside.sh
echo "Certificate test passed."
