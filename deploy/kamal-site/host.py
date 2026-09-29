"""Narrow host checks/records used over pinned SSH; Kamal owns activation/prune."""
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys

from contract import SERVICE, REPOSITORY, LOCAL_REPOSITORY, validate_record, validate_image, validate_scope, validate_container, cleanup_candidates

ROOT = Path('/var/lib/leapview-site/kamal')


def command(*args, check=True):
    p = subprocess.run(args, capture_output=True, text=True, timeout=900)
    if check and p.returncode:
        raise RuntimeError('host command failed: ' + args[0] + ' ' + args[1])
    return p.stdout.strip()


def inspect(kind, name):
    return json.loads(command('docker', kind, 'inspect', name))[0]


def protected_read(path):
    st = path.lstat()
    if stat.S_ISLNK(st.st_mode) or st.st_uid != 0 or st.st_mode & 0o022:
        raise ValueError('deployment state must be root-owned and not writable by other users')
    return json.loads(path.read_text())


def save(state):
    path = ROOT / 'state.next'
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, 'w') as f:
        json.dump(state, f); f.flush(); os.fsync(f.fileno())
    path.replace(ROOT / 'state.json')
    fd = os.open(ROOT, os.O_RDONLY | os.O_DIRECTORY)
    try: os.fsync(fd)
    finally: os.close(fd)


def inventory():
    info = json.loads(command('docker', 'info', '--format', '{{json .}}'))
    paths = {info['DockerRootDir'], '/var/lib/containerd'}
    disks = {}
    for path in sorted(paths):
        device = str(os.stat(path).st_dev)
        if device in disks:
            disks[device]['paths'].append(path); continue
        s = os.statvfs(path)
        disks[device] = {'paths': [path], 'capacity_bytes': s.f_blocks * s.f_frsize,
                         'available_bytes': s.f_bavail * s.f_frsize, 'available_inodes': s.f_favail}
    return {'docker': info['ServerVersion'], 'driver': info['Driver'], 'disks': disks,
            'timer': command('systemctl', 'is-active', 'leapview-site-reconcile.timer', check=False)}


def load_ready():
    st = ROOT.lstat()
    if not stat.S_ISDIR(st.st_mode) or st.st_uid != 0 or st.st_mode & 0o077:
        raise ValueError('protected handover directory missing or has unsafe permissions')
    ready = protected_read(ROOT / 'ready.json')
    if ready.get('schema') != 1 or ready.get('controller') != 'kamal' or ready.get('handover_verified') is not True:
        raise ValueError('controller handover is not verified')
    for unit in ('leapview-site-reconcile.timer', 'leapview-site-reconcile.service'):
        if command('systemctl', 'is-active', unit, check=False) not in ('inactive', 'failed'):
            raise ValueError('legacy controller is not inactive')
    if command('systemctl', 'is-enabled', 'leapview-site-reconcile.timer', check=False) not in ('disabled', 'masked'):
        raise ValueError('legacy timer is not disabled')
    proxy = inspect('container', 'kamal-proxy')
    if not proxy['State']['Running'] or proxy['HostConfig'].get('PortBindings'):
        raise ValueError('private Kamal proxy is not ready')
    proxy_image = inspect('image', proxy['Image'])
    if not any(ref.endswith('@sha256:826a6f66c6ba26ac26197ac8755804403c9bb617b90cfac25c7972154c5328ab')
               for ref in proxy_image.get('RepoDigests', [])):
        raise ValueError('proxy image differs from qualified digest')
    topology_ready(ready, proxy)
    state = protected_read(ROOT / 'state.json')
    if state.get('schema') != 1 or not state.get('active'):
        raise ValueError('verified initial Kamal state missing')
    for version, record in state['records'].items():
        validate_record(record)
        if version != record['version']: raise ValueError('record key differs from version')
    for version in (state['active'], state.get('prior')):
        if version and state['records'][version].get('verified') is not True:
            raise ValueError('active/prior version is not verified')
    return ready, state


