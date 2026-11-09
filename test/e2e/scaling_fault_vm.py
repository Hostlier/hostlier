#!/usr/bin/env python3
"""Prove installed k3s CLI rollback restores a scaled revision's actual allocation.

Run inside the disposable installed VM as root; existing application and Redis data
are preserved and initial limits/replicas are restored through explicit approval.
"""
from pathlib import Path
import sys

# Load shared authenticated CLI/terminal/cluster helpers without running their scenario dispatch.
source = (Path(__file__).parent / 'scaling_vm.py').read_text()
exec(compile(source.split("\n# Wait for authenticated readiness")[0], 'scaling_vm_helpers', 'exec'))


def status():
    """Read the authenticated controller revision independently of Kubernetes observations."""
    return json.loads(cli('status', 'fixture').stdout)


def rollback_scaled_revision():
    """Approve two real scales, then rollback to the first scale by immutable revision ID."""
    before = status()
    original = before['spec']
    original_file = (ROOT / '.hostlier').read_bytes()
    limits = original['resources']
    try:
        redis_query(write=True)
        approved('scale', 'fixture', '--replicas', '2', '--cpu', '0.25', '--memory', '96Mi')
        observed(2, '250m', '96Mi')
        historical = status()['revision']
        approved('scale', 'fixture', '--replicas', '1', '--cpu', '0.5', '--memory', '128Mi')
        observed(1, '500m', '128Mi')
        approved('rollback', 'fixture', '--revision', historical)
        observed(2, '250m', '96Mi')
        reverted = status()['spec']
        assert reverted['replicas'] == 2 and reverted['resources'] == {'cpu': '0.25', 'memory': '96Mi'}, reverted
        redis_query()
        assert (ROOT / '.hostlier').read_bytes() == original_file
        print(json.dumps({'passed': 'installed k3s CLI approved historical scaled-revision rollback',
                          'restoredReplicas': 2, 'restoredCPU': '250m', 'restoredMemory': '96Mi',
                          'dependencyData': 'preserved'}))
    finally:
        approved('scale', 'fixture', '--replicas', str(original.get('replicas', 1)),
                 '--cpu', limits['cpu'], '--memory', limits['memory'])
        observed(original.get('replicas', 1), str(int(float(limits['cpu']) * 1000)) + 'm', limits['memory'])
        redis_query()


# Only the explicitly invoked local installed scenario may mutate fixture capacity.
if sys.argv[1] == 'rollback':
    rollback_scaled_revision()
else:
    raise SystemExit('Usage: scaling_fault_vm.py rollback k3s')
