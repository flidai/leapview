#!/usr/bin/env python3
"""Manual production admission and supervised Kamal operations over pinned SSH."""
import argparse
import base64
import contextlib
import stat
import uuid
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import subprocess
import tempfile
import time
from urllib.request import urlopen

from contract import REPOSITORY, RUNTIME, SERVICE, validate_record
import nix_admission

HERE = Path(__file__).resolve().parent
HOST = '100.73.220.23'


def run(args, *, data=None, env=None):
    p = subprocess.run(args, input=data, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=900)
    if p.returncode == 255 and args[0] == 'ssh':
        raise ConnectionError('SSH result uncertain; stop and reconcile the owned attempt')
    if p.returncode:
        raise RuntimeError(shlex.join(args[:3]) + ' failed: ' + (p.stdout + p.stderr).decode(errors='replace')[-3000:])
    return p.stdout


def connect(directory):
    key = os.environ.get('SITE_SSH_KEY')
    if not key:
        key = str(directory / 'identity')
        Path(key).write_text(os.environ.pop('SITE_SSH_PRIVATE_KEY'))
        Path(key).chmod(0o600)
    scanned = run(['ssh-keyscan', '-T', '10', HOST]).decode().splitlines()
    expected = (HERE.parent / 'hetzner-site/ssh-host-key.sha256').read_text().strip()
    match = None
    for line in scanned:
        if not line or line.startswith('#'): continue
        candidate = directory / 'scanned-key'; candidate.write_text(line + '\n')
        if run(['ssh-keygen', '-lf', str(candidate)]).decode().split()[1] == expected:
            match = line; break
    if not match: raise ValueError('SSH host key does not match reviewed fingerprint')
    known = directory / 'known_hosts'; known.write_text(match + '\n')
    config = directory / 'ssh_config'
    config.write_text(f'Host {HOST}\n  User root\n  IdentityFile {json.dumps(str(Path(key).resolve()))}\n'
                      f'  IdentitiesOnly yes\n  BatchMode yes\n  ConnectTimeout 10\n  ServerAliveInterval 5\n  ServerAliveCountMax 2\n  StrictHostKeyChecking yes\n'
                      f'  UserKnownHostsFile {json.dumps(str(known))}\n')
    os.environ['SITE_SSH_CONFIG'] = str(config)


