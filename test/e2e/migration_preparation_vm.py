#!/usr/bin/env python3
"""Exercise only the named disposable Ubuntu 26.04 VM after migration_vm.py.

The external cluster in this guest is the test's own cluster. Production
preparation never performs the stopping, cgroup killing or unmounting below.
"""
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import time


def run(*args, ok=True):
    result = subprocess.run(args, capture_output=True, timeout=600)
    if ok and result.returncode:
        raise RuntimeError(f"{args[:3]}: {result.stderr.decode()[-2000:]} {result.stdout.decode()[-2000:]}")
    return result


def check(value, message):
    if not value:
        raise RuntimeError(message)


def command(*args, ok=True):
    return run("/tmp/hostlier-controller", "migrate", "prepare", *args, ok=ok)


def cold_test_cluster():
    # This fixture owns this entire disposable cluster; the product refuses live clusters.
    run("systemctl", "disable", "--now", "k3s.service")
    run("systemctl", "kill", "--kill-whom=all", "--signal=SIGKILL", "k3s.service", ok=False)
    run("systemctl", "reset-failed", "k3s.service")
    roots = ("/var/lib/kubelet", "/var/lib/rancher/k3s", "/var/lib/cni", "/etc/cni/net.d")
    mounts = [line.split()[4] for line in Path("/proc/self/mountinfo").read_text().splitlines()]
    for path in sorted(set(mounts), key=len, reverse=True):
        if any(path == root or path.startswith(root + "/") for root in roots):
            run("umount", "--", path)


def exercise():
    check(os.geteuid() == 0 and os.uname().nodename == "lima-hostlier-ubuntu2604",
          "Refusing prerequisite mutation outside the explicitly named disposable VM")
    check('VERSION_ID="26.04"' in Path("/etc/os-release").read_text(), "Wrong guest release")
    cold_test_cluster()
    state = Path("/var/lib/hostlier")
    check(json.loads((state / "installation.json").read_text())["runtime"] == "docker",
          "Fixture requires the completed Docker rollback")
    swap = Path("/hostlier-preparation-fixture.swap")
    run("fallocate", "-l", "128M", str(swap)); swap.chmod(0o600)
    run("mkswap", str(swap)); run("swapon", "--priority", "13", str(swap))
    original_fstab = Path("/etc/fstab").read_bytes()
    fixture_fstab = original_fstab + b"\n/hostlier-preparation-fixture.swap none swap sw,pri=13 0 0\n"
    Path("/etc/fstab").write_bytes(fixture_fstab)
    original_stat = Path("/etc/fstab").stat()
    run("systemctl", "daemon-reload")
    backup = "/var/backups/hostlier-preparation-fixture-" + str(time.time_ns())
    flags = ("--state-dir", str(state), "--backup-dir", backup, "--disable-swap", "--quarantine-k3s")
    preview = json.loads(command("plan", *flags).stdout)
    check(not preview["errors"], repr(preview["errors"]))
    check(not Path(backup).exists() and swap.exists(), "Preview changed host state")
    check(command("apply", *flags, "--yes", ok=False).returncode != 0, "Missing fingerprint accepted")
    command("apply", *flags, "--fingerprint", preview["fingerprint"], "--yes")
    journal_path = Path(backup) / "preparation.json"
    journal = json.loads(journal_path.read_text())
    check(journal["phase"] == "completed", "Preparation did not complete")
    check(len(Path("/proc/swaps").read_text().splitlines()) == 1, "Swap remains active")
    check(swap.exists(), "Swap file was deleted")
    prepared_stat = Path("/etc/fstab").stat()
    check((prepared_stat.st_uid, prepared_stat.st_gid, prepared_stat.st_mode) ==
          (original_stat.st_uid, original_stat.st_gid, original_stat.st_mode), "fstab metadata changed")
    for asset in journal["assets"]:
        check(not Path(asset["path"]).exists() and Path(asset["quarantine"]).exists(), "Missing retained original")
        check(hashlib.sha256(Path(asset["archive"]).read_bytes()).hexdigest() == asset["archiveSha256"], "Backup corruption")
    migration = json.loads(run("/tmp/hostlier-controller", "migrate", "plan", "--retry",
                               "--backup-dir", "/var/backups/hostlier-after-preparation", "--json").stdout)
    check(not migration["errors"], "Prepared migration preview remains blocked: " + repr(migration["errors"]))
    check(run("curl", "--fail", "--silent", "https://margin.apps.migration.test/data").stdout == b"k3s-primary-new",
          "Preparation disturbed Docker or lost its data")

    # A corrupt independent backup must block before the first restore.
    first = journal["assets"][0]
    archive = Path(first["archive"]); saved = archive.read_bytes()
    archive.write_bytes(bytes([saved[0] ^ 1]) + saved[1:])
    check(command("rollback", "--backup-dir", backup, "--yes", ok=False).returncode != 0,
          "Corrupt backup accepted")
    check(not Path(first["path"]).exists(), "Failed rollback changed source")
    archive.write_bytes(saved)

    # A new cluster installation must never be overwritten by the old quarantine.
    replacement = Path("/etc/rancher/k3s")
    replacement.mkdir(); (replacement / "new-config").write_text("replacement")
    check(command("rollback", "--backup-dir", backup, "--yes", ok=False).returncode != 0,
          "Replacement cluster overwritten")
    check((replacement / "new-config").read_text() == "replacement", "Replacement data changed")
    shutil.rmtree(replacement)

    # Simulate an interrupted rollback with one completed rename and its retained marker.
    os.rename(first["quarantine"], first["path"])
    journal["phase"] = "rolling-back"
    journal_path.write_text(json.dumps(journal))
    (state / "migration-preparation-active").write_text(backup)
    command("rollback", "--backup-dir", backup, "--yes")
    check(json.loads(journal_path.read_text())["phase"] == "rolled-back", "Rollback did not complete")
    check(Path("/etc/fstab").read_bytes() == fixture_fstab, "fstab bytes were not restored exactly")
    check(str(swap) in Path("/proc/swaps").read_text(), "Original swap not reactivated")
    check(not (state / "migration-preparation-active").exists(), "Recovery marker retained after success")
    check(run("systemctl", "show", "k3s.service", "--property=ActiveState", "--value").stdout.strip() == b"inactive",
          "Preparation rollback started the old cluster")
    command("rollback", "--backup-dir", backup, "--yes")  # Repeated recovery is a no-op.
    run("swapoff", "--", str(swap)); swap.unlink()
    Path("/etc/fstab").write_bytes(original_fstab); run("systemctl", "daemon-reload")
    print("PASS Ubuntu 26.04 prerequisite preview/apply/rollback: persistent swap policy, cold verified archives, quarantine, corrupt-backup/replacement refusal, interrupted recovery, Docker data intact", flush=True)


if __name__ == "__main__":
    exercise()
