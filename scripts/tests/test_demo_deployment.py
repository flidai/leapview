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
import subprocess
import types
import re

ROOT = pathlib.Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'scripts'))
def load(name):
    spec = importlib.util.spec_from_file_location(name, ROOT / 'scripts' / (name + '.py'))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module

class CredentialTransitionWorkflowTests(unittest.TestCase):
    def test_private_driver_protocol_regressions(self):
        subprocess.run(['node', '--test', str(ROOT / 'scripts/tests/demo_agent_credential_transition.test.mjs')],
                       cwd=ROOT, check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=60)

    def test_private_transition_dispatch_carries_only_a_reference_to_runtime(self):
        workflow = (ROOT / '.github/workflows/demo-deploy.yml').read_text()
        inputs = workflow.split('\npermissions:', 1)[0]
        transition = re.search(r'^      agent_credential_transition:\n(?P<fields>(?:        .*\n)+)', inputs, re.M)
        self.assertIsNotNone(transition, 'protected dispatch must accept a private transition reference')
        self.assertIn('type: string', transition['fields'])
        self.assertIn('required: false', transition['fields'])
        self.assertNotRegex(inputs, r'(?i)^      .*?(?:password|api_?key|secret):', 'dispatch must not accept plaintext credentials')
        runtime = workflow.split('\n  runtime:\n', 1)[1].split('\n  reconcile:\n', 1)[0]
        environment = runtime.split('\n    env:\n', 1)[1].split('\n    steps:\n', 1)[0]
        binding = 'DEMO_AGENT_CREDENTIAL_TRANSITION: ${{ inputs.agent_credential_transition }}'
        self.assertIn(binding, environment)
        self.assertEqual(workflow.count('${{ inputs.agent_credential_transition }}'), 1,
                         'reference must enter through an environment binding, not an interpolated shell command')

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
    def test_runtime_binding_carries_the_exact_qualified_artifact_attempt(self):
        runner = load('demo_compose_deploy')
        with tempfile.TemporaryDirectory() as directory:
            image = 'ghcr.io/flidai/leapview@sha256:'+'a'*64
            revision = 'b'*40
            pathlib.Path(directory, 'demo-qualification.json').write_text(json.dumps({
                'image':image, 'revision':revision, 'runId':'123', 'runAttempt':'2', 'qualified':True,
            }))
            env = {'QUALIFICATION_RUN':'123', 'RUNNER_TEMP':directory,
                   'GITHUB_RUN_ID':'456', 'GITHUB_RUN_ATTEMPT':'1'}
            previous = {'image':image, 'revision':revision}
            with patch.dict(os.environ, env, clear=True), \
                 patch.object(runner, 'read_contract', return_value={'permissionProfile':'leapview.permissions/v1'}):
                binding = runner.runtime_binding('deploy', previous, image, revision, 'sha256:'+'c'*64)
            self.assertEqual(binding['qualificationRunId'], '123')
            self.assertEqual(binding['qualificationAttempt'], '2')
            self.assertEqual(binding['permissionProfile'], 'leapview.permissions/v1')
            receipt = json.loads(pathlib.Path(directory, 'demo-qualification.json').read_text())
            receipt['image'] = 'ghcr.io/flidai/leapview@sha256:'+'d'*64
            pathlib.Path(directory, 'demo-qualification.json').write_text(json.dumps(receipt))
            with patch.dict(os.environ, env, clear=True):
                with self.assertRaisesRegex(ValueError, 'admitted qualification receipt'):
                    runner.runtime_binding('deploy', previous, image, revision, 'sha256:'+'c'*64)

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
                    if args[-1] == 'inspect': return json.dumps({'revision':revision, 'image':'ghcr.io/flidai/leapview@sha256:'+'c'*64}).encode()
                    self.fail('Unexpected preflight command: ' + repr(args))
                env = {'DEMO_IMAGE':'ghcr.io/flidai/leapview@sha256:'+'a'*64,
                       'SOURCE_REVISION':revision, 'DEMO_HOST':runner.HOST,
                       'DEMO_SSH_PRIVATE_KEY':'test-only',
                       'DEMO_PERMISSION_PROFILE':'leapview.permissions/v1',
                       'DEMO_PREDECESSOR_PERMISSION_PROFILE':'legacy-capabilities/v1',
                       'GITHUB_STEP_SUMMARY':str(pathlib.Path(directory)/'summary'),
                       'RUNNER_TEMP':directory, 'GITHUB_RUN_ID':'123', 'GITHUB_RUN_ATTEMPT':'1'}
                report = {'mode':mode, 'currentSchema':28, 'candidateSchema':30}
                previous_umask = os.umask(0o077)
                try:
                    with patch.dict(os.environ, env, clear=True), patch.object(sys, 'argv', ['runner','--preflight']), \
                         patch.object(runner.subprocess, 'check_output', side_effect=output), \
                         patch.object(runner.subprocess, 'run') as run, \
                         patch.object(runner.subprocess, 'Popen') as popen, \
                         patch.object(runner, 'inspect_transition', return_value=report), \
                         patch.object(runner, 'read_contract', return_value={'permissionProfile':'leapview.permissions/v1'}), \
                         patch.object(runner.upgrade, 'prepare', return_value=('helper', 'request', {'operationDigest':'sha256:'+'d'*64})), \
                         patch.object(runner, 'verify_public_revision') as public, patch('sys.stdout', io.StringIO()):
                        if mode in ('image-only', 'database-upgrade-required'): runner.main()
                        else:
                            with self.assertRaisesRegex(RuntimeError, mode): runner.main()
                        popen.assert_not_called()
                        public.assert_not_called()
                        self.assertEqual(run.call_count, 2)  # temporary inspector upload and cleanup only
                        self.assertIn('rm', run.call_args.args[0])
                        self.assertIn('-f', run.call_args.args[0])
                    self.assertIn(mode, pathlib.Path(env['GITHUB_STEP_SUMMARY']).read_text())
                finally:
                    os.umask(previous_umask)

