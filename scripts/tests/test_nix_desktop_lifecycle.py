from pathlib import Path
import sys
import unittest
from unittest.mock import patch, Mock
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import nix_desktop_lifecycle as lifecycle


class RecordingRuntime:
    def __init__(self):
        self.events = []
    def install(self, role):
        self.events.append(('install', role))
    def probe(self, expected, update, termination):
        self.events.append(('probe', expected, update, termination))


class LifecycleTests(unittest.TestCase):
    def pair(self):
        def value(version, char, run):
            return {'producer': {'artifacts': [{'runId': run}]}, 'binding': {
                'archive': {'sha256': 'sha256:' + char * 64, 'version': version, 'platform': 'linux/amd64'},
                'releaseAdmission': False}}
        return value('0.1.0', 'a', 1), value('0.2.0', 'b', 2)

    def test_equal_version_is_not_an_upgrade(self):
        old, new = self.pair()
        with patch.object(lifecycle, 'version_less', return_value=False):
            with self.assertRaisesRegex(ValueError, 'strictly newer'):
                lifecycle.validate_pair(old, new)
        with patch.object(lifecycle, 'version_less', return_value=True):
            lifecycle.validate_pair(old, new)
            new['binding']['archive']['sha256'] = old['binding']['archive']['sha256']
            with self.assertRaises(ValueError):
                lifecycle.validate_pair(old, new)

    def test_missing_probe_ack_or_wrong_termination_cannot_be_success(self):
        result = {'schemaVersion': 1, 'trustedShellReadback': True, 'acknowledgedWrite': True,
                  'durableReadback': True, 'termination': 'crash', 'mainProcessExited': True, 'processGroupStopped': True}
        lifecycle.check_probe(result, True, 'crash')
        for key in ['trustedShellReadback', 'acknowledgedWrite', 'durableReadback', 'mainProcessExited', 'processGroupStopped']:
            with self.subTest(key=key), self.assertRaises(ValueError):
                lifecycle.check_probe(dict(result, **{key: False}), True, 'crash')
        with self.assertRaises(ValueError):
            lifecycle.check_probe(result, True, 'graceful')

    def test_failed_candidate_install_cannot_reach_recovery_or_report_success(self):
        runtime = RecordingRuntime()
        original = runtime.install
        def install(role):
            original(role)
            if role == 'candidate':
                raise ValueError('injected dpkg failure')
        runtime.install = install
        with self.assertRaisesRegex(ValueError, 'dpkg'):
            lifecycle.exercise(runtime)
        self.assertEqual(runtime.events[-1], ('install', 'candidate'))
        self.assertEqual(len(runtime.events), 3)

    def test_offline_executor_rejects_host_interfaces_before_any_mutation(self):
        with patch.object(lifecycle.os, 'getuid', return_value=1000), \
             patch.dict(lifecycle.os.environ, {'LEAPVIEW_DESKTOP_LIFECYCLE_OFFLINE': '1'}), \
             patch.object(lifecycle.candidate, 'read_json_file', return_value={}), \
             patch.object(lifecycle.desktop, 'command_output', return_value='[{"ifname":"lo"},{"ifname":"eth0"}]'), \
             patch.object(lifecycle.desktop, 'run_command') as mutate:
            with self.assertRaisesRegex(ValueError, 'loopback-only'):
                lifecycle.execute_offline(Path('/unused'))
            mutate.assert_not_called()

    def test_offline_executor_removes_owned_package_when_probe_fails(self):
        runtime = Mock()
        runtime.probe.side_effect = ValueError('injected profile read failure')
        with patch.object(lifecycle.os, 'getuid', return_value=1000), \
             patch.dict(lifecycle.os.environ, {'LEAPVIEW_DESKTOP_LIFECYCLE_OFFLINE': '1'}), \
             patch.object(lifecycle.candidate, 'read_json_file', return_value={'work': '/private/work'}), \
             patch.object(lifecycle.desktop, 'command_output', return_value='[{"ifname":"lo"}]'), \
             patch.object(lifecycle.desktop, 'run_command'), \
             patch.object(lifecycle.desktop, 'refuse_preexisting_installation'), \
             patch.object(lifecycle.desktop, 'remove_candidate_installation') as cleanup, \
             patch.object(lifecycle.desktop, 'write_json') as write, \
             patch.object(lifecycle, 'Runtime', return_value=runtime):
            with self.assertRaisesRegex(ValueError, 'profile read'):
                lifecycle.execute_offline(Path('/unused'))
            cleanup.assert_called_once_with('/private/work')
            write.assert_not_called()

    def test_lifecycle_order_requires_same_profile_after_ack_crash_and_rollback(self):
        runtime = RecordingRuntime()
        lifecycle.exercise(runtime)
        self.assertEqual(runtime.events, [
            ('install', 'predecessor'), ('probe', 'seed', 'before-upgrade', 'graceful'),
            ('install', 'candidate'), ('probe', 'before-upgrade', 'after-upgrade', 'crash'),
            ('probe', 'after-upgrade', '-', 'graceful'),
            ('install', 'predecessor'), ('probe', 'after-upgrade', 'after-rollback', 'graceful'),
            ('probe', 'after-rollback', '-', 'graceful')])

if __name__ == '__main__':
    unittest.main()
