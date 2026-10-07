"""Protect exact-image qualification from unrelated workflow cancellation."""
from pathlib import Path
import re
import unittest


WORKFLOW = Path(__file__).resolve().parents[2] / '.github/workflows/demo-upgrade-qualification.yml'


class QualificationConcurrencyTests(unittest.TestCase):
    def context(self, image='', pull_request='', ref='refs/heads/main'):
        return {'github.workflow': 'Build / Main image',
                'inputs.candidate_image': image,
                'github.event.pull_request.number': pull_request,
                'github.ref': ref}

    def key(self, context):
        group = re.search(r'(?m)^  group: (.+)$', WORKFLOW.read_text())
        self.assertIsNotNone(group, 'qualification must declare its concurrency scope')

        def resolve(expression):
            # This workflow uses GitHub's simple value/fallback expressions.
            # Fail on unsupported syntax rather than model different semantics.
            for name in expression.group(1).split('||'):
                name = name.strip()
                self.assertIn(name, context)
                if context[name]:
                    return str(context[name])
            return ''

        return re.sub(r'\$\{\{\s*(.*?)\s*\}\}', resolve, group.group(1))

    def test_distinct_images_on_the_same_protected_ref_do_not_cancel_each_other(self):
        # A main push and authorized PR-image dispatch share their caller name
        # and main ref, including when both are attested by the same workflow SHA.
        main = self.context('ghcr.io/flidai/leapview@sha256:' + 'a' * 64)
        candidate = self.context('ghcr.io/flidai/leapview@sha256:' + 'b' * 64)
        self.assertNotEqual(self.key(main), self.key(candidate))

    def test_repeated_qualification_of_the_same_image_keeps_cancellation(self):
        image = 'ghcr.io/flidai/leapview@sha256:' + 'a' * 64
        retry = self.context(image, ref='refs/heads/image-retry')
        self.assertEqual(self.key(self.context(image)), self.key(retry))
        self.assertRegex(WORKFLOW.read_text(), r'(?m)^  cancel-in-progress: true$')

    def test_premerge_updates_share_their_pull_request_scope(self):
        first = self.context(pull_request=904, ref='refs/pull/904/merge')
        updated = self.context(pull_request=904, ref='refs/pull/904/head')
        other = self.context(pull_request=905, ref='refs/pull/905/merge')
        self.assertEqual(self.key(first), self.key(updated))
        self.assertNotEqual(self.key(first), self.key(other))

    def test_manual_qualification_without_an_image_retains_branch_scope(self):
        main = self.context()
        branch = self.context(ref='refs/heads/qualification-fix')
        self.assertEqual(self.key(main), self.key(self.context()))
        self.assertNotEqual(self.key(main), self.key(branch))


if __name__ == '__main__':
    unittest.main()
