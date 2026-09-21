#!/usr/bin/env python3
"""One-time rollout of the requested immutable demo build, with local rollback."""
import json
import hashlib
import os
from pathlib import Path
import pwd
import shutil
import subprocess
import sys
import time
import urllib.parse
import urllib.request

REVISION = '2e228ec6b42ab5c18bea04ce037695642aaf8019'
PREDECESSOR_REVISION = 'd24ad786be8e838d923f6e57cd744eb8623c893b'
EXPECTED_IMAGE = 'ghcr.io/flidai/leapview@sha256:29d832a2504ccb39b4b7a4d55defa2c9968116d228449ca6adc981d6adcd1bc2'
RELEASE = Path('/opt/leapview-demo/releases') / REVISION
IMAGE = (RELEASE / 'immutable-image.txt').read_text().strip()
SERVICE = 'leapview-demo-current.service'
UNIT = Path('/etc/systemd/system') / SERVICE
DATABASE_CONTAINER = 'leapview-postgres-3948794932-demo-current-postgres-1'
HOME_PATH = Path('/tmp/leapview-demo-host-state')


def output(*args):
    return subprocess.check_output(args, text=True).strip()


def sql(query):
    return output('docker', 'exec', DATABASE_CONTAINER, 'sh', '-c',
                  'exec psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d leapview_control -Atc "$1"', 'query', query)


def ready():
    try:
        with urllib.request.urlopen('http://127.0.0.1:8132/readyz', timeout=3) as response:
            return response.status == 200 and json.load(response)['status'] == 'ready'
    except Exception:
        return False


def await_ready():
    initial_restarts = int(output('systemctl', 'show', SERVICE, '--property=NRestarts', '--value') or '0')
    for _ in range(60):
        if ready():
            return
        restarts = int(output('systemctl', 'show', SERVICE, '--property=NRestarts', '--value') or '0')
        if restarts - initial_restarts >= 3:
            raise RuntimeError('Runtime repeatedly exited during startup')
        time.sleep(2)
    raise RuntimeError('Runtime did not become ready within the cutover window')


def write_private(path, value):
    path.write_text(value)
    path.chmod(0o600)


def sha256(path):
    digest = hashlib.sha256()
    with path.open('rb') as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b''):
            digest.update(chunk)
    return digest.hexdigest()


