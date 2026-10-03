import importlib.util
import contextlib
import io
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

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

class FreshStoreTests(unittest.TestCase):
    def measure(self, operation):
        root = '/nix/store/' + 'a' * 32 + '-nix-shell'
        realized = False
        commands = []

        def output(*args):
            if args[:2] == ('git', 'rev-parse'):
                return '1' * 40
            if args[:2] == ('nix', 'eval'):
                return root
            if args[:2] == ('nix', 'path-info'):
                if not realized:
                    raise subprocess.CalledProcessError(1, args, 'shell output is not valid')
                return json.dumps({root: {'narSize': 8}})
            self.fail(f'unexpected output command: {args}')

        def run(args, **kwargs):
            nonlocal realized
            commands.append(args)
            if args[:2] == ['nix', 'build']:
                self.assertEqual(args, ['nix', 'build', '--no-update-lock-file', '--no-link', cache.SHELL])
                realized = True
            elif args[:2] == ['nix', 'develop']:
                # Develop realizes an environment derivation, not the shell outPath.
                pass
            elif args[:2] == ['nix-store', '--export']:
                self.assertTrue(realized)
                kwargs['stdout'].write(b'closure')
            else:
                self.fail(f'unexpected run command: {args}')

        with tempfile.TemporaryDirectory() as directory:
            environment = {'RUNNER_TEMP': directory, 'CACHE_INPUT_ID': 'locked-inputs',
                           'GITHUB_RUN_ID': '1', 'GITHUB_RUN_ATTEMPT': '1',
                           'GITHUB_REF': 'refs/heads/main', 'DEFAULT_BRANCH': 'main',
                           'GITHUB_SHA': '1' * 40}
            with patch.dict(os.environ, environment, clear=True), \
                 patch('sys.argv', ['measure', operation]), \
                 patch.object(cache, 'output', side_effect=output), \
                 patch.object(cache.subprocess, 'run', side_effect=run), \
                 contextlib.redirect_stdout(io.StringIO()):
                cache.main()
            metrics = json.loads((Path(directory) / 'orchestration-cache-metrics.json').read_text())
            if operation == 'produce':
                manifest = json.loads((Path(directory) / 'orchestration-cache/manifest.json').read_text())
                self.assertEqual(manifest['root'], root)
                self.assertGreater(metrics['archiveBytes'], 0)
            return commands

    def test_producer_realizes_missing_shell_output_before_inventory(self):
        self.measure('produce')

    def test_cold_consumer_only_realizes_the_environment(self):
        commands = self.measure('baseline')
        self.assertFalse(any(command[:2] == ['nix', 'build'] for command in commands))

if __name__ == '__main__':
    unittest.main()
