#!/usr/bin/env python3
"""Authenticate retained Nix image outputs; collection grants no release authority."""

import argparse
from datetime import datetime, timezone
import io
import os
from pathlib import Path, PurePosixPath
import stat
import tempfile
import zipfile

import managed_admission_handoff as github
import nix_candidate_manifest as candidate

PRODUCERS = {'application-image': '.github/workflows/nix-candidate.yml',
             'site-image': '.github/workflows/nix-site-candidate.yml'}
REPOSITORY = 'flidai/leapview'
PHASES = ('qualified', 'binding', 'published')
MAX_ZIP = 2 * 1024**3
MAX_TOTAL = 4 * 1024**3
MAX_ENTRIES = 4096


def artifact_name(kind, phase, run_id, attempt, arch):
    if kind not in PRODUCERS or phase not in PHASES or arch not in ('amd64', 'arm64'):
        raise ValueError('unsupported Nix output selection')
    prefix = 'nix' if kind == 'application-image' else 'nix-site'
    stage = {'qualified': 'qualified', 'binding': 'candidate-binding', 'published': 'published-qualification'}[phase]
    return f'{prefix}-{stage}-{run_id}-{attempt}-{arch}'


def authorize(run, workflow, artifact, kind, phase, run_id, attempt, arch, *, now=None):
    now = now or datetime.now(timezone.utc)
    name = artifact_name(kind, phase, run_id, attempt, arch)
    if (not github._positive(run_id) or not github._positive(attempt)
            or not all(isinstance(item, dict) for item in (run, workflow, artifact))):
        raise ValueError('Nix output requires exact positive run and attempt')
    repository, head = run.get('repository') or {}, run.get('head_repository') or {}
    signer = run.get('head_sha', '')
    if (run.get('id') != run_id or run.get('run_attempt') != attempt
            or run.get('status') != 'completed' or run.get('conclusion') != 'success'
            or run.get('event') != 'workflow_dispatch' or run.get('head_branch') != 'main'
            or run.get('path') != PRODUCERS[kind] or workflow.get('path') != PRODUCERS[kind]
            or not github._positive(workflow.get('id')) or run.get('workflow_id') != workflow['id']
            or not isinstance(signer, str) or not candidate.REVISION.fullmatch(signer)
            or repository.get('full_name') != REPOSITORY or head.get('full_name') != REPOSITORY
            or not github._positive(repository.get('id')) or head.get('id') != repository['id']):
        raise ValueError('Nix output is not the exact successful protected main producer attempt')
    origin, digest = artifact.get('workflow_run') or {}, artifact.get('digest', '')
    if (artifact.get('name') != name or not github._positive(artifact.get('id'))
            or artifact.get('expired') is not False or not isinstance(digest, str) or not candidate.SHA256.fullmatch(digest)
            or not github._positive(artifact.get('size_in_bytes')) or artifact['size_in_bytes'] > MAX_ZIP
            or origin.get('id') != run_id or origin.get('head_sha') != signer
            or origin.get('head_branch') != 'main' or origin.get('repository_id') != repository['id']
            or origin.get('head_repository_id') != repository['id']):
        raise ValueError('Nix artifact is not the selected unexpired kind, phase, platform and source')
    try:
        if (not github._timestamp(run['run_started_at']) <= github._timestamp(artifact['created_at']) <= github._timestamp(run['updated_at'])
                or github._timestamp(artifact['expires_at']) <= now):
            raise ValueError('Nix artifact timestamp is outside the selected attempt')
    except (KeyError, github.HandoffError) as error:
        raise ValueError('Nix artifact timestamps are invalid') from error
    return {'kind': kind, 'phase': phase, 'runId': run_id, 'attempt': attempt,
            'architecture': arch, 'artifactId': artifact['id'], 'artifactSHA256': digest,
            'signerRevision': signer}


