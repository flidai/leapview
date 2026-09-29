#!/usr/bin/env python3
"""Qualify Nix runtime inventory and NVD matching; never replace OCI admission."""
import argparse
import copy
from collections import Counter
from datetime import date, datetime, timezone
import hashlib
import gzip
import shutil
import tempfile
import json
import os
from pathlib import Path
import re
import subprocess
import tarfile

ROOT = Path(__file__).resolve().parents[1]
POLICY = json.loads((ROOT / 'nix/runtime-security-policy.json').read_text())


def write(path, value):
    path.write_text(json.dumps(value, indent=2) + '\n')


def run(*args, env=None):
    return subprocess.check_output(args, text=True, env=env)


def store_paths(archive):
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
    if not paths:
        raise ValueError('archive has no Nix store inventory')
    return paths


def check_inventory(sbom, paths, expected_glibc_path=None):
    packages = [p for p in sbom['artifacts'] if p['type'] == 'nix']
    accounted = {p['metadata']['path'] for p in packages}
    for path in paths - accounted:
        if Path(path).name[33:] not in POLICY['uncatalogedStoreNames']:
            raise ValueError('unaccounted Nix store path: ' + path)
    if accounted - paths:
        raise ValueError('SBOM contains store paths absent from image')
    missing = set(POLICY['runtime']) - {p['name'] for p in packages}
    if missing:
        raise ValueError('missing runtime packages: ' + ', '.join(sorted(missing)))
    if expected_glibc_path is not None:
        actual = [p['metadata']['path'] for p in packages if p['name'] == 'glibc']
        if actual != [expected_glibc_path]:
            raise ValueError('image glibc does not match the patched Nix output: ' + repr(actual))
    for package in packages:
        enrich(package)  # Reject newly introduced, unclassified dependencies.
    return packages


def enrich(package):
    result = copy.deepcopy(package)
    name = package['name']
    if name in POLICY['nonRuntime']:
        return result
    if name not in POLICY['runtime']:
        raise ValueError('unclassified Nix package: ' + name)
    mapping = POLICY['runtime'][name]
    version = package['version']
    if not re.fullmatch(r'[0-9][0-9A-Za-z._+-]*', version):
        raise ValueError('unsupported Nix version: ' + version)
    # Retain the raw SBOM separately. Add upstream identity without changing
    # installed versions, paths, PURLs or claiming that a Nix patch fixes a CVE.
    cpe = f"cpe:2.3:a:{mapping['vendor']}:{mapping['product']}:{version}:*:*:*:*:*:*:*"
    result.setdefault('cpes', []).append({'cpe': cpe, 'source': 'leapview-reviewed-runtime-policy'})
    return result


