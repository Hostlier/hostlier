"""Fresh Lima fleet integration test. Run from the module root with make e2e-fleet."""
import argparse
import base64
import concurrent.futures
import errno
import hashlib
import ipaddress
import json
import os
import platform
import pty
import re
import secrets
import select
import shutil
import signal
import socket
import struct
import subprocess
import tarfile
import tempfile
import time
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
ROLES = ("primary", "secondary", "blue", "green")
MASTERS = ROLES[:2]
SOCKET = "/run/hostlier/controller.sock"


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def run(*args, **kw):
    result = subprocess.run(args, text=True, capture_output=True, timeout=kw.pop("timeout", 600), **kw)
    if result.returncode:
        raise RuntimeError(f"{Path(args[0]).name} failed ({result.returncode}): {result.stderr[-2000:]} {result.stdout[-2000:]}")
    return result.stdout.strip()


def available_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def archives(stage):
    assets = ROOT / "collector/dist/collector-image"
    if not (assets / "index.json").is_file():
        run(str(ROOT / "collector/package-image.sh"), timeout=2700)
    releases = stage / "profiles/releases"
    releases.mkdir(parents=True)
    linux = dict(os.environ, GOOS="linux", GOARCH="arm64", CGO_ENABLED="0")
    run("go", "build", "-o", stage / "laptop", "./cmd/hostlier")
    for kind, binary in (("cli", "hostlier"), ("controller", "hostlier-controller")):
        run("go", "build", "-trimpath", "-o", stage / binary, "./cmd/" + binary, env=linux)
        archive = releases / f"hostlier-{kind}-linux-arm64.tar.gz"
        with tarfile.open(archive, "w:gz", compresslevel=1) as tar:
            tar.add(stage / binary, arcname=binary)
            if kind == "controller":
                tar.add(assets, arcname="collector-image")
        digest = hashlib.sha256(archive.read_bytes()).hexdigest()
        Path(str(archive) + ".sha256").write_text(f"{digest}  {archive.name}\n")
    return sorted(releases.glob("*.gz"))


def certificates(stage, roles):
    def ssl(*args):
        return run("openssl", *args, cwd=stage)
    ssl("req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "2",
        "-subj", "/CN=Hostlier disposable fleet CA", "-keyout", "ca.key", "-out", "ca.crt")
    for role in roles:
        host = role + ".fleet.test"
        (stage / "extensions").write_text(f"subjectAltName=DNS:{host},DNS:*.{host},DNS:localhost\nextendedKeyUsage=serverAuth\n")
        ssl("req", "-newkey", "rsa:2048", "-nodes", "-subj", "/CN=" + host,
            "-keyout", host + ".key", "-out", "leaf.csr")
        ssl("x509", "-req", "-in", "leaf.csr", "-CA", "ca.crt", "-CAkey", "ca.key",
            "-CAcreateserial", "-days", "2", "-extfile", "extensions", "-out", host + ".crt")


