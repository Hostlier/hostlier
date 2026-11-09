#!/usr/bin/env python3
"""Live coexistence rehearsal. Run ONLY in the named disposable Ubuntu Lima VM.

setup creates two manually managed Docker/Nginx HTTPS sites before Hostlier exists.
exercise installs the real release, deploys through the real CLI/controller, and
monitors the legacy sites. Approval uses a test-only cryptographic authenticator;
production approval code and TLS validation remain enabled.
"""
import hashlib
import http.client
import json
import os
from pathlib import Path
import pty
import re
import select
import shutil
import ssl
import subprocess
import sys
import threading
import time

ROOT = Path('/root/hostlier-coexistence-e2e')
TLS = Path('/etc/hostlier-coexistence-fixture')
DOMAIN = 'coexist.example.test'
LEGACY = ['legacy-one.' + DOMAIN, 'legacy-two.' + DOMAIN]
CONTROLLER = 'hostlier.' + DOMAIN
NEW = 'new.' + DOMAIN
CLI = '/usr/local/bin/hostlier'
SOCKET = '/run/hostlier/controller.sock'
MARKER = 'hostlier-coexistence-disposable-v1\n'


def run(*args, timeout=600, check=True, **kwargs):
    result = subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                            timeout=timeout, **kwargs)
    if check and result.returncode:
        raise RuntimeError(f'{args[:3]} failed: {result.stderr.decode()} {result.stdout.decode()}')
    return result


def require_vm():
    if os.geteuid() != 0 or Path('/etc/hostname').read_text().strip() != 'lima-hostlier-coexistence-test':
        raise RuntimeError('This fixture may run only as root in lima-hostlier-coexistence-test')


def issue(host, generation):
    directory = TLS / host
    directory.mkdir(parents=True, exist_ok=True)
    key = directory / f'key-{generation}.pem'
    cert = directory / f'cert-{generation}.pem'
    csr = directory / f'request-{generation}.pem'
    extensions = directory / 'extensions.cnf'
    extensions.write_text(f'subjectAltName=DNS:{host}\nextendedKeyUsage=serverAuth\n')
    run('openssl', 'req', '-new', '-newkey', 'rsa:2048', '-nodes', '-subj', f'/CN={host}',
        '-keyout', str(key), '-out', str(csr))
    os.chmod(key, 0o600)
    run('openssl', 'x509', '-req', '-in', str(csr), '-CA', str(TLS/'ca.crt'),
        '-CAkey', str(TLS/'ca.key'), '-CAcreateserial', '-days', '3',
        '-extfile', str(extensions), '-out', str(cert))
    for name, target in [('fullchain.pem', cert.name), ('privkey.pem', key.name)]:
        temporary = directory / (name + '.new')
        temporary.symlink_to(target)
        temporary.replace(directory / name)


def setup():
    if Path('/var/lib/hostlier/installation.json').exists() or ROOT.exists():
        raise RuntimeError('Refusing to prepare an already initialized fixture')
    ROOT.mkdir(mode=0o700)
    (ROOT/'fixture').write_text(MARKER)
    TLS.mkdir(mode=0o755)
    run('openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '3',
        '-subj', '/CN=Hostlier coexistence test CA', '-keyout', str(TLS/'ca.key'),
        '-out', str(TLS/'ca.crt'))
    os.chmod(TLS/'ca.key', 0o600)
    shutil.copy(TLS/'ca.crt', '/usr/local/share/ca-certificates/hostlier-coexistence-fixture.crt')
    run('update-ca-certificates')
    configure_fixture_trust()
    for host in LEGACY + [CONTROLLER, NEW]:
        issue(host, 1)
    with open('/etc/hosts', 'a') as hosts:
        hosts.write('\n127.0.0.1 ' + ' '.join(LEGACY + [CONTROLLER, NEW]) + '\n')
    configure_legacy_sites()
    configure_legacy_renewal()
    # Explicit references keep this rehearsal independent of public ACME/DNS.
    Path('/etc/hostlier').mkdir(exist_ok=True)
    references = {host: {'certificate': str(TLS/host/'fullchain.pem'),
                         'key': str(TLS/host/'privkey.pem')} for host in [CONTROLLER, NEW]}
    Path('/etc/hostlier/nginx-existing-tls.json').write_text(json.dumps(references))
    (ROOT/'new.hostlier').write_text(f'''version: 1
name: coexist-new
image: nginx:1.28-alpine
port: 80
route: {NEW}
resources:
  memory: 128Mi
''')
    (ROOT/'conflict.hostlier').write_text(f'''version: 1
name: coexist-conflict
image: nginx:1.28-alpine
port: 80
route: {LEGACY[0]}/takeover
''')
    for index, host in enumerate(LEGACY, 1):
        assert probe(host)['body'] == f'legacy-{index}-unchanged\n'
    (ROOT/'baseline.json').write_text(json.dumps(snapshot(), indent=2))
    print(json.dumps({'prepared': 'two manual Docker apps behind existing Nginx', 'tls': 'trusted private CA'}))


