#!/usr/bin/env python3
"""Prepare the interrupted demo-02 first install without initializing LeapView.

Run this as root on app-leapview-demo-02 after reviewing the provider files.
The command repairs only the PostgreSQL CA mount and stages private controller
configuration under /opt/leapview. It never starts Compose, initializes an
administrator, or writes an installation-success marker.
"""
from __future__ import annotations

import argparse
import dataclasses
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import stat
import subprocess
import sys
from urllib.parse import parse_qs, unquote, urlsplit


HOST = 'app-leapview-demo-02'
BOOTSTRAP_ROOT = Path('/opt/leapview-cfo-bootstrap')
INSTALL_ROOT = Path('/opt/leapview')
PROVIDER_ROOT = Path('/etc/leapview-provider-cfo')
APP = 'leapview-cfo-leapview-1'
POSTGRES = 'demo02-postgres-cfo'
PROJECT = 'leapview-cfo'
NETWORK = 'leapview-cfo_default'
VOLUME = 'leapview-cfo_leapview-state'
APP_IMAGE = 'ghcr.io/flidai/leapview@sha256:bc07ec074a4df412f3cb1a81c32b35a54d92494e3f42dcdc115942ec81029540'
APP_REVISION = 'bbdaa69edab52136a56c8abebf57b97904081bc4'
APP_BIND = '127.0.0.1:8081'
DOMAIN = 'demo.leapview.dev'
APP_HOME = '/var/lib/leapview/home'
APP_CA = APP_HOME + '/postgres-root.crt'
POSTGRES_UID_GID = '999:999'
IMAGE_RE = re.compile(r'^[A-Za-z0-9._:/-]+@sha256:[0-9a-f]{64}$')
ROLE_RE = re.compile(r'^[A-Za-z_][A-Za-z0-9_]*$')
HEX_KEY_RE = re.compile(r'^[0-9a-f]{64}$')
SECRET_KEYS = (
    'LEAPVIEW_CSRF_KEY',
    'LEAPVIEW_METRICS_BEARER_TOKEN',
    'LEAPVIEW_AGENT_CREDENTIAL_KEY',
)

ROLE_URLS = (
    ('control-runtime', 'LEAPVIEW_POSTGRES_CONTROL_URL', 'leapview_control',
     'LEAPVIEW_POSTGRES_CONTROL_RUNTIME_ROLE'),
    ('control-migrator', 'LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL', 'leapview_control',
     'LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_ROLE'),
    ('control-maintenance', 'LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_URL', 'leapview_control',
     'LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_ROLE'),
    ('ducklake-runtime', 'LEAPVIEW_POSTGRES_DUCKLAKE_URL', 'leapview_ducklake',
     'LEAPVIEW_POSTGRES_DUCKLAKE_RUNTIME_ROLE'),
    ('ducklake-maintenance', 'LEAPVIEW_POSTGRES_DUCKLAKE_MAINTENANCE_URL', 'leapview_ducklake',
     'LEAPVIEW_POSTGRES_DUCKLAKE_MAINTENANCE_ROLE'),
)


class PreflightError(RuntimeError):
    """A safe, non-secret diagnostic for a failed prerequisite."""


@dataclasses.dataclass(frozen=True)
class Paths:
    bootstrap_root: Path = BOOTSTRAP_ROOT
    install_root: Path = INSTALL_ROOT
    provider_root: Path = PROVIDER_ROOT


def _checked(step, args, *, input_bytes=None):
    """Capture tool output and hide stderr/argv on failure to avoid secret leaks."""
    try:
        result = subprocess.run(args, input=input_bytes, stdout=subprocess.PIPE,
                                stderr=subprocess.PIPE, check=False)
    except OSError:
        raise PreflightError(step + ' could not run') from None
    if result.returncode:
        raise PreflightError(step + ' failed (exit ' + str(result.returncode) + ')')
    try:
        return result.stdout.decode('utf-8').strip()
    except UnicodeDecodeError:
        raise PreflightError(step + ' returned invalid text') from None


