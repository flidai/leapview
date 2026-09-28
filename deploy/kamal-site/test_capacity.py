from unittest import TestCase
from unittest.mock import patch

import host
from test_contract import record


class CapacityTest(TestCase):
    def test_capacity_and_size_envelope_fail_before_pull(self):
        r = dict(record(), compressed_bytes=100)
        state = {'active': 'previous', 'records': {'previous': record()}}
        budget = {'paths': ['/var/lib/docker', '/var/lib/containerd'], 'measured_peak_bytes': 1000, 'measured_peak_inodes': 20,
                  'candidate_headroom_bytes': 1500, 'reserve_bytes': 2*1024**3,
                  'reserve_inodes': 10000, 'qualified_compressed_bytes': 100}
        available = {'capacity_bytes': 10*1024**3, 'available_bytes': 3*1024**3,
                     'available_inodes': 20000, 'paths': ['/var/lib/docker', '/var/lib/containerd']}
        def check(b, a, candidate=r):
            with patch.object(host, 'running'), patch.object(host, 'scope'), \
                    patch.object(host, 'inventory', return_value={'docker': '29.1.3', 'driver': 'overlayfs', 'disks': {'1': a}}), \
                    patch.object(host, 'command') as mutation:
                try: return host.preflight({'capacity': {'1': b}}, state, candidate)
                finally: mutation.assert_not_called()
        check(budget, available)
        for field, value in [('available_bytes', 2*1024**3), ('available_inodes', 10019)]:
            with self.subTest(field=field), self.assertRaisesRegex(ValueError, 'insufficient'):
                check(budget, dict(available, **{field: value}))
        with self.assertRaisesRegex(ValueError, 'envelope'):
            check(budget, available, dict(r, compressed_bytes=101))
        for field in ('candidate_headroom_bytes', 'reserve_bytes', 'reserve_inodes'):
            with self.subTest(field=field), self.assertRaisesRegex(ValueError, 'margins'):
                check(dict(budget, **{field: 1}), available)
