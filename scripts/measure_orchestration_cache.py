#!/usr/bin/env python3
"""Opt-in orchestration cache measurements; production setup stays unchanged."""
import argparse
import gzip
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import time

ARCHIVE_BUDGET = 300_000_000
SHELL = '.#devShells.x86_64-linux.orchestration'


def output(*args):
    return subprocess.check_output(args, text=True).strip()


def digest(path):
    with path.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def validate_manifest(manifest, input_id, root):
    assert manifest['schemaVersion'] == 1
    assert manifest['profile'] == 'orchestration'
    assert manifest['platform'] == 'Linux-X64'
    assert manifest['nixVersion'] == '2.31.2'
    assert manifest['inputID'] == input_id
    assert manifest['root'] == root
    assert re.fullmatch(r'[0-9a-f]{40}', manifest['producerRevision'])
    paths = manifest['paths']
    assert paths and len(paths) == len(set(paths)) and root in paths
    for path in paths:
        assert re.fullmatch(r'/nix/store/[a-z0-9]{32}-[^/]+', path)
        assert not path.endswith('-source') and 'leapview' not in path
    assert 0 < manifest['archiveBytes'] <= ARCHIVE_BUDGET
    assert re.fullmatch(r'[0-9a-f]{64}', manifest['archiveSHA256'])


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('operation', choices=['produce', 'baseline', 'restore'])
    args = parser.parse_args()
    directory = Path(os.environ['RUNNER_TEMP']) / 'orchestration-cache'
    directory.mkdir(exist_ok=True)
    metrics_path = Path(os.environ['RUNNER_TEMP']) / 'orchestration-cache-metrics.json'
    metrics = {
        'schemaVersion': 1, 'operation': args.operation,
        'sourceRevision': output('git', 'rev-parse', 'HEAD'),
        'inputID': os.environ['CACHE_INPUT_ID'], 'runID': os.environ['GITHUB_RUN_ID'],
        'attempt': os.environ['GITHUB_RUN_ATTEMPT'], 'sample': os.environ.get('CACHE_SAMPLE', ''),
        'cacheHit': os.environ.get('CACHE_HIT') == 'true',
        'scope': 'orchestration toolchain only; no production adoption',
    }
    start = time.monotonic()
    root = output('nix', 'eval', '--no-update-lock-file', '--raw', SHELL + '.outPath')
    metrics['evaluationSeconds'] = time.monotonic() - start
    archive = directory / 'closure.nar.gz'
    manifest_path = directory / 'manifest.json'
    if args.operation == 'restore' and metrics['cacheHit']:
        start = time.monotonic()
        try:
            manifest = json.loads(manifest_path.read_text())
            validate_manifest(manifest, metrics['inputID'], root)
            assert archive.stat().st_size == manifest['archiveBytes']
            assert digest(archive) == manifest['archiveSHA256']
            with gzip.open(archive, 'rb') as stream:
                process = subprocess.Popen(['nix-store', '--import'], stdin=subprocess.PIPE, stdout=subprocess.DEVNULL)
                try:
                    while chunk := stream.read(1024 * 1024):
                        process.stdin.write(chunk)
                    process.stdin.close()
                    if process.wait() != 0:
                        raise RuntimeError('Nix closure import failed')
                finally:
                    if process.poll() is None:
                        process.kill()
                        process.wait()
            imported = output('nix-store', '--query', '--requisites', root).splitlines()
            assert sorted(imported) == sorted(manifest['paths'])
            metrics['archiveBytes'] = manifest['archiveBytes']
        except (AssertionError, OSError, ValueError, KeyError, RuntimeError, subprocess.CalledProcessError) as error:
            # Cache absence/corruption must not change toolchain correctness.
            metrics['cacheImportError'] = type(error).__name__
        metrics['importSeconds'] = time.monotonic() - start
    start = time.monotonic()
    subprocess.run(['nix', 'develop', '--no-update-lock-file', '.#orchestration', '-c',
                    'python3', 'scripts/export_nix_ci_environment.py', '--profile', 'orchestration'], check=True)
    metrics['realizationAndExportSeconds'] = time.monotonic() - start
    if args.operation == 'produce':
        assert os.environ['GITHUB_REF'] == 'refs/heads/' + os.environ['DEFAULT_BRANCH']
        assert metrics['sourceRevision'] == os.environ['GITHUB_SHA']
        closure = json.loads(output('nix', 'path-info', '--no-update-lock-file', '--recursive', '--json', root))
        paths = sorted(closure)
        manifest = {
            'schemaVersion': 1, 'profile': 'orchestration', 'platform': 'Linux-X64',
            'nixVersion': '2.31.2', 'inputID': metrics['inputID'], 'root': root,
            'producerRevision': metrics['sourceRevision'], 'paths': paths,
            'archiveBytes': 1, 'archiveSHA256': '0' * 64,
        }
        validate_manifest(manifest, metrics['inputID'], root)
        raw = Path(os.environ['RUNNER_TEMP']) / 'orchestration-closure.nar'
        start = time.monotonic()
        with raw.open('wb') as stream:
            subprocess.run(['nix-store', '--export', *paths], stdout=stream, check=True)
        metrics['serializationSeconds'] = time.monotonic() - start
        metrics['rawBytes'] = raw.stat().st_size
        start = time.monotonic()
        with raw.open('rb') as source, archive.open('wb') as target:
            with gzip.GzipFile(filename='', mode='wb', fileobj=target, compresslevel=6, mtime=0) as compressed:
                while chunk := source.read(1024 * 1024):
                    compressed.write(chunk)
        metrics['compressionSeconds'] = time.monotonic() - start
        metrics['compressionLevel'] = 6
        raw.unlink()
        manifest['archiveBytes'] = archive.stat().st_size
        manifest['archiveSHA256'] = digest(archive)
        validate_manifest(manifest, metrics['inputID'], root)
        manifest_path.write_text(json.dumps(manifest, indent=2) + '\n')
        metrics.update(archiveBytes=manifest['archiveBytes'], closurePaths=len(paths),
                       closureNARBytes=sum(info['narSize'] for info in closure.values()))
    metrics_path.write_text(json.dumps(metrics, indent=2) + '\n')
    print(json.dumps(metrics, indent=2))


if __name__ == '__main__':
    main()