@contextlib.contextmanager
def ownership():
    attempt = uuid.uuid4().hex
    source = (HERE / 'supervisor.py').read_text()
    command = shlex.join(['python3', '-c', source, 'serve', attempt])
    process = subprocess.Popen(['ssh', '-F', os.environ['SITE_SSH_CONFIG'], 'root@' + HOST, command],
                               stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    try:
        line = process.stdout.readline()
        if not line or json.loads(line).get('attempt') != attempt:
            raise RuntimeError('exclusive ownership refused: ' + process.stderr.read().decode()[-1500:])
        os.environ['SITE_ATTEMPT'] = attempt
        os.environ['SITE_SUPERVISOR_SOURCE'] = base64.b64encode(source.encode()).decode()
        yield process
        if process.stdin.closed or process.poll() is not None: raise RuntimeError('lock connection lost; explicit recovery required')
        process.stdin.write(b'finish\n'); process.stdin.flush()
        process.stdin.close()
        if process.wait(timeout=30): raise RuntimeError('ownership completion failed')
    finally:
        os.environ.pop('SITE_ATTEMPT', None)
        if not process.stdin.closed: process.stdin.close()
        # On failure EOF revokes the owner. Do not kill surviving host work or
        # release Kamal locks: owner.json remains for explicit reconciliation.
        process.stdout.close(); process.stderr.close()


def remote(operation, **values):
    source = (HERE / 'contract.py').read_text() + '\n' + '\n'.join(
        line for line in (HERE / 'host.py').read_text().splitlines() if not line.startswith('from contract import '))
    payload = base64.b64encode(json.dumps({'operation': operation, **values}).encode()).decode()
    source = 'import io,base64,sys; sys.stdin=io.StringIO(base64.b64decode(' + repr(payload) + ').decode())\n' + source
    command = shlex.join(['python3', '-c', source])
    if os.environ.get('SITE_ATTEMPT'):
        command = shlex.join(['python3', '-c', (HERE / 'supervisor.py').read_text(), 'exec', os.environ['SITE_ATTEMPT'], command])
    return json.loads(run(['ssh', '-F', os.environ['SITE_SSH_CONFIG'], 'root@' + HOST, command],
                          data=None))


def manifest(reference):
    raw = run(['docker', 'buildx', 'imagetools', 'inspect', '--raw', reference])
    return json.loads(raw), 'sha256:' + hashlib.sha256(raw).hexdigest()


def admitted_record(admission, release):
    attestation = admission['attestation']
    ref, digest = admission['image'], admission['digest']
    if (ref != REPOSITORY + '@' + digest or admission['registryDigest'] != digest
            or attestation['verified'] is not True or attestation['repository'] != 'flidai/leapview'
            or attestation['workflow'] != 'flidai/leapview/.github/workflows/site-image.yml'
            or admission['vulnerabilityPolicy']['passed'] is not True or admission['sbom']['discoverable'] is not True):
        raise ValueError('matching successful production admission required')
    index, actual = manifest(ref)
    if actual != digest: raise ValueError('registry index differs from admission')
    platforms = [m['digest'] for m in index['manifests']
                 if m.get('platform', {}).get('architecture') == 'amd64' and m.get('platform', {}).get('os') == 'linux']
    if len(platforms) != 1: raise ValueError('one amd64 platform required')
    platform, actual = manifest(REPOSITORY + '@' + platforms[0])
    if actual != platforms[0]: raise ValueError('platform manifest identity mismatch')
    return validate_record({'schema': 1, 'version': 'k' + digest.split(':')[1], 'image': ref,
        'revision': attestation['sourceRevision'], 'platform': actual, 'config': platform['config']['digest'],
        'runtime': RUNTIME, 'kamal': '2.12.0', 'admission': admission,
        'release': release, 'compressed_bytes': sum(layer['size'] for layer in platform['layers']) + platform['config']['size']})


def admission_environment():
    environment = os.environ.copy()
    if not any(environment.get(name, '').strip() for name in ('GH_TOKEN', 'GITHUB_TOKEN')):
        token = run(['gh', 'auth', 'token']).decode().strip()
        if not token:
            raise ValueError('authenticated GitHub CLI or explicit token required for live admission')
        environment['GH_TOKEN'] = token
    return environment


def prepare(directory, reference):
    if not re.fullmatch(re.escape(REPOSITORY) + r'@sha256:[a-f0-9]{64}', reference):
        raise ValueError('immutable production repository required; trial images are not admitted')
    # Discover the revision from the immutable image; live provenance must bind it.
    description = json.loads(run(['docker', 'buildx', 'imagetools', 'inspect', reference,
                                 '--format', '{{json .Image}}']))
    if 'linux/amd64' in description: description = description['linux/amd64']
    if description['config'].get('Labels', {}).get('service') != SERVICE:
        raise ValueError('production image is missing the required service ownership label')
    revision = description['config']['Labels']['org.opencontainers.image.revision']
    if not re.fullmatch('[a-f0-9]{40}', revision): raise ValueError('invalid image revision')
    runs = json.loads(run(['gh', 'api', 'repos/flidai/leapview/actions/workflows/site-deploy.yml/runs?head_sha=' + revision + '&status=success&per_page=100']))
    eligible = [r for r in runs['workflow_runs'] if r['head_sha'] == revision and r['head_branch'] == 'main'
                and r['event'] in ('push', 'workflow_dispatch') and r['conclusion'] == 'success'
                and r['repository']['full_name'] == 'flidai/leapview']
    if not eligible: raise ValueError('successful production main workflow required')
    selected = eligible[0]
    evidence = directory / 'workflow'; evidence.mkdir()
    run(['gh', 'run', 'download', str(selected['id']), '--repo', 'flidai/leapview',
         '--name', 'public-site-image-' + revision, '--dir', str(evidence)])
    if (evidence / 'site-image-reference.txt').read_text().strip() != reference:
        raise ValueError('successful production qualification does not identify this image')
    admission_path = directory / 'admission.json'
    run(['go', 'run', './internal/app/tools/ociadmission', '--image', reference, '--repository', REPOSITORY,
         '--expected-workflow', 'flidai/leapview/.github/workflows/site-image.yml', '--source-revision', revision,
         '--policy', '.github/security/container-vulnerability-policy.json', '--platform', 'linux/amd64',
         '--mode', 'live', '--output', str(admission_path)], env=admission_environment())
    release = json.loads(run(['gh', 'api', 'repos/flidai/leapview/contents/docs/public-release.json?ref=' + revision,
                              '-H', 'Accept: application/vnd.github.raw+json']))
    record = admitted_record(json.loads(admission_path.read_text()), release)
    record['qualification_run'] = selected['html_url']
    return record


def read_record(path):
    st = path.lstat()
    if not stat.S_ISREG(st.st_mode) or st.st_uid != os.getuid() or st.st_mode & 0o077:
        raise ValueError('prepared record must be owned by the operator with mode 0600')
    return validate_record(json.loads(path.read_text()))


def prepare_nix(directory, reference, selection):
    nix_admission.validate_selection(selection)
    if not re.fullmatch(re.escape(REPOSITORY) + r'@sha256:[a-f0-9]{64}', reference):
        raise ValueError('immutable production repository required for Nix site admission')
    admission, digest, artifact_digest = nix_admission.authenticate(directory, reference, selection, run)
    nix_admission.validate_receipt(admission, reference, selection)
    platform, actual = manifest(reference)
    if (actual != admission['ociDigest'] or platform.get('schemaVersion') != 2
            or 'manifests' in platform or not isinstance(platform.get('layers'), list)
            or not re.fullmatch(r'sha256:[a-f0-9]{64}', platform.get('config', {}).get('digest', ''))):
        raise ValueError('Nix site registry manifest differs from the admitted single-platform image')
    release = json.loads(run(['gh', 'api', 'repos/flidai/leapview/contents/docs/public-release.json?ref=' +
        selection['sourceRevision'], '-H', 'Accept: application/vnd.github.raw+json']))
    return validate_record({'schema': 1, 'version': 'k' + actual.split(':')[1], 'image': reference,
        'revision': selection['sourceRevision'], 'platform': actual, 'config': platform['config']['digest'],
        'runtime': RUNTIME, 'kamal': '2.12.0', 'admission': admission, 'release': release,
        'compressed_bytes': sum(layer['size'] for layer in platform['layers']) + platform['config']['size'],
        'qualification_run': 'https://github.com/flidai/leapview/actions/runs/' + str(selection['runId']),
        'nixAdmission': {**selection, 'admissionDigest': digest, 'artifactDigest': artifact_digest}})


def configure(directory, record):
    validate_record(record)
    config = {'service': 'leapview-site', 'image': 'leapview-site', 'minimum_version': '2.12.0',
        'servers': {'web': {'hosts': [HOST], 'cmd': '-addr=:8081 -image-reference=' + record['image'],
            'options': {'user': RUNTIME['user'], 'pull': 'never', 'read-only': True, 'cap-drop': 'ALL',
                        'security-opt': 'no-new-privileges=true', 'tmpfs': '/tmp:rw,noexec,nosuid,size=64m'}}},
        'registry': {'server': 'localhost:5555'},
        'builder': {'arch': 'amd64'}, 'ssh': {'user': 'root', 'config': [os.environ['SITE_SSH_CONFIG']], 'keys_only': True},
        'proxy': {'app_port': 8081, 'host': 'leapview.dev', 'forward_headers': True,
                  'healthcheck': {'path': '/readyz', 'interval': 2, 'timeout': 5},
                  'run': {'version': 'v0.9.2', 'publish': False}},
        'retain_containers': 1, 'hooks_path': str(directory / 'hooks'),
        'deploy_timeout': 60, 'drain_timeout': 30, 'stop_timeout': 30,
        'logging': {'driver': 'json-file', 'options': {'max-size': '10m', 'max-file': '3'}},
        'env': {'clear': {'LEAPVIEW_SITE_BASE_URL': RUNTIME['base_url']}}}
    path = directory / 'deploy.json'; path.write_text(json.dumps(config))
    hooks = directory / 'hooks'; hooks.mkdir(exist_ok=True)
    return path


def kamal(config, *args):
    os.environ['BUNDLE_GEMFILE'] = str(HERE / 'Gemfile')
    return run(['bundle', 'exec', 'ruby', '-r', str(HERE / 'guard.rb'), '-S', 'kamal', *args, '-c', str(config)])


def public_check(record):
    error = None
    for _ in range(12):
        try:
            with urlopen('https://leapview.dev/build.json', timeout=10) as response:
                build = json.load(response)
            if build.get('revision') != record['revision'] or build.get('image') != record['image']:
                raise ValueError('public image/source identity differs')
            for path in ('/healthz', '/readyz'):
                with urlopen('https://leapview.dev' + path, timeout=10) as response:
                    if response.status != 200: raise ValueError('public acceptance failed')
            with urlopen('https://leapview.dev/release.json', timeout=10) as response:
                if json.load(response) != record['release']: raise ValueError('release metadata differs from version record')
            with urlopen('https://leapview.dev/docs/installation', timeout=10) as response:
                documentation = response.read().decode()
            required = [record['release'][key] for key in ('version', 'tag', 'revision', 'image', 'releaseUrl')]
            for artifact in record['release']['artifacts']:
                required.extend([artifact['archiveUrl'], artifact['checksumUrl']])
            if any(value not in documentation for value in required):
                raise ValueError('installation docs/download links differ from saved release metadata')
            with urlopen('https://www.leapview.dev/', timeout=10) as response:
                if response.url != 'https://leapview.dev/': raise ValueError('www redirect differs')
                html = response.read().decode()
            assets = sorted(set(re.findall(r'(?:src|href)="(/[^"?]+\.(?:css|js))(?:\?[^" ]*)?"', html)))
            if not assets: raise ValueError('no site CSS/JS assets found')
            for asset in assets[:32]:
                if asset.startswith('//'): raise ValueError('nonlocal site asset')
                with urlopen('https://leapview.dev' + asset, timeout=10) as response:
                    if response.status != 200: raise ValueError('site asset failed')
            return
        except Exception as exc:
            error = exc; time.sleep(2)
    raise RuntimeError('public acceptance failed') from error


def restore_active(config, record):
    """Verify the restored route, stop only Kamal-stale containers, then clear pending."""
    remote('verify', version=record['version']); public_check(record)
    remote('stale-ready', version=record['version'])
    kamal(config, 'app', 'stale_containers', '--stop')
    remote('verify', version=record['version']); public_check(record)
    remote('restored', version=record['version'])


def deploy(directory, path):
    record = read_record(path)
    with ownership():
        status = remote('preflight', record=record)
        previous = status['state']['records'][status['state']['active']]
        if record['version'] == previous['version']:
            saved = {k: v for k, v in previous.items() if k not in ('local_id', 'verified')}
            if record != saved: raise ValueError('prepared record differs from verified active record')
            remote('verify', version=previous['version']); public_check(previous)
            print('Already running the exact verified image; no deployment or pruning needed'); return
        # JSON alone is never admission authority. Repeat live verification for
        # a new candidate, then bind every prepared field before host mutation.
        if 'nixAdmission' in record:
            selection = {name: record['nixAdmission'][name] for name in nix_admission.SELECTION_FIELDS}
            verified = prepare_nix(directory, record['image'], selection)
        else:
            verified = prepare(directory, record['image'])
        if record != verified: raise ValueError('prepared record differs from live admission; prepare again')
        remote('begin', record=record)
        transition(directory, record, previous, pull=True)


def transition(directory, record, previous, *, pull):
    config = configure(directory, record)
    if pull:
        # Pull failures may leave daemon work unresolved. Never switch or prune
        # in that failure path; keep pending ownership for explicit recovery.
        remote('pull', version=record['version'])
        remote('image', version=record['version'])
    try:
        if pull:
            kamal(config, 'app', 'boot', '--version', record['version'])
        else:
            remote('image', version=record['version'])
            kamal(config, 'rollback', record['version'])
        remote('verify', version=record['version']); public_check(record)
        remote('accept', version=record['version'])
    except (ConnectionError, subprocess.TimeoutExpired):
        raise
    except Exception:
        observed = remote('state')
        if observed['active'] != previous['version'] or observed.get('pending') != record['version']:
            raise RuntimeError('acceptance outcome changed or is uncertain; inspect host state before recovery')
        # A failed recovery deliberately leaves pending state, blocking new pulls.
        config = configure(directory, previous)
        remote('image', version=previous['version'])
        kamal(config, 'rollback', previous['version'])
        restore_active(config, previous)
        remote('cleanup'); kamal(config, 'prune', 'all'); remote('maintained')
        raise
    # Maintenance errors after acceptance must report the new version as live;
    # they do not silently roll it back or authorize another pull.
    remote('preserve-prior')
    remote('cleanup'); kamal(config, 'prune', 'all'); remote('maintained')
    remote('verify', version=record['version']); public_check(record)
    print('Public site accepted; retained verified rollback and completed maintenance:', record['image'])


def recover(directory):
    # Ownership-journal reconciliation remains an explicit operator prerequisite.
    # No admission, pull, or acceptance of an interrupted candidate occurs here.
    state = remote('recovery-begin')
    record = state['records'][state['active']]
    config = configure(directory, record)
    kamal(config, 'rollback', record['version'])
    restore_active(config, record)
    remote('preserve-prior')
    remote('cleanup'); kamal(config, 'prune', 'all'); remote('maintained')


def main():
    p = argparse.ArgumentParser()
    p.add_argument('operation', choices=['status', 'prepare', 'prepare-nix', 'deploy', 'rollback', 'recover', 'maintain'])
    p.add_argument('--image')
    p.add_argument('--record', type=Path)
    p.add_argument('--admission-run-id', type=int)
    p.add_argument('--admission-run-attempt', type=int)
    p.add_argument('--admission-artifact-id', type=int)
    p.add_argument('--source-revision')
    p.add_argument('--producer-revision')
    args = p.parse_args()
    if args.operation == 'deploy' and not args.record: p.error('--record required')
    with tempfile.TemporaryDirectory(prefix='leapview-site-') as tmp:
        directory = Path(tmp)
        if args.operation in ('prepare', 'prepare-nix'):
            if not args.image: p.error('--image required')
            if args.operation == 'prepare-nix':
                selection = {'runId': args.admission_run_id, 'runAttempt': args.admission_run_attempt,
                    'artifactId': args.admission_artifact_id, 'sourceRevision': args.source_revision,
                    'producerRevision': args.producer_revision}
                try:
                    nix_admission.validate_selection(selection)
                except ValueError as error:
                    p.error(str(error))
                record = prepare_nix(directory, args.image, selection)
            else:
                if any(value is not None for value in (args.admission_run_id, args.admission_run_attempt,
                        args.admission_artifact_id, args.source_revision, args.producer_revision)):
                    p.error('Nix admission inputs require prepare-nix')
                record = prepare(directory, args.image)
            output = args.record or Path.home() / '.local/state/leapview-site' / (record['version'] + '.json')
            output.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
            fd = os.open(output, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
            with os.fdopen(fd, 'w') as stream:
                json.dump(record, stream, indent=2); stream.flush(); os.fsync(stream.fileno())
            print(output); return
        connect(directory)
        if args.operation == 'status': print(json.dumps(remote('status'), indent=2))
        elif args.operation == 'deploy':
            if not args.record: p.error('--record required')
            deploy(directory, args.record)
        else:
            with ownership():
                if args.operation == 'rollback':
                    state = remote('rollback-begin')
                    transition(directory, state['records'][state['prior']], state['records'][state['active']], pull=False)
                elif args.operation == 'recover':
                    recover(directory)
                else:
                    state = remote('state')
                    record = state['records'][state['active']]
                    remote('verify', version=record['version']); public_check(record)
                    remote('maintenance-begin')
                    state = remote('state')
                    if state.get('maintenance_pending'): remote('preserve-prior')
                    remote('cleanup'); kamal(configure(directory, record), 'prune', 'all'); remote('maintained')


if __name__ == '__main__': main()
