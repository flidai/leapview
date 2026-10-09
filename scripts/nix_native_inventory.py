#!/usr/bin/env python3
"""Recover reviewed native source identities without claiming binary coverage.

Source inventories are useful scanner inputs, but neither a signed extension's
root revision nor a Cargo lock proves its entire compiled dependency closure.
The admission entrypoint refuses unresolved build edges from repository policy.
"""

import argparse
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timezone
import hashlib
import json
from pathlib import Path, PurePosixPath
import re
import sys
import tomllib
import urllib.request

import nix_candidate_manifest as candidate

ROOT = Path(__file__).resolve().parents[1]
POLICY_PATH = ROOT / 'nix/native-component-lock.json'
MAX_FILE_BYTES = 128 * 1024**2
MAX_FILES = 4096
SHA256 = re.compile(r'^sha256:[0-9a-f]{64}$')
REVISION = re.compile(r'^[0-9a-f]{40}$')


def digest(data):
    return 'sha256:' + hashlib.sha256(data).hexdigest()


def regular_file(directory, name):
    if not isinstance(name, str) or not name:
        raise ValueError('native evidence path must be nonempty')
    relative = PurePosixPath(name)
    if relative.is_absolute() or any(part in ('.', '..') for part in relative.parts) or str(relative) != name:
        raise ValueError('native evidence path must be canonical and relative: ' + name)
    directory = Path(directory)
    if directory.is_symlink() or not directory.is_dir():
        raise ValueError('native evidence root must be a regular directory')
    path = directory
    for part in relative.parts:
        path /= part
        if path.is_symlink():
            raise ValueError('native evidence cannot traverse symlinks: ' + name)
    if not path.is_file() or path.stat().st_size > MAX_FILE_BYTES:
        raise ValueError('native evidence must be a bounded regular file: ' + name)
    return path


def cargo_packages(data):
    lock = tomllib.loads(data.decode('utf-8'))
    if lock.get('version') not in (3, 4) or not isinstance(lock.get('package'), list):
        raise ValueError('unsupported Cargo lock inventory')
    packages, identities = [], set()
    for package in lock['package']:
        name, version = package.get('name'), package.get('version')
        source = package.get('source', 'workspace')
        if not all(isinstance(value, str) and value for value in (name, version, source)):
            raise ValueError('Cargo package identity is incomplete')
        identity = (name, version, source)
        if identity in identities:
            raise ValueError('duplicate Cargo package identity')
        identities.add(identity)
        checksum = package.get('checksum')
        if source.startswith('registry+'):
            if not isinstance(checksum, str) or not re.fullmatch(r'[0-9a-f]{64}', checksum):
                raise ValueError('Cargo registry checksum is missing: ' + name)
        elif source.startswith('git+'):
            if not REVISION.fullmatch(source.rsplit('#', 1)[-1]):
                raise ValueError('Cargo Git source revision is not exact: ' + name)
        elif source != 'workspace':
            raise ValueError('unsupported Cargo source: ' + name)
        dependencies = package.get('dependencies', [])
        if not isinstance(dependencies, list) or not all(isinstance(value, str) for value in dependencies):
            raise ValueError('Cargo dependency edges are malformed')
        packages.append({'name': name, 'version': version, 'source': source,
                         'checksum': checksum, 'dependencies': dependencies})
    return packages


