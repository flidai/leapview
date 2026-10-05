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


class NativePlatformTests(unittest.TestCase):
    def image_archive(self, directory, architecture):
        revision = 'a' * 40
        config = json.dumps({'os': 'linux', 'architecture': architecture,
                             'rootfs': {'type': 'layers', 'diff_ids': []},
                             'config': {'Labels': {
                                 'org.opencontainers.image.source': 'https://github.com/flidai/leapview',
                                 'org.opencontainers.image.revision': revision,
                                 'org.opencontainers.image.version': '0.3.0-alpha.1',
                                 'dev.leapview.build.kind': 'application-image',
                                 'dev.leapview.build.dirty': 'false'}}}).encode()
        config_name = hashlib.sha256(config).hexdigest() + '.json'
        archive = pathlib.Path(directory) / 'image.tar'
        with tarfile.open(archive, 'w') as output:
            for name, data in [('manifest.json', json.dumps([{'Config': config_name, 'Layers': []}]).encode()),
                               (config_name, config)]:
                member = tarfile.TarInfo(name)
                member.size = len(data)
                output.addfile(member, io.BytesIO(data))
        return archive

    def test_candidate_image_platform_selects_the_native_nix_glibc_output(self):
        for architecture, platform, system in [('amd64', 'linux/amd64', 'x86_64-linux'),
                                                ('arm64', 'linux/arm64', 'aarch64-linux')]:
            with self.subTest(platform=platform), patch.object(m, 'run', return_value='/nix/store/glibc') as run:
                with tempfile.TemporaryDirectory() as directory:
                    archive = self.image_archive(directory, architecture)
                    identity = m.candidate_manifest.image_identity(
                        archive, {'revision': 'a' * 40}, 'application-image')
                self.assertEqual(identity['platform'], platform)
                self.assertEqual(m.native_glibc_path(identity['platform']), '/nix/store/glibc')
                self.assertEqual(run.call_args.args[-1],
                                 '.#packages.' + system + '.glibc-runtime.outPath')
        self.assertEqual(m.assessment_file('linux/amd64').name, 'runtime-assessments.vex.json')
        self.assertEqual(m.assessment_file('linux/arm64').name, 'runtime-assessments.arm64.vex.json')
        with self.assertRaisesRegex(ValueError, 'unsupported runtime assessment platform'):
            m.assessment_file('linux/riscv64')
        protected_root = pathlib.Path('/protected/checkout')
        with patch.object(m, 'ROOT', protected_root):
            self.assertEqual(m.assessment_file('linux/arm64'),
                             protected_root / 'nix/runtime-assessments.arm64.vex.json')

    def test_candidate_image_identity_rejects_unsupported_isa(self):
        with tempfile.TemporaryDirectory() as directory:
            archive = self.image_archive(directory, 'riscv64')
            with self.assertRaisesRegex(ValueError, 'unsupported Nix candidate platform'):
                m.candidate_manifest.image_identity(
                    archive, {'revision': 'a' * 40}, 'application-image')
        for platform in ['linux/riscv64', 'darwin/arm64']:
            with self.subTest(platform=platform), self.assertRaisesRegex(ValueError, 'unsupported native runtime platform'):
                m.native_glibc_path(platform)


class CoverageTests(unittest.TestCase):
    def image_archive(self, path, revision):
        config = {'architecture': 'amd64', 'os': 'linux', 'rootfs': {'type': 'layers', 'diff_ids': []},
                  'config': {'Labels': {
                      'org.opencontainers.image.source': 'https://github.com/flidai/leapview',
                      'org.opencontainers.image.revision': revision,
                      'org.opencontainers.image.version': '0.3.0-alpha.1',
                      'dev.leapview.build.kind': 'application-image',
                      'dev.leapview.build.dirty': 'false'}}}
        config_bytes = json.dumps(config).encode()
        config_name = hashlib.sha256(config_bytes).hexdigest() + '.json'
        with tarfile.open(path, 'w') as output:
            for name, data in [('manifest.json', json.dumps([{'Config': config_name, 'Layers': []}]).encode()),
                               (config_name, config_bytes)]:
                member = tarfile.TarInfo(name)
                member.size = len(data)
                output.addfile(member, io.BytesIO(data))

    def test_spdx_export_and_partial_scan_evidence_fail_closed(self):
        for coverage_only, export in [(False, 'SPDX-2.3'), (True, 'SPDX-2.2'), (False, 'command-failure')]:
            with self.subTest(coverage_only=coverage_only, export=export), tempfile.TemporaryDirectory() as directory:
                root = pathlib.Path(directory)
                archive = root / 'image.tar'
                revision = 'a' * 40
                self.image_archive(archive, revision)
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

                argv = ['scan', str(archive), '--kind', 'application-image', '--source-revision', revision,
                        '--evidence-dir', str(evidence)]
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