class Fleet:
    def __init__(self, stage, roles):
        self.stage = stage
        self.names = {role: f"hf-{secrets.token_hex(3)}-{role}" for role in roles}
        self.created = []
        self.ports = {}
        self.users = {}
        self.env = dict(os.environ, HOSTLIER_HOME=str(stage / "profiles"), HOSTLIER_CA_FILE=str(stage / "ca.crt"))
        self.env["PATH"] = str(stage) + os.pathsep + self.env["PATH"]

    def shell(self, role, *args, **kw):
        return run("limactl", "shell", "--workdir=/tmp", self.names[role], *args, **kw)

    def copy(self, role, *files):
        run("limactl", "copy", *files, self.names[role] + ":/tmp/")

    def create(self, role):
        ports = ""
        if role in MASTERS:
            self.ports[role] = available_port()
            ports = f"  - guestPort: 443\n    hostPort: {self.ports[role]}\n"
        template = (ROOT / "test/e2e/lima-arm64.yaml").read_text()
        template += "\nnetworks:\n  - lima: user-v2\nportForwards:\n" + ports
        template += "  - guestPortRange: [1, 65535]\n    ignore: true\n"
        config = self.stage / (role + ".yaml")
        config.write_text(template)
        name = self.names[role]
        size = ["--memory=4", "--cpus=2"] if role == "k3s" else ["--memory=1", "--cpus=1"]
        run("limactl", "create", "--tty=false", *size, "--name=" + name, config)
        self.created.append(name)
        run("limactl", "start", "--tty=false", name)

    def provision(self, role):
        host = role + ".fleet.test"
        self.copy(role, self.stage / "ca.crt", self.stage / (host + ".crt"), self.stage / (host + ".key"),
                  ROOT / "test/e2e/fleet/guest.sh")
        self.shell(role, "sudo", "bash", "/tmp/guest.sh", host)

    def set_network(self):
        network = next(line.split()[2] for line in run("limactl", "network", "list").splitlines() if line.split()[0] == "user-v2")
        subnet = ipaddress.ip_network(network, strict=False)
        hosts = ""
        for role in self.names:
            interfaces = json.loads(self.shell(role, "ip", "-j", "-4", "addr", "show", "scope", "global"))
            ips = [ipaddress.IPv4Address(addr["local"]) for dev in interfaces for addr in dev["addr_info"]]
            matches = [ip for ip in ips if ip in subnet]
            require(len(matches) == 1, f"{role}: expected one address on {subnet}, found {ips}")
            ip = matches[0]
            hosts += f"{ip} {role}.fleet.test\n"
        for role in self.names:
            self.shell(role, "sudo", "tee", "-a", "/etc/hosts", input=hosts)

    def install(self, role, bundles, runtime=None):
        self.copy(role, *bundles)
        host = role + ".fleet.test"
        for bundle in bundles:
            self.shell(role, "tar", "-xzf", bundle.name)
        options = ["--runtime", runtime] if runtime else []
        self.shell(role, "sudo", "./hostlier-controller", "init", "--yes", "--domain", host, "--controller-hostname", host,
                   *options, timeout=900)

    def master(self, role, bundles):
        self.install(role, bundles)
        self.copy(role, self.stage / "owner.pub")
        self.shell(role, "sudo", "hostlier", "security", "enroll", "--public-key", "/tmp/owner.pub",
                   "--name", "Fleet fixture", "--socket", SOCKET)

    def ssh_config(self):
        run("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", self.stage / "box-key")
        with (self.stage / "ssh.config").open("w") as config, (self.stage / "known_hosts").open("w") as known:
            for role in ROLES[2:]:
                name = self.names[role]
                host = role + ".fleet.test"
                user = self.shell(role, "id", "-un")
                self.users[role] = user
                port = json.loads(run("limactl", "ls", "--format=json", name))["sshLocalPort"]
                self.shell(role, "sh", "-c", 'cat >> "$HOME/.ssh/authorized_keys"', input=(self.stage / "box-key.pub").read_text())
                key = self.shell(role, "cat", "/etc/ssh/ssh_host_ed25519_key.pub")
                known.write(f"{host} {key}\n")
                config.write(f"Host {host}\n  HostName 127.0.0.1\n  Port {port}\n  HostKeyAlias {host}\n"
                             f'  IdentityFile "{self.stage}/box-key"\n  IdentitiesOnly yes\n  UserKnownHostsFile "{self.stage}/known_hosts"\n')
        for name, script in (("ssh", 'echo ssh >> "$FLEET_SSH_LOG"\nexec /usr/bin/ssh "$@"'), ("open", "exit 0")):
            path = self.stage / name
            path.write_text("#!/bin/sh\n" + script + "\n")
            path.chmod(0o700)
        self.env["FLEET_SSH_LOG"] = str(self.stage / "ssh-count")

    def pair(self, master, cmd):
        # Keep interactive device possession and polling. Authorize this request
        # through the disposable master's protected socket instead of opening a UI.
        fd, slave = pty.openpty()
        try:
            # Administrative Lima SSH is separate from measured CLI bootstrap SSH.
            env = self.env if cmd[0] != "limactl" else os.environ
            proc = subprocess.Popen(cmd, stdin=slave, stdout=slave, stderr=slave, env=env)
        finally:
            os.close(slave)
        output = b""
        approved = False
        deadline = time.monotonic() + 150
        try:
            while time.monotonic() < deadline:
                if not select.select([fd], [], [], 1)[0]:
                    continue
                try:
                    chunk = os.read(fd, 32768)
                except OSError as exc:
                    if exc.errno != errno.EIO:
                        raise
                    break
                if not chunk:
                    break
                output += chunk
                match = re.search(rb"Request ID: ([a-zA-Z0-9_-]+)\r?\n", output)
                if not approved and match:
                    self.shell(master, "sudo", "hostlier", "pairing", "code", match[1].decode(), "--socket", SOCKET)
                    approved = True
            require(approved and proc.wait(timeout=5) == 0, "Device pairing failed: " + output.decode()[-1200:])
        finally:
            if proc.poll() is None:
                proc.kill()
                proc.wait()
            os.close(fd)

    def cli(self, *args):
        return json.loads(run(self.stage / "laptop", *args, "--json", env=self.env))

    def status(self, role, *args):
        result = self.cli("status", *args)
        require(result.get("hostname") == role + ".fleet.test", "Unexpected workload identity: " + repr(result.get("hostname")))

    def exercise(self):
        for master in MASTERS:
            self.pair(master, [self.stage / "laptop", "connect", f"https://localhost:{self.ports[master]}", "--name", master])
        self.cli("server", "use", "primary")
        pins = {}
        for expected, role in enumerate(ROLES[2:], 1):
            args = ["server", "add"] if role == "blue" else ["connect"]
            record = self.cli(*args, self.users[role] + "@" + role + ".fleet.test", "--ssh-config", str(self.stage / "ssh.config"))
            pins[role] = record["fingerprint"]
            require(self.ssh_count() == expected, f"{role} did not use exactly one bootstrap SSH connection")
            self.status("primary" if role == "blue" else "green")
        require(pins["blue"] != pins["green"], "Box controller identities must be distinct")
        for selection, role in (("primary/blue", "blue"), ("green", "green"), ("primary", "primary"), ("blue", "blue")):
            self.cli("server", "use", selection)
            self.status(role)
        print("PASS two SSH sessions, selection preserved by add and changed by connect, four laptop switches", flush=True)
        self.pair("secondary", ["limactl", "shell", "--workdir=/tmp", self.names["blue"], "sudo", "script", "-qefc",
                                "hostlier-controller connect https://secondary.fleet.test", "/dev/null"])
        for restarted in (False, True):
            if restarted:
                self.shell("blue", "sudo", "systemctl", "restart", "hostlier")
            self.wait_status("blue", "secondary")
            self.status("green", "--server", "primary", "--on", "green")
            for master in MASTERS:
                record = self.cli("server", "list", master)["hosts"]["blue"]
                require(record["fingerprint"] == pins["blue"], "Enrollment pin changed on " + master)
            self.shell("primary", "python3", "-c", "import socket; socket.create_connection(('blue.fleet.test', 7464), 5).close()")
            cursor = self.shell("blue", "sudo", "journalctl", "-u", "hostlier", "-n0", "--show-cursor")
            cursor = cursor.split("-- cursor: ")[-1].strip()
            denied = subprocess.run([self.stage / "laptop", "status", "--server", "primary", "--on", "blue"], env=self.env,
                                    capture_output=True, text=True, timeout=30)
            require(denied.returncode and "Enrolled host unreachable" in denied.stderr, "Former master unexpectedly reached Blue or failed for another reason")
            log = self.shell("blue", "sudo", "journalctl", "-u", "hostlier", "--after-cursor", cursor)
            require("fleet peer identity mismatch" in log, "No fresh TLS rejection from Blue")
            print("PASS handoff and old master denied" + (" after restart" if restarted else ""), flush=True)
        require(self.ssh_count() == 2, "Workload access reopened SSH")

    def idle_service(self, role):
        # Both runtime checks deploy and call the same echo application.
        binary = self.stage / "echo-service"
        run("go", "build", "-o", binary, "./test/e2e/fleet/fixture",
            env=dict(os.environ, GOOS="linux", GOARCH="arm64", CGO_ENABLED="0"))
        files = [binary, ROOT / "test/e2e/fleet/idle.sh"]
        if role == "k3s":
            files.append(ROOT / "test/e2e/fleet/k3s_idle.py")
        self.copy(role, *files)
        result = self.shell(role, "sudo", "bash", "/tmp/idle.sh", role, timeout=900)
        print("\n".join(line for line in result.splitlines() if line.startswith("PASS")), flush=True)

    def ssh_count(self):
        return len((self.stage / "ssh-count").read_text().splitlines())

    def wait_status(self, role, master):
        for attempt in range(30):
            try:
                self.status(role, "--server", master, "--on", role)
                return
            except RuntimeError:
                if attempt == 29:
                    raise
                time.sleep(1)

    def cleanup(self):
        failures = []
        for name in reversed(self.created):
            try:
                run("limactl", "delete", "--force", name)
            except RuntimeError as exc:
                failures.append(str(exc))
        require(not failures, "Cleanup failed: " + repr(failures))


