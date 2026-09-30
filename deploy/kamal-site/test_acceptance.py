import copy
import datetime as dt
import hashlib
import json
import math
import os
from pathlib import Path
from types import SimpleNamespace
import tempfile
import unittest
from unittest.mock import patch

import acceptance
import observe
from test_observe import config as host_config, host_sample, http_good


class AcceptanceTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.run = self.root / 'run'
        self.run.mkdir(mode=0o700)
        self.bundle = self.root / 'gate'
        self.started = (dt.datetime.now(dt.timezone.utc) - dt.timedelta(days=1, minutes=5)).replace(microsecond=0)
        cfg = host_config(duration=86400, probe=60, host=900)
        self.public = {'schemaVersion': 1, 'version': '1.0.0', 'image': 'ghcr.io/flidai/leapview@sha256:' + 'c' * 64}
        record = {'schema': 1, 'kamal': '2.12.0', **cfg['identity'],
                  'platform': 'sha256:' + '3' * 64, 'config': 'sha256:' + '4' * 64,
                  'runtime': {'port': 8081, 'user': '65532:65532', 'base_url': 'https://leapview.dev',
                              'tmpfs_mib': 64, 'log_size': '10m', 'log_files': 3}, 'release': self.public}
        inputs = {}
        for key, name, content in [('record', 'record.json', json.dumps(record)),
                                   ('ssh_config', 'ssh_config', 'operator-supplied fixture'),
                                   ('fingerprint_pin', 'fingerprint.sha256', 'fixture')]:
            path = self.write(name, content)
            inputs[key] = {'path': str(path), 'sha256': acceptance.digest(path.read_bytes())}
        cfg.update(input_files=inputs, observer_sha256=observe.observer_source_sha256())
        self.summary = observe._initial_summary(cfg, self.started.timestamp(), acceptance.CLOCK_TOLERANCE)
        self.summary.update(samples=1, host_samples=1, elapsed_seconds=1,
                            last_sample_at=self.iso(self.started))
        self.write_run('summary.json', self.summary)
        self.write_run('process.json', {'pid': os.getpid(), 'start_ticks': observe.proc_identity(os.getpid())['start_ticks'],
                                      'boot_id': observe.proc_boot_id(), 'observer_sha256': cfg['observer_sha256']})
        smoke = self.write('public_site_smoke.ts', 'fixture smoke source', mode=0o664)
        public = self.write('public-release.json', json.dumps(self.public))
        desktop = self.write('desktop-release.json', json.dumps({'schemaVersion': 1, 'status': 'withdrawn'}))
        bun = self.write('bun', '#!/bin/sh\nexit 0\n', mode=0o775)
        self.marker = acceptance.smoke_marker(cfg['base_url'], self.public)
        log = self.write('start-smoke.log', self.marker + '\n')
        os.utime(log, (self.started.timestamp() - 5, self.started.timestamp() - 5))
        hashes = self.write('start-smoke.sha256', ''.join(acceptance.digest(p.read_bytes()) + '  ' + str(p) + '\n'
                                                       for p in (smoke, public, desktop)))
        self.args = SimpleNamespace(run_dir=str(self.run), observer_source=observe.__file__,
                                    smoke_script=str(smoke), public_manifest=str(public), desktop_manifest=str(desktop),
                                    start_smoke_log=str(log), start_smoke_hashes=str(hashes), bun=str(bun),
                                    output_dir=str(self.bundle), alias=None)

    @staticmethod
    def iso(value):
        return value.isoformat().replace('+00:00', 'Z')

    def write(self, name, content, mode=0o600):
        path = self.root / name
        path.write_text(content)
        path.chmod(mode)
        return path

    def write_run(self, name, value):
        path = self.run / name
        path.write_text(json.dumps(value))
        path.chmod(0o600)

    def prepare(self):
        return acceptance.prepare(self.args)

    def complete(self):
        self.prepare()
        http = http_good()
        for response in http.values():
            response['canonical_url'] = True
        origin = self.summary['clock_origin_wall_unix_seconds']
        self.events = []
        for index in range(1441):
            elapsed = index * 60
            observed_wall = origin + elapsed
            self.events.append({'type': 'sample', 'index': index,
                'scheduled_at': self.iso(dt.datetime.fromtimestamp(math.floor(origin) + elapsed, dt.timezone.utc)),
                'observed_at': self.iso(dt.datetime.fromtimestamp(math.floor(observed_wall), dt.timezone.utc)),
                'observed_wall_unix_seconds': observed_wall,
                'elapsed_seconds': elapsed, 'sample_execution_ms': 1,
                'http': copy.deepcopy(http), 'host': host_sample() if index % 15 == 0 else None,
                'host_error': None, 'failures': []})
        self.save_events()
        self.summary.update(status='health_storage_passed', elapsed_seconds=86400,
                            ended_at=self.iso(dt.datetime.fromtimestamp(math.floor(origin + 86400), dt.timezone.utc)),
                            ended_wall_unix_seconds=origin + 86400,
                            last_sample_at=self.events[-1]['observed_at'], samples=1441, host_samples=97)
        self.write_run('summary.json', self.summary)

    def save_events(self):
        path = self.run / 'samples.jsonl'
        path.write_text(''.join(json.dumps(event) + '\n' for event in self.events))
        path.chmod(0o600)

    def validate(self):
        _, config, observer, summary, status = acceptance.load_bundle(self.bundle)
        with patch.object(acceptance, 'process_alive', return_value=False):
            return acceptance.validate_complete(config, observer, summary, status)

    def test_group_writable_workspace_sources_are_frozen_privately_before_execution(self):
        self.assertEqual(self.prepare()['status'], 'prepared')
        self.assertEqual(self.bundle.stat().st_mode & 0o777, 0o700)
        self.assertEqual((self.bundle / 'public_site_smoke.ts').stat().st_mode & 0o777, 0o600)
        self.assertEqual((self.bundle / 'bun').stat().st_mode & 0o777, 0o700)
        self.assertEqual(Path(self.args.smoke_script).stat().st_mode & 0o777, 0o664)
        self.assertEqual(acceptance.preflight(self.bundle)['rollout_acceptance'], 'pending')
        self.assertFalse((self.bundle / 'receipt.json').exists())

    def test_preflight_uses_frozen_copy_after_original_workspace_changes(self):
        self.prepare()
        Path(self.args.smoke_script).write_text('later unrelated workspace edit')
        self.assertEqual(acceptance.preflight(self.bundle)['status'], 'preflight_passed')

    def test_preparation_rejects_wrong_source_or_start_smoke_hash(self):
        Path(self.args.smoke_script).write_text('unrecorded source')
        with self.assertRaisesRegex(ValueError, 'start smoke hash'):
            self.prepare()
        self.assertFalse(self.bundle.exists())

    def test_preparation_rejects_old_or_post_start_smoke(self):
        for offset in (-400, 5):
            with self.subTest(offset=offset):
                os.utime(self.args.start_smoke_log, (self.started.timestamp() + offset,) * 2)
                with self.assertRaisesRegex(ValueError, 'within five minutes'):
                    self.prepare()
                self.assertFalse(self.bundle.exists())

    def test_preparation_rejects_manifest_different_from_admission(self):
        Path(self.args.public_manifest).write_text(json.dumps({'image': 'wrong release'}))
        with self.assertRaisesRegex(ValueError, 'admitted active release'):
            self.prepare()

    def test_preflight_rejects_frozen_source_drift(self):
        self.prepare()
        (self.bundle / 'public_site_smoke.ts').write_text('changed frozen source')
        with self.assertRaisesRegex(ValueError, 'hash mismatch'):
            acceptance.preflight(self.bundle)

    def test_preflight_rejects_changed_operator_input(self):
        self.prepare()
        Path(self.summary['input_files']['ssh_config']['path']).write_text('changed SSH config')
        with self.assertRaisesRegex(ValueError, 'input pin mismatch'):
            acceptance.preflight(self.bundle)

    def test_preflight_rejects_failed_and_interrupted_observer(self):
        self.prepare()
        self.summary.update(status='failed', failure='monitoring gap')
        self.write_run('summary.json', self.summary)
        with self.assertRaisesRegex(ValueError, 'full health/storage contract'):
            acceptance.preflight(self.bundle)
        self.summary.update(status='running', failure=None)
        self.write_run('summary.json', self.summary)
        with patch.object(observe, 'proc_identity', side_effect=FileNotFoundError()):
            # The bundle executes its frozen module, so simulate a vanished
            # process through its metadata and preserve that pin explicitly.
            process = json.loads((self.run / 'process.json').read_text())
            process['pid'] = 999999999
            self.write_run('process.json', process)
            with self.assertRaisesRegex(ValueError, 'process identity changed'):
                acceptance.preflight(self.bundle)

    def test_full_1441_public_and_97_host_samples_validate(self):
        self.complete()
        event_hash, baseline = self.validate()
        self.assertEqual(event_hash, acceptance.digest((self.run / 'samples.jsonl').read_bytes()))
        self.assertIsNotNone(baseline)

    def test_incomplete_counts_and_log_are_rejected(self):
        self.complete()
        self.summary['host_samples'] = 96
        self.write_run('summary.json', self.summary)
        with self.assertRaisesRegex(ValueError, '24-hour'):
            self.validate()
        self.summary['host_samples'] = 97
        self.write_run('summary.json', self.summary)
        self.events.pop()
        self.save_events()
        with self.assertRaisesRegex(ValueError, 'event log is incomplete'):
            self.validate()

    def test_failed_late_and_missing_endpoint_samples_are_rejected(self):
        self.complete()
        original = copy.deepcopy(self.events[600])
        cases = [lambda event: event.update(failures=['503']),
                 lambda event: event.update(observed_at=self.iso(self.started + dt.timedelta(seconds=36016))),
                 lambda event: event.update(elapsed_seconds=36016),
                 lambda event: event.update(observed_wall_unix_seconds=event['observed_wall_unix_seconds'] + 6,
                                             observed_at=self.iso(dt.datetime.fromtimestamp(math.floor(
                                                 event['observed_wall_unix_seconds'] + 6), dt.timezone.utc))),
                 lambda event: event.update(sample_execution_ms=15001),
                 lambda event: event.update(elapsed_seconds=36010, sample_execution_ms=10000),
                 lambda event: event['http'].pop('readyz'),
                 lambda event: event['http']['build'].update(image='wrong image')]
        for mutate in cases:
            with self.subTest(mutate=mutate):
                self.events[600] = copy.deepcopy(original)
                mutate(self.events[600])
                self.save_events()
                with self.assertRaises(ValueError):
                    self.validate()

    def shift_wall_clock(self, correction, *, from_index=1):
        origin = self.summary['clock_origin_wall_unix_seconds']
        for event in self.events:
            offset = correction if event['index'] >= from_index else 0
            event['observed_wall_unix_seconds'] = origin + event['elapsed_seconds'] + offset
            observed = dt.datetime.fromtimestamp(math.floor(event['observed_wall_unix_seconds']), dt.timezone.utc)
            event['observed_at'] = observed.isoformat().replace('+00:00', 'Z')
        self.summary['ended_wall_unix_seconds'] = origin + self.summary['elapsed_seconds'] + correction
        ended = dt.datetime.fromtimestamp(math.floor(self.summary['ended_wall_unix_seconds']), dt.timezone.utc)
        self.summary['ended_at'] = ended.isoformat().replace('+00:00', 'Z')
        self.summary['last_sample_at'] = self.events[-1]['observed_at']
        self.save_events()
        self.write_run('summary.json', self.summary)

    def test_permitted_wall_clock_corrections_are_validated_against_monotonic_elapsed(self):
        self.complete()
        base_events = copy.deepcopy(self.events)
        base_summary = copy.deepcopy(self.summary)
        for correction in (-4, 4):
            with self.subTest(correction=correction):
                self.events = copy.deepcopy(base_events)
                self.summary = copy.deepcopy(base_summary)
                self.shift_wall_clock(correction)
                self.validate()

    def test_wall_clock_drift_over_tolerance_is_rejected(self):
        self.complete()
        self.shift_wall_clock(6, from_index=600)
        with self.assertRaisesRegex(ValueError, 'clock tolerance'):
            self.validate()

    def test_fractional_clock_origin_keeps_second_floor_consistent(self):
        self.started = self.started.replace(microsecond=999800)
        self.summary['clock_origin_wall_unix_seconds'] = self.started.timestamp()
        self.write_run('summary.json', self.summary)
        self.complete()
        origin = self.summary['clock_origin_wall_unix_seconds']
        event = self.events[1]
        event['elapsed_seconds'] = 60.0004
        event['observed_wall_unix_seconds'] = origin + event['elapsed_seconds']
        event['observed_at'] = self.iso(dt.datetime.fromtimestamp(
            math.floor(event['observed_wall_unix_seconds']), dt.timezone.utc))
        self.save_events()
        self.validate()

    def test_host_reserve_or_restart_failure_is_independently_rejected(self):
        self.complete()
        self.events[600]['host']['filesystems'][0]['available_inodes'] = 1
        self.save_events()
        with self.assertRaisesRegex(ValueError, 'retained-image and storage'):
            self.validate()
        self.events[600]['host'] = host_sample()
        self.events[600]['host']['apps'][0]['restart_count'] = 1
        self.save_events()
        with self.assertRaisesRegex(ValueError, 'retained-image and storage'):
            self.validate()

    def test_no_end_smoke_runs_for_incomplete_observation(self):
        self.prepare()
        with patch.object(acceptance.subprocess, 'run') as command:
            self.assertEqual(acceptance.accept(self.bundle)['status'], 'failed')
            command.assert_not_called()
        self.assertTrue((self.bundle / 'attempt.json').exists())
        self.assertEqual(json.loads((self.bundle / 'receipt.json').read_text())['status'], 'failed')

    def test_failed_observer_is_recorded_as_failed_acceptance(self):
        self.prepare()
        self.summary.update(status='failed', failure='monitoring gap')
        self.write_run('summary.json', self.summary)
        self.assertEqual(acceptance.accept(self.bundle)['status'], 'failed')
        self.assertFalse((self.bundle / 'end-smoke.log').exists())

    def end_smoke(self, command, **kwargs):
        self.assertEqual(command[0], str(self.bundle / 'bun'))
        self.assertEqual(command[-1], str(self.bundle / 'public_site_smoke.ts'))
        self.assertNotIn('GH_TOKEN', kwargs['env'])
        kwargs['stdout'].write((self.marker + '\n').encode())
        return SimpleNamespace(returncode=0)

    def test_full_acceptance_runs_frozen_smoke_and_preserves_one_shot_receipt(self):
        self.complete()
        with patch.object(acceptance, 'process_alive', return_value=False), \
                patch.object(acceptance, 'check_current') as boundary, \
                patch.object(acceptance.subprocess, 'run', side_effect=self.end_smoke), \
                patch.dict(os.environ, {'GH_TOKEN': 'must-not-reach-smoke'}):
            result = acceptance.accept(self.bundle)
            self.assertEqual(result['status'], 'passed')
            self.assertEqual(result['rollout_acceptance'], 'final_retention_and_recovery_audit_required')
            self.assertEqual(boundary.call_count, 2)
            with self.assertRaises(FileExistsError):
                acceptance.accept(self.bundle)
        self.assertEqual((self.bundle / 'receipt.json').stat().st_mode & 0o777, 0o600)

    def test_failed_boundary_check_creates_failed_receipt_without_smoke(self):
        self.complete()
        with patch.object(acceptance, 'process_alive', return_value=False), \
                patch.object(acceptance, 'check_current', side_effect=ValueError('changed host')), \
                patch.object(acceptance.subprocess, 'run') as command:
            result = acceptance.accept(self.bundle)
            self.assertEqual(result['status'], 'failed')
            self.assertEqual(result['failure']['stage'], 'current_state_before_smoke')
            self.assertEqual(result['failure']['reason'], 'changed host')
            command.assert_not_called()

    def test_failure_receipt_redacts_sensitive_values(self):
        self.complete()
        with patch.object(acceptance, 'process_alive', return_value=False), \
                patch.object(acceptance, 'check_current', side_effect=ValueError(
                    'token=TOPSECRET /home/operator/.ssh/id_ed25519')):
            result = acceptance.accept(self.bundle)
        self.assertEqual(result['failure']['stage'], 'current_state_before_smoke')
        self.assertNotIn('TOPSECRET', result['failure']['reason'])
        self.assertNotIn('/home/operator', result['failure']['reason'])

    def test_subprocess_stderr_is_not_copied_into_failure_receipt(self):
        self.complete()
        import subprocess
        with patch.object(acceptance, 'process_alive', return_value=False), \
                patch.object(acceptance, 'check_current'), \
                patch.object(acceptance.subprocess, 'run', side_effect=subprocess.CalledProcessError(
                    1, ['bun'], stderr='PRIVATE SSH KEY')):
            result = acceptance.accept(self.bundle)
        self.assertEqual(result['failure']['stage'], 'end_smoke')
        self.assertEqual(result['failure']['reason'], 'local file or subprocess operation failed')
        self.assertNotIn('PRIVATE SSH KEY', json.dumps(result))

    def test_symlinked_inputs_and_unprotected_bundle_are_rejected(self):
        alternate = self.root / 'symlink.ts'
        alternate.symlink_to(self.args.smoke_script)
        self.args.smoke_script = str(alternate)
        with self.assertRaises(OSError):
            self.prepare()
        self.args.smoke_script = str(self.root / 'public_site_smoke.ts')
        self.prepare()
        self.bundle.chmod(0o755)
        with self.assertRaisesRegex(ValueError, 'mode 0700'):
            acceptance.preflight(self.bundle)


if __name__ == '__main__':
    unittest.main()