def _json_command(step, args):
    raw = _checked(step, args)
    try:
        return json.loads(raw)
    except json.JSONDecodeError:
        raise PreflightError(step + ' returned invalid JSON') from None


def _read_regular(path, label, expected_uid, maximum=1 << 20, private=True):
    flags = os.O_RDONLY | getattr(os, 'O_NOFOLLOW', 0)
    try:
        descriptor = os.open(path, flags)
    except OSError:
        raise PreflightError(label + ' is missing or cannot be opened safely') from None
    try:
        info = os.fstat(descriptor)
        if (not stat.S_ISREG(info.st_mode) or info.st_uid != expected_uid or
                info.st_size > maximum or (private and info.st_mode & 0o077)):
            raise PreflightError(label + ' has unsafe type, owner, mode, or size')
        with os.fdopen(os.dup(descriptor), 'rb') as stream:
            return stream.read()
    finally:
        os.close(descriptor)


def _check_directory(path, label, expected_uid, private=True):
    try:
        info = os.lstat(path)
    except OSError:
        raise PreflightError(label + ' is missing') from None
    if (not stat.S_ISDIR(info.st_mode) or info.st_uid != expected_uid or
            (private and info.st_mode & 0o077)):
        raise PreflightError(label + ' has unsafe type, owner, or mode')


def _parse_env(raw, label):
    values = {}
    for line_number, source_line in enumerate(raw.decode('utf-8').splitlines(), 1):
        line = source_line.strip()
        if not line or line.startswith('#'):
            continue
        name, separator, value = line.partition('=')
        if (not separator or not re.fullmatch(r'[A-Za-z_][A-Za-z0-9_]*', name) or
                name in values or '\x00' in value):
            raise PreflightError(label + ' has an invalid or duplicate entry at line ' + str(line_number))
        values[name] = value
    return values


def _replace_env_values(raw, replacements):
    lines = raw.decode('utf-8').splitlines()
    found = set()
    for index, line in enumerate(lines):
        stripped = line.strip()
        if not stripped or stripped.startswith('#') or '=' not in line:
            continue
        name = line.split('=', 1)[0].strip()
        if name in replacements:
            lines[index] = name + '=' + replacements[name]
            found.add(name)
    for name in SECRET_KEYS:
        if name in replacements and name not in found:
            lines.append(name + '=' + replacements[name])
    return ('\n'.join(lines).rstrip('\n') + '\n').encode('utf-8')


def _safe_source_ca(path, provider_root, expected_uid):
    path = Path(path)
    if not path.is_absolute() or ',' in str(path) or '\n' in str(path):
        raise PreflightError('PostgreSQL CA source path is invalid')
    _check_directory(provider_root, 'private provider directory', expected_uid)
    try:
        resolved_root = provider_root.resolve(strict=True)
        resolved_path = path.resolve(strict=True)
        resolved_path.relative_to(resolved_root)
        info = os.lstat(path)
    except (OSError, ValueError):
        raise PreflightError('PostgreSQL CA source must be an existing file inside the private provider directory') from None
    if (not stat.S_ISREG(info.st_mode) or info.st_uid != expected_uid or
            info.st_mode & 0o022 or info.st_size == 0 or info.st_size > (1 << 20)):
        raise PreflightError('PostgreSQL CA source has unsafe type, owner, mode, or size')
    raw = _read_regular(path, 'PostgreSQL CA source', expected_uid, private=False)
    return path, hashlib.sha256(raw).hexdigest()


