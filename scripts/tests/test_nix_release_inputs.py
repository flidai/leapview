from datetime import datetime, timezone
import io
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch
import zipfile

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import nix_release_inputs as inputs


class ReleaseInputsTests(unittest.TestCase):
    def metadata(self, kind='application-image', phase='qualified'):
        workflow = inputs.PRODUCERS[kind]
        run = {'id': 101, 'run_attempt': 2, 'status': 'completed', 'conclusion': 'success',
               'event': 'workflow_dispatch', 'path': workflow, 'workflow_id': 7,
               'head_branch': 'main', 'head_sha': 'a' * 40,
               'run_started_at': '2026-10-09T07:00:00Z', 'updated_at': '2026-10-09T08:00:00Z',
               'repository': {'id': 9, 'full_name': inputs.REPOSITORY},
               'head_repository': {'id': 9, 'full_name': inputs.REPOSITORY}}
        artifact = {'id': 501, 'name': inputs.artifact_name(kind, phase, 101, 2, 'amd64'),
                    'expired': False, 'digest': 'sha256:' + 'b' * 64, 'size_in_bytes': 1234,
                    'created_at': '2026-10-09T07:30:00Z', 'expires_at': '2099-01-01T00:00:00Z',
                    'workflow_run': {'id': 101, 'head_sha': 'a' * 40, 'head_branch': 'main',
                                     'repository_id': 9, 'head_repository_id': 9}}
        return run, {'id': 7, 'path': workflow}, artifact

    def authorize(self, run, workflow, artifact, kind='application-image', phase='qualified', arch='amd64'):
        return inputs.authorize(run, workflow, artifact, kind, phase, 101, 2, arch,
                                now=datetime(2026, 10, 9, 9, tzinfo=timezone.utc))

    def test_independent_image_kinds_require_exact_successful_attempt(self):
        for kind in inputs.PRODUCERS:
            run, workflow, artifact = self.metadata(kind)
            self.assertEqual(self.authorize(run, workflow, artifact, kind)['artifactId'], 501)
            for key, value in [('run_attempt', 3), ('conclusion', 'failure'), ('head_branch', 'other'),
                               ('event', 'pull_request'), ('path', '.github/workflows/release.yml')]:
                with self.subTest(kind=kind, key=key), self.assertRaises(ValueError):
                    self.authorize(dict(run, **{key: value}), workflow, artifact, kind)
            for key, value in [('expired', True), ('digest', ''), ('name', 'wrong-architecture'),
                               ('created_at', '2026-10-09T06:00:00Z')]:
                with self.subTest(kind=kind, key=key), self.assertRaises(ValueError):
                    self.authorize(run, workflow, dict(artifact, **{key: value}), kind)
            with self.assertRaises(ValueError):
                self.authorize(run, workflow, artifact, kind, arch='arm64')
        run, workflow, artifact = self.metadata()
        artifact['workflow_run']['repository_id'] = 10
        with self.assertRaises(ValueError):
            self.authorize(run, workflow, artifact)

    def zip(self, entries):
        data = io.BytesIO()
        with zipfile.ZipFile(data, 'w') as archive:
            for name, content in entries:
                archive.writestr(name, content)
        return data.getvalue()

    def test_extract_rejects_substitution_traversal_duplicate_and_symlink(self):
        safe = self.zip([('image.tar', b'image'), ('runtime/candidate-manifest.json', b'{}')])
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            inputs.extract(safe, inputs.candidate.digest_bytes(safe), root / 'safe')
            self.assertEqual((root / 'safe/image.tar').read_bytes(), b'image')
            with self.assertRaises(ValueError):
                inputs.extract(safe, 'sha256:' + 'a' * 64, root / 'substitution')
            for index, names in enumerate([['../escape'], ['/escape'], ['runtime\\escape'],
                                           ['a/../escape'], ['same', 'same'], ['same', 'same/child']]):
                raw = self.zip([(name, b'x') for name in names])
                with self.subTest(names=names), self.assertRaises(ValueError):
                    inputs.extract(raw, inputs.candidate.digest_bytes(raw), root / str(index))
            data = io.BytesIO()
            with zipfile.ZipFile(data, 'w') as archive:
                member = zipfile.ZipInfo('image.tar')
                member.external_attr = 0o120777 << 16
                archive.writestr(member, '../secret')
            with self.assertRaises(ValueError):
                inputs.extract(data.getvalue(), inputs.candidate.digest_bytes(data.getvalue()), root / 'link')

    def test_retained_bytes_are_rechecked_and_changed_stage_fails(self):
        run, workflow, artifact = self.metadata()
        raw = self.zip([('image.tar', b'image'), ('runtime/candidate-manifest.json', b'{}')])
        artifact['digest'], artifact['size_in_bytes'] = inputs.candidate.digest_bytes(raw), len(raw)
        identity = self.authorize(run, workflow, artifact)
        with tempfile.TemporaryDirectory() as temporary, patch.object(inputs, 'api', return_value=artifact):
            root = Path(temporary)
            (root / 'qualified.zip').write_bytes(raw)
            inputs.extract(raw, artifact['digest'], root / 'qualified')
            inputs.verify_retained(root, identity, run, workflow)
            (root / 'qualified/image.tar').write_bytes(b'changed')
            with self.assertRaisesRegex(ValueError, 'differ'):
                inputs.verify_retained(root, identity, run, workflow)

    def test_fetch_uses_selected_attempt_and_requires_all_independent_phases(self):
        run, workflow, template = self.metadata()
        artifacts, archives = [], {}
        for index, phase in enumerate(inputs.PHASES):
            raw = self.zip([('report.json', b'{}')])
            artifact = dict(template, id=501 + index, name=inputs.artifact_name('application-image', phase, 101, 2, 'amd64'),
                            digest=inputs.candidate.digest_bytes(raw), size_in_bytes=len(raw))
            artifacts.append(artifact)
            archives[f'actions/artifacts/{artifact["id"]}/zip'] = raw
        endpoints = {'actions/runs/101/attempts/2': run,
                     'actions/workflows/nix-candidate.yml': workflow,
                     'actions/runs/101/artifacts?per_page=100&page=1': {'artifacts': artifacts}}
        with tempfile.TemporaryDirectory() as temporary, patch.object(inputs, 'api', side_effect=endpoints.__getitem__) as api, \
                patch.object(inputs.github, '_github', side_effect=lambda path, **_: archives[path]):
            receipt = inputs.fetch('application-image', 101, 2, 'amd64', Path(temporary) / 'inputs')
            self.assertEqual([item['phase'] for item in receipt['artifacts']], list(inputs.PHASES))
            api.assert_any_call('actions/runs/101/attempts/2')
            self.assertNotIn(unittest.mock.call('actions/runs/101'), api.call_args_list)
            artifacts.pop()
            with self.assertRaisesRegex(ValueError, 'one exact'):
                inputs.fetch('application-image', 101, 2, 'amd64', Path(temporary) / 'missing')


if __name__ == '__main__':
    unittest.main()
