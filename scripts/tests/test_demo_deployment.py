"""Behavioral tests for the hosted demo deployment boundary (no host access)."""
import importlib.util
import pathlib
import unittest
from unittest.mock import patch
import tempfile
import os
import io
import json
import sys

ROOT = pathlib.Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'scripts'))
def load(name):
    spec = importlib.util.spec_from_file_location(name, ROOT / 'scripts' / (name + '.py'))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module

class AdmissionTests(unittest.TestCase):
    def setUp(self):
        self.policy = load('demo_image_policy')
        self.image = 'ghcr.io/flidai/leapview@sha256:' + 'a' * 64
        self.revision = 'b' * 40
        self.run = {'id': 123, 'run_attempt': 1, 'path': '.github/workflows/artifacts.yml',
                    'head_sha': self.revision, 'head_branch': 'main', 'event': 'push',
                    'conclusion': 'success', 'repository': {'full_name': 'flidai/leapview'}}
        self.receipt = {'image': self.image, 'revision': self.revision, 'runId': '123',
                        'runAttempt': '1', 'qualified': True}
    def test_matching_qualified_digest(self):
        self.assertEqual(self.policy.admit(self.run, self.receipt, self.image), self.revision)
    def test_same_revision_different_digest_is_rejected(self):
        with self.assertRaises(ValueError):
            self.policy.admit(self.run, self.receipt, self.image[:-1] + 'c')
    def test_wrong_workflow_failed_run_and_candidate_are_rejected(self):
        for key, value in [('conclusion', 'failure'), ('event', 'workflow_dispatch'),
                           ('path', '.github/workflows/ci.yml'), ('head_branch', 'other')]:
            with self.subTest(key=key), self.assertRaises(ValueError):
                self.policy.admit(dict(self.run, **{key: value}), self.receipt, self.image)
    def test_stale_attempt_rejected(self):
        with self.assertRaises(ValueError):
            self.policy.admit(dict(self.run, run_attempt=2), self.receipt, self.image)
    def test_mutable_or_foreign_image_rejected(self):
        for image in ['ghcr.io/flidai/leapview:latest', 'other@sha256:'+'a'*64]:
            with self.assertRaises(ValueError):
                self.policy.admit(self.run, self.receipt, image)

class PreflightTests(unittest.TestCase):
    def test_preflight_never_starts_host_transaction_or_fetches_viewer(self):
        for mode in ['image-only', 'database-upgrade-required', 'review-required']:
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as directory:
                runner = load('demo_compose_deploy')
                revision = 'b' * 40
                commands = []
                def output(args, **kwargs):
                    commands.append(args)
                    if args[0] == 'ssh-keyscan': return b'host public-key'
                    if args[0] == 'ssh-keygen': return '256 ' + runner.FINGERPRINT + ' host'
                    if args[-1] == 'inspect': return json.dumps({'revision':revision}).encode()
                    self.fail('Unexpected preflight command: ' + repr(args))
                env = {'DEMO_IMAGE':'ghcr.io/flidai/leapview@sha256:'+'a'*64,
                       'SOURCE_REVISION':revision, 'DEMO_HOST':runner.HOST,
                       'DEMO_SSH_PRIVATE_KEY':'test-only',
                       'GITHUB_STEP_SUMMARY':str(pathlib.Path(directory)/'summary')}
                report = {'mode':mode, 'currentSchema':28, 'candidateSchema':30}
                previous_umask = os.umask(0o077)
                try:
                    with patch.dict(os.environ, env, clear=True), patch.object(sys, 'argv', ['runner','--preflight']), \
                         patch.object(runner.subprocess, 'check_output', side_effect=output), \
                         patch.object(runner.subprocess, 'run') as run, \
                         patch.object(runner.subprocess, 'Popen') as popen, \
                         patch.object(runner, 'inspect_transition', return_value=report), \
                         patch.object(runner, 'verify_public_revision') as public, patch('sys.stdout', io.StringIO()):
                        if mode == 'image-only': runner.main()
                        else:
                            with self.assertRaisesRegex(RuntimeError, mode): runner.main()
                        popen.assert_not_called()
                        public.assert_not_called()
                        self.assertEqual(run.call_count, 2)  # temporary inspector upload and cleanup only
                        self.assertEqual(run.call_args.args[0][-3:-1], ['rm','-f'])
                    self.assertIn(mode, pathlib.Path(env['GITHUB_STEP_SUMMARY']).read_text())
                finally:
                    os.umask(previous_umask)

