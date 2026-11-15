#!/bin/sh
# apt-get install that survives networks blocking plain-HTTP mirrors: on
# failure, retry over HTTPS trusting Node's bundled roots plus podbox CAs.
set -e
export DEBIAN_FRONTEND=noninteractive
if ! apt-get update; then
  command -v node >/dev/null || { echo "apt-get update failed and no node to bootstrap HTTPS" >&2; exit 1; }
  node -e 'process.stdout.write(require("tls").rootCertificates.join("\n") + "\n")' > /tmp/roots.pem
  cat /usr/local/share/ca-certificates/podbox/*.crt >> /tmp/roots.pem 2>/dev/null || true
  for f in /etc/apt/sources.list /etc/apt/sources.list.d/*; do
    [ -f "$f" ] && sed -i 's|http://|https://|g' "$f"
  done
  echo 'Acquire::https::CAInfo "/tmp/roots.pem";' > /etc/apt/apt.conf.d/99podbox-bootstrap
  apt-get update
fi
apt-get install -y --no-install-recommends "$@"
rm -rf /var/lib/apt/lists/* /etc/apt/apt.conf.d/99podbox-bootstrap /tmp/roots.pem
