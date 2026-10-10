"""One selected SQLite archive shared by SQLite, GDAL and PROJ consumers."""
import json
import re
import shlex

ARCHIVES = {'lib/libsqlite3.a'}
RECIPES = ('nix/sqlite.nix', 'nix/sqlite-library.nix', 'nix/sqlite-library-source-lock.json',
           'nix/sqlite-shared-library.patch', 'nix/sqlite-smoke.c', 'scripts/nix_native_sqlite_receipt.py')
EVIDENCE = {'source.json', 'compiler.txt', 'build.txt', 'checks.txt', 'consumer-needed.txt'}


def decode(data):
    def unique(pairs):
        result = {}
        for name, value in pairs:
            if name in result:
                raise ValueError('duplicate SQLite evidence key')
            result[name] = value
        return result
    return json.loads(data, object_pairs_hook=unique)


def check(evidence, policy, platform, read):
    if decode(read(evidence / 'source.json')) != policy['selectedFiles']:
        raise ValueError('selected SQLite source changed')
    arch = {'linux/amd64': 'x86_64', 'linux/arm64': 'aarch64'}[platform]
    if not re.search(r'^compiler-target: ' + arch + r'-[^\n]*linux[^\n]*$', read(evidence / 'compiler.txt').decode(), re.M):
        raise ValueError('SQLite compiler target differs')
    commands = read(evidence / 'build.txt').decode()
    selected = [shlex.split(line.lstrip('+ ')) for line in commands.splitlines() if ' -c src/sqlite/sqlite3.c ' in line]
    flags = {'-fPIC', '-c', 'src/sqlite/sqlite3.c', '-o', 'sqlite3.o'} | {'-D' + flag for flag in policy['defines']}
    if len(selected) != 1 or not flags <= set(selected[0]):
        raise ValueError('SQLite static PIC compilation/feature selection differs')
    if any(arg.startswith(('-DSQLITE_', '-USQLITE_')) and arg not in flags for arg in selected[0]):
        raise ValueError('unexpected SQLite feature override')
    if '-march=native' in commands or '-mcpu=native' in commands:
        raise ValueError('SQLite compiler uses host-specific instructions')
    if read(evidence / 'checks.txt').decode() != policy['version'] + '\n' + policy['sourceID'] + '\n':
        raise ValueError('SQLite actual functionality/source identity proof missing')
    needed = read(evidence / 'consumer-needed.txt').decode()
    if not re.search(r'NEEDED.*libc\.so', needed) or re.search(r'NEEDED.*libsqlite', needed):
        raise ValueError('SQLite consumer dynamic library selection differs')


def check_engine(evidence, cache, commands, selection, read):
    binding = decode(read(evidence / 'sqlite-link.json'))
    if (set(binding) != {'archive', 'sha256'} or not isinstance(binding['archive'], str)
            or not binding['archive'].endswith('/lib/libsqlite3.a')
            or not re.fullmatch('[0-9a-f]{64}', binding['sha256'])):
        raise ValueError('SQLite archive binding invalid')
    if not re.search(r'^SQLITE_SELECTED_LIBRARY:[^=]+=' + re.escape(binding['archive']) + '$', cache, re.M):
        raise ValueError('SQLite engine selected archive differs')
    source = re.search(r'duckdb_extension_load\(sqlite_scanner SOURCE_DIR ([^\s()]+)\)', selection)
    if not source or not any(c.get('file') == source[1] + '/src/sqlite_scanner.cpp' for c in commands):
        raise ValueError('compiled SQLite wrapper missing')
    if any(c.get('file', '').endswith('/sqlite3.c') for c in commands):
        raise ValueError('duplicate SQLite amalgamation compilation')
