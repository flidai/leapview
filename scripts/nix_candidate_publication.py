"""Publish a verified AMD64 Nix candidate; retain no production release authority."""

import argparse
from datetime import datetime, timedelta
import json
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile

import nix_candidate_manifest as candidate
import nix_oci_content as oci
import nix_registry_content as registry
import nix_signed_evidence as signed

KIND = 'application-image'
PLATFORM = 'linux/amd64'
WORKFLOW = 'flidai/leapview/.github/workflows/nix-candidate.yml'
MAX_PREDICATE_BYTES = 16 * 1024 * 1024
IMAGE_PHASES = [('image bundle', 1200), ('target bootstrap', 1200),
                ('enterprise authoring', 1800), ('performance', 2700)]


def verified_record(source_root, paths, binary_verifier):
    # Only source identity comes from the candidate checkout. candidate.ROOT,
    # imported verifier code, policy and assessments stay at the protected revision.
    archive, manifest, runtime = map(Path, paths)
    return candidate.verify(candidate.read_json_file(manifest), archive,
                            candidate.checkout_source(source_root), kind=KIND,
                            runtime_dir=runtime, go_dir=runtime / 'go', binary_verifier=binary_verifier)


def bound_spdx(record, paths):
    if record['artifact']['kind'] != KIND or record['artifact']['platform'] != PLATFORM:
        raise ValueError('producer qualifies only the AMD64 application candidate')
    path = Path(paths[2]) / 'sbom.spdx.json'
    expected = [r['sha256'] for r in record['evidence']['nix-runtime']['reports']
                if r['path'] == 'sbom.spdx.json']
    if path.is_symlink() or not path.is_file():
        raise ValueError('runtime SPDX must be a regular report')
    with path.open('rb') as stream:
        data = stream.read(MAX_PREDICATE_BYTES + 1)
    if (len(expected) != 1 or not data or len(data) > MAX_PREDICATE_BYTES
            or candidate.digest_bytes(data) != expected[0]):
        raise ValueError('runtime SPDX changed or exceeds the attestation predicate limit')
    return candidate.read_json(data, MAX_PREDICATE_BYTES)


def unchanged(source_root, paths, before, binary_verifier):
    after = verified_record(source_root, paths, binary_verifier)
    if candidate.canonical_bytes(before) != candidate.canonical_bytes(after):
        raise ValueError('candidate changed during publication verification')


def record(source_root, paths, binary_verifier):
    result = candidate.collect(Path(paths[0]), KIND, candidate.checkout_source(source_root),
                               runtime_dir=Path(paths[2]), go_dir=Path(paths[2]) / 'go',
                               binary_verifier=binary_verifier)
    bound_spdx(result, paths)
    return result


def prepare(source_root, paths, layout, binary_verifier):
    record = verified_record(source_root, paths, binary_verifier)
    bound_spdx(record, paths)
    digest = oci.export_layout(Path(paths[0]), record, layout)
    result = oci.bind(layout, digest, [record], [PLATFORM])
    unchanged(source_root, paths, record, binary_verifier)
    return result


