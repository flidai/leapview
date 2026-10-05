#!/usr/bin/env python3
"""Qualify Nix runtime inventory and NVD matching; never replace OCI admission."""
import argparse
import copy
from collections import Counter
from datetime import date, datetime, timezone
import hashlib
import importlib.util
import gzip
import shutil
import tempfile
import json
import os
from pathlib import Path, PurePosixPath
import re
import subprocess
import sys
import tarfile

ROOT = Path(__file__).resolve().parents[1]
POLICY = json.loads((ROOT / 'nix/runtime-security-policy.json').read_text())
SITE_POLICY_PATH = ROOT / 'nix/site-runtime-security-policy.json'
SITE_POLICY = json.loads(SITE_POLICY_PATH.read_text())
SITE_RUNTIME_REPORTS = {
    'sbom.syft.json', 'sbom.spdx.json', 'runtime.syft.json', 'runtime.grype.json',
    'controls.synthetic.syft.json', 'controls.grype.json',
    'syft-config.json', 'grype-config.json', 'site-image-inventory.json',
}
NIX_SYSTEMS = {'linux/amd64': 'x86_64-linux', 'linux/arm64': 'aarch64-linux'}
_candidate_spec = importlib.util.spec_from_file_location(
    'nix_candidate_manifest_runtime', ROOT / 'scripts/nix_candidate_manifest.py')
if _candidate_spec is None or _candidate_spec.loader is None:
    raise RuntimeError('cannot load the candidate manifest contract')
candidate_manifest = importlib.util.module_from_spec(_candidate_spec)
_candidate_spec.loader.exec_module(candidate_manifest)
sys.modules.setdefault('nix_candidate_manifest', candidate_manifest)
_go_evidence_spec = importlib.util.spec_from_file_location(
    'nix_archive_go_evidence_runtime', ROOT / 'scripts/nix_archive_go_evidence.py')
if _go_evidence_spec is None or _go_evidence_spec.loader is None:
    raise RuntimeError('cannot load the exact image content reader')
go_evidence = importlib.util.module_from_spec(_go_evidence_spec)
_go_evidence_spec.loader.exec_module(go_evidence)


def write(path, value):
    path.write_text(json.dumps(value, indent=2) + '\n')


def run(*args, env=None):
    return subprocess.check_output(args, text=True, env=env)


def native_glibc_path(platform, env=None):
    try:
        system = NIX_SYSTEMS[platform]
    except KeyError as error:
        raise ValueError('unsupported native runtime platform: ' + str(platform)) from error
    return run('nix', 'eval', '--no-update-lock-file', '--raw',
               '.#packages.' + system + '.glibc-runtime.outPath', env=env).strip()


def assessment_file(platform):
    return candidate_manifest.runtime_assessment_path(platform, root=ROOT)


def store_paths(archive, *, allow_empty=False):
    """Inventory every layer independently of Syft, without extracting files.

    Nix's layered images are additive. Treat even a removed store path as present
    rather than allowing whiteouts to hide an unclassified dependency.
    """
    paths = set()
    with tarfile.open(archive, 'r|*') as outer:
        for member in outer:
            if member.name.endswith('/layer.tar'):
                with tarfile.open(fileobj=outer.extractfile(member), mode='r|*') as layer:
                    for entry in layer:
                        parts = entry.name.removeprefix('./').lstrip('/').split('/')
                        if len(parts) >= 3 and parts[:2] == ['nix', 'store']:
                            if re.fullmatch(r'[0-9a-z]{32}-.+', parts[2]):
                                paths.add('/nix/store/' + parts[2])
    if not paths and not allow_empty:
        raise ValueError('archive has no Nix store inventory')
    return paths