def topology_ready(ready, proxy):
    if proxy['HostConfig'].get('RestartPolicy', {}).get('Name') != 'unless-stopped':
        raise ValueError('proxy restart policy differs; restore pinned private proxy using the runbook')
    if 'kamal' not in proxy['NetworkSettings']['Networks']:
        raise ValueError('proxy is not on the persistent Kamal network')
    site = Path('/opt/leapview-site')
    topology = ready.get('topology', {})
    for name, field in (('compose.yaml', 'compose_sha256'), ('Caddyfile', 'caddy_sha256')):
        if hashlib.sha256((site / name).read_bytes()).hexdigest() != topology.get(field):
            raise ValueError('permanent Caddy configuration differs from the verified handover')
    config = json.loads(command('docker', 'compose', '--project-directory', str(site),
                                '--env-file', str(site / 'deployment.env'), '-f', str(site / 'compose.yaml'),
                                'config', '--format', 'json'))
    if set(config['services']) != {'caddy'} or not config['networks'].get('kamal', {}).get('external'):
        raise ValueError('active Compose definition must be Caddy-only with an external Kamal network')
    if not re.fullmatch(r'[^\s@]+@sha256:[a-f0-9]{64}', config['services']['caddy'].get('image', '')):
        raise ValueError('Caddy image must be pinned by immutable digest')
    ids = command('docker', 'ps', '-aq', '--filter', 'label=com.docker.compose.project=leapview-site',
                  '--filter', 'label=com.docker.compose.service=caddy').split()
    if len(ids) != 1: raise ValueError('exactly one Caddy container required')
    caddy = inspect('container', ids[0])
    if (not caddy['State']['Running'] or 'kamal' not in caddy['NetworkSettings']['Networks']
            or caddy['HostConfig'].get('RestartPolicy', {}).get('Name') != 'unless-stopped'
            or caddy['Config']['Image'] != config['services']['caddy']['image']):
        raise ValueError('Caddy runtime differs from persistent topology')
    ports = caddy['HostConfig'].get('PortBindings') or {}
    for port in ('80', '443'):
        if not any(binding.get('HostPort') == port and binding.get('HostIp', '') in ('', '0.0.0.0', '::')
                   for binding in (ports.get(port + '/tcp') or [])):
            raise ValueError('Caddy public HTTP/HTTPS ports differ from persistent topology')
    mounts = {m['Destination']: m for m in caddy['Mounts']}
    for destination, source, writable in (
        ('/etc/caddy/Caddyfile', str(site / 'Caddyfile'), False),
        ('/data', '/var/lib/leapview-site/caddy-data', True),
        ('/config', '/var/lib/leapview-site/caddy-config', True),
    ):
        mount = mounts.get(destination, {})
        if mount.get('Source') != source or mount.get('Type') != 'bind' or mount.get('RW') is not writable:
            raise ValueError('Caddy route/certificate mounts differ from persistent topology')
    text = (site / 'Caddyfile').read_text()
    if 'reverse_proxy kamal-proxy:80' not in text or 'admin off' not in text:
        raise ValueError('Caddy route is not persistently directed at the private Kamal proxy')


def local_image(record):
    image = inspect('image', LOCAL_REPOSITORY + ':' + record['version'])
    validate_image(record, image)
    selected = json.loads(command('docker', 'image', 'inspect', '--platform', 'linux/amd64', LOCAL_REPOSITORY + ':' + record['version']))[0]
    if selected.get('Descriptor', {}).get('digest') != record['platform']:
        raise ValueError('selected local platform differs from admitted manifest/config')
    if record.get('local_id') and image['Id'] != record['local_id']:
        raise ValueError('saved local image identity changed')
    return image


def running(record):
    image = local_image(record)
    container = inspect('container', SERVICE + '-web-' + record['version'])
    validate_container(record, container)
    if container['Image'] != image['Id']: raise ValueError('container differs from selected local image')
    return container


def scope(records):
    ids = set(command('docker', 'image', 'ls', '--filter', 'label=service=' + SERVICE, '--quiet', '--no-trunc').split())
    known = {record.get('local_id') for record in records.values()}
    for image_id in ids:
        validate_scope(inspect('image', image_id))
        if image_id not in known: raise ValueError('unrecorded service image; inspect ownership before mutation')


