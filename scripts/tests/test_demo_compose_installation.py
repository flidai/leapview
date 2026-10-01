"""First-install Compose evidence is tied to admitted and live host identity."""
import contextlib
import importlib.util
import datetime as dt
import io
import json
import os
import pathlib
import sys
import tempfile
from types import SimpleNamespace
import unittest
import zipfile
from unittest.mock import ANY, patch


ROOT = pathlib.Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'scripts'))
sys.path.insert(0, str(ROOT / 'scripts' / 'bootstrap'))
import compose_installation as operator
import compose_installation_writer as writer


IMAGE = 'ghcr.io/flidai/leapview@sha256:' + 'a' * 64
REVISION = 'c' * 40
INSTANCE = 'lvinst_' + 'A' * 32
TARGET = 'project:leapview-demo'
PROFILE = 'leapview.permissions/v1'
RUN_ID = '12345'
ATTEMPT = '2'


def qualification_archive():
    stream = io.BytesIO()
    with zipfile.ZipFile(stream, 'w') as archive:
        archive.writestr('qualification.json', '{"receipt":"fresh"}')
        archive.writestr('transition.json', '{"receipt":"transition"}')
    return stream.getvalue()


class ComposeInstallationOperatorTests(unittest.TestCase):
    def test_qualification_identity_uses_exact_run_attempt_artifact(self):
        calls = []

        def api(path):
            calls.append(path)
            if path == 'actions/runs/12345':
                return json.dumps({'id': 12345, 'run_attempt': 2})
            if path == 'actions/runs/12345/artifacts?per_page=100':
                return json.dumps({'artifacts': [
                    {'id': 99, 'name': 'production-image-qualification-2', 'expired': False}]})
            if path == 'actions/artifacts/99/zip':
                return qualification_archive()
            raise AssertionError(path)

        with patch.object(operator.image_policy, 'api', side_effect=api), \
             patch.object(operator.image_policy, 'admit', return_value=REVISION) as admit, \
             patch.object(operator, 'source_schema', return_value=(45, {})), \
             patch.object(operator, 'read_contract', return_value={'permissionProfile': PROFILE}) as contract, \
             patch.object(operator.image_policy, 'admit_transition') as transition:
            result = operator.qualification_identity(RUN_ID, IMAGE)

        self.assertEqual(result, {
            'image': IMAGE, 'revision': REVISION, 'schema': 45,
            'permissionProfile': PROFILE, 'qualificationRunId': RUN_ID,
            'qualificationAttempt': ATTEMPT,
        })
        self.assertEqual(calls[-1], 'actions/artifacts/99/zip')
        admit.assert_called_once()
        contract.assert_called_once_with(REVISION)
        transition.assert_called_once()

    def test_validation_failure_never_invokes_root_writer(self):
        with patch.object(operator, 'qualification_identity', return_value={
                'image': IMAGE, 'revision': REVISION, 'schema': 45,
                'permissionProfile': PROFILE, 'qualificationRunId': RUN_ID,
                'qualificationAttempt': ATTEMPT}), \
             patch.dict(os.environ, {'DEMO_HOST': operator.deploy.HOST}, clear=True), \
             patch.object(operator.deploy, 'verify_public_revision'), \
             patch.object(operator.deploy, 'verified_demo_ssh',
                          return_value=contextlib.nullcontext(['ssh', 'root@demo-02'])), \
             patch.object(operator.deploy, 'verify_publication', side_effect=RuntimeError('validation failed')), \
             patch.object(operator, '_invoke_writer') as invoke:
            with self.assertRaisesRegex(RuntimeError, 'validation failed'):
                operator.main(['--image', IMAGE, '--qualification-run', RUN_ID])
        invoke.assert_not_called()

    def test_successful_validation_precedes_writer_invocation(self):
        events = []
        identity = {
            'image': IMAGE, 'revision': REVISION, 'schema': 45,
            'permissionProfile': PROFILE, 'qualificationRunId': RUN_ID,
            'qualificationAttempt': ATTEMPT,
        }
        with patch.object(operator, 'qualification_identity', side_effect=lambda *_: events.append('qualified') or identity), \
             patch.dict(os.environ, {'DEMO_HOST': operator.deploy.HOST}, clear=True), \
             patch.object(operator.deploy, 'verified_demo_ssh') as verified_ssh, \
             patch.object(operator.deploy, 'verify_public_revision', side_effect=lambda *_args: events.append('public_revision')), \
             patch.object(operator.deploy, 'verify_publication', side_effect=lambda **_kwargs: events.append('validated')), \
             patch.object(operator, '_invoke_writer', side_effect=lambda _identity, _ssh: events.append('written')), \
             contextlib.redirect_stdout(io.StringIO()):
            verified_ssh.return_value = contextlib.nullcontext(['ssh', 'root@demo-02'])
            operator.main(['--image', IMAGE, '--qualification-run', RUN_ID])
        self.assertEqual(events, ['qualified', 'public_revision', 'validated', 'written'])
        verified_ssh.assert_called_once()

    def test_validation_and_writer_share_one_secret_consuming_ssh_context(self):
        identity = {
            'image': IMAGE, 'revision': REVISION, 'schema': 45,
            'permissionProfile': PROFILE, 'qualificationRunId': RUN_ID,
            'qualificationAttempt': ATTEMPT,
        }
        ssh = ['ssh', '-i', '/tmp/key', 'root@demo-02']
        events = []

        @contextlib.contextmanager
        def verified_context():
            key = os.environ.pop('DEMO_SSH_PRIVATE_KEY')
            self.assertEqual(key, 'private-key-bytes')
            events.append(('ssh-context', key))
            try:
                yield ssh
            finally:
                os.environ.pop('DEMO_SSH_PRIVATE_KEY', None)

        ssh_expected = ssh
        def validate_browser(*, ssh, revision_verified):
            self.assertEqual(ssh, ssh_expected)
            self.assertTrue(revision_verified)
            self.assertNotIn('DEMO_SSH_PRIVATE_KEY', os.environ)
            events.append('validated')

        with patch.object(operator, 'qualification_identity', return_value=identity), \
             patch.dict(os.environ, {
                 'DEMO_HOST': operator.deploy.HOST,
                 'DEMO_SSH_PRIVATE_KEY': 'private-key-bytes',
             }, clear=True), \
             patch.object(operator.deploy, 'verify_public_revision', side_effect=lambda *_args: events.append('public_revision')), \
             patch.object(operator.deploy, 'verified_demo_ssh', side_effect=verified_context), \
             patch.object(operator.deploy, 'verify_publication', side_effect=validate_browser), \
             patch.object(operator, '_invoke_writer', side_effect=lambda got_identity, got_ssh: (
                 self.assertEqual(got_identity, identity),
                 self.assertEqual(got_ssh, ssh_expected),
                 self.assertNotIn('DEMO_SSH_PRIVATE_KEY', os.environ),
                 events.append('written'))), \
             contextlib.redirect_stdout(io.StringIO()):
            operator.main(['--image', IMAGE, '--qualification-run', RUN_ID])

        self.assertEqual(events, ['public_revision', ('ssh-context', 'private-key-bytes'), 'validated', 'written'])
        self.assertNotIn('DEMO_SSH_PRIVATE_KEY', os.environ)

    def test_clone_origin_cannot_create_live_installation_evidence(self):
        identity = {'revision': REVISION, 'permissionProfile': PROFILE}
        with patch.dict(os.environ, {
                'DEMO_HOST': operator.deploy.HOST,
                'DEMO_TARGET': 'https://clone.invalid'}, clear=True):
            with self.assertRaisesRegex(ValueError, 'public demo origin'):
                operator._prepare_live_validation(identity)


class ComposeInstallationWriterTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = pathlib.Path(self.temp.name) / 'install'
        self.provider = pathlib.Path(self.temp.name) / 'provider'
        self.root.mkdir(mode=0o700)
        self.provider.mkdir(mode=0o700)
        self.observed = {
            'host': writer.EXPECTED_HOST,
            'installationRoot': str(self.root),
            'hostTargetId': TARGET,
            'instanceId': INSTANCE,
            'image': IMAGE,
            'revision': REVISION,
            'schema': 45,
        }

    def test_local_ready_and_instance_endpoints_match_the_public_contract(self):
        class Opener:
            def __init__(self):
                self.requests = []
                self.responses = iter((b'{"status":"ready"}',
                                       json.dumps({'id': INSTANCE}).encode()))

            def open(self, request, timeout):
                self.requests.append((request, timeout))
                return io.BytesIO(next(self.responses))

        opener = Opener()
        self.assertEqual(writer._ready_and_instance(opener), INSTANCE)
        self.assertEqual([request.full_url for request, _ in opener.requests], [
            'http://127.0.0.1:8081/readyz',
            'http://127.0.0.1:8081/api/v1/instance',
        ])
        self.assertTrue(all(request.get_header('Host') == writer.PUBLIC_HOST
                            for request, _ in opener.requests))

    def test_write_is_private_idempotent_and_contains_no_unmodeled_fields(self):
        timestamp = dt.datetime(2026, 9, 29, 3, 0, tzinfo=dt.timezone.utc)
        with patch.object(writer.os, 'geteuid', return_value=0), \
             patch.object(writer, '_acquire_host_locks', return_value=[]), \
             patch.object(writer, 'inspect_live', return_value=self.observed), \
             patch.object(writer.os, 'fchown') as chown:
            record = writer.write_evidence(
                image=IMAGE, revision=REVISION, schema=45, permission_profile=PROFILE,
                qualification_run_id=RUN_ID, qualification_attempt=ATTEMPT,
                root=self.root, provider=self.provider, now=timestamp,
                inspect=lambda *_args, **_kwargs: self.observed)
        path = self.provider / writer.RECEIPT
        self.assertEqual(path.read_text(), json.dumps(record, sort_keys=True, indent=2) + '\n')
        self.assertEqual(path.stat().st_mode & 0o777, 0o600)
        chown.assert_called_once_with(ANY, 0, 0)
        self.assertEqual(set(record), {
            'version', 'host', 'installationRoot', 'hostTargetId', 'instanceId', 'image',
            'revision', 'schema', 'permissionProfile', 'qualificationRunId',
            'qualificationAttempt', 'validatedAt',
        })
        self.assertEqual(record['version'], 'leapview-compose-installation-v1')
        self.assertEqual(record['validatedAt'], '2026-09-29T03:00:00Z')

        with patch.object(writer.os, 'geteuid', return_value=0), \
             patch.object(writer, '_acquire_host_locks', return_value=[]), \
             patch.object(writer, 'inspect_live', return_value=self.observed), \
             patch.object(writer, '_private_json', return_value=record), \
             patch.object(writer, '_atomic_root_private') as atomic:
            retry = writer.write_evidence(
                image=IMAGE, revision=REVISION, schema=45, permission_profile=PROFILE,
                qualification_run_id=RUN_ID, qualification_attempt=ATTEMPT,
                root=self.root, provider=self.provider,
                inspect=lambda *_args, **_kwargs: self.observed)
        self.assertEqual(retry, record)
        atomic.assert_not_called()

    def test_existing_mismatched_receipt_is_never_overwritten(self):
        with patch.object(writer.os, 'geteuid', return_value=0), \
             patch.object(writer, '_acquire_host_locks', return_value=[]), \
             patch.object(writer, 'inspect_live', return_value=self.observed), \
             patch.object(writer, '_private_json', return_value={
                 'version': 'wrong', 'validatedAt': '2026-09-29T00:00:00Z'}), \
             patch.object(writer, '_atomic_root_private') as atomic:
            with self.assertRaisesRegex(ValueError, 'does not match current live state'):
                writer.write_evidence(
                    image=IMAGE, revision=REVISION, schema=45, permission_profile=PROFILE,
                    qualification_run_id=RUN_ID, qualification_attempt=ATTEMPT,
                    root=self.root, provider=self.provider,
                    inspect=lambda *_args, **_kwargs: self.observed)
        atomic.assert_not_called()

    def test_existing_receipt_must_keep_exact_private_mode(self):
        path = self.provider / writer.RECEIPT
        path.write_text('{}\n')
        path.chmod(0o400)
        with patch.object(writer, '_fstat', return_value=SimpleNamespace(
                st_mode=writer.stat.S_IFREG | 0o400, st_uid=0, st_size=3)):
            with self.assertRaisesRegex(ValueError, 'permissions'):
                writer._private_json(path, 'installation evidence receipt', 16384,
                                     required_mode=0o600)

    def test_root_and_maintenance_gates_fail_closed(self):
        with patch.object(writer.os, 'geteuid', return_value=1000):
            with self.assertRaisesRegex(ValueError, 'must be written as root'):
                writer.write_evidence(
                    image=IMAGE, revision=REVISION, schema=45, permission_profile=PROFILE,
                    qualification_run_id=RUN_ID, qualification_attempt=ATTEMPT,
                    root=self.root, provider=self.provider)

        (self.root / 'upgrade-operation.json').write_text('{}')
        with patch.object(writer.os, 'geteuid', return_value=0), \
             patch.object(writer, '_acquire_host_locks', return_value=[]), \
             patch.object(writer, 'inspect_live', return_value=self.observed):
            with self.assertRaisesRegex(ValueError, 'maintenance operation'):
                writer.write_evidence(
                    image=IMAGE, revision=REVISION, schema=45, permission_profile=PROFILE,
                    qualification_run_id=RUN_ID, qualification_attempt=ATTEMPT,
                    root=self.root, provider=self.provider)

    def test_live_inspection_matches_runtime_marker_schema_and_instance(self):
        payloads = {name: ('payload-' + name).encode() for name in writer.PAYLOAD_MODES}
        container = {
            'State': {'Running': True},
            'Config': {'Image': IMAGE, 'Labels': {
                'com.docker.compose.project': writer.COMPOSE_PROJECT,
                'com.docker.compose.project.working_dir': str(self.root)}},
            'Mounts': [{'Name': writer.APP_VOLUME, 'Destination': '/var/lib/leapview'}],
            'HostConfig': {'PortBindings': {'8080/tcp': [
                {'HostIp': '127.0.0.1', 'HostPort': '8081'}]}},
            'Image': 'sha256:' + 'b' * 64,
        }
        image_info = {'Id': container['Image'], 'RepoDigests': [IMAGE]}
        calls = []

        def output(*args):
            calls.append(args)
            if args[:3] == ('docker', 'inspect', writer.APP):
                return json.dumps([container])
            if args[:4] == ('docker', 'image', 'inspect', IMAGE):
                return json.dumps([image_info])
            if args[:4] == ('docker', 'exec', writer.APP, 'leapview'):
                return json.dumps({'revision': REVISION, 'dirty': False})
            if args[:3] == ('docker', 'create', IMAGE):
                return 'container-id'
            if args[:4] == ('docker', 'exec', writer.POSTGRES, 'sh'):
                query = args[-1]
                return '45' if 'goose_db_version' in query else INSTANCE
            raise AssertionError(args)

        def run(*args):
            if args[:2] == ('docker', 'cp'):
                destination = pathlib.Path(args[-1])
                for name, value in payloads.items():
                    (destination / name).write_bytes(value)
            elif args[:2] != ('docker', 'rm'):
                raise AssertionError(args)

        env = ('LEAPVIEW_IMAGE=' + IMAGE + '\n' +
               'COMPOSE_PROJECT_NAME=leapview-cfo\n' +
               'COMPOSE_APP_BIND=127.0.0.1:8081\n' +
               'COMPOSE_HTTPS=1\n' +
               'CADDY_DOMAIN=demo.leapview.dev\n').encode()
        with patch.object(writer, '_private_directory'), \
             patch.object(writer, '_private_json', return_value={
                 'schemaVersion': 1, 'image': IMAGE, 'domain': writer.PUBLIC_HOST,
                 'https': True, 'targetId': TARGET}), \
             patch.object(writer, '_private_file', return_value=env), \
             patch.object(writer, '_verify_active_payload') as active_payload, \
             patch.object(writer, '_ready_and_instance', return_value=INSTANCE), \
             patch.object(writer, 'urllib') as unused, \
             patch.object(writer, '_image_payload', wraps=writer._image_payload):
            observed = writer.inspect_live(
                IMAGE, REVISION, 45, root=self.root, provider=self.provider,
                output=output, run=run, hostname=writer.EXPECTED_HOST)

        self.assertEqual(observed, {
            'host': writer.EXPECTED_HOST, 'installationRoot': str(self.root),
            'hostTargetId': TARGET, 'instanceId': INSTANCE, 'image': IMAGE,
            'revision': REVISION, 'schema': 45,
        })
        active_payload.assert_called_once_with(self.root, IMAGE, payloads)
        self.assertTrue(calls)

    def test_live_inspection_rejects_wrong_container_image_and_schema(self):
        with patch.object(writer, '_private_directory'), \
             patch.object(writer, '_private_json', return_value={
                 'schemaVersion': 1, 'image': IMAGE, 'domain': writer.PUBLIC_HOST,
                 'https': True, 'targetId': TARGET}), \
             patch.object(writer, '_private_file', return_value=(
                 'LEAPVIEW_IMAGE=' + IMAGE + '\nCOMPOSE_PROJECT_NAME=leapview-cfo\n'
                 'COMPOSE_APP_BIND=127.0.0.1:8081\nCOMPOSE_HTTPS=1\n'
                 'CADDY_DOMAIN=demo.leapview.dev\n').encode()), \
             patch.object(writer, '_output', return_value=json.dumps([{
                 'State': {'Running': True},
                 'Config': {'Image': 'ghcr.io/flidai/leapview:latest', 'Labels': {}},
             }])) as output:
            with self.assertRaisesRegex(ValueError, 'Live application container'):
                writer.inspect_live(IMAGE, REVISION, 45, root=self.root,
                                    provider=self.provider, hostname=writer.EXPECTED_HOST,
                                    output=output)

    def test_active_generation_must_match_image_bytes(self):
        release = self.root / 'releases' / ('sha256-' + 'a' * 64)
        release.parent.mkdir(mode=0o700)
        release.mkdir(mode=0o700)
        (release / 'leapviewctl').write_bytes(b'changed')
        (release / 'leapviewctl').chmod(0o700)
        for name, mode in writer.PAYLOAD_MODES.items():
            if name == 'leapviewctl':
                continue
            (release / name).write_bytes(b'expected')
            (release / name).chmod(mode)
        (self.root / 'current').symlink_to('releases/sha256-' + 'a' * 64)
        expected = {name: b'expected' for name in writer.PAYLOAD_MODES}
        original_lstat = pathlib.Path.lstat

        def root_owned_lstat(path):
            result = original_lstat(path)
            return SimpleNamespace(st_mode=result.st_mode, st_uid=0)

        with patch.object(pathlib.Path, 'lstat', root_owned_lstat), \
             self.assertRaisesRegex(ValueError, 'differs from the immutable image'):
            writer._verify_active_payload(self.root, IMAGE, expected)


if __name__ == '__main__':
    unittest.main()