def configure_legacy_sites():
    # These are operator fixture changes, all completed before the preserved baseline is recorded.
    Path('/etc/nginx/sites-enabled/default').unlink(missing_ok=True)
    directory = Path('/srv/hostlier-coexistence-legacy')
    for index, host in enumerate(LEGACY, 1):
        content = directory / str(index)
        content.mkdir(parents=True)
        (content/'index.html').write_text(f'legacy-{index}-unchanged\n')
        (content/'persistent.txt').write_text(f'legacy-{index}-persistent-data\n')
        run('docker', 'run', '-d', '--name', f'coexist-legacy-{index}', '--restart', 'unless-stopped',
            '-p', f'127.0.0.1:{18080+index}:80', '-v', f'{content}:/usr/share/nginx/html:ro',
            'nginx:1.28-alpine')
        default = ' default_server' if index == 1 else ''
        Path(f'/etc/nginx/conf.d/legacy-{index}.conf').write_text(f'''
server {{
  listen 80{default};
  server_name {host};
  return 308 https://$host$request_uri;
}}
server {{
  listen 443 ssl{default};
  server_name {host};
  ssl_certificate {TLS/host/'fullchain.pem'};
  ssl_certificate_key {TLS/host/'privkey.pem'};
  location / {{ proxy_pass http://127.0.0.1:{18080+index}; }}
}}
''')
    run('nginx', '-t')
    run('systemctl', 'reload', 'nginx')
    time.sleep(1)


def configure_fixture_trust():
    # Private-CA trust is fixture setup, completed before any preserved baseline.
    registry = Path('/etc/docker/certs.d') / CONTROLLER
    registry.mkdir(parents=True, exist_ok=True)
    shutil.copy(TLS/'ca.crt', registry/'ca.crt')
    override = Path('/etc/systemd/system/hostlier.service.d')
    override.mkdir(parents=True, exist_ok=True)
    (override/'coexistence-test-ca.conf').write_text(
        f'[Service]\nEnvironment=HOSTLIER_CA_FILE={TLS/"ca.crt"}\n')


def configure_legacy_renewal():
    installed_script = Path('/usr/local/lib/hostlier-coexistence-fixture.py')
    shutil.copy(__file__, installed_script)
    Path('/etc/systemd/system/coexist-legacy-renew.service').write_text(f'''[Unit]
Description=Operator-owned certificate renewal fixture
[Service]
Type=oneshot
ExecStart=/usr/bin/python3 {installed_script} renew
''')
    Path('/etc/systemd/system/coexist-legacy-renew.timer').write_text('''[Unit]
Description=Operator-owned certificate renewal schedule
[Timer]
OnCalendar=weekly
[Install]
WantedBy=timers.target
''')
    run('systemctl', 'daemon-reload')
    run('systemctl', 'enable', '--now', 'coexist-legacy-renew.timer')


def probe(host):
    connection = http.client.HTTPSConnection(host, context=ssl.create_default_context(), timeout=5)
    try:
        connection.connect()
        fingerprint = hashlib.sha256(connection.sock.getpeercert(binary_form=True)).hexdigest()
        connection.request('GET', '/', headers={'Connection': 'close'})
        response = connection.getresponse()
        body = response.read().decode()
        if response.status != 200:
            raise RuntimeError(f'{host}: HTTP {response.status}')
        return {'body': body, 'certificateSha256': fingerprint}
    finally:
        connection.close()


