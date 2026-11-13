#!/usr/bin/env python3
"""Exercise managed SSH in a disposable Ubuntu container, without host mounts or ports."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import uuid

ROOT = Path(__file__).resolve().parents[3]


def main():
    architecture = subprocess.check_output(['docker', 'info', '--format', '{{.Architecture}}'], text=True).strip()
    arch = {'aarch64': 'arm64', 'arm64': 'arm64', 'x86_64': 'amd64', 'amd64': 'amd64'}.get(architecture)
    if not arch:
        raise SystemExit('Docker must run Linux AMD64 or ARM64')
    image = 'hostlier-ssh-test:' + uuid.uuid4().hex[:12]
    with tempfile.TemporaryDirectory(prefix='hostlier-ssh-test-') as folder:
        stage = Path(folder)
        shutil.copy2(Path(__file__).with_name('Dockerfile'), stage / 'Dockerfile')
        env = {**os.environ, 'CGO_ENABLED': '0', 'GOOS': 'linux', 'GOARCH': arch}
        subprocess.run(['go', 'test', '-c', '-o', str(stage / 'sshaccess.test'), './internal/sshaccess'], cwd=ROOT, env=env, check=True)
        try:
            subprocess.run(['docker', 'build', '-t', image, str(stage)], check=True)
            for security in ([], ['--security-opt', 'no-new-privileges:true']):
                subprocess.run(['docker', 'run', '--rm', '--network', 'none', '--memory', '512m', '--pids-limit', '128', *security, image], check=True)
        finally:
            subprocess.run(['docker', 'image', 'rm', image], check=False, stdout=subprocess.DEVNULL)


if __name__ == '__main__':
    main()