class UpgradeGuardTests(unittest.TestCase):
    def setUp(self):
        self.runtime = load('demo_compose_runtime')
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.runtime.PROVIDER = pathlib.Path(self.directory.name)
        self.journal = self.runtime.PROVIDER/'upgrade-operation.json'

    def write(self, phase, version=1):
        self.journal.write_text(json.dumps({'version':version, 'state':{'phase':phase}}))
        self.journal.chmod(0o600)

    def test_incomplete_upgrade_blocks_image_only_deploy(self):
        for phase in ['prepared','quiescing','capturing','verified','migrating','starting',
                      'validating','committed','restoring','reopening','unknown']:
            with self.subTest(phase=phase):
                self.write(phase)
                with self.assertRaisesRegex(RuntimeError, 'Unfinished schema upgrade'):
                    with self.runtime.upgrade_guard(): self.fail('entered image rollout')

    def test_no_journal_or_terminal_upgrade_allows_image_only_path(self):
        with self.runtime.upgrade_guard(): pass
        for phase in ['succeeded','recovered']:
            self.write(phase)
            with self.runtime.upgrade_guard(): pass

    def test_competing_operator_is_rejected(self):
        with self.runtime.upgrade_guard():
            with self.assertRaises(BlockingIOError):
                with self.runtime.upgrade_guard(): self.fail('concurrent operator entered')

    def test_unknown_journal_version_and_permissions_rejected(self):
        self.write('succeeded', version=2)
        with self.assertRaises(RuntimeError):
            with self.runtime.upgrade_guard(): self.fail('accepted future version')
        self.write('succeeded')
        self.journal.chmod(0o644)
        with self.assertRaises(RuntimeError):
            with self.runtime.upgrade_guard(): self.fail('accepted writable journal')

class RolloutTests(unittest.TestCase):
    def setUp(self):
        self.rollout = load('demo_compose_runtime')
    def test_runner_eof_and_timeout_do_not_approve(self):
        for closed in [True,False]:
            read,write=os.pipe()
            with os.fdopen(read) as stream:
                if closed: os.close(write)
                try:
                    with self.assertRaises(RuntimeError): self.rollout.await_approval(stream,timeout=0.01)
                finally:
                    if not closed: os.close(write)
    def test_only_explicit_commit_approves(self):
        for value in ['commit\n','failed\n']:
            read,write=os.pipe()
            with os.fdopen(write,'w') as sender: sender.write(value)
            with os.fdopen(read) as stream:
                if value == 'commit\n': self.rollout.await_approval(stream,timeout=0.01)
                else:
                    with self.assertRaises(RuntimeError): self.rollout.await_approval(stream,timeout=0.01)
    def test_only_image_assignment_changes(self):
        old = 'ghcr.io/flidai/leapview@sha256:'+'a'*64
        new = 'ghcr.io/flidai/leapview@sha256:'+'b'*64
        original = f'COMPOSE_PROJECT_NAME=leapview-cfo\nLEAPVIEW_IMAGE={old}\nOTHER=keep\n'.encode()
        self.assertEqual(self.rollout.replace_image(original, old, new), original.replace(old.encode(), new.encode()))
    def test_stale_or_duplicate_pin_rejected(self):
        for env in [b'LEAPVIEW_IMAGE=wrong\n', b'LEAPVIEW_IMAGE=old\nLEAPVIEW_IMAGE=old\n']:
            with self.assertRaises(ValueError): self.rollout.replace_image(env, 'old', 'new')
    def test_failed_validation_rolls_back(self):
        events = []
        def apply(): events.append('apply')
        def validate(): events.append('validate'); raise RuntimeError('unhealthy')
        def rollback(): events.append('rollback')
        with self.assertRaises(RuntimeError): self.rollout.transaction(apply, validate, rollback)
        self.assertEqual(events, ['apply', 'validate', 'rollback'])
    def test_success_does_not_roll_back(self):
        events=[]
        self.rollout.transaction(lambda: events.append('apply'), lambda: events.append('validate'), lambda: events.append('rollback'))
        self.assertEqual(events, ['apply','validate'])
    def test_partial_apply_is_rolled_back(self):
        events=[]
        def apply(): events.append('apply'); raise RuntimeError('compose failed')
        with self.assertRaises(RuntimeError): self.rollout.transaction(apply, lambda: None, lambda: events.append('rollback'))
        self.assertEqual(events,['apply','rollback'])
    def test_rollback_failure_is_explicit(self):
        def fail(): raise RuntimeError('broken')
        with self.assertRaisesRegex(RuntimeError,'rollback failed'): self.rollout.transaction(fail, lambda:None, fail)

