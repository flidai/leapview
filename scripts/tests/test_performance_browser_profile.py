import hashlib
import json
import os
import socket
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from performance_browser_profile import admit_server


class NativeFixtureAdmissionTests(unittest.TestCase):
    def fixture(self, root):
        data = root / 'orders.csv'
        data.write_text('id,cents\n1,100\n')
        source = {'commit': 'a' * 40, 'tree': 'b' * 40}
        binary = Path('/proc/self/exe').resolve()
        listener = socket.socket()
        listener.bind(('127.0.0.1', 0))
        listener.listen()
        self.addCleanup(listener.close)
        receipt = {'source': source, 'binary': {'path': str(binary), 'sha256': hashlib.sha256(binary.read_bytes()).hexdigest()},
            'pid': os.getpid(), 'processStart': Path('/proc/self/stat').read_text().rsplit(')', 1)[1].split()[19],
            'baseURL': 'http://127.0.0.1:' + str(listener.getsockname()[1]), 'datasetRoot': str(root), 'datasetFiles': [{'path': str(data), 'sha256': hashlib.sha256(data.read_bytes()).hexdigest()}]}
        path = root / 'server.json'
        path.write_text(json.dumps(receipt))
        return source, receipt, path, data

    def test_rejects_changed_native_process_source_dataset_or_url(self):
        with tempfile.TemporaryDirectory() as directory:
            source, receipt, path, data = self.fixture(Path(directory))
            env = {'LEAPVIEW_BASE_URL': receipt['baseURL'], 'LEAPVIEW_BROWSER_PROFILE_SERVER_RECEIPT': str(path)}
            with patch.dict(os.environ, env), patch('performance_browser_profile.subprocess.check_output',
                    return_value=json.dumps({'product': 'leapview', 'revision': source['commit'], 'dirty': False})):
                self.assertEqual(admit_server(source)['pid'], os.getpid())
                with self.assertRaisesRegex(ValueError, 'source'):
                    admit_server({'commit': 'c' * 40, 'tree': 'b' * 40})
                data.write_text('changed')
                with self.assertRaisesRegex(ValueError, 'dataset'):
                    admit_server(source)
                data.write_text('id,cents\n1,100\n')
                receipt['processStart'] = '0'
                path.write_text(json.dumps(receipt))
                with self.assertRaisesRegex(ValueError, 'process'):
                    admit_server(source)

    def test_manifest_cannot_omit_an_actual_csv_input(self):
        with tempfile.TemporaryDirectory() as directory:
            source, receipt, path, data = self.fixture(Path(directory))
            (Path(directory) / 'customers.csv').write_text('id\n1\n')
            with patch.dict(os.environ, {'LEAPVIEW_BASE_URL': receipt['baseURL'], 'LEAPVIEW_BROWSER_PROFILE_SERVER_RECEIPT': str(path)}), \
                    patch('performance_browser_profile.subprocess.check_output', return_value=json.dumps({'product': 'leapview', 'revision': source['commit'], 'dirty': False})):
                with self.assertRaisesRegex(ValueError, 'every CSV'):
                    admit_server(source)

    def test_declared_source_cannot_override_binary_version_or_unowned_port(self):
        with tempfile.TemporaryDirectory() as directory:
            source, receipt, path, data = self.fixture(Path(directory))
            env = {'LEAPVIEW_BASE_URL': receipt['baseURL'], 'LEAPVIEW_BROWSER_PROFILE_SERVER_RECEIPT': str(path)}
            with patch.dict(os.environ, env), patch('performance_browser_profile.subprocess.check_output',
                    return_value=json.dumps({'product': 'leapview', 'revision': 'c' * 40, 'dirty': False})):
                with self.assertRaisesRegex(ValueError, 'binary version'):
                    admit_server(source)
            receipt['baseURL'] = 'http://127.0.0.1:1'
            path.write_text(json.dumps(receipt))
            with patch.dict(os.environ, {**env, 'LEAPVIEW_BASE_URL': receipt['baseURL']}), patch('performance_browser_profile.subprocess.check_output',
                    return_value=json.dumps({'product': 'leapview', 'revision': source['commit'], 'dirty': False})):
                with self.assertRaisesRegex(ValueError, 'listening port'):
                    admit_server(source)

    def test_same_port_on_a_different_loopback_address_does_not_admit(self):
        with tempfile.TemporaryDirectory() as directory:
            source, receipt, path, data = self.fixture(Path(directory))
            other = socket.socket()
            other.bind(('127.0.0.2', 0))
            other.listen()
            self.addCleanup(other.close)
            receipt['baseURL'] = 'http://127.0.0.1:' + str(other.getsockname()[1])
            path.write_text(json.dumps(receipt))
            with patch.dict(os.environ, {'LEAPVIEW_BASE_URL': receipt['baseURL'], 'LEAPVIEW_BROWSER_PROFILE_SERVER_RECEIPT': str(path)}), \
                    patch('performance_browser_profile.subprocess.check_output', return_value=json.dumps({'product': 'leapview', 'revision': source['commit'], 'dirty': False})):
                with self.assertRaisesRegex(ValueError, 'listening port'):
                    admit_server(source)


if __name__ == '__main__':
    unittest.main()