def main():
    os.umask(0o077)
    assert sys.argv[1:] in (['--check'], ['--apply']), 'Expected --check or --apply'
    assert IMAGE == EXPECTED_IMAGE, 'Staged image differs from the admitted image'
    assert output('systemctl', 'is-active', SERVICE) == 'active'
    pid = output('systemctl', 'show', SERVICE, '--property=MainPID', '--value')
    args = [item.decode() for item in Path('/proc', pid, 'cmdline').read_bytes().split(b'\0') if item]
    assert args[1:] == ['serve', '--production'], 'Unexpected service arguments'
    runtime_env = dict(item.decode().split('=', 1) for item in Path('/proc', pid, 'environ').read_bytes().split(b'\0') if b'=' in item)
    runtime_env.setdefault('HOME', pwd.getpwuid(Path('/proc', pid).stat().st_uid).pw_dir)
    assert runtime_env['LEAPVIEW_HOME'] == str(HOME_PATH)
    assert ready(), 'Predecessor must be healthy'
    previous = json.loads(output(f'/proc/{pid}/exe', 'version', '--json'))
    assert previous['revision'] == PREDECESSOR_REVISION and previous['dirty'] is False, 'Predecessor changed'
    predecessor_release = Path('/opt/leapview-demo/releases') / PREDECESSOR_REVISION
    predecessor_image = (predecessor_release / 'immutable-image.txt').read_text().strip()
    assert predecessor_image.startswith('ghcr.io/flidai/leapview@sha256:') and len(predecessor_image) == 95
    predecessor_checksum = (predecessor_release / 'leapview.sha256').read_text().split()[0]
    assert sha256(Path('/proc', pid, 'exe')) == predecessor_checksum
    assert sha256(predecessor_release / 'leapview') == predecessor_checksum
    identity = json.loads(output(str(RELEASE / 'leapview'), 'version', '--json'))
    assert identity['revision'] == REVISION and identity['dirty'] is False
    assert (RELEASE / 'immutable-image.txt').read_text().strip() == IMAGE
    subprocess.run(['sha256sum', '--check', str(RELEASE / 'leapview.sha256')], check=True)
    original_fragment = Path(output('systemctl', 'show', SERVICE, '--property=FragmentPath', '--value'))
    original_unit = original_fragment.read_text()
    assert original_fragment in (Path('/run/systemd/transient') / SERVICE, UNIT), 'Unexpected unit owner'
    if UNIT.exists():
        assert UNIT.read_text() == original_unit and str(RELEASE) not in original_unit, 'Persistent unit differs from the restored predecessor'
    assert sql('SELECT max(version_id) FROM public.goose_db_version WHERE is_applied') == '22'

    operation_env = runtime_env.copy()
    for line in Path('/tmp/leapview-main/.tmp/postgres-demo-current.env').read_text().splitlines():
        key, sep, value = line.partition('=')
        if sep and key in ('LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL', 'LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_URL'):
            operation_env[key] = value
    runtime_url = urllib.parse.urlsplit(runtime_env['LEAPVIEW_POSTGRES_CONTROL_URL'])
    if 'LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL' not in operation_env:
        container_env = dict(item.split('=', 1) for item in json.loads(output(
            'docker', 'inspect', '--format', '{{json .Config.Env}}', DATABASE_CONTAINER)) if '=' in item)
        password = container_env['LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_PASSWORD']
        authority = 'leapview_control_migrator:' + urllib.parse.quote(password, safe='') + '@' + runtime_url.netloc.rsplit('@', 1)[-1]
        operation_env['LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL'] = urllib.parse.urlunsplit(runtime_url._replace(netloc=authority))
        assert output('docker', 'exec', DATABASE_CONTAINER, 'sh', '-c',
                      'PGPASSWORD="$LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_PASSWORD" psql --host=127.0.0.1 --username=leapview_control_migrator --dbname=leapview_control -Atc "SELECT 1"') == '1'
    migration_url = urllib.parse.urlsplit(operation_env['LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL'])
    assert (runtime_url.hostname, runtime_url.port, runtime_url.path) == (migration_url.hostname, migration_url.port, migration_url.path)
    assert runtime_url.username != migration_url.username
    ports = output('docker', 'port', DATABASE_CONTAINER, '5432/tcp').splitlines()
    assert any(port.endswith(':' + str(runtime_url.port)) for port in ports), 'Database backup target differs from runtime'
    home_size = int(output('du', '-sb', str(HOME_PATH)).split()[0])
    database_size = int(sql("SELECT sum(pg_database_size(oid)) FROM pg_database WHERE datname IN ('leapview_control','leapview_ducklake')"))
    required = 2 * (home_size + database_size) + 512 * 1024 * 1024
    assert shutil.disk_usage('/opt').free > required, 'Insufficient room for backup and rollback'
    print(f'Preflight passed: exact image, healthy predecessor, schema 22, backup space {required} bytes', flush=True)
    if sys.argv[1] == '--check':
        return

    backup = Path('/opt/leapview-demo/rollbacks') / (time.strftime('%Y%m%dT%H%M%SZ', time.gmtime()) + '-' + REVISION[:12])
    backup.mkdir(parents=True, mode=0o700)
    write_private(backup / 'original.service', original_unit)
    write_private(backup / 'predecessor.json', json.dumps(previous))
    service_stopped = False
    migration_started = False
    try:
        service_stopped = True
        subprocess.run(['systemctl', 'stop', SERVICE], check=True)
        for database in ('leapview_control', 'leapview_ducklake'):
            with (backup / (database + '.dump')).open('wb') as handle:
                subprocess.run(['docker', 'exec', DATABASE_CONTAINER, 'sh', '-c',
                                'exec pg_dump -U "$POSTGRES_USER" --format=custom --dbname="$1"', 'backup', database], stdout=handle, check=True)
            with (backup / (database + '.dump')).open('rb') as handle:
                subprocess.run(['docker', 'exec', '-i', DATABASE_CONTAINER, 'pg_restore', '--list'], stdin=handle, stdout=subprocess.DEVNULL, check=True)
        subprocess.run(['tar', '-cpf', str(backup / 'home.tar'), '-C', str(HOME_PATH.parent), HOME_PATH.name], check=True)
        migration_started = True
        # The canonical initialization boundary applies the fenced Goose/River
        # baseline first and preserves the existing instance and credentials.
        with (backup / 'baseline-output.json').open('wb') as stdout, (backup / 'baseline-error.log').open('wb') as stderr:
            migration = subprocess.run([str(RELEASE / 'leapview'), 'admin', 'initialize', '--format', 'json'],
                                       cwd=RELEASE, env=operation_env, stdout=stdout, stderr=stderr)
        if migration.returncode and 'already initialized' not in (backup / 'baseline-error.log').read_text():
            raise RuntimeError('Canonical baseline operation failed; private diagnostics retained with backup')
        assert sql('SELECT max(version_id) FROM public.goose_db_version WHERE is_applied') == '22'
        environment_file = RELEASE / 'runtime.env'
        environment_lines = []
        for name, value in sorted(runtime_env.items()):
            if name.startswith('LEAPVIEW_') or name in ('HOME', 'PATH', 'USER', 'LOGNAME', 'LANG', 'LC_ALL', 'SHELL', 'TMPDIR'):
                escaped = value.replace('\\', '\\\\').replace('"', '\\"').replace('$', '\\$').replace('`', '\\`')
                environment_lines.append(name + '="' + escaped + '"')
        write_private(environment_file, '\n'.join(environment_lines) + '\n')
        base_unit = '\n'.join(line for line in original_unit.splitlines()
                              if not line.startswith(('EnvironmentFile=', 'ExecStart=', 'WorkingDirectory=')))
        unit = base_unit + '\n\n[Service]\nEnvironmentFile=' + str(environment_file) + '\nExecStart=' + str(RELEASE / 'leapview') + ' serve --production\nWorkingDirectory=' + str(RELEASE) + '\n\n[Install]\nWantedBy=multi-user.target\n'
        write_private(UNIT, unit)
        subprocess.run(['systemctl', 'daemon-reload'], check=True)
        subprocess.run(['systemctl', 'reset-failed', SERVICE], check=False)
        subprocess.run(['systemctl', 'start', SERVICE], check=True)
        await_ready()
        new_pid = output('systemctl', 'show', SERVICE, '--property=MainPID', '--value')
        live = json.loads(output(f'/proc/{new_pid}/exe', 'version', '--json'))
        assert live['revision'] == REVISION
        with urllib.request.urlopen('https://demo.leapview.dev/readyz', timeout=15) as response:
            assert response.status == 200 and json.load(response)['status'] == 'ready'
        with urllib.request.urlopen('https://demo.leapview.dev/login', timeout=15) as response:
            assert response.status == 200
        subprocess.run(['systemctl', 'enable', SERVICE], check=True)
        write_private(backup / 'rollout-success.json', json.dumps({'revision': REVISION, 'image': IMAGE, 'schema': 22}))
        print(f'Deployed {REVISION}; readiness and login passed; rollback backup: {backup}', flush=True)
    except BaseException as rollout_error:
        if service_stopped:
            subprocess.run(['systemctl', 'stop', SERVICE], check=False, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            rollback_errors = []
            if migration_started:
                for database in ('leapview_control', 'leapview_ducklake'):
                    try:
                        with (backup / (database + '.dump')).open('rb') as handle:
                            subprocess.run(['docker', 'exec', '-i', DATABASE_CONTAINER, 'sh', '-c',
                                            'exec pg_restore -U "$POSTGRES_USER" --clean --if-exists --create --exit-on-error --dbname=template1'], stdin=handle, check=True)
                    except BaseException as error:
                        rollback_errors.append(f'{database} restore: {error}')
                try:
                    HOME_PATH.rename(backup / 'failed-home')
                    subprocess.run(['tar', '-xpf', str(backup / 'home.tar'), '-C', str(HOME_PATH.parent)], check=True)
                except BaseException as error:
                    rollback_errors.append(f'home restore: {error}')
            try:
                write_private(UNIT, original_unit)
                subprocess.run(['systemctl', 'daemon-reload'], check=True)
            except BaseException as error:
                rollback_errors.append(f'unit restore: {error}')
            try:
                subprocess.run(['systemctl', 'reset-failed', SERVICE], check=False)
                subprocess.run(['systemctl', 'start', SERVICE], check=True)
                await_ready()
            except BaseException as error:
                rollback_errors.append(f'predecessor restart: {error}')
            if rollback_errors:
                raise RuntimeError('rollout failed and rollback was incomplete: ' + '; '.join(rollback_errors)) from rollout_error
            print('Rollout failed; predecessor and original database state restored', flush=True)
        raise


if __name__ == '__main__':
    main()
