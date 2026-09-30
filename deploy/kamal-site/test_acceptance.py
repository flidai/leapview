import copy
import datetime as dt
import hashlib
import json
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
        self.summary = observe._initial_summary(cfg, self.started.timestamp())
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
        self.events = [{'type': 'sample', 'index': index,
                        'scheduled_at': self.iso(self.started + dt.timedelta(seconds=60 * index)),
                        'observed_at': self.iso(self.started + dt.timedelta(seconds=60 * index)),
                        'elapsed_seconds': index * 60, 'sample_execution_ms': 1,
                        'http': copy.deepcopy(http), 'host': host_sample() if index % 15 == 0 else None,
                        'host_error': None, 'failures': []} for index in range(1441)]
        self.save_events()
        self.summary.update(status='health_storage_passed', elapsed_seconds=86400,
                            ended_at=self.iso(self.started + dt.timedelta(days=1)),
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
            self.assertEqual(acceptance.accept(self.bundle)['status'], 'failed')
            command.assert_not_called()

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
