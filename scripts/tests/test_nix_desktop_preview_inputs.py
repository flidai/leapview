import copy
import io
import json
from pathlib import Path
import shutil
import subprocess
import sys
import unittest
from unittest.mock import patch
import zipfile

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import nix_desktop_preview_inputs as preview
import test_nix_desktop_attestation as fixtures


class DesktopPreviewInputsTests(unittest.TestCase):
    def setUp(self):
        self.fixture = fixtures.DesktopAttestationTests('runTest')
        self.fixture.setUp()
        self.addCleanup(self.fixture.doCleanups)
        self.inputs = self.fixture.root / 'inputs'
        (self.inputs / 'candidate').mkdir(parents=True)
        shutil.copyfile(self.fixture.original_deb, self.inputs / 'candidate' / preview.inputs.ARCHIVE)
        shutil.copytree(self.fixture.qualified, self.inputs / 'signed')
        self.output = self.fixture.root / 'public-candidate'
        self.commands = []

    def verified(self, *_):
        return {'producer': {'sourceRevision': self.fixture.revision,
                             'signerRevision': self.fixture.revision},
                'binding': self.fixture.signed()}

    def command_run(self, command, **kwargs):
        if command[0] == 'dpkg':
            self.assertEqual(command, ['dpkg', '--compare-versions', '0.0.9', 'lt', '0.1.0'])
            return subprocess.CompletedProcess(command, 0)
        if command[0] == 'node':
            self.commands.append(command)
            return subprocess.CompletedProcess(command, 0)
        return self.fixture.gh(command, **kwargs)

    def stage(self):
        return preview.stage(self.inputs, self.fixture.source, self.fixture.verifier,
                             self.fixture.verifier, 101, 2, self.fixture.revision, self.output)

    def test_exact_signed_installer_and_evidence_are_reused_without_building(self):
        with patch.object(preview.inputs, 'verify', side_effect=self.verified) as verify, \
                patch.object(preview.subprocess, 'run', side_effect=self.command_run):
            result = self.stage()
        self.assertEqual(verify.call_count, 2)
        self.assertEqual(result['binding']['archive']['version'], '0.1.0')
        self.assertEqual((self.output / 'out/make' / preview.inputs.ARCHIVE).read_bytes(),
                         self.fixture.original_deb.read_bytes())
        for source in (self.inputs / 'signed/qualified').iterdir():
            self.assertEqual((self.output / 'out/evidence' / source.name).read_bytes(), source.read_bytes())
        self.assertEqual(self.commands[0][:2], ['node',
            str(self.fixture.verifier / 'desktop/scripts/verify-release-evidence.mjs')])
        self.assertFalse(any('make' == argument for command in self.commands for argument in command))

    def test_foreign_source_version_platform_and_missing_proof_never_stage(self):
        with patch.object(preview.subprocess, 'run', side_effect=self.command_run):
            verified = self.verified()
            for path, value in [('source', 'f' * 40), ('version', '0.1.1'), ('platform', 'linux/arm64')]:
                changed = copy.deepcopy(verified)
                if path == 'source':
                    changed['producer']['sourceRevision'] = value
                else:
                    changed['binding']['archive'][path] = value
                with self.subTest(path=path), patch.object(preview.inputs, 'verify', return_value=changed), \
                        self.assertRaises(ValueError):
                    self.stage()
                self.assertFalse(self.output.exists())
            with patch.object(preview.inputs, 'verify', side_effect=ValueError('signature rejected')), \
                    self.assertRaises(ValueError):
                self.stage()
            self.assertFalse(self.output.exists())

    def test_recheck_failure_and_changed_archive_leave_no_publishable_output(self):
        with patch.object(preview.subprocess, 'run', side_effect=self.command_run):
            verified = self.verified()
            with patch.object(preview.inputs, 'verify', side_effect=[verified, ValueError('expired')]), \
                    self.assertRaises(ValueError):
                self.stage()
            self.assertFalse(self.output.exists())
            (self.inputs / 'candidate' / preview.inputs.ARCHIVE).write_bytes(b'substituted')
            with patch.object(preview.inputs, 'verify', return_value=verified), self.assertRaises(ValueError):
                self.stage()
            self.assertFalse(self.output.exists())

    def lifecycle_fixture(self, verified):
        predecessor = copy.deepcopy(verified)
        predecessor['binding']['archive'].update(version='0.0.9', sha256='sha256:' + 'd' * 64)
        predecessor['producer']['artifacts'] = [{'runId': 100}]
        verified = copy.deepcopy(verified)
        verified['producer']['artifacts'] = [{'runId': 101}]
        report = {'schemaVersion': 1, 'result': 'success', 'verifierRevision': 'a' * 40,
            'scope': 'linux-amd64-preview-saved-profile-version-upgrade-crash-restart-offline-rollback',
            'releaseAdmission': False, 'privateProfileRemoved': True,
            'lifecycle': {'networkIsolated': True, 'packageRemoved': True},
            'inputs': {'predecessor': predecessor, 'candidate': verified}}
        raw = io.BytesIO()
        with zipfile.ZipFile(raw, 'w') as archive:
            archive.writestr('desktop-lifecycle.json', json.dumps(report))
        identity = {'artifactId': 501, 'artifactSHA256': preview.inputs.candidate.digest_bytes(raw.getvalue()),
                    'verifierRevision': 'a' * 40}
        return verified, report, raw.getvalue(), identity

    def test_actual_lifecycle_report_binds_exact_candidate_and_retained_zip(self):
        with patch.object(preview.subprocess, 'run', side_effect=self.command_run):
            verified, report, raw, identity = self.lifecycle_fixture(self.verified())
            with patch.object(preview, 'lifecycle_metadata', return_value=identity), \
                    patch.object(preview.inputs.github, '_github', return_value=raw):
                self.assertEqual(preview.verify_lifecycle(201, 1, verified)['report'], report)
                changed = copy.deepcopy(verified)
                changed['binding']['archive']['sha256'] = 'sha256:' + 'f' * 64
                with self.assertRaisesRegex(ValueError, 'exact preview'):
                    preview.verify_lifecycle(201, 1, changed)
            with patch.object(preview, 'lifecycle_metadata', return_value=identity), \
                    patch.object(preview.inputs.github, '_github', return_value=raw + b'tampered'), \
                    self.assertRaisesRegex(ValueError, 'digest'):
                preview.verify_lifecycle(201, 1, verified)

    def test_lifecycle_requires_successful_main_attempt_and_exact_artifact_origin(self):
        import test_nix_desktop_lifecycle_inputs as metadata
        fixture = metadata.DesktopLifecycleInputsTests('runTest')
        run, artifact = fixture.metadata()
        workflow = {'id': 7, 'path': preview.LIFECYCLE_WORKFLOW}
        run['path'] = preview.LIFECYCLE_WORKFLOW
        artifact.update(name='desktop-lifecycle-101-2', expires_at='2099-01-01T00:00:00Z')
        def endpoints(path):
            if path.endswith('/attempts/2'):
                return run
            if path == 'actions/workflows/nix-desktop-lifecycle.yml':
                return workflow
            return {'artifacts': [artifact]}
        with patch.object(preview.inputs, 'api', side_effect=endpoints):
            self.assertEqual(preview.lifecycle_metadata(101, 2)['artifactId'], 501)
            for field, value in [('conclusion', 'failure'), ('head_branch', 'other'), ('run_attempt', 3)]:
                old = run[field]
                run[field] = value
                with self.subTest(field=field), self.assertRaises(ValueError):
                    preview.lifecycle_metadata(101, 2)
                run[field] = old
            artifact['workflow_run']['repository_id'] = 42
            with self.assertRaises(ValueError):
                preview.lifecycle_metadata(101, 2)
