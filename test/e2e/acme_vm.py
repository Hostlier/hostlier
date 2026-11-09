#!/usr/bin/env python3
"""Disposable VM fixture: real Pebble HTTP-01, Certbot issuance and renewal.
Run as root with Docker, nginx, certbot and the compiled nginx test executable.
"""
import json
import os
from pathlib import Path
import ssl
import subprocess
import urllib.request

ROOT = Path('/root/hostlier-acme-e2e')
IMAGE = 'ghcr.io/letsencrypt/pebble@sha256:d9080f68f6cb6af8d82134ab26de0aaaf312ac9cba42aecc6d3aede6cb63007b'


def run(*args, **kwargs):
    return subprocess.run(args, check=True, timeout=300, **kwargs)


def main():
    ROOT.mkdir(mode=0o700, exist_ok=True)
    run('openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '2',
        '-subj', '/CN=127.0.0.1', '-addext', 'subjectAltName=IP:127.0.0.1',
        '-keyout', str(ROOT / 'server.key'), '-out', str(ROOT / 'server.crt'),
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    config = {'pebble': {'listenAddress': '127.0.0.1:14000',
        'managementListenAddress': '127.0.0.1:15000', 'certificate': '/fixture/server.crt',
        'privateKey': '/fixture/server.key', 'httpPort': 5002, 'tlsPort': 5001,
        'ocspResponderURL': '', 'externalAccountBindingRequired': False}}
    (ROOT / 'config.json').write_text(json.dumps(config))
    run('docker', 'run', '-d', '--name', 'hostlier-acme-pebble', '--network', 'host',
        '--add-host', 'acme.hostlier.test:127.0.0.1', '-e', 'PEBBLE_VA_NOSLEEP=1',
        '-e', 'PEBBLE_WFE_NONCEREJECT=0', '-v', str(ROOT) + ':/fixture:ro',
        IMAGE, '-config', '/fixture/config.json', '-strict=false', stdout=subprocess.DEVNULL)
    try:
        import time
        context = ssl.create_default_context(cafile=str(ROOT / 'server.crt'))
        for attempt in range(50):
            try:
                with urllib.request.urlopen('https://127.0.0.1:15000/roots/0', context=context) as response:
                    (ROOT / 'issuer.crt').write_bytes(response.read())
                break
            except OSError:
                time.sleep(.2)
        env = dict(os.environ, HOSTLIER_ACME_E2E=str(ROOT))
        run('/tmp/hostlier-nginx.test', '-test.run', '^TestLiveACMEIssuanceAndRenewal$', '-test.v', env=env)
    finally:
        run('docker', 'rm', '-f', 'hostlier-acme-pebble', stdout=subprocess.DEVNULL)


if __name__ == '__main__':
    main()