def publish(source_root, paths, layout, run_id, attempt, binary_verifier):
    if any(type(value) is not int or value <= 0 for value in [run_id, attempt]):
        raise ValueError('publication requires a positive workflow run and attempt identity')
    record = verified_record(source_root, paths, binary_verifier)
    bound_spdx(record, paths)
    descriptor = candidate.read_json_file(oci.local_file(layout, 'index.json'))['manifests'][0]
    digest = descriptor['digest']
    oci.bind(layout, digest, [record], [PLATFORM])
    repository = registry.REPOSITORIES[KIND]
    tag = f'{repository}:nix-candidate-{run_id}-{attempt}'
    with tempfile.TemporaryDirectory(prefix='leapview-nix-publish-') as temporary:
        digest_file = Path(temporary) / 'digest'
        try:
            subprocess.run(['skopeo', 'copy', '--all', '--preserve-digests',
                            '--dest-tls-verify=true', '--digestfile', str(digest_file),
                            'oci:' + str(layout) + ':candidate', 'docker://' + tag],
                           check=True, timeout=300, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        except (OSError, subprocess.SubprocessError):
            raise ValueError('candidate registry publication failed') from None
        with digest_file.open('rb') as stream:
            returned = stream.read(128).decode().strip()
        if returned != digest:
            raise ValueError('published registry digest differs from verified OCI content')
    image = repository + '@' + digest
    binding = registry.bind_registry(image, KIND, [record], [PLATFORM])
    unchanged(source_root, paths, record, binary_verifier)
    result = {'schemaVersion': 1, 'image': image, 'tag': tag,
              'source': record['source'], 'candidateDigest': record['candidateDigest'],
              'registryBindingDigest': binding['registryBindingDigest'], 'releaseAdmission': False}
    result['publicationDigest'] = candidate.digest_bytes(
        b'leapview/nix-candidate-publication/v1\n' + candidate.canonical_bytes(result))
    return result


def verify_signed(source_root, paths, image, signer_revision, binary_verifier):
    record = verified_record(source_root, paths, binary_verifier)
    document = bound_spdx(record, paths)
    result = signed.collect(image, KIND, [record], [paths], [PLATFORM], WORKFLOW, signer_revision)
    # collect also matches SPDX against the bound report; retain this explicit
    # read before any network call to enforce the producer's smaller size budget.
    if candidate.digest_bytes(candidate.canonical_bytes(document)) != result['spdx'][0]['predicateSHA256']:
        raise ValueError('signed SPDX differs from the producer document')
    unchanged(source_root, paths, record, binary_verifier)
    return result


def regular_json_bytes(path):
    path = Path(path)
    if path.is_symlink() or not path.is_file():
        raise ValueError('qualification evidence must be a regular file')
    with path.open('rb') as stream:
        data = stream.read(candidate.MAX_JSON_BYTES + 1)
    candidate.read_json(data)
    return data


def qualification_report(path, image):
    data = regular_json_bytes(path)
    report = candidate.read_json(data)
    if (not isinstance(report, dict) or set(report) != {'schemaVersion', 'result', 'image', 'phases'}
            or type(report['schemaVersion']) is not int or report['schemaVersion'] != 1
            or report['result'] != 'success' or report['image'] != image
            or not isinstance(report['phases'], list) or len(report['phases']) != len(IMAGE_PHASES)):
        raise ValueError('qualification must succeed for the exact published image')
    now, previous_end = candidate.current_time(), None
    for phase, (name, timeout) in zip(report['phases'], IMAGE_PHASES):
        if (not isinstance(phase, dict) or set(phase) != {'name', 'result', 'startedAt',
                'durationMillis', 'timeoutSeconds', 'cleanupGuaranteed'}
                or phase['name'] != name or phase['result'] != 'success'
                or phase['cleanupGuaranteed'] is not True or type(phase['timeoutSeconds']) is not int
                or phase['timeoutSeconds'] != timeout or type(phase['durationMillis']) is not int
                or not 0 <= phase['durationMillis'] <= timeout * 1000
                or not isinstance(phase['startedAt'], str)):
            raise ValueError('qualification phases are incomplete or outside their bounded contract')
        started = datetime.fromisoformat(phase['startedAt'])
        if started.tzinfo is None:
            raise ValueError('qualification phase timestamps must include a timezone')
        ended = started + timedelta(milliseconds=phase['durationMillis'])
        if (started > now or ended > now or now - started >= timedelta(hours=120)
                or (previous_end is not None and started < previous_end)):
            raise ValueError('qualification phases are stale, future-dated or overlapping')
        previous_end = ended
    return data


def bind_qualified(source_root, paths, image, signer_revision, binary_verifier,
                   signed_evidence, report_path):
    registry.image_digest(image, KIND)
    report = qualification_report(report_path, image)
    receipt = regular_json_bytes(signed_evidence)
    live = verify_signed(source_root, paths, image, signer_revision, binary_verifier)
    if candidate.canonical_bytes(candidate.read_json(receipt)) != candidate.canonical_bytes(live):
        raise ValueError('retained signature evidence differs from live protected verification')
    if (qualification_report(report_path, image) != report
            or regular_json_bytes(signed_evidence) != receipt):
        raise ValueError('qualification or signature reports changed during verification')
    result = {'schemaVersion': 1, 'image': image, 'platform': PLATFORM, 'source': live['source'],
              'candidateDigest': live['spdx'][0]['candidateDigest'],
              'signedEvidenceBindingDigest': live['signedEvidenceBindingDigest'],
              'qualifierRevision': signer_revision,
              'qualification': {'path': 'image-qualification-report.json',
                                'sha256': candidate.digest_bytes(report)},
              'releaseAdmission': False}
    result['qualifiedEvidenceBindingDigest'] = candidate.digest_bytes(
        b'leapview/nix-published-qualification/v1\n' + candidate.canonical_bytes(result))
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('operation', choices=['record', 'prepare', 'publish', 'verify-signed', 'bind-qualified'])
    parser.add_argument('--source-root', type=Path, required=True)
    parser.add_argument('--source-revision', required=True)
    parser.add_argument('--binary-verifier', type=Path, required=True)
    parser.add_argument('--candidate', nargs=3, required=True,
                        metavar=('ARCHIVE', 'MANIFEST', 'RUNTIME_EVIDENCE'))
    parser.add_argument('--layout', type=Path)
    parser.add_argument('--run-id', type=int)
    parser.add_argument('--run-attempt', type=int)
    parser.add_argument('--image')
    parser.add_argument('--signer-revision')
    parser.add_argument('--signed-evidence', type=Path)
    parser.add_argument('--qualification-report', type=Path)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--github-output', type=Path)
    args = parser.parse_args()
    os.umask(0o077)
    try:
        source = candidate.checkout_source(args.source_root)
        if not candidate.REVISION.fullmatch(args.source_revision) or source['revision'] != args.source_revision:
            raise ValueError('checkout differs from authorized exact candidate revision')
        if args.output.exists():
            raise ValueError('refusing to overwrite publication evidence')
        if args.operation != 'bind-qualified' and (args.signed_evidence or args.qualification_report):
            raise ValueError('qualification reports require the bind-qualified operation')
        if args.operation == 'record':
            if (args.layout or args.run_id is not None or args.run_attempt is not None
                    or args.image or args.signer_revision or args.github_output
                    or args.output.resolve() != Path(args.candidate[1]).resolve()):
                raise ValueError('record requires the candidate manifest as its only output')
            result = record(args.source_root, args.candidate, args.binary_verifier)
        elif args.operation in {'verify-signed', 'bind-qualified'}:
            if (not args.image or not args.signer_revision or args.layout
                    or args.run_id is not None or args.run_attempt is not None or args.github_output):
                raise ValueError('signature verification requires image and protected signer revision only')
            if args.operation == 'bind-qualified':
                if not args.signed_evidence or not args.qualification_report:
                    raise ValueError('qualified binding requires retained signatures and the exact image report')
                result = bind_qualified(args.source_root, args.candidate, args.image, args.signer_revision,
                                        args.binary_verifier, args.signed_evidence, args.qualification_report)
            else:
                result = verify_signed(args.source_root, args.candidate, args.image, args.signer_revision,
                                       args.binary_verifier)
        else:
            if not args.layout or args.image or args.signer_revision:
                raise ValueError('content publication requires a layout and no signed-image arguments')
            if args.operation == 'prepare':
                if args.run_id is not None or args.run_attempt is not None or args.github_output:
                    raise ValueError('preparation cannot publish or export signing outputs')
                result = prepare(args.source_root, args.candidate, args.layout, args.binary_verifier)
            else:
                result = publish(args.source_root, args.candidate, args.layout, args.run_id, args.run_attempt,
                                 args.binary_verifier)
        if candidate.canonical_bytes(candidate.checkout_source(args.source_root)) != candidate.canonical_bytes(source):
            raise ValueError('candidate source changed during publication verification')
        if len(json.dumps(result, indent=2).encode()) + 1 > candidate.MAX_JSON_BYTES:
            raise ValueError('publication evidence exceeds its JSON byte limit')
        args.output.parent.mkdir(parents=True, exist_ok=True)
        with args.output.open('x') as stream:
            stream.write(json.dumps(result, indent=2) + '\n')
        if args.github_output:
            with args.github_output.open('a') as stream:
                stream.write('image=' + result['image'] + '\n')
                stream.write('digest=' + result['image'].split('@')[1] + '\n')
        print(json.dumps({'operation': args.operation, 'releaseAdmission': False}))
    except (ValueError, KeyError, IndexError, TypeError, OSError, EOFError,
            tarfile.TarError, subprocess.SubprocessError) as error:
        raise SystemExit('candidate publication rejected: ' + str(error)) from error


if __name__ == '__main__':
    main()
