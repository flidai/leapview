#!/usr/bin/env python3
"""Write first-install evidence after the operator has completed validation.

This file is copied to demo-02 over pinned root SSH by compose_installation.py.
It verifies the installed host from live state and writes one private receipt.
It deliberately does not contain credentials or make a validation claim itself.
"""
import argparse
import datetime as dt
import fcntl
import json
import os
from pathlib import Path
import re
import socket
import stat
import subprocess
import tempfile
import urllib.request


ROOT = Path('/opt/leapview')
PROVIDER = Path('/etc/leapview-provider-cfo')
EXPECTED_HOST = 'app-leapview-demo-02'
APP = 'leapview-cfo-leapview-1'
POSTGRES = 'demo02-postgres-cfo'
APP_VOLUME = 'leapview-cfo_leapview-state'
COMPOSE_PROJECT = 'leapview-cfo'
PUBLIC_HOST = 'demo.leapview.dev'
RECEIPT = 'compose-installation.json'
IMAGE_RE = re.compile(r'^ghcr\.io/flidai/leapview@sha256:[0-9a-f]{64}$')
REVISION_RE = re.compile(r'^[0-9a-f]{40}$')
INSTANCE_RE = re.compile(r'^(?:lvinst_[A-Za-z0-9_-]{32}|instance_[0-9a-f]{32})$')
RUN_RE = re.compile(r'^[0-9]+$')
PROFILE_RE = re.compile(r'^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$')
PAYLOAD_MODES = {
    'leapviewctl': 0o700,
    'compose.yaml': 0o600,
    'compose.https.yaml': 0o600,
    'Caddyfile': 0o600,
    'deployment.env.example': 0o600,
    'leapviewctl-wrapper': 0o700,
}


def _run(*args, **kwargs):
    return subprocess.run(args, check=True, stdout=subprocess.DEVNULL,
                          stderr=subprocess.DEVNULL, **kwargs)


def _output(*args):
    return subprocess.check_output(args, text=True, stderr=subprocess.DEVNULL).strip()


def _fstat(fd):
    return os.fstat(fd)


def _private_json(path, label, maximum=1 << 20, required_mode=None):
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    try:
        info = _fstat(fd)
        if (not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or
                info.st_mode & 0o077 or info.st_size > maximum):
            raise ValueError('Invalid private ' + label)
        if required_mode is not None and stat.S_IMODE(info.st_mode) != required_mode:
            raise ValueError('Invalid private ' + label + ' permissions')
        with os.fdopen(os.dup(fd), 'rb') as stream:
            value = json.load(stream)
    finally:
        os.close(fd)
    if not isinstance(value, dict):
        raise ValueError('Invalid private ' + label)
    return value


def _private_file(path, label, maximum=1 << 20):
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    try:
        info = _fstat(fd)
        if (not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or
                info.st_mode & 0o077 or info.st_size > maximum):
            raise ValueError('Invalid private ' + label)
        with os.fdopen(os.dup(fd), 'rb') as stream:
            return stream.read()
    finally:
        os.close(fd)


def _private_directory(path, label):
    info = path.lstat()
    if (not stat.S_ISDIR(info.st_mode) or info.st_uid != 0 or
            stat.S_IMODE(info.st_mode) != 0o700):
        raise ValueError('Invalid private ' + label)


def _parse_env(data):
    values = {}
    for line in data.decode().splitlines():
        if not line or line.startswith('#'):
            continue
        key, separator, value = line.partition('=')
        if not separator or not re.fullmatch(r'[A-Z][A-Z0-9_]*', key) or key in values:
            raise ValueError('Invalid deployment descriptor')
        values[key] = value
    return values


def _ready_and_instance(opener):
    ready_request = urllib.request.Request(
        'http://127.0.0.1:8081/readyz', headers={'Host': PUBLIC_HOST})
    with opener.open(ready_request, timeout=10) as response:
        ready = json.load(response)
    if not isinstance(ready, dict) or ready.get('status') != 'ready':
        raise ValueError('Application readiness check failed')
    instance_request = urllib.request.Request(
        'http://127.0.0.1:8081/api/v1/instance', headers={'Host': PUBLIC_HOST})
    with opener.open(instance_request, timeout=10) as response:
        instance = json.load(response)
    if not isinstance(instance, dict) or not isinstance(instance.get('id'), str):
        raise ValueError('Live instance identity endpoint is invalid')
    return instance['id']


