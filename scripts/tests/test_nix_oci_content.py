import copy
import gzip
import importlib.util
import io
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch
from contextlib import redirect_stdout

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import nix_candidate_manifest as candidate


class OCIContentTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        (self.root / 'blobs/sha256').mkdir(parents=True)
        (self.root / 'oci-layout').write_text('{"imageLayoutVersion":"1.0.0"}')
        self.layer = b'ordered image layer'
        self.config = {'os': 'linux', 'architecture': 'amd64',
                       'rootfs': {'type': 'layers', 'diff_ids': [candidate.digest_bytes(self.layer)]}}
        self.record = {'artifact': {'kind': 'application-image', 'platform': 'linux/amd64',
                                   'version': 'test', 'configDigest': candidate.digest_bytes(candidate.canonical_bytes(self.config)),
                                   'layerDiffIDs': self.config['rootfs']['diff_ids']},
                       'source': {'revision': 'a' * 40}, 'candidateDigest': 'sha256:' + 'b' * 64,
                       'requiredReleaseEvidence': ['provenance'], 'releaseAdmission': False}
        self.manifest = {'schemaVersion': 2, 'mediaType': oci.MANIFEST,
                         'config': self.blob(candidate.canonical_bytes(self.config), oci.CONFIG),
                         'layers': [self.blob(gzip.compress(self.layer), oci.GZIP)]}
        self.publish(self.manifest)

    def blob(self, data, media):
        digest = candidate.digest_bytes(data)
        (self.root / 'blobs/sha256' / digest[7:]).write_bytes(data)
        return {'mediaType': media, 'digest': digest, 'size': len(data)}

    def publish(self, value):
        descriptor = self.blob(candidate.canonical_bytes(value), value['mediaType'])
        self.digest = descriptor['digest']
        (self.root / 'index.json').write_text(json.dumps({'schemaVersion': 2, 'manifests': [descriptor]}))

    def bind(self, records=None, platforms=None):
        return oci.bind(self.root, self.digest, records or [self.record], platforms or ['linux/amd64'])

    def test_binds_root_platform_config_compressed_layers_and_candidate(self):
        result = self.bind()
        self.assertEqual(result['oci']['digest'], self.digest)
        self.assertEqual(result['platforms'][0]['candidateDigest'], self.record['candidateDigest'])
        self.assertEqual(result['platforms'][0]['layers'][0]['diffID'], candidate.digest_bytes(self.layer))
        self.assertFalse(result['releaseAdmission'])
        oci.verify(result, self.bind())
        result['releaseAdmission'] = 0
        with self.assertRaises(ValueError):
            oci.verify(result, self.bind())

    def test_substituted_config_or_layer_fails_even_with_new_manifest_digest(self):
        for field in ['config', 'layers']:
            with self.subTest(field=field):
                changed = copy.deepcopy(self.manifest)
                if field == 'config':
                    changed[field] = self.blob(b'{}', oci.CONFIG)
                else:
                    changed[field] = [self.blob(gzip.compress(b'other layer'), oci.GZIP)]
                self.publish(changed)
                with self.assertRaises(ValueError):
                    self.bind()

    def test_descriptor_size_hash_type_and_external_content_fail(self):
        for mutation in [lambda d: d.update(size=True), lambda d: d.update(size=d['size'] + 1),
                         lambda d: d.update(digest='sha256:' + '0' * 64),
                         lambda d: d.update(urls=['https://example.test/layer']),
                         lambda d: d.update(mediaType='unknown')]:
            with self.subTest(mutation=mutation):
                changed = copy.deepcopy(self.manifest)
                mutation(changed['layers'][0])
                self.publish(changed)
                with self.assertRaises((ValueError, OSError)):
                    self.bind()

    def test_corrupted_blob_invalid_gzip_and_decompression_limit(self):
        path = self.root / 'blobs/sha256' / self.manifest['layers'][0]['digest'][7:]
        path.write_bytes(b'x' * self.manifest['layers'][0]['size'])
        with self.assertRaises(ValueError):
            self.bind()
        self.manifest['layers'] = [self.blob(b'not gzip', oci.GZIP)]
        self.publish(self.manifest)
        with self.assertRaises((ValueError, OSError)):
            self.bind()
        self.manifest['layers'] = [self.blob(gzip.compress(self.layer), oci.GZIP)]
        self.publish(self.manifest)
        with patch.object(oci, 'MAX_LAYER_BYTES', len(self.layer) - 1):
            with self.assertRaises(ValueError):
                self.bind()

    def test_symlink_blob_is_rejected(self):
        path = self.root / 'blobs/sha256' / self.manifest['config']['digest'][7:]
        data = path.read_bytes()
        path.unlink()
        other = self.root / 'external'
        other.write_bytes(data)
        path.symlink_to(other)
        with self.assertRaises(ValueError):
            self.bind()

    def test_reordered_layers_and_semantically_equal_config_bytes_are_rejected(self):
        config = copy.deepcopy(self.config)
        config['rootfs']['diff_ids'].append(candidate.digest_bytes(b'second layer'))
        config_blob = self.blob(candidate.canonical_bytes(config), oci.CONFIG)
        self.record['artifact']['configDigest'] = config_blob['digest']
        self.record['artifact']['layerDiffIDs'] = config['rootfs']['diff_ids']
        layers = self.manifest['layers'] + [self.blob(b'second layer', oci.LAYER)]
        self.manifest.update(config=config_blob, layers=layers)
        self.publish(self.manifest)
        self.bind()
        self.publish({**self.manifest, 'layers': list(reversed(layers))})
        with self.assertRaisesRegex(ValueError, 'ordered layer'):
            self.bind()
        changed = {**self.manifest, 'config': self.blob(json.dumps(config, indent=2).encode(), oci.CONFIG)}
        self.publish(changed)
        with self.assertRaisesRegex(ValueError, 'config differs'):
            self.bind()

    def test_ambiguous_json_and_total_read_budget_are_rejected(self):
        with patch.object(candidate, 'MAX_JSON_BYTES', 64):
            with self.assertRaisesRegex(ValueError, 'binding exceeds JSON'):
                self.bind()
        with patch.object(oci, 'MAX_LAYOUT_BYTES', 1):
            with self.assertRaisesRegex(ValueError, 'byte limit'):
                self.bind()
        for contents in ['{"schemaVersion":2,"schemaVersion":2,"manifests":[]}',
                         '{"schemaVersion":true,"manifests":[]}', '[]']:
            (self.root / 'index.json').write_text(contents)
            with self.assertRaises(ValueError):
                self.bind()

    def test_exact_platform_set_and_flat_index(self):
        descriptor = self.blob(candidate.canonical_bytes(self.manifest), oci.MANIFEST)
        descriptor['platform'] = {'os': 'linux', 'architecture': 'amd64'}
        self.publish({'schemaVersion': 2, 'mediaType': oci.INDEX, 'manifests': [descriptor]})
        self.assertEqual(self.bind()['platforms'][0]['manifestDigest'], descriptor['digest'])
        with self.assertRaises(ValueError):
            self.bind(platforms=['linux/amd64', 'linux/arm64'])
        for platform in [{'os': 'linux', 'architecture': 'arm64'},
                         {'os': 'linux', 'architecture': 'amd64', 'variant': 'v1'}]:
            descriptor['platform'] = platform
            self.publish({'schemaVersion': 2, 'mediaType': oci.INDEX, 'manifests': [descriptor]})
            with self.assertRaises(ValueError):
                self.bind()

    def test_duplicate_platform_wrong_root_and_missing_layers(self):
        with self.assertRaises(ValueError):
            oci.bind(self.root, 'sha256:' + 'c' * 64, [self.record], ['linux/amd64'])
        with self.assertRaises(ValueError):
            self.bind(records=[self.record, self.record])
        self.manifest['layers'] = []
        self.publish(self.manifest)
        with self.assertRaises(ValueError):
            self.bind()

    def test_two_platform_index_and_mixed_output_identity(self):
        arm = copy.deepcopy(self.record)
        arm['artifact']['platform'] = 'linux/arm64'
        config = {**self.config, 'architecture': 'arm64'}
        config_descriptor = self.blob(candidate.canonical_bytes(config), oci.CONFIG)
        arm['artifact']['configDigest'] = config_descriptor['digest']
        manifests = []
        for architecture, config_blob in [('arm64', config_descriptor), ('amd64', self.manifest['config'])]:
            descriptor = self.blob(candidate.canonical_bytes({**self.manifest, 'config': config_blob}), oci.MANIFEST)
            descriptor['platform'] = {'os': 'linux', 'architecture': architecture}
            manifests.append(descriptor)
        self.publish({'schemaVersion': 2, 'mediaType': oci.INDEX, 'manifests': manifests})
        platforms = ['linux/arm64', 'linux/amd64']
        result = self.bind(records=[arm, self.record], platforms=platforms)
        self.assertEqual([p['platform'] for p in result['platforms']], sorted(platforms))
        for change in [('kind', 'site-image'), ('version', 'other')]:
            changed = copy.deepcopy(arm)
            changed['artifact'][change[0]] = change[1]
            with self.assertRaises(ValueError):
                self.bind(records=[changed, self.record], platforms=platforms)
        arm['source']['revision'] = 'c' * 40
        with self.assertRaises(ValueError):
            self.bind(records=[arm, self.record], platforms=platforms)

    def test_cli_recomputes_archive_and_runtime_evidence_before_binding(self):
        # Use the collector's real archive/report fixtures, not a mocked verifier.
        from test_nix_candidate_manifest import CandidateTests
        fixture = CandidateTests()
        fixture.setUp()
        self.addCleanup(fixture.doCleanups)
        runtime = fixture.runtime_evidence()
        record = candidate.collect(fixture.archive, 'application-image', fixture.source, runtime_dir=runtime)
        manifest_path = self.root / 'candidate.json'
        manifest_path.write_text(json.dumps(record))
        exported = self.root / 'exported'
        self.digest = oci.export_layout(fixture.archive, record, exported)
        with self.assertRaises(ValueError):
            oci.export_layout(fixture.archive, record, exported)
        output = self.root / 'binding.json'
        argv = ['oci-content', 'bind', '--layout', str(exported), '--manifest-digest', self.digest,
                '--platform', 'linux/amd64', '--kind', 'application-image', '--candidate',
                str(fixture.archive), str(manifest_path), str(runtime)]
        with patch.object(candidate, 'checkout_source', return_value=fixture.source), redirect_stdout(io.StringIO()):
            export_argv = ['oci-content', 'export', '--layout', str(self.root / 'cli-export'),
                           '--platform', 'linux/amd64', '--kind', 'application-image', '--candidate',
                           str(fixture.archive), str(manifest_path), str(runtime)]
            with patch('sys.argv', export_argv):
                oci.main()
            bound = oci.bind(exported, self.digest, [record], ['linux/amd64'])
            self.assertEqual(bound['platforms'][0]['configDigest'], record['artifact']['configDigest'])
            with patch('sys.argv', argv + ['--output', str(output)]):
                oci.main()
                with self.assertRaises(SystemExit):
                    oci.main()  # Never overwrite a prior binding.
            with patch('sys.argv', argv + ['--verify', str(output)]):
                oci.main()
                (runtime / 'runtime.assessed.grype.json').write_text('{}')
                with self.assertRaisesRegex(SystemExit, 'report'):
                    oci.main()
            fixture.archive.write_bytes(b'substituted archive')
            with patch('sys.argv', argv + ['--verify', str(output)]):
                with self.assertRaises(SystemExit):
                    oci.main()


spec = importlib.util.spec_from_file_location('oci', Path(__file__).resolve().parents[1] / 'nix_oci_content.py')
oci = importlib.util.module_from_spec(spec)
spec.loader.exec_module(oci)

if __name__ == '__main__':
    unittest.main()
