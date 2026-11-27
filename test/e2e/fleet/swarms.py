"""Live K3s swarm/permission gate on two disposable Lima VMs; no production profiles used."""
import argparse
import base64
import errno
import hashlib
import io
import json
import os
from pathlib import Path
import pty
import re
import secrets
import select
import shutil
import signal
import struct
import subprocess
import tarfile
import tempfile
import time

from run import Fleet, ROOT, SOCKET, archives, certificates, require, run


def terminal(command, env, callback=None, timeout=600):
    """Exercise real terminal confirmation, never --yes or an approval bypass."""
    fd, slave = pty.openpty()
    process = subprocess.Popen(command, stdin=slave, stdout=slave, stderr=slave, env=env)
    os.close(slave)
    output, answered = b"", 0
    deadline = time.monotonic() + timeout
    try:
        while time.monotonic() < deadline:
            if not select.select([fd], [], [], .2)[0]:
                if process.poll() is not None:
                    break
                continue
            try:
                chunk = os.read(fd, 65536)
            except OSError as exc:
                if exc.errno != errno.EIO:
                    raise
                break
            if not chunk:
                break
            output += chunk
            if callback:
                callback(output)
            prompts = output.count(b"[y/N]")
            if prompts > answered:
                os.write(fd, b"yes\n")
                answered = prompts
        require(process.poll() is not None or time.monotonic() < deadline, "CLI terminal timed out")
        return process.wait(timeout=10), output.decode(errors="replace")
    finally:
        if process.poll() is None:
            process.kill()
            process.wait()
        os.close(fd)


def image_archive(stage):
    """Package the static echo fixture as a portable image without a Docker daemon."""
    binary = stage / "echo-service"
    run("go", "build", "-o", binary, "./test/e2e/fleet/fixture",
        env=dict(os.environ, GOOS="linux", GOARCH="arm64", CGO_ENABLED="0"))
    layer = io.BytesIO()
    with tarfile.open(fileobj=layer, mode="w") as tar:
        info = tarfile.TarInfo("echo-service")
        info.size, info.mode = binary.stat().st_size, 0o755
        tar.addfile(info, io.BytesIO(binary.read_bytes()))
    contents = layer.getvalue()
    config = json.dumps({"architecture": "arm64", "os": "linux", "config": {"Entrypoint": ["/echo-service"], "User": "65532:65532"},
                         "rootfs": {"type": "layers", "diff_ids": ["sha256:" + hashlib.sha256(contents).hexdigest()]}}).encode()
    config_name = hashlib.sha256(config).hexdigest() + ".json"
    manifest = [{"Config": config_name, "RepoTags": ["hostlier-swarm-echo:fixture"], "Layers": ["layer.tar"]}]
    with tarfile.open(stage / "echo-image.tar", "w") as tar:
        for name, data in ((config_name, config), ("layer.tar", contents), ("manifest.json", json.dumps(manifest).encode())):
            info = tarfile.TarInfo(name)
            info.size = len(data)
            tar.addfile(info, io.BytesIO(data))


