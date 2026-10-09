#!/usr/bin/env python3
"""Authenticate retained Desktop producer artifacts; never rebuild their bytes."""

import argparse
from datetime import datetime, timezone
import hashlib
import io
import os
from pathlib import Path, PurePosixPath
import re
import stat
import tempfile
import zipfile

import managed_admission_handoff as github
import nix_candidate_manifest as candidate
import nix_desktop_attestation as attestation

REPOSITORY = 'flidai/leapview'
WORKFLOW = '.github/workflows/nix-desktop-candidate.yml'
ARCHIVE = attestation.ARCHIVE_NAME
PHASES = ('candidate', 'qualified', 'signed')
MAX_ZIP = 1024 * 1024 * 1024
MAX_TOTAL = 2 * 1024 * 1024 * 1024


def authorize(run, workflow, artifact, run_id, attempt, phase, *, now=None):
    now = now or datetime.now(timezone.utc)
    if (phase not in PHASES or not github._positive(run_id) or not github._positive(attempt)
            or not all(isinstance(value, dict) for value in (run, workflow, artifact))):
        raise ValueError('Desktop input requires exact run, attempt and phase')
    repository, head = run.get('repository', {}), run.get('head_repository', {})
    signer = run.get('head_sha', '')
    if (run.get('id') != run_id or run.get('run_attempt') != attempt
            or run.get('status') != 'completed' or run.get('conclusion') != 'success'
            or run.get('event') != 'workflow_dispatch' or run.get('head_branch') != 'main'
            or run.get('path') != WORKFLOW or workflow.get('path') != WORKFLOW
            or not github._positive(workflow.get('id')) or run.get('workflow_id') != workflow['id']
            or not isinstance(signer, str) or not candidate.REVISION.fullmatch(signer)
            or repository.get('full_name') != REPOSITORY or head.get('full_name') != REPOSITORY
            or not github._positive(repository.get('id')) or head.get('id') != repository['id']):
        raise ValueError('Desktop producer is not the exact successful protected main attempt')
    origin = artifact.get('workflow_run', {})
    digest = artifact.get('digest', '')
    if (artifact.get('name') != f'nix-desktop-{phase}-{run_id}-{attempt}-amd64'
            or not github._positive(artifact.get('id')) or artifact.get('expired') is not False
            or not isinstance(digest, str) or not re.fullmatch(r'sha256:[0-9a-f]{64}', digest)
            or not github._positive(artifact.get('size_in_bytes')) or artifact['size_in_bytes'] > MAX_ZIP
            or origin.get('id') != run_id or origin.get('head_sha') != signer
            or origin.get('head_branch') != 'main' or origin.get('repository_id') != repository['id']
            or origin.get('head_repository_id') != repository['id']):
        raise ValueError('Desktop artifact is not the exact unexpired producer output')
    try:
        if (not github._timestamp(run['run_started_at']) <= github._timestamp(artifact['created_at']) <= github._timestamp(run['updated_at'])
                or github._timestamp(artifact['expires_at']) <= now):
            raise ValueError('Desktop artifact is outside its successful attempt lifetime')
    except (KeyError, github.HandoffError) as error:
        raise ValueError('Desktop producer timestamps are invalid') from error
    return {'runId': run_id, 'attempt': attempt, 'artifactId': artifact['id'],
            'artifactSHA256': digest, 'phase': phase, 'signerRevision': signer}


def extract(data, digest, destination, phase):
    if len(data) > MAX_ZIP or candidate.digest_bytes(data) != digest:
        raise ValueError('Desktop artifact ZIP differs from authenticated GitHub digest')
    destination = Path(destination)
    with zipfile.ZipFile(io.BytesIO(data)) as archive:
        files, size = [], 0
        for member in archive.infolist():
            path = PurePosixPath(member.filename)
            mode = member.external_attr >> 16
            if (path.is_absolute() or '..' in path.parts or '\\' in member.filename
                    or str(path) != member.filename.rstrip('/') or member.flag_bits & 1
                    or stat.S_IFMT(mode) not in (0, stat.S_IFREG, stat.S_IFDIR)):
                raise ValueError('Desktop ZIP contains an unsafe entry')
            if member.is_dir():
                if phase == 'candidate' or str(path) not in ('candidate', 'qualified'):
                    raise ValueError('Desktop ZIP contains an unexpected directory')
                continue
            allowed = (str(path) == ARCHIVE if phase == 'candidate' else
                       str(path) == 'candidate/' + ARCHIVE or
                       (len(path.parts) == 2 and path.parts[0] == 'qualified' and
                        re.fullmatch(r'[A-Za-z0-9_.-]+', path.name)))
            if not allowed or member.filename in [entry.filename for entry in files]:
                raise ValueError('Desktop ZIP inventory contains an unexpected or duplicate file')
            size += member.file_size
            if member.file_size > MAX_ZIP or size > MAX_TOTAL:
                raise ValueError('Desktop ZIP exceeds extracted size bounds')
            files.append(member)
        if len(files) != (1 if phase == 'candidate' else 8):
            raise ValueError('Desktop ZIP does not contain the exact evidence inventory')
        destination.mkdir(mode=0o700)
        for member in files:
            target = destination / member.filename
            target.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
            with archive.open(member) as source, target.open('xb') as output:
                copied = 0
                while chunk := source.read(65536):
                    copied += len(chunk)
                    if copied > member.file_size:
                        raise ValueError('Desktop ZIP member size changed')
                    output.write(chunk)


