import unittest

from identity_guard import validate


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


if __name__ == '__main__':
    unittest.main()
