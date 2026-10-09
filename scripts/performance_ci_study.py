#!/usr/bin/env python3
"""Bounded compile-once screening; never changes the maintained CI runner."""
import json
import os
from pathlib import Path
import re
import resource
import subprocess
import sys
import time

from performance_capacity import digest, identity, resources, oom_count, write
from performance_process import run_owned

PACKAGE = 'github.com/flidai/leapview/internal/app'
SKIP = '^TestMinIOParquetSourceRefreshContract$'


def environment(cache):
    return {**os.environ, 'GOFLAGS': '-tags=duckdb_arrow -p=1 -mod=readonly',
            'GOENV': 'off', 'GOWORK': 'off', 'GOTOOLCHAIN': 'local', 'CGO_ENABLED': '1',
            'GOMAXPROCS': '2', 'GOMEMLIMIT': '2GiB', 'GOCACHE': str(cache),
            'LEAPVIEW_POSTGRES_CONFORMANCE_SKIP': '1'}


def inputs():
    return json.loads(subprocess.check_output(['node', 'scripts/performance_ci_inputs.mjs'],
        env=environment(os.environ.get('GOCACHE', '/tmp/leapview-ci-input-probe')),
        text=True, timeout=180))


def pattern(text):
    text = text.strip()
    if not text.startswith('^(?:') or not text.endswith(')$') or '\n' in text:
        raise ValueError('testshard did not return one exact pattern')
    re.compile(text)
    return text


def validate_coverage(patterns, rows):
    if len(patterns) != 4 or len(rows) != 4:
        raise ValueError('all four stable shards must execute')
    selected = []
    flattened = []
    for expression, shard in zip(patterns, rows):
        # The maintained tool uses quoted Go identifiers, so this fixed format
        # is unambiguous; do not accept arbitrary regex language as inventory.
        names = expression.removeprefix('^(?:').removesuffix(')$').split('|')
        if not names or any(not re.fullmatch(r'Test[A-Za-z0-9_]+', name) for name in names):
            raise ValueError('unexpected shard inventory')
        expected = set(names) - {'TestMinIOParquetSourceRefreshContract'}
        observed = {row['test'] for row in shard if '/' not in row['test']}
        if expected != observed:
            raise ValueError('discovery and actual named execution disagree')
        selected.extend(names)
        flattened.extend(shard)
    if len(selected) != len(set(selected)) or len(flattened) != len({row['test'] for row in flattened}):
        raise ValueError('duplicate selected or executed tests across shards')
    return sorted(flattened, key=lambda row: row['test'])


def arm(root, output, cache, mode):
    """A fresh process gives each arm its own complete child CPU/RSS accounting."""
    output.mkdir(exist_ok=False)
    env = environment(cache)
    started = time.monotonic()
    before = resources()
    steps = []

    def execute(name, command, cwd=None):
        path = output / (name + '.log')
        step_start = time.monotonic()
        with path.open('xb') as log:
            os.chdir(cwd or root)
            try:
                receipt = run_owned(command, stdout=log, env=env, timeout=900)
            finally:
                os.chdir(root)
        receipt.update(command=command, cwd=str(cwd or root), wallSeconds=time.monotonic() - step_start,
                       logSHA256=digest(path))
        steps.append({'name': name, **receipt})
        write(output / (name + '-execution.json'), receipt)
        if receipt['exitCode'] or receipt['terminationReason'] or not receipt['cleanupComplete']:
            raise ValueError('first failed/timeout/resource/cleanup step: ' + name)
        return path

    patterns, executions = [], []
    failure = None
    try:
        if mode == 'compile-once':
            binary, tool = output / 'app.test', output / 'testshard'
            execute('compile-app', ['go', 'test', '-c', '-o', str(binary), './internal/app'])
            execute('compile-sharder', ['go', 'build', '-o', str(tool), './internal/app/tools/testshard'])
            listing = execute('discover-all', [str(binary), '-test.list=^Test'], root / 'internal/app')
        for shard in range(4):
            selector = ['--shard-index', str(shard), '--shard-count', '4']
            command = (['go', 'run', './internal/app/tools/testshard', '--package', './internal/app'] + selector
                       if mode == 'maintained' else [str(tool), '--list-file', str(listing)] + selector)
            expressions = execute('discover-' + str(shard), command)
            selected = pattern(expressions.read_text())
            patterns.append(selected)
            command = (['go', 'test', '-json', '-count=1', '-timeout=10m', './internal/app', '-run', selected, '-skip', SKIP]
                       if mode == 'maintained' else ['go', 'tool', 'test2json', '-t', '-p', PACKAGE, str(binary),
                            '-test.run=' + selected, '-test.skip=' + SKIP, '-test.count=1',
                            '-test.timeout=10m', '-test.v=test2json'])
            log = execute('execute-' + str(shard), command, None if mode == 'maintained' else root / 'internal/app')
            rows = json.loads(subprocess.check_output(['node', 'scripts/performance_ci_events.mjs', str(log)], text=True, timeout=30))
            executions.append(rows)
        coverage = validate_coverage(patterns, executions)
    except (ValueError, subprocess.SubprocessError) as error:
        failure = str(error)
        coverage = None
    usage = resource.getrusage(resource.RUSAGE_CHILDREN)
    after = resources()
    write(output / 'receipt.json', {'mode': mode, 'wallSeconds': time.monotonic() - started,
        'userCPUSeconds': usage.ru_utime, 'systemCPUSeconds': usage.ru_stime,
        'maxResidentMemoryKiB': usage.ru_maxrss, 'resourceBefore': before, 'resourceAfter': after,
        'oomAccountingAvailable': oom_count(before) is not None and oom_count(after) is not None,
        'oomKillObserved': None if oom_count(before) is None or oom_count(after) is None else oom_count(before) != oom_count(after),
        'patterns': patterns, 'coverage': coverage, 'steps': steps, 'failure': failure})
    return 0 if failure is None else 1