class DockerBackend:
    """Narrow read-only inspectors and isolated one-shot probes for demo-02."""

    def hostname(self):
        return _checked('target hostname', ['hostname'])

    def image(self, image):
        values = _json_command('application image inspection', ['docker', 'image', 'inspect', image])
        if not isinstance(values, list) or len(values) != 1 or not isinstance(values[0], dict):
            raise PreflightError('application image inspection returned an unexpected result')
        return values[0]

    def postgres(self):
        values = _json_command('PostgreSQL container inspection', ['docker', 'inspect', POSTGRES])
        if not isinstance(values, list) or len(values) != 1 or not isinstance(values[0], dict):
            raise PreflightError('PostgreSQL container inspection returned an unexpected result')
        return values[0]

    def volume(self):
        values = _json_command('application state volume inspection', ['docker', 'volume', 'inspect', VOLUME])
        if not isinstance(values, list) or len(values) != 1 or not isinstance(values[0], dict):
            raise PreflightError('application state volume inspection returned an unexpected result')
        return values[0]

    def network(self):
        values = _json_command('application network inspection', ['docker', 'network', 'inspect', NETWORK])
        if not isinstance(values, list) or len(values) != 1 or not isinstance(values[0], dict):
            raise PreflightError('application network inspection returned an unexpected result')
        return values[0]

    def optional_app_container(self):
        try:
            result = subprocess.run(['docker', 'inspect', APP], stdout=subprocess.PIPE,
                                    stderr=subprocess.PIPE, check=False)
        except OSError:
            raise PreflightError('application container inspection could not run') from None
        if result.returncode:
            diagnostic = result.stderr.decode('utf-8', errors='replace').lower()
            if 'no such object' in diagnostic or 'no such container' in diagnostic:
                return None
            raise PreflightError('application container inspection failed')
        try:
            values = json.loads(result.stdout.decode('utf-8'))
        except (UnicodeDecodeError, json.JSONDecodeError):
            raise PreflightError('application container inspection returned invalid JSON') from None
        if not isinstance(values, list) or len(values) != 1:
            raise PreflightError('application container inspection returned an unexpected result')
        return values[0]

    def postgres_version(self):
        return _checked('PostgreSQL engine version',
                        ['docker', 'exec', POSTGRES, 'postgres', '--version'])

    def validate_ca_source(self, source):
        _checked('provider CA certificate validation',
                 ['openssl', 'x509', '-in', str(source), '-noout', '-checkend', '0'])

    def check_home_owner(self):
        script = ('set -eu\n'
                  'for utility in stat cp chown chmod mv sha256sum sync; do command -v "$utility" >/dev/null; done\n'
                  'test -d /var/lib/leapview/home\n'
                  'test "$(stat -c %u:%g /var/lib/leapview/home)" = 999:999\n')
        _checked('application home ownership check', [
            'docker', 'run', '--rm', '--network', 'none', '--user', POSTGRES_UID_GID,
            '--mount', 'type=volume,src=' + VOLUME + ',dst=/var/lib/leapview,readonly',
            '--entrypoint', '/busybox/sh', APP_IMAGE, '-ec', script,
        ])

    def install_ca(self, source):
        script = r'''set -eu
home=/var/lib/leapview/home
source=/run/leapview-postgres-root.crt
target=$home/postgres-root.crt
test -d "$home"
test "$(stat -c %u:%g "$home")" = 999:999
source_hash=$(sha256sum "$source")
source_hash=${source_hash%% *}
if [ -e "$target" ] || [ -L "$target" ]; then
  test -f "$target" && test ! -L "$target"
  test "$(stat -c %u:%g "$target")" = 999:999
  test -r "$target"
  target_hash=$(sha256sum "$target")
  target_hash=${target_hash%% *}
  test "$source_hash" = "$target_hash"
else
  temporary=$home/.postgres-root.crt.preflight.$$
  trap 'rm -f "$temporary"' EXIT
  cp "$source" "$temporary"
  chown 999:999 "$temporary"
  chmod 0444 "$temporary"
  sync
  mv "$temporary" "$target"
  sync
fi
test -r "$target"
test "$(stat -c %u:%g "$target")" = 999:999
sha256sum "$target"
'''
        _checked('PostgreSQL CA installation', [
            'docker', 'run', '--rm', '--network', 'none', '--user', '0:0',
            '--mount', 'type=volume,src=' + VOLUME + ',dst=/var/lib/leapview',
            '--mount', 'type=bind,src=' + str(source) + ',dst=/run/leapview-postgres-root.crt,readonly',
            '--entrypoint', '/busybox/sh', APP_IMAGE, '-ec', script,
        ])

    def verify_ca_as_app(self):
        script = 'set -eu\ntest -s "$1"\ntest -r "$1"\ntest "$(stat -c %u:%g "$1")" = 999:999\nsha256sum "$1"\n'
        output = _checked('PostgreSQL CA application readability check', [
            'docker', 'run', '--rm', '--network', 'none', '--user', POSTGRES_UID_GID,
            '--mount', 'type=volume,src=' + VOLUME + ',dst=/var/lib/leapview,readonly',
            '--entrypoint', '/busybox/sh', APP_IMAGE, '-ec', script, 'leapview-preflight', APP_CA,
        ])
        fields = output.split()
        if len(fields) < 2 or not re.fullmatch(r'[0-9a-f]{64}', fields[0]):
            raise PreflightError('PostgreSQL CA readability check returned an invalid digest')
        return fields[0]

    def empty_database(self, database):
        query = ("SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace "
                 "WHERE n.nspname NOT IN ('pg_catalog','information_schema') "
                 "AND c.relkind IN ('r','v','m','S','f','p')")
        output = _checked(database + ' empty-schema check', [
            'docker', 'exec', '--user', 'postgres', POSTGRES, 'psql', '--no-psqlrc',
            '--tuples-only', '--no-align', '--quiet', '--dbname=' + database,
            '--command=' + query,
        ])
        if output != '0':
            raise PreflightError(database + ' already contains application schema state')

    def probe_role(self, postgres_image, role):
        script = r'''set -eu
IFS= read -r PGPASSWORD
export PGPASSWORD
connection="host=$1 port=$2 dbname=$3 user=$4 sslmode=verify-full sslrootcert=$5 connect_timeout=10"
exec psql --no-psqlrc --no-password --tuples-only --no-align --quiet \
  --dbname="$connection" --command='SELECT current_user || chr(9) || current_database()'
'''
        password_input = role['password'].encode('utf-8') + b'\n'
        output = _checked('TLS role connection for ' + role['name'], [
            'docker', 'run', '--rm', '--interactive', '--network', NETWORK, '--user', POSTGRES_UID_GID,
            '--mount', 'type=volume,src=' + VOLUME + ',dst=/var/lib/leapview,readonly',
            '--entrypoint', '/bin/sh', postgres_image, '-ec', script, 'leapview-role-probe',
            role['host'], str(role['port']), role['database'], role['username'], APP_CA,
        ], input_bytes=password_input)
        expected = role['username'] + '\t' + role['database']
        if output != expected:
            raise PreflightError('TLS role connection returned an unexpected identity for ' + role['name'])

    def validate_config(self, app_env_path):
        _checked('production application configuration validation', [
            'docker', 'run', '--rm', '--network', 'none', '--user', POSTGRES_UID_GID,
            '--env-file', str(app_env_path),
            '--mount', 'type=volume,src=' + VOLUME + ',dst=/var/lib/leapview,readonly',
            '--entrypoint', '/usr/local/bin/leapview', APP_IMAGE,
            'config', 'validate', '--production',
        ])