def _image_payload(image, run=_run, output=_output):
    """Read the immutable image's installed payload without starting it."""
    with tempfile.TemporaryDirectory(prefix='leapview-install-evidence-') as directory:
        destination = Path(directory) / 'payload'
        destination.mkdir(mode=0o700)
        container_id = output('docker', 'create', image)
        try:
            run('docker', 'cp', container_id + ':/usr/local/share/leapview/deployment/.',
                str(destination))
            payload = {}
            for item in destination.rglob('*'):
                info = item.lstat()
                if stat.S_ISDIR(info.st_mode):
                    continue
                if not stat.S_ISREG(info.st_mode):
                    raise ValueError('Immutable deployment payload contains a special file')
                relative = str(item.relative_to(destination))
                payload[relative] = item.read_bytes()
        finally:
            run('docker', 'rm', container_id)
    if any(not payload.get(name) for name in PAYLOAD_MODES):
        raise ValueError('Immutable image is missing a required deployment payload file')
    return {name: payload[name] for name in PAYLOAD_MODES}


def _verify_active_payload(root, image, payload):
    digest = image.rsplit(':', 1)[1]
    expected = 'releases/sha256-' + digest
    current = root / 'current'
    info = current.lstat()
    if not stat.S_ISLNK(info.st_mode) or info.st_uid != 0 or os.readlink(current) != expected:
        raise ValueError('Active deployment generation does not match the immutable image')
    release = root / expected
    release_info = release.lstat()
    if (not stat.S_ISDIR(release_info.st_mode) or release_info.st_uid != 0 or
            stat.S_IMODE(release_info.st_mode) != 0o700):
        raise ValueError('Active deployment generation is not private')
    actual = {}
    for item in release.iterdir():
        file_info = item.lstat()
        if (not stat.S_ISREG(file_info.st_mode) or file_info.st_uid != 0 or
                stat.S_IMODE(file_info.st_mode) != PAYLOAD_MODES.get(item.name)):
            raise ValueError('Active deployment payload has unsafe files or permissions')
        actual[item.name] = item.read_bytes()
    if actual != payload:
        raise ValueError('Active deployment payload differs from the immutable image')


