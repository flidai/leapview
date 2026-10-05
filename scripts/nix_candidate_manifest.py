#!/usr/bin/env python3
"""Bind Nix candidate identities and evidence without granting release admission."""

import argparse
import copy
from datetime import datetime, date, timedelta, timezone
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import subprocess
import tarfile

ROOT = Path(__file__).resolve().parents[1]
SHA256 = re.compile(r'^sha256:[0-9a-f]{64}$')
REVISION = re.compile(r'^[0-9a-f]{40}$')
PLATFORMS = {'linux/amd64', 'linux/arm64'}
ASSESSMENT_FILES = {'linux/amd64': 'runtime-assessments.vex.json',
                    'linux/arm64': 'runtime-assessments.arm64.vex.json'}
KINDS = {'application-image', 'site-image', 'cli-archive', 'application-archive', 'desktop-archive'}
MAX_JSON_BYTES = 2 * 1024 * 1024
MAX_REPORT_BYTES = 128 * 1024 * 1024
RUNTIME_REPORTS = {'sbom.syft.json', 'sbom.spdx.json', 'runtime.syft.json', 'runtime.grype.json',
                   'runtime.assessed.grype.json', 'controls.synthetic.syft.json', 'controls.grype.json',
                   'assessments.vex.json', 'syft-config.json', 'grype-config.json'}
COMMON_GATES = ['provenance', 'spdx', 'go-and-embedded-native-coverage',
                'canonical-release-identity', 'supported-platform-matrix', 'exact-artifact-promotion',
                'supported-host-compatibility', 'installation-upgrade-rollback-recovery']


def digest_bytes(data):
    return 'sha256:' + hashlib.sha256(data).hexdigest()


def digest_file(path):
    with Path(path).open('rb') as stream:
        return 'sha256:' + hashlib.file_digest(stream, 'sha256').hexdigest()


def json_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError('duplicate JSON key: ' + key)
        result[key] = value
    return result


def read_json(data, limit=MAX_JSON_BYTES):
    if len(data) > limit:
        raise ValueError('JSON evidence exceeds its byte limit')
    def reject_constant(value):
        raise ValueError('nonfinite JSON number: ' + value)
    return json.loads(data, object_pairs_hook=json_object, parse_constant=reject_constant)


def canonical_bytes(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':'), allow_nan=False).encode()


def read_json_file(path, limit=MAX_JSON_BYTES):
    with Path(path).open('rb') as stream:
        return read_json(stream.read(limit + 1), limit)


def safe_path(value):
    if not isinstance(value, str) or not value or '\\' in value:
        raise ValueError('invalid relative path')
    path = PurePosixPath(value)
    if path.is_absolute() or '..' in path.parts or str(path) != value:
        raise ValueError('path is not canonical and relative')
    return value


def validate_source(source):
    if set(source) != {'repository', 'revision', 'inputs'} or source['repository'] != 'flidai/leapview':
        raise ValueError('unsupported source identity')
    if not isinstance(source['revision'], str) or not REVISION.fullmatch(source['revision']):
        raise ValueError('source revision must be an exact commit')
    inputs = source['inputs']
    if not isinstance(inputs, list) or not inputs:
        raise ValueError('locked inputs are missing')
    paths = []
    for entry in inputs:
        if set(entry) != {'path', 'sha256'} or not SHA256.fullmatch(entry['sha256']):
            raise ValueError('invalid locked input identity')
        paths.append(safe_path(entry['path']))
    if paths != sorted(set(paths)):
        raise ValueError('locked input paths must be sorted and unique')


