import json
import pathlib
import sys
import unittest
from unittest.mock import Mock

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[1]))
from demo_release_contract import LEGACY_REVISION, MANIFEST, read_contract


class ReleaseContractTests(unittest.TestCase):
    def test_exact_legacy_boundary_does_not_guess_from_schema(self):
        git = Mock(side_effect=AssertionError('legacy manifest absent by definition'))
        self.assertEqual(read_contract(LEGACY_REVISION, git)['permissionProfile'], 'legacy-capabilities/v1')
        git.assert_not_called()

    def test_contract_is_read_from_bound_source_not_worktree(self):
        revision = 'a' * 40
        contract = dict(version=1, permissionProfile='leapview.permissions/v1',
                        publicationAPI='delivery/v1', hostTransition='compose-postgres-local/v1')
        git = Mock(return_value=json.dumps(contract).encode())
        self.assertEqual(read_contract(revision, git), contract)
        git.assert_called_once_with('show', revision+':'+MANIFEST)

    def test_unknown_contract_fails_before_workload_authentication(self):
        for contract in ({}, {'version': 2}, {'version': True},
                         dict(version=1, permissionProfile='legacy-capabilities/v1',
                              publicationAPI='delivery/v1', hostTransition='compose-postgres-local/v1')):
            with self.subTest(contract=contract), self.assertRaises(ValueError):
                read_contract('b'*40, Mock(return_value=json.dumps(contract).encode()))
