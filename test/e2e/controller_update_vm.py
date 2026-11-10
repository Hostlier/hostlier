#!/usr/bin/env python3
"""Exercise the real detached maintenance runner in a fresh disposable Ubuntu VM.

Requires /tmp/hostlier-controller from the current Linux server build. This is a
systemd/receipt fault test with a harmless target installer, not a package or
production-upgrade rehearsal. Never run on a server with hostlier.service.
"""
import hashlib
import json
import os
from pathlib import Path
import shutil
import sqlite3
import subprocess
import time
import uuid

ROOT = Path("/var/lib/hostlier-maintenance-smoke")
UNIT = Path("/etc/systemd/system/hostlier.service")


def run(*args):
    return subprocess.check_output(args, stderr=subprocess.STDOUT, timeout=60).decode().strip()


def check(condition, message):
    if not condition:
        raise RuntimeError(message)


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def stage(failure):
    identity = uuid.uuid4().hex
    directory = ROOT / "controller-updates" / identity
    directory.mkdir(parents=True, mode=0o700)
    shutil.copy("/tmp/hostlier-controller", directory / "runner")
    (directory / "runner").chmod(0o700)
    target = directory / "hostlier-controller"
    target.write_text("""#!/bin/sh
set -eu
test "$1" = init
test "$2" = --approved-fingerprint
test "$3" = fixture-reviewed
test "$4" = --yes
test "$5" = --json
sleep 2
printf 'verified fingerprint reached target\n'
exit """ + ("1\n" if failure else "0\n"))
    target.chmod(0o700)
    record = {
        "plan": {"id": identity, "version": "fixture"},
        "actor": "fixture-owner", "generation": 7,
        "installerFingerprint": "fixture-reviewed",
        "targetSha256": digest(target), "sourceSha256": digest(directory / "runner"),
        "requestId": uuid.uuid4().hex,
        "job": {"id": identity, "version": "fixture", "status": "queued"},
        "queuedAt": int(time.time() * 1000), "initArgs": ["init"],
    }
    (directory / "record.json").write_text(json.dumps(record))
    (directory / "record.json").chmod(0o600)
    with sqlite3.connect(ROOT / "state.db") as db:
        db.execute("CREATE TABLE IF NOT EXISTS controller_snapshot (id INTEGER PRIMARY KEY, payload BLOB, schema_version INTEGER)")
        db.execute("INSERT OR REPLACE INTO controller_snapshot VALUES (1, ?, 1)",
                   (json.dumps({"generation": 7, "controllerUpdateId": identity}).encode(),))
    return identity, directory


def exercise(failure):
    identity, directory = stage(failure)
    previous_pid = run("systemctl", "show", "--property=MainPID", "--value", "hostlier.service")
    run("systemd-run", "--unit=hostlier-update-" + identity, "--on-active=5s",
        "--timer-property=AccuracySec=1s", "--collect", "--property=Type=oneshot",
        "--property=TimeoutStartSec=35min", str(directory / "runner"),
        "maintenance-run", "--state-dir", str(ROOT), "--id", identity)
    deadline = time.monotonic() + 90
    while time.monotonic() < deadline:
        record = json.loads((directory / "record.json").read_text())
        if record["job"]["status"] in ("failed", "succeeded"):
            break
        time.sleep(.2)
    expected = "failed" if failure else "succeeded"
    check(record["job"]["status"] == expected, "Incorrect terminal receipt: " + json.dumps(record["job"]))
    check(run("systemctl", "is-active", "hostlier.service") == "active", "Controller API service not recovered")
    current_pid = run("systemctl", "show", "--property=MainPID", "--value", "hostlier.service")
    check(current_pid != "0" and current_pid != previous_pid, "Old API process was not stopped and restarted")
    check("verified fingerprint reached target" in (directory / "installer.log").read_text(), "Target did not verify approval")
    replay = subprocess.run([str(directory / "runner"), "maintenance-run", "--state-dir", str(ROOT), "--id", identity], capture_output=True)
    check(replay.returncode != 0, "Terminal job replayed")
    print(json.dumps({"case": expected, "job": record["job"], "serviceRestarted": True, "terminalReplayDenied": True}))


def main():
    check(os.geteuid() == 0, "Run only as root inside a disposable VM")
    check(not UNIT.exists() and not Path("/var/lib/hostlier").exists(), "Refusing an existing Hostlier installation")
    check(not ROOT.exists(), "Fresh disposable state is required")
    ROOT.mkdir(mode=0o700)
    UNIT.write_text("[Unit]\nDescription=Disposable maintenance API placeholder\n[Service]\nType=simple\nExecStart=/usr/bin/sleep infinity\nKillMode=control-group\n")
    run("systemctl", "daemon-reload")
    run("systemctl", "start", "hostlier.service")
    try:
        exercise(False)
        exercise(True)
    finally:
        run("systemctl", "stop", "hostlier.service")
        UNIT.unlink()
        run("systemctl", "daemon-reload")


if __name__ == "__main__":
    main()