def _validate_app_image(observed):
    if not isinstance(observed, dict):
        raise PreflightError('application image inspection is invalid')
    repo_digests = observed.get('RepoDigests')
    labels = observed.get('Config', {}).get('Labels', {})
    if (not isinstance(repo_digests, list) or APP_IMAGE not in repo_digests or
            not isinstance(labels, dict) or labels.get('org.opencontainers.image.revision') != APP_REVISION or
            labels.get('dev.leapview.build.dirty') not in (False, 'false')):
        raise PreflightError('application image does not match the admitted digest and clean source revision')


def _validate_postgres(observed, network, backend):
    if not isinstance(observed, dict):
        raise PreflightError('PostgreSQL container inspection is invalid')
    if observed.get('Name') != '/' + POSTGRES:
        raise PreflightError('PostgreSQL container name differs from the required host topology')
    labels = observed.get('Config', {}).get('Labels', {})
    if not isinstance(labels, dict) or labels.get('com.docker.compose.project') != PROJECT:
        raise PreflightError('PostgreSQL Compose project differs from the required host topology')
    state = observed.get('State', {})
    if not isinstance(state, dict) or state.get('Running') is not True or state.get('Health', {}).get('Status') != 'healthy':
        raise PreflightError('PostgreSQL container is not running and healthy')
    image = observed.get('Config', {}).get('Image', '')
    if not IMAGE_RE.fullmatch(image):
        raise PreflightError('PostgreSQL image is not pinned by an immutable digest')
    ports = observed.get('NetworkSettings', {}).get('Ports', {})
    if isinstance(ports, dict) and ports.get('5432/tcp'):
        raise PreflightError('PostgreSQL is published on a host port')
    networks = observed.get('NetworkSettings', {}).get('Networks', {})
    if not isinstance(networks, dict) or NETWORK not in networks:
        raise PreflightError('PostgreSQL is not attached to the application network')
    if not isinstance(network, dict) or network.get('Name') != NETWORK:
        raise PreflightError('application Docker network differs from the required host topology')
    version = backend.postgres_version()
    if not re.search(r'\bPostgreSQL\) 18(?:\.|\b)', version):
        raise PreflightError('PostgreSQL engine must remain on major version 18')
    return image


