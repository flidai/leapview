#!/usr/bin/env python3
"""Demo-02 image-only transaction. EOF/timeout before browser approval rolls back."""
import datetime
import contextlib
import stat
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import select
import signal
import shutil
import subprocess
import sys
import tempfile
import urllib.parse
import urllib.request

ROOT = Path('/opt/leapview')
PROVIDER = Path('/etc/leapview-provider-cfo')
APP = 'leapview-cfo-leapview-1'
POSTGRES = 'demo02-postgres-cfo'
VOLUME = 'leapview-cfo_leapview-state'
LOG = sys.stderr
IMAGE_RE = r'ghcr\.io/flidai/leapview@sha256:[0-9a-f]{64}'

def run(*args, **kwargs):
    return subprocess.run(args, check=True, stdout=kwargs.pop('stdout', LOG), stderr=LOG, **kwargs)

def out(*args): return subprocess.check_output(args, text=True, stderr=LOG).strip()

def replace_image(data, old, new):
    lines = data.splitlines(keepends=True)
    pins = [i for i, line in enumerate(lines) if line.startswith(b'LEAPVIEW_IMAGE=')]
    if len(pins) != 1 or lines[pins[0]].strip() != ('LEAPVIEW_IMAGE='+old).encode():
        raise ValueError('Deployment image pin differs from running container')
    lines[pins[0]] = ('LEAPVIEW_IMAGE='+new+'\n').encode()
    return b''.join(lines)

def await_approval(stream, timeout=300):
    if not select.select([stream],[],[],timeout)[0] or stream.readline().strip() != 'commit':
        raise RuntimeError('Browser validation missing, failed, timed out or runner disconnected')


def transaction(apply, validate, rollback):
    try:
        apply()
        validate()
    except BaseException as error:
        try: rollback()
        except BaseException as rollback_error:
            raise RuntimeError('Deployment failed; rollback failed; operator recovery required') from rollback_error
        raise error

def atomic(path, data):
    temp = path.with_name(path.name+'.demo-tmp')
    temp.write_bytes(data)
    temp.chmod(0o600)
    os.replace(temp, path)

def link(target):
    temp = ROOT/'current.demo-tmp'
    temp.unlink(missing_ok=True)
    temp.symlink_to(target)
    os.replace(temp, ROOT/'current')

def ready():
    with urllib.request.urlopen(urllib.request.Request('http://127.0.0.1:8081/readyz', headers={'Host': 'demo.leapview.dev'}), timeout=10) as response:
        if json.load(response)['status'] != 'ready': raise RuntimeError('Application not ready')

def inspect():
    if out('hostname') != 'app-leapview-demo-02': raise RuntimeError('Unexpected target host')
    info = json.loads(out('docker', 'inspect', APP))[0]
    if info['Config']['Labels'].get('com.docker.compose.project') != 'leapview-cfo':
        raise RuntimeError('Unexpected Compose project')
    if not any(m.get('Name') == VOLUME and m['Destination'] == '/var/lib/leapview' for m in info['Mounts']):
        raise RuntimeError('Unexpected application state volume')
    env = dict(v.split('=', 1) for v in info['Config']['Env'] if '=' in v)
    for key, database in [('LEAPVIEW_POSTGRES_CONTROL_URL', 'leapview_control'),
                          ('LEAPVIEW_POSTGRES_DUCKLAKE_URL', 'leapview_ducklake')]:
        url = urllib.parse.urlsplit(env[key])
        if url.hostname != POSTGRES or url.path != '/'+database:
            raise RuntimeError('Backup target does not match application database binding')
    image = info['Config']['Image']
    if not re.fullmatch(IMAGE_RE, image): raise RuntimeError('Predecessor must use an immutable image')
    version = json.loads(out('docker', 'exec', APP, 'leapview', 'version', '--json'))
    if version['dirty']: raise RuntimeError('Dirty predecessor')
    ready()
    return {'image': image, 'revision': version['revision']}