def run(directory):
    root = Path.cwd()
    source, original_inputs = identity(), inputs()
    directory.mkdir(parents=True, exist_ok=False)
    write(directory / 'build-inputs.json', original_inputs)
    write(directory / 'protocol.json', {'source': source, 'kind': 'compile-once-screening',
        'pairsPerCondition': 1, 'conditions': ['cold', 'warm'],
        'order': [['cold', 'maintained'], ['cold', 'compile-once'], ['warm', 'compile-once'], ['warm', 'maintained']],
        'primary': 'whole-arm wallSeconds', 'guardrails': ['child CPU', 'max RSS', 'exact executed test/subtest/skip-reason parity'],
        'cache': 'separate initially empty owned Go build caches; each warm arm reuses only its own cache; dependency module cache stays warm',
        'selection': 'same four stable application shard patterns; same dedicated-lane MinIO exclusion and PostgreSQL skip environment',
        'environment': {'GOFLAGS': '-tags=duckdb_arrow -p=1 -mod=readonly', 'GOMAXPROCS': '2', 'GOMEMLIMIT': '2GiB'},
        'stop': 'first failed step,900s step timeout,4GiB observed RSS,OOM,cleanup failure,source/input drift,coverage or skip difference',
        'decision': 'one screening pair per condition cannot support adoption/statistical gain; keep maintained implementation',
        'limitation': 'serial application-only experiment; does not replace full CI or dedicated external/postgres/generation/frontend lanes',
        'buildInputsSHA256': digest(directory / 'build-inputs.json'), 'resources': resources()})
    caches = {}
    for mode in ('maintained', 'compile-once'):
        caches[mode] = directory / (mode + '-cache')
        caches[mode].mkdir()
    results, failure = [], None
    try:
        for condition, mode in [('cold', 'maintained'), ('cold', 'compile-once'), ('warm', 'compile-once'), ('warm', 'maintained')]:
            if identity() != source or inputs() != original_inputs:
                raise ValueError('source or actual build inputs changed')
            output = directory / (condition + '-' + mode)
            code = subprocess.run([sys.executable, str(Path(__file__).resolve()), '_arm', str(root),
                str(output), str(caches[mode]), mode]).returncode
            receipt = json.loads((output / 'receipt.json').read_text())
            results.append({'condition': condition, 'mode': mode, 'receiptSHA256': digest(output / 'receipt.json'), 'receipt': receipt})
            if code or receipt['failure'] or receipt['oomKillObserved']:
                raise ValueError('experiment stopped at ' + condition + '-' + mode)
            if len(results) > 1 and (receipt['patterns'] != results[0]['receipt']['patterns'] or
                    receipt['coverage'] != results[0]['receipt']['coverage']):
                raise ValueError('exact discovery/execution/skip parity differs between arms')
        if identity() != source or inputs() != original_inputs:
            raise ValueError('source or build inputs changed before final receipt')
    except (ValueError, subprocess.SubprocessError) as error:
        failure = str(error)
    write(directory / 'decision.json', {'source': source, 'result': 'stopped' if failure else 'screening-complete',
        'failure': failure, 'arms': results, 'adoption': 'none; retain maintained runner',
        'limitation': 'one pair per cache condition is descriptive/inconclusive, not statistical performance acceptance'})
    return 1 if failure else 0


if __name__ == '__main__':
    if len(sys.argv) == 6 and sys.argv[1] == '_arm':
        sys.exit(arm(Path(sys.argv[2]), Path(sys.argv[3]), Path(sys.argv[4]), sys.argv[5]))
    if len(sys.argv) != 2 or not Path(sys.argv[1]).is_absolute():
        sys.exit('usage: performance_ci_study.py NEW_ABSOLUTE_EVIDENCE_DIR')
    sys.exit(run(Path(sys.argv[1])))