def _validate_deployment(values):
    required = {
        'LEAPVIEW_IMAGE': APP_IMAGE,
        'COMPOSE_PROJECT_NAME': PROJECT,
        'COMPOSE_APP_BIND': APP_BIND,
        'COMPOSE_HTTPS': '1',
        'CADDY_DOMAIN': DOMAIN,
        'CADDY_HTTP_BIND': '80',
        'CADDY_HTTPS_BIND': '443',
        'CADDY_HTTPS_UDP_BIND': '443',
    }
    for key, expected in required.items():
        if values.get(key) != expected:
            raise PreflightError('deployment configuration has an unexpected ' + key)
    if not IMAGE_RE.fullmatch(values.get('CADDY_IMAGE', '')):
        raise PreflightError('proxy image must be pinned by immutable digest')


def _parse_role_urls(values):
    if values.get('LEAPVIEW_POSTGRES_REQUIRE_TLS', '').lower() != 'true':
        raise PreflightError('application database configuration must require TLS')
    if values.get('LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_URL', '').strip():
        raise PreflightError('operation-only DuckLake migrator credentials must stay outside serving configuration')
    roles = []
    usernames = set()
    for name, url_key, database, role_key in ROLE_URLS:
        raw = values.get(url_key, '')
        try:
            parsed = urlsplit(raw)
            query = parse_qs(parsed.query, keep_blank_values=True, strict_parsing=True)
            port = parsed.port or 5432
        except ValueError:
            raise PreflightError(url_key + ' is not a valid PostgreSQL URL') from None
        username = unquote(parsed.username or '')
        password = unquote(parsed.password or '')
        role_value = values.get(role_key, '').strip()
        if (parsed.scheme not in ('postgres', 'postgresql') or parsed.hostname != POSTGRES or
                parsed.path != '/' + database or port != 5432 or not username or not password or
                any(char in password for char in '\r\n\x00') or
                not ROLE_RE.fullmatch(username) or not ROLE_RE.fullmatch(role_value) or
                username != role_value or username in usernames):
            raise PreflightError(url_key + ' does not match the required host, database, or distinct role')
        if query.get('sslmode') != ['verify-full'] or query.get('sslrootcert') != [APP_CA]:
            raise PreflightError(url_key + ' must use verify-full and the installed application CA path')
        usernames.add(username)
        roles.append({
            'name': name,
            'host': parsed.hostname,
            'port': port,
            'database': database,
            'username': username,
            'password': password,
        })
    return roles


