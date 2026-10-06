#!/usr/bin/env python3
"""Demo-02 image-only transaction. EOF/timeout before browser approval rolls back."""
import contextlib
import datetime
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
RUN_ID_RE = re.compile(r'^[0-9]+$')
# Match hostinstall's requiredPayloadFiles. The cross-language staging test
# exercises both directions so the two generation formats cannot drift.
RUNTIME_PAYLOAD_MODES = {
    'leapviewctl': 0o700,
    'leapviewctl-wrapper': 0o700,
    'compose.yaml': 0o600,
    'compose.postgres.yaml': 0o600,
    'compose.https.yaml': 0o600,
    'compose.first-install-bootstrap.yaml': 0o600,
    'Caddyfile.first-install-bootstrap': 0o600,
    'first-install.env': 0o600,
    'Caddyfile': 0o600,
    'deployment.env.example': 0o600,
    'leapview.env.example': 0o600,
    'postgres/bundled-entrypoint.sh': 0o644,
    'postgres/bundled-init.sh': 0o644,
}

def replace_installation_image(data, old, new):
    marker = json.loads(data)
    if (not re.fullmatch(IMAGE_RE, old) or not re.fullmatch(IMAGE_RE, new) or
            marker.get('image') != old or marker.get('bootstrapPhase') != 'public' or
            marker.get('generation') != 'sha256-'+old.split('sha256:')[1]):
        raise ValueError('Image deployment requires the matching public installation marker')
    marker['image'] = new
    marker['generation'] = 'sha256-'+new.split('sha256:')[1]
    return (json.dumps(marker, indent=2)+'\n').encode()

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
    version = json.loads(out('docker', 'exec', APP, 'leapview', 'version', '--format', 'json'))
    if version['dirty']: raise RuntimeError('Dirty predecessor')
    ready()
    return {'image': image, 'revision': version['revision']}


def _private_json(path, label, maximum=1 << 20, required_mode=None):
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    try:
        info = os.fstat(descriptor)
        if (not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_size > maximum):
            raise ValueError('Invalid private '+label)
        if required_mode is not None and stat.S_IMODE(info.st_mode) != required_mode:
            raise ValueError('Invalid private '+label+' permissions')
        if info.st_mode & 0o077:
            raise ValueError('Invalid private '+label)
        with os.fdopen(os.dup(descriptor), 'rb') as stream:
            value = json.load(stream)
    finally:
        os.close(descriptor)
    if not isinstance(value, dict):
        raise ValueError('Invalid private '+label)
    return value


def _valid_utc_timestamp(value):
    if not isinstance(value, str) or not value.endswith('Z'):
        return False
    try:
        parsed = datetime.datetime.fromisoformat(value[:-1]+'+00:00')
    except ValueError:
        return False
    return parsed.utcoffset() == datetime.timedelta(0)


def _active_payload_image():
    link = ROOT/'current'
    if not link.is_symlink():
        raise ValueError('Active host payload link is missing')
    target = os.readlink(link)
    match = re.fullmatch(r'releases/sha256-([0-9a-f]{64})', target)
    if not match:
        raise ValueError('Active host payload target is invalid')
    generation = ROOT/target
    if not generation.is_dir() or generation.is_symlink():
        raise ValueError('Active host payload generation is unavailable')
    return 'ghcr.io/flidai/leapview@sha256:'+match.group(1)


def _assert_no_unfinished_maintenance_journals():
    paths = [ROOT/'upgrade-operation.json']
    history = ROOT/'upgrade-history'
    if history.is_dir():
        paths.extend(sorted(history.glob('*.json')))
    for path in paths:
        try:
            envelope = _private_json(path, 'upgrade journal', 16384)
        except FileNotFoundError:
            continue
        if envelope.get('version') != 1:
            raise ValueError('Unknown private upgrade journal version')
        state = envelope.get('state')
        if not isinstance(state, dict) or state.get('phase') not in ('succeeded', 'recovered'):
            raise ValueError('Unfinished schema upgrade blocks first-install evidence')


