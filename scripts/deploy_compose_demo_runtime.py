#!/usr/bin/env python3
"""Deploy one exact qualified image to the Compose-managed hosted demo."""

import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import time
import urllib.request

REVISION = '18ff2bb555cf78a19e980ac91b4f5604c9272247'
IMAGE = 'ghcr.io/flidai/leapview@sha256:a91d0bc1927a2f093136cb403914f567d5f6927cd8b24900763783adb16175bc'
PREDECESSOR_REVISION = '38804a01c488ffaeea54202b2541779fb83354b9'
PREDECESSOR_IMAGE = 'ghcr.io/flidai/leapview@sha256:42968438532d74503b84e144b081feb2174d476f243bdf798d63715458a93a73'
ROOT = Path('/opt/leapview')
DEPLOYMENT_ENV = ROOT / 'deployment.env'
APP_ENV = ROOT / 'leapview.env'
MARKER = ROOT / '.host-install.json'
CURRENT = ROOT / 'current'
AGENT_KEY_FILE = Path('/run/leapview-demo-agent-api-key')
AGENT_BASE_URL = 'https://api.deepseek.com'
AGENT_MODEL = 'deepseek-v4-flash'
PAYLOAD_MODES = {
    'leapviewctl': 0o700,
    'compose.yaml': 0o600,
    'compose.https.yaml': 0o600,
    'Caddyfile': 0o600,
    'deployment.env.example': 0o600,
    'leapviewctl-wrapper': 0o700,
}


def output(*args, env=None):
    return subprocess.check_output(args, text=True, env=env).strip()


def write_private_atomic(path, contents):
    temporary = path.with_name('.' + path.name + '.tmp')
    temporary.write_text(contents)
    temporary.chmod(0o600)
    temporary.replace(path)


def replace_env(contents, updates):
    remaining = dict(updates)
    replaced = set()
    result = []
    for line in contents.splitlines():
        name, separator, _ = line.partition('=')
        if separator and name in updates:
            if name in replaced:
                raise RuntimeError(f'{name} must occur at most once')
            replaced.add(name)
            value = remaining.pop(name)
            if any(character in value for character in '\r\n'):
                raise RuntimeError(f'{name} must be a single-line value')
            result.append(f'{name}={value}')
        else:
            result.append(line)
    for name, value in remaining.items():
        if any(character in value for character in '\r\n'):
            raise RuntimeError(f'{name} must be a single-line value')
        result.append(f'{name}={value}')
    return '\n'.join(result) + '\n'


def env_value(contents, name):
    values = []
    for line in contents.splitlines():
        key, separator, value = line.partition('=')
        if separator and key == name:
            values.append(value)
    if len(values) != 1:
        raise RuntimeError(f'{name} must occur exactly once')
    return values[0]


def app_container():
    containers = output(
        'docker', 'ps',
        '--filter', 'label=com.docker.compose.project=leapview',
        '--filter', 'label=com.docker.compose.service=leapview',
        '--format', '{{.ID}}',
    ).splitlines()
    if len(containers) != 1:
        raise RuntimeError(f'expected one running LeapView container, found {len(containers)}')
    return containers[0]


def running_identity(container):
    return json.loads(output('docker', 'exec', container, 'leapview', 'version', '--json'))


def await_ready():
    for _ in range(60):
        try:
            with urllib.request.urlopen('https://demo.leapview.dev/readyz', timeout=5) as response:
                if response.status == 200 and json.load(response).get('status') == 'ready':
                    return
        except Exception:
            pass
        time.sleep(2)
    raise RuntimeError('hosted demo did not become ready within two minutes')


def start():
    environment = os.environ.copy()
    environment['LEAPVIEWCTL_ROOT'] = str(ROOT)
    subprocess.run(['/usr/local/sbin/leapviewctl', 'start'], check=True, env=environment)


def stage_payload():
    generation = IMAGE.rsplit('@', 1)[1].replace(':', '-')
    destination = ROOT / 'releases' / generation
    container = output('docker', 'create', IMAGE)
    try:
        with tempfile.TemporaryDirectory(dir=ROOT / 'releases', prefix='.generation-') as temporary_name:
            temporary = Path(temporary_name)
            temporary.chmod(0o700)
            subprocess.run([
                'docker', 'cp', f'{container}:/usr/local/share/leapview/deployment/.', str(temporary)
            ], check=True)
            for name, mode in PAYLOAD_MODES.items():
                source = temporary / name
                if not source.is_file() or source.is_symlink() or source.stat().st_size == 0:
                    raise RuntimeError(f'qualified image payload is missing {name}')
                source.chmod(mode)
            if destination.is_symlink():
                raise RuntimeError('target generation must not be a symlink')
            if destination.exists():
                if not destination.is_dir():
                    raise RuntimeError('target generation must be a directory')
                for name, mode in PAYLOAD_MODES.items():
                    existing = destination / name
                    if (not existing.is_file() or existing.is_symlink() or
                            existing.read_bytes() != (temporary / name).read_bytes() or
                            existing.stat().st_mode & 0o777 != mode):
                        raise RuntimeError('existing target generation differs from qualified image')
            else:
                temporary.rename(destination)
    finally:
        subprocess.run(['docker', 'rm', container], check=False, stdout=subprocess.DEVNULL)
    return generation


def activate(generation):
    temporary = ROOT / ('.current-' + generation)
    temporary.unlink(missing_ok=True)
    temporary.symlink_to(Path('releases') / generation)
    temporary.replace(CURRENT)