def stage_release(image):
    releases = ROOT/'releases'
    release = releases/('sha256-'+image.split('sha256:')[1])
    # Never extract into an existing release: it may be the live current target.
    with tempfile.TemporaryDirectory(prefix='.demo-stage-', dir=releases) as directory:
        staged = Path(directory)/'payload'
        staged.mkdir(mode=0o700)
        cid = out('docker', 'create', image)
        try: run('docker', 'cp', cid+':/usr/local/share/leapview/deployment/.', str(staged))
        finally: run('docker', 'rm', cid)
        for name in ['compose.yaml', 'compose.https.yaml', 'Caddyfile', 'deployment.env.example']:
            if (staged/name).read_bytes() != (ROOT/name).read_bytes():
                raise RuntimeError('Deployment payload changed; reviewed host upgrade required: '+name)
            (staged/name).chmod(0o600)
        (staged/'leapviewctl').chmod(0o700)
        if release.exists() or release.is_symlink():
            def contents(path):
                entries = {}
                for entry in path.rglob('*'):
                    if entry.is_symlink():
                        raise RuntimeError('Existing release contains a symlink; operator review required')
                    entries[entry.relative_to(path)] = None if entry.is_dir() else entry.read_bytes()
                return entries
            if release.is_symlink() or contents(release) != contents(staged):
                raise RuntimeError('Existing release differs from image payload; operator review required')
        else:
            staged.rename(release)
    return release