def extract(data, digest, destination):
    if len(data) > MAX_ZIP or candidate.digest_bytes(data) != digest:
        raise ValueError('Nix ZIP differs from authenticated GitHub digest')
    destination = Path(destination)
    with zipfile.ZipFile(io.BytesIO(data)) as archive:
        entries = archive.infolist()
        if not 0 < len(entries) <= MAX_ENTRIES:
            raise ValueError('Nix ZIP entry inventory exceeds bound')
        files, names, total = [], set(), 0
        for member in entries:
            name = member.filename.rstrip('/') if member.is_dir() else member.filename
            path, mode = PurePosixPath(name), stat.S_IFMT(member.external_attr >> 16)
            if (not name or path.is_absolute() or '..' in path.parts or '\\' in name
                    or str(path) != name or name in names or member.flag_bits & 1
                    or mode not in (0, stat.S_IFREG, stat.S_IFDIR)
                    or member.compress_type not in (zipfile.ZIP_STORED, zipfile.ZIP_DEFLATED)
                    or (member.is_dir() and member.file_size != 0)
                    or (not member.is_dir() and mode == stat.S_IFDIR)):
                raise ValueError('Nix ZIP contains an unsafe or repeated entry')
            names.add(name)
            if not member.is_dir():
                total += member.file_size
                if member.file_size > MAX_ZIP or total > MAX_TOTAL:
                    raise ValueError('Nix ZIP unpacked bytes exceed bound')
                files.append((path, member))
        regular_names = {str(path) for path, _ in files}
        if not files or any(str(parent) in regular_names for path, _ in files for parent in path.parents):
            raise ValueError('Nix ZIP contains a file/directory collision')
        # Verify the complete archive before writing into a fresh private directory.
        destination.mkdir(mode=0o700)
        for path, member in files:
            target = destination / str(path)
            target.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
            with archive.open(member) as source, target.open('xb') as output:
                copied = 0
                while chunk := source.read(65536):
                    copied += len(chunk)
                    if copied > member.file_size:
                        raise ValueError('Nix ZIP member exceeds declared size')
                    output.write(chunk)
                if copied != member.file_size:
                    raise ValueError('Nix ZIP member size differs from declaration')
            target.chmod(0o600)


def api(path, limit=8 * 1024**2):
    return candidate.read_json(github._github(path, limit=limit), limit)


def _regular(path, limit):
    path = Path(path)
    before = path.lstat()
    if not stat.S_ISREG(before.st_mode) or not 0 < before.st_size <= limit:
        raise ValueError('Nix retained file must be bounded and regular')
    with path.open('rb') as stream:
        opened = os.fstat(stream.fileno())
        if (opened.st_dev, opened.st_ino) != (before.st_dev, before.st_ino):
            raise ValueError('Nix retained file changed while opening')
        data = stream.read(limit + 1)
    if len(data) != before.st_size:
        raise ValueError('Nix retained file changed size')
    return data


def verify_retained(directory, identity, run, workflow):
    directory = Path(directory)
    metadata = api(f'actions/artifacts/{identity["artifactId"]}')
    current = authorize(run, workflow, metadata, identity['kind'], identity['phase'],
                        identity['runId'], identity['attempt'], identity['architecture'])
    if current != identity:
        raise ValueError('Nix retained identity differs from authenticated metadata')
    data = _regular(directory / (identity['phase'] + '.zip'), MAX_ZIP)
    with tempfile.TemporaryDirectory(prefix='nix-retained-verification-') as temporary:
        checked = Path(temporary) / 'checked'
        extract(data, current['artifactSHA256'], checked)
        staged = directory / identity['phase']
        if staged.is_symlink():
            raise ValueError('Nix retained directory is redirected')
        expected = sorted(path.relative_to(checked) for path in checked.rglob('*') if path.is_file())
        actual = sorted(path.relative_to(staged) for path in staged.rglob('*') if path.is_file() or path.is_symlink())
        if expected != actual:
            raise ValueError('Nix retained inventory differs from authenticated ZIP')
        for relative in expected:
            if any(parent.is_symlink() for parent in (staged / relative).parents):
                raise ValueError('Nix retained evidence is redirected')
            if candidate.digest_bytes(_regular(staged / relative, MAX_ZIP)) != candidate.digest_file(checked / relative):
                raise ValueError('Nix retained bytes differ from authenticated ZIP')


