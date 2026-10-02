import copy
import datetime
import importlib.util
import io
import json
import hashlib
import subprocess
import tarfile
import tempfile
import pathlib
import unittest
from unittest.mock import patch

ROOT = pathlib.Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('runtime_security', ROOT / 'scripts/check_nix_runtime_security.py')
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


class CoverageTests(unittest.TestCase):
    def test_spdx_export_and_partial_scan_evidence_fail_closed(self):
        for coverage_only, export in [(False, 'SPDX-2.3'), (True, 'SPDX-2.2'), (False, 'command-failure')]:
            with self.subTest(coverage_only=coverage_only, export=export), tempfile.TemporaryDirectory() as directory:
                root = pathlib.Path(directory)
                archive = root / 'image.tar'
                with tarfile.open(archive, 'w'):
                    pass
                evidence = root / 'evidence'

                def run(*args, **kwargs):
                    if args[1] == 'version':
                        return json.dumps({'version': m.POLICY[args[0] + 'Version']})
                    if args[0] == 'nix':
                        return '/nix/store/' + 'a' * 32 + '-glibc-2.42-84'
                    output = pathlib.Path(args[-1].split('=', 1)[1])
                    if 'convert' in args:
                        original = pathlib.Path(args[args.index('convert') + 1])
                        self.assertEqual(original, evidence / 'sbom.syft.json')
                        self.assertEqual(json.loads(original.read_bytes()), {'artifacts': []})
                        if export == 'command-failure':
                            raise subprocess.CalledProcessError(1, args)
                        output.write_text(json.dumps({'spdxVersion': export}))
                    else:
                        output.write_text('{"artifacts": []}')
                    return ''

                argv = ['scan', str(archive), '--evidence-dir', str(evidence)]
                if coverage_only:
                    argv.append('--coverage-only')
                with patch('sys.argv', argv), patch.object(m, 'run', side_effect=run):
                    with self.assertRaises(SystemExit):
                        m.main()
                summary = json.loads((evidence / 'summary.json').read_bytes())
                self.assertEqual(summary['enforcementMode'], 'coverage-only' if coverage_only else 'enforce')
                self.assertFalse(summary['coverageQualified'])
                self.assertFalse(summary['releaseReady'])
                self.assertIn('error', summary)
                self.assertEqual((evidence / 'sbom.syft.json').read_bytes(), b'{"artifacts": []}')
                names = {'sbom.syft.json', 'grype-config.json', 'syft-config.json'}
                if export != 'command-failure':
                    names.add('sbom.spdx.json')
                self.assertEqual(set(summary['reportSHA256']), names)
                for name, digest in summary['reportSHA256'].items():
                    self.assertEqual(digest, hashlib.sha256((evidence / name).read_bytes()).hexdigest())

    def package(self, name='glibc', version='2.42-84'):
        path = '/nix/store/' + 'a' * 32 + '-' + name + '-' + version
        return {'id': name, 'name': name, 'version': version, 'type': 'nix',
                'metadata': {'path': path}, 'cpes': [], 'purl': 'pkg:nix/' + name}

    def test_enrichment_preserves_evidence_and_adds_upstream_identity(self):
        p = self.package('xgcc', '15.3.0')
        original = copy.deepcopy(p)
        result = m.enrich(p)
        self.assertEqual(p, original)
        self.assertEqual(result['metadata'], p['metadata'])
        self.assertEqual(result['version'], p['version'])
        self.assertIn('cpe:2.3:a:gnu:gcc:15.3.0:', result['cpes'][-1]['cpe'])

    def test_unknown_package_fails_closed(self):
        with self.assertRaisesRegex(ValueError, 'unclassified'):
            m.enrich(self.package('openssl'))

    def test_inventory_cannot_silently_drop_a_store_path(self):
        p = self.package()
        with self.assertRaisesRegex(ValueError, 'unaccounted'):
            m.check_inventory({'artifacts': [p]}, {p['metadata']['path'], '/nix/store/' + 'b' * 32 + '-hidden-library'})

    def test_old_glibc_path_fails_even_when_inventory_is_complete(self):
        packages = [self.package(name) for name in m.POLICY['runtime']]
        paths = {p['metadata']['path'] for p in packages}
        with self.assertRaisesRegex(ValueError, 'does not match the patched Nix output'):
            m.check_inventory({'artifacts': packages}, paths,
                              '/nix/store/' + 'b' * 32 + '-glibc-2.42-84')

    def test_missing_required_library_fails(self):
        with self.assertRaisesRegex(ValueError, 'missing runtime'):
            m.check_inventory({'artifacts': []}, set())

    def test_control_requires_exact_vulnerability_and_package(self):
        report = {'matches': [{'artifact': {'id': 'other'}, 'vulnerability': {'id': 'CVE-2023-4911'}}]}
        with self.assertRaisesRegex(ValueError, 'control'):
            m.check_controls(report, {'glibc': 'CVE-2023-4911'})

    def test_high_without_fix_still_blocks(self):
        report = {'matches': [{'artifact': {'name': 'glibc', 'version': '2.42'},
                              'vulnerability': {'id': 'CVE-test', 'severity': 'High', 'fix': {'versions': []}}}]}
        self.assertEqual(len(m.blocking_findings(report)), 1)

    def test_archive_inventory_reads_gzip_layers_without_extracting(self):
        with tempfile.TemporaryDirectory() as directory:
            layer = io.BytesIO()
            path = 'nix/store/' + 'a' * 32 + '-glibc-2.42/lib/libc.so.6'
            with tarfile.open(fileobj=layer, mode='w') as output:
                entry = tarfile.TarInfo(path)
                entry.size = 4
                output.addfile(entry, io.BytesIO(b'ELF!'))
            archive = pathlib.Path(directory) / 'image.tar.gz'
            with tarfile.open(archive, 'w:gz') as output:
                entry = tarfile.TarInfo('layer/layer.tar')
                entry.size = len(layer.getvalue())
                output.addfile(entry, io.BytesIO(layer.getvalue()))
            self.assertEqual(m.store_paths(archive), {'/' + '/'.join(path.split('/')[:3])})
            self.assertFalse((pathlib.Path(directory) / 'nix').exists())

    def test_complete_inventory_and_explicit_map_payload(self):
        packages = [self.package(name) for name in m.POLICY['runtime']]
        paths = {p['metadata']['path'] for p in packages}
        paths.add('/nix/store/' + 'b' * 32 + '-leapview-map-assets')
        self.assertEqual(m.check_inventory({'artifacts': packages}, paths), packages)

    def test_sbom_from_different_image_fails(self):
        packages = [self.package(name) for name in m.POLICY['runtime']]
        with self.assertRaisesRegex(ValueError, 'absent from image'):
            m.check_inventory({'artifacts': packages}, set())

    def test_positive_control(self):
        m.check_controls({'matches': [{'artifact': {'id': 'glibc'}, 'vulnerability': {'id': 'CVE-test'}}]}, {'glibc': 'CVE-test'})