def _installation_record(marker, image, revision, schema, instance_id):
    _assert_no_unfinished_maintenance_journals()
    if _active_payload_image() != image:
        raise ValueError('Active payload differs from the installed image')
    value = _private_json(PROVIDER/'compose-installation.json', 'installation evidence',
                          required_mode=0o600)
    expected_fields = {
        'version', 'host', 'installationRoot', 'hostTargetId', 'instanceId', 'image',
        'revision', 'schema', 'permissionProfile', 'qualificationRunId',
        'qualificationAttempt', 'validatedAt',
    }
    if set(value) != expected_fields:
        raise ValueError('Installation evidence has an unsupported shape')
    if (value.get('version') != 'leapview-compose-installation-v1'
            or value.get('host') != 'app-leapview-demo-02'
            or value.get('installationRoot') != str(ROOT)
            or not isinstance(marker.get('targetId'), str) or not marker['targetId']
            or value.get('hostTargetId') != marker['targetId']
            or value.get('instanceId') != instance_id
            or value.get('image') != image
            or value.get('revision') != revision
            or type(value.get('schema')) is not int or value['schema'] != schema
            or not isinstance(value.get('permissionProfile'), str) or not value['permissionProfile'].strip()
            or not isinstance(value.get('qualificationRunId'), str)
            or not RUN_ID_RE.fullmatch(value['qualificationRunId'])
            or not isinstance(value.get('qualificationAttempt'), str)
            or not RUN_ID_RE.fullmatch(value['qualificationAttempt'])
            or not _valid_utc_timestamp(value.get('validatedAt'))):
        raise ValueError('Installation evidence differs from live host identity')
    return value


def _select_runtime_evidence(marker, image, revision, schema, instance_id):
    try:
        receipt = _private_json(PROVIDER/'compose-deployment.json', 'deployment receipt')
    except FileNotFoundError:
        evidence = _installation_record(marker, image, revision, schema, instance_id)
        return 'installation', None, evidence
    if receipt.get('image') != image or receipt.get('revision') != revision:
        raise ValueError('Installed deployment descriptors disagree with the running identity')
    return 'deployment', receipt, None


def _journal_for_operation(operation_id):
    paths = [ROOT/'upgrade-operation.json']
    history = ROOT/'upgrade-history'
    if history.is_dir():
        paths.extend(sorted(history.glob('*.json')))
    matches = []
    for path in paths:
        try:
            envelope = _private_json(path, 'upgrade journal', 16384)
        except FileNotFoundError:
            continue
        if envelope.get('version') != 1:
            raise ValueError('Unknown private upgrade journal version')
        state = envelope.get('state')
        if not isinstance(state, dict):
            raise ValueError('Invalid private upgrade journal state')
        identity = state.get('identity')
        if isinstance(identity, dict) and identity.get('artifactAdmissionDigest') == operation_id:
            matches.append(state)
    if len(matches) > 1:
        # A history copy and current journal may both represent the same final
        # operation after a crash during rollover. They must be byte-equivalent
        # evidence rather than two conflicting versions.
        if any(state != matches[0] for state in matches[1:]):
            raise ValueError('Conflicting durable upgrade journal evidence')
    return matches[0] if matches else None


def _operation_request(operation_id):
    digest = operation_id.removeprefix('sha256:')
    request_path = PROVIDER/'upgrade-operations'/digest/'request.json'
    request = _private_json(request_path, 'upgrade request')
    if (request.get('version') != 1 or
            request.get('candidateImage') == request.get('predecessorImage') or
            request.get('candidateRevision') == request.get('predecessorRevision')):
        raise ValueError('Invalid persisted upgrade request')
    plan = request.get('plan')
    profile = request.get('profile')
    if not isinstance(plan, dict) or not isinstance(profile, dict):
        raise ValueError('Persisted upgrade request is incomplete')
    return {
        'deploymentRunId': request.get('deploymentRunId'),
        'deploymentAttempt': request.get('deploymentAttempt'),
        'candidateImage': request.get('candidateImage'),
        'candidateRevision': request.get('candidateRevision'),
        'predecessorImage': request.get('predecessorImage'),
        'predecessorRevision': request.get('predecessorRevision'),
        'currentSchema': plan.get('currentSchema'),
        'candidateSchema': plan.get('candidateSchema'),
        'profileID': profile.get('id'),
        'profileRoot': profile.get('root'),
        'profileStateRoot': profile.get('stateRoot'),
    }