def inspect_live(image, revision, expected_schema, *, root=ROOT, provider=PROVIDER,
                 output=_output, run=_run, urlopen=None, hostname=None):
    """Compare requested qualification identity with independently read state."""
    if not IMAGE_RE.fullmatch(image) or not REVISION_RE.fullmatch(revision):
        raise ValueError('Invalid immutable image identity')
    if type(expected_schema) is not int or expected_schema < 1:
        raise ValueError('Invalid qualified schema')
    hostname = socket.gethostname() if hostname is None else hostname
    if hostname != EXPECTED_HOST:
        raise ValueError('Unexpected target host')
    _private_directory(root, 'installation root')
    _private_directory(provider, 'provider state directory')

    if (os.path.lexists(provider / 'compose-deployment.json') or
            os.path.lexists(root / 'upgrade-operation.json')):
        raise ValueError('First-install evidence is unavailable after deployment or upgrade evidence exists')
    history = root / 'upgrade-history'
    try:
        history_info = history.lstat()
    except FileNotFoundError:
        history_info = None
    if history_info is not None:
        if not stat.S_ISDIR(history_info.st_mode) or history_info.st_uid != 0 or history_info.st_mode & 0o077:
            raise ValueError('Invalid private upgrade history')
        if any(history.iterdir()):
            raise ValueError('First-install evidence is unavailable after upgrade history exists')

    marker = _private_json(root / '.host-install.json', 'host installation marker')
    if (type(marker.get('schemaVersion')) is not int or marker['schemaVersion'] != 1 or
            marker.get('image') != image or marker.get('domain') != PUBLIC_HOST or
            marker.get('https') is not True):
        raise ValueError('Host installation marker differs from the qualified HTTPS installation')
    target_id = marker.get('targetId')
    if not isinstance(target_id, str) or not target_id.strip() or target_id != target_id.strip():
        raise ValueError('Host installation target identity is missing or invalid')

    deployment = _parse_env(_private_file(root / 'deployment.env', 'deployment descriptor'))
    if (deployment.get('LEAPVIEW_IMAGE') != image or
            deployment.get('COMPOSE_PROJECT_NAME') != COMPOSE_PROJECT or
            deployment.get('COMPOSE_APP_BIND') != '127.0.0.1:8081' or
            deployment.get('COMPOSE_HTTPS') != '1' or
            deployment.get('CADDY_DOMAIN') != PUBLIC_HOST):
        raise ValueError('Deployment descriptor differs from the qualified CFO topology')

    container = json.loads(output('docker', 'inspect', APP))[0]
    labels = container.get('Config', {}).get('Labels') or {}
    if (not container.get('State', {}).get('Running') or
            container.get('Config', {}).get('Image') != image or
            labels.get('com.docker.compose.project') != COMPOSE_PROJECT or
            labels.get('com.docker.compose.project.working_dir') != str(root)):
        raise ValueError('Live application container differs from the installed Compose identity')
    if not any(mount.get('Name') == APP_VOLUME and mount.get('Destination') == '/var/lib/leapview'
               for mount in container.get('Mounts', [])):
        raise ValueError('Live application state volume differs from the installation profile')
    port_bindings = container.get('HostConfig', {}).get('PortBindings') or {}
    if not any(binding.get('HostIp') == '127.0.0.1' and binding.get('HostPort') == '8081'
               for bindings in port_bindings.values() for binding in (bindings or [])):
        raise ValueError('Live application is not bound to loopback port 8081')
    image_info = json.loads(output('docker', 'image', 'inspect', image))[0]
    if (container.get('Image') != image_info.get('Id') or
            image not in image_info.get('RepoDigests', [])):
        raise ValueError('Running container content does not match its immutable image reference')
    runtime = json.loads(output('docker', 'exec', APP, 'leapview', 'version', '--json'))
    if runtime.get('revision') != revision or runtime.get('dirty') is not False:
        raise ValueError('Running application source differs from the qualified revision')
    payload = _image_payload(image, run=run, output=output)
    _verify_active_payload(root, image, payload)

    schema_text = output('docker', 'exec', POSTGRES, 'sh', '-c',
        'psql -U "$POSTGRES_USER" -d leapview_control -Atc '
        '"SELECT max(version_id) FROM public.goose_db_version WHERE is_applied"')
    if not re.fullmatch(r'[0-9]+', schema_text) or int(schema_text) != expected_schema:
        raise ValueError('Live Goose schema differs from the qualified schema')
    instance_id = output('docker', 'exec', POSTGRES, 'sh', '-c',
        'psql -U "$POSTGRES_USER" -d leapview_control -Atc '
        '"SELECT instance_id FROM platform.instance_identity WHERE singleton_id = 1"')
    if not INSTANCE_RE.fullmatch(instance_id):
        raise ValueError('Live database instance identity is missing or invalid')
    if urlopen is None:
        class NoRedirect(urllib.request.HTTPRedirectHandler):
            def redirect_request(self, req, fp, code, msg, headers, newurl):
                raise ValueError('Local readiness endpoint redirected')

        urlopen = urllib.request.build_opener(NoRedirect())
    public_instance = _ready_and_instance(urlopen)
    if public_instance != instance_id:
        raise ValueError('Application instance identity differs from the live database')
    return {
        'host': hostname,
        'installationRoot': str(root),
        'hostTargetId': target_id,
        'instanceId': instance_id,
        'image': image,
        'revision': revision,
        'schema': expected_schema,
    }


def _validate_record_inputs(permission_profile, qualification_run_id, qualification_attempt):
    if not isinstance(permission_profile, str) or not PROFILE_RE.fullmatch(permission_profile):
        raise ValueError('Invalid qualified permission profile')
    if (not isinstance(qualification_run_id, str) or not RUN_RE.fullmatch(qualification_run_id) or
            not isinstance(qualification_attempt, str) or not RUN_RE.fullmatch(qualification_attempt)):
        raise ValueError('Invalid qualification run identity')


def _timestamp(value):
    if value.tzinfo is None or value.utcoffset() is None:
        raise ValueError('Validation timestamp must be timezone-aware')
    return value.astimezone(dt.timezone.utc).isoformat(timespec='seconds').replace('+00:00', 'Z')


