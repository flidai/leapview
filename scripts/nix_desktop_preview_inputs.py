#!/usr/bin/env python3
"""Stage the exact signed, qualified Nix Linux preview without rebuilding it."""

import argparse
from datetime import datetime, timezone
import io
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import zipfile

import nix_desktop_lifecycle_inputs as inputs
import nix_desktop_lifecycle as lifecycle

LIFECYCLE_WORKFLOW = '.github/workflows/nix-desktop-lifecycle.yml'
MAX_LIFECYCLE_BYTES = 16 * 1024 * 1024


def lifecycle_metadata(run_id, attempt):
    run = inputs.api(f'actions/runs/{run_id}/attempts/{attempt}')
    workflow = inputs.api('actions/workflows/nix-desktop-lifecycle.yml')
    repository, head = run.get('repository', {}), run.get('head_repository', {})
    if (not inputs.github._positive(run_id) or not inputs.github._positive(attempt)
            or run.get('id') != run_id or run.get('run_attempt') != attempt
            or run.get('status') != 'completed' or run.get('conclusion') != 'success'
            or run.get('event') != 'workflow_dispatch' or run.get('head_branch') != 'main'
            or run.get('path') != LIFECYCLE_WORKFLOW or workflow.get('path') != LIFECYCLE_WORKFLOW
            or not inputs.github._positive(workflow.get('id')) or run.get('workflow_id') != workflow['id']
            or not inputs.candidate.REVISION.fullmatch(run.get('head_sha', ''))
            or repository.get('full_name') != inputs.REPOSITORY or head.get('full_name') != inputs.REPOSITORY
            or not inputs.github._positive(repository.get('id')) or head.get('id') != repository['id']):
        raise ValueError('Desktop lifecycle requires an exact successful protected main attempt')
    records = []
    for page in range(1, 11):
        values = inputs.api(f'actions/runs/{run_id}/artifacts?per_page=100&page={page}')['artifacts']
        records.extend(values)
        if len(values) < 100:
            break
    else:
        raise ValueError('Desktop lifecycle artifact pagination exceeds bound')
    matches = [record for record in records if record.get('name') == f'desktop-lifecycle-{run_id}-{attempt}']
    if len(matches) != 1:
        raise ValueError('Desktop lifecycle has no unique retained receipt')
    artifact = matches[0]
    origin = artifact.get('workflow_run', {})
    if (not inputs.github._positive(artifact.get('id')) or artifact.get('expired') is not False
            or not re.fullmatch(r'sha256:[0-9a-f]{64}', artifact.get('digest', ''))
            or not inputs.github._positive(artifact.get('size_in_bytes')) or artifact['size_in_bytes'] > MAX_LIFECYCLE_BYTES
            or origin.get('id') != run_id or origin.get('head_sha') != run['head_sha']
            or origin.get('head_branch') != 'main' or origin.get('repository_id') != repository['id']
            or origin.get('head_repository_id') != repository['id']
            or not inputs.github._timestamp(run['run_started_at']) <= inputs.github._timestamp(artifact['created_at']) <= inputs.github._timestamp(run['updated_at'])
            or inputs.github._timestamp(artifact['expires_at']) <= datetime.now(timezone.utc)):
        raise ValueError('Desktop lifecycle receipt is not the exact live producer artifact')
    return {'runId': run_id, 'attempt': attempt, 'artifactId': artifact['id'],
            'artifactSHA256': artifact['digest'], 'verifierRevision': run['head_sha']}


def verify_lifecycle(run_id, attempt, verified):
    identity = lifecycle_metadata(run_id, attempt)
    data = inputs.github._github(f"actions/artifacts/{identity['artifactId']}/zip", limit=MAX_LIFECYCLE_BYTES)
    if inputs.candidate.digest_bytes(data) != identity['artifactSHA256']:
        raise ValueError('Desktop lifecycle ZIP differs from authenticated GitHub digest')
    with zipfile.ZipFile(io.BytesIO(data)) as archive:
        members = archive.infolist()
        if (len(members) != 1 or members[0].filename != 'desktop-lifecycle.json'
                or members[0].flag_bits & 1 or members[0].file_size > MAX_LIFECYCLE_BYTES):
            raise ValueError('Desktop lifecycle receipt inventory is invalid')
        report = inputs.candidate.read_json(archive.read(members[0]), MAX_LIFECYCLE_BYTES)
    if (report.get('schemaVersion') != 1 or report.get('result') != 'success'
            or report.get('verifierRevision') != identity['verifierRevision']
            or report.get('scope') != 'linux-amd64-preview-saved-profile-version-upgrade-crash-restart-offline-rollback'
            or report.get('releaseAdmission') is not False or report.get('privateProfileRemoved') is not True
            or report.get('lifecycle', {}).get('networkIsolated') is not True
            or report.get('lifecycle', {}).get('packageRemoved') is not True
            or inputs.candidate.canonical_bytes(report['inputs']['candidate']) != inputs.candidate.canonical_bytes(verified)):
        raise ValueError('Desktop lifecycle does not qualify this exact preview candidate')
    lifecycle.validate_pair(report['inputs']['predecessor'], verified)
    if lifecycle_metadata(run_id, attempt) != identity:
        raise ValueError('Desktop lifecycle producer identity changed during verification')
    return {'producer': identity, 'report': report}