def _runtime_outcome_evidence():
    if out('hostname') != 'app-leapview-demo-02':
        raise ValueError('Unexpected target host')
    info = json.loads(out('docker', 'inspect', APP))[0]
    if (info['Config']['Labels'].get('com.docker.compose.project') != 'leapview-cfo' or
            info['Config']['Labels'].get('com.docker.compose.project.working_dir') != str(ROOT) or
            not info.get('State', {}).get('Running')):
        raise ValueError('Unexpected or stopped application container')
    if not any(m.get('Name') == VOLUME and m.get('Destination') == '/var/lib/leapview'
               for m in info.get('Mounts', [])):
        raise ValueError('Unexpected application state volume')
    image = info['Config']['Image']
    if not re.fullmatch(IMAGE_RE, image):
        raise ValueError('Runtime image must be immutable')
    image_info = json.loads(out('docker', 'image', 'inspect', image))[0]
    if info.get('Image') != image_info.get('Id') or image not in image_info.get('RepoDigests', []):
        raise ValueError('Container content does not match its immutable image reference')
    version = json.loads(out('docker', 'exec', APP, 'leapview', 'version', '--format', 'json'))
    if version.get('dirty') is not False or not re.fullmatch(r'[0-9a-f]{40}', version.get('revision', '')):
        raise ValueError('Runtime source identity is invalid')
    env = dict(v.split('=', 1) for v in info['Config']['Env'] if '=' in v)
    for key, database in [('LEAPVIEW_POSTGRES_CONTROL_URL', 'leapview_control'),
                          ('LEAPVIEW_POSTGRES_DUCKLAKE_URL', 'leapview_ducklake')]:
        url = urllib.parse.urlsplit(env[key])
        if url.hostname != POSTGRES or url.path != '/'+database:
            raise ValueError('Database binding differs from the admitted host topology')
    descriptor_lines = (ROOT/'deployment.env').read_text().splitlines()
    pins = [line.split('=', 1)[1] for line in descriptor_lines if line.startswith('LEAPVIEW_IMAGE=')]
    if pins != [image]:
        raise ValueError('Deployment descriptor differs from the running image')
    marker = _private_json(ROOT/'.host-install.json', 'installation descriptor')
    if marker.get('image') != image:
        raise ValueError('Installed deployment descriptors disagree with the running identity')
    schema_raw = out('docker', 'exec', POSTGRES, 'sh', '-c',
        'psql -U "$POSTGRES_USER" -d leapview_control -Atc "SELECT max(version_id) FROM public.goose_db_version WHERE is_applied"')
    if not re.fullmatch(r'[0-9]+', schema_raw):
        raise ValueError('Live Goose schema is unavailable')
    schema = int(schema_raw)
    instance_id = out('docker', 'exec', POSTGRES, 'sh', '-c',
        'psql -U "$POSTGRES_USER" -d leapview_control -Atc "SELECT instance_id FROM platform.instance_identity WHERE singleton_id = 1"')
    if not re.fullmatch(r'(lvinst_[A-Za-z0-9_-]{32}|instance_[0-9a-f]{32})', instance_id):
        raise ValueError('Live instance identity is unavailable')
    # A present receipt remains authoritative, including malformed and
    # mismatched receipts. Installation evidence is only a missing-file path.
    evidence_type, receipt, installation = _select_runtime_evidence(
        marker, image, version['revision'], schema, instance_id)
    return {
        'image': image,
        'revision': version['revision'],
        'containerImageID': info['Image'],
        'repositoryDigests': image_info.get('RepoDigests', []),
        'schema': schema,
        'descriptorImage': pins[0],
        'markerImage': marker.get('image'),
        'markerTargetId': marker.get('targetId'),
        'instanceId': instance_id,
        'evidenceType': evidence_type,
        'installationEvidence': installation,
        'receiptImage': receipt.get('image') if evidence_type == 'deployment' else None,
        'receiptRevision': receipt.get('revision') if evidence_type == 'deployment' else None,
        'receiptPreviousImage': receipt.get('previousImage') if evidence_type == 'deployment' else None,
        'receiptUpgradeOperation': receipt.get('upgradeOperation') if evidence_type == 'deployment' else None,
    }


