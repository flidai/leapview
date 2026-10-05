#!/usr/bin/env python3
"""Bounded orchestration closure publication, import and paired measurements."""
import argparse
from datetime import datetime
import gzip
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import time

ARCHIVE_BUDGET = 300_000_000
CACHE_PREFIX = 'nix-orchestration-v1-Linux-X64-2.31.2-'
SHELL = '.#devShells.x86_64-linux.orchestration'


def output(*args):
    return subprocess.check_output(args, text=True).strip()


def digest(path):
    with path.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def retention_plan(entries, key, ref, envelope_bytes):
    assert re.fullmatch(re.escape(CACHE_PREFIX) + r'[0-9a-f]{64}', key)
    assert 0 < envelope_bytes <= ARCHIVE_BUDGET
    scoped = [entry for entry in entries if entry['ref'] == ref and
              re.fullmatch(re.escape(CACHE_PREFIX) + r'[0-9a-f]{64}', entry['key']) and
              entry['key'] != key]
    for entry in scoped:
        assert type(entry['id']) is int and entry['id'] > 0
        assert type(entry['size_in_bytes']) is int and entry['size_in_bytes'] >= 0
        assert re.fullmatch(r'\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z', entry['created_at'])
    scoped.sort(key=lambda entry: (datetime.fromisoformat(entry['created_at']), entry['id']))
    retired = []
    while len(scoped) >= 3 or sum(entry['size_in_bytes'] for entry in scoped) + envelope_bytes > 900_000_000:
        retired.append(scoped.pop(0))
    return retired


def verify_producer():
    assert os.environ['GITHUB_REF'] == 'refs/heads/' + os.environ['DEFAULT_BRANCH']
    assert output('git', 'rev-parse', 'HEAD') == os.environ['GITHUB_SHA']


def enforce_retention(directory):
    verify_producer()
    manifest_path = directory / 'manifest.json'
    manifest = json.loads(manifest_path.read_text())
    validate_manifest(manifest, os.environ['CACHE_INPUT_ID'],
                      output('nix', 'eval', '--no-update-lock-file', '--raw', SHELL + '.outPath'))
    envelope_bytes = manifest['archiveBytes'] + manifest_path.stat().st_size + 1_048_576
    endpoint = f"repos/{os.environ['GITHUB_REPOSITORY']}/actions/caches"
    pages = json.loads(output('gh', 'api', '--paginate', '--slurp', '--method', 'GET', endpoint,
                             '-f', 'key=' + CACHE_PREFIX, '-f', 'ref=' + os.environ['GITHUB_REF']))
    entries = [entry for page in pages for entry in page['actions_caches']]
    retired = retention_plan(entries, os.environ['CACHE_KEY'], os.environ['GITHUB_REF'], envelope_bytes)
    receipt = {'envelopeBytes': envelope_bytes, 'inventory': entries, 'retired': []}
    receipt_path = directory.parent / 'orchestration-cache-retention.json'
    receipt_path.write_text(json.dumps(receipt, indent=2) + '\n')
    for entry in retired:
        subprocess.run(['gh', 'api', '--method', 'DELETE', endpoint + '/' + str(entry['id'])], check=True)
        receipt['retired'].append(entry)
        receipt_path.write_text(json.dumps(receipt, indent=2) + '\n')


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
    parser.add_argument('operation', choices=['produce', 'baseline', 'restore', 'retain'])
    args = parser.parse_args()
    directory = Path(os.environ['RUNNER_TEMP']) / 'orchestration-cache'
    directory.mkdir(exist_ok=True)
    if args.operation == 'retain':
        enforce_retention(directory)
        return
    if args.operation == 'produce':
        verify_producer()
    metrics_path = Path(os.environ['RUNNER_TEMP']) / 'orchestration-cache-metrics.json'
    metrics = {
        'schemaVersion': 1, 'operation': args.operation,
        'sourceRevision': output('git', 'rev-parse', 'HEAD'),
        'inputID': os.environ['CACHE_INPUT_ID'], 'runID': os.environ['GITHUB_RUN_ID'],
        'attempt': os.environ['GITHUB_RUN_ATTEMPT'], 'sample': os.environ.get('CACHE_SAMPLE', ''),
        'cacheHit': os.environ.get('CACHE_HIT') == 'true',
        'scope': 'orchestration toolchain only; complete locked realization required',
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
        except (AssertionError, OSError, ValueError, TypeError, KeyError, RuntimeError, subprocess.CalledProcessError) as error:
            # Cache absence/corruption must not change toolchain correctness.
            metrics['cacheImportError'] = type(error).__name__
        metrics['importSeconds'] = time.monotonic() - start
    start = time.monotonic()
    if args.operation == 'produce':
        # Develop realizes an environment derivation, leaving the shell output
        # absent on fresh stores. Inventory/export needs the actual shell root.
        subprocess.run(['nix', 'build', '--no-update-lock-file', '--no-link', SHELL], check=True)
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