def main():
    if os.geteuid() != 0:
        raise RuntimeError('Compose demo rollout must run as root')
    os.umask(0o077)
    for path in (DEPLOYMENT_ENV, APP_ENV, MARKER, CURRENT, ROOT / 'releases'):
        if not path.exists():
            raise RuntimeError(f'Compose-managed demo path is missing: {path}')
    if not CURRENT.is_symlink():
        raise RuntimeError('active deployment generation is not a symlink')

    agent_key = AGENT_KEY_FILE.read_text()
    AGENT_KEY_FILE.unlink()
    if not agent_key or any(character in agent_key for character in '\r\n'):
        raise RuntimeError('invalid agent provider key')

    subprocess.run(['docker', 'pull', IMAGE], check=True)
    digests = json.loads(output('docker', 'image', 'inspect', IMAGE, '--format', '{{json .RepoDigests}}'))
    if IMAGE not in digests:
        raise RuntimeError('pulled image does not expose the admitted digest')
    label_revision = output(
        'docker', 'image', 'inspect', IMAGE,
        '--format', '{{index .Config.Labels "org.opencontainers.image.revision"}}',
    )
    if label_revision != REVISION:
        raise RuntimeError('pulled image revision label differs from the admitted revision')

    container = app_container()
    current_image = output('docker', 'inspect', container, '--format', '{{.Config.Image}}')
    current_identity = running_identity(container)
    allowed = {
        (PREDECESSOR_IMAGE, PREDECESSOR_REVISION),
        (IMAGE, REVISION),
    }
    if (current_image, current_identity.get('revision')) not in allowed or current_identity.get('dirty') is not False:
        raise RuntimeError('running demo is not the reviewed predecessor or target')
    deployment_contents = DEPLOYMENT_ENV.read_text()
    if env_value(deployment_contents, 'LEAPVIEW_IMAGE') != current_image:
        raise RuntimeError('Compose deployment image differs from the running container')

    marker = json.loads(MARKER.read_text())
    if marker.get('image') != current_image or not marker.get('targetId'):
        raise RuntimeError('installed host marker differs from the running container')

    generation = stage_payload()
    previous_generation = os.readlink(CURRENT)
    previous_path = Path(previous_generation)
    if (previous_path.parent != Path('releases') or not previous_path.name or
            '/' in previous_path.name or '\\' in previous_path.name):
        raise RuntimeError('active deployment generation has an unexpected target')
    backup = ROOT / 'rollbacks' / (time.strftime('%Y%m%dT%H%M%SZ', time.gmtime()) + '-' + REVISION[:12])
    backup.mkdir(parents=True, mode=0o700)
    app_contents = APP_ENV.read_text()
    for path in (DEPLOYMENT_ENV, MARKER):
        shutil.copy2(path, backup / path.name)
    (backup / 'previous-generation.txt').write_text(previous_generation + '\n')
    (backup / 'previous-generation.txt').chmod(0o600)

    changed = False
    try:
        # Arm rollback before the first durable mutation. A write or activation
        # failure must restore the complete predecessor configuration.
        changed = True
        write_private_atomic(APP_ENV, replace_env(app_contents, {
            'LEAPVIEW_AGENT_API_KEY': agent_key,
            'LEAPVIEW_AGENT_BASE_URL': AGENT_BASE_URL,
            'LEAPVIEW_AGENT_MODEL': AGENT_MODEL,
        }))
        agent_key = ''
        write_private_atomic(DEPLOYMENT_ENV, replace_env(deployment_contents, {'LEAPVIEW_IMAGE': IMAGE}))
        marker['image'] = IMAGE
        write_private_atomic(MARKER, json.dumps(marker, indent=2) + '\n')
        activate(generation)
        start()
        await_ready()

        container = app_container()
        if output('docker', 'inspect', container, '--format', '{{.Config.Image}}') != IMAGE:
            raise RuntimeError('restarted demo is not using the admitted image')
        identity = running_identity(container)
        if identity.get('revision') != REVISION or identity.get('dirty') is not False:
            raise RuntimeError('restarted demo build identity differs from the admitted revision')
        names = output('docker', 'exec', container, 'sh', '-c',
                       "test -n \"$LEAPVIEW_AGENT_API_KEY\" && "
                       "test \"$LEAPVIEW_AGENT_BASE_URL\" = 'https://api.deepseek.com' && "
                       "test \"$LEAPVIEW_AGENT_MODEL\" = 'deepseek-v4-flash' && printf configured")
        if names != 'configured':
            raise RuntimeError('agent provider configuration was not installed')
        (backup / 'rollout-success.json').write_text(json.dumps({
            'revision': REVISION, 'image': IMAGE, 'predecessorRevision': current_identity['revision']
        }) + '\n')
        (backup / 'rollout-success.json').chmod(0o600)
        print(f'Deployed exact qualified Compose image {IMAGE}; readiness and agent configuration passed')
    except BaseException:
        if changed:
            write_private_atomic(DEPLOYMENT_ENV, (backup / DEPLOYMENT_ENV.name).read_text())
            write_private_atomic(APP_ENV, app_contents)
            write_private_atomic(MARKER, (backup / MARKER.name).read_text())
            rollback_link = ROOT / '.current-rollback'
            rollback_link.unlink(missing_ok=True)
            rollback_link.symlink_to(previous_generation)
            rollback_link.replace(CURRENT)
            start()
            await_ready()
            print('Compose rollout failed; reviewed predecessor configuration was restored', flush=True)
        raise


if __name__ == '__main__':
    main()