def source_inventory(directory, policy):
    if policy.get('version') != 1 or not isinstance(policy.get('sources'), list) or not policy['sources']:
        raise ValueError('native source policy is missing or unsupported')
    sources, hashes = [], {}
    for source in policy['sources']:
        if not REVISION.fullmatch(source.get('sourceRevision', '')):
            raise ValueError('native source policy revision must be exact')
        files = source.get('files')
        if not isinstance(files, list) or not files:
            raise ValueError('native source policy omits source evidence: ' + source['name'])
        record = {'name': source['name'], 'repository': source['repository'],
                  'sourceRevision': source['sourceRevision'], 'files': [],
                  'cargoPackages': [], 'vcpkgManifests': []}
        for proof in files:
            name = proof['path']
            if name in hashes or len(hashes) >= MAX_FILES:
                raise ValueError('duplicate or oversized native source inventory')
            with regular_file(directory, name).open('rb') as stream:
                data = stream.read(MAX_FILE_BYTES + 1)
            if len(data) > MAX_FILE_BYTES:
                raise ValueError('native source evidence exceeds byte limit: ' + name)
            actual = digest(data)
            if not SHA256.fullmatch(proof.get('sha256', '')) or actual != proof['sha256']:
                raise ValueError('native source evidence differs from reviewed policy: ' + name)
            blob = hashlib.sha1(b'blob ' + str(len(data)).encode() + b'\0' + data).hexdigest()
            if not REVISION.fullmatch(proof.get('gitBlob', '')) or blob != proof['gitBlob']:
                raise ValueError('native source Git blob differs from reviewed policy: ' + name)
            hashes[name] = actual
            record['files'].append({'path': name, 'sha256': actual, 'gitBlob': blob})
            if PurePosixPath(name).name == 'Cargo.lock':
                record['cargoPackages'].extend(cargo_packages(data))
            elif PurePosixPath(name).name == 'vcpkg.json':
                manifest = json.loads(data)
                if not isinstance(manifest, dict):
                    raise ValueError('native vcpkg manifest is malformed: ' + name)
                # Preserve conditional features and host-only edges. Resolving
                # them requires the actual triplet/build receipt, not guessing.
                record['vcpkgManifests'].append({'path': name, 'manifest': manifest})
        sources.append(record)
    return {'version': 1, 'coverageComplete': False,
            'scope': 'authenticated source manifests; compiled closure not asserted',
            'sources': sources, 'fileHashes': dict(sorted(hashes.items())),
            'unresolvedBuildEdges': policy.get('unresolvedBuildEdges', [])}


def write_json(path, value):
    path.write_text(json.dumps(value, indent=2) + '\n')


def fetch_source(directory, source, proof):
    prefix = source['name'] + '/'
    if not proof['path'].startswith(prefix):
        raise ValueError('native source path differs from its source root')
    repository = source['repository'].removeprefix('https://github.com/')
    if not re.fullmatch(r'[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+', repository):
        raise ValueError('native source repository is not an official GitHub identity')
    relative = proof['path'][len(prefix):]
    url = 'https://raw.githubusercontent.com/%s/%s/%s' % (
        repository, source['sourceRevision'], relative)
    request = urllib.request.Request(url, headers={'User-Agent': 'LeapView-native-inventory/1'})
    with urllib.request.urlopen(request, timeout=60) as response:
        data = response.read(MAX_FILE_BYTES + 1)
    if len(data) > MAX_FILE_BYTES or digest(data) != proof['sha256']:
        raise ValueError('downloaded native source differs from reviewed policy: ' + proof['path'])
    target = directory / proof['path']
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_bytes(data)


