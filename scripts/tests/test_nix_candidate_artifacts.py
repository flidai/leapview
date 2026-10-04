import copy
from pathlib import Path
import sys
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import nix_candidate_artifacts as artifacts


class ArtifactTests(unittest.TestCase):
    def fixture(self, architecture='arm64', phase='qualified'):
        return {'id': 15, 'name': f'{artifacts.PREFIXES[phase]}-123-2-{architecture}',
                'expired': False, 'digest': 'sha256:' + 'a' * 64,
                'workflow_run': {'id': 123, 'head_branch': 'main', 'head_sha': 'b' * 40}}

    def resolve(self, entries, architecture='arm64'):
        return artifacts.resolve([{'artifacts': entries}], 123, 2, 'b' * 40, architecture, ['qualified'])

    def test_selects_exact_architecture_and_attempt_across_pages(self):
        arm, amd = self.fixture(), self.fixture('amd64')
        amd['id'] = 16
        pages = [{'artifacts': [amd]}, {'artifacts': [arm]}]
        self.assertEqual(artifacts.resolve(pages, 123, 2, 'b' * 40, 'arm64', ['qualified']), {'qualified': 15})
        self.assertEqual(artifacts.resolve(pages, 123, 2, 'b' * 40, 'amd64', ['qualified']), {'qualified': 16})

    def test_rejects_substitution_missing_or_duplicate_originals(self):
        valid = self.fixture()
        mutations = [('id', True), ('id', 0), ('expired', True), ('digest', 'unknown'),
                     ('name', 'nix-qualified-123-1-arm64'), ('name', 'nix-qualified-123-2-amd64')]
        for key, value in mutations:
            invalid = copy.deepcopy(valid)
            invalid[key] = value
            with self.subTest(key=key, value=value), self.assertRaises(ValueError):
                self.resolve([invalid])
        for key, value in [('id', 124), ('id', True), ('head_branch', 'feature'), ('head_sha', 'c' * 40)]:
            invalid = copy.deepcopy(valid)
            invalid['workflow_run'][key] = value
            with self.subTest(workflow_key=key, value=value), self.assertRaises(ValueError):
                self.resolve([invalid])
        for entries in ([], [valid, copy.deepcopy(valid)]):
            with self.assertRaises(ValueError):
                self.resolve(entries)

    def test_rejects_ambiguous_identity_and_metadata(self):
        for pages in ({'artifacts': []}, [], [{'artifacts': None}], [{'artifacts': [None]}]):
            with self.subTest(pages=pages), self.assertRaises(ValueError):
                artifacts.resolve(pages, 123, 2, 'b' * 40, 'arm64', ['qualified'])
        for run_id, attempt, revision, arch, phases in [(True, 2, 'b'*40, 'arm64', ['qualified']),
                (123, 0, 'b'*40, 'arm64', ['qualified']), (123, 2, 'b'*39, 'arm64', ['qualified']),
                (123, 2, 'b'*40, 'x86', ['qualified']), (123, 2, 'b'*40, 'arm64', []),
                (123, 2, 'b'*40, 'arm64', ['qualified', 'qualified'])]:
            with self.assertRaises(ValueError):
                artifacts.resolve([{'artifacts': [self.fixture()]}], run_id, attempt, revision, arch, phases)


if __name__ == '__main__':
    unittest.main()