class StagingTests(unittest.TestCase):
    def setUp(self):
        self.rollout = load('demo_compose_runtime')
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = pathlib.Path(self.directory.name)
        self.rollout.ROOT = self.root
        self.image = 'ghcr.io/flidai/leapview@sha256:' + 'a' * 64
        self.release = self.root/'releases'/('sha256-'+'a'*64)
        self.release.mkdir(parents=True)
        self.payload = {name: b'packaged content' for name in
                        ['compose.yaml', 'compose.https.yaml', 'Caddyfile',
                         'deployment.env.example', 'leapviewctl']}
        for name, data in self.payload.items():
            (self.release/name).write_bytes(data)
            (self.root/name).symlink_to('current/'+name)
        (self.root/'current').symlink_to('releases/'+self.release.name)

    def copy(self, *args):
        if args[:2] == ('docker', 'cp'):
            for name, data in self.payload.items():
                (pathlib.Path(args[-1])/name).write_bytes(data)

    def stage(self):
        with patch.object(self.rollout, 'out', return_value='container'), patch.object(self.rollout, 'run', side_effect=self.copy):
            return self.rollout.stage_release(self.image)

    def test_same_image_retry_preserves_custom_configuration_on_rejection(self):
        (self.release/'Caddyfile').write_bytes(b'operator configuration')
        with self.assertRaisesRegex(RuntimeError, 'Deployment payload changed'):
            self.stage()
        self.assertEqual((self.release/'Caddyfile').read_bytes(), b'operator configuration')
        self.assertEqual((self.root/'current').resolve(), self.release)

    def test_same_image_retry_preserves_existing_files(self):
        before = {name: (self.release/name).stat() for name in self.payload}
        self.assertEqual(self.stage(), self.release)
        for name, previous in before.items():
            current = (self.release/name).stat()
            self.assertEqual((previous.st_ino, previous.st_mtime_ns, previous.st_mode),
                             (current.st_ino, current.st_mtime_ns, current.st_mode))
        self.assertEqual(list((self.root/'releases').iterdir()), [self.release])

    def test_changed_existing_release_tool_is_rejected_without_overwriting(self):
        (self.release/'leapviewctl').write_bytes(b'operator tool')
        with self.assertRaisesRegex(RuntimeError, 'Existing release'):
            self.stage()
        self.assertEqual((self.release/'leapviewctl').read_bytes(), b'operator tool')

    def test_interrupted_extraction_leaves_current_untouched(self):
        def failed_copy(*args):
            if args[:2] == ('docker', 'cp'):
                (pathlib.Path(args[-1])/'Caddyfile').write_bytes(b'partial extraction')
                raise RuntimeError('copy interrupted')
        with patch.object(self.rollout, 'out', return_value='container'), patch.object(self.rollout, 'run', side_effect=failed_copy):
            with self.assertRaisesRegex(RuntimeError, 'copy interrupted'):
                self.rollout.stage_release(self.image)
        for name, data in self.payload.items():
            self.assertEqual((self.release/name).read_bytes(), data)
        self.assertEqual(list((self.root/'releases').iterdir()), [self.release])

    def test_new_release_is_staged_without_mutating_current(self):
        previous = self.release
        self.image = 'ghcr.io/flidai/leapview@sha256:'+'b'*64
        release = self.stage()
        self.assertNotEqual(release, previous)
        self.assertEqual((self.root/'current').resolve(), previous)
        for name, data in self.payload.items():
            self.assertEqual((release/name).read_bytes(), data)
        self.assertEqual((release/'leapviewctl').stat().st_mode & 0o777, 0o700)

class PublicIdentityTests(unittest.TestCase):
    def test_public_endpoint_must_match_exact_revision(self):
        deploy=load('demo_compose_deploy')
        for revision, succeeds in [('a'*40,True),('b'*40,False)]:
            with patch.dict(os.environ,DEMO_PUBLISHER_CLIENT_ID='id',DEMO_PUBLISHER_CLIENT_SECRET='secret',DEMO_PROJECT_ID='project'):
                responses=[io.BytesIO(b'{"access_token":"test"}'),io.BytesIO(json.dumps({'buildRevision':revision,'buildDirty':False}).encode())]
                with patch.object(deploy.urllib.request,'urlopen',side_effect=responses):
                    if succeeds: deploy.verify_public_revision('a'*40)
                    else:
                        with self.assertRaises(RuntimeError): deploy.verify_public_revision('a'*40)

class RuntimePinTests(unittest.TestCase):
    def test_successful_record_overrides_legacy_variable(self):
        record=load('demo_runtime_record')
        with tempfile.NamedTemporaryFile() as output, patch.dict(os.environ, GITHUB_OUTPUT=output.name, DEMO_RUNTIME_REVISION='a'*40):
            with patch.object(record,'api',side_effect=[[{'id':1,'sha':'b'*40}],[{'state':'success'}]]):
                record.resolve()
            self.assertIn('revision='+'b'*40, pathlib.Path(output.name).read_text())
    def test_incomplete_deployment_never_falls_back_to_stale_variable(self):
        record=load('demo_runtime_record')
        for state in ['failure','error','in_progress','pending']:
            with self.subTest(state=state), tempfile.NamedTemporaryFile() as output, patch.dict(os.environ, GITHUB_OUTPUT=output.name, DEMO_RUNTIME_REVISION='a'*40):
                with patch.object(record,'api',side_effect=[[{'id':1,'sha':'b'*40}],[{'state':state}]]), self.assertRaises(RuntimeError):
                    record.resolve()
                self.assertEqual(pathlib.Path(output.name).read_text(),'')
    def test_bootstrap_without_records_uses_existing_pin(self):
        record=load('demo_runtime_record')
        with tempfile.NamedTemporaryFile() as output, patch.dict(os.environ, GITHUB_OUTPUT=output.name, DEMO_RUNTIME_REVISION='a'*40):
            with patch.object(record,'api',return_value=[]): record.resolve()
            self.assertIn('revision='+'a'*40,pathlib.Path(output.name).read_text())

if __name__ == '__main__': unittest.main()
