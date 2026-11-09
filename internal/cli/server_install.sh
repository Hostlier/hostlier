# Executed as root on the target over the one SSH session. stdout is the protocol stream.
set -eu
umask 077
exec 9>/run/hostlier-server-add.lock
flock -n 9 || { echo 'Another server installation is running' >&2; exit 1; }
socket=/run/hostlier/controller.sock
if [ -S "$socket" ]; then
    # Use the same local master-change endpoint as direct connect on an installed box.
    # Older controllers return 404 and continue through the release installer.
    member=$(python3 - "$socket" "$3" <<'CHECK_MEMBER'
import http.client, json, socket, sys

def request(method, body=None):
    with socket.socket(socket.AF_UNIX) as stream:
        stream.settimeout(10)
        stream.connect(sys.argv[1])
        client = http.client.HTTPConnection("localhost")
        client.sock = stream
        client.request(method, "/v1/fleet/master", body, {"Content-Type": "application/json"})
        response = client.getresponse()
        if response.status == 404 and method == "GET":
            return None
        if response.status != 200:
            sys.exit("Installed controller refused fleet configuration")
        return json.loads(response.read(65536))

member = request("GET")
if member is not None:
    pin = sys.argv[2]
    if member["fingerprint"] == pin:
        sys.exit("Cannot enroll the master as its own fleet member")
    if member["master"] not in ("", pin):
        sys.exit("This box has another master. Run hostlier-controller connect on the box to switch.")
    if not member["master"]:
        member = request("PUT", json.dumps({"previous": "", "fingerprint": pin}))
    print(json.dumps(member))
CHECK_MEMBER
    )
    if [ -n "$member" ]; then
        printf 'installed\n%s\n' "$member"
        exit 0
    fi
fi
stage=$(mktemp -d)
trap 'rm -rf -- "$stage"' EXIT
cd "$stage"
case "$(uname -m)" in
    x86_64) arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *) echo 'Unsupported server architecture' >&2; exit 1 ;;
esac
printf '%s\n' "$arch"
tar -xf -
for kind in controller cli; do
    archive="hostlier-$kind-linux-$arch.tar.gz"
    sha256sum -c "$archive.sha256" >&2
done
tar -xzf "hostlier-controller-linux-$arch.tar.gz" hostlier-controller collector-image
tar -xzf "hostlier-cli-linux-$arch.tar.gz" hostlier
if [ -S "$socket" ]; then
    identity=$(./hostlier hosts identity --socket "$socket" --json)
    own_pin=$(printf '%s' "$identity" | python3 -c 'import json,sys; print(json.load(sys.stdin)["fingerprint"])')
    [ "$own_pin" != "$3" ] || { echo 'Cannot enroll the master as its own fleet member' >&2; exit 1; }
fi
./hostlier-controller init --yes --domain "$1" --controller-hostname "$2" --master-fingerprint "$3" >&2
./hostlier hosts identity --socket "$socket" --json