def outcome(operation, operation_id):
    if operation not in ('deploy', 'upgrade', 'recover') or not re.fullmatch(r'sha256:[0-9a-f]{64}', operation_id):
        raise ValueError('Invalid runtime outcome identity')
    if os.geteuid() != 0:
        raise ValueError('Read-only outcome inspection requires root')
    # The inspection is a coherent snapshot relative to both supported host
    # mutation paths. Existing lock files are opened without creating them.
    locks = []
    try:
        for path in (ROOT/'.leapviewctl.lock', Path('/run/leapview-demo-deploy.lock')):
            try:
                fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
            except FileNotFoundError:
                continue
            try:
                fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BaseException:
                os.close(fd)
                raise
            locks.append(fd)
        if operation == 'deploy':
            _assert_no_unfinished_maintenance_journals()
        evidence = _runtime_outcome_evidence()
        if evidence['evidenceType'] == 'installation' and operation != 'deploy':
            raise ValueError('Installation evidence is only valid for deploy')
        journal = None
        request = None
        journal_state = None
        if operation in ('upgrade', 'recover'):
            journal = _journal_for_operation(operation_id)
            request = _operation_request(operation_id)
            if journal is not None:
                identity = journal.get('identity', {})
                if (identity.get('candidate') != request['candidateImage'] or
                        identity.get('predecessor') != request['predecessorImage'] or
                        identity.get('target') != request['profileID']):
                    raise ValueError('Durable journal differs from its persisted request')
                journal_state = journal.get('phase')
                wanted_operation_path = str(Path(request['profileStateRoot'])/'upgrade-operations'/operation_id[7:])
                if (journal.get('phase') in ('committed', 'succeeded') and
                        evidence['receiptUpgradeOperation'] != wanted_operation_path):
                    raise ValueError('Deployment receipt differs from the bound host operation')
        elif journal is not None:
            raise AssertionError('Image-only operation acquired a native journal')
        print(json.dumps({
            'version': 1,
            'operation': operation,
            'operationId': operation_id,
            'journalState': journal_state,
            'journal': journal,
            'request': request,
            'runtime': evidence,
        }, sort_keys=True))
    finally:
        for fd in reversed(locks):
            fcntl.flock(fd, fcntl.LOCK_UN)
            os.close(fd)

def stage_release(image):
    releases = ROOT/'releases'
    release = releases/('sha256-'+image.split('sha256:')[1])
    # Never extract into an existing release: it may be the live current target.
    with tempfile.TemporaryDirectory(prefix='.demo-stage-', dir=releases) as directory:
        packaged = Path(directory)/'payload'
        packaged.mkdir(mode=0o700)
        cid = out('docker', 'create', image)
        try: run('docker', 'cp', cid+':/usr/local/share/leapview/deployment/.', str(packaged))
        finally: run('docker', 'rm', cid)

        def contents(path):
            entries = {}
            for entry in path.rglob('*'):
                mode = entry.lstat().st_mode
                if not (stat.S_ISDIR(mode) or stat.S_ISREG(mode)):
                    raise RuntimeError('Deployment payload contains a link or special file; operator review required')
                if stat.S_ISREG(mode):
                    entries[str(entry.relative_to(path))] = entry.read_bytes()
            return entries

        complete = contents(packaged)
        if any(not complete.get(name) for name in RUNTIME_PAYLOAD_MODES):
            raise RuntimeError('Required runtime payload file is missing or empty')
        runtime = {name: complete[name] for name in RUNTIME_PAYLOAD_MODES}
        # Seed defaults belong to the candidate, not the installed topology.
        for name in ['compose.yaml', 'compose.postgres.yaml', 'compose.https.yaml',
                     'postgres/bundled-entrypoint.sh', 'postgres/bundled-init.sh',
                     'Caddyfile', 'deployment.env.example']:
            if runtime[name] != (ROOT/name).read_bytes():
                raise RuntimeError('Deployment payload changed; reviewed host upgrade required: '+name)
        if release.exists() or release.is_symlink():
            # Host upgrades stage only runtime files. Older demo deployments
            # staged the whole image payload; accept either exact layout without
            # rewriting the active generation or trusting arbitrary extras.
            if release.is_symlink() or contents(release) not in (runtime, complete):
                raise RuntimeError('Existing release differs from image payload; operator review required')
        else:
            staged = Path(directory)/'generation'
            staged.mkdir(mode=0o700)
            for name, mode in RUNTIME_PAYLOAD_MODES.items():
                path = staged/name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes(runtime[name])
                path.chmod(mode)
            staged.rename(release)
    return release


@contextlib.contextmanager
def upgrade_guard():
    # Shared with hostinstall.FileJournal. Hold this across the entire image
    # transaction so a schema upgrade cannot enter its maintenance window.
    descriptor = os.open(ROOT/'.leapviewctl.lock', os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    with os.fdopen(descriptor, 'r+') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        journal = ROOT/'upgrade-operation.json'
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
    if len(sys.argv) == 4 and sys.argv[1] == 'outcome':
        return outcome(sys.argv[2], sys.argv[3])
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
    new_marker = replace_installation_image(original_marker, old, image)
    previous_link = os.readlink(ROOT/'current')
    runtime_hash = hashlib.sha256((ROOT/'leapview.env').read_bytes()).hexdigest()
    if shutil.disk_usage(PROVIDER).free < 10*1024**3:
        raise RuntimeError('Insufficient free space before image pull (10 GiB reserve required)')
    run('docker', 'pull', image)
    identity = json.loads(out('docker', 'run', '--rm', image, 'version', '--format', 'json'))
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
        atomic(ROOT/'.host-install.json', new_marker)
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
