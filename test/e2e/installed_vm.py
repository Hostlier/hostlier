#!/usr/bin/env python3
"""Run INSIDE a disposable Ubuntu VM after hostlier init. Mutates test CA/hosts.

sudo python3 installed_vm.py setup|deploy|dependencies|domains|verify [docker|k3s]
Use verify again after reboot. Never run against a production server.
"""
import json
import os
from pathlib import Path
import pty
import select
import shutil
import subprocess
import sys
import time
import urllib.request

ROOT = Path('/root/hostlier-vm-e2e')
ROOT.mkdir(mode=0o700, exist_ok=True)
os.environ['HOSTLIER_HOME'] = str(ROOT / 'client')
os.chdir(ROOT)
HOSTS = ['hostlier.apps.hostlier.test', 'fixture.apps.hostlier.test']
BASE = 'https://' + HOSTS[0]
RUNTIME = sys.argv[2] if len(sys.argv) > 2 else 'docker'


def run(*args, input=None, check=True):
    result = subprocess.run(args, input=input, stdout=subprocess.PIPE,
                            stderr=subprocess.PIPE, timeout=600)
    if check and result.returncode:
        raise RuntimeError(f'{args[0:3]} failed: {result.stderr.decode()} {result.stdout.decode()}')
    return result


def cli(*args, **kwargs):
    return run('/usr/local/bin/hostlier', *args, **kwargs)


def http(url, data=None):
    request = urllib.request.Request(url, data=data,
        headers={'Content-Type': 'application/json'} if data else {})
    with urllib.request.urlopen(request, timeout=15) as response:
        return response.read()


def approved(*args):
    master, slave = pty.openpty()
    process = subprocess.Popen(['/usr/local/bin/hostlier', *args],
        stdin=slave, stdout=slave, stderr=slave)
    os.close(slave)
    output = b''
    sent = False
    deadline = time.time() + 600
    while time.time() < deadline:
        if select.select([master], [], [], .1)[0]:
            try:
                chunk = os.read(master, 65536)
            except OSError:
                break
            if not chunk:
                break
            output += chunk
            if b'[y/N]' in output and not sent:
                os.write(master, b'y\n')
                sent = True
        if process.poll() is not None:
            break
    if process.poll() is None:
        process.kill()
    code = process.wait()
    os.close(master)
    if code or not sent:
        raise RuntimeError('Approved CLI failed: ' + output.decode())
    return output


def redis_query(write=False):
    # Expansion happens inside the workload, never in host argv or output.
    request = 'SET fixture persisted\\r\\n' if write else ''
    script = '''set -eu
u=${REDIS_URL#redis://}; auth=${u%@*}; address=${u#*@}; address=${address%/*}
user=${auth%%:*}; password=${auth#*:}; host=${address%:*}; port=${address##*:}
printf 'AUTH %s %s\\r\\n''' + request + '''GET fixture\\r\\nQUIT\\r\\n' "$user" "$password" | nc -w 10 "$host" "$port"
'''
    if RUNTIME == 'docker':
        container = run('docker', 'ps', '-q', '--filter', 'label=io.hostlier.app=fixture').stdout.decode().strip()
        result = run('docker', 'exec', container, 'sh', '-c', script)
    else:
        objects = json.loads(run('/usr/local/bin/k3s', 'kubectl', 'get', 'deploy', '-A', '-l',
            'io.hostlier.app=fixture', '-o', 'json').stdout)['items']
        obj = next(obj for obj in objects if obj['spec']['replicas'] == 1)
        result = run('/usr/local/bin/k3s', 'kubectl', 'exec', '-n', obj['metadata']['namespace'],
            'deployment/' + obj['metadata']['name'], '--', 'sh', '-c', script)
    assert b'persisted' in result.stdout and b'WRONGPASS' not in result.stdout, result.stdout


def verify():
    for service in ['hostlier', 'caddy', 'docker' if RUNTIME == 'docker' else 'k3s']:
        assert run('systemctl', 'is-active', service).stdout.strip() == b'active'
        assert run('systemctl', 'is-enabled', service).stdout.strip() == b'enabled'
    assert b'Welcome to nginx!' in http('https://' + HOSTS[1])
    status = json.loads(cli('status', 'fixture').stdout)
    assert status['status'] == 'running', status
    logs = Path('/var/log/hostlier/hostlier.log')
    assert logs.exists() and logs.stat().st_mode & 0o777 == 0o600
    assert b'vm-secret-fixture' not in logs.read_bytes()
    cli('controller', 'status')
    if 'dependencies:' in (ROOT / '.hostlier').read_text():
        redis_query()
    print(json.dumps({'passed': 'installed HTTPS app, controller, services, logs', 'runtime': RUNTIME}))


