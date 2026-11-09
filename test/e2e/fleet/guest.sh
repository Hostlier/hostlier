#!/bin/bash
# Executed only inside a freshly created fleet fixture, before Hostlier installation.
set -eu
host=$1
test ! -e /var/lib/hostlier/installation.json
cd /tmp
export DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=l
apt-get -o Acquire::Retries=3 update -qq
apt-get -o Acquire::Retries=3 install -y --no-install-recommends docker.io nginx certbot logrotate
systemctl enable --now docker nginx
install -m644 ca.crt /usr/local/share/ca-certificates/fleet-test.crt
update-ca-certificates
install -d -m700 /etc/fleet-test
install -m600 "$host.key" /etc/fleet-test/key
install -m644 "$host.crt" /etc/fleet-test/cert
mkdir -p /etc/hostlier
printf '{"%s":{"certificate":"/etc/fleet-test/cert","key":"/etc/fleet-test/key"}}\n' "$host" > /etc/hostlier/nginx-existing-tls.json
# Loopback-only laptop port forwarding uses localhost. Guest traffic uses the
# normal Hostlier-generated TLS virtual host and its verified DNS name.
cat > /etc/nginx/conf.d/fleet-localhost.conf <<'NGINX'
server {
    listen 443 ssl default_server;
    server_name localhost;
    ssl_certificate /etc/fleet-test/cert;
    ssl_certificate_key /etc/fleet-test/key;
    location / { proxy_pass http://127.0.0.1:7463; }
}
NGINX
nginx -t
systemctl reload nginx
