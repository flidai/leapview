#!/usr/bin/env python3
"""Serialized runner entrypoint. Kamal owns pulling, proxy switching and pruning."""
import argparse
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

from contract import REPOSITORY, RUNTIME, activation_allowed, validate_record

HERE = Path(__file__).resolve().parent
HOST = '100.73.220.23'


def run(args, *, data=None):
    p = subprocess.run(args, input=data, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=300)
    if p.returncode:
        raise RuntimeError(shlex.join(args[:3]) + ' failed: ' + p.stderr.decode(errors='replace')[-1500:])
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
                      f'  IdentitiesOnly yes\n  BatchMode yes\n  ConnectTimeout 10\n  StrictHostKeyChecking yes\n'
                      f'  UserKnownHostsFile {json.dumps(str(known))}\n')
    os.environ['SITE_SSH_CONFIG'] = str(config)


def remote(operation, **values):
    source = (HERE / 'contract.py').read_text() + '\n' + '\n'.join(
        line for line in (HERE / 'host.py').read_text().splitlines() if not line.startswith('from contract import '))
    command = shlex.join(['python3', '-c', source])
    return json.loads(run(['ssh', '-F', os.environ['SITE_SSH_CONFIG'], 'root@' + HOST, command],
                          data=json.dumps({'operation': operation, **values}).encode()))


def manifest(reference):
    raw = run(['docker', 'buildx', 'imagetools', 'inspect', '--raw', reference])
    return json.loads(raw), 'sha256:' + hashlib.sha256(raw).hexdigest()


def admitted_record(path):
    admission = json.loads(path.read_text())
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
        'release': json.loads((HERE.parents[1] / 'docs/public-release.json').read_text())})


def configure(directory, record):
    validate_record(record)
    config = {'service': 'leapview-site', 'image': 'flidai/leapview-site', 'minimum_version': '2.12.0',
        'servers': {'web': {'hosts': [HOST], 'cmd': '-addr=:8081 -image-reference=' + record['image'],
            'options': {'user': RUNTIME['user'], 'read-only': True, 'cap-drop': 'ALL',
                        'security-opt': 'no-new-privileges=true', 'tmpfs': '/tmp:rw,noexec,nosuid,size=64m'}}},
        'registry': {'server': 'ghcr.io', 'username': 'unused-public-pull', 'password': 'unused-public-pull'},
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
    hook = hooks / 'pre-app-boot'
    hook.write_text('#!/bin/sh\nset -eu\nexec ' + shlex.join(['python3', str(Path(__file__).resolve()), 'verify-image'])
                    + ' --version "$KAMAL_VERSION"\n')
    hook.chmod(0o700)
    return path


def kamal(config, *args):
    os.environ['BUNDLE_GEMFILE'] = str(HERE / 'Gemfile')
    run(['bundle', 'exec', 'kamal', *args, '-c', str(config)])


def public_check(record):
    error = None
    for _ in range(12):
        try:
            with urlopen('https://leapview.dev/build.json', timeout=10) as response:
                build = json.load(response)
            if build.get('revision') != record['revision'] or build.get('image') != record['image']:
                raise ValueError('public image/source identity differs')
            for path in ('/healthz', '/readyz', '/docs/installation'):
                with urlopen('https://leapview.dev' + path, timeout=10) as response:
                    if response.status != 200: raise ValueError('public acceptance failed')
            with urlopen('https://leapview.dev/release.json', timeout=10) as response:
                if json.load(response) != record['release']: raise ValueError('release metadata differs from version record')
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


def deploy(directory, admission):
    status = remote('preflight')
    record = admitted_record(admission)
    previous = status['state']['records'][status['state']['active']]
    if record['version'] == previous['version']:
        remote('verify', version=previous['version']); public_check(previous)
        print('Already running the exact verified image; no deployment or pruning needed'); return
    tag = REPOSITORY + ':' + record['version']
    run(['docker', 'buildx', 'imagetools', 'create', '--tag', tag, record['image']])
    if manifest(tag)[1] != record['image'].split('@')[1]: raise ValueError('version tag mapping changed')
    remote('begin', record=record)
    transition(directory, record, previous, pull=True)


def transition(directory, record, previous, *, pull):
    config = configure(directory, record)
    try:
        if pull:
            kamal(config, 'redeploy', '--skip-push', '--version', record['version'])
        else:
            remote('image', version=record['version'])
            kamal(config, 'rollback', record['version'])
        remote('verify', version=record['version']); public_check(record)
        remote('accept', version=record['version'])
    except Exception:
        observed = remote('state')
        if observed['active'] != previous['version'] or observed.get('pending') != record['version']:
            raise RuntimeError('acceptance outcome changed or is uncertain; inspect host state before recovery')
        # A failed recovery deliberately leaves pending state, blocking new pulls.
        config = configure(directory, previous)
        remote('image', version=previous['version'])
        kamal(config, 'rollback', previous['version'])
        remote('verify', version=previous['version']); public_check(previous)
        remote('restored', version=previous['version'])
        remote('cleanup'); kamal(config, 'prune', 'all'); remote('maintained')
        raise
    # Maintenance errors after acceptance must report the new version as live;
    # they do not silently roll it back or authorize another pull.
    remote('cleanup'); kamal(config, 'prune', 'all'); remote('maintained')
    remote('verify', version=record['version']); public_check(record)
    print('Public site accepted; retained verified rollback and completed maintenance:', record['image'])


def main():
    p = argparse.ArgumentParser()
    p.add_argument('operation', choices=['access-check', 'deploy', 'rollback', 'verify-image'])
    p.add_argument('--admission', type=Path)
    p.add_argument('--version')
    args = p.parse_args()
    if args.operation != 'access-check' and not activation_allowed(os.environ.get('LEAPVIEW_SITE_DEPLOYMENT_MODE'),
            os.environ.get('GITHUB_REPOSITORY'), os.environ.get('GITHUB_REF')):
        raise SystemExit('Production activation is disabled')
    if args.operation == 'verify-image':
        remote('image', version=args.version); return
    with tempfile.TemporaryDirectory(prefix='leapview-site-') as tmp:
        directory = Path(tmp)
        connect(directory)
        if args.operation == 'access-check': print(json.dumps(remote('inventory'), indent=2))
        elif args.operation == 'rollback':
            state = remote('rollback-begin')
            transition(directory, state['records'][state['prior']], state['records'][state['active']], pull=False)
        else:
            if not args.admission: p.error('--admission required')
            deploy(directory, args.admission)


if __name__ == '__main__': main()
