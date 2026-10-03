import hashlib
import importlib.util
import json
from pathlib import Path
import sys
import unittest


sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
spec = importlib.util.spec_from_file_location(
    'release_image_promotion', Path(__file__).resolve().parents[1] / 'release_image_promotion.py')
promotion = importlib.util.module_from_spec(spec)
spec.loader.exec_module(promotion)

REPOSITORY = 'ghcr.io/flidai/leapview'
REVISION = 'a' * 40
CANDIDATE_DIGEST = 'sha256:' + 'c' * 64


def image_index(version, *, arm_version=None, marker='1'):
    manifests = []
    configs = {}
    for architecture, label_version in [('amd64', version), ('arm64', arm_version or version)]:
        digest = 'sha256:' + marker * 63 + ('d' if architecture == 'amd64' else 'e')
        manifests.append({
            'mediaType': 'application/vnd.oci.image.manifest.v1+json',
            'digest': digest,
            'size': 123,
            'platform': ({'architecture': architecture, 'os': 'linux', 'variant': 'v8'}
                         if architecture == 'arm64' else
                         {'architecture': architecture, 'os': 'linux'}),
        })
        configs[f'{REPOSITORY}@{digest}'] = json.dumps({
            'os': 'linux', 'architecture': architecture,
            'config': {'Labels': {promotion.VERSION_LABEL: label_version}},
        }).encode()
    manifests.append({
        'mediaType': 'application/vnd.oci.image.manifest.v1+json',
        'digest': 'sha256:' + 'b' * 64,
        'size': 77,
        'annotations': {
            'vnd.docker.reference.type': 'attestation-manifest',
            'vnd.docker.reference.digest': 'sha256:' + 'a' * 64,
        },
        'platform': {'architecture': 'unknown', 'os': 'unknown'},
    })
    raw = json.dumps({
        'schemaVersion': 2,
        'mediaType': 'application/vnd.oci.image.index.v1+json',
        'manifests': manifests,
    }, separators=(',', ':')).encode()
    return raw, configs


def manifest_digest(raw):
    return 'sha256:' + hashlib.sha256(raw).hexdigest()


class Completed:
    def __init__(self, stdout=b'', stderr=b'', returncode=0):
        self.stdout = stdout
        self.stderr = stderr
        self.returncode = returncode


class FakeDocker:
    def __init__(self):
        self.manifests = {}
        self.configs = {}
        self.calls = []

    def missing(self, reference):
        return Completed(stderr=f'ERROR: no such manifest: {reference}'.encode(), returncode=1)

    def __call__(self, command, *, capture_output):
        self.calls.append(command)
        if command[:5] == ['docker', 'buildx', 'imagetools', 'inspect', '--raw']:
            reference = command[5]
            return self.manifests.get(reference, self.missing(reference))
        if command[:6] == ['docker', 'buildx', 'imagetools', 'inspect', '--format', '{{json .Image}}']:
            reference = command[6]
            if reference not in self.configs:
                return Completed(stderr=b'config blob unavailable', returncode=1)
            return Completed(stdout=self.configs[reference])
        if command[:4] == ['docker', 'buildx', 'imagetools', 'create']:
            return Completed()
        raise AssertionError(f'unexpected command: {command}')


