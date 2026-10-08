#!/usr/bin/env python3
"""Bind protected Desktop attestations to original qualified Debian bytes."""

import argparse
from pathlib import Path
import subprocess
import tempfile

import nix_candidate_manifest as candidate
import nix_cli_publication as archives
import nix_desktop_qualification as desktop


WORKFLOW = 'flidai/leapview/.github/workflows/nix-desktop-candidate.yml'
ARCHIVE_NAME = 'leapview-desktop-linux-x64.deb'
PROVENANCE = 'https://slsa.dev/provenance/v1'
SPDX = 'https://spdx.dev/Document/v2.3'


def inventory(directory):
    directory = Path(directory)
    if directory.is_symlink() or not directory.is_dir():
        raise ValueError('Desktop evidence directory must be regular')
    paths = sorted(directory.iterdir())
    if len(paths) != 7:
        raise ValueError('Desktop evidence inventory is not closed')
    return [{
        'path': path.name,
        'sha256': candidate.digest_bytes(archives._lstat_regular(path, candidate.MAX_REPORT_BYTES)),
    } for path in paths]


def verify(original_deb, qualified, source_root, verifier_root, source_revision):
    original_deb, qualified = Path(original_deb), Path(qualified)
    deb = qualified / 'candidate' / ARCHIVE_NAME
    evidence = qualified / 'qualified'
    if original_deb.name != ARCHIVE_NAME:
        raise ValueError('original Desktop candidate basename differs')
    original_hash = candidate.digest_bytes(archives._lstat_regular(original_deb, desktop.MAX_DEB_BYTES))
    copied_hash = candidate.digest_bytes(archives._lstat_regular(deb, desktop.MAX_DEB_BYTES))
    if original_hash != copied_hash:
        raise ValueError('qualified Desktop package differs from original builder artifact')
    before = inventory(evidence)
    report = desktop.verify_report(deb, source_root, verifier_root, source_revision, evidence)
    expected_paths = sorted([entry['path'] for entry in report['evidence']] + ['qualification-report.json'])
    if [entry['path'] for entry in before] != expected_paths or inventory(evidence) != before:
        raise ValueError('Desktop qualification evidence inventory differs or changed')
    if report['artifact']['sha256'] != original_hash:
        raise ValueError('Desktop qualification belongs to another builder artifact')
    return {
        'schemaVersion': 1,
        'archive': {'basename': ARCHIVE_NAME, 'sha256': original_hash},
        'sourceRevision': source_revision,
        'candidateDigest': report['candidateDigest'],
        'qualificationSHA256': next(entry['sha256'] for entry in before
                                    if entry['path'] == 'qualification-report.json'),
        'evidence': before,
        'releaseAdmission': False,
    }


def spdx_bytes(qualified):
    paths = list((Path(qualified) / 'qualified').glob('*.spdx.json'))
    if len(paths) != 1:
        raise ValueError('Desktop qualification requires exactly one SPDX predicate')
    return archives._lstat_regular(paths[0], candidate.MAX_REPORT_BYTES)


def verify_attestation(path, digest, signer_revision, predicate_type, expected_predicate=None):
    if not candidate.REVISION.fullmatch(signer_revision):
        raise ValueError('Desktop signer revision must be an exact protected commit')
    with tempfile.TemporaryFile() as output:
        command = ['gh', 'attestation', 'verify', str(path), '--repo', 'flidai/leapview',
                   '--signer-workflow', WORKFLOW, '--source-digest', signer_revision,
                   '--source-ref', 'refs/heads/main', '--deny-self-hosted-runners',
                   '--predicate-type', predicate_type, '--limit', '10', '--format', 'json']
        try:
            subprocess.run(command, check=True, timeout=120, stdout=output, stderr=subprocess.DEVNULL)
        except (OSError, subprocess.SubprocessError):
            raise ValueError('protected Desktop attestation verification failed') from None
        output.seek(0)
        entries = candidate.read_json(output.read(candidate.MAX_REPORT_BYTES + 1), candidate.MAX_REPORT_BYTES)
    return archives._match_attestation(entries, path, digest, predicate_type, expected_predicate)