class RuntimeInspectionTests(unittest.TestCase):
    def test_inspection_reads_both_installed_version_contracts(self):
        runtime = load('demo_compose_runtime')
        revision = 'b' * 40
        image = 'ghcr.io/flidai/leapview@sha256:' + 'a' * 64
        info = {'Config': {'Image': image,
                          'Labels': {'com.docker.compose.project': 'leapview-cfo'},
                          'Env': ['LEAPVIEW_POSTGRES_CONTROL_URL=postgres://demo02-postgres-cfo/leapview_control',
                                  'LEAPVIEW_POSTGRES_DUCKLAKE_URL=postgres://demo02-postgres-cfo/leapview_ducklake']},
                'Mounts': [{'Name': runtime.VOLUME, 'Destination': '/var/lib/leapview'}]}
        for flag, help_text in [(('--json',), 'Flags:\n      --json   emit machine-readable JSON\n'),
                                (('--format', 'json'), 'Flags:\n      --format string   output format: text or json\n')]:
            with self.subTest(flag=flag):
                commands = []
                def output(*args):
                    commands.append(args)
                    if args == ('hostname',): return 'app-leapview-demo-02'
                    if args[:2] == ('docker', 'inspect'): return json.dumps([info])
                    if args[-2:] == ('version', '--help'): return help_text
                    if args == ('docker', 'exec', runtime.APP, 'leapview', 'version', *flag):
                        return json.dumps({'revision': revision, 'dirty': False})
                    raise subprocess.CalledProcessError(1, args)
                with patch.object(runtime, 'out', side_effect=output), patch.object(runtime, 'ready'):
                    self.assertEqual(runtime.inspect(), {'image': image, 'revision': revision})
                self.assertEqual(commands[-1][-len(flag):], flag)

    def test_version_discovery_never_retries_failed_or_invalid_identity(self):
        runtime = load('demo_compose_runtime')
        revision = 'b' * 40
        for label, responses, calls in [
            ('help failure', [subprocess.CalledProcessError(1, 'help')], 1),
            ('unsupported flags', ['Flags:\n      --yaml\n'], 1),
            ('wrong format type', ['Flags:\n      --format int\n'], 1),
            ('version failure', ['      --json\n', subprocess.CalledProcessError(1, 'version')], 2),
            ('malformed JSON', ['      --json\n', 'text version'], 2),
            ('missing dirty', ['      --json\n', json.dumps({'revision': revision})], 2),
            ('dirty', ['      --json\n', json.dumps({'revision': revision, 'dirty': True})], 2),
            ('invalid revision', ['      --json\n', json.dumps({'revision': 'unknown', 'dirty': False})], 2),
        ]:
            with self.subTest(label=label), patch.object(runtime, 'out', side_effect=responses) as output:
                with self.assertRaises((subprocess.CalledProcessError, ValueError, RuntimeError)):
                    runtime.runtime_version()
                self.assertEqual(output.call_count, calls)

