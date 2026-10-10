import hashlib
import io
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import nix_native_inventory as native


class NativeInventoryTests(unittest.TestCase):
    def test_rebuilt_client_inventory_matches_selected_recipes(self):
        policy = json.loads(native.POLICY_PATH.read_text())
        replacements = {entry['name']: entry for entry in policy['sourceBuiltReplacements']}
        for family in ('http', 'database'):
            lock = native.ROOT / ('nix/' + family + '-source-lock.json')
            selected = json.loads(lock.read_text())
            for name, wrapper in selected['wrappers'].items():
                with self.subTest(extension=name):
                    entry = replacements[name]
                    self.assertEqual(entry['nativeDependencyLockSHA256'], native.digest(lock.read_bytes()))
                    self.assertEqual({key: entry['wrapper'][key] for key in wrapper}, wrapper)

    def test_rebuilt_avro_inventory_matches_exact_custom_fork_policy(self):
        lock = native.ROOT / 'nix/avro-source-lock.json'
        selected = json.loads(lock.read_text())
        policy = json.loads(native.POLICY_PATH.read_text())
        entry = next(e for e in policy['sourceBuiltReplacements'] if e['name'] == 'avro')
        self.assertEqual(entry['nativeDependencyLockSHA256'], native.digest(lock.read_bytes()))
        self.assertEqual({key: entry['wrapper'][key] for key in selected['wrapper']}, selected['wrapper'])
        self.assertEqual(selected['libraries']['avro']['revision'], '8af400279c445a81b8552a7670d8c1ebd92ba34a')

    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.data = b'''version = 4
[[package]]
name = "root"
version = "1.0.0"
dependencies = ["codec"]
[[package]]
name = "codec"
version = "2.3.4"
source = "registry+https://github.com/rust-lang/crates.io-index"
checksum = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
'''
        (self.root / 'Cargo.lock').write_bytes(self.data)
        self.proof = {'path': 'Cargo.lock', 'sha256': native.digest(self.data),
                      'gitBlob': hashlib.sha1(b'blob ' + str(len(self.data)).encode()
                                             + b'\0' + self.data).hexdigest()}
        self.policy = {'version': 1, 'sources': [{'name': 'engine',
                       'repository': 'https://github.com/example/engine',
                       'sourceRevision': 'a' * 40, 'files': [self.proof]}],
                       'unresolvedBuildEdges': [{'component': 'engine',
                          'reason': 'actual binary dependency closure unavailable'}]}

    def test_authenticated_lock_preserves_registry_checksum_and_edges(self):
        result = native.source_inventory(self.root, self.policy)
        packages = result['sources'][0]['cargoPackages']
        self.assertEqual(packages[0]['dependencies'], ['codec'])
        self.assertEqual(packages[1]['checksum'], 'a' * 64)
        self.assertEqual(result['fileHashes'], {'Cargo.lock': native.digest(self.data)})
        self.assertFalse(result['coverageComplete'])

    def test_changed_upstream_lock_is_rejected(self):
        (self.root / 'Cargo.lock').write_bytes(self.data.replace(b'2.3.4', b'2.3.5'))
        with self.assertRaisesRegex(ValueError, 'source evidence differs'):
            native.source_inventory(self.root, self.policy)

    def test_git_blob_is_checked_independently(self):
        self.proof['gitBlob'] = 'b' * 40
        with self.assertRaisesRegex(ValueError, 'Git blob'):
            native.source_inventory(self.root, self.policy)

    def test_symlink_and_parent_traversal_rejected(self):
        (self.root / 'alias').symlink_to(self.root)
        for path in ['../Cargo.lock', '/Cargo.lock', 'alias/Cargo.lock']:
            with self.subTest(path=path), self.assertRaises(ValueError):
                native.regular_file(self.root, path)

    def test_registry_package_without_checksum_is_not_inventoried(self):
        with self.assertRaisesRegex(ValueError, 'registry checksum'):
            native.cargo_packages(self.data.replace(b'checksum = "' + b'a' * 64 + b'"', b''))

    def test_git_package_requires_exact_revision(self):
        data = self.data.replace(b'registry+https://github.com/rust-lang/crates.io-index',
                                b'git+https://github.com/example/codec#main')
        with self.assertRaisesRegex(ValueError, 'Git source revision'):
            native.cargo_packages(data)

    def test_producer_complete_claim_cannot_override_missing_build_closure(self):
        (self.root / 'native-evidence.json').write_text(json.dumps({'coverageComplete': True}))
        with self.assertRaisesRegex(ValueError, 'engine: actual binary dependency closure unavailable'):
            native.verify(self.root, self.root / 'image.tar', 'application-image',
                          'linux/amd64', 'c' * 40, self.policy)

    def test_collector_retains_raw_reports_without_claiming_binary_coverage(self):
        self.proof['path'] = 'engine/Cargo.lock'
        archive = self.root / 'image.tar'
        archive.write_bytes(b'exact candidate')
        destination = self.root / 'collection'
        responses = [io.BytesIO(self.data), io.BytesIO(b'{"results":[{}]}')]
        with patch.object(native.candidate, 'image_identity', return_value={'platform': 'linux/amd64'}), \
                patch.object(native.urllib.request, 'urlopen', side_effect=responses):
            result = native.collect(destination, archive, 'application-image',
                                    'linux/amd64', 'c' * 40, self.policy)
        self.assertFalse(result['coverageComplete'])
        self.assertNotIn('verifiedDigest', result)
        self.assertEqual(result['sourcePackageCount'], 1)
        self.assertEqual(result['artifact']['sha256'], native.digest(b'exact candidate'))
        self.assertEqual(set(result['fileHashes']), {'sources/engine/Cargo.lock',
                         'source-inventory.json', 'osv/0000-request.json', 'osv/0000-response.json'})
        self.assertEqual((destination / 'sources/engine/Cargo.lock').read_bytes(), self.data)

    def test_collector_rejects_unrequested_or_paginated_scanner_result(self):
        self.proof['path'] = 'engine/Cargo.lock'
        archive = self.root / 'image.tar'
        archive.write_bytes(b'candidate')
        for response in [b'{"results":[]}', b'{"results":[{"next_page_token":"more"}]}']:
            destination = self.root / ('collection-' + native.digest(response)[7:])
            with self.subTest(response=response), \
                    patch.object(native.candidate, 'image_identity', return_value={'platform': 'linux/amd64'}), \
                    patch.object(native.urllib.request, 'urlopen', side_effect=[io.BytesIO(self.data), io.BytesIO(response)]), \
                    self.assertRaises(ValueError):
                native.collect(destination, archive, 'application-image', 'linux/amd64', 'c' * 40, self.policy)

    def test_collector_does_not_reuse_or_overwrite_an_existing_evidence_tree(self):
        archive = self.root / 'image.tar'
        archive.write_bytes(b'candidate')
        with patch.object(native.candidate, 'image_identity', return_value={'platform': 'linux/amd64'}), \
                self.assertRaisesRegex(ValueError, 'new directory'):
            native.collect(self.root, archive, 'application-image', 'linux/amd64', 'c' * 40, self.policy)


if __name__ == '__main__':
    unittest.main()