def fetch(kind, run_id, attempt, arch, directory):
    artifact_name(kind, 'qualified', run_id, attempt, arch)
    run = api(f'actions/runs/{run_id}/attempts/{attempt}')
    workflow = api('actions/workflows/' + Path(PRODUCERS[kind]).name)
    artifacts = []
    for page in range(1, 11):
        values = api(f'actions/runs/{run_id}/artifacts?per_page=100&page={page}')['artifacts']
        artifacts.extend(values)
        if len(values) < 100:
            break
    else:
        raise ValueError('Nix artifact pagination exceeds bound')
    identities = []
    for phase in PHASES:
        matches = [item for item in artifacts if item.get('name') == artifact_name(kind, phase, run_id, attempt, arch)]
        if len(matches) != 1:
            raise ValueError('Nix producer lacks one exact qualified/binding/published artifact')
        identities.append(authorize(run, workflow, matches[0], kind, phase, run_id, attempt, arch))
    directory = Path(directory)
    directory.mkdir(mode=0o700)
    for identity in identities:
        data = github._github(f'actions/artifacts/{identity["artifactId"]}/zip', limit=MAX_ZIP)
        with (directory / (identity['phase'] + '.zip')).open('xb') as output:
            output.write(data)
        extract(data, identity['artifactSHA256'], directory / identity['phase'])
    result = {'schemaVersion': 1, 'kind': kind, 'runId': run_id, 'attempt': attempt,
              'architecture': arch, 'signerRevision': run['head_sha'], 'artifacts': identities}
    with (directory / 'inputs.json').open('xb') as output:
        output.write(candidate.canonical_bytes(result))
    return result


def verify(directory, kind, run_id, attempt, arch):
    directory = Path(directory)
    selection = candidate.read_json(_regular(directory / 'inputs.json', candidate.MAX_JSON_BYTES))
    run = api(f'actions/runs/{run_id}/attempts/{attempt}')
    workflow = api('actions/workflows/' + Path(PRODUCERS[kind]).name)
    if not isinstance(selection, dict) or not isinstance(selection.get('artifacts'), list) or not all(isinstance(item, dict) for item in selection['artifacts']):
        raise ValueError('Nix retained selection is invalid')
    expected = {'schemaVersion': 1, 'kind': kind, 'runId': run_id, 'attempt': attempt,
                'architecture': arch, 'signerRevision': run.get('head_sha'), 'artifacts': selection.get('artifacts')}
    if selection != expected or [item.get('phase') for item in selection['artifacts']] != list(PHASES):
        raise ValueError('Nix retained selection differs from requested producer')
    for identity in selection['artifacts']:
        if (identity.get('kind'), identity.get('runId'), identity.get('attempt'), identity.get('architecture')) != (kind, run_id, attempt, arch):
            raise ValueError('Nix retained phase differs from requested selection')
        verify_retained(directory, identity, run, workflow)
    return selection


def source_revision(directory, kind, run_id, attempt, arch):
    # This selects a read-only source checkout only. The live issuer later
    # recomputes the complete source/candidate binding before granting admission.
    verify(directory, kind, run_id, attempt, arch)
    manifest = candidate.read_json_file(Path(directory) / 'qualified/runtime/candidate-manifest.json')
    revision = manifest.get('source', {}).get('revision', '')
    if (manifest.get('source', {}).get('repository') != REPOSITORY
            or not isinstance(revision, str) or not candidate.REVISION.fullmatch(revision)):
        raise ValueError('authenticated candidate lacks exact repository source identity')
    return revision


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('operation', choices=('fetch', 'verify', 'source'))
    parser.add_argument('--kind', choices=tuple(PRODUCERS), required=True)
    parser.add_argument('--run-id', type=int, required=True)
    parser.add_argument('--attempt', type=int, required=True)
    parser.add_argument('--architecture', choices=('amd64', 'arm64'), required=True)
    parser.add_argument('--directory', type=Path, required=True)
    parser.add_argument('--github-output', type=Path)
    args = parser.parse_args()
    os.umask(0o077)
    if args.operation == 'fetch':
        fetch(args.kind, args.run_id, args.attempt, args.architecture, args.directory)
    elif args.operation == 'verify':
        verify(args.directory, args.kind, args.run_id, args.attempt, args.architecture)
    else:
        revision = source_revision(args.directory, args.kind, args.run_id, args.attempt, args.architecture)
        if args.github_output is not None:
            with args.github_output.open('a') as stream:
                stream.write('source_revision=' + revision + '\n')
        else:
            print(revision)


if __name__ == '__main__':
    main()
