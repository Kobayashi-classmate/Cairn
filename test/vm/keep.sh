#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Runs as root inside the VM that KEEP=1 leaves running for a person to try
# (see run.sh): installs the panel the way a user would (firewall on,
# Chinese by default), points the ACME resolver at the test zone and
# prints what the person needs, after a "-- setup" line. First-run setup
# is left to the person.
set -euo pipefail
R=http://10.0.2.2:8080/releases
H=10.0.2.2
grep -q ' pebble$' /etc/hosts || echo "$H pebble" >>/etc/hosts
curl -fsSL "$R/latest/download/install.sh" -o /root/install.sh
sh /root/install.sh --mirror "$R" --lang zh-CN --yes >/root/install.log 2>&1 || { tail -30 /root/install.log; exit 1; }
printf '\n[acme]\nresolvers = ["%s:53"]\n' "$H" >>/etc/cairn/config.toml
systemctl restart cairn-web.service
systemctl stop cairn-helper.service 2>/dev/null || true
token=$(grep -o 'token=[A-Za-z0-9_-]*' /root/install.log | head -1 | cut -d= -f2)
. /etc/os-release
cat <<EOF
-- setup
${PRETTY_NAME}, Cairn $(cairn version 2>/dev/null || dpkg-query -W -f '${Version}' cairn 2>/dev/null || rpm -q cairn)

Panel (through the SSH tunnel): https://127.0.0.1:28443/setup?token=$token
  The link is valid for 60 minutes; a new one (it shows the VM's own address, use 127.0.0.1:28443 instead):
  docker exec cairn-vm ssh -i /work/key -p 2222 -o StrictHostKeyChecking=no tester@127.0.0.1 sudo cairn setup-link
Sites: add "127.0.0.1 site.test www.site.test app.test shop.test" to your computer's hosts file,
  then http://site.test:28080 and https://site.test:28444 (also through the tunnel).

Test services (the VM reaches them at $H):
  ACME CA: Certificates > Settings > custom CA, directory https://pebble:14000/dir,
    root certificate: the PEM below. Every name under .test resolves to this VM.
  DNS-01: DNS account of type RFC 2136, server $H:53, key name "cairn.", algorithm hmac-sha256,
    key: see below; zone "test".
  Backups: S3 storage, endpoint $H:8081, bucket cairn-backups, access key TESTKEY,
    secret key TESTSECRET, path style, plain HTTP.
Root certificate of Pebble's HTTPS:
$(cat /tmp/minica.pem)
EOF
