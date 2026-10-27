import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / 'scripts/prepare_ci_fixture_images.sh'
IMAGE = re.search(r'const PostgreSQL18Image = "([^"]+)"',
                  (ROOT / 'internal/platform/postgres/postgrestest/harness.go').read_text())[1]


MINIO_IMAGE = re.search(r'^FROM ([^ ]+)',
                       (ROOT / 'internal/platform/testminio/Dockerfile').read_text(), re.M)[1]
REGISTRY_IMAGE = re.search(r'const qualificationRegistryImage = "([^"]+)"',
                          (ROOT / 'internal/app/cli/composectl/qualification_image.go').read_text())[1]
DOCKER_CLIENT_IMAGE = re.search(r'const qualificationHistoricalDockerClientImage = "([^"]+)"',
                               (ROOT / 'internal/app/cli/composectl/qualification_historical_docker_test.go').read_text())[1]


class PrepareFixtureImageTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.bin = self.root / 'bin'
        self.bin.mkdir()
        self.log = self.root / 'calls.jsonl'
        (self.bin / 'docker').write_text(r'''#!/usr/bin/env python3
import json, os, pathlib, sys
log = pathlib.Path(os.environ['POSTGRES_IMAGE_TEST_LOG'])
calls = [json.loads(line) for line in log.read_text().splitlines()] if log.exists() else []
with log.open('a') as output:
    output.write(json.dumps({'args': sys.argv[1:]}) + '\n')
mode = os.environ['POSTGRES_IMAGE_TEST_MODE']
marker = log.with_suffix('.pulled')
if sys.argv[1:3] == ['image', 'inspect']:
    if mode == 'cached' or marker.exists():
        print('sha256:' + 'a' * 64)
        sys.exit(0)
    sys.exit(1)
if sys.argv[1] == 'pull':
    attempt = 1 + sum(call.get('args', [''])[0] == 'pull' for call in calls)
    if mode == 'limited' or (mode == 'limited_once' and attempt == 1):
        print('Error response from daemon: toomanyrequests: Rate exceeded', file=sys.stderr)
        sys.exit(7)
    if mode == 'data_limited' or (mode == 'data_limited_once' and attempt == 1):
        print('Error response from daemon: toomanyrequests: Data limit exceeded', file=sys.stderr)
        sys.exit(7)
    if mode == 'digest':
        print('manifest verification failed for digest', file=sys.stderr)
        sys.exit(8)
    marker.touch()
''')
        (self.bin / 'sleep').write_text(r'''#!/usr/bin/env python3
import json, os, sys
with open(os.environ['POSTGRES_IMAGE_TEST_LOG'], 'a') as output:
    output.write(json.dumps({'sleep': sys.argv[1:]}) + '\n')
''')
        for file in self.bin.iterdir():
            file.chmod(0o755)

    def prepare(self, mode, fixture='postgres'):
        result = subprocess.run(['bash', str(SCRIPT), fixture], text=True, capture_output=True,
                                env={**os.environ, 'PATH': str(self.bin) + os.pathsep + os.environ['PATH'],
                                     'POSTGRES_IMAGE_TEST_LOG': str(self.log), 'POSTGRES_IMAGE_TEST_MODE': mode})
        calls = [json.loads(line) for line in self.log.read_text().splitlines()] if self.log.exists() else []
        for call in calls:
            if 'args' in call:
                self.assertIn({'postgres': IMAGE, 'minio': MINIO_IMAGE, 'registry': REGISTRY_IMAGE,
                               'docker-client': DOCKER_CLIENT_IMAGE}[fixture], call['args'],
                              'all operations must use the canonical fixture digest')
        return result, calls

    def test_fixture_sources_keep_their_exact_digests_on_docker_hub(self):
        expected = {
            'postgres': 'docker.io/library/postgres:18-alpine@sha256:63bdc97d67b5133bf0e5ebd500bec6d046fa851dc81340d838f0347e616107e8',
            'minio': 'docker.io/library/golang:1.27.3-bookworm@sha256:972b4110ba0f47539a975c31e52c64765e1ff19f2d74469ae4a595c6676e80a5',
            'registry': 'docker.io/library/registry:2.8.3@sha256:a3d8aaa63ed8681a604f1dea0aa03f100d5895b6a58ace528858a7b332415373',
            'docker-client': 'docker.io/library/docker:29.8.1-cli@sha256:6602978e2be3c20e530e33773b8cadcef5fe998a71a534ee24516f1176973cdf',
        }
        actual = {
            'postgres': IMAGE,
            'minio': MINIO_IMAGE,
            'registry': REGISTRY_IMAGE,
            'docker-client': DOCKER_CLIENT_IMAGE,
        }
        self.assertEqual(actual, expected)

    def test_cached_pinned_image_does_not_contact_a_registry(self):
        result, calls = self.prepare('cached')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(calls), 1)
        self.assertEqual(calls[0]['args'][:2], ['image', 'inspect'])

    def test_prepares_missing_image_before_parallel_container_startup(self):
        result, calls = self.prepare('missing')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual([call['args'][0] for call in calls], ['image', 'pull', 'image'])

    def test_retries_observed_registry_rate_limit(self):
        result, calls = self.prepare('limited_once')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(sum(call.get('args', [''])[0] == 'pull' for call in calls), 2)
        self.assertEqual([call['sleep'] for call in calls if 'sleep' in call], [['5']])

    def test_persistent_rate_limit_fails_after_three_attempts(self):
        result, calls = self.prepare('limited')
        self.assertEqual(result.returncode, 7)
        self.assertEqual(sum(call.get('args', [''])[0] == 'pull' for call in calls), 3)
        self.assertEqual([call['sleep'] for call in calls if 'sleep' in call], [['5'], ['10']])

    def test_digest_failure_stops_immediately(self):
        result, calls = self.prepare('digest')
        self.assertEqual(result.returncode, 8)
        self.assertEqual(sum(call.get('args', [''])[0] == 'pull' for call in calls), 1)
        self.assertFalse(any('sleep' in call for call in calls))

    def test_minio_prepares_exact_source_build_base_without_pulling_scratch(self):
        result, calls = self.prepare('missing', 'minio')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual([call['args'][0] for call in calls], ['image', 'pull', 'image'])

    def test_minio_retries_observed_registry_rate_limit(self):
        result, calls = self.prepare('limited_once', 'minio')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(sum(call.get('args', [''])[0] == 'pull' for call in calls), 2)

    def test_registry_retries_observed_rate_limit_with_the_qualification_pin(self):
        result, calls = self.prepare('limited_once', 'registry')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(sum(call.get('args', [''])[0] == 'pull' for call in calls), 2)
        self.assertEqual([call['sleep'] for call in calls if 'sleep' in call], [['5']])

    def test_registry_digest_failure_does_not_retry_or_change_reference(self):
        result, calls = self.prepare('digest', 'registry')
        self.assertEqual(result.returncode, 8, result.stderr)
        self.assertEqual(sum(call.get('args', [''])[0] == 'pull' for call in calls), 1)
        self.assertFalse(any('sleep' in call for call in calls))

    def test_postgres_retries_observed_data_limit(self):
        result, calls = self.prepare('data_limited_once')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(sum(call.get('args', [''])[0] == 'pull' for call in calls), 2)

    def test_docker_client_retries_data_limit_with_the_historical_fixture_pin(self):
        result, calls = self.prepare('data_limited_once', 'docker-client')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(sum(call.get('args', [''])[0] == 'pull' for call in calls), 2)
        self.assertEqual([call['sleep'] for call in calls if 'sleep' in call], [['5']])

    def test_docker_client_persistent_data_limit_fails_after_three_attempts(self):
        result, calls = self.prepare('data_limited', 'docker-client')
        self.assertEqual(result.returncode, 7, result.stderr)
        self.assertEqual(sum(call.get('args', [''])[0] == 'pull' for call in calls), 3)

    def test_docker_client_digest_failure_does_not_retry_or_change_reference(self):
        result, calls = self.prepare('digest', 'docker-client')
        self.assertEqual(result.returncode, 8, result.stderr)
        self.assertEqual(sum(call.get('args', [''])[0] == 'pull' for call in calls), 1)
        self.assertFalse(any('sleep' in call for call in calls))


if __name__ == '__main__':
    unittest.main()
