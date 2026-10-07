"""Publish a verified Linux Nix candidate; retain no production release authority."""

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

WORKFLOWS = {
    'application-image': 'flidai/leapview/.github/workflows/nix-candidate.yml',
    'site-image': 'flidai/leapview/.github/workflows/nix-site-candidate.yml',
}
TAG_PREFIXES = {'application-image': 'nix-candidate', 'site-image': 'nix-site-candidate'}
MAX_PREDICATE_BYTES = 16 * 1024 * 1024
IMAGE_PHASES = [('image bundle', 1200), ('target bootstrap', 1200),
                ('enterprise authoring', 1800), ('performance', 2700)]
SITE_CHECKS = ('native-platform', 'read-only-nonroot', 'health', 'readiness',
               'public-release', 'build-identity', 'installation-docs')


def verified_record(source_root, paths, binary_verifier, kind):
    # Only source identity comes from the candidate checkout. candidate.ROOT,
    # imported verifier code, policy and assessments stay at the protected revision.
    archive, manifest, runtime = map(Path, paths)
    result = candidate.verify(candidate.read_json_file(manifest), archive,
                              candidate.checkout_source(source_root), kind=kind,
                              runtime_dir=runtime, go_dir=runtime / 'go', binary_verifier=binary_verifier)
    if kind == 'site-image':
        local_site_qualification(runtime / 'site-qualification-report.json', result)
    return result


def record_platform(record, kind):
    artifact = record.get('artifact')
    if not isinstance(artifact, dict) or artifact.get('kind') != kind:
        raise ValueError('producer accepts only the selected Nix image kind')
    platform = artifact.get('platform')
    if not isinstance(platform, str) or platform not in candidate.PLATFORMS:
        raise ValueError('producer requires a supported Linux image platform')
    return platform


def platform_architecture(platform):
    return {'linux/amd64': 'amd64', 'linux/arm64': 'arm64'}[platform]


def bound_spdx(record, paths, kind):
    record_platform(record, kind)
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


def unchanged(source_root, paths, before, binary_verifier, kind):
    after = verified_record(source_root, paths, binary_verifier, kind)
    if candidate.canonical_bytes(before) != candidate.canonical_bytes(after):
        raise ValueError('candidate changed during publication verification')


def collect_signed(record, paths, image, signer_revision, kind):
    document = bound_spdx(record, paths, kind)
    platform = record_platform(record, kind)
    result = signed.collect(image, kind, [record], [paths], [platform], WORKFLOWS[kind], signer_revision)
    if (not isinstance(result, dict) or not isinstance(result.get('spdx'), list) or len(result['spdx']) != 1
            or not isinstance(result['spdx'][0], dict)
            or result['spdx'][0].get('platform') != platform
            or result['spdx'][0].get('candidateDigest') != record['candidateDigest']):
        raise ValueError('signed SPDX evidence does not bind the verified candidate platform')
    # collect also matches SPDX against the bound report; retain this explicit
    # read before any network call to enforce the producer's smaller size budget.
    if candidate.digest_bytes(candidate.canonical_bytes(document)) != result['spdx'][0]['predicateSHA256']:
        raise ValueError('signed SPDX differs from the producer document')
    return result


def record(source_root, paths, binary_verifier, kind):
    result = candidate.collect(Path(paths[0]), kind, candidate.checkout_source(source_root),
                               runtime_dir=Path(paths[2]), go_dir=Path(paths[2]) / 'go',
                               binary_verifier=binary_verifier)
    if kind == 'site-image':
        local_site_qualification(Path(paths[2]) / 'site-qualification-report.json', result)
    bound_spdx(result, paths, kind)
    return result


