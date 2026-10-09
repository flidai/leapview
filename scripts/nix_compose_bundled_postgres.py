"""Bounded proofs over installer-owned bundled PostgreSQL; no secret values leave the guest."""

from pathlib import PurePosixPath
import json
import re
import shlex

ROLE_MATERIAL = ('bootstrap-password', 'control-runtime-password', 'control-migrator-password',
    'control-maintenance-password', 'ducklake-runtime-password', 'ducklake-migrator-password',
    'ducklake-maintenance-password')
MOUNTED_MATERIAL = (*ROLE_MATERIAL, 'ca.crt', 'server.crt', 'server.key')
PRIVATE_MATERIAL = (*ROLE_MATERIAL, 'ca.key', 'ca.crt', 'server.key', 'server.crt')
SECRET_ROOT = '/opt/leapview/.postgres-secrets/'
CONTAINER = re.compile(r'^leapview-postgres-[1-9][0-9]*$')


def validate_inspection(value, image, image_id):
    if (not isinstance(value, dict) or set(value) != {'id', 'imageID', 'configuredImage', 'name',
            'state', 'restartPolicy', 'project', 'service', 'ports', 'networks', 'mounts'}
            or not re.fullmatch(r'[0-9a-f]{64}', value.get('id', ''))
            or value.get('imageID') != image_id or value.get('configuredImage') != image
            or not CONTAINER.fullmatch(value.get('name', '').removeprefix('/'))
            or value.get('state') != 'running' or value.get('restartPolicy') != 'unless-stopped'
            or value.get('project') != 'leapview' or value.get('service') != 'postgres'
            or value.get('ports') not in ({}, {'5432/tcp': None})
            or not isinstance(value.get('networks'), dict) or set(value['networks']) != {'leapview_postgres-private'}
            or not isinstance(value.get('mounts'), list) or any(not isinstance(mount, dict) for mount in value['mounts'])):
        raise ValueError('bundled PostgreSQL is not the exact private installer-owned running service')
    mounts = value['mounts']
    volumes = [mount for mount in mounts if mount.get('Destination') == '/var/lib/postgresql']
    if (len(volumes) != 1 or volumes[0].get('Type') != 'volume'
            or volumes[0].get('Name') != 'leapview_leapview-postgres-data' or volumes[0].get('RW') is not True):
        raise ValueError('bundled PostgreSQL has no exact persistent Compose data volume')
    for name in MOUNTED_MATERIAL:
        matches = [mount for mount in mounts if mount.get('Destination') == '/run/leapview-postgres-secrets/' + name]
        if (len(matches) != 1 or matches[0].get('Type') != 'bind'
                or matches[0].get('Source') != SECRET_ROOT + name or matches[0].get('RW') is not False):
            raise ValueError('bundled PostgreSQL secret or TLS mount is not private, immutable installer material')
    return {'containerName': value['name'].removeprefix('/'), 'containerID': value['id'],
        'configuredImage': image, 'imageID': image_id, 'dataVolume': volumes[0]['Name'],
        'privateNetwork': 'leapview_postgres-private', 'publishedPorts': False}


def inspection_command(docker_env):
    fields = {'id': '.Id', 'imageID': '.Image', 'configuredImage': '.Config.Image',
        'name': '.Name', 'state': '.State.Status', 'restartPolicy': '.HostConfig.RestartPolicy.Name',
        'project': 'index .Config.Labels "com.docker.compose.project"',
        'service': 'index .Config.Labels "com.docker.compose.service"',
        'ports': '.NetworkSettings.Ports', 'networks': '.NetworkSettings.Networks', 'mounts': '.Mounts'}
    template = '{' + ','.join('"' + key + '":{{json ' + expression + '}}' for key, expression in fields.items()) + '}'
    return ('set -eu; id=$(env ' + docker_env + ' docker ps --quiet --no-trunc '
        '--filter label=com.docker.compose.project=leapview --filter label=com.docker.compose.service=postgres); '
        'test -n "$id"; test "$(printf \'%s\\n\' "$id" | wc -l)" -eq 1; env ' + docker_env +
        ' docker inspect --format ' + shlex.quote(template) + ' "$id"')


def capture(guest, record, docker_env, fixture, prefix):
    raw = guest.run(inspection_command(docker_env), timeout=60)
    result = validate_inspection(json.loads(raw), fixture['image'], fixture['imageID'])
    network = guest.run('env ' + docker_env + ' docker network inspect --format \'{{json .Internal}}\' '
                        'leapview_postgres-private', timeout=30)
    if network.strip() != b'true':
        raise ValueError('bundled PostgreSQL network is not actually internal')
    roles = guest.run(readiness_command(result['containerName'], docker_env), timeout=60)
    if roles.decode().strip().splitlines() != [
            'leapview_control_runtime|leapview_control|true',
            'leapview_ducklake_runtime|leapview_ducklake|true']:
        raise ValueError('bundled PostgreSQL runtime roles do not authenticate over verified TLS')
    volume_labels = guest.run('env ' + docker_env + ' docker volume inspect --format \'{{json .Labels}}\' '
                             'leapview_leapview-postgres-data', timeout=30)
    labels = json.loads(volume_labels)
    if (not isinstance(labels, dict) or labels.get('com.docker.compose.project') != 'leapview'
            or labels.get('com.docker.compose.volume') != 'leapview-postgres-data'):
        raise ValueError('bundled PostgreSQL data volume is not owned by the selected Compose project')
    record(prefix + '-inspection.json', raw)
    record(prefix + '-network-internal.txt', network)
    record(prefix + '-tls-role-probes.txt', roles)
    record(prefix + '-volume-labels.json', volume_labels)
    result['privateNetworkInternal'] = True
    return result