class AssessmentTests(unittest.TestCase):
    def setUp(self):
        self.document = m.json.loads((ROOT / 'nix/runtime-assessments.vex.json').read_text())
        self.path = '/nix/store/kj7ia0isvb6xh74qavgcshmb7fcskj4l-glibc-2.42-84'
        self.purl = 'pkg:nix/glibc@2.42-84?outputhash=kj7ia0isvb6xh74qavgcshmb7fcskj4l'
        self.packages = [{'name': 'glibc', 'version': '2.42-84', 'purl': self.purl,
                          'metadata': {'path': self.path}}]
        self.today = datetime.date(2026, 9, 29)

    def validate(self):
        return m.validate_assessments(self.document, self.packages, self.today)

    def match(self, cve='CVE-2026-19499'):
        return {'artifact': {'id': 'glibc', 'purl': self.purl,
                             'locations': [{'path': self.path}]},
                'vulnerability': {'id': cve, 'namespace': 'nvd:cpe', 'severity': 'High'}}

    def test_document_names_only_exact_installed_package(self):
        self.assertEqual(len(self.validate()), 11)
        self.packages[0]['metadata']['path'] = self.path.replace('kj7ia0', 'aaaaaa')
        with self.assertRaisesRegex(ValueError, 'identity'):
            self.validate()

    def test_expired_assessment_fails(self):
        with self.assertRaisesRegex(ValueError, 'expired'):
            m.validate_assessments(self.document, self.packages, datetime.date(2027, 1, 1))

    def test_future_or_unbounded_review_is_rejected(self):
        with self.assertRaisesRegex(ValueError, 'future-dated'):
            m.validate_assessments(self.document, self.packages, datetime.date(2026, 9, 28))
        with patch.dict(m.POLICY, assessmentReviewUntil='2027-12-28'), self.assertRaisesRegex(ValueError, '90 days'):
            self.validate()

    def test_duplicate_or_broad_assessment_fails(self):
        self.document['statements'].append(copy.deepcopy(self.document['statements'][0]))
        with self.assertRaisesRegex(ValueError, 'duplicate'):
            self.validate()
        self.document['statements'].pop()
        self.document['statements'][0]['products'][0]['@id'] = 'pkg:nix/glibc@2.42-84'
        with self.assertRaisesRegex(ValueError, 'identity'):
            self.validate()

    def test_new_cve_still_blocks_while_fixed_finding_is_retained(self):
        fixed, new = self.match(), self.match('CVE-2099-12345')
        filtered = {'matches': [new], 'ignoredMatches': [fixed]}
        m.check_assessed_report({'matches': [fixed, new]}, filtered, self.validate())
        self.assertEqual(m.blocking_findings(filtered), [new])

    def test_scanner_cannot_silently_drop_or_change_a_finding(self):
        original = self.match()
        with self.assertRaisesRegex(ValueError, 'partition'):
            m.check_assessed_report({'matches': [original]}, {'matches': [], 'ignoredMatches': []}, self.validate())
        changed = copy.deepcopy(original)
        changed['vulnerability']['severity'] = 'Low'
        with self.assertRaisesRegex(ValueError, 'partition'):
            m.check_assessed_report({'matches': [original]}, {'matches': [changed]}, self.validate())

    def test_unreviewed_ignore_or_changed_package_is_rejected(self):
        for match in [self.match('CVE-2099-12345'), self.match()]:
            if match['vulnerability']['id'] == 'CVE-2026-19499':
                match['artifact']['purl'] = self.purl.replace('kj7ia0', 'aaaaaa')
            with self.assertRaisesRegex(ValueError, 'unassessed'):
                m.check_assessed_report({'matches': [match]}, {'matches': [], 'ignoredMatches': [match]}, self.validate())

    def test_purl_alone_cannot_override_different_store_location(self):
        match = self.match()
        match['artifact']['locations'][0]['path'] = self.path.replace('kj7ia0', 'aaaaaa')
        with self.assertRaisesRegex(ValueError, 'unassessed'):
            m.check_assessed_report({'matches': [match]}, {'matches': [], 'ignoredMatches': [match]}, self.validate())


if __name__ == '__main__':
    unittest.main()