def preflight(ready, state, record=None):
    if state.get('pending'): raise ValueError('unresolved deployment: inspect and recover before retrying')
    if state.get('maintenance_pending'): raise ValueError('unfinished maintenance blocks additional pulls')
    running(state['records'][state['active']])
    scope(state['records'])
    if record and record['version'] == state['active']: return inventory()
    current = inventory()
    if not current['docker'].startswith('29.') or current['driver'] != 'overlayfs':
        raise ValueError('runtime differs from qualified Docker/containerd target')
    budgets = ready.get('capacity', {})
    if set(budgets) != set(current['disks']): raise ValueError('capacity policy missing a backing filesystem')
    for path, available in current['disks'].items():
        budget = budgets[path]
        if set(budget.get('paths', [])) != set(available['paths']):
            raise ValueError('capacity backing paths changed; repeat qualification')
        measured, inode_peak, headroom, reserve, inode_reserve, envelope = (
            budget[k] for k in ('measured_peak_bytes', 'measured_peak_inodes', 'candidate_headroom_bytes',
                                'reserve_bytes', 'reserve_inodes', 'qualified_compressed_bytes'))
        if any(type(n) is not int or n <= 0 for n in (measured, inode_peak, headroom, reserve, inode_reserve, envelope)):
            raise ValueError('measured capacity policy required')
        if (headroom < (3 * measured + 1) // 2 or reserve < max(2 * 1024**3, (available['capacity_bytes'] + 9) // 10)
                or inode_reserve < max(10000, 2 * inode_peak)):
            raise ValueError('capacity margins below qualified policy')
        if record and (type(record.get('compressed_bytes')) is not int or record['compressed_bytes'] > envelope):
            raise ValueError('candidate exceeds tested image-size envelope; renewed qualification required')
        if available['available_bytes'] < headroom + reserve or available['available_inodes'] < inode_peak + inode_reserve:
            raise ValueError('insufficient deployment headroom; no pull or cleanup performed')
    return current


def rollback_ready(state):
    if state.get('pending') or state.get('maintenance_pending'):
        raise ValueError('resolve pending deployment/maintenance before a new rollback')
    prior = state.get('prior')
    if not prior or prior == state['active']: raise ValueError('no distinct verified prior version')
    record = state['records'][prior]
    if record.get('verified') is not True: raise ValueError('prior is not verified')
    scope(state['records'])
    image = local_image(record)
    try:
        container = inspect('container', SERVICE + '-web-' + prior)
        validate_container(record, container, require_running=False)
        if container['Image'] != image['Id']: raise ValueError('prior image/container disagree')
    except Exception as exc:
        raise ValueError('prior recovery container missing or contradictory; inspect saved record; no pull permitted') from exc


def reconcile_pending(state):
    """Finish only local identity bookkeeping for an audited interrupted pull."""
    pending = state['records'][state['pending']]
    alias = LOCAL_REPOSITORY + ':' + pending['version']
    local = json.loads(command('docker', 'image', 'inspect', alias, check=False) or '[]')
    canonical = json.loads(command('docker', 'image', 'inspect', pending['image'], check=False) or '[]')
    if canonical:
        validate_image(pending, canonical[0])
        selected = json.loads(command('docker', 'image', 'inspect', '--platform', 'linux/amd64', pending['image']))[0]
        if selected.get('Descriptor', {}).get('digest') != pending['platform']:
            raise ValueError('pending image platform differs from admitted identity')
        if pending.get('local_id') and pending['local_id'] != canonical[0]['Id']:
            raise ValueError('pending saved image identity changed')
        if local and local[0]['Id'] != canonical[0]['Id']:
            raise ValueError('pending local and canonical images disagree')
        if not local: command('docker', 'tag', pending['image'], alias)
        pending['local_id'] = local_image(pending)['Id']
        # Normalize the same admitted identity for native pruning, without force.
        command('docker', 'image', 'rm', pending['image'])
    elif local:
        pending['local_id'] = local_image(pending)['Id']