def digest(path):
    path = Path(path)
    return {'sha256': hashlib.sha256(path.read_bytes()).hexdigest(),
            'symlink': os.readlink(path) if path.is_symlink() else None,
            'mode': oct(path.stat().st_mode & 0o777)}


def snapshot():
    configs = list(Path('/etc/nginx').rglob('*'))
    configs += list(Path('/etc/docker').rglob('*'))
    configs += [Path('/etc/systemd/system/coexist-legacy-renew.service'),
                Path('/etc/systemd/system/coexist-legacy-renew.timer'),
                Path('/usr/local/lib/hostlier-coexistence-fixture.py')]
    configs += list(Path('/srv/hostlier-coexistence-legacy').rglob('*'))
    files = {str(p): digest(p) for p in configs if p.is_file() and p.name != 'zz-hostlier.conf'}
    certificates = {str(TLS/host/name): digest(TLS/host/name)
                    for host in LEGACY for name in ['fullchain.pem', 'privkey.pem']}
    containers = []
    for index in [1, 2]:
        obj = json.loads(run('docker', 'inspect', f'coexist-legacy-{index}').stdout)[0]
        containers.append({key: obj[key] for key in ['Id', 'RestartCount', 'Config', 'HostConfig', 'Mounts']})
        containers[-1]['startedAt'] = obj['State']['StartedAt']
        assert obj['State']['Running']
    services = {}
    for service in ['nginx', 'docker']:
        services[service] = run('systemctl', 'show', service, '--property=MainPID',
                                '--property=ExecMainStartTimestampMonotonic', '--property=UnitFileState').stdout.decode()
        files['unit:' + service] = hashlib.sha256(run('systemctl', 'cat', service).stdout).hexdigest()
    services['renewalTimer'] = run('systemctl', 'is-active', 'coexist-legacy-renew.timer').stdout.decode()
    return {'files': files, 'certificates': certificates, 'containers': containers, 'services': services,
            'https': {host: probe(host) for host in LEGACY}}


def assert_preserved(baseline, phase, certificates=True):
    current = snapshot()
    for field in ['files', 'containers', 'services']:
        assert current[field] == baseline[field], f'{phase}: legacy {field} changed'
    if certificates:
        assert current['certificates'] == baseline['certificates'], f'{phase}: legacy certificate files changed'
        assert current['https'] == baseline['https'], f'{phase}: legacy TLS/body changed'
    (ROOT/f'{phase}.json').write_text(json.dumps(current, indent=2))
    return current


def signer(code=''):
    env = dict(os.environ, HOSTLIER_COEXISTENCE_VM='1', HOSTLIER_COEXISTENCE_APPROVAL=code)
    run('/tmp/hostlier-coexistence-cli.test', '-test.run', '^TestCoexistenceVMApproval$',
        '-test.v', '-test.timeout', '30s', env=env, timeout=40)


def approved_cli(label, *args, expect_success=True):
    master, slave = pty.openpty()
    process = subprocess.Popen([CLI, *args, '--socket', SOCKET], stdin=slave, stdout=slave, stderr=slave,
                               env=dict(os.environ, HOSTLIER_HOME=str(ROOT/'client')))
    os.close(slave)
    output = b''
    confirmed = 0
    signed = set()
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
                prompts = output.count(b'[y/N]')
                if prompts > confirmed:
                    os.write(master, b'y\n')
                    confirmed = prompts
                for code in re.findall(rb'Human approval request: ([A-Z0-9]+)', output):
                    if code not in signed:
                        signer(code.decode())
                        signed.add(code)
            elif process.poll() is not None:
                break
    finally:
        if process.poll() is None:
            process.kill()
        code = process.wait()
        os.close(master)
        (ROOT/f'{label}-cli.log').write_bytes(output)
    assert bool(signed), f'{label}: no independent signature was verified'
    assert (code == 0) == expect_success, f'{label}: exit {code}: {output.decode()}'
    return output


