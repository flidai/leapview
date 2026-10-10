import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


SCRIPT = Path(__file__).resolve().parents[1] / 'prepare_sqlc_modules.sh'


class PrepareSQLCModulesTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.bin = self.root / 'bin'
        self.bin.mkdir()
        self.log = self.root / 'calls.jsonl'
        self.project = self.root / 'project'
        self.project.mkdir()
        (self.project / 'go.mod').write_text('module unchanged\n')
        (self.project / 'go.sum').write_text('retained checksums\n')
        (self.bin / 'go').write_text(r'''#!/usr/bin/env python3
import json, os, pathlib, sys
log = pathlib.Path(os.environ['SQLC_TEST_LOG'])
calls = [json.loads(line) for line in log.read_text().splitlines()] if log.exists() else []
with log.open('a') as output:
    output.write(json.dumps({'args': sys.argv[1:], 'cwd': os.getcwd(),
                             'toolchain': os.environ.get('GOTOOLCHAIN'),
                             'sumdb': os.environ.get('GOSUMDB'),
                             'debug': os.environ.get('GODEBUG')}) + '\n')
if sys.argv[1:] == ['mod', 'download', 'all']:
    attempt = 1 + sum(call.get('args') == ['mod', 'download', 'all'] for call in calls)
    failure = os.environ['SQLC_TEST_FAILURE']
    if failure == 'timeout' or (failure == 'timeout_once' and attempt == 1):
        print('Get "https://sum.golang.org/lookup/github.com/sqlc-dev/sqlc@v1.31.1": dial tcp: i/o timeout', file=sys.stderr)
        sys.exit(7)
    if failure == 'checksum':
        print('verifying module: checksum mismatch\nSECURITY ERROR', file=sys.stderr)
        sys.exit(8)
    if failure == 'revision':
        print('invalid version: unknown revision v1.31.1', file=sys.stderr)
        sys.exit(9)
''')
        (self.bin / 'sleep').write_text(r'''#!/usr/bin/env python3
import json, os, sys
with open(os.environ['SQLC_TEST_LOG'], 'a') as output:
    output.write(json.dumps({'sleep': sys.argv[1:]}) + '\n')
''')
        for executable in self.bin.iterdir():
            executable.chmod(0o755)

    def prepare(self, failure='none'):
        result = subprocess.run(['bash', str(SCRIPT)], cwd=self.project, text=True,
                                capture_output=True, env={
                                    **os.environ, 'PATH': str(self.bin) + os.pathsep + os.environ['PATH'],
                                    'SQLC_TEST_LOG': str(self.log), 'SQLC_TEST_FAILURE': failure,
                                    'GOSUMDB': 'sum.golang.org',
                                })
        calls = [json.loads(line) for line in self.log.read_text().splitlines()] if self.log.exists() else []
        self.assertEqual((self.project / 'go.mod').read_text(), 'module unchanged\n')
        self.assertEqual((self.project / 'go.sum').read_text(), 'retained checksums\n')
        return result, calls

    def test_downloads_complete_pinned_graph_without_changing_the_project(self):
        result, calls = self.prepare()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual([call['args'] for call in calls], [
            ['mod', 'init', 'leapview-ci-sqlc'],
            ['mod', 'edit', '-go=1.26.9', '-require=github.com/sqlc-dev/sqlc@v1.31.1'],
            ['mod', 'download', 'all'],
        ])
        for call in calls:
            self.assertEqual(call['toolchain'], 'go1.26.9')
            self.assertEqual(call['sumdb'], 'sum.golang.org')
            self.assertEqual(call['debug'], 'http2client=0')
            self.assertNotEqual(call['cwd'], str(self.project))
            self.assertFalse(Path(call['cwd']).exists(), 'temporary module must be cleaned up')

    def test_retries_the_observed_checksum_service_transport_timeout(self):
        result, calls = self.prepare('timeout_once')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(sum(call.get('args') == ['mod', 'download', 'all'] for call in calls), 2)
        self.assertEqual([call['sleep'] for call in calls if 'sleep' in call], [['5']])
        self.assertIn('i/o timeout', result.stderr)

    def test_persistent_transport_failure_stops_after_three_attempts(self):
        result, calls = self.prepare('timeout')
        self.assertEqual(result.returncode, 7)
        self.assertEqual(sum(call.get('args') == ['mod', 'download', 'all'] for call in calls), 3)
        self.assertEqual([call['sleep'] for call in calls if 'sleep' in call], [['5'], ['10']])

    def test_checksum_failure_is_not_retried_or_bypassed(self):
        result, calls = self.prepare('checksum')
        self.assertEqual(result.returncode, 8)
        self.assertEqual(sum(call.get('args') == ['mod', 'download', 'all'] for call in calls), 1)
        self.assertFalse(any('sleep' in call for call in calls))
        self.assertIn('SECURITY ERROR', result.stderr)

    def test_invalid_pinned_revision_is_not_retried(self):
        result, calls = self.prepare('revision')
        self.assertEqual(result.returncode, 9)
        self.assertEqual(sum(call.get('args') == ['mod', 'download', 'all'] for call in calls), 1)
        self.assertFalse(any('sleep' in call for call in calls))


if __name__ == '__main__':
    unittest.main()