def image_identity(archive, source, kind):
    """Read a Docker archive without extraction; verify configuration and layers.

    The config digest is an OCI content identity, not a registry manifest digest.
    Every uncompressed layer must match the config's ordered rootfs diff IDs.
    """
    documents, layers, seen = {}, {}, set()
    with tarfile.open(archive, 'r|*') as outer:
        for member in outer:
            name = member.name.removeprefix('./')
            if member.isdir():
                continue
            safe_path(name)
            if name in seen:
                raise ValueError('duplicate archive member: ' + name)
            seen.add(name)
            if not member.isfile():
                raise ValueError('archive metadata and layers must be regular files')
            stream = outer.extractfile(member)
            if name.endswith('.json'):
                if member.size > MAX_JSON_BYTES:
                    raise ValueError('archive JSON exceeds its byte limit')
                data = stream.read(MAX_JSON_BYTES + 1)
                documents[name] = (read_json(data), digest_bytes(data))
            elif name.endswith('/layer.tar') or name == 'layer.tar':
                layers[name] = 'sha256:' + hashlib.file_digest(stream, 'sha256').hexdigest()
    manifest = documents.get('manifest.json', (None,))[0]
    if not isinstance(manifest, list) or len(manifest) != 1:
        raise ValueError('Docker archive must contain exactly one image')
    record = manifest[0]
    config_name = safe_path(record['Config'])
    if config_name not in documents or not re.fullmatch(r'[0-9a-f]{64}\.json', config_name):
        raise ValueError('image config content identity is missing')
    config, config_digest = documents[config_name]
    if config_name != config_digest.removeprefix('sha256:') + '.json':
        raise ValueError('image config filename does not match its content')
    layer_names = [safe_path(name) for name in record['Layers']]
    if len(layer_names) != len(set(layer_names)) or set(layer_names) != set(layers):
        raise ValueError('image layer inventory does not match manifest')
    diff_ids = [layers[name] for name in layer_names]
    if config['rootfs']['type'] != 'layers' or config['rootfs']['diff_ids'] != diff_ids:
        raise ValueError('image layer content does not match rootfs diff IDs')
    platform = config['os'] + '/' + config['architecture']
    if platform not in PLATFORMS:
        raise ValueError('unsupported Nix candidate platform')
    labels = config['config']['Labels']
    if labels['org.opencontainers.image.source'] != 'https://github.com/flidai/leapview' or (
            labels['org.opencontainers.image.revision'] != source['revision']):
        raise ValueError('image source does not match candidate source')
    if labels['dev.leapview.build.dirty'] != 'false':
        raise ValueError('dirty image cannot establish exact source identity')
    if labels['dev.leapview.build.kind'] != kind:
        raise ValueError('image belongs to a different output kind')
    version = labels['org.opencontainers.image.version']
    if not isinstance(version, str) or not version or version != version.strip():
        raise ValueError('image version is missing or noncanonical')
    return {'format': 'docker-archive', 'platform': platform, 'version': version,
            'configDigest': config_digest, 'layerDiffIDs': diff_ids}


def current_date():
    return datetime.now(timezone.utc).date()


def current_time():
    return datetime.now(timezone.utc)


def runtime_assessment_path(platform, *, root=None):
    try:
        name = ASSESSMENT_FILES[platform]
    except KeyError as error:
        raise ValueError('unsupported runtime assessment platform: ' + str(platform)) from error
    return Path(ROOT if root is None else root) / 'nix' / name


