#!/usr/bin/env python3
"""Verify registry provenance and SPDX against exact Nix candidate/runtime content."""

import argparse
import json
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile

import nix_candidate_manifest as candidate
import nix_registry_content as registry

PROVENANCE = 'https://slsa.dev/provenance/v1'
SPDX = 'https://spdx.dev/Document/v2.3'
MAX_ATTESTATION_BYTES = candidate.MAX_REPORT_BYTES
WORKFLOWS = {
    'application-image': {'flidai/leapview/.github/workflows/artifacts.yml',
                          'flidai/leapview/.github/workflows/release.yml'},
    'site-image': {'flidai/leapview/.github/workflows/site-image.yml'},
}


def validate_signer(kind, workflow, revision):
    if workflow not in WORKFLOWS.get(kind, set()) or not candidate.REVISION.fullmatch(revision):
        raise ValueError('signer must be an approved output workflow at an exact protected revision')


def match_statement(entries, image, predicate_type, expected_predicate=None):
    """Only parse the verified result from a successful live gh invocation."""
    repository, digest = image.split('@')
    subject = [{'name': repository, 'digest': {'sha256': digest[7:]}}]
    if not isinstance(entries, list) or not entries:
        raise ValueError('verified attestation statements are missing')
    matches = []
    for entry in entries:
        if not isinstance(entry, dict):
            continue
        result = entry.get('verificationResult')
        statement = result.get('statement') if isinstance(result, dict) else None
        if not isinstance(statement, dict) or statement.get('_type') != 'https://in-toto.io/Statement/v1':
            continue
        if statement.get('subject') != subject or statement.get('predicateType') != predicate_type:
            continue
        predicate = statement.get('predicate')
        if not isinstance(predicate, dict):
            continue
        if expected_predicate is not None and candidate.canonical_bytes(predicate) != candidate.canonical_bytes(expected_predicate):
            continue
        matches.append(candidate.digest_bytes(candidate.canonical_bytes(statement)))
    if not matches:
        raise ValueError('verified statement differs from required subject, predicate or runtime SPDX')
    return sorted(set(matches))[0]


def verify_attestation(image, workflow, signer_revision, predicate_type, expected_predicate=None):
    with tempfile.TemporaryFile() as output:
        try:
            subprocess.run(['gh', 'attestation', 'verify', 'oci://' + image,
                            '--repo', 'flidai/leapview', '--signer-workflow', workflow,
                            '--source-digest', signer_revision, '--source-ref', 'refs/heads/main',
                            '--deny-self-hosted-runners', '--bundle-from-oci',
                            '--predicate-type', predicate_type, '--limit', '10', '--format', 'json'],
                           check=True, timeout=120, stdout=output, stderr=subprocess.DEVNULL)
        except (OSError, subprocess.SubprocessError):
            raise ValueError('trusted registry attestation verification failed') from None
        output.seek(0)
        entries = candidate.read_json(output.read(MAX_ATTESTATION_BYTES + 1), MAX_ATTESTATION_BYTES)
    return match_statement(entries, image, predicate_type, expected_predicate)


def collect(image, kind, records, paths, platforms, workflow, signer_revision):
    """Internal adapter: records must first be recomputed by candidate.verify."""
    validate_signer(kind, workflow, signer_revision)
    registry_record = registry.bind_registry(image, kind, records, platforms)
    provenance = verify_attestation(image, workflow, signer_revision, PROVENANCE)
    by_platform = {record['artifact']['platform']: (record, Path(paths[index][2]))
                   for index, record in enumerate(records)}
    spdx = []
    repository = image.split('@')[0]
    for platform in registry_record['contentBinding']['platforms']:
        record, directory = by_platform[platform['platform']]
        path = directory / 'sbom.spdx.json'
        expected = [entry['sha256'] for entry in record['evidence']['nix-runtime']['reports']
                    if entry['path'] == 'sbom.spdx.json']
        with path.open('rb') as stream:
            data = stream.read(candidate.MAX_REPORT_BYTES + 1)
        if len(expected) != 1 or not data or len(data) > candidate.MAX_REPORT_BYTES or candidate.digest_bytes(data) != expected[0]:
            raise ValueError('runtime SPDX changed or is missing from verified candidate evidence')
        document = candidate.read_json(data, candidate.MAX_REPORT_BYTES)
        platform_image = repository + '@' + platform['manifestDigest']
        statement = verify_attestation(platform_image, workflow, signer_revision, SPDX, document)
        spdx.append({'platform': platform['platform'], 'image': platform_image,
                     'candidateDigest': record['candidateDigest'], 'reportSHA256': expected[0],
                     'predicateSHA256': candidate.digest_bytes(candidate.canonical_bytes(document)),
                     'statementSHA256': statement})
    result = {'schemaVersion': 1, 'image': image, 'kind': kind, 'source': records[0]['source'],
              'registryBindingDigest': registry_record['registryBindingDigest'],
              'signer': {'workflow': workflow, 'sourceRevision': signer_revision, 'sourceRef': 'refs/heads/main'},
              'provenance': {'predicateType': PROVENANCE, 'statementSHA256': provenance},
              'spdx': spdx, 'releaseAdmission': False}
    result['signedEvidenceBindingDigest'] = candidate.digest_bytes(
        b'leapview/nix-signed-evidence/v1\n' + candidate.canonical_bytes(result))
    if len(json.dumps(result, indent=2).encode()) + 1 > candidate.MAX_JSON_BYTES:
        raise ValueError('signed evidence binding exceeds JSON byte limit')
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--image', required=True)
    parser.add_argument('--kind', choices=sorted(WORKFLOWS), required=True)
    parser.add_argument('--platform', action='append', required=True)
    parser.add_argument('--candidate', nargs=3, action='append', required=True,
                        metavar=('ARCHIVE', 'MANIFEST', 'RUNTIME_EVIDENCE'))
    parser.add_argument('--expected-workflow', required=True)
    parser.add_argument('--signer-revision', required=True,
                        help='protected workflow event SHA; distinct from candidate source on workflow_dispatch')
    output = parser.add_mutually_exclusive_group(required=True)
    output.add_argument('--output', type=Path)
    output.add_argument('--verify', type=Path)
    args = parser.parse_args()
    os.umask(0o077)
    try:
        validate_signer(args.kind, args.expected_workflow, args.signer_revision)
        registry.image_digest(args.image, args.kind)
        source = candidate.checkout_source(candidate.ROOT)
        records = registry.verified_records(args.candidate, args.kind, source)
        result = collect(args.image, args.kind, records, args.candidate, args.platform,
                         args.expected_workflow, args.signer_revision)
        current = registry.verified_records(args.candidate, args.kind, candidate.checkout_source(candidate.ROOT))
        if candidate.canonical_bytes(current) != candidate.canonical_bytes(records):
            raise ValueError('candidate changed during signed evidence verification')
        if args.verify:
            if candidate.canonical_bytes(candidate.read_json_file(args.verify)) != candidate.canonical_bytes(result):
                raise ValueError('signed evidence binding differs from freshly verified evidence')
        else:
            args.output.parent.mkdir(parents=True, exist_ok=True)
            with args.output.open('x') as stream:
                stream.write(json.dumps(result, indent=2) + '\n')
        print(json.dumps({'image': args.image, 'signedEvidenceBindingDigest': result['signedEvidenceBindingDigest'],
                          'releaseAdmission': False}))
    except (ValueError, KeyError, TypeError, OSError, EOFError, tarfile.TarError, subprocess.SubprocessError) as error:
        raise SystemExit('signed evidence binding rejected: ' + str(error)) from error


if __name__ == '__main__':
    main()