def collect(directory, archive, kind, platform, revision, policy):
    """Retain independently hashed source material and fresh OSV diagnostics.

    This is deliberately an inventory producer, not binary security admission.
    The candidate's native closure must still pass the separate verifier.
    """
    if kind != 'application-image' or platform not in ('linux/amd64', 'linux/arm64'):
        raise ValueError('native collection currently requires an application image')
    if not REVISION.fullmatch(revision):
        raise ValueError('native source revision must be exact')
    archive, directory = Path(archive), Path(directory)
    if archive.is_symlink() or not archive.is_file():
        raise ValueError('native candidate must be a regular archive')
    archive_digest = candidate.digest_file(archive)
    artifact = candidate.image_identity(archive, {'revision': revision,
                                      'repository': 'flidai/leapview'}, kind)
    artifact.update({'kind': kind, 'sha256': archive_digest})
    if artifact['platform'] != platform:
        raise ValueError('native candidate platform differs from request')
    if directory.exists():
        raise ValueError('native collection output must be a new directory')
    directory.mkdir(mode=0o700, parents=True)
    sources_directory = directory / 'sources'
    sources_directory.mkdir(mode=0o700)
    with ThreadPoolExecutor(max_workers=4) as pool:
        futures = [pool.submit(fetch_source, sources_directory, source, proof)
                   for source in policy['sources'] for proof in source['files']]
        for future in futures:
            future.result()
    inventory = source_inventory(sources_directory, policy)
    write_json(directory / 'source-inventory.json', inventory)
    identities = sorted({(package['name'], package['version'])
                         for source in inventory['sources'] for package in source['cargoPackages']
                         if package['source'].startswith('registry+')})
    reports = directory / 'osv'
    reports.mkdir(mode=0o700)
    findings = []
    for index in range(0, len(identities), 100):
        batch = identities[index:index + 100]
        queries = [{'package': {'ecosystem': 'crates.io', 'name': name}, 'version': version}
                   for name, version in batch]
        payload = json.dumps({'queries': queries}).encode()
        request = urllib.request.Request('https://api.osv.dev/v1/querybatch', data=payload,
                                         headers={'Content-Type': 'application/json'})
        with urllib.request.urlopen(request, timeout=60) as response:
            raw = response.read(MAX_FILE_BYTES + 1)
        if len(raw) > MAX_FILE_BYTES:
            raise ValueError('native OSV report exceeds byte limit')
        result = candidate.read_json(raw, MAX_FILE_BYTES)
        if not isinstance(result.get('results'), list) or len(result['results']) != len(batch):
            raise ValueError('native OSV response does not cover requested source inventory')
        if any('next_page_token' in item for item in result['results']):
            raise ValueError('native OSV response is paginated; complete diagnostic result unavailable')
        (reports / ('%04d-request.json' % index)).write_bytes(payload)
        (reports / ('%04d-response.json' % index)).write_bytes(raw)
        findings.extend({'name': identity[0], 'version': identity[1], 'advisories': item['vulns']}
                        for identity, item in zip(batch, result['results']) if item.get('vulns'))
    if candidate.digest_file(archive) != artifact['sha256']:
        raise ValueError('native candidate changed during collection')
    summary = {'version': 1, 'artifact': artifact, 'sourceRevision': revision,
               'platform': platform, 'coverageComplete': False,
               'observedAt': datetime.now(timezone.utc).isoformat(),
               'scope': inventory['scope'], 'policySHA256': candidate.digest_file(POLICY_PATH),
               'sourcePackageCount': len(identities), 'sourceFindings': findings,
               'unresolvedBuildEdges': inventory['unresolvedBuildEdges']}
    summary['fileHashes'] = {
        path.relative_to(directory).as_posix(): candidate.digest_file(path)
        for path in sorted(directory.rglob('*')) if path.is_file()}
    if len(summary['fileHashes']) > MAX_FILES:
        raise ValueError('native collection exceeds evidence file limit')
    write_json(directory / 'native-evidence.json', summary)
    return summary


def verify(directory, archive, kind, platform, revision, policy):
    if kind not in ('application-image', 'application-archive') or platform not in ('linux/amd64', 'linux/arm64'):
        raise ValueError('unsupported native application artifact profile')
    if not REVISION.fullmatch(revision):
        raise ValueError('native source revision must be exact')
    unresolved = policy.get('unresolvedBuildEdges')
    if not isinstance(unresolved, list):
        raise ValueError('native source policy omits build closure disposition')
    if unresolved:
        reasons = ['%s: %s' % (edge['component'], edge['reason']) for edge in unresolved]
        raise ValueError('native coverage incomplete: ' + '; '.join(reasons))
    # A source-only inventory cannot become admission by deleting its blockers
    # or submitting a producer-controlled complete=true assertion.
    raise ValueError('native binary closure and fresh scanner profile are not qualified')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest='command', required=True)
    inventory = commands.add_parser('inventory')
    inventory.add_argument('directory', type=Path)
    inventory.add_argument('--output', type=Path)
    collection = commands.add_parser('collect')
    collection.add_argument('directory', type=Path)
    collection.add_argument('--artifact', type=Path, required=True)
    collection.add_argument('--kind', required=True)
    collection.add_argument('--platform', required=True)
    collection.add_argument('--source-revision', required=True)
    collection.add_argument('--output', type=Path)
    verification = commands.add_parser('verify')
    verification.add_argument('directory', type=Path)
    verification.add_argument('--artifact', type=Path, required=True)
    verification.add_argument('--kind', required=True)
    verification.add_argument('--platform', required=True)
    verification.add_argument('--source-revision', required=True)
    verification.add_argument('--output', type=Path)
    args = parser.parse_args()
    policy = json.loads(POLICY_PATH.read_text())
    if args.command == 'inventory':
        result = source_inventory(args.directory, policy)
    elif args.command == 'collect':
        result = collect(args.directory, args.artifact, args.kind, args.platform, args.source_revision, policy)
    else:
        result = verify(args.directory, args.artifact, args.kind, args.platform, args.source_revision, policy)
    encoded = json.dumps(result, indent=2) + '\n'
    if args.output:
        args.output.write_text(encoded)
    else:
        print(encoded, end='')


if __name__ == '__main__':
    try:
        main()
    except (ValueError, OSError, KeyError, TypeError) as error:
        print('native inventory: ' + str(error), file=sys.stderr)
        sys.exit(1)