def check_inventory(sbom, paths, expected_glibc_path=None, policy=POLICY):
    packages = [p for p in sbom['artifacts'] if p['type'] == 'nix']
    accounted = {p['metadata']['path'] for p in packages}
    for path in paths - accounted:
        if Path(path).name[33:] not in policy['uncatalogedStoreNames']:
            raise ValueError('unaccounted Nix store path: ' + path)
    if accounted - paths:
        raise ValueError('SBOM contains store paths absent from image')
    missing = set(policy['runtime']) - {p['name'] for p in packages}
    if missing:
        raise ValueError('missing runtime packages: ' + ', '.join(sorted(missing)))
    if expected_glibc_path is not None:
        actual = [p['metadata']['path'] for p in packages if p['name'] == 'glibc']
        if actual != [expected_glibc_path]:
            raise ValueError('image glibc does not match the patched Nix output: ' + repr(actual))
    for package in packages:
        enrich(package, policy)  # Reject newly introduced, unclassified dependencies.
    return packages


def enrich(package, policy=POLICY):
    result = copy.deepcopy(package)
    name = package['name']
    if name in policy['nonRuntime']:
        return result
    if name not in policy['runtime']:
        raise ValueError('unclassified Nix package: ' + name)
    mapping = policy['runtime'][name]
    version = package['version']
    if not re.fullmatch(r'[0-9][0-9A-Za-z._+-]*', version):
        raise ValueError('unsupported Nix version: ' + version)
    # Retain the raw SBOM separately. Add upstream identity without changing
    # installed versions, paths, PURLs or claiming that a Nix patch fixes a CVE.
    cpe = f"cpe:2.3:a:{mapping['vendor']}:{mapping['product']}:{version}:*:*:*:*:*:*:*"
    result.setdefault('cpes', []).append({'cpe': cpe, 'source': 'leapview-reviewed-runtime-policy'})
    return result


def controls(sbom, policy=POLICY, *, site=False):
    result = copy.deepcopy(sbom)
    result['artifacts'] = []
    result['artifactRelationships'] = []
    result['source']['name'] = 'SYNTHETIC-MATCHING-CONTROLS-NOT-AN-IMAGE-SBOM'
    expected = {}
    if site:
        control = policy['syntheticControl']
        package_id = 'LEAPVIEW-SITE-RUNTIME-CONTROL'
        output_hash = '0' * 32
        package = {
            'id': package_id,
            'name': control['name'],
            'version': control['version'],
            'type': 'nix',
            'metadata': {'path': f"/nix/store/{output_hash}-{control['name']}-site-control"},
            'cpes': [{'cpe': f"cpe:2.3:a:{control['vendor']}:{control['product']}:{control['version']}:*:*:*:*:*:*:*",
                      'source': 'leapview-reviewed-site-runtime-control'}],
        }
        result['artifacts'].append(package)
        expected[package_id] = control['cve']
        return result, expected
    for package in sbom['artifacts']:
        mapping = policy['runtime'].get(package['name'], {})
        if not mapping.get('controlCVE'):
            continue
        package = copy.deepcopy(package)
        package['version'] = mapping['controlVersion']
        package['cpes'] = []
        package.pop('purl', None)
        result['artifacts'].append(enrich(package, policy))
        expected[package['id']] = mapping['controlCVE']
    return result, expected


def check_controls(report, expected):
    found = {(m['artifact']['id'], m['vulnerability']['id']) for m in report['matches']}
    missing = [(key, cve) for key, cve in expected.items() if (key, cve) not in found]
    if not expected or missing:
        raise ValueError('vulnerability matching control failed: ' + repr(missing))


def blocking_findings(report):
    return [m for m in report['matches'] if m['vulnerability']['severity'].upper() in ('HIGH', 'CRITICAL')]


def validate_assessments(document, packages, today):
    """Constrain standard OpenVEX to dated, exact installed Nix identities."""
    issued = datetime.fromisoformat(document['timestamp'].replace('Z', '+00:00')).date()
    expires = date.fromisoformat(POLICY['assessmentReviewUntil'])
    if not issued <= today < expires or not 0 < (expires - issued).days <= 90:
        raise ValueError('runtime assessments are expired, future-dated or exceed 90 days')
    identities = {}
    for package in packages:
        path = package['metadata']['path']
        output_hash = Path(path).name[:32]
        expected = f"pkg:nix/{package['name']}@{package['version']}?outputhash={output_hash}"
        if package.get('purl') == expected:
            identities[expected] = path
    assessments = {}
    for statement in document['statements']:
        cve = statement['vulnerability']['name']
        if not re.fullmatch(r'CVE-[0-9]{4}-[0-9]{4,}', cve):
            raise ValueError('assessment must name one CVE')
        if statement['status'] not in ('fixed', 'not_affected') or not statement.get('impact_statement'):
            raise ValueError('assessment lacks a disposition and supporting evidence')
        if len(statement['products']) != 1:
            raise ValueError('assessment must name one exact installed package identity')
        purl = statement['products'][0]['@id']
        if purl not in identities:
            raise ValueError('assessment package identity is not installed: ' + purl)
        key = (cve, purl)
        if key in assessments:
            raise ValueError('duplicate runtime assessment: ' + cve)
        assessments[key] = identities[purl]
    if not assessments:
        raise ValueError('empty runtime assessments')
    return assessments