def recovery_ready(state):
    """Validate an explicit restore without guessing whether a switch completed."""
    if not state.get('pending') or state['pending'] == state['active']:
        raise ValueError('no distinct unresolved candidate; inspect state or use maintain')
    record = state['records'][state['active']]
    if record.get('verified') is not True: raise ValueError('saved active is not verified')
    reconcile_pending(state)
    scope(state['records'])
    image = local_image(record)
    container = inspect('container', SERVICE + '-web-' + record['version'])
    validate_container(record, container, require_running=False)
    if container['Image'] != image['Id']: raise ValueError('saved active image/container disagree')


def preserve_prior(state):
    """Recreate only a missing stopped recovery container after acceptance.

    Kamal still owns boot and traffic switching. This never starts a process or
    pulls: it preserves the exact previously verified image/runtime for rollback
    when the broken current container was missing at the start of recovery.
    """
    if not state.get('prior'): return
    record = state['records'][state['prior']]
    local_image(record)
    name = SERVICE + '-web-' + record['version']
    raw = command('docker', 'container', 'inspect', name, check=False)
    containers = json.loads(raw or '[]')
    if containers:
        validate_container(record, containers[0], require_running=False); return
    if record.get('verified') is not True: raise ValueError('cannot preserve an unverified recovery version')
    command('docker', 'create', '--pull', 'never', '--name', name, '--network', 'kamal', '--restart', 'unless-stopped',
            '--label', 'service=' + SERVICE, '--label', 'role=web', '--label', 'version=' + record['version'],
            '--user', record['runtime']['user'], '--read-only', '--cap-drop', 'ALL',
            '--security-opt', 'no-new-privileges=true', '--tmpfs', '/tmp:rw,noexec,nosuid,size=64m',
            '--log-driver', 'json-file', '--log-opt', 'max-size=10m', '--log-opt', 'max-file=3',
            '--env', 'LEAPVIEW_SITE_BASE_URL=' + record['runtime']['base_url'],
            LOCAL_REPOSITORY + ':' + record['version'], '-addr=:8081', '-image-reference=' + record['image'])
    validate_container(record, inspect('container', name), require_running=False)


def status():
    report = {'errors': []}
    for name, callback in (
        ('inventory', inventory), ('state', lambda: protected_read(ROOT / 'state.json')),
        ('owner', lambda: protected_read(ROOT / 'owner.json')),
        ('proxy', lambda: inspect('container', 'kamal-proxy')),
        ('handover', lambda: protected_read(ROOT / 'ready.json'))):
        try: report[name] = callback()
        except Exception as exc: report['errors'].append(name + ': ' + str(exc))
    if 'state' in report:
        state = report['state']
        for label in ('active', 'prior'):
            try:
                record = state['records'][state[label]]
                report[label + '_container'] = inspect('container', SERVICE + '-web-' + record['version'])
                local_image(record)
            except Exception as exc: report['errors'].append(label + ': ' + str(exc))
        try: rollback_ready(state); report['rollback_available'] = True
        except Exception as exc:
            report['rollback_available'] = False; report['errors'].append('rollback: ' + str(exc))
    return report