def runtime_evidence(directory, artifact):
    directory = Path(directory)
    with (directory / 'summary.json').open('rb') as stream:
        summary_bytes = stream.read(MAX_JSON_BYTES + 1)
    summary = read_json(summary_bytes)
    if type(summary.get('schemaVersion')) is not int or summary['schemaVersion'] != 1 or (
            summary.get('enforcementMode') != 'enforce' or
            summary.get('coverageQualified') is not True or
            summary.get('runtimeVulnerabilityGatePassed') is not True or 'error' in summary):
        raise ValueError('runtime scan did not complete in enforcement mode')
    if summary.get('archiveSHA256') != artifact['sha256'].removeprefix('sha256:'):
        raise ValueError('runtime scan belongs to another archive')
    if summary.get('platform') != artifact['platform']:
        raise ValueError('runtime scan belongs to another platform')
    policy_path = ROOT / 'nix/runtime-security-policy.json'
    policy = read_json_file(policy_path)
    if summary.get('policySHA256') != digest_file(policy_path).removeprefix('sha256:'):
        raise ValueError('runtime policy changed since scan')
    database = summary.get('database', {}).get('status', {})
    if database.get('valid') is not True or not isinstance(database.get('built'), str):
        raise ValueError('runtime database validity or build time is missing')
    try:
        built = datetime.fromisoformat(database['built'])
    except ValueError as error:
        raise ValueError('invalid runtime database build time') from error
    if built.tzinfo is None:
        raise ValueError('runtime database build time must include its timezone')
    age = current_time() - built
    if age < timedelta(0) or age >= timedelta(hours=policy['databaseMaxAgeHours']):
        raise ValueError('runtime database is stale or from the future')
    if summary.get('assessmentReviewUntil') != policy['assessmentReviewUntil'] or (
            current_date() >= date.fromisoformat(policy['assessmentReviewUntil'])):
        raise ValueError('runtime assessments are changed or expired')
    for scanner in ['syft', 'grype']:
        if summary.get('scanners', {}).get(scanner) != policy[scanner + 'Version']:
            raise ValueError('runtime scanner does not match current policy')
    expected = summary.get('reportSHA256', {})
    if set(expected) != RUNTIME_REPORTS:
        raise ValueError('runtime report inventory is incomplete or unsupported')
    reports = []
    for name in sorted(RUNTIME_REPORTS):
        path = directory / name
        if path.stat().st_size > MAX_REPORT_BYTES:
            raise ValueError('runtime report exceeds its byte limit')
        actual = digest_file(path)
        if actual.removeprefix('sha256:') != expected[name]:
            raise ValueError('runtime report changed since scan: ' + name)
        reports.append({'path': name, 'sha256': actual})
    assessment_source = runtime_assessment_path(artifact['platform'])
    assessment_bytes = (directory / 'assessments.vex.json').read_bytes()
    if assessment_bytes != assessment_source.read_bytes():
        raise ValueError('runtime assessment bytes do not match the platform review')
    assessments = digest_bytes(assessment_bytes)
    if assessments != digest_file(assessment_source) or (
            summary.get('assessmentsSHA256') != assessments.removeprefix('sha256:')):
        raise ValueError('runtime assessment bytes changed since scan')
    spdx = read_json_file(directory / 'sbom.spdx.json', MAX_REPORT_BYTES)
    if spdx.get('spdxVersion') != 'SPDX-2.3' or spdx.get('SPDXID') != 'SPDXRef-DOCUMENT' or (
            not spdx.get('documentNamespace') or not spdx.get('packages')):
        raise ValueError('runtime SPDX inventory is missing or unsupported')
    return {'summarySHA256': digest_bytes(summary_bytes), 'reports': reports,
            'assessmentSource': 'nix/' + assessment_source.name,
            'scope': 'nix-runtime-only', 'enforcementMode': 'enforce'}


