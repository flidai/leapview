"""Final CFO validation must never enter rollout or recovery."""
from contextlib import contextmanager
import pathlib
import sys
import unittest
from unittest.mock import patch

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[1]))
import demo_compose_deploy as deploy


class PublishedCFOTests(unittest.TestCase):
    def test_revision_mismatch_stops_before_host_access(self):
        with patch.dict('os.environ', {'SOURCE_REVISION': 'a'*40}), \
                patch.object(deploy, 'read_contract', return_value={'permissionProfile': 'leapview.permissions/v1'}), \
                patch.object(deploy, 'verify_public_revision', side_effect=RuntimeError('revision mismatch')), \
                patch.object(deploy, 'verified_demo_ssh') as ssh:
            with self.assertRaisesRegex(RuntimeError, 'revision mismatch'):
                deploy.verify_publication()
            ssh.assert_not_called()

    def test_browser_failure_cleans_helper_without_runtime_commands(self):
        @contextmanager
        def ssh():
            yield ['ssh', 'test-host']

        calls = []
        def run(args, **kwargs):
            calls.append(args)
            if args[0] == 'node':
                raise RuntimeError('visuals failed')

        with patch.dict('os.environ', {'SOURCE_REVISION': 'a'*40}), \
                patch.object(deploy, 'read_contract', return_value={'permissionProfile': 'leapview.permissions/v1'}), \
                patch.object(deploy, 'verify_public_revision'), \
                patch.object(deploy, 'verified_demo_ssh', ssh), \
                patch.object(deploy, 'viewer_environment', return_value={}), \
                patch.object(deploy.subprocess, 'run', side_effect=run):
            with self.assertRaisesRegex(RuntimeError, 'visuals failed'):
                deploy.verify_publication()
        self.assertEqual(len(calls), 3)
        self.assertTrue(calls[0][-1].startswith('umask 077; cat > /run/leapview-demo-verify-'))
        self.assertEqual(calls[1], ['node', 'scripts/demo_validate_browser.mjs'])
        self.assertEqual(calls[2][2:4], ['rm', '-f'])
