"""Selected database client build evidence, not exhaustive binary admission."""

import json
import re
import shlex

LIBRARIES = ('libpq', 'mariadb')
ARCHIVES = {'lib/libpq.a', 'lib/libpgcommon.a', 'lib/libpgport.a', 'lib/libmariadbclient.a'}
RECIPES = ('nix/database-connectors.nix', 'nix/check-extension-version.py', 'nix/database-source-lock.json',
           'nix/libpq-static-openssl-refs.patch', 'nix/postgres-static-dependencies.patch', 'nix/mysql-static-dependencies.patch',
           'nix/check-mariadb-static-auth.c', 'scripts/nix_native_database_receipt.py')
EVIDENCE = {name + '-' + kind for name in LIBRARIES for kind in ('source.json', 'compiler.txt')} | {
    'libpq-config.txt', 'libpq-options.txt', 'libpq-build.txt',
    'mariadb-cache.txt', 'mariadb-commands.json', 'mariadb-plugins.c',
}


def decode(data):
    def pairs(items):
        result = {}
        for name, value in items:
            if name in result:
                raise ValueError('duplicate database evidence JSON key')
            result[name] = value
        return result
    return json.loads(data, object_pairs_hook=pairs)


def check(evidence, policy, platform, read):
    arch = 'aarch64' if platform == 'linux/arm64' else 'x86_64'
    for name in LIBRARIES:
        if decode(read(evidence / (name + '-source.json'))) != policy['libraries'][name]['selectedFiles']:
            raise ValueError('database dependency selected source identity differs: ' + name)
        compiler = read(evidence / (name + '-compiler.txt')).decode()
        if not re.search(r'^compiler-target: ' + arch + r'-[^\n]*linux[^\n]*$', compiler, re.M):
            raise ValueError('database dependency compiler target differs: ' + name)
    pgconfig = read(evidence / 'libpq-config.txt').decode()
    for name in ('USE_OPENSSL', 'HAVE_LIBZ'):
        if not re.search(r'^#define ' + name + r' 1$', pgconfig, re.M):
            raise ValueError('libpq required feature differs: ' + name)
    for name in ('ENABLE_GSS', 'USE_LIBCURL', 'USE_LZ4', 'USE_ZSTD', 'ENABLE_NLS'):
        if re.search(r'^#define ' + name + r' ', pgconfig, re.M):
            raise ValueError('unexpected libpq dependency: ' + name)
    options = shlex.split(read(evidence / 'libpq-options.txt').decode())
    if not {'--with-openssl', '--without-libcurl', '--without-gssapi'} <= set(options):
        raise ValueError('libpq selected authentication options differ')
    commands = read(evidence / 'libpq-build.txt').decode()
    for name in ('fe-auth-scram.c', 'fe-secure-openssl.c', 'fe-auth-oauth.c', 'fe-connect.c'):
        if not any(name in line and ' -c ' in line for line in commands.splitlines()):
            raise ValueError('libpq compilation missing: ' + name)
    cache = read(evidence / 'mariadb-cache.txt').decode()
    expected = {'WITH_SSL': 'OPENSSL', 'WITH_EXTERNAL_ZLIB': '(ON|TRUE)', 'WITH_CURL': '(OFF|FALSE)'}
    expected.update({'CLIENT_PLUGIN_' + name: selected for name, selected in policy['mariadbPlugins'].items()})
    for name, selected in expected.items():
        if not re.search(r'^' + name + r':[^=]+=' + selected + r'$', cache, re.M):
            raise ValueError('MariaDB selected feature differs: ' + name)
    plugins = read(evidence / 'mariadb-plugins.c').decode()
    names = re.findall(r'\(struct st_mysql_client_plugin \*\)\s*&([a-z0-9_]+)_client_plugin', plugins)
    selected = {name.lower() for name, mode in policy['mariadbPlugins'].items() if mode == 'STATIC'}
    if len(names) != len(set(names)) or set(names) != selected:
        raise ValueError('MariaDB builtin authentication/plugin selection differs')
    home = re.search(r'^CMAKE_HOME_DIRECTORY:INTERNAL=([^\n]+)$', cache, re.M)
    compiled = decode(read(evidence / 'mariadb-commands.json'))
    for name in ('libmariadb/mariadb_lib.c', 'libmariadb/secure/openssl.c', 'plugins/auth/caching_sha2_pw.c', 'plugins/auth/ed25519.c'):
        if not home or not any(c.get('file') == home[1] + '/' + name for c in compiled):
            raise ValueError('MariaDB required compilation missing: ' + name)
    commands += '\n' + json.dumps(compiled)
    if '-march=native' in commands or '-mcpu=native' in commands:
        raise ValueError('database dependency uses host-specific instruction selection')


def check_engine(evidence, policy, cache, commands, selection, read):
    bindings = decode(read(evidence / 'database-link.json'))
    if set(bindings) != ARCHIVES or any(set(v) != {'archive', 'sha256'} or not v['archive'].endswith('/' + name) or not re.fullmatch('[0-9a-f]{64}', v['sha256']) for name, v in bindings.items()):
        raise ValueError('database CMake dependency bindings differ')
    for name, macro in (('lib/libpq.a', 'PostgreSQL_LIBRARY_RELEASE'), ('lib/libmariadbclient.a', 'MYSQL_LIBRARIES')):
        if not re.search(r'^' + macro + r':[^=]+=' + re.escape(bindings[name]['archive']) + r'$', cache, re.M):
            raise ValueError('database CMake library selection differs: ' + macro)
    http = decode(read(evidence / 'http-link.json'))
    dependencies = [bindings[name]['archive'] for name in ('lib/libpgcommon.a', 'lib/libpgport.a')]
    dependencies += [http[name]['archive'] for name in ('lib/libssl.a', 'lib/libcrypto.a', 'lib/libz.a')]
    if not re.search(r'^DATABASE_STATIC_LIBRARIES:[^=]+=' + re.escape(';'.join(dependencies)) + r'$', cache, re.M):
        raise ValueError('database CMake transitive dependency selection differs')
    for name in ('postgres', 'mysql'):
        selected = re.search(r'duckdb_extension_load\(' + name + r'_scanner SOURCE_DIR ([^\s()]+) EXTENSION_VERSION ' + policy['wrappers'][name]['revision'] + r'\)', selection)
        if not selected or not any(c.get('file') == selected[1] + '/src/' + name + '_connection.cpp' for c in commands):
            raise ValueError('compiled database wrapper selection missing: ' + name)
