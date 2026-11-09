#!/usr/bin/env python3
"""Run only inside the task-owned installed local VM; preserve application and Redis fixtures.

Usage: sudo python3 scaling_vm.py exercise|zero|verify-zero|up k3s
Place installed_vm.py beside this file. Reboot between zero and verify-zero.
"""
from pathlib import Path
import sys

# Reuse the existing installed fixture's authenticated CLI, PTY approval and protocol helpers.
fixture_source = (Path(__file__).parent / 'installed_vm.py').read_text()
fixture_source = fixture_source.replace("obj['spec']['replicas'] == 1", "obj['spec']['replicas'] > 0")
exec(compile(fixture_source.split("\nif sys.argv[1] == 'setup':")[0], 'installed_vm_helpers', 'exec'))


def deployments():
    """Inspect real workload counters and limits independently of controller metadata."""
    return json.loads(run('/usr/local/bin/k3s', 'kubectl', 'get', 'deployment', '-A',
        '-l', 'io.hostlier.app=fixture', '-o', 'json').stdout)['items']


def observed(count, cpu=None, memory=None):
    """Require actual desired and ready capacity to match approved scaling."""
    if RUNTIME == 'docker':
        ids = run('docker', 'ps', '-q', '--filter', 'label=io.hostlier.app=fixture').stdout.decode().split()
        assert len(ids) == count, ids
        if cpu:
            container = json.loads(run('docker', 'inspect', ids[0]).stdout)[0]
            host = container['HostConfig']
            assert host['NanoCpus'] == int(float(cpu.removesuffix('m')) * 1000000)
            assert host['Memory'] == int(memory.removesuffix('Mi')) * 1024 * 1024
        print(json.dumps({'observedRunning': len(ids)}))
        return
    items = deployments()
    desired = sum(item['spec'].get('replicas', 1) for item in items)
    ready = sum(item.get('status', {}).get('readyReplicas', 0) for item in items)
    assert desired == count and ready == count, (desired, ready, count)
    active = [item for item in items if item['spec'].get('replicas', 1)]
    if cpu:
        limits = active[0]['spec']['template']['spec']['containers'][0]['resources']['limits']
        assert limits['cpu'] == cpu and limits['memory'] == memory, limits
    print(json.dumps({'observedDesired': desired, 'observedReady': ready}))


def unavailable():
    """Require the retained route to explicitly return unavailable at desired zero."""
    import urllib.error
    try:
        http('https://fixture.apps.hostlier.test')
    except urllib.error.HTTPError as error:
        assert error.code == 503, error
        return
    raise AssertionError('zero route was available')


def replica_responses():
    """Write each real pod identity into its ephemeral response and observe service distribution."""
    pods = json.loads(run('/usr/local/bin/k3s', 'kubectl', 'get', 'pod', '-A',
        '-l', 'io.hostlier.app=fixture', '-o', 'json').stdout)['items']
    names = set()
    for pod in pods:
        name, namespace = pod['metadata']['name'], pod['metadata']['namespace']
        names.add(name)
        run('/usr/local/bin/k3s', 'kubectl', 'exec', '-n', namespace, name,
            '--', 'sh', '-c', 'hostname > /usr/share/nginx/html/index.html')
    from concurrent.futures import ThreadPoolExecutor
    # Parallel requests open multiple upstream service connections; sequential keepalive may pin one pod.
    with ThreadPoolExecutor(max_workers=32) as workers:
        responses = set(workers.map(lambda _: http('https://fixture.apps.hostlier.test').decode().strip(), range(240)))
    assert responses == names, (responses, names)
    print(json.dumps({'routedHealthyReplicas': sorted(responses)}))


def decline_scale(interrupt=False):
    """Exercise the actual terminal approval prompt and decline the concrete plan."""
    master, slave = pty.openpty()
    proposal = ['--cpu', '0.4'] if RUNTIME == 'docker' else ['--replicas', '2']
    process = subprocess.Popen(['/usr/local/bin/hostlier', 'scale', 'fixture', *proposal],
        stdin=slave, stdout=slave, stderr=slave)
    os.close(slave)
    output, sent = b'', False
    deadline = time.time() + 60
    while time.time() < deadline and process.poll() is None:
        if select.select([master], [], [], .1)[0]:
            try:
                output += os.read(master, 65536)
            except OSError:
                break
            if b'[y/N]' in output and not sent:
                if interrupt:
                    import signal
                    process.send_signal(signal.SIGINT)
                else:
                    os.write(master, b'n\n')
                sent = True
    if process.poll() is None:
        process.kill()
    assert process.wait() != 0 and sent, output
    os.close(master)


def no_approval():
    """Missing TTY and forbidden bypass cannot change desired capacity."""
    proposal = ['--cpu', '0.4'] if RUNTIME == 'docker' else ['--replicas', '2']
    for flags in [[], ['--yes']]:
        result = cli('scale', 'fixture', *proposal, *flags, check=False)
        assert result.returncode != 0, result.stdout
    decline_scale()
    decline_scale(interrupt=True)
    observed(1)


# Wait for authenticated readiness after an intentional controller restart.
for attempt in range(100):
    if cli('status', 'fixture', check=False).returncode == 0:
        break
    time.sleep(.2)
else:
    raise RuntimeError('installed controller did not become ready')

if sys.argv[1] == 'exercise':
    no_approval()
    before = (ROOT / '.hostlier').read_bytes()
    if RUNTIME == 'docker':
        try:
            approved('scale', 'fixture', '--replicas', '3')
        except RuntimeError:
            pass
        else:
            raise AssertionError('Docker accepted multiple replicas')
        observed(1)
        approved('scale', 'fixture', '--cpu', '0.25', '--memory', '96Mi')
        observed(1, '250m', '96Mi')
    else:
        approved('scale', 'fixture', '--replicas', '3', '--cpu', '0.25', '--memory', '96Mi')
        observed(3, '250m', '96Mi')
        replica_responses()
    redis_query(write=True)
    approved('scale', 'fixture', '--replicas', '1', '--cpu', '0.5', '--memory', '128Mi')
    observed(1, '500m', '128Mi')
    assert (ROOT / '.hostlier').read_bytes() == before
    redis_query()
    print(f'PASS: installed CLI {RUNTIME} replica/vertical scaling, approval guards, routing, dependency data')
elif sys.argv[1] == 'approval':
    no_approval()
    print('PASS: missing TTY, forbidden bypass, decline and interrupted approval')
elif sys.argv[1] == 'zero':
    approved('scale', 'fixture', '--replicas', '0')
    observed(0)
    unavailable()
    print('PASS: zero; reboot VM then run verify-zero')
elif sys.argv[1] == 'verify-zero':
    retained = json.loads(cli('status', 'fixture').stdout)
    assert retained['spec']['replicas'] == 0, retained['spec']
    assert retained['status'] == 'stopped', retained['status']
    observed(0)
    unavailable()
    for action in ['start', 'restart']:
        assert cli(action, 'fixture', check=False).returncode != 0
    observed(0)
    print('PASS: reboot retains zero and lifecycle cannot override approval')
elif sys.argv[1] == 'up':
    approved('scale', 'fixture', '--replicas', '1')
    observed(1)
    redis_query()
    print('PASS: approved up restores route and dependency data')
else:
    raise SystemExit('exercise|zero|verify-zero|up')