def stage(directory, source_root, signer_root, verifier_root, run_id, attempt,
          expected_source, output, *, lifecycle_proof=None):
    directory, source_root, verifier_root, output = map(Path,
        (directory, source_root, verifier_root, output))
    if (not isinstance(expected_source, str) or not inputs.candidate.REVISION.fullmatch(expected_source)
            or output.exists() or output.is_symlink() or not output.parent.is_dir()
            or any(parent.is_symlink() for parent in output.parents)):
        raise ValueError('Desktop preview requires exact source and a fresh local output')
    verified = inputs.verify(directory, source_root, signer_root, run_id, attempt)
    if lifecycle_proof is not None and inputs.candidate.canonical_bytes(lifecycle_proof['report']['inputs']['candidate']) != inputs.candidate.canonical_bytes(verified):
        raise ValueError('Desktop preview producer changed after lifecycle verification')
    archive = verified['binding']['archive']
    package = inputs.candidate.read_json_file(source_root / 'desktop/package.json')
    if (verified['producer']['sourceRevision'] != expected_source
            or archive['platform'] != 'linux/amd64' or archive['version'] != package['version']):
        raise ValueError('Desktop preview source, version or platform differs from qualified Nix output')
    original = directory / 'candidate' / inputs.ARCHIVE
    if inputs.attestation.desktop.digest_file(original) != archive['sha256']:
        raise ValueError('Desktop preview archive differs from verified original bytes')
    # Nothing becomes publishable until both the protected evidence verifier and
    # the repeated authenticated producer/byte proof succeed.
    with tempfile.TemporaryDirectory(prefix='.desktop-preview-', dir=output.parent) as temporary:
        staged = Path(temporary) / 'candidate'
        evidence, make = staged / 'out/evidence', staged / 'out/make'
        evidence.mkdir(parents=True, mode=0o700)
        make.mkdir(mode=0o700)
        installer = make / inputs.ARCHIVE
        shutil.copyfile(original, installer)
        for source in (directory / 'signed/qualified').iterdir():
            inputs.attestation.archives._lstat_regular(source, inputs.candidate.MAX_REPORT_BYTES)
            shutil.copyfile(source, evidence / source.name)
        policy = source_root / 'desktop/release-policy.json'
        inputs.attestation.archives._lstat_regular(policy, inputs.candidate.MAX_REPORT_BYTES)
        shutil.copyfile(policy, staged / 'release-policy.json')
        manifests, sboms = list(evidence.glob('*.release.json')), list(evidence.glob('*.spdx.json'))
        if len(manifests) != 1 or len(sboms) != 1:
            raise ValueError('Desktop preview has no exact release evidence pair')
        subprocess.run(['node', str(verifier_root / 'desktop/scripts/verify-release-evidence.mjs'),
            '--artifact', str(installer), '--checksums', str(evidence / 'checksums.txt'),
            '--manifest', str(manifests[0]), '--policy', str(staged / 'release-policy.json'),
            '--sbom', str(sboms[0])], check=True, timeout=120)
        repeated = inputs.verify(directory, source_root, signer_root, run_id, attempt)
        if (inputs.candidate.canonical_bytes(repeated) != inputs.candidate.canonical_bytes(verified)
                or inputs.attestation.desktop.digest_file(installer) != archive['sha256']
                or inputs.attestation.desktop.digest_file(policy) != inputs.attestation.desktop.digest_file(staged / 'release-policy.json')):
            raise ValueError('Desktop preview evidence changed during staging')
        for source in (directory / 'signed/qualified').iterdir():
            if inputs.attestation.desktop.digest_file(source) != inputs.attestation.desktop.digest_file(evidence / source.name):
                raise ValueError('Desktop preview staged evidence differs from authenticated original')
        inputs.attestation.desktop.write_json(staged / 'nix-producer-binding.json', verified)
        if lifecycle_proof is not None:
            inputs.attestation.desktop.write_json(staged / 'nix-lifecycle-binding.json', lifecycle_proof)
        staged.rename(output)
    return verified


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('operation', choices=('fetch', 'stage'))
    parser.add_argument('--run-id', type=int, required=True)
    parser.add_argument('--attempt', type=int, required=True)
    parser.add_argument('--directory', type=Path, required=True)
    parser.add_argument('--github-output', type=Path)
    parser.add_argument('--source-root', type=Path)
    parser.add_argument('--signer-root', type=Path)
    parser.add_argument('--verifier-root', type=Path)
    parser.add_argument('--expected-source')
    parser.add_argument('--output', type=Path)
    parser.add_argument('--lifecycle-run', type=int)
    parser.add_argument('--lifecycle-attempt', type=int)
    args = parser.parse_args()
    os.umask(0o077)
    if args.operation == 'fetch':
        if args.github_output is None:
            raise ValueError('Desktop preview fetch requires identity output')
        result = inputs.fetch(args.run_id, args.attempt, args.directory)
        with args.github_output.open('a') as output:
            for field in ('sourceRevision', 'signerRevision'):
                output.write(f'{field}={result[field]}\n')
    else:
        if any(value is None for value in (args.source_root, args.signer_root,
                args.verifier_root, args.expected_source, args.output)):
            raise ValueError('Desktop preview stage requires exact source, signer and protected tools')
        if args.lifecycle_run is None or args.lifecycle_attempt is None:
            raise ValueError('Desktop preview publication requires completed exact-version lifecycle proof')
        verified = inputs.verify(args.directory, args.source_root, args.signer_root, args.run_id, args.attempt)
        proof = verify_lifecycle(args.lifecycle_run, args.lifecycle_attempt, verified)
        stage(args.directory, args.source_root, args.signer_root, args.verifier_root,
              args.run_id, args.attempt, args.expected_source, args.output, lifecycle_proof=proof)


if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError, inputs.github.HandoffError):
        raise SystemExit('Desktop preview rejected; exact signed qualified producer bytes are required') from None
