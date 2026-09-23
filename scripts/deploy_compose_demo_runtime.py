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

REVISION = '2caaf4d0c3e0ce637a22c376f63240c27dfaf20d'
IMAGE = 'ghcr.io/flidai/leapview@sha256:35d1207a312279cc7bcf3c64a9284a8410d1f21c30920f2cbf1935113553eb24'
PREDECESSOR_REVISION = '5a50b4c4d065278172c9779b98abb094215014c2'
PREDECESSOR_IMAGE = 'ghcr.io/flidai/leapview@sha256:a24ef9fc224f4366b232938158f13c2665a069fc8f467f8856c6e183d76bf7eb'
ROOT = Path('/opt/leapview')
DEPLOYMENT_ENV = ROOT / 'deployment.env'
APP_ENV = ROOT / 'leapview.env'
MARKER = ROOT / '.host-install.json'
CURRENT = ROOT / 'current'
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
        '--filter', 'label=com.docker.compose.project=leapview-cfo',
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
    mounts = json.loads(output('docker', 'inspect', container, '--format', '{{json .Mounts}}'))
    if not any(mount.get('Name') == 'leapview-cfo_leapview-state' and
               mount.get('Destination') == '/var/lib/leapview' for mount in mounts):
        raise RuntimeError('CFO state volume is not mounted at the expected location')
    if output('docker', 'inspect', '-f', '{{.State.Running}}', 'demo02-postgres-cfo') != 'true':
        raise RuntimeError('CFO PostgreSQL container is not running')
    caddy_before = output('docker', 'inspect', '-f', '{{.Id}}', 'leapview-cfo-caddy-1')
    database_before = output('docker', 'inspect', '-f', '{{.Id}}', 'demo02-postgres-cfo')
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
    for name in ('LEAPVIEW_AGENT_API_KEY', 'LEAPVIEW_AGENT_BASE_URL', 'LEAPVIEW_AGENT_MODEL'):
        if not env_value(app_contents, name):
            raise RuntimeError(f'existing {name} configuration is incomplete')
    for path in (DEPLOYMENT_ENV, MARKER):
        shutil.copy2(path, backup / path.name)
    (backup / 'previous-generation.txt').write_text(previous_generation + '\n')
    (backup / 'previous-generation.txt').chmod(0o600)

    changed = False
    try:
        # Arm rollback before the first durable mutation. A write or activation
        # failure must restore the complete predecessor configuration.
        changed = True
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
                       'test -n "$LEAPVIEW_AGENT_API_KEY" && '
                       'test -n "$LEAPVIEW_AGENT_BASE_URL" && '
                       'test -n "$LEAPVIEW_AGENT_MODEL" && printf configured')
        if names != 'configured':
            raise RuntimeError('agent provider configuration was not installed')
        if output('docker', 'inspect', '-f', '{{.Id}}', 'leapview-cfo-caddy-1') != caddy_before:
            raise RuntimeError('Caddy container changed during application rollout')
        if output('docker', 'inspect', '-f', '{{.Id}}', 'demo02-postgres-cfo') != database_before:
            raise RuntimeError('CFO PostgreSQL container changed during application rollout')
        (backup / 'rollout-success.json').write_text(json.dumps({
            'revision': REVISION, 'image': IMAGE, 'predecessorRevision': current_identity['revision']
        }) + '\n')
        (backup / 'rollout-success.json').chmod(0o600)
        print(f'Deployed exact qualified Compose image {IMAGE}; readiness and agent configuration passed')
    except BaseException:
        if changed:
            write_private_atomic(DEPLOYMENT_ENV, (backup / DEPLOYMENT_ENV.name).read_text())
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
