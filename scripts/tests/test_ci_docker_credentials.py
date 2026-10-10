import base64
import io
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import mask_ci_docker_credentials as masking


class DockerCredentialMaskingTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.config = Path(self.temp.name) / "config.json"
        self.env = {"GITHUB_ACTIONS": "true", "RUNNER_OS": "Linux",
                    "RUNNER_ENVIRONMENT": "github-hosted", "DOCKER_CONFIG": self.temp.name}
        self.output = io.StringIO()

    def masks(self):
        masking.mask_credentials(self.env, self.output)
        lines = self.output.getvalue().splitlines()
        self.assertTrue(all(line.startswith("::add-mask::") for line in lines))
        return [line.removeprefix("::add-mask::").replace("%0A", "\n").replace("%0D", "\r").replace("%25", "%") for line in lines]

    def test_masks_encoded_auth_and_decoded_password_in_rate_limit_diagnostic(self):
        password = "synthetic-private-registry-password"
        auth = base64.b64encode(("hosted-runner:" + password).encode()).decode()
        content = json.dumps({"auths": {"https://index.docker.io/v1/": {"auth": auth}}})
        self.config.write_text(content)
        os.chmod(self.config, 0o600)
        masks = self.masks()
        diagnostic = 'toomanyrequests Docker config {Auth:' + auth + ' Password:' + password + '}'
        for value in masks:
            diagnostic = diagnostic.replace(value, "***")
        self.assertNotIn(auth, diagnostic)
        self.assertNotIn(password, diagnostic)
        self.assertIn("toomanyrequests", diagnostic)
        self.assertEqual(self.config.read_text(), content)
        self.assertEqual(self.config.stat().st_mode & 0o777, 0o600)

    def test_masks_environment_and_file_auth_without_mutating_either(self):
        self.env["DOCKER_AUTH_CONFIG"] = json.dumps({"auths": {"one": {"identitytoken": "identity-secret"}}})
        self.config.write_text(json.dumps({"auths": {"two": {"registrytoken": "registry-secret", "password": "password-secret"}}}))
        previous = dict(self.env)
        self.assertEqual(set(self.masks()), {"identity-secret", "registry-secret", "password-secret"})
        self.assertEqual(self.env, previous)

    def test_escapes_workflow_commands_and_masks_json_representation(self):
        password = 'percent%\r\n::notice::injected"\\value'
        self.config.write_text(json.dumps({"auths": {"test": {"password": password}}}))
        self.assertEqual(set(self.masks()), {password, json.dumps(password, ensure_ascii=False)[1:-1]})
        self.assertEqual(len(self.output.getvalue().splitlines()), 2)

    def test_empty_or_helper_only_config_needs_no_mask(self):
        for value in [None, {}, {"credsStore": "osxkeychain", "auths": {"one": {"auth": ""}}}]:
            with self.subTest(value=value):
                if value is not None:
                    self.config.write_text(json.dumps(value))
                self.assertEqual(self.masks(), [])

    def test_invalid_auth_is_still_masked_without_decoding(self):
        self.config.write_text(json.dumps({"auths": {"one": {"auth": "invalid-base64"}}}))
        self.assertEqual(self.masks(), ["invalid-base64"])

    def test_malformed_config_fails_without_printing_values(self):
        for value in ['{"private":"secret"', '[]', '{"auths": []}', '{"auths":{"one":null}}', '{"auths":{"one":{"auth":42}}}']:
            with self.subTest(value=value):
                self.config.write_text(value)
                with self.assertRaisesRegex(ValueError, "Docker credential masking failed"):
                    masking.mask_credentials(self.env, self.output)
                self.assertEqual(self.output.getvalue(), "")

    def test_refuses_non_hosted_execution_without_emitting_secrets(self):
        self.config.write_text('{"auths":{"one":{"password":"private"}}}')
        for key, value in [("GITHUB_ACTIONS", "false"), ("RUNNER_OS", "Darwin"), ("RUNNER_ENVIRONMENT", "self-hosted")]:
            with self.subTest(key=key), self.assertRaisesRegex(ValueError, "requires a GitHub-hosted Linux runner"):
                masking.mask_credentials(dict(self.env, **{key: value}), self.output)
        self.assertEqual(self.output.getvalue(), "")

    def test_oversize_config_fails_before_emitting_any_masks(self):
        self.env["DOCKER_AUTH_CONFIG"] = '{"auths":{"one":{"password":"environment-secret"}}}'
        self.config.write_bytes(b" " * (masking.MAX_BYTES + 1))
        with self.assertRaisesRegex(ValueError, "Docker credential masking failed"):
            masking.mask_credentials(self.env, self.output)
        self.assertEqual(self.output.getvalue(), "")

    def test_malformed_environment_config_fails_without_values(self):
        self.env["DOCKER_AUTH_CONFIG"] = '{"private":"environment-secret"'
        with self.assertRaisesRegex(ValueError, "Docker credential masking failed"):
            masking.mask_credentials(self.env, self.output)
        self.assertEqual(self.output.getvalue(), "")


if __name__ == "__main__":
    unittest.main()
