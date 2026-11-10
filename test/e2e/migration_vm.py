#!/usr/bin/env python3
"""Run only INSIDE the disposable hostlier-runtime-migration-test Ubuntu VM.

Copy current hostlier-controller, hostlier, collector-image/, migration-fixture and
this script into /tmp, install Docker/Nginx/Certbot, then run setup and exercise.
Uses trusted private TLS and protected local administration; never production.
"""
import io
import json
import os
from pathlib import Path
import pty
import select
import signal
import shutil
import subprocess
import sys
import tarfile
import time
import urllib.request

ROOT = Path("/root/hostlier-migration-e2e")
SOCKET = "/run/hostlier/controller.sock"
CONTROL = "hostlier.apps.migration.test"
APP = "margin.apps.migration.test"
TLS = Path("/etc/hostlier-migration-fixture")


def check(condition, message):
    if not condition:
        raise RuntimeError(message)


def run(*args, data=None, timeout=900):
    result = subprocess.run(args, input=data, capture_output=True, timeout=timeout)
    check(result.returncode == 0, f"{args[:3]} failed: {result.stderr.decode()[-4000:]} {result.stdout.decode()[-2000:]}")
    return result.stdout


def cli(*args, data=None):
    return run("/usr/local/bin/hostlier", *args, "--socket", SOCKET, data=data)


def approved(*args):
    master, slave = pty.openpty()
    process = subprocess.Popen(["/usr/local/bin/hostlier", *args, "--socket", SOCKET],
                               stdin=slave, stdout=slave, stderr=slave)
    os.close(slave)
    output = b""
    sent = False
    deadline = time.monotonic() + 300
    try:
        while time.monotonic() < deadline:
            if select.select([master], [], [], .1)[0]:
                try:
                    chunk = os.read(master, 65536)
                except OSError:
                    break
                if not chunk:
                    break
                output += chunk
                if b"[y/N]" in output and not sent:
                    os.write(master, b"y\n")
                    sent = True
            if process.poll() is not None:
                break
        check(process.wait(timeout=5) == 0 and sent, "Deployment approval failed: " + output.decode()[-5000:])
    finally:
        if process.poll() is None:
            process.kill()
        os.close(master)


def request(path, value=None):
    with urllib.request.urlopen("https://" + APP + path, data=value, timeout=20) as response:
        return response.read()


def ready():
    deadline = time.monotonic() + 90
    while time.monotonic() < deadline:
        try:
            if request("/ready") == b"ready":
                cli("status", "margin")
                return
        except Exception:
            pass
        time.sleep(.5)
    raise RuntimeError("Application/controller did not become ready")