def main():
    request = json.load(sys.stdin)
    operation = request['operation']
    if operation == 'status':
        print(json.dumps(status())); return
    if operation == 'inventory':
        print(json.dumps(inventory())); return
    ready, state = load_ready()
    version = request.get('version')
    result = {}
    if operation == 'state':
        result = state
    elif operation == 'preflight':
        result = {'inventory': preflight(ready, state, request.get('record')), 'state': state}
    elif operation == 'begin':
        record = validate_record(request['record'])
        preflight(ready, state, record)
        if record['version'] == state['active']: raise ValueError('already active: use verified no-op')
        if record['version'] == state.get('prior'): raise ValueError('use the recorded rollback operation')
        if record['version'] in state['records'] and state['records'][record['version']]['image'] != record['image']:
            raise ValueError('version collision')
        state['records'][record['version']] = record
        state['pending'] = record['version']; save(state)
    elif operation == 'rollback-begin':
        rollback_ready(state)
        state['pending'] = state['prior']; save(state)
        result = state
    elif operation == 'recovery-begin':
        recovery_ready(state)
        save(state)
        result = state
    elif operation == 'maintenance-begin':
        running(state['records'][state['active']])
        if state.get('pending'):
            reconcile_pending(state)
            scope(state['records'])
            ids = command('docker', 'ps', '-aq', '--filter', 'label=service=' + SERVICE).split()
            cleanup_candidates([inspect('container', cid) for cid in ids], state['records'], state['active'], state.get('prior'))
            state['pending'] = None
            state['maintenance_pending'] = True
            save(state)
        scope(state['records'])
        result = state
    elif operation == 'pull':
        if state.get('pending') != version: raise ValueError('pull requires persisted pending attempt')
        record = state['records'][version]
        command('docker', 'pull', '--platform', 'linux/amd64', record['image'])
        image = inspect('image', record['image'])
        validate_image(record, image)
        command('docker', 'tag', record['image'], LOCAL_REPOSITORY + ':' + version)
        local_image(record)
        # Keep the admitted content under its host-local identity. Otherwise
        # Docker 29 exposes an extra repository:<none> row that native Kamal
        # pruning cannot parse. Never force-remove a referenced image.
        command('docker', 'image', 'rm', record['image'])
    elif operation == 'image':
        record = state['records'][version]
        if version not in (state.get('pending'), state['active'], state.get('prior')):
            raise ValueError('unselected pre-boot version')
        image = local_image(record)
        record['local_id'] = image['Id']; save(state)
    elif operation == 'verify':
        running(state['records'][version])
    elif operation == 'accept':
        if state.get('pending') != version: raise ValueError('candidate is not pending')
        running(state['records'][version])
        state['records'][version]['verified'] = True
        state['prior'], state['active'], state['pending'] = state['active'], version, None
        state['maintenance_pending'] = True
        save(state)
    elif operation == 'restored':
        if version != state['active']: raise ValueError('restore must target last verified active version')
        running(state['records'][version])
        state['pending'] = None; state['maintenance_pending'] = True; save(state)
    elif operation == 'preserve-prior':
        if state.get('pending') or not state.get('maintenance_pending'):
            raise ValueError('recovery material can only be preserved after verified acceptance')
        running(state['records'][state['active']]); preserve_prior(state)
    elif operation == 'cleanup':
        if state.get('pending'): raise ValueError('unresolved attempt blocks cleanup')
        scope(state['records']); running(state['records'][state['active']])
        ids = command('docker', 'ps', '-aq', '--filter', 'label=service=' + SERVICE).split()
        containers = [inspect('container', cid) for cid in ids]
        if state.get('prior'): local_image(state['records'][state['prior']])
        selected = cleanup_candidates(containers, state['records'], state['active'], state.get('prior'))
        for cid in selected:
            if inspect('container', cid)['State']['Running']: raise ValueError('container changed during cleanup')
            command('docker', 'container', 'rm', cid)
        result = {'removed_container_ids': selected}
    elif operation == 'maintained':
        if state.get('pending'): raise ValueError('unresolved attempt')
        scope(state['records']); running(state['records'][state['active']])
        ids = command('docker', 'ps', '-aq', '--filter', 'label=service=' + SERVICE).split()
        containers = [inspect('container', cid) for cid in ids]
        if cleanup_candidates(containers, state['records'], state['active'], state.get('prior')):
            raise ValueError('unexpected containers remain after native cleanup')
        retained = {state['active'], state.get('prior')} - {None}
        expected_ids = {local_image(state['records'][v])['Id'] for v in retained}
        actual_ids = set(command('docker', 'image', 'ls', '--filter', 'label=service=' + SERVICE, '--quiet', '--no-trunc').split())
        if actual_ids != expected_ids: raise ValueError('native pruning did not retain exactly current plus distinct prior')
        state['records'] = {v: r for v, r in state['records'].items() if v in (state['active'], state.get('prior'))}
        state['maintenance_pending'] = False; save(state)
    else: raise ValueError('unknown host operation')
    print(json.dumps(result))


if __name__ == '__main__': main()