def _validate_application(values):
    required = {
        'LEAPVIEW_PRODUCTION': '1',
        'LEAPVIEW_ENVIRONMENT': 'prod',
        'LEAPVIEW_ADDR': ':8080',
        'LEAPVIEW_HOME': APP_HOME,
        'LEAPVIEW_PUBLIC_URL': 'https://' + DOMAIN,
        'LEAPVIEW_ALLOWED_HOSTS': DOMAIN,
        'LEAPVIEW_COOKIE_SECURE': 'true',
        'LEAPVIEW_TRUST_PROXY_HEADERS': 'true',
        'LEAPVIEW_POSTGRES_REQUIRE_TLS': 'true',
    }
    for key, expected in required.items():
        if values.get(key) != expected:
            raise PreflightError('application configuration has an unexpected ' + key)


def _current_root_files(paths, expected_uid):
    try:
        info = os.lstat(paths.install_root)
    except FileNotFoundError:
        return None
    if (not stat.S_ISDIR(info.st_mode) or info.st_uid != expected_uid or info.st_mode & 0o077):
        raise PreflightError('canonical installation root has unsafe type, owner, or mode')
    try:
        children = {item.name for item in paths.install_root.iterdir()}
    except OSError:
        raise PreflightError('canonical installation root cannot be inspected') from None
    if not children.issubset({'deployment.env', 'leapview.env'}):
        raise PreflightError('canonical installation root contains partial or unreviewed installation state')
    result = {}
    for name in children:
        result[name] = _read_regular(paths.install_root / name, 'canonical ' + name, expected_uid)
    return result


def _merge_existing_application(source_raw, existing_raw):
    source_values = _parse_env(source_raw, 'bootstrap application environment')
    if existing_raw is None:
        return source_raw, source_values
    existing_values = _parse_env(existing_raw, 'canonical application environment')
    allowed_extras = set(SECRET_KEYS)
    for key, value in source_values.items():
        if key in SECRET_KEYS and (not value.strip() or '<generated' in value):
            if key not in existing_values or not existing_values[key].strip() or '<generated' in existing_values[key]:
                raise PreflightError('canonical application environment lost a previously staged key')
            continue
        if existing_values.get(key) != value:
            raise PreflightError('bootstrap and canonical application environments disagree')
    if set(existing_values) - set(source_values) - allowed_extras:
        raise PreflightError('canonical application environment has unreviewed fields')
    return existing_raw, existing_values


