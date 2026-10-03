import importlib.util
import contextlib
import io
import gzip
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch
from types import SimpleNamespace

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

class RetentionTests(unittest.TestCase):
    prefix = 'nix-orchestration-v1-Linux-X64-2.31.2-'
    current = prefix + 'f' * 64

    def entry(self, identity, size=250_000_000, ref='refs/heads/main'):
        return {'id': identity, 'key': self.prefix + f'{identity:064x}',
                'ref': ref, 'size_in_bytes': size,
                'created_at': f'2026-10-0{identity}T00:00:00.201868Z'}

    def test_keep_two_newest_previous_identities_and_make_room_for_current(self):
        entries = [self.entry(i) for i in [3, 1, 2]]
        retired = cache.retention_plan(entries, self.current, 'refs/heads/main', 250_000_000)
        self.assertEqual([e['id'] for e in retired], [1])

    def test_storage_budget_also_retires_oldest_identities(self):
        entries = [self.entry(i, 400_000_000) for i in [2, 1]]
        retired = cache.retention_plan(entries, self.current, 'refs/heads/main', 250_000_000)
        self.assertEqual([e['id'] for e in retired], [1])

    def test_other_namespaces_refs_and_current_identity_are_never_deleted(self):
        unrelated = [dict(self.entry(4), key='go-validation-v2-main'),
                     self.entry(5, ref='refs/pull/42/merge'),
                     dict(self.entry(6), key=self.current)]
        retired = cache.retention_plan([self.entry(i) for i in [1, 2, 3]] + unrelated,
                                       self.current, 'refs/heads/main', 250_000_000)
        self.assertEqual([e['id'] for e in retired], [1])

    def test_bad_envelope_identity_or_entry_fails_before_any_deletion(self):
        for entries, key, size in [([], self.current, 300_000_001),
                                   ([], 'go-validation-v2-main', 250_000_000),
                                   ([dict(self.entry(1), id='1')], self.current, 250_000_000),
                                   ([dict(self.entry(1), size_in_bytes=-1)], self.current, 250_000_000)]:
            with self.subTest(entries=entries, key=key, size=size):
                with self.assertRaises((AssertionError, ValueError)):
                    cache.retention_plan(entries, key, 'refs/heads/main', size)

    def test_retention_executes_only_planned_deletions_and_records_receipts(self):
        entries = [self.entry(i) for i in [3, 1, 2]] + [self.entry(4, ref='refs/pull/42/merge')]
        manifest = CacheManifestTests().manifest()
        commands = []

        def output(*args):
            if args[:2] == ('git', 'rev-parse'):
                return '1' * 40
            if args[:2] == ('nix', 'eval'):
                return manifest['root']
            if args[:2] == ('gh', 'api'):
                return json.dumps([{'actions_caches': entries}])
            self.fail(f'unexpected command: {args}')

        with tempfile.TemporaryDirectory() as directory:
            archive_dir = Path(directory) / 'orchestration-cache'
            archive_dir.mkdir()
            (archive_dir / 'manifest.json').write_text(json.dumps(manifest))
            env = {'GITHUB_REF': 'refs/heads/main', 'DEFAULT_BRANCH': 'main', 'GITHUB_SHA': '1' * 40,
                   'GITHUB_REPOSITORY': 'flidai/leapview', 'CACHE_INPUT_ID': 'locked-inputs',
                   'CACHE_KEY': self.current}
            with patch.dict(os.environ, env, clear=True), patch.object(cache, 'output', side_effect=output), \
                 patch.object(cache.subprocess, 'run', side_effect=lambda args, **kwargs: commands.append(args)):
                cache.enforce_retention(archive_dir)
            self.assertEqual(commands, [['gh', 'api', '--method', 'DELETE',
                                         'repos/flidai/leapview/actions/caches/1']])
            receipt = json.loads((Path(directory) / 'orchestration-cache-retention.json').read_text())
            self.assertEqual([e['id'] for e in receipt['retired']], [1])

    def test_untrusted_producer_fails_before_toolchain_or_cache_work(self):
        for ref, sha in [('refs/heads/experiment', '1' * 40), ('refs/heads/main', '2' * 40)]:
            with self.subTest(ref=ref, sha=sha), tempfile.TemporaryDirectory() as directory, \
                 patch.dict(os.environ, {'RUNNER_TEMP': directory, 'GITHUB_REF': ref,
                                         'DEFAULT_BRANCH': 'main', 'GITHUB_SHA': sha}, clear=True), \
                 patch('sys.argv', ['measure', 'produce']), \
                 patch.object(cache, 'output', return_value='1' * 40), \
                 patch.object(cache.subprocess, 'run') as run:
                with self.assertRaises(AssertionError):
                    cache.main()
                run.assert_not_called()