def verify_signed(original_deb, original_qualified, qualified, source_root, verifier_root,
                  source_revision, signer_revision):
    original = verify(original_deb, original_qualified, source_root, verifier_root, source_revision)
    current = verify(original_deb, qualified, source_root, verifier_root, source_revision)
    if candidate.canonical_bytes(original) != candidate.canonical_bytes(current):
        raise ValueError('signed Desktop evidence differs from original qualification artifact')
    if candidate.checkout_source(Path(verifier_root))['revision'] != signer_revision:
        raise ValueError('Desktop signer revision differs from the protected verifier')
    qualified = Path(qualified)
    deb = qualified / 'candidate' / ARCHIVE_NAME
    sbom_bytes = spdx_bytes(qualified)
    sbom = candidate.read_json(sbom_bytes, candidate.MAX_REPORT_BYTES)
    provenance = verify_attestation(deb, current['archive']['sha256'], signer_revision, PROVENANCE)
    spdx = verify_attestation(deb, current['archive']['sha256'], signer_revision, SPDX, sbom)
    receipt = verify_attestation(qualified / 'qualified/qualification-report.json',
                                 current['qualificationSHA256'], signer_revision, PROVENANCE)
    if (candidate.canonical_bytes(verify(original_deb, qualified, source_root, verifier_root,
                                        source_revision)) != candidate.canonical_bytes(current)
            or spdx_bytes(qualified) != sbom_bytes):
        raise ValueError('Desktop candidate or evidence changed during live verification')
    binding = {
        **current,
        'signer': {'workflow': WORKFLOW, 'sourceRevision': signer_revision, 'sourceRef': 'refs/heads/main'},
        'provenance': {'predicateType': PROVENANCE, 'statementSHA256': provenance},
        'spdx': {'predicateType': SPDX, 'statementSHA256': spdx,
                 'reportSHA256': candidate.digest_bytes(sbom_bytes),
                 'predicateSHA256': candidate.digest_bytes(candidate.canonical_bytes(sbom))},
        'qualificationProvenance': {'predicateType': PROVENANCE, 'statementSHA256': receipt},
    }
    binding['signedEvidenceBindingDigest'] = candidate.digest_bytes(
        b'leapview/nix-desktop-signed-evidence/v1\n' + candidate.canonical_bytes(binding))
    return binding


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('operation', choices=('verify', 'verify-signed'))
    parser.add_argument('--original-deb', type=Path, required=True)
    parser.add_argument('--qualified', type=Path, required=True)
    parser.add_argument('--source-root', type=Path, required=True)
    parser.add_argument('--verifier-root', type=Path, required=True)
    parser.add_argument('--source-revision', required=True)
    parser.add_argument('--original-qualified', type=Path)
    parser.add_argument('--signer-revision')
    parser.add_argument('--predicate-output', type=Path)
    parser.add_argument('--output', type=Path)
    args = parser.parse_args()
    try:
        if args.operation == 'verify-signed':
            if not args.original_qualified or not args.signer_revision or not args.output:
                raise ValueError('live verification requires original qualification, signer revision and output')
            result = verify_signed(args.original_deb, args.original_qualified, args.qualified,
                                   args.source_root, args.verifier_root, args.source_revision, args.signer_revision)
            archives._write_json_new(args.output, result)
        else:
            result = verify(args.original_deb, args.qualified, args.source_root, args.verifier_root,
                            args.source_revision)
            if args.predicate_output:
                archives._write_new(args.predicate_output, spdx_bytes(args.qualified))
        print(candidate.canonical_bytes(result).decode())
    except (OSError, ValueError, TypeError, KeyError, subprocess.SubprocessError) as error:
        raise SystemExit('Desktop attestation rejected: ' + str(error)) from error


if __name__ == '__main__':
    main()
