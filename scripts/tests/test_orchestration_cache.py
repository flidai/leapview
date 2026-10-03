import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('cache_measurement', Path(__file__).parents[1] / 'measure_orchestration_cache.py')
cache = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cache)

class CacheManifestTests(unittest.TestCase):
    def manifest(self):
        root = '/nix/store/' + 'a' * 32 + '-nix-shell'
        return {'schemaVersion': 1, 'profile': 'orchestration', 'platform': 'Linux-X64',
                'nixVersion': '2.31.2', 'inputID': 'locked-inputs', 'root': root,
                'producerRevision': '1' * 40, 'paths': [root], 'archiveBytes': 250_000_000,
                'archiveSHA256': 'a' * 64}

    def test_toolchain_manifest_is_accepted(self):
        manifest = self.manifest()
        cache.validate_manifest(manifest, 'locked-inputs', manifest['root'])

    def test_changed_inputs_and_wrong_platform_are_rejected(self):
        for field, value in [('inputID', 'changed'), ('profile', 'validation'),
                             ('platform', 'Darwin-ARM64'), ('nixVersion', 'changed'),
                             ('producerRevision', '')]:
            with self.subTest(field=field):
                manifest = self.manifest(); manifest[field] = value
                with self.assertRaises(AssertionError):
                    cache.validate_manifest(manifest, 'locked-inputs', self.manifest()['root'])

    def test_workspaces_application_sources_and_duplicate_paths_are_rejected(self):
        for path in ['/home/runner/work/leapview', '/nix/store/' + 'b' * 32 + '-source',
                     '/nix/store/' + 'b' * 32 + '-leapview', self.manifest()['root']]:
            with self.subTest(path=path):
                manifest = self.manifest(); manifest['paths'].append(path)
                with self.assertRaises(AssertionError):
                    cache.validate_manifest(manifest, 'locked-inputs', manifest['root'])

    def test_budget_and_archive_identity_are_required(self):
        for field, value in [('archiveBytes', 0), ('archiveBytes', cache.ARCHIVE_BUDGET + 1),
                             ('archiveSHA256', '')]:
            with self.subTest(field=field):
                manifest = self.manifest(); manifest[field] = value
                with self.assertRaises(AssertionError):
                    cache.validate_manifest(manifest, 'locked-inputs', manifest['root'])

if __name__ == '__main__':
    unittest.main()