@contextlib.contextmanager
def upgrade_guard():
    # Shared with demoupgrade.FileJournal. Hold this across the entire image
    # transaction so a schema upgrade cannot enter its maintenance window.
    descriptor = os.open(PROVIDER/'upgrade.lock', os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    with os.fdopen(descriptor, 'r+') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        journal = PROVIDER/'upgrade-operation.json'
        try:
            info = journal.lstat()
        except FileNotFoundError:
            yield
            return
        if not stat.S_ISREG(info.st_mode) or info.st_mode & 0o077 or info.st_size > 16384:
            raise RuntimeError('Invalid private upgrade journal; operator recovery required')
        data = json.loads(journal.read_text())
        if not isinstance(data, dict) or type(data.get('version')) is not int or data['version'] != 1:
            raise RuntimeError('Unknown upgrade journal; operator recovery required')
        state = data.get('state')
        if not isinstance(state, dict) or state.get('phase') not in ('succeeded', 'recovered'):
            raise RuntimeError('Unfinished schema upgrade; explicit recovery or commit completion required')
        yield


def main():
    os.umask(0o077)
    # Recovery needs the read-only viewer credential handoff while the journal
    # fences all runtime inspection and mutation.
    if sys.argv[1:] == ["viewer"]:
        return _main()
    with upgrade_guard():
        _main()


def _main():
    global LOG
    os.umask(0o077)
    if sys.argv[1:] == ['viewer']:
        data = json.loads((PROVIDER/'jacob-cutover-secrets.json').read_text())['Infisical']['prod:/demo/access']
        print(json.dumps({key:data[key] for key in ['DEMO_VIEWER_EMAIL','DEMO_VIEWER_PASSWORD']}))
        return
    if sys.argv[1:] == ['inspect']:
        print(json.dumps(inspect())); return
    image, revision, previous_revision = sys.argv[1:]
    if not re.fullmatch(IMAGE_RE, image) or not all(re.fullmatch(r'[0-9a-f]{40}', r) for r in [revision, previous_revision]):
        raise ValueError('Invalid immutable image/revision')
    lock = open('/run/leapview-demo-deploy.lock', 'w')
    fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    previous = inspect()
    if previous['revision'] != previous_revision: raise RuntimeError('Predecessor changed during admission')
    old = previous['image']
    original_env = (ROOT/'deployment.env').read_bytes()
    new_env = replace_image(original_env, old, image)
    if b'COMPOSE_PROJECT_NAME=leapview-cfo\n' not in original_env or b'COMPOSE_HTTPS=1\n' not in original_env:
        raise RuntimeError('Unexpected deployment layout')
    original_marker = (ROOT/'.host-install.json').read_bytes()
    previous_link = os.readlink(ROOT/'current')
    runtime_hash = hashlib.sha256((ROOT/'leapview.env').read_bytes()).hexdigest()
    if shutil.disk_usage(PROVIDER).free < 10*1024**3:
        raise RuntimeError('Insufficient free space before image pull (10 GiB reserve required)')
    run('docker', 'pull', image)
    identity = json.loads(out('docker', 'run', '--rm', image, 'version', '--json'))
    if identity['revision'] != revision or identity['dirty']: raise RuntimeError('Image identity mismatch')
    release = stage_release(image)
    volume = out('docker', 'volume', 'inspect', VOLUME, '--format', '{{.Mountpoint}}')
    db_size = int(out('docker', 'exec', POSTGRES, 'sh', '-c',
        'psql -U "$POSTGRES_USER" -d leapview_control -Atc "SELECT sum(pg_database_size(oid)) FROM pg_database WHERE datname IN (\'leapview_control\',\'leapview_ducklake\')"'))
    state_size = int(out('du', '-sb', volume).split()[0])
    if shutil.disk_usage(PROVIDER).free < 2*(db_size+state_size)+1024**3:
        raise RuntimeError('Insufficient coordinated-backup space')
    backup = PROVIDER/('compose-backup-'+datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%dT%H%M%S%fZ'))
    backup.mkdir(mode=0o700)
    # Keep recovery subprocesses independent of a disconnected SSH log pipe.
    LOG = (backup/'rollout.log').open('a', buffering=1)
    for name in ['deployment.env', 'leapview.env', '.host-install.json']: shutil.copy2(ROOT/name, backup/name)
    (backup/'previous-current.txt').write_text(previous_link+'\n')
    compose = ['docker','compose','--project-name','leapview-cfo','--project-directory',str(ROOT),
               '--env-file',str(ROOT/'deployment.env'),'-f',str(ROOT/'compose.yaml'),'-f',str(ROOT/'compose.https.yaml')]
    def start(): run(*compose, 'up','-d','--no-deps','--wait','--wait-timeout','180','leapview')
    def apply():
        run('docker', 'stop', '--time', '120', APP)
        for database in ['leapview_control', 'leapview_ducklake']:
            with (backup/(database+'.dump')).open('wb') as stream:
                run('docker','exec',POSTGRES,'sh','-c','exec pg_dump -U "$POSTGRES_USER" -Fc --dbname "$1"','backup',database,stdout=stream)
            with (backup/(database+'.dump')).open('rb') as stream:
                run('docker','exec','-i',POSTGRES,'pg_restore','--list',stdin=stream,stdout=subprocess.DEVNULL)
        with (backup/'globals.sql').open('wb') as stream:
            run('docker','exec',POSTGRES,'sh','-c','exec pg_dumpall -U "$POSTGRES_USER" --globals-only',stdout=stream)
        run('tar','-czf',str(backup/'state.tar.gz'),'-C',volume,'.')
        run('tar','-tzf',str(backup/'state.tar.gz'),stdout=subprocess.DEVNULL)
        atomic(ROOT/'deployment.env',new_env)
        link('releases/'+release.name)
        start()
    def validate():
        live = inspect()
        if live != {'image':image,'revision':revision}: raise RuntimeError('Live image identity mismatch')
        if hashlib.sha256((ROOT/'leapview.env').read_bytes()).hexdigest() != runtime_hash:
            raise RuntimeError('Runtime configuration changed')
        print('AWAITING_BROWSER_VALIDATION',flush=True)
        await_approval(sys.stdin)
        marker = json.loads(original_marker); marker['image'] = image
        atomic(ROOT/'.host-install.json',(json.dumps(marker,indent=2)+'\n').encode())
        receipt = dict(image=image,revision=revision,previousImage=old,backup=str(backup))
        atomic(PROVIDER/'compose-deployment.json',(json.dumps(receipt,indent=2)+'\n').encode())
    def rollback():
        atomic(ROOT/'deployment.env',original_env)
        atomic(ROOT/'.host-install.json',original_marker)
        link(previous_link)
        start()
        if inspect() != previous: raise RuntimeError('Predecessor recovery verification failed')
        print('Previous image restored',file=LOG)
    def interrupted(signum, frame):
        # Arm rollback on SSH hangup / runner cancellation; do not interrupt recovery twice.
        signal.signal(signal.SIGHUP, signal.SIG_IGN)
        signal.signal(signal.SIGTERM, signal.SIG_IGN)
        raise RuntimeError('Deployment interrupted before commit')
    signal.signal(signal.SIGHUP, interrupted)
    signal.signal(signal.SIGTERM, interrupted)
    transaction(apply,validate,rollback)
    print('DEPLOYMENT_COMMITTED',flush=True)

if __name__ == '__main__': main()