class SwarmGate(Fleet):
    def __init__(self, stage, report):
        super().__init__(stage, ("primary", "k3s"))
        self.report = report
        self.checks = []
        # Opening a browser is a test failure. Login is approved via the fixture's protected socket.
        blocker = stage / "open"
        blocker.write_text('#!/bin/sh\necho attempted >> "$HOSTLIER_BROWSER_ATTEMPTS"\nexit 1\n')
        blocker.chmod(0o700)
        self.env["HOSTLIER_BROWSER_ATTEMPTS"] = str(stage / "browser-attempts")

    def install(self, role, bundles, runtime=None):
        super().install(role, bundles, runtime)
        # Controller installation does not install a CLI; stage the separately built CLI for local pairing.
        self.shell(role, "sudo", "install", "-m755", "/tmp/hostlier", "/usr/local/bin/hostlier")

    def passed(self, message):
        self.checks.append(message)
        print("PASS " + message, flush=True)

    def command(self, profile, *args, mutate=False, deny=False, remote=True):
        browser_log = self.stage / "browser-attempts"
        browser_before = browser_log.read_bytes() if browser_log.exists() else b""
        command = [str(self.stage / "laptop"), *args, "--server", profile]
        if remote:
            command += ["--on", "k3s"]
        if mutate or deny:
            code, output = terminal(command, self.env)
        else:
            command += ["--json"]
            result = subprocess.run(command, env=self.env, capture_output=True, text=True, timeout=120)
            code, output = result.returncode, result.stdout + result.stderr
        browser_after = browser_log.read_bytes() if browser_log.exists() else b""
        require(browser_after == browser_before, "A management operation attempted to open a browser")
        if deny:
            require(code != 0 and any(term in output.lower() for term in
                    ("scope_denied", "outside", "permissions", "not authorized", "cannot be deleted", "move or purge", "obtain a new explicit grant", "expired", "revoked")),
                    "Expected authorization/state denial: " + output[-2500:])
            return output
        require(code == 0, "CLI failed: " + " ".join(args) + "\n" + output[-4000:])
        return output if mutate or tuple(args[:2]) == ("app", "logs") else json.loads(output)

    def login(self, name, restricted=False):
        approved = False
        def approve(output):
            nonlocal approved
            match = re.search(rb"Request ID: ([a-zA-Z0-9_-]+)\r?\n", output)
            if match and not approved:
                flags = ["--app", "unused-local-app", "--for", "2h", "--max-uses", "100"] if restricted else []
                self.shell("primary", "sudo", "hostlier", "pairing", "code", match[1].decode(), "--socket", SOCKET, *flags)
                approved = True
        code, output = terminal([str(self.stage / "laptop"), "connect", f"https://localhost:{self.ports['primary']}", "--name", name], self.env, approve, 180)
        require(code == 0 and approved, "Pairing failed: " + output[-1800:])
        clients = self.command("admin", "access", "clients", "list", remote=False)["clients"]
        return next(client["id"] for client in clients if client["name"] == name and not client.get("revoked"))

    def grant(self, actor, actions, swarm=None):
        args = ["access", "authorize", "--device", actor, "--on", "k3s", "--scope", "actions", "--actions", actions,
                "--for", "2h", "--max-uses", "100"]
        if swarm:
            args += ["--swarm", swarm]
        self.command("admin", *args, mutate=True, remote=False)

    def join(self):
        approved = False
        def approve(output):
            nonlocal approved
            match = re.search(rb"Approval code: ([A-F0-9]{8}-[A-F0-9]{8})", output)
            if match and not approved:
                # Verify the logged-in main CLI can approve this controller without fingerprint copying.
                self.command("admin", "server", "approve", match[1].decode(), mutate=True, remote=False)
                approved = True
        command = ["limactl", "shell", "--workdir=/tmp", self.names["k3s"], "sudo", "hostlier-controller", "connect",
                   "https://primary.fleet.test", "--name", "k3s"]
        code, output = terminal(command, os.environ, approve, 180)
        require(code == 0 and approved, "Controller enrollment failed: " + output[-2000:])
        # Prove the master never needs an inbound controller connection to this member.
        self.shell("k3s", "sudo", "iptables", "-I", "INPUT", "-p", "tcp", "--dport", "7464", "-j", "REJECT")
        self.wait_status("k3s", "admin")
        self.passed("CLI-only controller enrollment; remote access with member port 7464 blocked")

    def configure_apps(self):
        script = '''import json
from pathlib import Path
p=Path('/etc/hostlier/nginx-existing-tls.json')
refs=json.loads(p.read_text())
for app in ('api','hidden'):
 host=app+'.k3s.fleet.test'
 refs[host]=refs['k3s.fleet.test']
 with open('/etc/hosts','a') as f: f.write('127.0.0.1 '+host+'\\n')
p.write_text(json.dumps(refs))
'''
        self.shell("k3s", "sudo", "python3", "-c", script)
        for app, group in (("api", "research"), ("hidden", "private")):
            manifest = {"version": 2, "name": app, "swarm": group,
                        "source": {"container": {"archive": str(self.stage / "echo-image.tar"), "architecture": "arm64"}},
                        "endpoint": {"exposure": "public", "port": 8080, "route": app + ".k3s.fleet.test"}, "health": {"path": "/ready"},
                        "env": {"FLEET_TEST_RELEASE": "one"}}
            (self.stage / (app + ".hostlier")).write_text(json.dumps(manifest))

    def topology(self):
        result = json.loads(self.shell("k3s", "sudo", "k3s", "kubectl", "get", "deploy,svc,pod", "-A", "-l", "io.hostlier.app=api", "-o", "json"))
        require(any(item["kind"] == "Pod" and item["status"].get("phase") == "Running" for item in result["items"]), "No running application Pod")
        return sorted((item["kind"], item["metadata"]["namespace"], item["metadata"]["name"], item["metadata"]["uid"]) for item in result["items"])

    def http(self, app="api", version=None):
        if version is not None:
            result = self.shell("k3s", "curl", "--fail", "--silent", "--show-error", "--max-time", "45",
                                "https://" + app + ".k3s.fleet.test/version")
            require(result == version, "CLI succeeded before the intended revision was serving: " + result)
        body = "swarm-proof-" + secrets.token_hex(8)
        result = self.shell("k3s", "curl", "--fail", "--silent", "--show-error", "--max-time", "45", "--data-binary", body,
                            "https://" + app + ".k3s.fleet.test/proof")
        require(result == body, "Application HTTPS response mismatch")

    def prepare_workloads(self):
        self.configure_apps()
        self.command("admin", "swarm", "create", "research", mutate=True)
        self.command("admin", "swarm", "create", "private", mutate=True)
        original = self.command("admin", "swarm", "show", "research")
        ids = {name: self.login(name, True) for name in ("viewer", "operator", "manager")}
        self.grant(ids["viewer"], "swarms.view", "research")
        self.grant(ids["operator"], "apps.deploy,apps.stop", "research")
        self.grant(ids["manager"], "swarms.manage")
        self.command("operator", "deploy", "--file", str(self.stage / "api.hostlier"), mutate=True)
        self.http("api", "one")
        revision = self.command("admin", "app", "status", "api")["revision"]
        self.command("admin", "deploy", "--file", str(self.stage / "hidden.hostlier"), mutate=True)
        self.http("hidden", "one")
        replacement = json.loads((self.stage / "api.hostlier").read_text())
        replacement["env"]["FLEET_TEST_RELEASE"] = "two"
        replacement_path = self.stage / "api-next.hostlier"
        replacement_path.write_text(json.dumps(replacement))
        self.command("operator", "deploy", "--file", str(replacement_path), mutate=True)
        self.http("api", "two")
        self.command("operator", "rollback", "api", "--revision", revision, deny=True)
        self.command("admin", "rollback", "api", "--revision", revision, mutate=True)
        self.http("api", "one")
        self.report["immediateDeploymentProbes"] = 3
        self.report["immediateRollbackProbes"] = 1
        self.passed("initial deploys, replacement and rollback serve the intended revision immediately after CLI success; deploy-only login cannot roll back")
        return original, ids

    def exercise(self):
        original, ids = self.prepare_workloads()
        self.verify_workloads(original, ids)

    def verify_workloads(self, original, ids):
        self.http(); self.http("hidden")
        self.passed("restricted login deployed through master onto real remote K3s; both apps serve verified HTTPS")
        visible = self.command("viewer", "swarm", "list")["swarms"]
        require([group["name"] for group in visible] == ["research"], "Group listing leaked another group")
        apps = self.command("viewer", "app", "list", "--swarm", "research")["applications"]
        require([app["name"] for app in apps] == ["api"], "App listing leaked another group")
        self.command("viewer", "app", "status", "api")
        self.command("viewer", "app", "logs", "api", "--tail", "3")
        for args in (("app", "status", "hidden"), ("app", "logs", "hidden"), ("swarm", "members", "private"),
                     ("app", "stop", "api"), ("swarm", "create", "forbidden")):
            self.command("viewer", *args, deny=True)
        self.command("operator", "deploy", "--file", str(self.stage / "hidden.hostlier"), deny=True)
        self.command("manager", "app", "stop", "api", deny=True)
        self.command("operator", "app", "stop", "api", mutate=True)
        self.command("operator", "app", "start", "api", deny=True)
        self.command("admin", "app", "start", "api", mutate=True)
        self.http()
        # Every successful CLI return must be immediately usable, without retries or a grace period.
        for _ in range(3):
            self.command("admin", "app", "stop", "api", mutate=True)
            self.command("admin", "app", "start", "api", mutate=True)
            self.http()
            self.command("admin", "app", "restart", "api", mutate=True)
            self.http()
        self.report["immediateLifecycleProbes"] = 7
        self.passed("four starts and three restarts serve verified HTTPS immediately after CLI success, without retries")
        self.passed("view/deploy/stop/start/group-admin permissions enforced; cross-group status, logs and deployment denied")
        before = self.command("admin", "app", "status", "api")
        topology = self.topology()
        self.command("manager", "swarm", "move", "api", "--from", "research", "--to", "private", mutate=True)
        after = self.command("admin", "app", "status", "api")
        require(before["runtimeId"] == after["runtimeId"] and before["revision"] == after["revision"], "Move changed runtime or revision")
        require(self.topology() == topology, "Move recreated Kubernetes objects")
        for profile in ("viewer", "operator"):
            self.command(profile, "app", "status", "api", deny=True)
            self.command(profile, "app", "logs", "api", deny=True)
        self.command("operator", "app", "stop", "api", deny=True)
        self.command("operator", "operation", before["revision"], deny=True)
        self.command("operator", "operation", before["revision"], "--watch", deny=True)
        self.http()
        self.passed("move preserves Kubernetes UIDs/runtime/revision/HTTPS; old group loses reads, lifecycle and operation-watch access")
        self.command("manager", "swarm", "delete", "private", deny=True)
        self.command("manager", "swarm", "delete", "research", mutate=True)
        self.command("manager", "swarm", "create", "research", mutate=True)
        replacement = self.command("admin", "swarm", "show", "research")
        require(original["id"] != replacement["id"], "Recreated group reused identity")
        self.command("viewer", "swarm", "show", "research", deny=True)
        self.command("manager", "swarm", "move", "api", "--from", "private", "--to", "default", mutate=True)
        self.command("manager", "swarm", "delete", "default", deny=True)
        require(self.topology() == topology, "Move to default recreated Kubernetes objects")
        self.passed("occupied/default groups cannot be deleted; recreated names do not inherit old grants; move to default preserves workload")
        for role in ("k3s", "primary"):
            self.shell(role, "sudo", "systemctl", "restart", "hostlier")
            self.wait_status("k3s", "admin")
        current = self.command("admin", "app", "status", "api")
        require(current["swarmId"] == "default" and self.topology() == topology, "Restart changed membership or workload")
        self.command("viewer", "app", "status", "api", deny=True)
        self.command("operator", "app", "stop", "api", deny=True)
        self.http()
        grants = self.command("admin", "access", "authorizations", "list", remote=False)
        items = grants["authorizations"].values()
        grant = next(g for g in items if g["device"] == ids["manager"] and g["scope"] == "actions")
        self.command("admin", "access", "authorizations", "revoke", grant["id"], mutate=True, remote=False)
        self.command("manager", "swarm", "create", "revoked", deny=True)
        self.passed("master/member restarts retain grouping and permission boundaries; revoked manager cannot mutate groups")
        # Connect may attempt the browser by design; all operations after login must remain CLI-only.
        self.report.update({"checks": self.checks, "cliSHA256": hashlib.sha256((self.stage / "laptop").read_bytes()).hexdigest(), "controllerSHA256": hashlib.sha256((self.stage / "hostlier-controller").read_bytes()).hexdigest(), "k3s": self.shell("k3s", "sudo", "k3s", "--version"),
                            "architecture": self.shell("k3s", "uname", "-m"), "runtimeId": current["runtimeId"],
                            "kubernetesObjects": topology})


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--report", required=True, type=Path)
    parser.add_argument("--keep-on-failure", action="store_true")
    args = parser.parse_args()
    os.chdir(ROOT)
    os.umask(0o077)
    signal.signal(signal.SIGTERM, lambda *_: (_ for _ in ()).throw(KeyboardInterrupt()))
    stage = Path(tempfile.mkdtemp(prefix="hostlier-swarms-", dir="/tmp"))
    report = {"status": "running", "startedAt": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), "checks": []}
    fleet = SwarmGate(stage, report)
    success = False
    try:
        print("Building working tree and portable fixture image", flush=True)
        bundles = archives(stage)
        image_archive(stage)
        certificates(stage, fleet.names)
        alg = b"sk-ssh-ed25519@openssh.com"
        blob = b"".join(struct.pack(">I", len(v)) + v for v in (alg, secrets.token_bytes(32), b"ssh:hostlier"))
        (stage / "owner.pub").write_bytes(alg + b" " + base64.b64encode(blob) + b"\n")
        for role in fleet.names:
            print("Creating disposable " + role + " VM", flush=True)
            fleet.create(role)
        fleet.set_network()
        for role in fleet.names:
            print("Provisioning " + role + " (K3s only, no Docker)", flush=True)
            # The inherited idle fixture installs Docker on role=k3s; this gate does not.
            host = role + ".fleet.test"
            fleet.copy(role, stage / "ca.crt", stage / (host + ".crt"), stage / (host + ".key"), ROOT / "test/e2e/fleet/guest.sh")
            fleet.shell(role, "sudo", "bash", "/tmp/guest.sh", host)
            if role == "primary":
                fleet.master(role, bundles)
            else:
                fleet.install(role, bundles, "k3s")
            fleet.shell(role, "sh", "-c", "! command -v docker && test ! -S /var/run/docker.sock")
        fleet.login("admin")
        fleet.join()
        fleet.exercise()
        success = True
        report["status"] = "passed"
    except BaseException as exc:
        report.update(status="failed", error=str(exc), checks=fleet.checks)
        raise
    finally:
        if success or not args.keep_on_failure:
            print("Removing only this run's disposable VMs and credentials", flush=True)
            try:
                fleet.cleanup()
                shutil.rmtree(stage)
                report["cleanup"] = "complete"
            except BaseException as exc:
                report["cleanup"] = str(exc)
                report["status"] = "failed"
                raise
            finally:
                args.report.write_text(json.dumps(report, indent=2) + "\n")
        else:
            report["fixtureDirectory"] = str(stage)
            report["virtualMachines"] = fleet.names
            args.report.write_text(json.dumps(report, indent=2) + "\n")
            print("Failed fixture retained for debugging: " + str(stage), flush=True)
    print("PASS live swarm flow and cleanup; report: " + str(args.report), flush=True)


if __name__ == "__main__":
    main()
