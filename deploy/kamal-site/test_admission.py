import os
import unittest
from unittest.mock import patch

import deploy


class AdmissionCredentialsTest(unittest.TestCase):
    def test_stored_gh_login_is_available_only_to_admission_child(self):
        with patch.dict(os.environ, {'PATH': '/fixture/bin'}, clear=True), \
                patch.object(deploy, 'run', return_value=b'fixture-credential\n') as run:
            environment = deploy.admission_environment()
            run.assert_called_once_with(['gh', 'auth', 'token'])
            self.assertEqual(environment['GH_TOKEN'], 'fixture-credential')
            self.assertEqual(environment['PATH'], '/fixture/bin')
            self.assertNotIn('GH_TOKEN', os.environ)

    def test_explicit_credentials_do_not_read_another_login(self):
        for name in ('GH_TOKEN', 'GITHUB_TOKEN'):
            with self.subTest(name=name), patch.dict(os.environ, {name: 'fixture-credential'}, clear=True), \
                    patch.object(deploy, 'run') as run:
                self.assertEqual(deploy.admission_environment()[name], 'fixture-credential')
                run.assert_not_called()

    def test_empty_stored_login_fails_before_verification(self):
        with patch.dict(os.environ, {}, clear=True), patch.object(deploy, 'run', return_value=b'\n'):
            with self.assertRaisesRegex(ValueError, 'authenticated GitHub'):
                deploy.admission_environment()


if __name__ == '__main__':
    unittest.main()
