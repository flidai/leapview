import json
from pathlib import Path
import sys
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import nix_native_sqlite_receipt as sqlite

POLICY = json.loads((Path(__file__).resolve().parents[2] / 'nix/sqlite-library-source-lock.json').read_text())


def sqlite_fixture(policy):
    return {'source.json': json.dumps(policy['selectedFiles']).encode(),
            'compiler.txt': b'gcc 15\ncompiler-target: x86_64-unknown-linux-gnu\n',
            'build.txt': ('+ gcc -O2 -fPIC ' + ' '.join('-D' + x for x in policy['defines']) + ' -c src/sqlite/sqlite3.c -o sqlite3.o\n').encode(),
            'checks.txt': (policy['version'] + '\n' + policy['sourceID'] + '\n').encode(),
            'consumer-needed.txt': b'(NEEDED) Shared library: [libc.so.6]\n'}


class SQLiteReceiptTests(unittest.TestCase):
    def test_selected_archive_evidence_passes(self):
        data = sqlite_fixture(POLICY)
        sqlite.check(Path('.'), POLICY, 'linux/amd64', lambda path: data[path.name])

    def test_missing_rebound_source_feature_compiler_and_consumer_rejected(self):
        replacements = {'source.json': b'{}', 'compiler.txt': b'compiler-target: aarch64-linux-gnu\n',
                        'checks.txt': b'success\n', 'consumer-needed.txt': b'(NEEDED) libsqlite3.so\n'}
        for name, replacement in replacements.items():
            with self.subTest(name=name):
                data = sqlite_fixture(POLICY); data[name] = replacement
                with self.assertRaises(ValueError):
                    sqlite.check(Path('.'), POLICY, 'linux/amd64', lambda path: data[path.name])
        for token in ['-fPIC', *('-D' + x for x in POLICY['defines'])]:
            data = sqlite_fixture(POLICY);data['build.txt'] = data['build.txt'].replace(token.encode(), b'')
            with self.assertRaises(ValueError):
                sqlite.check(Path('.'), POLICY, 'linux/amd64', lambda path: data[path.name])
        for token in ['-USQLITE_ENABLE_RTREE', '-DSQLITE_OMIT_LOAD_EXTENSION', '-march=native']:
            data = sqlite_fixture(POLICY);data['build.txt'] = data['build.txt'].replace(b' -O2 ', (' -O2 ' + token + ' ').encode())
            with self.assertRaises(ValueError):
                sqlite.check(Path('.'), POLICY, 'linux/amd64', lambda path: data[path.name])

    def test_duplicate_inline_engine_and_archive_rebinding_rejected(self):
        binding = {'archive': '/store/sqlite/lib/libsqlite3.a', 'sha256': 'a' * 64}
        cache = 'SQLITE_SELECTED_LIBRARY:FILEPATH=' + binding['archive'] + '\n'
        commands = [{'file': '/source/src/sqlite_scanner.cpp'}]
        selection = 'duckdb_extension_load(sqlite_scanner SOURCE_DIR /source)\n'
        read = lambda _: json.dumps(binding).encode()
        sqlite.check_engine(Path('.'), cache, commands, selection, read)
        for altered_cache, altered_commands in [(cache.replace('/store/', '/other/'), commands),
                (cache, commands + [{'file': '/source/src/sqlite/sqlite3.c'}]), (cache, [])]:
            with self.assertRaises(ValueError):
                sqlite.check_engine(Path('.'), altered_cache, altered_commands, selection, read)