def setup():
    check(not Path("/var/lib/hostlier/installation.json").exists(), "Fixture requires a fresh server")
    ROOT.mkdir(mode=0o700)
    os.chdir(ROOT)
    run("systemctl", "enable", "--now", "docker", "nginx")
    run("openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "2",
        "-subj", "/CN=Disposable migration CA", "-addext", "basicConstraints=critical,CA:TRUE",
        "-addext", "keyUsage=critical,keyCertSign,cRLSign", "-keyout", "ca.key", "-out", "ca.crt")
    Path("extensions").write_text(f"subjectAltName=DNS:{CONTROL},DNS:{APP},DNS:localhost\nextendedKeyUsage=serverAuth\nkeyUsage=critical,digitalSignature,keyEncipherment\nauthorityKeyIdentifier=keyid,issuer\n")
    run("openssl", "req", "-newkey", "rsa:2048", "-nodes", "-subj", "/CN=" + CONTROL,
        "-keyout", "leaf.key", "-out", "leaf.csr")
    run("openssl", "x509", "-req", "-in", "leaf.csr", "-CA", "ca.crt", "-CAkey", "ca.key",
        "-CAcreateserial", "-days", "2", "-extfile", "extensions", "-out", "leaf.crt")
    shutil.copy("ca.crt", "/usr/local/share/ca-certificates/migration-test.crt")
    TLS.mkdir(mode=0o700)
    shutil.copy("leaf.crt", TLS / "leaf.crt")
    shutil.copy("leaf.key", TLS / "leaf.key")
    run("update-ca-certificates")
    with open("/etc/hosts", "a") as hosts:
        hosts.write(f"\n127.0.0.1 {CONTROL} {APP}\n")
    Path("/etc/hostlier").mkdir(mode=0o700, exist_ok=True)
    cert = {"certificate": str(TLS / "leaf.crt"), "key": str(TLS / "leaf.key")}
    Path("/etc/hostlier/nginx-existing-tls.json").write_text(json.dumps({CONTROL: cert, APP: cert}))
    Path("/etc/nginx/conf.d/migration-default.conf").write_text(f"""
server {{
    listen 443 ssl default_server;
    server_name localhost;
    ssl_certificate {TLS}/leaf.crt;
    ssl_certificate_key {TLS}/leaf.key;
    return 404;
}}
""")
    run("nginx", "-t")
    run("systemctl", "reload", "nginx")
    shutil.copy("/tmp/hostlier", "/usr/local/bin/hostlier")
    os.chmod("/usr/local/bin/hostlier", 0o755)
    run("/tmp/hostlier-controller", "init", "--domain", "apps.migration.test", "--runtime", "docker", "--yes")
    with tarfile.open("rootfs.tar", "w") as archive:
        archive.add("/tmp/migration-fixture", arcname="fixture")
        for name in ("data",):
            header = tarfile.TarInfo(name)
            header.type, header.mode, header.uid, header.gid = tarfile.DIRTYPE, 0o700, 1000, 1000
            archive.addfile(header)
        header = tarfile.TarInfo("data/value")
        header.size, header.mode, header.uid, header.gid = 4, 0o600, 1000, 1000
        archive.addfile(header, io.BytesIO(b"seed"))
    run("docker", "import", "--change", 'ENTRYPOINT ["/fixture"]', "rootfs.tar", "migration-fixture:test")
    run("docker", "save", "-o", "image.tar", "migration-fixture:test")
    cli("secret", "set", "migration-env", "--stdin", data=b"migration-env-secret")
    cli("secret", "set", "migration-file", "--stdin", data=b"migration-file-secret")
    # JSON is a strict subset of YAML. Both containers have independent storage,
    # credentials, memory limits and the same Margin confinement requirements.
    security = {"noSwap": True, "runAsUser": 1000, "runAsGroup": 1000,
                "readOnlyRootFilesystem": True, "noNewPrivileges": True,
                "dropCapabilities": ["ALL"], "pidsLimit": 256,
                "tmpfs": {"/tmp": {"size": "16Mi", "mode": 0o1777}}}
    component = {"source": {"container": {"archive": str(ROOT / "image.tar"), "architecture": "arm64"}},
                 "resources": {"cpu": "0.5", "memory": "128Mi"}, "security": security,
                 "volumes": {"data": {"mount": "/data", "size": "1Gi"}}}
    sidecar = dict(component, workload="service", endpoint={"exposure": "loopback", "port": 8081},
                   env={"PORT": "8081", "DATA_PATH": "/data"},
                   health={"exec": ["/fixture", "health", "http://127.0.0.1:8081/ready"]})
    project = dict(component, version=2, name="margin", workload="service",
                   endpoint={"exposure": "public", "port": 8080, "route": "https://" + APP},
                   env={"PORT": "8080", "DATA_PATH": "/data"}, secrets={"TOKEN": "secret://migration-env"},
                   configs={"credential": {"mount": "/run/secrets/credential", "secret": "migration-file", "mode": 0o444}},
                   health={"path": "/ready"}, sidecars={"storage": sidecar})
    Path("app.hostlier").write_text(json.dumps(project))
    deploy()


def deploy():
    project = json.loads((ROOT / "app.hostlier").read_text())
    project["secrets"]["TOKEN"] = "secret://migration-env"
    (ROOT / "app.hostlier").write_text(json.dumps(project))
    approved("deploy", "--file", str(ROOT / "app.hostlier"))
    ready()
    check(request("/data", b"docker-primary") == b"docker-primary", "initial primary write")
    check(request("/storage", b"docker-sidecar") == b"docker-sidecar", "initial sidecar write")
    (ROOT / "source-id").write_bytes(cli("status", "margin"))
    print("PASS Docker fixture: nonroot/read-only/capability/scratch probes, pinned secrets, primary and sidecar writes", flush=True)