def validate_retained(metadata, read, fixture):
    if not isinstance(metadata, dict) or set(metadata) != {'beforePendingReboot', 'afterPendingReboot', 'afterPublicReboot'}:
        raise ValueError('bundled PostgreSQL receipt lacks both automatic reboot proofs')
    for phase, prefix in [('beforePendingReboot', 'bundled-postgres-before-pending-reboot'),
            ('afterPendingReboot', 'bundled-postgres-after-pending-reboot'),
            ('afterPublicReboot', 'bundled-postgres-after-public-reboot')]:
        actual = validate_inspection(json.loads(read(prefix + '-inspection.json')), fixture['image'], fixture['imageID'])
        if read(prefix + '-network-internal.txt').strip() != b'true':
            raise ValueError('retained bundled PostgreSQL private-network evidence differs')
        if read(prefix + '-tls-role-probes.txt').decode().strip().splitlines() != [
                'leapview_control_runtime|leapview_control|true',
                'leapview_ducklake_runtime|leapview_ducklake|true']:
            raise ValueError('retained bundled PostgreSQL TLS role evidence differs')
        labels = json.loads(read(prefix + '-volume-labels.json'))
        if (not isinstance(labels, dict) or labels.get('com.docker.compose.project') != 'leapview'
                or labels.get('com.docker.compose.volume') != 'leapview-postgres-data'):
            raise ValueError('retained bundled PostgreSQL volume ownership differs')
        actual['privateNetworkInternal'] = True
        if actual != metadata[phase] or actual != metadata['beforePendingReboot']:
            raise ValueError('bundled PostgreSQL identity changed across reboot')
    for prefix in ('bundled-postgres-after-pending-reboot', 'bundled-postgres-after-public-reboot'):
        if json.loads(read(prefix + '-material-preserved.json')) != {
                'credentialsPreserved': True, 'tlsMaterialPreserved': True, 'privateProofRemoved': True}:
            raise ValueError('bundled PostgreSQL credentials or private TLS material did not persist')
    if read('bundled-postgres-pool-fixture-removed.txt').strip() != b'absent':
        raise ValueError('external pool probe remains alongside bundled PostgreSQL')
    return metadata


def readiness_command(container_name, docker_env, *, control_only=False):
    if not CONTAINER.fullmatch(container_name):
        raise ValueError('bundled PostgreSQL requires exact Compose container identity')
    checks = []
    for prefix, database in [('control', 'leapview_control'), ('ducklake', 'leapview_ducklake')]:
        if control_only and prefix != 'control':
            continue
        role = 'leapview_' + prefix + '_runtime'
        sql = "SELECT current_user::text || '|' || current_database()::text || '|' || (SELECT ssl::text FROM pg_stat_ssl WHERE pid=pg_backend_pid())"
        probe = ('export PGPASSWORD="$(cat /run/leapview-postgres-secrets/' + prefix + '-runtime-password)" '
            'PGSSLMODE=verify-full PGSSLROOTCERT=/run/leapview-postgres-secrets/ca.crt; '
            'exec psql --host=postgres --username=' + role + ' --dbname=' + database +
            ' --tuples-only --no-align --command=' + shlex.quote(sql))
        checks.append('actual=$(env ' + docker_env + ' docker exec ' + shlex.quote(container_name) +
            ' sh -ec ' + shlex.quote(probe) + '); test "$actual" = ' +
            shlex.quote(role + '|' + database + '|true') + '; printf \'%s\\n\' "$actual"')
    return 'set -eu; ' + '; '.join(checks)


def _snapshot_path(path):
    value = PurePosixPath(path)
    if not value.is_absolute() or '..' in value.parts or str(value) != path or '\n' in path:
        raise ValueError('bundled credential proof requires an exact private guest path')
    return shlex.quote(path)


def material_snapshot_command(path):
    target = _snapshot_path(path)
    guards = []
    for name in PRIVATE_MATERIAL:
        file = shlex.quote(SECRET_ROOT + name)
        mode = '644' if name.endswith('.crt') else '600'
        guards.append('test -f ' + file + '; test ! -L ' + file +
            '; test "$(stat -c \'%u:%g:%a\' ' + file + ')" = 0:0:' + mode)
    files = ' '.join(shlex.quote(SECRET_ROOT + name) for name in PRIVATE_MATERIAL)
    return ('set -eu; umask 077; ' + '; '.join(guards) + '; test ! -e ' + target +
        '; sha256sum ' + files + ' > ' + target + '; chmod 600 ' + target)


def material_verify_command(path):
    target = _snapshot_path(path)
    return ('set -eu; test -f ' + target + '; test ! -L ' + target +
        '; test "$(stat -c \'%u:%g:%a\' ' + target + ')" = 0:0:600; '
        'sha256sum --check --status ' + target + ' >/dev/null; rm -f -- ' + target +
        '; printf \'{"credentialsPreserved":true,"tlsMaterialPreserved":true,"privateProofRemoved":true}\\n\'')
