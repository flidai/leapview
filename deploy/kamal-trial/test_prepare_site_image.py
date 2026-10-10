import hashlib
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

import prepare_site_image


class ArchivedImageTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.layout = self.root / 'oci'
        self.blobs = self.layout / 'blobs' / 'sha256'
        self.blobs.mkdir(parents=True)
        (self.layout / 'oci-layout').write_text('{"imageLayoutVersion":"1.0.0"}')
        self.config = 'sha256:' + 'a' * 64
        self.platform = self.store({'schemaVersion': 2, 'config': {'digest': self.config}})
        self.index = self.store({'schemaVersion': 2, 'manifests': [
            {'digest': self.platform, 'platform': {'os': 'linux', 'architecture': 'amd64'}}]})
        self.descriptor = {'digest': self.index, 'annotations': {'org.opencontainers.image.ref.name': 'trial-original'}}
        self.write_layout([self.descriptor])
        self.admission = self.root / 'admission.json'
        self.admission.write_text(json.dumps({'image': 'ghcr.io/flidai/leapview-site-kamal-trial@' + self.index,
                                             'digest': self.index, 'registryDigest': self.index}))
        self.output = self.root / 'prepared'

    def store(self, value):
        raw = json.dumps(value).encode()
        digest = hashlib.sha256(raw).hexdigest()
        (self.blobs / digest).write_bytes(raw)
        return 'sha256:' + digest

    def write_layout(self, descriptors):
        (self.layout / 'index.json').write_text(json.dumps({'manifests': descriptors}))

    def prepare(self):
        args = ['prepare_site_image.py', '--admission', str(self.admission), '--output', str(self.output),
                '--archive-layout', str(self.layout)]

        def copy(command, *, check):
            self.assertTrue(check)
            self.assertEqual(command[:4], ['skopeo', 'copy', '--all', '--preserve-digests'])
            self.assertEqual(command[4], 'oci:' + str(self.layout.resolve()) + ':trial-original')
            (self.output / 'site.oci.tar').write_bytes(b'copied original OCI image')

        with patch.object(sys, 'argv', args), patch.object(prepare_site_image.subprocess, 'run', side_effect=copy) as run, \
                patch.object(prepare_site_image.subprocess, 'check_output', side_effect=AssertionError('registry access')):
            prepare_site_image.main()
        return run

    def test_prepares_original_identity_without_registry_access(self):
        run = self.prepare()
        run.assert_called_once()
        record = json.loads((self.output / 'site-record.json').read_text())
        self.assertEqual(record['platform_digest'], self.platform)
        self.assertEqual(record['config_digest'], self.config)
        self.assertEqual(record['admission'], json.loads(self.admission.read_text()))
        self.assertEqual(record['archive_sha256'], hashlib.sha256(b'copied original OCI image').hexdigest())

    def test_missing_original_digest_blocks_copy(self):
        self.write_layout([{**self.descriptor, 'digest': 'sha256:' + 'b' * 64}])
        with self.assertRaisesRegex(SystemExit, 'exactly one archived image'):
            self.prepare()
        self.assertFalse(self.output.exists())

    def test_shared_tag_cannot_substitute_another_image(self):
        self.write_layout([self.descriptor, {**self.descriptor, 'digest': 'sha256:' + 'b' * 64}])
        with self.assertRaisesRegex(SystemExit, 'ambiguous archived image tag'):
            self.prepare()
        self.assertFalse(self.output.exists())

    def test_corrupt_original_index_blocks_copy(self):
        (self.blobs / self.index.split(':')[1]).write_text('{}')
        with self.assertRaisesRegex(SystemExit, 'manifest digest mismatch'):
            self.prepare()
        self.assertFalse(self.output.exists())

    def test_corrupt_platform_manifest_blocks_copy(self):
        (self.blobs / self.platform.split(':')[1]).write_text('{}')
        with self.assertRaisesRegex(SystemExit, 'manifest digest mismatch'):
            self.prepare()
        self.assertFalse(self.output.exists())

    def test_existing_archive_is_preserved(self):
        self.output.mkdir()
        archive = self.output / 'site.oci.tar'
        archive.write_bytes(b'previous archive')
        with self.assertRaisesRegex(SystemExit, 'refusing to overwrite'):
            self.prepare()
        self.assertEqual(archive.read_bytes(), b'previous archive')

    def test_invalid_child_digest_is_rejected_before_reading_a_path(self):
        self.index = self.store({'manifests': [
            {'digest': 'sha256:../../outside', 'platform': {'os': 'linux', 'architecture': 'amd64'}}]})
        self.write_layout([{**self.descriptor, 'digest': self.index}])
        self.admission.write_text(json.dumps({'image': 'ghcr.io/flidai/leapview-site-kamal-trial@' + self.index,
                                             'digest': self.index, 'registryDigest': self.index}))
        with self.assertRaisesRegex(SystemExit, 'invalid manifest digest'):
            self.prepare()
        self.assertFalse(self.output.exists())

    def test_original_registry_preparation_still_works(self):
        evidence = json.loads(self.admission.read_text())
        repository = evidence['image'].split('@')[0]

        def inspect(command):
            self.assertEqual(command[:3], ['skopeo', 'inspect', '--raw'])
            self.assertTrue(command[3].startswith('docker://' + repository + '@'))
            return (self.blobs / command[3].split('@sha256:')[1]).read_bytes()

        def copy(command, *, check):
            self.assertEqual(command[4], 'docker://' + evidence['image'])
            (self.output / 'site.oci.tar').write_bytes(b'original registry image')

        args = ['prepare_site_image.py', '--admission', str(self.admission), '--output', str(self.output)]
        with patch.object(sys, 'argv', args), patch.object(prepare_site_image.subprocess, 'run', side_effect=copy), \
                patch.object(prepare_site_image.subprocess, 'check_output', side_effect=inspect):
            prepare_site_image.main()
        self.assertEqual(json.loads((self.output / 'site-record.json').read_text())['platform_digest'], self.platform)


if __name__ == '__main__':
    unittest.main()
