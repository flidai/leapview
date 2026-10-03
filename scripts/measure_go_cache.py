#!/usr/bin/env python3
"""Read-only hosted cache composition and controlled compiler reuse screening."""
import hashlib
import json
import os
from pathlib import Path
import subprocess
import time

out = Path(os.environ['RUNNER_TEMP']) / 'go-cache-assessment'
out.mkdir(exist_ok=True)

def run(*args):
    return subprocess.check_output(args, text=True).strip()

def inventory(root):
    sizes = [p.stat().st_size for p in root.rglob('*') if p.is_file() and not p.is_symlink()]
    return {'files': len(sizes), 'logicalBytes': sum(sizes)}

identity = json.loads(run('go', 'env', '-json', 'GOVERSION', 'GOOS', 'GOARCH', 'CGO_ENABLED', 'CC', 'CXX', 'CGO_CFLAGS', 'CGO_LDFLAGS', 'GOEXPERIMENT'))
mod = Path(run('go', 'env', 'GOMODCACHE'))
build = Path(run('go', 'env', 'GOCACHE'))
report = {
    'schemaVersion': 1, 'sourceRevision': run('git', 'rev-parse', 'HEAD'),
    'runID': os.environ['GITHUB_RUN_ID'], 'attempt': os.environ['GITHUB_RUN_ATTEMPT'],
    'workload': os.environ['CACHE_WORKLOAD'], 'compiler': identity,
    'inputSHA256': {p: hashlib.sha256(Path(p).read_bytes()).hexdigest() for p in ['flake.lock', 'nix/toolchain.nix', 'go.mod', 'go.sum', 'Taskfile.yml', '.github/actions/setup-ci/action.yml']},
    'restored': {'modules': inventory(mod), 'build': inventory(build)},
    'packages': ['./internal/platform/ci', './internal/analytics/query', './internal/recoveryset/capture'],
    'scope': 'compiler-only screening; no runtime-test or paired workflow speedup claim',
}
(out / 'assessment.json').write_text(json.dumps(report, indent=2) + '\n')
# Download is outside both compiler trials, with identical modules and flags.
env = dict(os.environ, GODEBUG='http2client=0')
subprocess.run(['go', 'mod', 'download'], env=env, check=True)
for label, cache in [('restored', build), ('empty-build-cache', out / 'empty-build-cache')]:
    cache.mkdir(exist_ok=True)
    log = out / f'{label}.log'
    start = time.monotonic()
    with log.open('w') as stream:
        result = subprocess.run(['go', 'test', '-x', '-run', '^$', '-count=1', '-tags=duckdb_arrow', *report['packages']], env=dict(os.environ, GOCACHE=str(cache)), stdout=stream, stderr=stream)
    content = log.read_text()
    report[label + 'Trial'] = {
        'seconds': time.monotonic() - start, 'exitCode': result.returncode,
        'compileInvocations': sum('/compile ' in line and not line.startswith('#') for line in content.splitlines()),
        'cgoInvocations': sum('/cgo ' in line and not line.startswith('#') for line in content.splitlines()),
        'logSHA256': hashlib.sha256(log.read_bytes()).hexdigest(),
    }
    (out / 'assessment.json').write_text(json.dumps(report, indent=2) + '\n')
    if result.returncode:
        raise SystemExit(result.returncode)
print(json.dumps(report, indent=2))