def check_assessed_report(raw, assessed, assessments):
    """Require Grype to retain every finding and only filter exact assessments."""
    def identity(match):
        # Include the complete match so severity, package locations or evidence
        # cannot change between the raw and assessed scans of the same SBOM/DB.
        return json.dumps({k: v for k, v in match.items() if k != 'appliedIgnoreRules'}, sort_keys=True)

    ignored = assessed.get('ignoredMatches', [])
    if raw.get('ignoredMatches') or Counter(map(identity, raw['matches'])) != Counter(
            map(identity, assessed['matches'] + ignored)):
        raise ValueError('assessed findings do not partition the raw scan')
    for match in ignored:
        key = (match['vulnerability']['id'], match['artifact'].get('purl'))
        path = assessments.get(key)
        locations = {location['path'] for location in match['artifact']['locations']}
        if path is None or locations != {path}:
            raise ValueError('scanner filtered an unassessed finding: ' + repr(key))


def site_payload_inventory(archive, artifact, policy=SITE_POLICY):
    """Hash the site image's complete, deliberately small root filesystem."""
    candidate = candidate_manifest

    if artifact.get('kind') != 'site-image':
        raise ValueError('site payload inventory requires a site image')
    expected_files = set(policy['requiredFiles'])
    map_prefix = policy['mapAssetsPrefix'].rstrip('/') + '/'
    file_entries, directory_entries = {}, {}
    layer_names = go_evidence.layer_names(archive, artifact)
    indexes = {name: index for index, name in enumerate(layer_names)}
    seen_layers, entry_count, total_bytes = set(), 0, 0

    with tarfile.open(archive, 'r|*') as outer:
        for member in outer:
            name = member.name.removeprefix('./')
            if name not in indexes:
                continue
            if name in seen_layers or not member.isfile() or not 0 <= member.size <= go_evidence.MAX_LAYER_BYTES:
                raise ValueError('duplicate, redirected or oversized site image layer')
            seen_layers.add(name)
            index = indexes[name]
            source = go_evidence.HashedLayer(outer.extractfile(member))
            with tarfile.open(fileobj=source, mode='r|', tarinfo=go_evidence.LayerTarInfo) as layer:
                for entry in layer:
                    entry_count += 1
                    total_bytes += entry.size
                    if entry.size < 0 or entry_count > go_evidence.MAX_ENTRIES or total_bytes > go_evidence.MAX_LAYER_BYTES:
                        raise ValueError('site image content exceeds inventory limits')
                    path = go_evidence.member_path(entry)
                    if path is None:
                        continue
                    if PurePosixPath(path).name.startswith('.wh.'):
                        raise ValueError('site image may not contain overlay whiteouts')
                    if entry.isdir():
                        if path not in {'.data', '.data/map-assets', 'etc', 'etc/ssl', 'etc/ssl/certs'} and not path.startswith(map_prefix):
                            raise ValueError('site image contains an unexpected directory: ' + path)
                        if entry.mode & 0o7777 != int(policy['directoryMode'], 8):
                            raise ValueError('site image directories must not be writable or privileged')
                        value = {'path': path, 'type': 'directory', 'mode': entry.mode & 0o7777, 'size': 0}
                        previous = directory_entries.get(path)
                        if previous is not None and previous != value:
                            raise ValueError('site image directory metadata changes across layers: ' + path)
                        directory_entries[path] = value
                        continue
                    if not entry.isfile():
                        raise ValueError('site image payload must contain only regular files and directories')
                    if path not in expected_files and not path.startswith(map_prefix):
                        raise ValueError('site image contains an unexpected file: ' + path)
                    required_mode = int(policy['executableMode'], 8) if path == 'leapview-site' else int(policy['dataMode'], 8)
                    if entry.mode & 0o7777 != required_mode or entry.size <= 0:
                        raise ValueError('site image file permissions or size are invalid: ' + path)
                    if path in file_entries:
                        raise ValueError('duplicate site image file across layers: ' + path)
                    content = layer.extractfile(entry)
                    digest, size = hashlib.sha256(), 0
                    while chunk := content.read(1024 * 1024):
                        size += len(chunk)
                        if size > go_evidence.MAX_BINARY_BYTES and path == 'leapview-site':
                            raise ValueError('site Go binary exceeds its inventory size limit')
                        digest.update(chunk)
                    if size != entry.size:
                        raise ValueError('truncated site image file: ' + path)
                    file_entries[path] = {'path': path, 'type': 'file', 'mode': entry.mode & 0o7777,
                                          'size': size, 'sha256': 'sha256:' + digest.hexdigest()}
            while source.read(1024 * 1024):
                pass
            if 'sha256:' + source.digest.hexdigest() != artifact['layerDiffIDs'][index]:
                raise ValueError('site image layer differs from candidate content')

    if seen_layers != set(layer_names):
        raise ValueError('site image layer inventory is incomplete')
    if expected_files - set(file_entries):
        raise ValueError('site image is missing a required binary or certificate bundle')
    if not any(path.startswith(map_prefix) for path in file_entries):
        raise ValueError('site image is missing its map assets')
    for path in file_entries:
        parent = PurePosixPath(path).parent
        while str(parent) != '.':
            if str(parent) in file_entries:
                raise ValueError('site image file is an ancestor of another payload: ' + str(parent))
            parent = parent.parent
    inventory = {
        'schemaVersion': 1,
        'profile': 'site-image',
        'archiveSHA256': candidate.digest_file(archive),
        'platform': artifact['platform'],
        'configDigest': artifact['configDigest'],
        'layerDiffIDs': artifact['layerDiffIDs'],
        'files': [file_entries[path] for path in sorted(file_entries)],
        'directories': [directory_entries[path] for path in sorted(directory_entries)],
    }
    return inventory


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('archive', type=Path, help='Docker archive from a Nix image output')
    parser.add_argument('--kind', choices=('application-image', 'site-image'), required=True)
    parser.add_argument('--source-revision', required=True, help='exact 40-character candidate commit')
    parser.add_argument('--evidence-dir', type=Path, required=True)
    parser.add_argument('--coverage-only', action='store_true', help='qualify detection, without granting vulnerability clearance')
    args = parser.parse_args()
    os.umask(0o077)
    evidence = args.evidence_dir.resolve()
    evidence.mkdir(parents=True, exist_ok=False)  # Never reuse stale evidence.
    policy = SITE_POLICY if args.kind == 'site-image' else POLICY
    policy_path = SITE_POLICY_PATH if args.kind == 'site-image' else ROOT / 'nix/runtime-security-policy.json'
    summary = {'schemaVersion': 1, 'coverageQualified': False, 'releaseReady': False,
               'kind': args.kind, 'profile': args.kind, 'sourceRevision': args.source_revision,
               'enforcementMode': 'coverage-only' if args.coverage_only else 'enforce',
               'scanners': {}, 'policySHA256': hashlib.sha256(
                   policy_path.read_bytes()).hexdigest()}
    try:
        if not candidate_manifest.REVISION.fullmatch(args.source_revision):
            raise ValueError('source revision must be an exact 40-character commit')
        for tool in ('syft', 'grype'):
            info = json.loads(run(tool, 'version', '-o', 'json'))
            version = info['version']
            summary['scanners'][tool] = version
            if version != policy[tool + 'Version']:
                raise ValueError('unexpected ' + tool + ' version')
        archive = args.archive.resolve(strict=True)
        artifact = candidate_manifest.image_identity(archive, {'revision': args.source_revision}, args.kind)
        artifact.update(kind=args.kind, sha256=candidate_manifest.digest_file(archive))
        summary['archiveSHA256'] = artifact['sha256'].removeprefix('sha256:')
        summary['platform'] = artifact['platform']
        summary['configDigest'] = artifact['configDigest']
        summary['layerDiffIDs'] = artifact['layerDiffIDs']
        # Explicit config and relevant environment values prevent ambient ignores
        # or a disabled matcher from silently changing this qualification.
        env = {k: v for k, v in os.environ.items() if not k.startswith(('SYFT_', 'GRYPE_'))}
        env.update(GRYPE_CHECK_FOR_APP_UPDATE='false', SYFT_CHECK_FOR_APP_UPDATE='false')
        config = evidence / 'grype-config.json'
        write(config, {'match': {'stock': {'using-cpes': True}}, 'ignore': [],
                       'only-fixed': False, 'db': {'validate-age': True,
                       'max-allowed-built-age': str(policy['databaseMaxAgeHours']) + 'h',
                       'validate-by-hash-on-start': True}})
        syft_config = evidence / 'syft-config.json'
        write(syft_config, {})
        raw_path = evidence / 'sbom.syft.json'
        # Syft's Docker archive reader expects an uncompressed tar. Nix emits
        # gzip; stage only for the scan and always remove this large temporary.
        with tempfile.TemporaryDirectory(prefix='leapview-runtime-scan-') as staging:
            scan_archive = archive
            with archive.open('rb') as stream:
                compressed = stream.read(2) == b'\x1f\x8b'
            if compressed:
                scan_archive = Path(staging) / 'image.tar'
                with gzip.open(archive, 'rb') as source, scan_archive.open('wb') as target:
                    shutil.copyfileobj(source, target)
            run('syft', '--config', str(syft_config), 'docker-archive:' + str(scan_archive), '-o', 'syft-json=' + str(raw_path), env=env)
        sbom = json.loads(raw_path.read_text())
        # Keep native Syft evidence intact while exporting the release contract's
        # SPDX representation from that same inventory, without a second scan.
        spdx_path = evidence / 'sbom.spdx.json'
        run('syft', '--config', str(syft_config), 'convert', str(raw_path),
            '-o', 'spdx-json=' + str(spdx_path), env=env)
        if json.loads(spdx_path.read_text()).get('spdxVersion') != 'SPDX-2.3':
            raise ValueError('runtime SPDX export has an unsupported version')
        platform = artifact['platform']
        summary['image'] = sbom['source']
        vex_path = None
        if args.kind == 'application-image':
            glibc_path = native_glibc_path(platform, env)
            packages = check_inventory(sbom, store_paths(archive), glibc_path)
            summary['expectedGlibcPath'] = glibc_path
            summary['inventory'] = [{'name': p['name'], 'version': p['version'], 'path': p['metadata']['path']} for p in packages]
            vex_bytes = assessment_file(platform).read_bytes()
            vex = candidate_manifest.read_json(vex_bytes)
            assessments = validate_assessments(vex, packages, datetime.now(timezone.utc).date())
            vex_path = evidence / 'assessments.vex.json'
            vex_path.write_bytes(vex_bytes)
            summary['assessmentsSHA256'] = hashlib.sha256(vex_bytes).hexdigest()
            summary['assessmentReviewUntil'] = POLICY['assessmentReviewUntil']
            runtime = copy.deepcopy(sbom)
            runtime['artifacts'] = [enrich(p) for p in packages if p['name'] in POLICY['runtime']]
            runtime['artifactRelationships'] = []
            control_sbom, expected = controls(runtime)
        else:
            go_evidence.check_entrypoint_config(archive, artifact, go_evidence.SITE_ENTRYPOINTS)
            if store_paths(archive, allow_empty=True):
                raise ValueError('site image unexpectedly contains Nix store paths')
            inventory = site_payload_inventory(archive, artifact)
            write(evidence / 'site-image-inventory.json', inventory)
            artifacts = sbom.get('artifacts')
            if not isinstance(artifacts, list) or any(not isinstance(item, dict) or not isinstance(item.get('type'), str)
                                                     for item in artifacts):
                raise ValueError('site Syft inventory is malformed')
            if any(item['type'] == 'nix' for item in artifacts):
                raise ValueError('site image contains an unexpected Nix package')
            runtime = copy.deepcopy(sbom)
            separate_types = set(policy['separateScannerTypes'])
            runtime['artifacts'] = [item for item in artifacts if item['type'] not in separate_types]
            runtime['artifactRelationships'] = []
            control_sbom, expected = controls(runtime, policy, site=True)
            summary['inventory'] = {'files': len(inventory['files']), 'directories': len(inventory['directories']),
                                    'binarySHA256': next(item['sha256'] for item in inventory['files']
                                                         if item['path'] == 'leapview-site')}
        runtime_path = evidence / 'runtime.syft.json'
        write(runtime_path, runtime)
        control_path = evidence / 'controls.synthetic.syft.json'
        write(control_path, control_sbom)
        run('grype', '--config', str(config), 'db', 'update', env=env)
        env['GRYPE_DB_AUTO_UPDATE'] = 'false'  # Same database for both scans.
        for name, path in [('runtime', runtime_path), ('controls', control_path)]:
            vex_args = ['--vex', str(vex_path)] if name == 'controls' and vex_path is not None else []
            run('grype', '--config', str(config), 'sbom:' + str(path), *vex_args,
                '-o', 'json', '--file', str(evidence / (name + '.grype.json')), env=env)
        control_report = json.loads((evidence / 'controls.grype.json').read_text())
        report = json.loads((evidence / 'runtime.grype.json').read_text())
        if report['descriptor']['db'] != control_report['descriptor']['db']:
            raise ValueError('control and candidate databases differ')
        if args.kind == 'application-image':
            assessed_path = evidence / 'runtime.assessed.grype.json'
            run('grype', '--config', str(config), 'sbom:' + str(runtime_path),
                '--vex', str(vex_path), '-o', 'json', '--file', str(assessed_path), env=env)
            assessed = json.loads(assessed_path.read_text())
            if report['descriptor']['db'] != assessed['descriptor']['db']:
                raise ValueError('raw and assessed databases differ')
            if control_report.get('ignoredMatches'):
                raise ValueError('VEX filtered a synthetic matching control')
            check_assessed_report(report, assessed, assessments)
            findings = blocking_findings(assessed)
            assessed_count = len(assessed.get('ignoredMatches', []))
        else:
            if report.get('ignoredMatches') or control_report.get('ignoredMatches'):
                raise ValueError('site runtime scans may not filter vulnerability matches')
            findings = blocking_findings(report)
            assessed_count = 0
        check_controls(control_report, expected)
        summary['coverageQualified'] = True
        summary['database'] = report['descriptor']['db']
        summary['controlsPassed'] = len(expected)
        summary['assessedFindings'] = assessed_count
        summary['rawBlockingFindings'] = len(blocking_findings(report))
        summary['blockingFindings'] = [{'package': m['artifact']['name'], 'version': m['artifact']['version'],
                                       'id': m['vulnerability']['id'], 'severity': m['vulnerability']['severity']}
                                      for m in findings]
        summary['runtimeVulnerabilityGatePassed'] = not summary['blockingFindings']
        # This tool alone never establishes provenance, Go/embedded library
        # coverage, multiarch qualification or any other production release gate.
        if summary['blockingFindings'] and not args.coverage_only:
            raise ValueError('unresolved HIGH/CRITICAL runtime findings; see evidence')
    except (ValueError, KeyError, OSError, subprocess.CalledProcessError) as error:
        summary['error'] = str(error)
        raise SystemExit(str(error)) from error
    finally:
        # Partial scans retain their actual files; only complete, enforced scans
        # can be joined into candidate evidence by the shared collector.
        summary['reportSHA256'] = {}
        for path in sorted(evidence.glob('*.json')):
            if path.name != 'summary.json':
                with path.open('rb') as stream:
                    summary['reportSHA256'][path.name] = hashlib.file_digest(stream, 'sha256').hexdigest()
        write(evidence / 'summary.json', summary)
    print(json.dumps({k: v for k, v in summary.items() if k not in ('image', 'inventory', 'blockingFindings', 'database')}, indent=2))


if __name__ == '__main__':
    main()
