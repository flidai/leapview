"""Narrow host checks/records used over pinned SSH; Kamal owns activation/prune."""
import json
import os
from pathlib import Path
import stat
import subprocess
import sys

from contract import SERVICE, REPOSITORY, validate_record, validate_image, validate_scope, validate_container, cleanup_candidates

ROOT = Path('/var/lib/leapview-site/kamal')


def command(*args, check=True):
    p = subprocess.run(args, capture_output=True, text=True, timeout=60)
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
    paths = {'/', info['DockerRootDir'], '/var/lib/containerd'}
    disks = {}
    for path in sorted(paths):
        s = os.statvfs(path)
        disks[path] = {'available_bytes': s.f_bavail * s.f_frsize, 'available_inodes': s.f_favail}
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


def local_image(record):
    image = inspect('image', REPOSITORY + ':' + record['version'])
    validate_image(record, image)
    if record.get('local_id') and image['Id'] != record['local_id']:
        raise ValueError('saved local image identity changed')
    return image


def running(record):
    image = local_image(record)
    container = inspect('container', SERVICE + '-web-' + record['version'])
    validate_container(record, container)
    if container['Image'] != image['Id']: raise ValueError('container differs from selected local image')
    return container


def scope():
    ids = set(command('docker', 'image', 'ls', '--filter', 'label=service=' + SERVICE, '--quiet', '--no-trunc').split())
    for image_id in ids: validate_scope(inspect('image', image_id))


def preflight(ready, state):
    if state.get('pending'): raise ValueError('unresolved deployment: inspect and recover before retrying')
    if state.get('maintenance_pending'): raise ValueError('unfinished maintenance blocks additional pulls')
    running(state['records'][state['active']])
    scope()
    current = inventory()
    if not current['docker'].startswith('29.') or current['driver'] != 'overlayfs':
        raise ValueError('runtime differs from qualified Docker/containerd target')
    budgets = ready.get('capacity', {})
    if set(budgets) != set(current['disks']): raise ValueError('capacity policy missing a backing filesystem')
    for path, available in current['disks'].items():
        budget = budgets[path]
        # Values come from recorded production-image peak measurements at handover.
        peak, margin, inodes = (budget[k] for k in ('candidate_peak_bytes', 'reserve_bytes', 'reserve_inodes'))
        if any(type(n) is not int or n <= 0 for n in (peak, margin, inodes)):
            raise ValueError('measured capacity policy required')
        if available['available_bytes'] < peak + margin or available['available_inodes'] < inodes:
            raise ValueError('insufficient deployment headroom; no pull or cleanup performed')
    return current


def main():
    request = json.load(sys.stdin)
    operation = request['operation']
    if operation == 'inventory':
        print(json.dumps(inventory())); return
    ready, state = load_ready()
    version = request.get('version')
    result = {}
    if operation == 'state':
        result = state
    elif operation == 'preflight':
        result = {'inventory': preflight(ready, state), 'state': state}
    elif operation == 'begin':
        preflight(ready, state)
        record = validate_record(request['record'])
        if record['version'] == state['active']: raise ValueError('already active: use verified no-op')
        if record['version'] == state.get('prior'): raise ValueError('use the recorded rollback operation')
        if record['version'] in state['records'] and state['records'][record['version']]['image'] != record['image']:
            raise ValueError('version collision')
        state['records'][record['version']] = record
        state['pending'] = record['version']; save(state)
    elif operation == 'rollback-begin':
        if state.get('pending') or state.get('maintenance_pending'):
            raise ValueError('resolve pending deployment/maintenance before a new rollback')
        if not state.get('prior'): raise ValueError('no verified prior version')
        scope(); running(state['records'][state['active']]); local_image(state['records'][state['prior']])
        state['pending'] = state['prior']; save(state)
        result = state
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
    elif operation == 'cleanup':
        if state.get('pending'): raise ValueError('unresolved attempt blocks cleanup')
        scope(); running(state['records'][state['active']])
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
        scope(); running(state['records'][state['active']])
        ids = command('docker', 'ps', '-aq', '--filter', 'label=service=' + SERVICE).split()
        containers = [inspect('container', cid) for cid in ids]
        if cleanup_candidates(containers, state['records'], state['active'], state.get('prior')):
            raise ValueError('unexpected containers remain after native cleanup')
        state['records'] = {v: r for v, r in state['records'].items() if v in (state['active'], state.get('prior'))}
        state['maintenance_pending'] = False; save(state)
    else: raise ValueError('unknown host operation')
    print(json.dumps(result))


if __name__ == '__main__': main()
