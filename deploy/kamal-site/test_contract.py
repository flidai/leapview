import copy
import unittest

from contract import activation_allowed, validate_record, validate_image, cleanup_candidates


def record():
    return {'schema': 1, 'version': 'k' + 'a' * 64, 'revision': 'b' * 40,
            'image': 'ghcr.io/flidai/leapview-site@sha256:' + 'a' * 64,
            'platform': 'sha256:' + 'c' * 64, 'config': 'sha256:' + 'd' * 64,
            'kamal': '2.12.0', 'runtime': {'port': 8081, 'user': '65532:65532',
                'base_url': 'https://leapview.dev', 'tmpfs_mib': 64, 'log_size': '10m', 'log_files': 3}}


class ContractTest(unittest.TestCase):
    def test_activation_is_default_off(self):
        for mode in ('', 'legacy', 'paused', 'KAMAL', None):
            self.assertFalse(activation_allowed(mode, 'flidai/leapview', 'refs/heads/main'))
        self.assertTrue(activation_allowed('kamal', 'flidai/leapview', 'refs/heads/main'))
        self.assertFalse(activation_allowed('kamal', 'fork/leapview', 'refs/heads/main'))
        self.assertFalse(activation_allowed('kamal', 'flidai/leapview', 'refs/heads/feature'))

    def test_record_binds_digest_and_version(self):
        validate_record(record())
        for field, value in [('version', 'k' + 'f' * 64), ('image', 'ghcr.io/flidai/leapview-site:latest'),
                             ('revision', 'main'), ('kamal', 'latest'), ('platform', 'sha256:no')]:
            bad = record(); bad[field] = value
            with self.assertRaises(ValueError): validate_record(bad)
        bad = record(); bad['runtime']['base_url'] = 'https://elsewhere.test'
        with self.assertRaises(ValueError): validate_record(bad)

    def test_image_identity_and_ownership(self):
        r = record()
        image = {'RepoDigests': [r['image']], 'RepoTags': ['ghcr.io/flidai/leapview-site:' + r['version']],
                 'Os': 'linux', 'Architecture': 'amd64',
                 'Config': {'Labels': {'service': 'leapview-site', 'org.opencontainers.image.revision': r['revision']}}}
        validate_image(r, image)
        for key, value in [('Architecture', 'arm64'), ('RepoDigests', []), ('RepoTags', ['foreign:keep'])]:
            bad = copy.deepcopy(image); bad[key] = value
            with self.assertRaises(ValueError): validate_image(r, bad)

    def test_cleanup_protects_distinct_verified_prior(self):
        current, prior, rejected = ('k' + x * 64 for x in 'abc')
        def c(cid, version, running=False):
            return {'Id': cid, 'Name': '/leapview-site-web-' + version, 'Image': version,
                    'State': {'Running': running}, 'Created': cid,
                    'Config': {'Labels': {'service': 'leapview-site'}}}
        containers = [c('1', prior), c('2', current), c('3', rejected), c('4', current, True)]
        known = {v: {'local_id': v} for v in (current, prior, rejected)}
        self.assertEqual(cleanup_candidates(containers, known, current, prior), ['2', '3'])
        with self.assertRaises(ValueError): cleanup_candidates(containers, {}, current, prior)
        with self.assertRaises(ValueError): cleanup_candidates(containers[1:], known, current, prior)
        containers[2]['State']['Running'] = True
        with self.assertRaises(ValueError): cleanup_candidates(containers, known, current, prior)


if __name__ == '__main__': unittest.main()
