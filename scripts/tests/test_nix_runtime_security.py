import copy
import importlib.util
import io
import tarfile
import tempfile
import pathlib
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('runtime_security', ROOT / 'scripts/check_nix_runtime_security.py')
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


class CoverageTests(unittest.TestCase):
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


if __name__ == '__main__':
    unittest.main()
