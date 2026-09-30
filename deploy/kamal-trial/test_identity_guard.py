import unittest

from identity_guard import validate, validate_cleanup_scope, verified_noop


class IdentityGuardTests(unittest.TestCase):
    def setUp(self):
        self.record = {'schema': 1, 'service': 'leapview-site-trial', 'version': 'good-01',
                       'status': 'verified', 'reference': '127.0.0.1:5000/site@sha256:' + 'a' * 64,
                       'image_id': 'sha256:' + 'b' * 64}
        self.image = {'Id': self.record['image_id'], 'RepoDigests': [self.record['reference']],
                      'Os': 'linux', 'Architecture': 'amd64',
                      'Config': {'Labels': {'service': self.record['service']}}}

    def test_accepts_verified_local_identity_without_registry(self):
        self.assertEqual(validate(self.record, self.image, rollback=True), self.record)

    def test_rejects_tag_substitution_missing_digest_platform_and_service(self):
        for key, value in [('Id', 'sha256:' + 'c' * 64), ('RepoDigests', []),
                           ('Architecture', 'arm64'), ('Os', 'windows'), ('Config', {})]:
            with self.subTest(key=key), self.assertRaises(ValueError):
                validate(self.record, {**self.image, key: value})

    def test_rejects_unverified_rollback_and_corrupt_records(self):
        for key, value in [('status', 'candidate'), ('schema', 2), ('version', '../bad'),
                           ('reference', '127.0.0.1:5000/site:mutable'), ('image_id', '')]:
            with self.subTest(key=key), self.assertRaises(ValueError):
                validate({**self.record, key: value}, self.image, rollback=True)

    def test_foreign_aliases_block_cleanup_but_caddy_is_not_owned(self):
        validate_cleanup_scope([self.image, {'Config': {'Labels': {}}, 'RepoTags': ['caddy:2']}])
        for key, alias in [('RepoTags', 'other/site:latest'), ('RepoDigests', 'other/site@sha256:' + 'c' * 64)]:
            with self.subTest(key=key), self.assertRaises(ValueError):
                validate_cleanup_scope([{**self.image, key: [alias]}])

    def test_noop_requires_matching_runtime_not_only_same_tag(self):
        container = {'State': {'Running': True}, 'Image': self.record['image_id'],
                     'Config': {'User': '65532:65532', 'Cmd': ['/fixture'],
                                'Env': ['TRIAL_IMAGE_REFERENCE=' + self.record['reference']]},
                     'HostConfig': {'ReadonlyRootfs': True, 'CapDrop': ['ALL'],
                                    'SecurityOpt': ['no-new-privileges=true']}}
        served = {'version': self.record['version'], 'image_reference': self.record['reference']}
        self.assertTrue(verified_noop(self.record, self.image, container, served))
        self.assertFalse(verified_noop(self.record, self.image, container, {**served, 'image_reference': 'wrong'}))
        for key, value in [('State', {'Running': False}), ('HostConfig', {}), ('Config', {}), ('Image', 'wrong')]:
            with self.subTest(key=key):
                self.assertFalse(verified_noop(self.record, self.image, {**container, key: value}, served))


if __name__ == '__main__':
    unittest.main()
