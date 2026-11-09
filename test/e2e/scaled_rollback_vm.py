#!/usr/bin/env python3
"""Run an installed CLI scaled-revision rollback gate inside the task-owned local VM."""
from pathlib import Path
import sys

# Reuse the installed fixture's authenticated PTY approvals and backend observations.
source = (Path(__file__).parent / 'scaling_vm.py').read_text()
exec(compile(source.split('\n# Wait for authenticated readiness')[0], 'scaling_helpers', 'exec'))

# Preserve the original live revision so the fixture remains usable by subsequent gates.
original = json.loads(cli('status', 'fixture').stdout)
replicas = 3 if RUNTIME == 'k3s' else 1
try:
    approved('scale', 'fixture', '--replicas', str(replicas), '--cpu', '0.25', '--memory', '96Mi')
    selected = json.loads(cli('status', 'fixture').stdout)
    observed(replicas, '250m', '96Mi')
    redis_query(write=True)
    approved('scale', 'fixture', '--replicas', '1', '--cpu', '0.5', '--memory', '128Mi')
    observed(1, '500m', '128Mi')
    approved('rollback', 'fixture', '--revision', selected['revision'])
    current = json.loads(cli('status', 'fixture').stdout)
    assert current['spec']['resources'] == selected['spec']['resources'], current['spec']
    assert current['spec']['replicas'] == replicas, current['spec']
    observed(replicas, '250m', '96Mi')
    redis_query()
    print(f'PASS: installed CLI {RUNTIME} rollback restores scaled revision limits/replicas, healthy routing and existing Redis data')
finally:
    # Restore through the same explicit approval boundary used by users.
    approved('rollback', 'fixture', '--revision', original['revision'])