if sys.argv[1] == 'setup':
    with open('/etc/hosts', 'a') as hosts:
        hosts.write('\n127.0.0.1 ' + ' '.join(HOSTS) + '\n')
    config = json.loads(http('http://127.0.0.1:2019/config/'))
    config['apps'].setdefault('tls', {})['automation'] = {
        'policies': [{'subjects': HOSTS, 'issuers': [{'module': 'internal'}]}]}
    http('http://127.0.0.1:2019/load', json.dumps(config).encode())
    root = Path('/var/lib/caddy/.local/share/caddy/pki/authorities/local/root.crt')
    for attempt in range(100):
        if root.exists():
            break
        time.sleep(.2)
    shutil.copy(root, '/usr/local/share/ca-certificates/hostlier-e2e.crt')
    run('update-ca-certificates')
    run('systemctl', 'restart', 'hostlier')
    time.sleep(1)
    token = json.loads(cli('pairing', 'create', '--socket', '/run/hostlier/controller.sock').stdout)['token']
    cli('connect', BASE, '--name', 'vm', '--token-stdin', input=token.encode())
    cli('secret', 'set', 'fixture-secret', '--stdin', input=b'vm-secret-fixture')
    (ROOT / '.hostlier').write_text('''version: 1
name: fixture
image: nginx:1.28-alpine
port: 80
route: fixture.apps.hostlier.test
secrets:
  FIXTURE_SECRET: fixture-secret
resources:
  cpu: "0.5"
  memory: 128Mi
''')
    print(json.dumps({'passed': 'test CA trust, HTTPS pairing, secret upload', 'runtime': RUNTIME}))
elif sys.argv[1] == 'deploy':
    cli('plan')
    assert cli('deploy', check=False).returncode != 0
    assert cli('deploy', '--yes', check=False).returncode != 0
    approved('deploy')
    verify()
    cli('restart', 'fixture')
    verify()
    print(json.dumps({'passed': 'plan, approval enforcement, real deployment, restart', 'runtime': RUNTIME}))
elif sys.argv[1] == 'verify':
    verify()
elif sys.argv[1] == 'domains':
    config_path = Path('/etc/hostlier/config.yaml')
    cli('domains', 'add', 'cli.hostlier.test')
    assert 'cli.hostlier.test' in config_path.read_text()
    domains = json.loads(cli('domains').stdout)['domains']
    if 'file.hostlier.test' not in domains:
        domains.append('file.hostlier.test')
    config_path.write_text('version: 1\ndomains:\n' + ''.join('  - ' + domain + '\n' for domain in domains))
    run('systemctl', 'restart', 'hostlier')
    for attempt in range(100):
        result = cli('domains', check=False)
        if result.returncode == 0:
            break
        time.sleep(.2)
    assert 'file.hostlier.test' in json.loads(result.stdout)['domains']
    assert cli('domains', 'remove', 'apps.hostlier.test', check=False).returncode != 0
    verify()
    print(json.dumps({'passed': 'domain API persists config, config edits load at restart, active route guard', 'runtime': RUNTIME}))
elif sys.argv[1] == 'slow-backup':
    approved('resource', 'create', 'neo4j', 'fixture-graph', '--storage', '1Gi')
    destination = ROOT / ('graph-' + str(time.time_ns()) + '.backup')
    started = time.monotonic()
    approved('resource', 'backup', 'fixture-graph', '--output', str(destination))
    assert destination.stat().st_size > 0
    assert destination.stat().st_mode & 0o777 == 0o600
    verify()
    print(json.dumps({'passed': 'installed Neo4j offline backup over HTTPS', 'runtime': RUNTIME,
        'seconds': round(time.monotonic() - started, 2)}))
elif sys.argv[1] == 'dependencies':
    approved('resource', 'create', 'redis', 'fixture-cache', '--storage', '1Gi')
    project = ROOT / '.hostlier'
    if 'dependencies:' not in project.read_text():
        with project.open('a') as stream:
            stream.write('''dependencies:
  cache:
    type: redis
    source: existing
    instance: fixture-cache
bindings:
  REDIS_URL: cache.url
''')
    approved('deploy')
    redis_query(write=True)
    assert cli('resource', 'stop', 'fixture-cache', check=False).returncode != 0
    assert cli('resource', 'delete', 'fixture-cache', check=False).returncode != 0
    verify()
    print(json.dumps({'passed': 'approved resource, binding, private app-to-Redis protocol, dependent guards', 'runtime': RUNTIME}))
else:
    raise SystemExit('Use setup, deploy, dependencies, domains, slow-backup, or verify')
