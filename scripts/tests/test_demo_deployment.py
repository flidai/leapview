"""Behavioral tests for the hosted demo deployment boundary (no host access)."""
import importlib.util
import pathlib
import unittest
from unittest.mock import patch
import tempfile
import os
import io
import json

ROOT = pathlib.Path(__file__).resolve().parents[2]
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