def _valid_timestamp(value):
    if not isinstance(value, str) or not re.fullmatch(
            r'\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z', value):
        return False
    try:
        return dt.datetime.fromisoformat(value[:-1] + '+00:00').tzinfo == dt.timezone.utc
    except ValueError:
        return False


def _atomic_root_private(path, data):
    fd, temporary = tempfile.mkstemp(prefix='.' + path.name + '.', dir=path.parent)
    try:
        os.fchown(fd, 0, 0)
        os.fchmod(fd, 0o600)
        with os.fdopen(fd, 'wb', closefd=False) as stream:
            stream.write(data)
            stream.flush()
            os.fsync(fd)
        # link() is atomic and refuses to replace an existing receipt.
        os.link(temporary, path, follow_symlinks=False)
        os.unlink(temporary)
        dir_fd = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(dir_fd)
        finally:
            os.close(dir_fd)
    except BaseException:
        try:
            os.unlink(temporary)
        except FileNotFoundError:
            pass
        raise
    finally:
        try:
            os.close(fd)
        except OSError:
            pass


def _acquire_host_locks(root):
    locks = []
    try:
        for lock_path in (root / '.leapviewctl.lock', Path('/run/leapview-demo-deploy.lock')):
            try:
                fd = os.open(lock_path, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
            except FileNotFoundError:
                continue
            try:
                info = os.fstat(fd)
                if (not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or
                        stat.S_IMODE(info.st_mode) != 0o600):
                    raise ValueError('Invalid private host operation lock')
                fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except ValueError:
                os.close(fd)
                raise
            except BaseException as exc:
                os.close(fd)
                raise ValueError('Installation is busy with another host operation') from exc
            locks.append(fd)
        return locks
    except BaseException:
        for fd in reversed(locks):
            os.close(fd)
        raise


def write_evidence(*, image, revision, schema, permission_profile,
                   qualification_run_id, qualification_attempt, root=ROOT,
                   provider=PROVIDER, inspect=inspect_live, now=None):
    if os.geteuid() != 0:
        raise ValueError('Installation evidence must be written as root')
    _validate_record_inputs(permission_profile, qualification_run_id, qualification_attempt)
    locks = _acquire_host_locks(root)
    try:
        pending = root / 'upgrade-operation.json'
        if pending.exists():
            raise ValueError('Cannot write first-install evidence during a host maintenance operation')
        observed = inspect(image, revision, schema, root=root, provider=provider)
        record = {
            'version': 'leapview-compose-installation-v1',
            **observed,
            'permissionProfile': permission_profile,
            'qualificationRunId': qualification_run_id,
            'qualificationAttempt': qualification_attempt,
            'validatedAt': _timestamp(now or dt.datetime.now(dt.timezone.utc)),
        }
        destination = provider / RECEIPT
        try:
            existing = _private_json(destination, 'installation evidence receipt', 16384,
                                     required_mode=0o600)
        except FileNotFoundError:
            existing = None
        if existing is not None:
            if not _valid_timestamp(existing.get('validatedAt')):
                raise ValueError('Existing first-install evidence has an invalid validation timestamp')
            prior = dict(record)
            prior['validatedAt'] = existing.get('validatedAt')
            if existing != prior:
                raise ValueError('Existing first-install evidence does not match current live state')
            # An exact retry preserves the original successful validation time.
            return existing
        encoded = (json.dumps(record, sort_keys=True, indent=2) + '\n').encode()
        _atomic_root_private(destination, encoded)
        return record
    finally:
        for fd in reversed(locks):
            fcntl.flock(fd, fcntl.LOCK_UN)
            os.close(fd)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--image', required=True)
    parser.add_argument('--revision', required=True)
    parser.add_argument('--schema', required=True, type=int)
    parser.add_argument('--permission-profile', required=True)
    parser.add_argument('--qualification-run-id', required=True)
    parser.add_argument('--qualification-attempt', required=True)
    args = parser.parse_args(argv)
    record = write_evidence(
        image=args.image, revision=args.revision, schema=args.schema,
        permission_profile=args.permission_profile,
        qualification_run_id=args.qualification_run_id,
        qualification_attempt=args.qualification_attempt)
    print(json.dumps({'written': True, 'version': record['version'],
                      'image': record['image'], 'revision': record['revision'],
                      'validatedAt': record['validatedAt']}, sort_keys=True))


if __name__ == '__main__':
    main()