def exercise():
    assert (ROOT/'fixture').read_text() == MARKER
    baseline = json.loads((ROOT/'baseline.json').read_text())
    observations = {'samples': 0, 'failures': []}
    stopping = threading.Event()

    def monitor():
        while not stopping.is_set():
            for index, host in enumerate(LEGACY, 1):
                try:
                    assert probe(host)['body'] == f'legacy-{index}-unchanged\n'
                except Exception as error:
                    observations['failures'].append({'time': time.time(), 'host': host, 'error': str(error)})
                observations['samples'] += 1
            stopping.wait(.1)

    thread = threading.Thread(target=monitor)
    thread.start()
    started = time.monotonic()
    phases = []
    try:
        preview = json.loads(run('/tmp/hostlier', 'init', '--domain', DOMAIN, '--dry-run').stdout)
        (ROOT/'init-plan.json').write_text(json.dumps(preview, indent=2))
        assert preview['supported'], preview['errors']
        assert (preview['runtime'], preview['proxy'], preview['dockerMode']) == ('docker', 'nginx-existing', 'existing')
        assert_preserved(baseline, 'after-preview')
        run('install', '-m755', '/tmp/hostlier', CLI)
        result = run('/tmp/hostlier', 'init', '--domain', DOMAIN, '--yes', timeout=420)
        (ROOT/'init.log').write_bytes(result.stdout + result.stderr)
        assert_preserved(baseline, 'after-init')
        phases.append('automatic detection and installed controller/collector ready')
        signer()
        approved_cli('deploy', 'deploy', '--file', str(ROOT/'new.hostlier'))
        assert 'Welcome to nginx!' in probe(NEW)['body']
        assert_preserved(baseline, 'after-deploy')
        phases.append('real CLI deployment serves verified HTTPS')
        approved_cli('restart', 'restart', 'coexist-new')
        assert 'Welcome to nginx!' in probe(NEW)['body']
        assert_preserved(baseline, 'after-restart')
        approved_cli('conflict', 'deploy', '--file', str(ROOT/'conflict.hostlier'), expect_success=False)
        assert_preserved(baseline, 'after-conflict')
        assert not run('docker', 'ps', '-aq', '--filter', 'label=io.hostlier.app=coexist-conflict').stdout.strip()
        phases.append('conflicting legacy hostname rejected without a candidate container')
        run(CLI, 'init', '--domain', DOMAIN, '--yes', timeout=420)
        assert 'Welcome to nginx!' in probe(NEW)['body']
        assert_preserved(baseline, 'after-reinit')
        phases.append('repeat init preserves both legacy and Hostlier routes')
        run('systemctl', 'start', 'coexist-legacy-renew.service')
        time.sleep(1)
        renewed = assert_preserved(baseline, 'after-renewal', certificates=False)
        for host in LEGACY:
            assert renewed['https'][host]['certificateSha256'] != baseline['https'][host]['certificateSha256']
        assert 'Welcome to nginx!' in probe(NEW)['body']
        phases.append('operator-owned renewal rotates legacy certificates and leaves new app working')
        approved_cli('delete', 'delete', 'coexist-new')
        assert_preserved(renewed, 'after-delete')
        phases.append('deleting Hostlier app leaves legacy sites unchanged')
        assert not observations['failures'], observations['failures'][:5]
    finally:
        stopping.set()
        thread.join(timeout=10)
        report = {'passed': len(phases) == 6 and not observations['failures'], 'phases': phases,
                  'elapsedSeconds': round(time.monotonic()-started, 2), **observations,
                  'os': run('lsb_release', '-ds').stdout.decode().strip(),
                  'architecture': run('uname', '-m').stdout.decode().strip(),
                  'docker': run('docker', 'version', '--format', '{{.Server.Version}}').stdout.decode().strip(),
                  'nginx': run('nginx', '-v').stderr.decode().strip(),
                  'certificateAuthority': 'trusted private test CA; no public ACME',
                  'approval': 'test-only software authenticator through installed production verification API'}
        (ROOT/'result.json').write_text(json.dumps(report, indent=2))
        print(json.dumps(report, indent=2))


def renew():
    assert (ROOT/'fixture').read_text() == MARKER
    generation = time.time_ns()
    for host in LEGACY:
        issue(host, generation)
    run('nginx', '-t')
    run('systemctl', 'reload', 'nginx')


if __name__ == '__main__':
    require_vm()
    {'setup': setup, 'exercise': exercise, 'renew': renew}[sys.argv[1]]()
