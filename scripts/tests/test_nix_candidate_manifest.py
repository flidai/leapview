import copy
from datetime import date, datetime, timedelta, timezone
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import tarfile
import tempfile
import subprocess
import unittest
from contextlib import redirect_stdout
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('candidate', ROOT / 'scripts/nix_candidate_manifest.py')
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


def sha(value):
    return 'sha256:' + hashlib.sha256(value).hexdigest()


class CandidateTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        self.archive = self.root / 'image.tar.gz'
        self.source = {'repository': 'flidai/leapview', 'revision': 'a' * 40,
                       'inputs': [{'path': 'flake.lock', 'sha256': sha(b'lock')} ]}
        self.config = {'architecture': 'amd64', 'os': 'linux',
                       'rootfs': {'type': 'layers', 'diff_ids': [sha(b'layer')]},
                       'config': {'Labels': {
                           'org.opencontainers.image.source': 'https://github.com/flidai/leapview',
                           'org.opencontainers.image.revision': self.source['revision'],
                           'org.opencontainers.image.version': '0.3.0-alpha.1',
                           'dev.leapview.build.kind': 'application-image',
                           'dev.leapview.build.dirty': 'false'}}}
        deadline = date.fromisoformat(json.loads((ROOT / 'nix/runtime-security-policy.json').read_bytes())['assessmentReviewUntil'])
        clock = patch.object(m, 'current_date', return_value=deadline - timedelta(days=1))
        clock.start()
        self.addCleanup(clock.stop)
        self.write_archive()

    def write_archive(self, layer=b'layer', mutate_manifest=None, duplicate=False):
        config = json.dumps(self.config).encode()
        name = hashlib.sha256(config).hexdigest() + '.json'
        manifest = [{'Config': name, 'Layers': ['one/layer.tar'], 'RepoTags': ['candidate:test']}]
        if mutate_manifest:
            mutate_manifest(manifest)
        entries = [('manifest.json', json.dumps(manifest).encode()), (name, config), ('one/layer.tar', layer)]
        if duplicate:
            entries.append(entries[0])
        with tarfile.open(self.archive, 'w:gz') as output:
            for path, data in entries:
                entry = tarfile.TarInfo(path)
                entry.size = len(data)
                output.addfile(entry, io.BytesIO(data))

    def collect(self, **kwargs):
        return m.collect(self.archive, 'application-image', self.source, **kwargs)

    def test_content_identity_and_missing_gates_are_explicit(self):
        manifest = self.collect()
        self.assertEqual(manifest['artifact']['sha256'], sha(self.archive.read_bytes()))
        self.assertEqual(manifest['artifact']['platform'], 'linux/amd64')
        self.assertEqual(manifest['artifact']['layerDiffIDs'], [sha(b'layer')])
        self.assertEqual(manifest['source'], self.source)
        self.assertFalse(manifest['releaseAdmission'])
        self.assertIn('provenance', manifest['requiredReleaseEvidence'])
        self.assertIn('go-and-embedded-native-coverage', manifest['requiredReleaseEvidence'])
        self.assertIn('oci-admission', manifest['requiredReleaseEvidence'])
        self.assertIn('canonical-release-identity', manifest['requiredReleaseEvidence'])
        self.assertIn('supported-platform-matrix', manifest['requiredReleaseEvidence'])
        self.assertIn('exact-artifact-promotion', manifest['requiredReleaseEvidence'])
        self.assertEqual(manifest['evidence'], {})
        m.verify(manifest, self.archive, self.source)

    def test_source_revision_and_dirty_builds_fail_closed(self):
        for key, value in [('org.opencontainers.image.revision', 'b' * 40),
                           ('org.opencontainers.image.source', 'https://github.com/elsewhere/repo'),
                           ('dev.leapview.build.dirty', 'true')]:
            with self.subTest(key=key):
                original = copy.deepcopy(self.config)
                self.config['config']['Labels'][key] = value
                self.write_archive()
                with self.assertRaises(ValueError):
                    self.collect()
                self.config = original

    def test_image_kind_cannot_reuse_another_outputs_evidence(self):
        with self.assertRaises(ValueError):
            m.collect(self.archive, 'site-image', self.source)
        self.config['config']['Labels']['dev.leapview.build.kind'] = 'site-image'
        self.write_archive()
        self.assertEqual(m.collect(self.archive, 'site-image', self.source)['artifact']['kind'], 'site-image')

    def test_layer_tampering_and_duplicate_members_are_rejected(self):
        self.write_archive(layer=b'changed')
        with self.assertRaisesRegex(ValueError, 'layer'):
            self.collect()
        self.write_archive(duplicate=True)
        with self.assertRaisesRegex(ValueError, 'duplicate'):
            self.collect()

    def test_manifest_cannot_change_members_or_add_an_image(self):
        mutations = [lambda x: x[0].update(Config='../escape.json'),
                     lambda x: x[0].update(Layers=['absent/layer.tar']),
                     lambda x: x.append(copy.deepcopy(x[0]))]
        for mutation in mutations:
            with self.subTest(mutation=mutation):
                self.write_archive(mutate_manifest=mutation)
                with self.assertRaises(ValueError):
                    self.collect()

    def test_changed_input_platform_or_artifact_invalidates_record(self):
        manifest = self.collect()
        for field, value in [('releaseAdmission', True), ('releaseAdmission', 0), ('schemaVersion', True),
                             ('requiredReleaseEvidence', []),
                             ('artifact', {**manifest['artifact'], 'platform': 'linux/arm64'})]:
            changed = copy.deepcopy(manifest)
            changed[field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                m.verify(changed, self.archive, self.source)
        with self.assertRaises(ValueError):
            m.verify(manifest, self.archive, self.source, kind='site-image')
        changed_source = copy.deepcopy(self.source)
        changed_source['inputs'][0]['sha256'] = sha(b'changed lock')
        with self.assertRaises(ValueError):
            m.verify(manifest, self.archive, changed_source)
        self.config['architecture'] = 'arm64'
        self.write_archive()
        with self.assertRaises(ValueError):
            m.verify(manifest, self.archive, self.source)

    def test_archive_kinds_require_explicit_platform_and_source_identity(self):
        artifact = self.root / 'client.tar.gz'
        artifact.write_bytes(b'opaque archive; installation qualifier owns its contents')
        identity = {'platform': 'linux/arm64', 'version': '0.3.0-alpha.1',
                    'sourceRevision': self.source['revision']}
        for kind in ['cli-archive', 'application-archive', 'desktop-archive']:
            manifest = m.collect(artifact, kind, self.source, archive_identity=identity)
            self.assertEqual(manifest['artifact']['sha256'], sha(artifact.read_bytes()))
            self.assertIn('embedded-source-identity', manifest['requiredReleaseEvidence'])
            self.assertNotIn('oci-admission', manifest['requiredReleaseEvidence'])
            self.assertFalse(manifest['releaseAdmission'])
            m.verify(manifest, artifact, self.source, archive_identity=identity)
        for invalid in [None, {**identity, 'platform': 'darwin/arm64'},
                        {**identity, 'sourceRevision': 'b' * 40}]:
            with self.assertRaises(ValueError):
                m.collect(artifact, 'cli-archive', self.source, archive_identity=invalid)

    def test_unknown_kind_and_malformed_source_are_rejected(self):
        with self.assertRaises(ValueError):
            m.collect(self.archive, 'unknown', self.source)
        for source in [{**self.source, 'revision': 'main'},
                       {**self.source, 'inputs': []},
                       {**self.source, 'inputs': [{'path': '../secret', 'sha256': sha(b'lock')}]}]:
            with self.assertRaises(ValueError):
                m.collect(self.archive, 'application-image', source)

    def test_cli_collect_verify_and_checkout_identity(self):
        checkout = self.root / 'checkout'
        checkout.mkdir()
        def git(*args):
            return subprocess.check_output(['git', '-C', str(checkout), *args], stderr=subprocess.DEVNULL, text=True).strip()
        git('init')
        git('config', 'user.name', 'Candidate test')
        git('config', 'user.email', 'candidate@example.test')
        (checkout / 'flake.lock').write_text('{}')
        git('add', 'flake.lock')
        git('commit', '-m', 'test fixture')
        self.source = m.checkout_source(checkout)
        self.assertEqual(self.source['revision'], git('rev-parse', 'HEAD'))
        self.config['config']['Labels']['org.opencontainers.image.revision'] = self.source['revision']
        self.write_archive()
        output = self.root / 'candidate.json'
        argv = ['collect', str(self.archive), '--kind', 'application-image']
        with patch.object(m, 'ROOT', checkout), redirect_stdout(io.StringIO()):
            with patch('sys.argv', argv + ['--output', str(output)]):
                m.main()
            with patch('sys.argv', argv + ['--verify', str(output)]):
                m.main()
            with patch('sys.argv', argv + ['--output', str(output)]):
                with self.assertRaises(SystemExit):
                    m.main()  # Evidence is never overwritten.
            with patch('sys.argv', ['verify', str(self.archive), '--kind', 'site-image', '--verify', str(output)]):
                with self.assertRaises(SystemExit):
                    m.main()
            (checkout / 'flake.lock').write_text('{"changed":true}')
            with patch('sys.argv', argv + ['--verify', str(output)]):
                with self.assertRaisesRegex(SystemExit, 'clean tracked checkout'):
                    m.main()

    def test_ambiguous_json_is_rejected(self):
        for data in [b'{"schemaVersion":1,"schemaVersion":2}', b'{"x":NaN}', b'{"x":Infinity}']:
            with self.subTest(data=data), self.assertRaises(ValueError):
                m.read_json(data)
        path = self.root / 'oversize.json'
        path.write_bytes(b'{}' + b' ' * 100)
        with self.assertRaisesRegex(ValueError, 'byte limit'):
            m.read_json_file(path, limit=8)

    def runtime_evidence(self):
        directory = self.root / 'runtime'
        directory.mkdir()
        names = ['sbom.syft.json', 'sbom.spdx.json', 'runtime.syft.json', 'runtime.grype.json',
                 'runtime.assessed.grype.json', 'controls.synthetic.syft.json', 'controls.grype.json',
                 'assessments.vex.json', 'syft-config.json', 'grype-config.json']
        for name in names:
            data = {'spdxVersion': 'SPDX-2.3', 'SPDXID': 'SPDXRef-DOCUMENT',
                    'documentNamespace': 'https://example.test/inventory', 'packages': [{'name': 'glibc'}]}
            (directory / name).write_text(json.dumps(data))
        (directory / 'assessments.vex.json').write_bytes((ROOT / 'nix/runtime-assessments.vex.json').read_bytes())
        policy = ROOT / 'nix/runtime-security-policy.json'
        self.summary = {'schemaVersion': 1, 'enforcementMode': 'enforce',
                        'coverageQualified': True, 'runtimeVulnerabilityGatePassed': True,
                        'archiveSHA256': sha(self.archive.read_bytes()).removeprefix('sha256:'),
                        'policySHA256': sha(policy.read_bytes()).removeprefix('sha256:'),
                        'assessmentsSHA256': sha((directory / 'assessments.vex.json').read_bytes()).removeprefix('sha256:'),
                        'assessmentReviewUntil': json.loads(policy.read_bytes())['assessmentReviewUntil'],
                        'database': {'status': {'valid': True, 'built': datetime.now(timezone.utc).isoformat()}},
                        'scanners': {tool: json.loads(policy.read_bytes())[tool + 'Version'] for tool in ['syft', 'grype']},
                        'reportSHA256': {name: sha((directory / name).read_bytes()).removeprefix('sha256:') for name in names}}
        (directory / 'summary.json').write_text(json.dumps(self.summary))
        return directory

    def test_runtime_evidence_is_bound_to_exact_bytes_without_release_authority(self):
        directory = self.runtime_evidence()
        manifest = self.collect(runtime_dir=directory)
        self.assertEqual(manifest['evidence']['nix-runtime']['summarySHA256'], sha((directory / 'summary.json').read_bytes()))
        self.assertEqual(len(manifest['evidence']['nix-runtime']['reports']), 10)
        self.assertFalse(manifest['releaseAdmission'])
        self.assertIn('go-and-embedded-native-coverage', manifest['requiredReleaseEvidence'])
        m.verify(manifest, self.archive, self.source, runtime_dir=directory)
        (directory / 'runtime.assessed.grype.json').write_text('{}')
        with self.assertRaisesRegex(ValueError, 'report'):
            m.verify(manifest, self.archive, self.source, runtime_dir=directory)

    def test_replayed_coverage_only_or_incomplete_runtime_reports_fail(self):
        directory = self.runtime_evidence()
        changes = [('archiveSHA256', 'b' * 64), ('enforcementMode', 'coverage-only'),
                   ('schemaVersion', True),
                   ('coverageQualified', False), ('runtimeVulnerabilityGatePassed', False),
                   ('policySHA256', 'c' * 64), ('reportSHA256', {}), ('error', 'scan failed')]
        for field, value in changes:
            with self.subTest(field=field):
                summary = {**self.summary, field: value}
                (directory / 'summary.json').write_text(json.dumps(summary))
                with self.assertRaises(ValueError):
                    self.collect(runtime_dir=directory)
        (directory / 'summary.json').write_text(json.dumps(self.summary))
        with patch.object(m, 'current_date', return_value=date.fromisoformat(self.summary['assessmentReviewUntil'])):
            with self.assertRaisesRegex(ValueError, 'expired'):
                self.collect(runtime_dir=directory)

    def test_stale_invalid_or_future_database_evidence_is_rejected(self):
        directory = self.runtime_evidence()
        now = datetime.now(timezone.utc)
        for status in [{}, {'valid': False, 'built': now.isoformat()},
                       {'valid': True, 'built': (now - timedelta(hours=121)).isoformat()},
                       {'valid': True, 'built': (now + timedelta(hours=1)).isoformat()},
                       {'valid': True, 'built': '2026-10-01T00:00:00'}]:
            with self.subTest(status=status):
                summary = {**self.summary, 'database': {'status': status}}
                (directory / 'summary.json').write_text(json.dumps(summary))
                with self.assertRaisesRegex(ValueError, 'database'):
                    self.collect(runtime_dir=directory)


if __name__ == '__main__':
    unittest.main()