def api(path, limit=8 * 1024 * 1024):
    return candidate.read_json(github._github(path, limit=limit), limit)


def fetch(run_id, attempt, directory):
    directory = Path(directory)
    directory.mkdir(mode=0o700)
    run = api(f'actions/runs/{run_id}/attempts/{attempt}')
    workflow = api('actions/workflows/nix-desktop-candidate.yml')
    artifacts = []
    for page in range(1, 11):
        values = api(f'actions/runs/{run_id}/artifacts?per_page=100&page={page}')['artifacts']
        artifacts.extend(values)
        if len(values) < 100:
            break
    else:
        raise ValueError('Desktop producer artifact pagination exceeds bound')
    identities = []
    for phase in PHASES:
        name = f'nix-desktop-{phase}-{run_id}-{attempt}-amd64'
        matches = [value for value in artifacts if value.get('name') == name]
        if len(matches) != 1:
            raise ValueError('Desktop producer lacks one exact original/qualified/signed artifact')
        identity = authorize(run, workflow, matches[0], run_id, attempt, phase)
        archive = github._github(f"actions/artifacts/{identity['artifactId']}/zip", limit=MAX_ZIP)
        with (directory / (phase + '.zip')).open('xb') as output:
            output.write(archive)
        extract(archive, identity['artifactSHA256'], directory / phase, phase)
        del archive
        identities.append(identity)
    manifest = candidate.read_json_file(directory / 'signed/qualified/candidate-manifest.json')
    source = manifest['source']['revision']
    if not isinstance(source, str) or not candidate.REVISION.fullmatch(source):
        raise ValueError('Desktop source identity is invalid')
    result = {'sourceRevision': source, 'signerRevision': run['head_sha'], 'artifacts': identities}
    attestation.desktop.write_json(directory / 'inputs.json', result)
    return result


def verify_retained(directory, identity, run, workflow, run_id, attempt):
    phase = identity.get('phase')
    if phase not in PHASES or not github._positive(identity.get('artifactId')):
        raise ValueError('Desktop retained artifact identity is invalid')
    metadata = api(f"actions/artifacts/{identity['artifactId']}")
    current = authorize(run, workflow, metadata, run_id, attempt, phase)
    if candidate.canonical_bytes(current) != candidate.canonical_bytes(identity):
        raise ValueError('Desktop retained artifact no longer matches current authenticated metadata')
    archive = directory / (phase + '.zip')
    raw = attestation.archives._lstat_regular(archive, MAX_ZIP)
    with tempfile.TemporaryDirectory(prefix='desktop-input-verification-') as temporary:
        checked = Path(temporary) / 'checked'
        extract(raw, current['artifactSHA256'], checked, phase)
        staged = directory / phase
        expected = sorted(path.relative_to(checked) for path in checked.rglob('*') if path.is_file())
        actual = sorted(path.relative_to(staged) for path in staged.rglob('*') if path.is_file() or path.is_symlink())
        if expected != actual or staged.is_symlink():
            raise ValueError('Desktop extracted input inventory changed')
        for relative in expected:
            if any(parent.is_symlink() for parent in (staged / relative).parents):
                raise ValueError('Desktop extracted input is redirected')
            data = attestation.archives._lstat_regular(staged / relative, MAX_ZIP)
            if candidate.digest_bytes(data) != attestation.desktop.digest_file(checked / relative):
                raise ValueError('Desktop extracted input differs from authenticated archive')


def verify(directory, source_root, signer_root, run_id, attempt):
    directory = Path(directory)
    inputs = candidate.read_json_file(directory / 'inputs.json')
    run = api(f'actions/runs/{run_id}/attempts/{attempt}')
    workflow = api('actions/workflows/nix-desktop-candidate.yml')
    records = inputs.get('artifacts', [])
    if len(records) != 3 or [record.get('phase') for record in records] != list(PHASES):
        raise ValueError('Desktop retained input phase inventory differs')
    for record in records:
        verify_retained(directory, record, run, workflow, run_id, attempt)
    if inputs['signerRevision'] != run['head_sha']:
        raise ValueError('Desktop retained signer differs from selected producer')
    binding = attestation.verify_signed(directory / 'candidate' / ARCHIVE,
        directory / 'qualified', directory / 'signed', source_root, signer_root,
        inputs['sourceRevision'], inputs['signerRevision'])
    return {'producer': inputs, 'binding': binding}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--predecessor-run', type=int, required=True)
    parser.add_argument('--predecessor-attempt', type=int, required=True)
    parser.add_argument('--candidate-run', type=int, required=True)
    parser.add_argument('--candidate-attempt', type=int, required=True)
    parser.add_argument('--directory', type=Path, required=True)
    parser.add_argument('--github-output', type=Path, required=True)
    args = parser.parse_args()
    os.umask(0o077)
    if args.predecessor_run == args.candidate_run:
        raise ValueError('Desktop lifecycle requires two different producer runs')
    args.directory.mkdir(mode=0o700)
    for role in ('predecessor', 'candidate'):
        value = fetch(getattr(args, role + '_run'), getattr(args, role + '_attempt'), args.directory / role)
        with args.github_output.open('a') as output:
            for field in ('sourceRevision', 'signerRevision'):
                output.write(f'{role}_{field}={value[field]}\n')


if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError, KeyError, TypeError, zipfile.BadZipFile, github.HandoffError):
        raise SystemExit('Desktop lifecycle inputs rejected; successful authenticated retained artifacts are required') from None
