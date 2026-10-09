from datetime import datetime, timezone
import hashlib
import io
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch
import zipfile

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import nix_desktop_lifecycle_inputs as inputs


class DesktopLifecycleInputsTests(unittest.TestCase):
    def metadata(self):
        run = {'id': 101, 'run_attempt': 2, 'status': 'completed', 'conclusion': 'success',
               'event': 'workflow_dispatch', 'path': inputs.WORKFLOW, 'workflow_id': 7,
               'head_branch': 'main', 'head_sha': 'a' * 40,
               'run_started_at': '2026-10-08T10:00:00Z', 'updated_at': '2026-10-08T11:00:00Z',
               'repository': {'id': 9, 'full_name': inputs.REPOSITORY},
               'head_repository': {'id': 9, 'full_name': inputs.REPOSITORY}}
        artifact = {'id': 501, 'name': 'nix-desktop-signed-101-2-amd64', 'expired': False,
                    'digest': 'sha256:' + 'b' * 64, 'size_in_bytes': 1200,
                    'created_at': '2026-10-08T10:30:00Z', 'expires_at': '2026-10-22T10:30:00Z',
                    'workflow_run': {'id': 101, 'head_sha': 'a' * 40, 'head_branch': 'main',
                                     'repository_id': 9, 'head_repository_id': 9}}
        return run, artifact

    def authorize(self, run, artifact):
        return inputs.authorize(run, {'id': 7, 'path': inputs.WORKFLOW}, artifact,
                                101, 2, 'signed', now=datetime(2026, 10, 9, tzinfo=timezone.utc))

    def test_authenticated_whole_success_exact_attempt(self):
        run, artifact = self.metadata()
        self.assertEqual(self.authorize(run, artifact)['artifactId'], 501)
        for field, value in [('conclusion', 'failure'), ('status', 'in_progress'), ('head_branch', 'other'),
                             ('run_attempt', 3), ('path', 'untrusted.yml'), ('event', 'pull_request')]:
            changed = dict(run, **{field: value})
            with self.subTest(field=field), self.assertRaises(ValueError):
                self.authorize(changed, artifact)
        for field, value in [('expired', True), ('name', 'nix-desktop-signed-101-1-amd64'),
                             ('digest', 'missing'), ('created_at', '2026-10-08T09:00:00Z')]:
            with self.subTest(field=field), self.assertRaises(ValueError):
                self.authorize(run, dict(artifact, **{field: value}))
        artifact['workflow_run']['repository_id'] = 10
        with self.assertRaises(ValueError):
            self.authorize(run, artifact)

    def test_retained_stage_is_rechecked_against_current_api_and_original_zip(self):
        run, artifact = self.metadata()
        artifact['name'] = 'nix-desktop-candidate-101-2-amd64'
        data = self.bundle(inputs.ARCHIVE)
        artifact['digest'] = 'sha256:' + hashlib.sha256(data).hexdigest()
        artifact['expires_at'] = '2099-01-01T00:00:00Z'
        identity = inputs.authorize(run, {'id': 7, 'path': inputs.WORKFLOW}, artifact, 101, 2, 'candidate')
        with tempfile.TemporaryDirectory() as temporary, patch.object(inputs, 'api', return_value=artifact):
            directory = Path(temporary)
            (directory / 'candidate.zip').write_bytes(data)
            inputs.extract(data, artifact['digest'], directory / 'candidate', 'candidate')
            inputs.verify_retained(directory, identity, run, {'id': 7, 'path': inputs.WORKFLOW}, 101, 2)
            (directory / 'candidate' / inputs.ARCHIVE).write_bytes(b'substituted after download')
            with self.assertRaisesRegex(ValueError, 'differs'):
                inputs.verify_retained(directory, identity, run, {'id': 7, 'path': inputs.WORKFLOW}, 101, 2)
            with self.assertRaises(ValueError):
                inputs.verify_retained(directory, identity, run, {'id': 7, 'path': inputs.WORKFLOW}, 101, 3)

    def retained_attempt(self):
        run, template = self.metadata()
        workflow = {'id': 7, 'path': inputs.WORKFLOW}
        artifacts, archives, identities = [], {}, []
        for index, phase in enumerate(inputs.PHASES):
            data = io.BytesIO()
            with zipfile.ZipFile(data, 'w') as archive:
                prefix = '' if phase == 'candidate' else 'candidate/'
                archive.writestr(prefix + inputs.ARCHIVE, b'candidate bytes')
                if phase != 'candidate':
                    archive.writestr('qualified/candidate-manifest.json',
                                     json.dumps({'source': {'revision': 'c' * 40}}))
                    for item in range(6):
                        archive.writestr(f'qualified/evidence-{item}.json', b'{}')
            raw = data.getvalue()
            artifact = dict(template, id=501 + index,
                            name=f'nix-desktop-{phase}-101-2-amd64',
                            digest=inputs.candidate.digest_bytes(raw), size_in_bytes=len(raw),
                            expires_at='2099-01-01T00:00:00Z')
            artifacts.append(artifact)
            archives[f'actions/artifacts/{artifact["id"]}/zip'] = raw
            identities.append(inputs.authorize(run, workflow, artifact, 101, 2, phase))
        endpoints = {
            'actions/runs/101': dict(run, run_attempt=3, conclusion='failure'),
            'actions/runs/101/attempts/2': run,
            'actions/workflows/nix-desktop-candidate.yml': workflow,
            'actions/runs/101/artifacts?per_page=100&page=1': {'artifacts': artifacts},
            **{f'actions/artifacts/{artifact["id"]}': artifact for artifact in artifacts},
        }
        identity = {'sourceRevision': 'c' * 40, 'signerRevision': run['head_sha'],
                    'artifacts': identities}
        return endpoints, archives, identity

    def test_fetch_selects_successful_earlier_attempt_after_failed_rerun(self):
        endpoints, archives, expected = self.retained_attempt()
        with tempfile.TemporaryDirectory() as temporary, \
                patch.object(inputs, 'api', side_effect=endpoints.__getitem__) as api, \
                patch.object(inputs.github, '_github', side_effect=lambda path, **_: archives[path]):
            directory = Path(temporary) / 'inputs'
            self.assertEqual(inputs.fetch(101, 2, directory), expected)
            self.assertEqual(inputs.candidate.read_json_file(directory / 'inputs.json'), expected)
            api.assert_any_call('actions/runs/101/attempts/2')
            self.assertNotIn(unittest.mock.call('actions/runs/101'), api.call_args_list)

    def test_verify_selects_successful_earlier_attempt_after_failed_rerun(self):
        endpoints, archives, identity = self.retained_attempt()
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            inputs.attestation.desktop.write_json(directory / 'inputs.json', identity)
            for record in identity['artifacts']:
                raw = archives[f'actions/artifacts/{record["artifactId"]}/zip']
                (directory / (record['phase'] + '.zip')).write_bytes(raw)
                inputs.extract(raw, record['artifactSHA256'], directory / record['phase'], record['phase'])
            with patch.object(inputs, 'api', side_effect=endpoints.__getitem__) as api, \
                    patch.object(inputs.attestation, 'verify_signed', return_value={'verified': True}) as verify_signed:
                result = inputs.verify(directory, 'source-root', 'signer-root', 101, 2)
                self.assertEqual(result, {'producer': identity, 'binding': {'verified': True}})
                api.assert_any_call('actions/runs/101/attempts/2')
                self.assertNotIn(unittest.mock.call('actions/runs/101'), api.call_args_list)
                for record in identity['artifacts']:
                    api.assert_any_call(f'actions/artifacts/{record["artifactId"]}')
                verify_signed.assert_called_once_with(
                    directory / 'candidate' / inputs.ARCHIVE,
                    directory / 'qualified', directory / 'signed', 'source-root', 'signer-root',
                    identity['sourceRevision'], identity['signerRevision'])

    def test_duplicate_members_and_extra_inventory_are_rejected(self):
        data = io.BytesIO()
        with zipfile.ZipFile(data, 'w') as archive:
            archive.writestr('candidate/' + inputs.ARCHIVE, b'candidate')
            archive.writestr('qualified/arbitrary.json', b'{}')
        with tempfile.TemporaryDirectory() as temporary, self.assertRaises(ValueError):
            inputs.extract(data.getvalue(), inputs.candidate.digest_bytes(data.getvalue()), Path(temporary) / 'bad', 'signed')

    def bundle(self, name, mode=None):
        data = io.BytesIO()
        with zipfile.ZipFile(data, 'w') as archive:
            info = zipfile.ZipInfo(name)
            if mode:
                info.external_attr = mode << 16
            archive.writestr(info, b'candidate bytes')
        return data.getvalue()

    def test_extract_verifies_digest_and_never_accepts_paths_or_links(self):
        with tempfile.TemporaryDirectory() as root:
            for index, (name, mode) in enumerate([('../escape', None), ('/absolute', None),
                                                (inputs.ARCHIVE, 0o120777), ('extra.txt', None)]):
                data = self.bundle(name, mode)
                with self.subTest(name=name), self.assertRaises(ValueError):
                    inputs.extract(data, 'sha256:' + hashlib.sha256(data).hexdigest(), Path(root) / str(index), 'candidate')
            data = self.bundle(inputs.ARCHIVE)
            with self.assertRaises(ValueError):
                inputs.extract(data, 'sha256:' + '0' * 64, Path(root) / 'wrong', 'candidate')
            destination = Path(root) / 'valid'
            inputs.extract(data, 'sha256:' + hashlib.sha256(data).hexdigest(), destination, 'candidate')
            self.assertEqual((destination / inputs.ARCHIVE).read_bytes(), b'candidate bytes')
            with self.assertRaises(FileExistsError):
                inputs.extract(data, 'sha256:' + hashlib.sha256(data).hexdigest(), destination, 'candidate')


if __name__ == '__main__':
    unittest.main()
