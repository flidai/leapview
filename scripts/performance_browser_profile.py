#!/usr/bin/env python3
"""Three fresh instrumented route profiles of an admitted owned native fixture."""
import json
import os
from pathlib import Path
import subprocess
import sys
from urllib.parse import urlparse

from performance_capacity import digest, identity, resources, oom_count, write
from performance_process import run_owned


def admit_server(source):
    path = Path(os.environ['LEAPVIEW_BROWSER_PROFILE_SERVER_RECEIPT'])
    if path.stat().st_size > 1 << 20:
        raise ValueError('server admission receipt is too large')
    receipt = json.loads(path.read_text())
    if receipt.get('source') != source:
        raise ValueError('native server source must match the frozen study source')
    binary = receipt['binary']
    if digest(binary['path']) != binary['sha256']:
        raise ValueError('native server binary differs from admission')
    version = json.loads(subprocess.check_output([binary['path'], 'version', '--format', 'json'], text=True, timeout=30))
    if version.get('product') != 'leapview' or version.get('revision') != source['commit'] or version.get('dirty') is not False:
        raise ValueError('native binary version does not identify the frozen clean source')
    pid = receipt['pid']
    if isinstance(pid, bool) or not isinstance(pid, int) or pid < 1:
        raise ValueError('invalid owned native server pid')
    actual = Path('/proc') / str(pid)
    if digest(actual / 'exe') != binary['sha256'] or actual.joinpath('stat').read_text().rsplit(')', 1)[1].split()[19] != receipt['processStart']:
        raise ValueError('admitted native server process changed')
    url = urlparse(os.environ['LEAPVIEW_BASE_URL'])
    if url.scheme != 'http' or url.hostname not in ('localhost', '127.0.0.1', '::1') or receipt['baseURL'] != os.environ['LEAPVIEW_BASE_URL']:
        raise ValueError('study requires the exact admitted local native fixture URL')
    inodes = set()
    for fd in actual.joinpath('fd').iterdir():
        try:
            target = os.readlink(fd)
            if target.startswith('socket:['):
                inodes.add(target[8:-1])
        except FileNotFoundError:
            pass
    port = url.port or 80
    owns_port = False
    for protocol in ('tcp', 'tcp6'):
        for line in actual.joinpath('net', protocol).read_text().splitlines()[1:]:
            row = line.split()
            if row[3] == '0A' and int(row[1].split(':')[1], 16) == port and row[9] in inodes:
                owns_port = True
    if not owns_port:
        raise ValueError('admitted native server does not own the fixture listening port')
    files = receipt['datasetFiles']
    if not isinstance(files, list) or not files:
        raise ValueError('fixed dataset file identity is required')
    root = Path(receipt['datasetRoot']).resolve()
    actual_files = {str(path.resolve()) for path in root.glob('*.csv')}
    if {str(Path(file['path']).resolve()) for file in files} != actual_files or len(files) != len(actual_files):
        raise ValueError('dataset manifest must cover every CSV input exactly once')
    for file in files:
        if digest(file['path']) != file['sha256']:
            raise ValueError('fixed dataset bytes changed')
    return {'receiptSHA256': digest(path), 'source': source, 'binarySHA256': binary['sha256'],
            'pid': pid, 'processStart': receipt['processStart'],
            'datasetRoot': str(root), 'datasetFiles': files, 'baseURL': receipt['baseURL']}


def run(directory):
    source = identity()
    server = admit_server(source)
    directory.mkdir(parents=True, exist_ok=False)
    write(directory / 'protocol.json', {'source': source, 'server': server, 'resources': resources(),
        'freshBrowserProcesses': 3, 'routes': ['visual-showcase/overview', 'visual-showcase/tables', 'visual-showcase/chart-map'],
        'primary': 'instrumented rendering/parse trace and actual raw/encoded/decoded payload sizes',
        'operations': 'one route navigation, four virtual table scroll positions, three governed tiled maps, client stream teardown per session; rapid filters use the separate maintained five-session study',
        'stop': 'first failed, timeout1200s,observed4GiB RSS,cleanup,source/server/dataset identity or correctness failure',
        'decision': 'no optimization candidate and no adoption; three fixed profiles do not support user p95 or capacity claims',
        'trace': 'observer overhead applies; local raw traces remain private, without automatic artifact upload'})
    profiles, failure = [], None
    try:
        for session in range(1, 4):
            if identity() != source or admit_server(source) != server:
                raise ValueError('source or admitted native fixture changed')
            output = directory / ('session-' + str(session))
            before = resources()
            with (directory / ('session-' + str(session) + '.log')).open('xb') as log:
                execution = run_owned(['bun', 'scripts/performance_browser_profile.ts'], stdout=log, timeout=1200,
                    env={**os.environ, 'LEAPVIEW_BROWSER_PROFILE_OUTPUT': str(output)})
            after = resources()
            execution.update(resourceBefore=before, resourceAfter=after,
                oomAccountingAvailable=oom_count(before) is not None and oom_count(after) is not None,
                oomKillObserved=None if oom_count(before) is None or oom_count(after) is None else oom_count(before) != oom_count(after))
            write(directory / ('session-' + str(session) + '-execution.json'), execution)
            if execution['exitCode'] or execution['terminationReason'] or execution['oomKillObserved']:
                raise ValueError('browser profile failed: ' + str(execution))
            report = json.loads((output / 'profile.json').read_text())
            if report['failure'] or [row['name'] for row in report['records']] != ['dense', 'tables', 'maps']:
                raise ValueError('incomplete route profile')
            profiles.append({'session': session, 'reportSHA256': digest(output / 'profile.json')})
        if identity() != source or admit_server(source) != server:
            raise ValueError('source or admitted native fixture changed before final receipt')
    except (ValueError, subprocess.SubprocessError) as error:
        failure = str(error)
    write(directory / 'decision.json', {'source': source, 'result': 'stopped' if failure else 'profiled',
        'failure': failure, 'profiles': profiles, 'adoption': 'none',
        'limitation': 'instrumented fixed local Olist fixture; no p95/comparison/capacity claim or server-side resource teardown proof'})
    return 1 if failure else 0


if __name__ == '__main__':
    if len(sys.argv) != 2 or not Path(sys.argv[1]).is_absolute():
        sys.exit('usage: performance_browser_profile.py NEW_ABSOLUTE_EVIDENCE_DIR')
    sys.exit(run(Path(sys.argv[1])))