def _prepare_private_root(paths, deployment_raw, application_raw, expected_uid):
    existing = _current_root_files(paths, expected_uid)
    if existing is None:
        try:
            paths.install_root.mkdir(mode=0o700)
        except FileExistsError:
            existing = _current_root_files(paths, expected_uid)
            if existing is None:
                raise PreflightError('canonical installation root changed during preparation')
        except OSError:
            raise PreflightError('canonical installation root could not be created') from None
    if existing is not None:
        if 'deployment.env' in existing and existing['deployment.env'] != deployment_raw:
            raise PreflightError('canonical deployment configuration changed since preflight began')
        application_raw, existing_values = _merge_existing_application(
            application_raw, existing.get('leapview.env'))
    else:
        existing_values = _parse_env(application_raw, 'bootstrap application environment')

    generated = {}
    for key in SECRET_KEYS:
        current = existing_values.get(key, '').strip()
        if current and '<generated' not in current:
            continue
        value = secrets.token_hex(32)
        if key == 'LEAPVIEW_AGENT_CREDENTIAL_KEY' and not HEX_KEY_RE.fullmatch(value):
            raise AssertionError('agent credential key generation returned invalid data')
        generated[key] = value
        existing_values[key] = value
    if existing is None or 'leapview.env' not in existing:
        staged_application = _replace_env_values(application_raw, existing_values)
    else:
        staged_application = _replace_env_values(application_raw, generated)
    final_values = _parse_env(staged_application, 'staged application environment')
    for key in SECRET_KEYS:
        value = final_values.get(key, '')
        if key == 'LEAPVIEW_AGENT_CREDENTIAL_KEY':
            if not HEX_KEY_RE.fullmatch(value):
                raise PreflightError('existing agent credential key is invalid and was preserved')
        elif len(value) < 32 or '<generated' in value:
            raise PreflightError(key + ' is invalid and was preserved')
    _validate_application(final_values)

    def install_file(name, data):
        destination = paths.install_root / name
        try:
            info = os.lstat(destination)
        except FileNotFoundError:
            info = None
        if info is not None:
            if (not stat.S_ISREG(info.st_mode) or info.st_uid != expected_uid or
                    info.st_mode & 0o077):
                raise PreflightError('canonical ' + name + ' has unsafe type, owner, or mode')
            if _read_regular(destination, 'canonical ' + name, expected_uid) != data:
                raise PreflightError('canonical ' + name + ' would overwrite existing values')
            return
        temporary = paths.install_root / ('.' + name + '.preflight-' + secrets.token_hex(8))
        flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, 'O_NOFOLLOW', 0)
        try:
            descriptor = os.open(temporary, flags, 0o600)
            try:
                os.fchown(descriptor, expected_uid, os.getegid())
                with os.fdopen(descriptor, 'wb', closefd=False) as stream:
                    stream.write(data)
                    stream.flush()
                    os.fsync(descriptor)
            finally:
                os.close(descriptor)
            os.link(temporary, destination, follow_symlinks=False)
            os.unlink(temporary)
            directory_fd = os.open(paths.install_root, os.O_RDONLY | getattr(os, 'O_DIRECTORY', 0))
            try:
                os.fsync(directory_fd)
            finally:
                os.close(directory_fd)
        except FileExistsError:
            try:
                temporary.unlink(missing_ok=True)
            except OSError:
                pass
            existing_data = _read_regular(destination, 'canonical ' + name, expected_uid)
            if existing_data != data:
                raise PreflightError('canonical ' + name + ' changed during preparation') from None
        except OSError:
            try:
                temporary.unlink(missing_ok=True)
            except OSError:
                pass
            raise PreflightError('canonical ' + name + ' could not be written atomically') from None

    install_file('deployment.env', deployment_raw)
    install_file('leapview.env', staged_application)
    return final_values, sorted(generated)