def exercise():
    ready()
    source_primary = request("/data")
    source_sidecar = request("/storage")
    command = ["/usr/local/bin/hostlier-controller", "migrate"]
    retry = ["--retry"] if Path("/var/lib/hostlier/migration.json").exists() else []
    backup = str(ROOT / ("backups-" + str(time.time_ns())))
    preview = json.loads(run(*command, "plan", "--to", "k3s", "--backup-dir", backup, "--json", *retry))
    check(not preview["errors"], "Migration preflight: " + repr(preview["errors"]))
    check(len(preview["volumes"]) == 2 and preview["security"]["podPidsLimit"] == 256, "Incomplete storage/security preview")
    check(json.loads(cli("doctor"))["runtime"] == "docker", "preview changed runtime")
    run(*command, "apply", "--to", "k3s", "--backup-dir", backup, "--yes", *retry, timeout=1200)
    ready()
    check(json.loads(cli("doctor"))["runtime"] == "k3s", "cutover runtime")
    check(request("/data") == source_primary and request("/storage") == source_sidecar, "cold transfer lost data")
    check(request("/data", b"k3s-primary-new") == b"k3s-primary-new", "target primary write")
    check(request("/storage", b"k3s-sidecar-new") == b"k3s-sidecar-new", "target sidecar write")
    pods = json.loads(run("k3s", "kubectl", "get", "pods", "-A", "-l", "io.hostlier.app=margin", "-o", "json"))["items"]
    check(len(pods) == 1, "expected one target Pod")
    pod = pods[0]
    check(pod["status"]["phase"] == "Running", "target Pod not running")
    container = pod["spec"]["containers"][0]
    check(container["securityContext"]["runAsUser"] == 1000 and container["securityContext"]["readOnlyRootFilesystem"], "target confinement")
    config = json.loads(run("k3s", "kubectl", "get", "--raw", "/api/v1/nodes/" + pod["spec"]["nodeName"] + "/proxy/configz"))["kubeletconfig"]
    check(config["podPidsLimit"] == 256 and config["failSwapOn"], "live node security budgets")
    original = json.loads((ROOT / "source-id").read_bytes())["runtimeId"]
    check(run("docker", "inspect", "--format", "{{.State.Running}}", original).strip() == b"false", "Docker source still writing")
    run("systemctl", "restart", "hostlier")
    ready()
    check(request("/data") == b"k3s-primary-new", "target restart lost data")
    run(*command, "rollback", "--yes", timeout=900)
    ready()
    check(json.loads(cli("doctor"))["runtime"] == "docker", "rollback runtime")
    check(request("/data") == b"k3s-primary-new" and request("/storage") == b"k3s-sidecar-new", "rollback discarded target writes")
    journal = json.loads(Path("/var/lib/hostlier/migration.json").read_text())
    check(journal["phase"] == "rolled-back" and len(journal["backups"]) == 2 and len(journal["targetBackups"]) == 2, "rollback journal/backups incomplete")
    for archive in [journal["metadata"], *journal["backups"].values(), *journal["targetBackups"].values()]:
        check(Path(archive["file"]).is_file(), "backup disappeared")
    print("PASS real Docker → k3s → Docker: cold primary/sidecar data, pinned credentials, security probes, node PID/no-swap configuration, restart, and rollback preserving target writes", flush=True)


def crash_recovery():
    ready()
    command = ["/usr/local/bin/hostlier-controller", "migrate"]
    backup = str(ROOT / ("crash-backups-" + str(time.time_ns())))
    process = subprocess.Popen([*command, "apply", "--retry", "--backup-dir", backup, "--yes"],
                               stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
    stopped = False
    deadline = time.monotonic() + 600
    try:
        while time.monotonic() < deadline:
            line = process.stderr.readline()
            if b"Hostlier migration: restoring" in line:
                process.send_signal(signal.SIGSTOP)
                stopped = True
                break
            if not line and process.poll() is not None:
                break
        check(stopped, "Migration did not reach the crash injection boundary")
        run("systemctl", "start", "hostlier")
        state = run("systemctl", "show", "--property=ActiveState", "--value", "hostlier").strip()
        check(state == b"inactive", "systemd started the controller during an unresolved migration")
    finally:
        if process.poll() is None:
            process.kill()
        process.wait(timeout=10)
    run(*command, "rollback", "--yes")
    ready()
    check(request("/data") == b"k3s-primary-new" and request("/storage") == b"k3s-sidecar-new",
          "Crash recovery lost source data")
    print("PASS SIGKILL during restore: startup guard blocked systemd, journal recovery restored Docker and both volumes", flush=True)


if __name__ == "__main__":
    check(os.geteuid() == 0 and os.uname().nodename in ("hostlier-runtime-migration-test", "lima-hostlier-runtime-migration-test", "lima-hostlier-ubuntu2604"),
          "Refusing to run outside the named disposable migration VM")
    if sys.argv[1] == "setup":
        setup()
    elif sys.argv[1] == "deploy":
        deploy()
    elif sys.argv[1] == "exercise":
        exercise()
    elif sys.argv[1] == "crash":
        crash_recovery()
    else:
        raise RuntimeError("setup|exercise required")