def collect(archive, kind, source, *, archive_identity=None, runtime_dir=None,
            go_dir=None, binary_verifier=None):
    if kind not in KINDS:
        raise ValueError('unsupported candidate kind')
    validate_source(source)
    if kind.endswith('-image'):
        artifact = image_identity(archive, source, kind)
        gates = COMMON_GATES + ['oci-admission', 'nix-runtime-enforcement']
    else:
        if not isinstance(archive_identity, dict) or set(archive_identity) != {'platform', 'version', 'sourceRevision'}:
            raise ValueError('archive requires an explicit source/platform/version identity')
        if archive_identity['platform'] not in PLATFORMS or archive_identity['sourceRevision'] != source['revision']:
            raise ValueError('archive identity does not match candidate source/platform')
        if not isinstance(archive_identity['version'], str) or not archive_identity['version'] or (
                archive_identity['version'] != archive_identity['version'].strip()):
            raise ValueError('archive version is missing')
        artifact = {'format': 'archive', 'platform': archive_identity['platform'],
                    'version': archive_identity['version']}
        gates = COMMON_GATES + ['embedded-source-identity']
    artifact.update(kind=kind, sha256=digest_file(archive))
    manifest = {'schemaVersion': 1, 'artifact': artifact, 'source': copy.deepcopy(source),
                'evidence': {}, 'requiredReleaseEvidence': sorted(gates), 'releaseAdmission': False}
    if runtime_dir is not None:
        if not kind.endswith('-image'):
            raise ValueError('image runtime evidence cannot qualify an archive output')
        manifest['evidence']['nix-runtime'] = runtime_evidence(runtime_dir, artifact)
    if (go_dir is None) != (binary_verifier is None):
        raise ValueError('Go archive evidence requires a protected binary verifier')
    if go_dir is not None:
        import nix_archive_go_evidence
        manifest['evidence']['go-binaries'] = nix_archive_go_evidence.verify(
            archive, artifact, go_dir, binary_verifier)
    manifest['candidateDigest'] = digest_bytes(b'leapview/nix-candidate-manifest/v1\n' + canonical_bytes(manifest))
    return manifest


def verify(manifest, archive, source, *, kind=None, **kwargs):
    expected = collect(archive, kind or manifest['artifact']['kind'], source, **kwargs)
    # Python equality considers 0 == False and 1 == True; JSON types are part of
    # the persisted contract, so compare canonical bytes instead.
    if canonical_bytes(manifest) != canonical_bytes(expected):
        raise ValueError('candidate manifest does not match current artifact, source, inputs or evidence')
    return expected


def checkout_source(root):
    def git(*args):
        return subprocess.check_output(['git', '-C', str(root), *args], text=True).strip()
    if git('status', '--porcelain', '--untracked-files=no'):
        raise ValueError('candidate collection requires a clean tracked checkout')
    tracked = [path for path in git('ls-files', '-z').split('\0') if path]
    paths = [path for path in tracked if Path(path).name in {
        'go.mod', 'go.sum', 'package.json', 'bun.lock', 'flake.nix', 'flake.lock'
    } or path.startswith('nix/')]
    return {'repository': 'flidai/leapview', 'revision': git('rev-parse', 'HEAD'),
            'inputs': [{'path': path, 'sha256': digest_file(root / path)} for path in sorted(paths)]}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('archive', type=Path)
    parser.add_argument('--kind', choices=sorted(KINDS), required=True)
    parser.add_argument('--output', type=Path)
    parser.add_argument('--verify', type=Path, help='recompute and compare an existing manifest')
    parser.add_argument('--archive-identity', type=Path, help='archive builder source/platform/version JSON')
    parser.add_argument('--runtime-evidence', type=Path)
    parser.add_argument('--go-evidence', type=Path)
    parser.add_argument('--binary-verifier', type=Path)
    args = parser.parse_args()
    os.umask(0o077)
    try:
        source = checkout_source(ROOT)
        options = {'archive_identity': read_json_file(args.archive_identity) if args.archive_identity else None,
                   'runtime_dir': args.runtime_evidence,
                   'go_dir': args.go_evidence, 'binary_verifier': args.binary_verifier}
        if args.verify:
            manifest = verify(read_json_file(args.verify), args.archive, source, kind=args.kind, **options)
        else:
            manifest = collect(args.archive, args.kind, source, **options)
        if args.output:
            args.output.parent.mkdir(parents=True, exist_ok=True)
            with args.output.open('x') as target:
                target.write(json.dumps(manifest, indent=2) + '\n')
        print(json.dumps({'candidateDigest': manifest['candidateDigest'], 'artifact': manifest['artifact'],
                          'releaseAdmission': False}, indent=2))
    except (ValueError, KeyError, TypeError, OSError, tarfile.TarError, subprocess.CalledProcessError) as error:
        raise SystemExit('candidate evidence rejected: ' + str(error)) from error


if __name__ == '__main__':
    main()