def controls(sbom):
    result = copy.deepcopy(sbom)
    result['artifacts'] = []
    result['artifactRelationships'] = []
    result['source']['name'] = 'SYNTHETIC-MATCHING-CONTROLS-NOT-AN-IMAGE-SBOM'
    expected = {}
    for package in sbom['artifacts']:
        mapping = POLICY['runtime'].get(package['name'], {})
        if not mapping.get('controlCVE'):
            continue
        package = copy.deepcopy(package)
        package['version'] = mapping['controlVersion']
        package['cpes'] = []
        package.pop('purl', None)
        result['artifacts'].append(enrich(package))
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


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('archive', type=Path, help='Docker archive from .#leapview-image')
    parser.add_argument('--evidence-dir', type=Path, required=True)
    parser.add_argument('--coverage-only', action='store_true', help='qualify detection, without granting vulnerability clearance')
    args = parser.parse_args()
    os.umask(0o077)
    evidence = args.evidence_dir.resolve()
    evidence.mkdir(parents=True, exist_ok=False)  # Never reuse stale evidence.
    summary = {'schemaVersion': 1, 'coverageQualified': False, 'releaseReady': False,
               'scanners': {}, 'policySHA256': hashlib.sha256(
                   (ROOT / 'nix/runtime-security-policy.json').read_bytes()).hexdigest()}
    try:
        for tool in ('syft', 'grype'):
            info = json.loads(run(tool, 'version', '-o', 'json'))
            summary['scanners'][tool] = info['version']
            if info['version'] != POLICY[tool + 'Version']:
                raise ValueError('unexpected ' + tool + ' version')
        archive = args.archive.resolve(strict=True)
        with archive.open('rb') as stream:
            summary['archiveSHA256'] = hashlib.file_digest(stream, 'sha256').hexdigest()
        # Explicit config and relevant environment values prevent ambient ignores
        # or a disabled matcher from silently changing this qualification.
        env = {k: v for k, v in os.environ.items() if not k.startswith(('SYFT_', 'GRYPE_'))}
        env.update(GRYPE_CHECK_FOR_APP_UPDATE='false', SYFT_CHECK_FOR_APP_UPDATE='false')
        config = evidence / 'grype-config.json'
        write(config, {'match': {'stock': {'using-cpes': True}}, 'ignore': [],
                       'only-fixed': False, 'db': {'validate-age': True,
                       'max-allowed-built-age': '120h', 'validate-by-hash-on-start': True}})
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
        expected_glibc_path = run(
            'nix', 'eval', '--no-update-lock-file', '--raw',
            '.#packages.x86_64-linux.glibc-runtime.outPath', env=env).strip()
        packages = check_inventory(sbom, store_paths(archive), expected_glibc_path)
        summary['expectedGlibcPath'] = expected_glibc_path
        summary['image'] = sbom['source']
        summary['inventory'] = [{'name': p['name'], 'version': p['version'], 'path': p['metadata']['path']} for p in packages]
        vex_bytes = (ROOT / 'nix/runtime-assessments.vex.json').read_bytes()
        vex = json.loads(vex_bytes)
        assessments = validate_assessments(vex, packages, datetime.now(timezone.utc).date())
        vex_path = evidence / 'assessments.vex.json'
        vex_path.write_bytes(vex_bytes)
        summary['assessmentsSHA256'] = hashlib.sha256(vex_bytes).hexdigest()
        summary['assessmentReviewUntil'] = POLICY['assessmentReviewUntil']
        runtime = copy.deepcopy(sbom)
        runtime['artifacts'] = [enrich(p) for p in packages if p['name'] in POLICY['runtime']]
        runtime['artifactRelationships'] = []
        runtime_path = evidence / 'runtime.syft.json'
        write(runtime_path, runtime)
        control_sbom, expected = controls(runtime)
        control_path = evidence / 'controls.synthetic.syft.json'
        write(control_path, control_sbom)
        run('grype', '--config', str(config), 'db', 'update', env=env)
        env['GRYPE_DB_AUTO_UPDATE'] = 'false'  # Same database for both scans.
        for name, path in [('runtime', runtime_path), ('controls', control_path)]:
            vex_args = ['--vex', str(vex_path)] if name == 'controls' else []
            run('grype', '--config', str(config), 'sbom:' + str(path), *vex_args,
                '-o', 'json', '--file', str(evidence / (name + '.grype.json')), env=env)
        control_report = json.loads((evidence / 'controls.grype.json').read_text())
        report = json.loads((evidence / 'runtime.grype.json').read_text())
        assessed_path = evidence / 'runtime.assessed.grype.json'
        run('grype', '--config', str(config), 'sbom:' + str(runtime_path),
            '--vex', str(vex_path), '-o', 'json', '--file', str(assessed_path), env=env)
        assessed = json.loads(assessed_path.read_text())
        if report['descriptor']['db'] != control_report['descriptor']['db']:
            raise ValueError('control and candidate databases differ')
        if report['descriptor']['db'] != assessed['descriptor']['db']:
            raise ValueError('raw and assessed databases differ')
        if control_report.get('ignoredMatches'):
            raise ValueError('VEX filtered a synthetic matching control')
        check_assessed_report(report, assessed, assessments)
        check_controls(control_report, expected)
        summary['coverageQualified'] = True
        summary['database'] = report['descriptor']['db']
        summary['controlsPassed'] = len(expected)
        summary['assessedFindings'] = len(assessed.get('ignoredMatches', []))
        summary['rawBlockingFindings'] = len(blocking_findings(report))
        summary['blockingFindings'] = [{'package': m['artifact']['name'], 'version': m['artifact']['version'],
                                       'id': m['vulnerability']['id'], 'severity': m['vulnerability']['severity']}
                                      for m in blocking_findings(assessed)]
        summary['runtimeVulnerabilityGatePassed'] = not summary['blockingFindings']
        # This tool alone never establishes provenance, Go/embedded library
        # coverage, multiarch qualification or any other production release gate.
        if summary['blockingFindings'] and not args.coverage_only:
            raise ValueError('unresolved HIGH/CRITICAL runtime findings; see evidence')
    except (ValueError, KeyError, OSError, subprocess.CalledProcessError) as error:
        summary['error'] = str(error)
        raise SystemExit(str(error)) from error
    finally:
        write(evidence / 'summary.json', summary)
    print(json.dumps({k: v for k, v in summary.items() if k not in ('image', 'inventory', 'blockingFindings', 'database')}, indent=2))


if __name__ == '__main__':
    main()