class SiteInventoryTests(unittest.TestCase):
    def image_archive(self, directory, extra=None):
        revision = 'a' * 40
        files = {'leapview-site': (b'go-site-binary', 0o555),
                 'etc/ssl/certs/ca-certificates.crt': (b'ca-certificates', 0o444),
                 '.data/map-assets/world.bin': (b'map-data', 0o444)}
        if extra:
            files.update(extra)
        layer_bytes = io.BytesIO()
        with tarfile.open(fileobj=layer_bytes, mode='w') as layer:
            for path in ['.data', '.data/map-assets', 'etc', 'etc/ssl', 'etc/ssl/certs']:
                entry = tarfile.TarInfo('/' + path)
                entry.type = tarfile.DIRTYPE
                entry.mode = 0o555
                layer.addfile(entry)
            for path, (data, mode) in files.items():
                entry = tarfile.TarInfo('/' + path)
                entry.size = len(data)
                entry.mode = mode
                layer.addfile(entry, io.BytesIO(data))
        layer_data = layer_bytes.getvalue()
        config = {'architecture': 'amd64', 'os': 'linux',
                  'rootfs': {'type': 'layers', 'diff_ids': ['sha256:' + hashlib.sha256(layer_data).hexdigest()]},
                  'config': {'Entrypoint': ['/leapview-site'], 'Cmd': ['-addr=:8081'],
                             'User': '65532:65532', 'WorkingDir': '/',
                             'ExposedPorts': {'8081/tcp': {}},
                             'Env': ['LEAPVIEW_SITE_BASE_URL=', 'LEAPVIEW_SITE_SHOWCASE_EMBED_URL='],
                             'Labels': {
                                 'org.opencontainers.image.source': 'https://github.com/flidai/leapview',
                                 'org.opencontainers.image.revision': revision,
                                 'org.opencontainers.image.version': '0.3.0-alpha.1',
                                 'dev.leapview.build.kind': 'site-image',
                                 'dev.leapview.build.dirty': 'false'}}}
        config_bytes = json.dumps(config).encode()
        config_name = hashlib.sha256(config_bytes).hexdigest() + '.json'
        archive = pathlib.Path(directory) / 'site-image.tar.gz'
        with tarfile.open(archive, 'w:gz') as output:
            for name, data in [('manifest.json', json.dumps([{'Config': config_name, 'Layers': ['layer/layer.tar']}]).encode()),
                               (config_name, config_bytes), ('layer/layer.tar', layer_data)]:
                member = tarfile.TarInfo(name)
                member.size = len(data)
                output.addfile(member, io.BytesIO(data))
        artifact = m.candidate_manifest.image_identity(archive, {'revision': revision}, 'site-image')
        artifact.update(kind='site-image', sha256=m.candidate_manifest.digest_file(archive))
        return archive, artifact, files

    def test_exact_site_payload_inventory_hashes_each_file(self):
        with tempfile.TemporaryDirectory() as directory:
            archive, artifact, files = self.image_archive(directory)
            inventory = m.site_payload_inventory(archive, artifact)
            self.assertEqual(inventory['profile'], 'site-image')
            self.assertEqual({item['path'] for item in inventory['files']}, set(files))
            expected = {'sha256:' + hashlib.sha256(data).hexdigest() for data, _ in files.values()}
            self.assertEqual({item['sha256'] for item in inventory['files']}, expected)
            self.assertEqual(inventory['configDigest'], artifact['configDigest'])
            self.assertEqual(inventory['layerDiffIDs'], artifact['layerDiffIDs'])

    def test_site_payload_rejects_unlisted_files_and_writable_modes(self):
        for extra, expected in [({'etc/passwd': (b'account', 0o444)}, 'unexpected file'),
                                ({'.data/map-assets/.wh.world': (b'', 0o444)}, 'whiteouts'),
                                ({'../escape': (b'escape', 0o444)}, 'relative'),
                                ({'leapview-site': (b'go-site-binary', 0o755)}, 'permissions')]:
            with self.subTest(extra=extra), tempfile.TemporaryDirectory() as directory:
                archive, artifact, _ = self.image_archive(directory, extra)
                with self.assertRaisesRegex(ValueError, expected):
                    m.site_payload_inventory(archive, artifact)

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

    def test_image_glibc_must_match_the_native_isa_output(self):
        packages = [self.package(name) for name in m.POLICY['runtime']]
        arm_path = '/nix/store/f4b8yxq1bn6y0n38km6bcahpm6vdgsh2-glibc-2.42-84'
        next(package for package in packages if package['name'] == 'glibc')['metadata']['path'] = arm_path
        paths = {package['metadata']['path'] for package in packages}
        amd_path = '/nix/store/kj7ia0isvb6xh74qavgcshmb7fcskj4l-glibc-2.42-84'
        with self.assertRaisesRegex(ValueError, 'does not match the patched Nix output'):
            m.check_inventory({'artifacts': packages}, paths, amd_path)

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

    def test_arm_document_is_bound_to_the_native_arm64_identity(self):
        arm_document = m.json.loads((ROOT / 'nix/runtime-assessments.arm64.vex.json').read_text())
        amd_decisions = [(item['vulnerability']['name'], item['status']) for item in self.document['statements']]
        arm_decisions = [(item['vulnerability']['name'], item['status']) for item in arm_document['statements']]
        self.assertEqual(arm_decisions, amd_decisions)
        arm_path = '/nix/store/f4b8yxq1bn6y0n38km6bcahpm6vdgsh2-glibc-2.42-84'
        arm_purl = 'pkg:nix/glibc@2.42-84?outputhash=f4b8yxq1bn6y0n38km6bcahpm6vdgsh2'
        arm_packages = [{'name': 'glibc', 'version': '2.42-84', 'purl': arm_purl,
                         'metadata': {'path': arm_path}}]
        self.assertEqual(len(m.validate_assessments(arm_document, arm_packages,
                                                    datetime.date(2026, 10, 5))), 11)
        with self.assertRaisesRegex(ValueError, 'identity'):
            m.validate_assessments(self.document, arm_packages, datetime.date(2026, 10, 5))
        with self.assertRaisesRegex(ValueError, 'identity'):
            m.validate_assessments(arm_document, self.packages, datetime.date(2026, 10, 5))
        probe_claim = next(item['impact_statement'] for item in arm_document['statements']
                           if item['vulnerability']['name'] == 'CVE-2026-19499')
        self.assertIn('still required', probe_claim)
        self.assertIn('does not claim that the probe passed', probe_claim)

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