def run_k3s_idle(fleet, bundles):
    fleet.create("k3s")
    fleet.set_network()
    fleet.provision("k3s")
    print("Installing Hostlier and its pinned k3s runtime", flush=True)
    fleet.install("k3s", bundles, "k3s")
    print("Deploying and testing the k3s idle service", flush=True)
    fleet.idle_service("k3s")


def run_fleet(fleet, bundles):
    # A structurally valid SSH security-key public record marks owner setup complete.
    # No private owner key or signature is used. Pairing uses the local admin API.
    alg = b"sk-ssh-ed25519@openssh.com"
    blob = b"".join(struct.pack(">I", len(v)) + v for v in (alg, secrets.token_bytes(32), b"ssh:hostlier"))
    (fleet.stage / "owner.pub").write_bytes(alg + b" " + base64.b64encode(blob) + b"\n")
    # The first guest starts Lima's shared user-v2 network.
    fleet.create("primary")
    with concurrent.futures.ThreadPoolExecutor(3) as pool:
        list(pool.map(fleet.create, ROLES[1:]))
    fleet.set_network()
    print("Preparing Docker, Nginx and guest TLS", flush=True)
    with concurrent.futures.ThreadPoolExecutor(4) as pool:
        list(pool.map(fleet.provision, ROLES))
    for role in MASTERS:
        print("Installing master " + role, flush=True)
        fleet.master(role, bundles)
    fleet.ssh_config()
    fleet.exercise()
    if os.environ.get("FLEET_TEST_IDLE") == "1":
        print("Deploying and waking the idle HTTP fixture", flush=True)
        fleet.idle_service("blue")