def prepare(source_root, paths, layout, binary_verifier, kind):
    record = verified_record(source_root, paths, binary_verifier, kind)
    site_report = (local_site_qualification(Path(paths[2]) / 'site-qualification-report.json', record)
                   if kind == 'site-image' else None)
    bound_spdx(record, paths, kind)
    digest = oci.export_layout(Path(paths[0]), record, layout)
    result = oci.bind(layout, digest, [record], [record_platform(record, kind)])
    unchanged(source_root, paths, record, binary_verifier, kind)
    if kind == 'site-image':
        if local_site_qualification(Path(paths[2]) / 'site-qualification-report.json', record) != site_report:
            raise ValueError('site qualification report changed during candidate preparation')
        # Keep the standard OCI binding independently verifiable. Site runtime
        # evidence belongs to this producer's receipt, not the OCI wire format.
        result = {'schemaVersion': 1, 'contentBinding': result,
                  'siteQualification': site_report, 'releaseAdmission': False}
        result['preparationDigest'] = candidate.digest_bytes(
            b'leapview/nix-site-preparation/v1\n' + candidate.canonical_bytes(result))
    return result


def publish(source_root, paths, layout, run_id, attempt, binary_verifier, kind):
    if any(type(value) is not int or value <= 0 for value in [run_id, attempt]):
        raise ValueError('publication requires a positive workflow run and attempt identity')
    if kind not in WORKFLOWS:
        raise ValueError('publication requires a supported Nix image kind')
    record = verified_record(source_root, paths, binary_verifier, kind)
    site_report = (local_site_qualification(Path(paths[2]) / 'site-qualification-report.json', record)
                   if kind == 'site-image' else None)
    bound_spdx(record, paths, kind)
    platform = record_platform(record, kind)
    descriptor = candidate.read_json_file(oci.local_file(layout, 'index.json'))['manifests'][0]
    digest = descriptor['digest']
    oci.bind(layout, digest, [record], [platform])
    repository = registry.REPOSITORIES[kind]
    tag = f'{repository}:{TAG_PREFIXES[kind]}-{run_id}-{attempt}-{platform_architecture(platform)}'
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
    binding = registry.bind_registry(image, kind, [record], [platform])
    unchanged(source_root, paths, record, binary_verifier, kind)
    if kind == 'site-image':
        if local_site_qualification(Path(paths[2]) / 'site-qualification-report.json', record) != site_report:
            raise ValueError('site qualification report changed during candidate publication')
    result = {'schemaVersion': 1, 'image': image, 'tag': tag,
              'source': record['source'], 'candidateDigest': record['candidateDigest'],
              'registryBindingDigest': binding['registryBindingDigest'], 'releaseAdmission': False}
    if kind == 'site-image':
        result['siteQualification'] = site_report
    result['publicationDigest'] = candidate.digest_bytes(
        b'leapview/nix-candidate-publication/v1\n' + candidate.canonical_bytes(result))
    return result


def verify_signed(source_root, paths, image, signer_revision, binary_verifier, kind):
    record = verified_record(source_root, paths, binary_verifier, kind)
    result = collect_signed(record, paths, image, signer_revision, kind)
    unchanged(source_root, paths, record, binary_verifier, kind)
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


def validate_site_qualification_report(data, image, record):
    report = candidate.read_json(data)
    artifact = record.get('artifact')
    source = record.get('source')
    if (not isinstance(artifact, dict) or artifact.get('kind') != 'site-image'
            or not isinstance(source, dict)
            or set(report) != {'schemaVersion', 'result', 'sourceRevision', 'platform', 'image',
                               'archiveSHA256', 'releaseAdmission', 'startedAt', 'completedAt', 'checks'}
            or type(report['schemaVersion']) is not int or report['schemaVersion'] != 1
            or report['result'] != 'success' or report['sourceRevision'] != source.get('revision')
            or report['platform'] != artifact.get('platform') or report['image'] != image
            or report['archiveSHA256'] != artifact.get('sha256')
            or report['releaseAdmission'] is not False
            or not isinstance(report['checks'], dict) or set(report['checks']) != set(SITE_CHECKS)
            or any(value is not True for value in report['checks'].values())):
        raise ValueError('site qualification must bind every required check to the exact candidate and image')
    try:
        started, completed = (datetime.fromisoformat(report[key])
                              for key in ('startedAt', 'completedAt'))
    except (TypeError, ValueError):
        raise ValueError('site qualification timestamps must be ISO-8601 UTC values') from None
    now = candidate.current_time()
    if (started.tzinfo is None or completed.tzinfo is None
            or started.utcoffset() != timedelta(0) or completed.utcoffset() != timedelta(0)
            or started > completed or completed > now
            or now - started >= timedelta(hours=120)
            or completed - started > timedelta(minutes=15)):
        raise ValueError('site qualification timestamps must be fresh, bounded and ordered UTC values')
    return data