class PromotionTests(unittest.TestCase):
    def test_plan_uses_numeric_frontiers_independently(self):
        tags = promotion.plan_tags(
            '2.3.2', REVISION, CANDIDATE_DIGEST,
            version_digest=None,
            latest_version='2.3.0', major_minor_version='2.3.5')
        self.assertEqual(tags, [f'sha-{REVISION[:7]}', '2.3.2', 'latest'])

    def test_plan_compares_numeric_core_components(self):
        tags = promotion.plan_tags(
            '2.10.0', REVISION, CANDIDATE_DIGEST,
            version_digest=None,
            latest_version='2.9.9', major_minor_version=None)
        self.assertEqual(tags, [f'sha-{REVISION[:7]}', '2.10.0', 'latest', '2.10'])

    def test_older_release_keeps_latest_but_can_create_its_minor_alias(self):
        tags = promotion.plan_tags(
            '2.3.9', REVISION, CANDIDATE_DIGEST,
            version_digest=None,
            latest_version='2.4.0', major_minor_version=None)
        self.assertEqual(tags, [f'sha-{REVISION[:7]}', '2.3.9', '2.3'])

    def test_prerelease_only_publishes_version_and_revision_tags(self):
        tags = promotion.plan_tags(
            '2.4.0-rc.1', REVISION, CANDIDATE_DIGEST,
            version_digest=None,
            latest_version=None, major_minor_version=None)
        self.assertEqual(tags, [f'sha-{REVISION[:7]}', '2.4.0-rc.1'])

    def test_version_tag_is_immutable_and_revision_alias_is_retained(self):
        with self.assertRaisesRegex(promotion.PromotionError, 'immutable version'):
            promotion.plan_tags('2.4.0', REVISION, CANDIDATE_DIGEST,
                                version_digest='sha256:' + 'd' * 64,
                                latest_version=None, major_minor_version=None)
        tags = promotion.plan_tags('2.4.0', REVISION, CANDIDATE_DIGEST,
                                   version_digest=CANDIDATE_DIGEST,
                                   latest_version=None, major_minor_version=None)
        self.assertEqual(tags, [f'sha-{REVISION[:7]}', 'latest', '2.4'])

    def test_buildx_inspects_raw_index_and_config_only_for_exact_platform_digests(self):
        raw, configs = image_index('2.4.0')
        docker = FakeDocker()
        docker.manifests[f'{REPOSITORY}@{manifest_digest(raw)}'] = Completed(stdout=raw)
        docker.configs.update(configs)
        registry = promotion.DockerRegistry(run=docker)

        manifest = registry.manifest(f'{REPOSITORY}@{manifest_digest(raw)}')
        self.assertEqual(manifest.digest, manifest_digest(raw))
        self.assertEqual(registry.manifest_version(REPOSITORY, manifest), '2.4.0')
        config_calls = [call for call in docker.calls if '--format' in call]
        self.assertEqual(len(config_calls), 2)
        self.assertTrue(all(call[5] == '{{json .Image}}' for call in config_calls))
        self.assertTrue(all('@sha256:' in call[6] for call in config_calls))

    def test_only_explicit_missing_manifest_is_an_absent_frontier(self):
        reference = f'{REPOSITORY}:latest'
        self.assertTrue(promotion._confirmed_missing(f'ERROR: no such manifest: {reference}', reference))
        self.assertTrue(promotion._confirmed_missing(f'ERROR: {reference}: not found', reference))
        for stderr in ['ERROR: denied', 'ERROR: network timeout',
                       f'ERROR: failed to fetch {reference}: not found',
                       'ERROR: manifest unknown']:
            with self.subTest(stderr=stderr):
                self.assertFalse(promotion._confirmed_missing(stderr, reference))

    def test_promotion_reads_every_frontier_before_publishing_tags(self):
        raw, configs = image_index('2.4.0')
        candidate_digest = manifest_digest(raw)
        docker = FakeDocker()
        docker.manifests[f'{REPOSITORY}@{candidate_digest}'] = Completed(stdout=raw)
        docker.configs.update(configs)

        tags = promotion.promote(
            promotion.DockerRegistry(run=docker), REPOSITORY,
            f'{REPOSITORY}@{candidate_digest}', candidate_digest, '2.4.0', REVISION)
        self.assertEqual(tags, [f'sha-{REVISION[:7]}', '2.4.0', 'latest', '2.4'])
        create_index = next(i for i, call in enumerate(docker.calls) if call[3] == 'create')
        self.assertEqual(create_index, len(docker.calls) - 1)
        self.assertEqual(docker.calls[create_index][-1], f'{REPOSITORY}@{candidate_digest}')
        self.assertEqual([docker.calls[create_index][i + 1]
                          for i, value in enumerate(docker.calls[create_index]) if value == '--tag'],
                         [f'{REPOSITORY}:{tag}' for tag in tags])

    def test_candidate_manifest_digest_must_match_the_admitted_reference(self):
        raw, _ = image_index('2.4.0')
        expected_digest = 'sha256:' + 'd' * 64
        docker = FakeDocker()
        docker.manifests[f'{REPOSITORY}@{expected_digest}'] = Completed(stdout=raw)
        with self.assertRaisesRegex(promotion.PromotionError, 'different digest'):
            promotion.promote(
                promotion.DockerRegistry(run=docker), REPOSITORY,
                f'{REPOSITORY}@{expected_digest}', expected_digest, '2.4.0', REVISION)
        self.assertFalse(any(call[3] == 'create' for call in docker.calls))

    def test_existing_alias_labels_control_their_own_frontier(self):
        candidate_raw, candidate_configs = image_index('2.4.2', marker='1')
        latest_raw, latest_configs = image_index('2.4.0', marker='2')
        minor_raw, minor_configs = image_index('2.4.5', marker='3')
        candidate_digest = manifest_digest(candidate_raw)
        docker = FakeDocker()
        docker.manifests.update({
            f'{REPOSITORY}@{candidate_digest}': Completed(stdout=candidate_raw),
            f'{REPOSITORY}:latest': Completed(stdout=latest_raw),
            f'{REPOSITORY}:2.4': Completed(stdout=minor_raw),
            f'{REPOSITORY}:sha-{REVISION[:7]}': Completed(stdout=latest_raw),
        })
        docker.configs.update(candidate_configs | latest_configs | minor_configs)

        tags = promotion.promote(
            promotion.DockerRegistry(run=docker), REPOSITORY,
            f'{REPOSITORY}@{candidate_digest}', candidate_digest, '2.4.2', REVISION)
        self.assertEqual(tags, [f'sha-{REVISION[:7]}', '2.4.2', 'latest'])
        self.assertFalse(any(call[:5] == ['docker', 'buildx', 'imagetools', 'inspect', '--raw']
                             and call[-1] == f'{REPOSITORY}:sha-{REVISION[:7]}'
                             for call in docker.calls))

    def test_inconsistent_platform_versions_fail_before_any_tag_write(self):
        raw, configs = image_index('2.4.0', arm_version='2.3.9')
        candidate_digest = manifest_digest(raw)
        docker = FakeDocker()
        docker.manifests[f'{REPOSITORY}@{candidate_digest}'] = Completed(stdout=raw)
        docker.configs.update(configs)
        with self.assertRaisesRegex(promotion.PromotionError, 'versions differ'):
            promotion.promote(
                promotion.DockerRegistry(run=docker), REPOSITORY,
                f'{REPOSITORY}@{candidate_digest}', candidate_digest, '2.4.0', REVISION)
        self.assertFalse(any(call[3] == 'create' for call in docker.calls))

    def test_unreadable_alias_and_missing_label_fail_closed_before_publish(self):
        raw, configs = image_index('2.4.0')
        candidate_digest = manifest_digest(raw)
        for failure in [
            Completed(stderr=b'ERROR: denied: requested access', returncode=1),
            Completed(stderr=b'ERROR: network timeout', returncode=1),
            Completed(stdout=b'{broken json'),
        ]:
            docker = FakeDocker()
            docker.manifests[f'{REPOSITORY}@{candidate_digest}'] = Completed(stdout=raw)
            docker.configs.update(configs)
            docker.manifests[f'{REPOSITORY}:latest'] = failure
            with self.subTest(failure=failure), self.assertRaises(promotion.PromotionError):
                promotion.promote(
                    promotion.DockerRegistry(run=docker), REPOSITORY,
                    f'{REPOSITORY}@{candidate_digest}', candidate_digest, '2.4.0', REVISION)
            self.assertFalse(any(call[3] == 'create' for call in docker.calls))

        docker = FakeDocker()
        docker.manifests[f'{REPOSITORY}@{candidate_digest}'] = Completed(stdout=raw)
        docker.configs.update(configs)
        first_config = next(iter(docker.configs))
        docker.configs[first_config] = json.dumps({
            'os': 'linux', 'architecture': 'amd64', 'config': {'Labels': {}}
        }).encode()
        with self.assertRaisesRegex(promotion.PromotionError, 'missing org.opencontainers'):
            promotion.promote(
                promotion.DockerRegistry(run=docker), REPOSITORY,
                f'{REPOSITORY}@{candidate_digest}', candidate_digest, '2.4.0', REVISION)
        self.assertFalse(any(call[3] == 'create' for call in docker.calls))

        latest_raw, latest_configs = image_index('2.4.1', marker='2')
        docker = FakeDocker()
        docker.manifests.update({
            f'{REPOSITORY}@{candidate_digest}': Completed(stdout=raw),
            f'{REPOSITORY}:latest': Completed(stdout=latest_raw),
        })
        docker.configs.update(configs | latest_configs)
        arm_config = f'{REPOSITORY}@' + 'sha256:' + '2' * 63 + 'e'
        docker.configs[arm_config] = json.dumps({
            'os': 'linux', 'architecture': 'arm64', 'config': {'Labels': {}}
        }).encode()
        with self.assertRaisesRegex(promotion.PromotionError, 'missing org.opencontainers'):
            promotion.promote(
                promotion.DockerRegistry(run=docker), REPOSITORY,
                f'{REPOSITORY}@{candidate_digest}', candidate_digest, '2.4.0', REVISION)
        self.assertFalse(any(call[3] == 'create' for call in docker.calls))


if __name__ == '__main__':
    unittest.main()
