"""An image-qualified receipt cannot substitute for an exact transition proof."""
import copy
import pathlib
import sys
import unittest

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[1]))
import demo_image_policy as policy


class TransitionAdmissionTests(unittest.TestCase):
    def setUp(self):
        self.run = {'id': 123, 'run_attempt': 2}
        self.image = 'ghcr.io/flidai/leapview@sha256:' + 'a' * 64
        self.revision = 'b' * 40
        self.contract = {'permissionProfile': 'leapview.permissions/v1',
                         'hostTransition': 'compose-postgres-local/v1'}
        self.receipt = dict(version=1, status='passed', runId='123', runAttempt='2',
                            validatorRevision=self.revision, contract=self.contract['hostTransition'],
                            predecessor=dict(image=policy.LEGACY_IMAGE, revision=policy.LEGACY_REVISION,
                                             schema=32, permissionProfile='legacy-capabilities/v1'),
                            candidate=dict(image=self.image, revision=self.revision, schema=44,
                                           permissionProfile=self.contract['permissionProfile']),
                            checks={key: True for key in policy.TRANSITION_CHECKS})

    def admit(self, receipt):
        return policy.admit_transition(self.run, receipt, self.image, self.revision, 44, self.contract)

    def test_exact_complete_transition(self):
        self.assertEqual(self.admit(self.receipt), self.receipt)

    def test_qualification_cannot_be_reused_for_another_build_or_attempt(self):
        alterations = [('runId', '124'), ('runAttempt', '1'), ('validatorRevision', 'c' * 40),
                       ('version', True), ('status', 'skipped'), ('contract', 'unknown')]
        for field, value in alterations:
            with self.subTest(field=field), self.assertRaises(ValueError):
                self.admit(dict(self.receipt, **{field: value}))
        for side in ['predecessor', 'candidate']:
            for field, value in [('image', 'local-test-image'), ('revision', 'c' * 40),
                                 ('schema', 43), ('permissionProfile', 'unknown')]:
                changed = copy.deepcopy(self.receipt)
                changed[side][field] = value
                with self.subTest(side=side, field=field), self.assertRaises(ValueError):
                    self.admit(changed)

    def test_missing_or_non_boolean_assertions_do_not_pass(self):
        for key in policy.TRANSITION_CHECKS:
            for value in [None, False, 'true', 1]:
                changed = copy.deepcopy(self.receipt)
                if value is None:
                    del changed['checks'][key]
                else:
                    changed['checks'][key] = value
                with self.subTest(check=key, value=value), self.assertRaises(ValueError):
                    self.admit(changed)
        for receipt in [None, {}, {'qualified': True}, dict(self.receipt, checks=[])]:
            with self.assertRaises(ValueError):
                self.admit(receipt)


if __name__ == '__main__':
    unittest.main()