def site_qualification_report(path, image, record):
    data = regular_json_bytes(path)
    return validate_site_qualification_report(data, image, record)


def local_site_qualification(path, record):
    data = regular_json_bytes(path)
    report = candidate.read_json(data)
    image_id = report.get('image') if isinstance(report, dict) else None
    if not isinstance(image_id, str) or not candidate.SHA256.fullmatch(image_id):
        raise ValueError('prepublication site qualification must name a local Docker image ID')
    validate_site_qualification_report(data, image_id, record)
    return {'path': 'site-qualification-report.json', 'sha256': candidate.digest_bytes(data)}


def bind_qualified(source_root, paths, image, signer_revision, binary_verifier,
                   signed_evidence, report_path, kind):
    registry.image_digest(image, kind)
    if kind == 'site-image':
        record = verified_record(source_root, paths, binary_verifier, kind)
        report = site_qualification_report(report_path, image, record)
    else:
        report = qualification_report(report_path, image)
        record = verified_record(source_root, paths, binary_verifier, kind)
    receipt = regular_json_bytes(signed_evidence)
    platform = record_platform(record, kind)
    live = collect_signed(record, paths, image, signer_revision, kind)
    unchanged(source_root, paths, record, binary_verifier, kind)
    if candidate.canonical_bytes(candidate.read_json(receipt)) != candidate.canonical_bytes(live):
        raise ValueError('retained signature evidence differs from live protected verification')
    verify_report = (site_qualification_report(report_path, image, record) if kind == 'site-image'
                     else qualification_report(report_path, image))
    if (verify_report != report
            or regular_json_bytes(signed_evidence) != receipt):
        raise ValueError('qualification or signature reports changed during verification')
    report_name = 'site-qualification-report.json' if kind == 'site-image' else 'image-qualification-report.json'
    result = {'schemaVersion': 1, 'image': image, 'platform': platform, 'source': live['source'],
              'candidateDigest': live['spdx'][0]['candidateDigest'],
              'signedEvidenceBindingDigest': live['signedEvidenceBindingDigest'],
              'qualifierRevision': signer_revision,
              'qualification': {'path': report_name,
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
    parser.add_argument('--kind', choices=sorted(WORKFLOWS), required=True)
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
            result = record(args.source_root, args.candidate, args.binary_verifier, args.kind)
        elif args.operation in {'verify-signed', 'bind-qualified'}:
            if (not args.image or not args.signer_revision or args.layout
                    or args.run_id is not None or args.run_attempt is not None or args.github_output):
                raise ValueError('signature verification requires image and protected signer revision only')
            if args.operation == 'bind-qualified':
                if not args.signed_evidence or not args.qualification_report:
                    raise ValueError('qualified binding requires retained signatures and the exact image report')
                result = bind_qualified(args.source_root, args.candidate, args.image, args.signer_revision,
                                        args.binary_verifier, args.signed_evidence, args.qualification_report,
                                        args.kind)
            else:
                result = verify_signed(args.source_root, args.candidate, args.image, args.signer_revision,
                                       args.binary_verifier, args.kind)
        else:
            if not args.layout or args.image or args.signer_revision:
                raise ValueError('content publication requires a layout and no signed-image arguments')
            if args.operation == 'prepare':
                if args.run_id is not None or args.run_attempt is not None or args.github_output:
                    raise ValueError('preparation cannot publish or export signing outputs')
                result = prepare(args.source_root, args.candidate, args.layout, args.binary_verifier, args.kind)
            else:
                result = publish(args.source_root, args.candidate, args.layout, args.run_id, args.run_attempt,
                                 args.binary_verifier, args.kind)
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