def main():
    require(platform.system() == "Darwin" and platform.machine() == "arm64", "This Lima VZ fixture requires Apple Silicon")
    for command in ("go", "openssl", "limactl", "ssh", "ssh-keygen"):
        require(shutil.which(command), "Missing prerequisite: " + command)
    os.chdir(ROOT)
    os.umask(0o077)
    def interrupted(*_):
        raise KeyboardInterrupt()
    signal.signal(signal.SIGTERM, interrupted)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--k3s-idle", action="store_true", help="run only the fresh k3s sleep/wake test")
    args = parser.parse_args()
    roles = ("k3s",) if args.k3s_idle else ROLES
    with tempfile.TemporaryDirectory(prefix="hf-", dir="/tmp") as temp:
        stage = Path(temp)
        fleet = Fleet(stage, roles)
        print("Building current tree and test certificates", flush=True)
        bundles = archives(stage)
        certificates(stage, roles)
        try:
            print("Creating fresh VMs " + ", ".join(fleet.names.values()), flush=True)
            if args.k3s_idle:
                run_k3s_idle(fleet, bundles)
            else:
                run_fleet(fleet, bundles)
        finally:
            print("Removing disposable VMs", flush=True)
            fleet.cleanup()
    print("PASS VM test and cleanup")


if __name__ == "__main__":
    main()
