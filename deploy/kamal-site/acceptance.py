#!/usr/bin/env python3
"""Freeze public-site acceptance inputs and verify a complete observation."""
import argparse
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys
import types

DURATION = 86400
PROBE_INTERVAL = 60
HOST_INTERVAL = 900
GAP_GRACE = 15
PUBLIC_SAMPLES = 1441
HOST_SAMPLES = 97


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def read_file(path, *, private=True, source=False):
    """Open a regular operator-owned input without following a final symlink."""
    fd = os.open(str(path), os.O_RDONLY | getattr(os, 'O_NOFOLLOW', 0) | os.O_NONBLOCK)
    with os.fdopen(fd, 'rb') as stream:
        st = os.fstat(stream.fileno())
        if not stat.S_ISREG(st.st_mode) or st.st_uid != os.getuid():
            raise ValueError('input must be an operator-owned regular file')
        if st.st_mode & (0o077 if private else (0o002 if source else 0o022)):
            raise ValueError('input permissions are too broad')
        if st.st_size > 200_000_000:
            raise ValueError('input exceeds acceptance size limit')
        return stream.read()


def private_directory(path):
    st = path.lstat()
    if not stat.S_ISDIR(st.st_mode) or st.st_uid != os.getuid() or st.st_mode & 0o077:
        raise ValueError('directory must be operator-owned and mode 0700')