class UpgradeGuardTests(unittest.TestCase):
    def setUp(self):
        self.runtime = load('demo_compose_runtime')
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.runtime.PROVIDER = pathlib.Path(self.directory.name)
        self.runtime.ROOT = pathlib.Path(self.directory.name)
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


class RuntimeEvidenceTests(unittest.TestCase):
    def setUp(self):
        self.runtime = load('demo_compose_runtime')
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = pathlib.Path(self.directory.name)/'root'
        self.provider = pathlib.Path(self.directory.name)/'provider'
        self.root.mkdir()
        self.provider.mkdir()
        self.runtime.ROOT = self.root
        self.runtime.PROVIDER = self.provider
        self.image = 'ghcr.io/flidai/leapview@sha256:'+'a'*64
        self.revision = 'b'*40
        release = self.root/'releases'/('sha256-'+'a'*64)
        release.mkdir(parents=True)
        (self.root/'current').symlink_to('releases/'+release.name)
        self.marker = {'image':self.image, 'targetId':'app-leapview-demo-02'}
        self.instance = 'lvinst_'+'i'*32
        self.installation = {
            'version':'leapview-compose-installation-v1',
            'host':'app-leapview-demo-02',
            'installationRoot':str(self.root),
            'hostTargetId':self.marker['targetId'],
            'instanceId':self.instance,
            'image':self.image,
            'revision':self.revision,
            'schema':30,
            'permissionProfile':'leapview.permissions/v1',
            'qualificationRunId':'123456',
            'qualificationAttempt':'2',
            'validatedAt':'2026-09-28T12:34:56Z',
        }

    def write_installation(self, value=None, mode=0o600):
        path = self.provider/'compose-installation.json'
        path.write_text(json.dumps(self.installation if value is None else value))
        path.chmod(mode)
        return path

    def select(self):
        real_fstat = self.runtime.os.fstat
        def as_root(descriptor):
            info = real_fstat(descriptor)
            return types.SimpleNamespace(st_mode=info.st_mode, st_uid=0, st_size=info.st_size)
        with patch.object(self.runtime.os, 'fstat', side_effect=as_root):
            return self.runtime._select_runtime_evidence(
                self.marker, self.image, self.revision, 30, self.instance)

    def test_missing_deployment_receipt_uses_exact_private_installation_evidence(self):
        self.write_installation()
        kind, receipt, installation = self.select()
        self.assertEqual(kind, 'installation')
        self.assertIsNone(receipt)
        self.assertEqual(installation, self.installation)

    def test_malformed_or_mismatched_deployment_receipt_never_falls_back(self):
        self.write_installation()
        receipt = self.provider/'compose-deployment.json'
        receipt.write_text('{malformed')
        receipt.chmod(0o600)
        with self.assertRaises(ValueError): self.select()
        receipt.write_text(json.dumps({'image':'ghcr.io/flidai/leapview@sha256:'+'c'*64,
                                      'revision':self.revision}))
        with self.assertRaisesRegex(ValueError, 'descriptors disagree'): self.select()

    def test_unfinished_maintenance_journal_denies_installation_evidence(self):
        self.write_installation()
        journal = self.root/'upgrade-operation.json'
        journal.write_text(json.dumps({'version':1, 'state':{'phase':'migrating'}}))
        journal.chmod(0o600)
        with self.assertRaisesRegex(ValueError, 'Unfinished schema upgrade'):
            self.select()

    def test_installation_evidence_requires_exact_mode_identity_and_active_payload(self):
        self.write_installation(mode=0o640)
        with self.assertRaisesRegex(ValueError, 'permissions'):
            self.select()
        self.write_installation(dict(self.installation, instanceId='lvinst_'+'x'*32))
        with self.assertRaisesRegex(ValueError, 'live host identity'):
            self.select()
        self.write_installation()
        (self.root/'current').unlink()
        (self.root/'current').symlink_to('releases/sha256-'+'c'*64)
        with self.assertRaisesRegex(ValueError, 'Active host payload'):
            self.select()

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
                        ['compose.yaml', 'compose.postgres.yaml', 'compose.https.yaml', 'compose.first-install-bootstrap.yaml',
                         'Caddyfile', 'Caddyfile.first-install-bootstrap', 'first-install.env',
                         'deployment.env.example', 'leapview.env.example',
                         'leapviewctl', 'leapviewctl-wrapper',
                         'postgres/bundled-entrypoint.sh', 'postgres/bundled-init.sh']}
        for name, data in self.payload.items():
            release_path = self.release/name
            release_path.parent.mkdir(parents=True, exist_ok=True)
            release_path.write_bytes(data)
            link_path = self.root/name
            link_path.parent.mkdir(parents=True, exist_ok=True)
            link_target = os.path.relpath(self.root/'current'/name, link_path.parent)
            link_path.symlink_to(link_target)
        (self.root/'current').symlink_to('releases/'+self.release.name)

    def copy(self, *args):
        if args[:2] == ('docker', 'cp'):
            for name, data in self.payload.items():
                target = pathlib.Path(args[-1])/name
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_bytes(data)

    def stage(self):
        with patch.object(self.rollout, 'out', return_value='container'), patch.object(self.rollout, 'run', side_effect=self.copy):
            return self.rollout.stage_release(self.image)

    def test_new_generation_stages_seed_without_requiring_installed_template(self):
        (self.root/'leapview.env.example').unlink()
        (self.release/'leapview.env.example').unlink()
        (self.root/'leapview.env').write_bytes(b'OPERATOR_SETTING=preserved\n')
        self.image = 'ghcr.io/flidai/leapview@sha256:' + 'b' * 64
        staged = self.stage()
        self.assertEqual(staged.name, 'sha256-' + 'b' * 64)
        self.assertEqual((staged/'leapview.env.example').read_bytes(), self.payload['leapview.env.example'])
        self.assertEqual((self.root/'leapview.env').read_bytes(), b'OPERATOR_SETTING=preserved\n')
        self.assertEqual((self.root/'current').resolve(), self.release)

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

    def add_auxiliary_payload(self):
        self.payload.update({'README.md': b'documentation',
                             'qualification/browser.mjs': b'qualification helper'})

    def test_host_upgrade_generation_accepts_image_auxiliary_files(self):
        before = {name: (self.release/name).stat() for name in self.payload}
        self.add_auxiliary_payload()
        self.assertEqual(self.stage(), self.release)
        released_files = {str(path.relative_to(self.release)) for path in self.release.rglob('*') if path.is_file()}
        self.assertEqual(released_files, set(before))
        for name, previous in before.items():
            current = (self.release/name).stat()
            self.assertEqual((previous.st_ino, previous.st_mtime_ns, previous.st_mode),
                             (current.st_ino, current.st_mtime_ns, current.st_mode))

    def test_new_generation_contains_only_canonical_runtime_payload(self):
        runtime_files = set(self.payload)
        self.add_auxiliary_payload()
        self.image = 'ghcr.io/flidai/leapview@sha256:'+'b'*64
        staged = self.stage()
        staged_files = {str(path.relative_to(staged)) for path in staged.rglob('*') if path.is_file()}
        self.assertEqual(staged_files, runtime_files)
        for name in runtime_files:
            expected = (0o700 if name in ('leapviewctl', 'leapviewctl-wrapper') else
                        0o644 if name.startswith('postgres/') else 0o600)
            self.assertEqual((staged/name).stat().st_mode & 0o777, expected)

    def test_legacy_complete_payload_is_accepted_without_rewriting(self):
        self.add_auxiliary_payload()
        for name, data in self.payload.items():
            target = self.release/name
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(data)
        before = {name: (self.release/name).stat() for name in self.payload}
        self.assertEqual(self.stage(), self.release)
        for name, previous in before.items():
            current = (self.release/name).stat()
            self.assertEqual((previous.st_ino, previous.st_mtime_ns, previous.st_mode),
                             (current.st_ino, current.st_mtime_ns, current.st_mode))

    def test_unverified_extra_files_are_still_rejected(self):
        for name, contents in [('unexpected', b'unknown'), ('README.md', b'modified docs')]:
            with self.subTest(name=name):
                self.add_auxiliary_payload()
                extra = self.release/name
                extra.write_bytes(contents)
                with self.assertRaisesRegex(RuntimeError, 'Existing release'):
                    self.stage()
                self.assertEqual(extra.read_bytes(), contents)
                extra.unlink()

    def test_missing_or_modified_wrapper_is_rejected(self):
        self.payload.pop('leapviewctl-wrapper')
        with self.assertRaisesRegex(RuntimeError, 'payload'):
            self.stage()
        self.payload['leapviewctl-wrapper'] = b'packaged content'
        (self.release/'leapviewctl-wrapper').write_bytes(b'modified wrapper')
        with self.assertRaisesRegex(RuntimeError, 'Existing release'):
            self.stage()

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
            with patch.dict(os.environ,DEMO_PUBLISHER_CLIENT_ID='id',DEMO_PUBLISHER_CLIENT_SECRET='secret',DEMO_PROJECT_ID='project',DEMO_PERMISSION_PROFILE='leapview.permissions/v1'):
                responses=[io.BytesIO(b'{"access_token":"test"}'),io.BytesIO(json.dumps({'buildRevision':revision,'buildDirty':False}).encode())]
                with patch.object(deploy,'demo_urlopen',side_effect=responses) as urlopen:
                    if succeeds: deploy.verify_public_revision('a'*40)
                    else:
                        with self.assertRaises(RuntimeError): deploy.verify_public_revision('a'*40)
                    form = urlopen.call_args_list[0].args[0].data.decode()
                    self.assertIn('scope=delivery.read', form)

