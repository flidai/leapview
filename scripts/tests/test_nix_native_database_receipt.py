import importlib.util
import json
import os
import subprocess
from pathlib import Path
import tempfile
import unittest

SCRIPT = Path(__file__).resolve().parents[1] / 'nix_native_database_receipt.py'
spec = importlib.util.spec_from_file_location('database_receipt', SCRIPT)
database = importlib.util.module_from_spec(spec)
spec.loader.exec_module(database)
REPO = SCRIPT.parent.parent


def database_fixture(policy):
    files = {}
    for name, library in policy['libraries'].items():
        files[name + '-source.json'] = json.dumps(library['selectedFiles']).encode()
        files[name + '-compiler.txt'] = b'gcc 15\ncompiler-target: x86_64-unknown-linux-gnu\n'
    files['libpq-config.txt'] = b'#define USE_OPENSSL 1\n#define HAVE_LIBZ 1\n'
    files['libpq-options.txt'] = b'--with-openssl --without-libcurl --without-gssapi'
    files['libpq-build.txt'] = b'cc -c fe-auth-scram.c\ncc -c fe-secure-openssl.c\ncc -c fe-auth-oauth.c\ncc -c fe-connect.c\n'
    files['mariadb-cache.txt'] = ('WITH_SSL:STRING=OPENSSL\nWITH_EXTERNAL_ZLIB:BOOL=ON\nWITH_CURL:BOOL=OFF\nCMAKE_HOME_DIRECTORY:INTERNAL=/source\n' + ''.join('CLIENT_PLUGIN_' + name + ':STRING=' + selected + '\n' for name, selected in policy['mariadbPlugins'].items())).encode()
    files['mariadb-commands.json'] = json.dumps([{'file': '/source/' + name, 'command': 'cc -fPIC -c ' + name} for name in ('libmariadb/mariadb_lib.c', 'libmariadb/secure/openssl.c', 'plugins/auth/caching_sha2_pw.c', 'plugins/auth/ed25519.c')]).encode()
    files['mariadb-plugins.c'] = ''.join('  (struct st_mysql_client_plugin *)&' + name.lower() + '_client_plugin,\n' for name, mode in policy['mariadbPlugins'].items() if mode == 'STATIC').encode()
    return files


class DatabaseReceipts(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.policy = json.loads((REPO / 'nix/database-source-lock.json').read_text())
        self.files = database_fixture(self.policy)

    def check(self):
        database.check(self.root, self.policy, 'linux/amd64', lambda path: self.files[path.name])

    def test_libpq_process_exit_check_allows_only_exact_thread_symbol(self):
        # Execute the actual patched upstream nm pipeline; static OpenSSL adds
        # pthread_exit, which terminates a thread rather than its host process.
        patch = (REPO / 'nix/libpq-static-openssl-refs.patch').read_text()
        pipeline = next(line for line in patch.splitlines() if line.startswith('+\t@if nm '))
        pipeline = pipeline.removeprefix('+\t@if ').split('; then')[0].replace('$<', 'fixture').replace('$$', '$')
        for name, allowed in [('pthread_exit', True), ('pthread_exit@GLIBC_2.2.5', True),
                              ('exit', False), ('exit@GLIBC_2.2.5', False),
                              ('_exit', False), ('_Exit', False), ('_Exit@GLIBC_2.2.5', False),
                              ('my_pthread_exit', False), ('pthread_exit_extra', False)]:
            with self.subTest(symbol=name):
                result = subprocess.run(['bash', '-c', 'nm() { printf "%s\\n" "$TEST_NM_OUTPUT"; }; if ' + pipeline + '; then exit 1; fi'],
                    env={**os.environ, 'TEST_NM_OUTPUT': 'libpq.so: U ' + name}, capture_output=True)
                self.assertEqual(result.returncode, 0 if allowed else 1, result.stderr)

    def test_selected_password_tls_profile_roundtrip(self):
        self.check()

    def test_missing_selected_source_is_rejected(self):
        self.files['libpq-build.txt'] = b'cc -c fe-connect.c\n'
        with self.assertRaisesRegex(ValueError, 'compilation missing'):
            self.check()

    def test_auth_plugin_cannot_fall_back_to_dynamic_loading(self):
        self.files['mariadb-plugins.c'] = self.files['mariadb-plugins.c'].replace(b'  (struct st_mysql_client_plugin *)&client_ed25519_client_plugin,\n', b'')
        with self.assertRaisesRegex(ValueError, 'builtin authentication/plugin selection'):
            self.check()

    def test_source_or_feature_substitution_is_rejected(self):
        for name, data in [('libpq-source.json', b'{}'), ('libpq-config.txt', b'#define HAVE_LIBZ 1\n'), ('mariadb-cache.txt', self.files['mariadb-cache.txt'].replace(b'CLIENT_PLUGIN_CACHING_SHA2_PASSWORD:STRING=STATIC', b'CLIENT_PLUGIN_CACHING_SHA2_PASSWORD:STRING=DYNAMIC'))]:
            with self.subTest(name=name):
                original = self.files[name]
                self.files[name] = data
                with self.assertRaises(ValueError):
                    self.check()
                self.files[name] = original

    def test_wrong_architecture_and_host_specific_flags_are_rejected(self):
        self.files['mariadb-compiler.txt'] = b'gcc\ncompiler-target: aarch64-unknown-linux-gnu\n'
        with self.assertRaisesRegex(ValueError, 'compiler target'):
            self.check()
        self.files['mariadb-compiler.txt'] = self.files['libpq-compiler.txt']
        self.files['libpq-build.txt'] += b'cc -march=native -c fe-connect.c\n'
        with self.assertRaisesRegex(ValueError, 'host-specific'):
            self.check()


if __name__ == '__main__':
    unittest.main()
