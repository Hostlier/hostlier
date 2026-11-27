#!/usr/bin/env python3
"""Exercise runtime selection only on the named disposable Ubuntu fixtures."""
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys

from migration_vm import check, cli, configure_test_edge, ready, request, run, setup


INSTALLATION = Path("/var/lib/hostlier/installation.json")
CONTROLLER = Path("/usr/local/bin/hostlier-controller")
DOMAIN = "apps.migration.test"


def preview(binary, *args):
    return json.loads(run(binary, "init", "--dry-run", "--json", *args))


def rejected(binary, runtime, expected):
    result = subprocess.run([binary, "init", "--domain", DOMAIN, "--dry-run", "--runtime", runtime],
                            capture_output=True, text=True, timeout=30)
    check(result.returncode != 0 and expected in result.stderr,
          f"Unexpected preview for {runtime}: {result.stdout} {result.stderr}")


def identity(service):
    return run("systemctl", "show", "--property=MainPID", "--value", service).strip()


def apply_preview(binary, plan, *args):
    check(plan["supported"], "Unexpected installation blockers: " + ", ".join(plan["errors"]))
    run(binary, "init", "--yes", "--approved-fingerprint", plan["fingerprint"], *args)
    check(hashlib.sha256(CONTROLLER.read_bytes()).digest() == hashlib.sha256(Path(binary).read_bytes()).digest(),
          "Reviewed controller binary was not installed")


def fresh(binary):
    configure_test_edge()
    for legacy in ("docker", "containerd"):
        rejected(binary, legacy, "New Hostlier installations require k3s")
    check(not INSTALLATION.exists(), "Rejected fresh install created a runtime record")
    plan = preview(binary, "--domain", DOMAIN)
    check(plan["runtime"] == "k3s", "Fresh init selected the wrong runtime")
    apply_preview(binary, plan, "--domain", DOMAIN)
    check(json.loads(INSTALLATION.read_text())["runtime"] == "k3s", "K3s was not recorded")
    check(run("k3s", "kubectl", "get", "--raw", "/readyz").strip() == b"ok", "K3s is not ready")
    check(shutil.which("docker") is None and not Path("/var/run/docker.sock").exists(), "Docker was installed")
    print("PASS fresh install selected K3s, rejected both legacy flags, and reached readiness without Docker", flush=True)


def docker_workloads():
    return sorted(run("docker", "ps", "--filter", "label=io.hostlier.app=margin", "--format", "{{.ID}}").splitlines())


def upgrade(binary):
    ready()
    install = json.loads(INSTALLATION.read_text())
    check(install["runtime"] == "docker", "Fixture must begin on recorded Docker")
    check(hashlib.sha256(CONTROLLER.read_bytes()).digest() != hashlib.sha256(Path(binary).read_bytes()).digest(),
          "Fixture needs a different candidate binary")
    before = json.loads(cli("status", "margin"))["runtimeId"]
    daemon, containers = identity("docker"), docker_workloads()
    check(daemon != b"0" and len(containers) >= 2, "Running Docker app group was not observed")
    paths = ("/data", "/storage")
    values = tuple(request(path) for path in paths)
    rejected(binary, "k3s", "Existing Hostlier installation uses docker")
    check(preview(binary)["runtime"] == "docker", "Omitted runtime did not retain Docker")
    # Verified controller updates supply the recorded runtime as an explicit init argument.
    args = ("--runtime", "docker")
    apply_preview(binary, preview(binary, *args), *args)
    ready()
    check(json.loads(INSTALLATION.read_text())["runtime"] == "docker", "Upgrade changed the recorded runtime")
    check(identity("docker") == daemon and docker_workloads() == containers, "Upgrade replaced the daemon or app containers")
    check(json.loads(cli("status", "margin"))["runtimeId"] == before, "Upgrade changed the app identity")
    check(tuple(request(path) for path in paths) == values, "Upgrade lost primary or sidecar data")
    print("PASS reviewed Docker update preserved the daemon, running workload IDs, HTTPS, and both persistent volumes", flush=True)


def main():
    mode = sys.argv[1]
    expected = "hostlier-k3s-only-test" if mode == "fresh" else "hostlier-docker-test"
    check(os.geteuid() == 0 and os.uname().nodename in (expected, "lima-" + expected),
          "Refusing to run outside the named disposable runtime transition fixture")
    if mode == "legacy-setup":
        setup()
    elif mode == "fresh":
        fresh(sys.argv[2])
    elif mode == "upgrade":
        upgrade(sys.argv[2])
    else:
        raise RuntimeError("Expected fresh, legacy-setup, or upgrade")


if __name__ == "__main__":
    main()