class RuntimePinTests(unittest.TestCase):
    def test_successful_record_overrides_legacy_variable(self):
        record=load('demo_runtime_record')
        with tempfile.NamedTemporaryFile() as output, patch.dict(os.environ, GITHUB_OUTPUT=output.name, DEMO_RUNTIME_REVISION='a'*40):
            with patch.object(record,'api',side_effect=[[{'id':1,'sha':'b'*40}],[{'state':'success'}]]), \
                 patch.object(record,'read_contract',return_value={'permissionProfile':'legacy-capabilities/v1'}):
                record.resolve()
            self.assertIn('revision='+'b'*40, pathlib.Path(output.name).read_text())
            self.assertIn('permission_profile=legacy-capabilities/v1', pathlib.Path(output.name).read_text())
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
            with patch.object(record,'api',return_value=[]), \
                 patch.object(record,'read_contract',return_value={'permissionProfile':'legacy-capabilities/v1'}):
                record.resolve()
            self.assertIn('revision='+'a'*40,pathlib.Path(output.name).read_text())



class InstallationMarkerTests(unittest.TestCase):
    def test_image_change_preserves_public_phase_and_updates_generation(self):
        runtime = load('demo_compose_runtime')
        old = 'ghcr.io/flidai/leapview@sha256:' + 'a' * 64
        new = 'ghcr.io/flidai/leapview@sha256:' + 'b' * 64
        marker = {'image': old, 'bootstrapPhase': 'public',
                  'generation': 'sha256-'+'a'*64, 'targetId': 'target'}
        updated = json.loads(runtime.replace_installation_image(json.dumps(marker).encode(), old, new))
        self.assertEqual(updated, dict(marker, image=new, generation='sha256-'+'b'*64))
        for field, value in [('bootstrapPhase', 'private-bootstrap'),
                             ('bootstrapPhase', None), ('generation', 'foreign'), ('image', new)]:
            with self.subTest(field=field, value=value):
                invalid = dict(marker, **{field: value})
                with self.assertRaisesRegex(ValueError, 'public installation marker'):
                    runtime.replace_installation_image(json.dumps(invalid).encode(), old, new)

if __name__ == '__main__': unittest.main()
