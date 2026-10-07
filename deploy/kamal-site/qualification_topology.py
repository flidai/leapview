"""Exercise production handover checks against the disposable Docker topology.

Only fixture paths/repository and absent systemd are bound here. Docker inspection,
proxy digest, Caddy runtime, ports, mounts, and configuration checks remain intact.
This is test scaffolding, never an operator admission or handover path.
"""
import hashlib
import json
from pathlib import Path
import types


def verify_topology(here, trial, state, topology):
    if __import__('os').getpid() != 1:
        raise RuntimeError('topology qualification requires the guarded fixture namespace')
    source = (here / 'contract.py').read_text() + '\n' + '\n'.join(
        line for line in (here / 'host.py').read_text().splitlines()
        if not line.startswith('from contract import '))
    source = source.replace('ghcr.io/flidai/leapview-site', trial.repo)
    source = source.replace("Path('/var/lib/leapview-site/kamal')", 'Path(' + repr(str(state)) + ')')
    source = source.replace("Path('/opt/leapview-site')", 'Path(' + repr(str(topology)) + ')')
    source = source.replace('/var/lib/leapview-site/caddy-', str(topology) + '/caddy-')
    checker = types.ModuleType('qualified_production_host')
    exec(compile(source, str(here / 'host.py'), 'exec'), checker.__dict__)
    original_command = checker.command
    def command(*args, **kwargs):
        if args[0] == 'systemctl':
            if args[1] == 'is-active': return 'inactive'
            if args[1] == 'is-enabled': return 'disabled'
            raise AssertionError('unexpected systemd fixture operation')
        return original_command(*args, **kwargs)
    checker.command = command
    ready_path = state / 'ready.json'
    ready = json.loads(ready_path.read_text())
    ready['topology'] = {field: hashlib.sha256((topology / name).read_bytes()).hexdigest()
                         for name, field in (('compose.yaml', 'compose_sha256'), ('Caddyfile', 'caddy_sha256'))}
    ready_path.write_text(json.dumps(ready))
    checker.load_ready()
    # Real config drift must block before deployment even with a healthy app.
    route = topology / 'Caddyfile'
    saved = route.read_bytes()
    try:
        route.write_bytes(saved + b'\n# unrecorded change\n')
        try:
            checker.load_ready()
        except ValueError as exc:
            if 'permanent Caddy configuration differs' not in str(exc): raise
        else:
            raise AssertionError('production readiness accepted changed Caddy configuration')
    finally:
        route.write_bytes(saved)
    checker.load_ready()
    trial.report['production_topology_checks'] = {
        'actual_docker_checks': ['private pinned proxy', 'restart policies', 'external network',
                                 'Caddy-only Compose', 'public HTTP/HTTPS ports',
                                 'persistent certificate/configuration binds', 'readonly Caddyfile',
                                 'configuration hashes and drift rejection'],
        'fixture_bindings': ['canonical repository', 'state/topology paths', 'systemd inactive/disabled'],
        'checks_passed': trial.report.get('production_topology_checks', {}).get('checks_passed', 0) + 1,
    }
    print('PASS production handover checks against actual disposable proxy/Caddy topology', flush=True)
