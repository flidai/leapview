import copy
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from test_contract import record
import capacity_contract as capacity


class CapacityTests(unittest.TestCase):
    def inputs(self):
        candidate = record()
        active, prior = copy.deepcopy(candidate), copy.deepcopy(candidate)
        for current, digit in [(active, '2'), (prior, '3')]:
            current['image'] = current['image'].replace('a' * 64, digit * 64)
            current['version'] = 'k' + digit * 64
        state = {'active': active['version'], 'prior': prior['version'],
                 'pending': None, 'maintenance_pending': None,
                 'records': {r['version']: r for r in [active, prior]}}
        budget = {'paths': ['/data'], 'qualified_images': [active['image'], prior['image']],
                  'measured_peak_bytes': 10, 'measured_peak_inodes': 10,
                  'candidate_headroom_bytes': 15, 'reserve_bytes': 2 * 1024**3,
                  'reserve_inodes': 10000, 'qualified_compressed_bytes': 1}
        baseline = {'state': state, 'handover': {'capacity': {'1': budget}}}
        candidate['compressed_bytes'] = active['compressed_bytes'] = prior['compressed_bytes'] = 5
        raw_candidate, raw_baseline = (json.dumps(v).encode() for v in [candidate, baseline])
        samples = [{'time': 1, 'available_bytes': 1000, 'available_inodes': 1000},
                   {'time': 2, 'available_bytes': 800, 'available_inodes': 900}]
        report = {'schema': 1, 'passed': True, 'production_changed': False,
                  'candidate': candidate['image'], 'candidate_revision': candidate['revision'],
                  'candidate_record_sha256': hashlib.sha256(raw_candidate).hexdigest(),
                  'baseline_sha256': hashlib.sha256(raw_baseline).hexdigest(),
                  'runtime_artifacts': capacity.RUNTIME_ARTIFACTS,
                  'runtime': {'docker': '29.1.3', 'driver': 'overlayfs', 'containerd': 'containerd github.com/containerd/containerd/v2 v2.2.1 abc'},
                  'qualified_images': [r['image'] for r in [candidate, active, prior]],
                  'samples': samples, 'measured_peak_bytes': 200, 'measured_peak_inodes': 100,
                  'qualified_compressed_bytes': 5, 'lifecycle': capacity.LIFECYCLE,
                  'checks': [{'image': r['image'], 'selected_config_verified': True} for r in [candidate, active, prior]] +
                            [{'image': r['image'], 'build_identity': True, 'health': True,
                              'routes': capacity.ROUTES} for r in [active, candidate, active, candidate]]}
        return raw_candidate, raw_baseline, report

    def test_report_recomputes_exact_measurement_and_binds_retained_images(self):
        candidate, baseline, report = self.inputs()
        payload = capacity.registration_payload(candidate, baseline, json.dumps(report).encode())
        self.assertEqual(payload['bytes'], 200)
        self.assertEqual(payload['inodes'], 100)
        for key, value in [('passed', False), ('production_changed', True),
                           ('candidate_revision', 'd' * 40), ('baseline_sha256', '0' * 64),
                           ('measured_peak_bytes', 201), ('measured_peak_inodes', True),
                           ('qualified_images', report['qualified_images'][:2]),
                           ('checks', report['checks'][:-1]), ('lifecycle', capacity.LIFECYCLE[:-1]),
                           ('runtime_artifacts', {})]:
            changed = copy.deepcopy(report)
            changed[key] = value
            with self.subTest(key=key), self.assertRaises(ValueError):
                capacity.registration_payload(candidate, baseline, json.dumps(changed).encode())

    def test_pending_or_same_image_cannot_supply_cold_measurement(self):
        candidate_raw, baseline_raw, _ = self.inputs()
        candidate, baseline = json.loads(candidate_raw), json.loads(baseline_raw)
        for key in ['pending', 'maintenance_pending']:
            changed = copy.deepcopy(baseline)
            changed['state'][key] = 'an owned unfinished attempt'
            with self.subTest(key=key), self.assertRaises(ValueError):
                capacity.validate_inputs(candidate, changed)
        baseline['state']['prior'] = baseline['state']['active']
        with self.assertRaises(ValueError):
            capacity.validate_inputs(candidate, baseline)

    def test_supervised_registration_cas_and_atomic_budget_update(self):
        candidate, baseline_raw, report = self.inputs()
        baseline = json.loads(baseline_raw)
        payload = capacity.registration_payload(candidate, baseline_raw, json.dumps(report).encode())
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            ready, state = root / 'ready.json', root / 'state.json'
            ready.write_text(json.dumps(baseline['handover']))
            state.write_text(json.dumps(baseline['state']))
            ready.chmod(0o600)
            state.chmod(0o600)
            stats = type('Stats', (), {'f_blocks': 10000000, 'f_frsize': 4096,
                                      'f_bavail': 9000000, 'f_favail': 100000})()
            device = type('Device', (), {'st_dev': 1})()
            with patch.object(capacity.os, 'statvfs', return_value=stats), \
                    patch.object(capacity.os, 'stat', return_value=device), \
                    patch.object(capacity.os, 'geteuid', return_value=0), \
                    patch.object(capacity.Path, 'lstat', return_value=type('Meta', (), {'st_mode': 0o100700, 'st_uid': 0})()):
                changed = dict(payload, active='changed')
                before = ready.read_bytes()
                with self.assertRaises(ValueError):
                    capacity.register(root, changed)
                self.assertEqual(before, ready.read_bytes())
                result = capacity.register(root, payload)
                budget = json.loads(ready.read_text())['capacity']['1']
                self.assertEqual(budget['measured_peak_bytes'], 200)
                self.assertEqual(budget['candidate_headroom_bytes'], 300)
                self.assertIn(payload['candidate'], budget['qualified_images'])
                self.assertTrue(result['state_unchanged'])
                self.assertEqual(json.loads(state.read_text()), baseline['state'])
                with self.assertRaises(ValueError):
                    capacity.register(root, payload)

    def test_failed_disposable_daemon_is_unmounted_and_cannot_pass(self):
        import capacity_measure
        candidate_raw, baseline_raw, _ = self.inputs()
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            candidate, baseline = root / 'candidate.json', root / 'baseline.json'
            candidate.write_bytes(candidate_raw)
            baseline.write_bytes(baseline_raw)
            output = root / 'proof'
            stats = type('Stats', (), {'f_bavail': 20 * 1024**3, 'f_frsize': 1})()
            with patch.object(capacity_measure, 'verify_runtime', return_value=capacity.RUNTIME_ARTIFACTS), \
                    patch.object(capacity_measure.os, 'geteuid', return_value=0), \
                    patch.object(capacity_measure.os, 'readlink', side_effect=['private', 'original']), \
                    patch.object(capacity_measure.os, 'statvfs', return_value=stats), \
                    patch.object(capacity_measure.subprocess, 'run') as command, \
                    patch.object(capacity_measure.subprocess, 'Popen', side_effect=RuntimeError('denied daemon')), \
                    patch('sys.argv', ['capacity_measure', '--candidate', str(candidate),
                                      '--baseline', str(baseline), '--runtime', str(root), '--output', str(output)]):
                with self.assertRaisesRegex(RuntimeError, 'denied daemon'):
                    capacity_measure.main()
            report = json.loads((output / 'measurement.json').read_text())
            self.assertFalse(report['passed'])
            self.assertNotIn('measured_peak_bytes', report)
            self.assertEqual(command.call_args_list[-1].args[0], ['umount', str(output / 'storage')])

    def test_registration_refuses_stale_admission_or_existing_output_before_owner(self):
        import capacity_register
        candidate_raw, baseline_raw, report = self.inputs()
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            candidate, baseline, measurement = (root / k for k in ['candidate', 'baseline', 'measurement'])
            candidate.write_bytes(candidate_raw)
            baseline.write_bytes(baseline_raw)
            measurement.write_text(json.dumps(report))
            candidate.chmod(0o600)
            output = root / 'receipt'
            argv = ['capacity_register', '--candidate', str(candidate), '--baseline', str(baseline),
                    '--measurement', str(measurement), '--output', str(output)]
            with patch('sys.argv', argv), patch.object(capacity_register.deploy, 'prepare', return_value={}), \
                    patch.object(capacity_register.deploy, 'connect') as connect, \
                    patch.object(capacity_register.deploy, 'ownership') as owner:
                with self.assertRaisesRegex(ValueError, 'live admission'):
                    capacity_register.main()
                connect.assert_not_called()
                owner.assert_not_called()
                self.assertEqual(output.read_bytes(), b'')
                with self.assertRaises(FileExistsError):
                    capacity_register.main()
                owner.assert_not_called()