def write_new(path, raw, mode=0o600):
    fd = os.open(str(path), os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, 'O_NOFOLLOW', 0), mode)
    with os.fdopen(fd, 'wb') as stream:
        stream.write(raw)
        stream.flush()
        os.fsync(stream.fileno())
    os.chmod(path, mode)
    directory_fd = os.open(str(path.parent), os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(directory_fd)
    finally:
        os.close(directory_fd)


def read_json(path):
    return json.loads(read_file(path))


def timestamp(value):
    if not isinstance(value, str) or not value.endswith('Z'):
        raise ValueError('UTC timestamp required')
    return dt.datetime.fromisoformat(value[:-1] + '+00:00')


def origin(raw, *, allow_http=False):
    from urllib.parse import urlsplit
    value = urlsplit(raw)
    if (value.scheme not in (('http', 'https') if allow_http else ('https',))
            or not value.hostname or value.username or value.password
            or value.path not in ('', '/') or value.query or value.fragment):
        raise ValueError('canonical public origin required')
    return value.scheme + '://' + value.netloc


def observer_module(raw, filename):
    module = types.ModuleType('frozen_site_observer')
    module.__file__ = str(filename)
    exec(compile(raw, str(filename), 'exec'), module.__dict__)
    return module


def process_alive(observer, process):
    try:
        current = observer.proc_identity(process['pid'])
        return (current['state'] not in ('Z', 'X') and current['start_ticks'] == process['start_ticks']
                and observer.proc_boot_id() == process['boot_id'])
    except (KeyError, OSError, ValueError):
        return False


def expected_config(summary):
    if (summary.get('schema') != 1 or summary.get('duration_seconds') != DURATION
            or summary.get('probe_interval_seconds') != PROBE_INTERVAL
            or summary.get('host_interval_seconds') != HOST_INTERVAL
            or summary.get('scope') != 'health_and_storage_observation_only'
            or summary.get('rollout_acceptance') != 'incomplete_public_adoption_smoke_required'
            or summary.get('failure') is not None):
        raise ValueError('observer does not implement the full health/storage contract')
    result = {key: summary[key] for key in ('identity', 'prior_version', 'active_image_id',
                                          'prior_image_id', 'min_free_bytes', 'min_free_inodes',
                                          'input_files', 'target', 'base_url', 'observer_sha256')}
    for key in ('min_free_bytes', 'min_free_inodes'):
        if type(result[key]) is not int or result[key] <= 0:
            raise ValueError('positive qualified reserve thresholds required')
    identity = result['identity']
    if (not re.fullmatch(r'ghcr\.io/flidai/leapview-site@sha256:[a-f0-9]{64}', identity.get('image', ''))
            or identity.get('version') != 'k' + identity['image'].split(':')[-1]
            or not re.fullmatch(r'[a-f0-9]{40}', identity.get('revision', ''))
            or not re.fullmatch(r'k[a-f0-9]{64}', result['prior_version'])
            or result['prior_version'] == identity['version']):
        raise ValueError('distinct admitted active/prior identities required')
    for key in ('active_image_id', 'prior_image_id'):
        if not re.fullmatch(r'sha256:[a-f0-9]{64}', result[key]):
            raise ValueError('full local image identities required')
    if result['active_image_id'] == result['prior_image_id']:
        raise ValueError('distinct active/prior local images required')
    if origin(result['base_url']) != result['base_url']:
        raise ValueError('observer public origin is not canonical')
    return result


def check_input_pins(expected):
    inputs = expected['input_files']
    if set(inputs) != {'record', 'ssh_config', 'fingerprint_pin'}:
        raise ValueError('record, SSH config and fingerprint pins required')
    for item in inputs.values():
        if (not isinstance(item, dict) or set(item) != {'path', 'sha256'}
                or not Path(item['path']).is_absolute()
                or not re.fullmatch(r'[a-f0-9]{64}', item['sha256'])
                or digest(read_file(item['path'])) != item['sha256']):
            raise ValueError('observer input pin mismatch')


def smoke_marker(base_url, public):
    return 'public adoption smoke passed for ' + base_url + ' and ' + public['image']


def check_start_hashes(raw, sources):
    lines = raw.decode().splitlines()
    if len(lines) != 3:
        raise ValueError('start smoke must pin exactly three inputs')
    pins = {}
    for line in lines:
        match = re.fullmatch(r'([a-f0-9]{64})  (.+)', line)
        if not match or match[2] in pins:
            raise ValueError('invalid or duplicate start smoke hash entry')
        pins[match[2]] = match[1]
    for argument, content in sources:
        labels = {str(argument), str(argument.resolve())}
        if argument.name == 'public_site_smoke.ts':
            labels.add('scripts/public_site_smoke.ts')
        matched = labels.intersection(pins)
        if len(matched) != 1 or pins.pop(matched.pop()) != digest(content):
            raise ValueError('start smoke hash does not match selected input')
    if pins:
        raise ValueError('unexpected start smoke input')


def prepare(args):
    bundle = Path(args.output_dir)
    private_directory(bundle.parent)
    run = Path(args.run_dir).absolute()
    private_directory(run)
    summary = read_json(run / 'summary.json')
    expected = expected_config(summary)
    check_input_pins(expected)
    source = Path(args.observer_source)
    observer_raw = read_file(source, private=False, source=True)
    if digest(observer_raw) != expected['observer_sha256']:
        raise ValueError('observer source differs from the recorded run')
    observer = observer_module(observer_raw, source)
    actual_summary, status = observer.status(run)
    if status != 'running' or actual_summary != summary or summary['samples'] < 1 or summary['host_samples'] < 1:
        raise ValueError('prepare requires a running observer with its first public and host samples')
    process_raw = read_file(run / 'process.json')
    if json.loads(process_raw).get('observer_sha256') != expected['observer_sha256']:
        raise ValueError('observer process source differs from the recorded run')
    sources = [(Path(args.smoke_script), read_file(args.smoke_script, private=False, source=True)),
               (Path(args.public_manifest), read_file(args.public_manifest)),
               (Path(args.desktop_manifest), read_file(args.desktop_manifest))]
    public = json.loads(sources[1][1])
    desktop = json.loads(sources[2][1])
    record = read_json(expected['input_files']['record']['path'])
    if (observer.read_record(expected['input_files']['record']['path']) != expected['identity']
            or record.get('release') != public):
        raise ValueError('public manifest differs from the admitted active release')
    if desktop.get('schemaVersion') != 1 or desktop.get('status') not in ('preparing', 'published', 'withdrawn'):
        raise ValueError('desktop status manifest required')
    start_log = read_file(args.start_smoke_log)
    start_hashes = read_file(args.start_smoke_hashes)
    check_start_hashes(start_hashes, sources)
    marker = smoke_marker(expected['base_url'], public)
    if marker.encode() not in start_log:
        raise ValueError('observation-start smoke did not record success')
    aliases = args.alias
    if aliases is None and expected['base_url'] == 'https://leapview.dev':
        aliases = ['http://leapview.dev', 'https://www.leapview.dev']
    if not aliases:
        raise ValueError('public redirect aliases must be supplied for this origin')
    aliases = [origin(alias, allow_http=True) for alias in aliases]
    started = timestamp(summary['started_at'])
    smoke_finished = Path(args.start_smoke_log).stat().st_mtime
    if not 0 <= started.timestamp() + 1 - smoke_finished <= 301:
        raise ValueError('start smoke must finish within five minutes before observation start')
    bun_path = Path(args.bun)
    if not bun_path.stat().st_mode & 0o111:
        raise ValueError('Bun input must be executable')
    files = {'observe.py': observer_raw, 'acceptance.py': read_file(__file__, private=False, source=True),
             'public_site_smoke.ts': sources[0][1], 'public-release.json': sources[1][1],
             'desktop-release.json': sources[2][1], 'start-smoke.log': start_log,
             'start-smoke.sha256': start_hashes, 'bun': read_file(bun_path, private=False, source=True)}
    os.mkdir(bundle, 0o700)
    for name, raw in files.items():
        write_new(bundle / name, raw, 0o700 if name == 'bun' else 0o600)
    config = {'schema': 1, 'run_dir': str(run), 'started_at': summary['started_at'],
              'start_smoke_finished_at': dt.datetime.fromtimestamp(smoke_finished, dt.timezone.utc).isoformat(),
              'expected_end': (started + dt.timedelta(seconds=DURATION)).isoformat().replace('+00:00', 'Z'),
              'expected': expected, 'process_sha256': digest(process_raw), 'aliases': aliases,
              'smoke_marker': marker, 'files': {name: digest(raw) for name, raw in files.items()}}
    raw = (json.dumps(config, sort_keys=True, separators=(',', ':')) + '\n').encode()
    write_new(bundle / 'config.json', raw)
    write_new(bundle / 'config.json.sha256', (digest(raw) + '\n').encode())
    return {'status': 'prepared', 'bundle': str(bundle.absolute()), 'expected_end': config['expected_end']}


def load_bundle(path):
    bundle = Path(path).absolute()
    private_directory(bundle)
    raw = read_file(bundle / 'config.json')
    if read_file(bundle / 'config.json.sha256').decode() != digest(raw) + '\n':
        raise ValueError('acceptance configuration hash mismatch')
    config = json.loads(raw)
    required = {'observe.py', 'acceptance.py', 'public_site_smoke.ts', 'public-release.json',
                'desktop-release.json', 'start-smoke.log', 'start-smoke.sha256', 'bun'}
    if config.get('schema') != 1 or set(config['files']) != required:
        raise ValueError('unsupported acceptance bundle')
    for name, expected in config['files'].items():
        if digest(read_file(bundle / name)) != expected:
            raise ValueError('frozen acceptance input hash mismatch: ' + name)
    if digest(read_file(__file__, private=False, source=True)) != config['files']['acceptance.py']:
        raise ValueError('acceptance runner differs from frozen source')
    if not (bundle / 'bun').stat().st_mode & 0o111:
        raise ValueError('frozen Bun lacks execute permission')
    check_input_pins(config['expected'])
    run = Path(config['run_dir'])
    private_directory(run)
    if digest(read_file(run / 'process.json')) != config['process_sha256']:
        raise ValueError('observer process identity changed')
    observer = observer_module(read_file(bundle / 'observe.py'), bundle / 'observe.py')
    summary, status = observer.status(run)
    if expected_config(summary) != config['expected'] or summary['started_at'] != config['started_at']:
        raise ValueError('observer identity or observation inputs changed')
    if timestamp(config['expected_end']) != timestamp(config['started_at']) + dt.timedelta(seconds=DURATION):
        raise ValueError('acceptance end does not cover a full 24 hours')
    return bundle, config, observer, summary, status


def preflight(path):
    _, config, observer, summary, status = load_bundle(path)
    if status not in ('running', 'health_storage_passed') or summary['samples'] < 1 or summary['host_samples'] < 1:
        raise ValueError('observer failed, interrupted, or lacks its first samples')
    if status == 'health_storage_passed':
        validate_complete(config, observer, summary, status)
    return {'status': 'preflight_passed', 'observer_status': status,
            'expected_end': config['expected_end'], 'rollout_acceptance': 'pending'}


def validate_complete(config, observer, summary, status):
    if (status != 'health_storage_passed' or summary.get('failure') is not None
            or summary.get('samples') != PUBLIC_SAMPLES or summary.get('host_samples') != HOST_SAMPLES
            or summary.get('elapsed_seconds', 0) < DURATION
            or timestamp(summary['ended_at']) < timestamp(config['expected_end'])):
        raise ValueError('full 24-hour health/storage observation has not passed')
    process = read_json(Path(config['run_dir']) / 'process.json')
    if process_alive(observer, process):
        raise ValueError('observer is still running after reporting completion')
    raw = read_file(Path(config['run_dir']) / 'samples.jsonl')
    lines = raw.splitlines()
    if len(lines) != PUBLIC_SAMPLES:
        raise ValueError('observation event log is incomplete')
    expected = config['expected']
    baseline = None
    for index, line in enumerate(lines):
        event = json.loads(line)
        scheduled = timestamp(config['started_at']) + dt.timedelta(seconds=PROBE_INTERVAL * index)
        if (event.get('type') != 'sample' or event.get('index') != index or event.get('failures') != []
                or timestamp(event['scheduled_at']) != scheduled
                or not scheduled <= timestamp(event['observed_at']) <= scheduled + dt.timedelta(seconds=GAP_GRACE)
                or event.get('host_error') is not None):
            raise ValueError('observation contains a missing, late, or failed sample')
        elapsed = event.get('elapsed_seconds')
        execution = event.get('sample_execution_ms')
        if (not isinstance(elapsed, (int, float)) or not index * PROBE_INTERVAL <= elapsed <= index * PROBE_INTERVAL + GAP_GRACE
                or type(execution) is not int or not 0 <= execution <= GAP_GRACE * 1000
                # Both fields round independently to milliseconds. Allow only
                # that rounding error when reconstructing the sample end.
                or elapsed + execution / 1000 > index * PROBE_INTERVAL + GAP_GRACE + 0.001):
            raise ValueError('observation sample exceeded its monotonic timing budget')
        http = event['http']
        if any(http.get(name, {}).get('ok') is not True or http[name].get('status') != 200
               or http[name].get('canonical_url') is not True for name in ('healthz', 'readyz', 'build')):
            raise ValueError('observation contains an unsuccessful public check')
        if http['build'].get('image') != expected['identity']['image'] or http['build'].get('revision') != expected['identity']['revision']:
            raise ValueError('public build identity drifted during observation')
        if (event.get('host') is not None) != (index % (HOST_INTERVAL // PROBE_INTERVAL) == 0):
            raise ValueError('host observation cadence is incomplete')
        if event.get('host') is not None:
            errors, baseline = observer.validate_host(event['host'], expected, baseline)
            if errors:
                raise ValueError('host observation does not meet the retained-image and storage contract')
    last = json.loads(lines[-1])
    if last['observed_at'] != summary['last_sample_at'] or timestamp(last['observed_at']) < timestamp(config['expected_end']):
        raise ValueError('final observation sample does not cover the required end')
    return digest(raw), baseline


def check_current(observer, expected, baseline):
    http = observer.probe_public(expected['identity'], expected['base_url'])
    if any(http.get(name, {}).get('ok') is not True for name in ('healthz', 'readyz', 'build')):
        raise ValueError('current public identity or health differs from accepted observation')
    inputs = expected['input_files']
    host = observer.collect_host(inputs['ssh_config']['path'], inputs['fingerprint_pin']['path'],
                                 inputs['ssh_config']['sha256'], inputs['fingerprint_pin']['sha256'],
                                 expected['identity'], expected['prior_version'], expected['active_image_id'],
                                 expected['prior_image_id'], expected['min_free_bytes'], expected['min_free_inodes'],
                                 expected['target'])
    errors, _ = observer.validate_host(host, expected, baseline)
    if errors:
        raise ValueError('current host differs from accepted observation')


def accept(path):
    bundle = Path(path).absolute()
    private_directory(bundle)
    # The exclusive claim makes attempts one-shot. Keep failed/interrupted
    # evidence; prepare a new bundle to retry instead of overwriting receipts.
    write_new(bundle / 'attempt.json', b'{"status":"started"}\n')
    receipt = {'schema': 1, 'status': 'failed', 'scope': 'observation_and_boundary_smokes_only',
               'rollout_acceptance': 'final_retention_and_recovery_audit_required',
               'production_mutations': 'none'}
    try:
        _, config, observer, summary, status = load_bundle(bundle)
        event_sha, baseline = validate_complete(config, observer, summary, status)
        if dt.datetime.now(dt.timezone.utc) < timestamp(config['expected_end']):
            raise ValueError('acceptance cannot run before the observation ends')
        receipt['samples_sha256'] = event_sha
        check_current(observer, config['expected'], baseline)
        env = {name: os.environ[name] for name in ('HOME', 'PATH', 'LANG', 'LC_ALL', 'TZ') if name in os.environ}
        env.update(LEAPVIEW_PUBLIC_SITE_URL=config['expected']['base_url'],
                   LEAPVIEW_PUBLIC_SITE_ALIASES=','.join(config['aliases']),
                   LEAPVIEW_PUBLIC_RELEASE_MANIFEST=str(bundle / 'public-release.json'),
                   LEAPVIEW_DESKTOP_RELEASE_MANIFEST=str(bundle / 'desktop-release.json'))
        with (bundle / 'end-smoke.log').open('xb') as log:
            os.chmod(bundle / 'end-smoke.log', 0o600)
            result = subprocess.run([str(bundle / 'bun'), 'run', str(bundle / 'public_site_smoke.ts')],
                                    cwd=str(bundle), env=env, stdout=log, stderr=subprocess.STDOUT,
                                    timeout=900, check=False)
            log.flush()
            os.fsync(log.fileno())
        log_raw = read_file(bundle / 'end-smoke.log')
        if result.returncode or config['smoke_marker'].encode() not in log_raw:
            raise ValueError('observation-end public adoption smoke failed')
        load_bundle(bundle)
        check_current(observer, config['expected'], baseline)
        receipt.update(status='passed', end_smoke_sha256=digest(log_raw),
                       config_sha256=digest(read_file(bundle / 'config.json')))
    except Exception as exc:
        receipt['failure'] = type(exc).__name__
    write_new(bundle / 'receipt.json', (json.dumps(receipt, sort_keys=True) + '\n').encode())
    return receipt


def main(argv=None):
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest='command', required=True)
    prepare_cmd = commands.add_parser('prepare')
    for name in ('run-dir', 'observer-source', 'smoke-script', 'public-manifest', 'desktop-manifest',
                 'start-smoke-log', 'start-smoke-hashes', 'bun', 'output-dir'):
        prepare_cmd.add_argument('--' + name, required=True)
    prepare_cmd.add_argument('--alias', action='append')
    for name in ('preflight', 'run', 'status'):
        commands.add_parser(name).add_argument('--bundle', required=True)
    args = parser.parse_args(argv)
    try:
        if args.command == 'prepare':
            result = prepare(args)
        elif args.command == 'preflight':
            result = preflight(args.bundle)
        elif args.command == 'run':
            result = accept(args.bundle)
        else:
            bundle = Path(args.bundle)
            private_directory(bundle)
            result = read_json(bundle / 'receipt.json') if (bundle / 'receipt.json').exists() else {
                'status': 'interrupted' if (bundle / 'attempt.json').exists() else 'not_started'}
        print(json.dumps(result, sort_keys=True))
        return 1 if result.get('status') in ('failed', 'interrupted') else 0
    except Exception as exc:
        print('acceptance rejected: ' + str(exc), file=sys.stderr)
        return 1


if __name__ == '__main__':
    sys.exit(main())
