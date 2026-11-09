#!/bin/bash
# Build an image archive in the guest and deploy through its installed controller.
set -euo pipefail
cd /tmp
role=$1
host="$role.fleet.test"
route="echo.$host"
interval=3s
if [ "$role" = k3s ]; then interval=20s; fi
printf '127.0.0.1 %s\n' "$route" >> /etc/hosts
python3 - "$host" "$route" <<'PY'
import json, sys
from pathlib import Path
path = Path('/etc/hostlier/nginx-existing-tls.json')
refs = json.loads(path.read_text())
refs[sys.argv[2]] = refs[sys.argv[1]]
path.write_text(json.dumps(refs))
PY
tar -cf rootfs.tar echo-service
docker import --change 'ENTRYPOINT ["/echo-service"]' rootfs.tar fleet-echo:fixture
docker save -o echo-image.tar fleet-echo:fixture
cat > echo.hostlier <<YAML
version: 2
name: echo
source:
  container:
    archive: ./echo-image.tar
    architecture: arm64
endpoint:
  exposure: public
  port: 8080
  route: $route
health:
  path: /ready
sleepAfter: $interval
YAML
sock=/run/hostlier/controller.sock
hostlier deploy --file /tmp/echo.hostlier --socket "$sock"
if [ "$role" = k3s ]; then
    exec python3 /tmp/k3s_idle.py "$route"
fi
# The deployment imports a content-addressed tag. Read the exact primary runtime ID.
id=$(hostlier status echo --socket "$sock" --json | python3 -c 'import json,sys; print(json.load(sys.stdin)["runtimeId"])')
for attempt in $(seq 1 45); do
    if [ "$(docker inspect -f '{{.State.Running}}' "$id")" = false ]; then break; fi
    sleep 1
done
test "$(docker inspect -f '{{.State.Running}}' "$id")" = false
systemctl restart hostlier
for attempt in $(seq 1 30); do
    if hostlier status echo --socket "$sock" --json > observed.json; then break; fi
    sleep 1
done
python3 -c 'import json; assert json.load(open("observed.json"))["runtimeStatus"]["idleSleeping"]'
response=$(curl --fail --silent --show-error --max-time 90 --data-binary 'first request after sleep' "https://$route/order")
test "$response" = 'first request after sleep'
test "$(docker inspect -f '{{.State.Running}}' "$id")" = true
test "$(docker logs "$id" 2>&1 | grep -c 'POST /order')" -eq 1
echo "PASS Docker slept, persisted across restart, and served the first POST once"