def run_preflight(ca_source, *, backend=None, paths=None, enforce_root=True):
    """Check and stage the target; injected paths/backend are test seams only."""
    backend = backend or DockerBackend()
    paths = paths or Paths()
    effective_uid = os.geteuid()
    if enforce_root and effective_uid != 0:
        raise PreflightError('preflight must run as root')
    expected_uid = 0 if enforce_root else effective_uid
    completed = []

    def step(name, action):
        try:
            result = action()
        except PreflightError:
            raise
        except BaseException:
            raise PreflightError(name + ' failed') from None
        completed.append(name)
        return result

    hostname = step('target identity', backend.hostname)
    if hostname != HOST:
        raise PreflightError('unexpected target hostname')

    step('private provider directory', lambda: _check_directory(
        paths.provider_root, 'private provider directory', expected_uid))
    step('bootstrap directory', lambda: _check_directory(
        paths.bootstrap_root, 'interrupted bootstrap directory', expected_uid))
    deployment_raw = step('deployment environment read', lambda: _read_regular(
        paths.bootstrap_root / 'deployment.env', 'bootstrap deployment environment', expected_uid))
    deployment = step('deployment topology', lambda: _parse_env(deployment_raw, 'bootstrap deployment environment'))
    step('immutable image and loopback binding', lambda: _validate_deployment(deployment))
    application_source_raw = step('application environment read', lambda: _read_regular(
        paths.bootstrap_root / 'leapview.env', 'bootstrap application environment', expected_uid))
    root_files = step('installation state inventory', lambda: _current_root_files(paths, expected_uid))
    for candidate in (paths.bootstrap_root / 'initial-credentials.json',
                      paths.install_root / 'initial-credentials.json'):
        if candidate.exists() or candidate.is_symlink():
            raise PreflightError('initial credential file already exists; do not repeat first initialization')
    if root_files is not None and ('.host-install.json' in root_files or 'current' in root_files):
        raise PreflightError('installation marker or active generation already exists')

    image_info = step('admitted application image', lambda: backend.image(APP_IMAGE))
    step('application image identity', lambda: _validate_app_image(image_info))
    postgres_info = step('PostgreSQL container inspection', backend.postgres)
    network_info = step('application network inspection', backend.network)
    postgres_image = step('PostgreSQL topology and engine', lambda: _validate_postgres(
        postgres_info, network_info, backend))
    volume_info = step('application state volume inspection', backend.volume)
    if not isinstance(volume_info, dict) or volume_info.get('Name') != VOLUME:
        raise PreflightError('application state volume differs from the required host topology')
    app_container = step('unexpected application container check', backend.optional_app_container)
    if app_container is not None:
        raise PreflightError('application container already exists; inspect the interrupted initialization before proceeding')
    step('application home ownership', backend.check_home_owner)

    ca_path, ca_digest = step('provider CA source', lambda: _safe_source_ca(
        ca_source, paths.provider_root, expected_uid))
    step('provider CA certificate validation', lambda: backend.validate_ca_source(ca_path))
    step('application CA installation', lambda: backend.install_ca(ca_path))
    installed_ca_digest = step('application CA readability', backend.verify_ca_as_app)
    if installed_ca_digest != ca_digest:
        raise PreflightError('installed PostgreSQL CA differs from the preserved provider certificate')

    # Parse database URLs only after the configured certificate is installed
    # and readable as the application UID.
    application_values = step('application database URL validation', lambda: _parse_env(
        application_source_raw, 'bootstrap application environment'))
    step('HTTPS, proxy, and runtime configuration', lambda: _validate_application(application_values))
    roles = step('TLS database URL validation', lambda: _parse_role_urls(application_values))
    for database in ('leapview_control', 'leapview_ducklake'):
        step(database + ' empty-schema check', lambda database=database: backend.empty_database(database))
    for role in roles:
        step(role['name'] + ' TLS role connection', lambda role=role: backend.probe_role(postgres_image, role))

    final_values, generated_keys = step('private configuration staging', lambda: _prepare_private_root(
        paths, deployment_raw, application_source_raw, expected_uid))
    step('production application configuration validation', lambda: backend.validate_config(
        paths.install_root / 'leapview.env'))
    for name in ('initial-credentials.json', '.host-install.json'):
        if (paths.install_root / name).exists() or (paths.install_root / name).is_symlink():
            raise PreflightError('preflight unexpectedly created ' + name)

    return {
        'schemaVersion': 1,
        'status': 'preflight-ready',
        'host': HOST,
        'installationRoot': str(paths.install_root),
        'image': APP_IMAGE,
        'sourceRevision': APP_REVISION,
        'applicationBinding': APP_BIND,
        'postgresSchema': 'empty',
        'checkedRoles': [role['name'] for role in roles],
        'generatedKeyNames': generated_keys,
        'caSha256': ca_digest,
        'applicationInitialized': False,
        'steps': completed,
    }


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--ca-source', required=True,
                        help='existing provider CA certificate file inside /etc/leapview-provider-cfo')
    args = parser.parse_args(argv)
    try:
        result = run_preflight(args.ca_source)
    except PreflightError as error:
        print('demo install preflight failed: ' + str(error), file=sys.stderr)
        return 1
    print(json.dumps(result, sort_keys=True))
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
