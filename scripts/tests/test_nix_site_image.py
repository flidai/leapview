import copy
import io
import json
from pathlib import Path
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import check_nix_site_image as site


class SiteRuntimeTests(unittest.TestCase):
    def setUp(self):
        self.artifact = {'platform': 'linux/amd64', 'version': '1.0',
                         'layerDiffIDs': ['sha256:' + 'd' * 64]}
        self.revision = 'a' * 40
        self.image = {'Id': 'sha256:' + 'b' * 64, 'Os': 'linux', 'Architecture': 'amd64',
                      'RootFS': {'Layers': self.artifact['layerDiffIDs']},
                      'Config': {'User': '65532:65532', 'Entrypoint': ['/leapview-site'],
                                 'Cmd': ['-addr=:8081'], 'WorkingDir': '/',
                                 'ExposedPorts': {'8081/tcp': {}}, 'Healthcheck': None,
                                 'Env': ['LEAPVIEW_SITE_BASE_URL=',
                                         'LEAPVIEW_SITE_SHOWCASE_EMBED_URL='],
                                 'Labels': {'org.opencontainers.image.revision': self.revision,
                                            'org.opencontainers.image.version': '1.0',
                                            'dev.leapview.build.kind': 'site-image'}}}
        self.release = {'version': '1.0', 'tag': 'v1.0', 'revision': 'c' * 40,
                        'image': 'ghcr.io/flidai/leapview@sha256:' + 'e' * 64,
                        'releaseUrl': 'https://example.test/release',
                        'artifacts': [{'archiveUrl': 'https://example.test/archive',
                                       'checksumUrl': 'https://example.test/checksum'}]}

    @staticmethod
    def write_saved_image(path, config_path='blobs/sha256/site-config', config=b'{"runtime":true}',
                          extra_members=(), manifest_images=None):
        manifest = [{'Config': config_path, 'Layers': []}] if manifest_images is None else manifest_images
        members = [('manifest.json', json.dumps(manifest).encode()),
                   (config_path, config), *extra_members]
        with tarfile.open(path, 'w') as archive:
            for name, data in members:
                info = tarfile.TarInfo(name)
                info.size = len(data)
                archive.addfile(info, io.BytesIO(data))
        return site.candidate.digest_bytes(config)

    def test_saved_image_config_binds_generic_manifest_path_to_candidate_digest(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'docker-save.tar'
            digest = self.write_saved_image(path, config_path='content/config.json')
            self.assertEqual(site.saved_config_digest(path, digest), digest)
            with self.assertRaisesRegex(ValueError, 'differs from the verified candidate'):
                site.saved_config_digest(path, 'sha256:' + 'f' * 64)

    def test_saved_image_manifest_and_members_are_strictly_bounded(self):
        cases = [
            ('../config.json', b'{}', (), None),
            ('config.json', b'{}', (('manifest.json', b'[]'),), None),
            ('config.json', b'{}', (), [{'Config': 'config.json'}, {'Config': 'config.json'}]),
        ]
        for config_path, config, extra, manifest in cases:
            with self.subTest(config_path=config_path, manifest=manifest), tempfile.TemporaryDirectory() as directory:
                path = Path(directory) / 'docker-save.tar'
                self.write_saved_image(path, config_path, config, extra, manifest)
                with self.assertRaises(ValueError):
                    site.saved_config_digest(path, site.candidate.digest_bytes(config))

    def test_saved_image_byte_and_member_limits_are_enforced(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'docker-save.tar'
            digest = self.write_saved_image(path)
            with patch.object(site, 'MAX_SAVED_IMAGE_BYTES', 10):
                with self.assertRaisesRegex(ValueError, 'byte limit'):
                    site.saved_config_digest(path, digest)
            with patch.object(site.binary_evidence, 'MAX_ENTRIES', 1):
                with self.assertRaisesRegex(ValueError, 'member limit'):
                    site.saved_config_digest(path, digest)

    def test_save_uses_selected_immutable_reference_and_checks_the_result(self):
        with tempfile.TemporaryDirectory() as directory:
            expected = self.write_saved_image(Path(directory) / 'expected.tar')
            artifact = {'configDigest': expected}
            calls = []

            def run(*args):
                calls.append(args)
                self.write_saved_image(args[4])
                return ''

            image_id = 'sha256:' + 'a' * 64
            with patch.object(site, 'run', side_effect=run):
                self.assertEqual(site.verify_saved_image_config(image_id, artifact), expected)
            self.assertEqual(calls[0][:4], ('docker', 'image', 'save', '--output'))
            self.assertEqual(calls[0][-1], image_id)

    def test_foreign_platform_or_substituted_layers_fail_before_execution(self):
        site.validate_image(self.image, self.artifact, self.revision)
        for key, value in [('Architecture', 'arm64'), ('RootFS', {'Layers': ['wrong']})]:
            image = copy.deepcopy(self.image)
            image[key] = value
            with self.subTest(key=key), self.assertRaises(ValueError):
                site.validate_image(image, self.artifact, self.revision)

    def test_root_user_and_wrong_source_are_rejected(self):
        for mutate in [lambda c: c.update(User='0'),
                       lambda c: c['Labels'].update({'org.opencontainers.image.revision': 'f' * 40})]:
            image = copy.deepcopy(self.image)
            mutate(image['Config'])
            with self.assertRaises(ValueError):
                site.validate_image(image, self.artifact, self.revision)

    def test_loaded_image_runtime_config_must_match_the_archive_contract(self):
        mutations = [
            ('Cmd', ['-addr=:8082']),
            ('Env', ['LEAPVIEW_SITE_BASE_URL=https://wrong.example',
                     'LEAPVIEW_SITE_SHOWCASE_EMBED_URL=']),
            ('Env', ['LEAPVIEW_SITE_BASE_URL=',
                     'LEAPVIEW_SITE_SHOWCASE_EMBED_URL=', 'EXTRA=value']),
            ('WorkingDir', '/tmp'),
            ('ExposedPorts', {'8082/tcp': {}}),
            ('Healthcheck', {'Test': ['CMD', '/leapview-site', 'healthcheck']}),
        ]
        for key, value in mutations:
            image = copy.deepcopy(self.image)
            image['Config'][key] = value
            with self.subTest(key=key, value=value), self.assertRaises(ValueError):
                site.validate_image(image, self.artifact, self.revision)

    def test_release_build_and_installation_must_agree_with_exact_source(self):
        release = json.dumps(self.release).encode()
        image = self.image['Id']
        build = json.dumps({'schemaVersion': 1, 'revision': self.revision, 'image': image}).encode()
        installation = ' '.join(site.release_values(self.release)).encode()
        site.validate_responses(release, release, build, installation, self.revision, image)
        for args in [(release + b' ', release, build, installation),
                     (release, release, build.replace(self.revision.encode(), b'f' * 40), installation),
                     (release, release, build, installation.replace(b'https://example.test/checksum', b''))]:
            with self.assertRaises(ValueError):
                site.validate_responses(*args, self.revision, image)

    def test_native_machine_is_required(self):
        with patch.object(site.platform, 'machine', return_value='aarch64'):
            self.assertEqual(site.native_platform(), 'linux/arm64')
        with patch.object(site.platform, 'machine', return_value='riscv64'):
            with self.assertRaises(ValueError):
                site.native_platform()

    def test_container_is_removed_when_http_validation_fails(self):
        calls = []
        def run(*args):
            calls.append(args)
            if args[:3] == ('docker', 'network', 'create'):
                return '2' * 64
            if args[:2] == ('docker', 'create'):
                return '1' * 64
            if args[:2] == ('docker', 'inspect'):
                return json.dumps([{'Config': {'User': '65532:65532'},
                                   'HostConfig': {'ReadonlyRootfs': True},
                                   'NetworkSettings': {'Ports': {'8081/tcp': [
                                       {'HostIp': '127.0.0.1', 'HostPort': '12345'}]}}}])
            return ''
        with patch.object(site, 'run', side_effect=run), patch.object(
                site, 'fetch', side_effect=ValueError('invalid response')):
            with self.assertRaisesRegex(ValueError, 'invalid response'):
                site.exercise(self.image['Id'], self.artifact, self.revision, b'{}')
        invocation = next(args for args in calls if args[:2] == ('docker', 'create'))
        self.assertIn('--read-only', invocation)
        self.assertIn('--cap-drop=ALL', invocation)
        self.assertIn('--security-opt=no-new-privileges', invocation)
        self.assertIn(('docker', 'rm', '--force', '1' * 64), calls)
        self.assertIn(('docker', 'network', 'rm', '2' * 64), calls)

    def test_startup_failure_cleans_the_created_container_and_private_network(self):
        calls = []
        def run(*args):
            calls.append(args)
            if args[:3] == ('docker', 'network', 'create'):
                return '2' * 64
            if args[:2] == ('docker', 'create'):
                return '1' * 64
            if args[:2] == ('docker', 'start'):
                raise ValueError('container startup failed')
            return ''
        with patch.object(site, 'run', side_effect=run):
            with self.assertRaisesRegex(ValueError, 'startup failed'):
                site.exercise(self.image['Id'], self.artifact, self.revision, b'{}')
        self.assertIn(('docker', 'rm', '--force', '1' * 64), calls)
        self.assertIn(('docker', 'network', 'rm', '2' * 64), calls)


if __name__ == '__main__':
    unittest.main()