class FreshStoreTests(unittest.TestCase):
    def measure(self, operation, hit=False, corruption=None):
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
            if args[:3] == ('nix-store', '--query', '--requisites'):
                return root
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
                           'GITHUB_SHA': '1' * 40, 'CACHE_HIT': str(hit).lower()}
            if hit:
                archive_dir = Path(directory) / 'orchestration-cache'
                archive_dir.mkdir()
                archive = archive_dir / 'closure.nar.gz'
                archive.write_bytes(gzip.compress(b'closure'))
                manifest = CacheManifestTests().manifest()
                manifest.update(archiveBytes=archive.stat().st_size, archiveSHA256=cache.digest(archive))
                if corruption == 'inputs':
                    manifest['inputID'] = 'different'
                elif corruption == 'paths-type':
                    manifest['paths'] = None
                elif corruption == 'bytes-type':
                    manifest['archiveBytes'] = 'invalid'
                elif corruption == 'digest':
                    manifest['archiveSHA256'] = '0' * 64
                elif corruption == 'compression':
                    archive.write_bytes(b'not a gzip archive')
                    manifest.update(archiveBytes=archive.stat().st_size, archiveSHA256=cache.digest(archive))
                (archive_dir / 'manifest.json').write_text(json.dumps(manifest))
            process = SimpleNamespace(stdin=io.BytesIO(), wait=lambda: 0, poll=lambda: 0)
            with patch.dict(os.environ, environment, clear=True), \
                 patch('sys.argv', ['measure', operation]), \
                 patch.object(cache, 'output', side_effect=output), \
                 patch.object(cache.subprocess, 'run', side_effect=run), \
                 patch.object(cache.subprocess, 'Popen', return_value=process), \
                 contextlib.redirect_stdout(io.StringIO()):
                cache.main()
            metrics = json.loads((Path(directory) / 'orchestration-cache-metrics.json').read_text())
            if operation == 'produce':
                manifest = json.loads((Path(directory) / 'orchestration-cache/manifest.json').read_text())
                self.assertEqual(manifest['root'], root)
                self.assertGreater(metrics['archiveBytes'], 0)
            if hit:
                self.assertEqual('cacheImportError' in metrics, corruption is not None)
                if corruption is None:
                    self.assertGreater(metrics['archiveBytes'], 0)
            return commands

    def test_producer_realizes_missing_shell_output_before_inventory(self):
        self.measure('produce')

    def test_cold_consumer_only_realizes_the_environment(self):
        commands = self.measure('baseline')
        self.assertFalse(any(command[:2] == ['nix', 'build'] for command in commands))

    def test_cache_service_absence_still_requires_complete_realization(self):
        commands = self.measure('restore')
        self.assertEqual([command[:2] for command in commands], [['nix', 'develop']])

    def test_valid_warm_import_still_requires_complete_realization(self):
        commands = self.measure('restore', hit=True)
        self.assertEqual([command[:2] for command in commands], [['nix', 'develop']])

    def test_rejected_inputs_digest_and_compression_do_not_skip_realization(self):
        for corruption in ['inputs', 'digest', 'compression']:
            with self.subTest(corruption=corruption):
                commands = self.measure('restore', hit=True, corruption=corruption)
                self.assertEqual([command[:2] for command in commands], [['nix', 'develop']])

    def test_malformed_metadata_types_still_require_realization(self):
        for corruption in ['paths-type', 'bytes-type']:
            with self.subTest(corruption=corruption):
                commands = self.measure('restore', hit=True, corruption=corruption)
                self.assertEqual([command[:2] for command in commands], [['nix', 'develop']])

if __name__ == '__main__':
    unittest.main()
