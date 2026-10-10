#!/usr/bin/env python3
"""Retain/verify native build composition, never whole-application admission.

These receipts require an independently authenticated build/source context. Hashes
detect substitution; they do not authenticate a builder or prove exhaustive ELF
membership, vulnerability coverage, or the closure of signed extensions.
"""

import argparse
import base64
import hashlib
import json
from pathlib import Path
import re
import shlex
import shutil
import stat
import sys
import tomllib
import nix_native_database_receipt as database
import nix_native_excel_receipt as excel
import nix_native_avro_receipt as avro
import nix_native_delta_receipt as delta
import nix_native_azure_receipt as azure
import nix_native_vortex_receipt as vortex

PLATFORMS = {'linux/amd64': 'x86_64-unknown-linux-gnu', 'linux/arm64': 'aarch64-unknown-linux-gnu'}
COMMON_RECIPES = (
    'flake.lock', 'nix/native-receipts.nix', 'nix/native-component-lock.json',
    'internal/extension/builtin.go', 'scripts/nix_native_build_receipt.py', 'scripts/nix_native_database_receipt.py', 'scripts/nix_native_excel_receipt.py', 'scripts/nix_native_avro_receipt.py', 'scripts/nix_native_delta_receipt.py', 'scripts/nix_native_azure_receipt.py', 'scripts/nix_native_vortex_receipt.py',
)
LANCE_RECIPES = (
    'nix/lance.nix', 'nix/lance-Cargo.lock', 'nix/quick-xml-backport-lock.json',
    'nix/apply-quick-xml-backports.py',
    'nix/quick-xml/0.37.5-backport.patch', 'nix/quick-xml/0.38.4-backport.patch',
)
CROARING_RECIPES = ('nix/ducklake.nix', 'nix/ducklake-source-lock.json')
HTTP_RECIPES = ('nix/http.nix', 'nix/check-extension-version.py', 'nix/http-source-lock.json', 'nix/httpfs-static-dependencies.patch', 'nix/quack-engine-includes.patch')
HTTP_LIBRARIES = ('curl', 'openssl', 'nghttp2', 'zlib')
HTTP_ARCHIVES = {'lib/libcurl.a', 'lib/libssl.a', 'lib/libcrypto.a', 'lib/libnghttp2.a', 'lib/libz.a'}
BUILD_TAGS = 'duckdb_arrow,duckdb_use_static_lib,leapview_static_lance,leapview_static_sqlite,leapview_static_ducklake,leapview_static_http,leapview_static_database,leapview_static_excel,leapview_static_avro,leapview_static_delta,leapview_static_azure,leapview_static_vortex'
COMPONENTS = ('duckdb', 'lance', 'croaring', 'http', 'database', 'excel', 'avro', 'delta', 'azure', 'vortex')
DUCKDB_RECIPES = ('nix/duckdb.nix', 'nix/sqlite.nix') + CROARING_RECIPES + HTTP_RECIPES + database.RECIPES + excel.RECIPES + avro.RECIPES + delta.RECIPES + azure.RECIPES + vortex.RECIPES
RECIPE_FILES = COMMON_RECIPES + LANCE_RECIPES + DUCKDB_RECIPES + (
    'nix/application.nix', 'nix/patched-runtime.nix', 'nix/glibc-CVE-2026-19499.patch', 'nix/portable.nix',
    'nix/ca-root.sh', 'nix/check-http-default-ca.sh',
)
EVIDENCE = {
    'database': database.EVIDENCE,
    'excel': excel.EVIDENCE,
    'avro': avro.EVIDENCE,
    'delta': delta.EVIDENCE,
    'azure': azure.EVIDENCE,
    'vortex': vortex.EVIDENCE,
    'http': {name + '-' + kind for name in HTTP_LIBRARIES for kind in ('compiler.txt', 'source.json', 'config.txt', 'build.txt')} | {'curl-options.txt', 'nghttp2-options.txt'},
    'lance': {'compiler.txt', 'cargo.txt', 'cargo.jsonl', 'patches.json'},
    'duckdb': {'compiler.txt', 'cmake-cache.txt', 'compile-commands.json', 'extensions.cmake', 'sqlite-source.json', 'croaring-link.json', 'http-link.json', 'database-link.json', 'excel-link.json', 'avro-link.json', 'delta-link.json', 'azure-link.json', 'vortex-link.json', 'vortex-engine-source.json'},
    'croaring': {'compiler.txt', 'cmake-cache.txt', 'compile-commands.json', 'source.json'},
}
LIMIT = 64 * 1024 * 1024
SCOPE = 'authenticated build composition; exhaustive binary closure and vulnerability admission not asserted'


def regular(path):
    try:
        if not stat.S_ISREG(path.lstat().st_mode):
            raise ValueError(f'not a regular file: {path}')
    except OSError as error:
        raise ValueError(f'missing regular file: {path}') from error
    return path


def read(path, limit=LIMIT):
    regular(path)
    if path.stat().st_size > limit:
        raise ValueError(f'evidence exceeds size bound: {path.name}')
    with path.open('rb') as handle:
        data = handle.read(limit + 1)
    if len(data) > limit:
        raise ValueError(f'evidence exceeds size bound: {path.name}')
    return data


def evidence_read(path):
    # Producers read raw build files. Retained evidence uses a canonical base64
    # envelope: literal Nix store references in compiler logs would otherwise
    # drag build-only libraries/toolchains into the image's runtime closure.
    if path.exists() or path.is_symlink():
        return read(path)
    encoded = read(path.with_name(path.name + '.b64'), ((LIMIT + 2) // 3) * 4)
    decoded = base64.b64decode(encoded, validate=True)
    if len(decoded) > LIMIT or base64.b64encode(decoded) != encoded:
        raise ValueError('invalid bounded evidence envelope')
    return decoded


def retain_evidence(source, destination):
    destination.mkdir()
    for path in sorted(source.iterdir()):
        (destination / (path.name + '.b64')).write_bytes(base64.b64encode(read(path)))


def digest(path):
    regular(path)
    with path.open('rb') as handle:
        return hashlib.file_digest(handle, 'sha256').hexdigest()


def canonical(value):
    return (json.dumps(value, sort_keys=True, separators=(',', ':'), allow_nan=False) + '\n').encode()


def write(path, value):
    path.write_bytes(canonical(value))


def capture(stream, destination):
    """Bound compiler stdout while it is produced, not after filling the disk."""
    size = 0
    with destination.open('xb') as output:
        while chunk := stream.read(64 * 1024):
            size += len(chunk)
            if size > LIMIT:
                raise ValueError('Cargo build evidence exceeds size bound')
            output.write(chunk)


def decode_json(data):
    def pairs(items):
        result = {}
        for key, value in items:
            if key in result:
                raise ValueError('duplicate JSON key')
            result[key] = value
        return result
    return json.loads(data, object_pairs_hook=pairs)


def load(path):
    return decode_json(read(path))


def recipes(repo, component='application'):
    names = RECIPE_FILES
    if component in EVIDENCE:
        names = COMMON_RECIPES + {'lance': LANCE_RECIPES, 'duckdb': DUCKDB_RECIPES, 'croaring': CROARING_RECIPES, 'http': HTTP_RECIPES, 'database': database.RECIPES + HTTP_RECIPES, 'excel': excel.RECIPES + HTTP_RECIPES, 'avro': avro.RECIPES + HTTP_RECIPES, 'delta': delta.RECIPES, 'azure': azure.RECIPES + HTTP_RECIPES, 'vortex': vortex.RECIPES}[component]
    return {name: digest(repo / name) for name in names}


def patched_files(repo):
    policy = load(repo / 'nix/quick-xml-backport-lock.json')
    return {f"quick-xml-{patch['version']}/{entry['path']}": entry['patchedSHA256']
            for patch in policy['backports'] for entry in patch['files']}


def capture_patches(repo, vendor, destination):
    expected = patched_files(repo)
    actual = {name: digest(vendor / name) for name in expected}
    if actual != expected:
        raise ValueError('compiled vendored patch source differs')
    write(destination, actual)


def sources(repo, component):
    policy = load(repo / 'nix/native-component-lock.json')
    if component == 'vortex':
        return load(repo / 'nix/vortex-source-lock.json')
    if component == 'delta':
        return load(repo / 'nix/delta-source-lock.json')
    if component == 'azure':
        return load(repo / 'nix/azure-source-lock.json')
    if component == 'avro':
        return load(repo / 'nix/avro-source-lock.json')
    if component == 'excel':
        return load(repo / 'nix/excel-source-lock.json')
    if component == 'database':
        return load(repo / 'nix/database-source-lock.json')
    if component == 'http':
        return load(repo / 'nix/http-source-lock.json')
    if component == 'croaring':
        return load(repo / 'nix/ducklake-source-lock.json')['croaring']
    if component == 'lance':
        source = next(s for s in policy['sources'] if s['name'] == 'extensions/lance')
        return {'revision': source['sourceRevision'],
                'cargoLockSHA256': digest(repo / 'nix/lance-Cargo.lock'),
                'patchSHA256': digest(repo / 'nix/quick-xml-backport-lock.json')}
    sqlite = next(s for s in policy['sourceBuiltReplacements'] if s['name'] == 'sqlite')
    return {'engineRevision': policy['duckdbSourceRevision'],
            'http': load(repo / 'nix/http-source-lock.json')['wrappers'],
            'database': load(repo / 'nix/database-source-lock.json')['wrappers'],
            'excel': load(repo / 'nix/excel-source-lock.json')['wrapper'],
            'avro': load(repo / 'nix/avro-source-lock.json')['wrapper'],
            'delta': load(repo / 'nix/delta-source-lock.json')['wrapper'],
            'azure': load(repo / 'nix/azure-source-lock.json')['wrapper'],
            'vortex': load(repo / 'nix/vortex-source-lock.json')['wrapper'],
            'ducklake': load(repo / 'nix/ducklake-source-lock.json')['ducklake'],
            'sqlite': {key: sqlite[key] for key in ('wrapper', 'amalgamation')}}


def capture_croaring_source(repo, source, destination):
    expected = sources(repo, 'croaring')['patchedFiles']
    actual = {name: digest(source / name) for name in expected}
    if actual != expected:
        raise ValueError('CRoaring source/patch identity differs')
    write(destination, actual)


def capture_http_source(repo, source, destination, library):
    expected = sources(repo, 'http')['libraries'][library]['selectedFiles']
    actual = {name: digest(source / name) for name in expected}
    if actual != expected:
        raise ValueError('HTTP dependency selected source identity differs')
    write(destination, actual)


def check_http(evidence, repo, platform):
    policy = sources(repo, 'http')
    arch = 'aarch64' if platform == 'linux/arm64' else 'x86_64'
    for name in HTTP_LIBRARIES:
        expected = policy['libraries'][name]['selectedFiles']
        if decode_json(evidence_read(evidence / (name + '-source.json'))) != expected:
            raise ValueError('HTTP dependency selected source identity differs: ' + name)
        compiler = evidence_read(evidence / (name + '-compiler.txt')).decode()
        if not re.search(r'^compiler-target: ' + arch + r'-[^\n]*linux[^\n]*$', compiler, re.M):
            raise ValueError('HTTP dependency compiler target differs: ' + name)
        commands = evidence_read(evidence / (name + '-build.txt')).decode()
        if '-march=native' in commands or '-mcpu=native' in commands:
            raise ValueError('HTTP dependency uses host-specific instruction selection')
        source = {'curl': 'url.c', 'openssl': 'ssl_lib.c', 'nghttp2': 'nghttp2_session.c', 'zlib': 'inflate.c'}[name]
        if not any(source in line and ' -c ' in line for line in commands.splitlines()):
            raise ValueError('HTTP dependency actual compilation missing: ' + name)
    config = evidence_read(evidence / 'curl-config.txt').decode()
    for macro in ('USE_OPENSSL', 'USE_NGHTTP2', 'HAVE_LIBZ'):
        if not re.search(r'^#define ' + macro + r' 1$', config, re.M):
            raise ValueError('HTTP CURL feature selection differs: ' + macro)
    for macro in ('USE_GNUTLS', 'USE_RUSTLS', 'USE_LIBSSH2', 'HAVE_BROTLI', 'HAVE_ZSTD'):
        if re.search(r'^#define ' + macro + r' ', config, re.M):
            raise ValueError('unexpected CURL native dependency: ' + macro)
    for name in ('curl', 'nghttp2'):
        options = shlex.split(evidence_read(evidence / (name + '-options.txt')).decode())
        if '--enable-static' not in options or '--disable-shared' not in options or '--enable-shared' in options:
            raise ValueError('HTTP dependency must select static libraries: ' + name)
    tls = evidence_read(evidence / 'openssl-config.txt').decode()
    # Read the effective Configure result, not a producer-supplied feature boolean.
    disabled = tls.partition('Disabled features:')[2].partition('Config target attributes:')[0]
    if not all(re.search(r'^\s+' + name + r'\s+', disabled, re.M) for name in ('shared', 'module')):
        raise ValueError('OpenSSL shared/provider modules are not disabled')
    zlib = evidence_read(evidence / 'zlib-config.txt').decode()
    if not re.search(r'^SHAREDLIB=\s*$', zlib, re.M):
        raise ValueError('zlib shared output is enabled')


def check_croaring(evidence, repo, platform):
    policy = sources(repo, 'croaring')
    if decode_json(evidence_read(evidence / 'source.json')) != policy['patchedFiles']:
        raise ValueError('CRoaring source/patch identity differs')
    cache = evidence_read(evidence / 'cmake-cache.txt').decode()
    for name, value in policy['cmake'].items():
        if not re.search(r'^' + re.escape(name) + r':BOOL=' + ('(ON|TRUE)' if value else '(OFF|FALSE)') + r'$', cache, re.M):
            raise ValueError('CRoaring compiled CMake option differs: ' + name)
    check_c_compiler(evidence, platform)
    home = re.search(r'^CMAKE_HOME_DIRECTORY:INTERNAL=([^\n]+)$', cache, re.M)
    commands = decode_json(evidence_read(evidence / 'compile-commands.json'))
    if not home or not isinstance(commands, list) or not any(c.get('file') == home[1] + '/src/roaring.c' for c in commands):
        raise ValueError('compiled CRoaring source missing')


def check_c_compiler(evidence, platform):
    compiler = evidence_read(evidence / 'compiler.txt').decode()
    arch = 'aarch64' if platform == 'linux/arm64' else 'x86_64'
    if not re.search(r'^compiler-target: ' + arch + r'-[^\n]*linux[^\n]*$', compiler, re.M):
        raise ValueError('C/C++ compiler target differs from platform')


def compiled_cargo(evidence, repo, platform):
    if decode_json(evidence_read(evidence / 'patches.json')) != patched_files(repo):
        raise ValueError('compiled vendored patch evidence differs')
    compiler = evidence_read(evidence / 'compiler.txt').decode()
    if 'rustc 1.98.1' not in compiler or f'host: {PLATFORMS[platform]}' not in compiler:
        raise ValueError('Rust compiler/platform differs from native recipe')
    if not evidence_read(evidence / 'cargo.txt').decode().startswith('cargo 1.98.1'):
        raise ValueError('Cargo compiler differs from native recipe')
    lock = tomllib.loads(read(repo / 'nix/lance-Cargo.lock').decode())
    locked = {(p['name'], p['version'], p.get('source', '')): p for p in lock['package']}
    artifacts, finished, root = {}, False, False
    for line in evidence_read(evidence / 'cargo.jsonl').decode().splitlines():
        # cargoBuildHook also emits its exact command line and hook diagnostics.
        if not line.startswith('{'):
            continue
        item = json.loads(line)
        if item.get('reason') == 'build-finished':
            finished = item.get('success') is True
        if item.get('reason') != 'compiler-artifact':
            continue
        source, fragment = item['package_id'].rsplit('#', 1)
        name, version = fragment.rsplit('@', 1)
        if source.startswith('path+file:'):
            source = ''
        key = (name, version, source)
        if key not in locked:
            raise ValueError(f'compiled package absent from patched Cargo.lock: {name}@{version}')
        package = locked[key]
        value = {'name': name, 'version': version, 'source': source,
                 'checksum': package.get('checksum'), 'target': item['target']['name'],
                 'kinds': sorted(item['target']['kind']), 'features': sorted(item['features']),
                 'profile': item['profile']}
        # Build scripts, proc macros and target crates remain distinct artifacts.
        artifacts[canonical(value)] = value
        root |= (name == 'lance_duckdb_ffi' and 'staticlib' in item['target']['kind'] and
                 any(Path(p).name == 'liblance_duckdb_ffi.a' for p in item['filenames']) and
                 item['profile'].get('test') is False)
    if not finished or not root:
        raise ValueError('receipt requires a successful Cargo build with the produced FFI staticlib')
    return [artifacts[key] for key in sorted(artifacts)]


def check_cmake(evidence, repo, platform):
    cache = evidence_read(evidence / 'cmake-cache.txt').decode()
    expected = platform.replace('/', '_')
    if not re.search(r'^DUCKDB_EXPLICIT_PLATFORM:[^=]+=' + expected + r'$', cache, re.M):
        raise ValueError('CMake platform differs')
    check_c_compiler(evidence, platform)
    commands = decode_json(evidence_read(evidence / 'compile-commands.json'))
    if not isinstance(commands, list) or not commands:
        raise ValueError('missing actual CMake compilation commands')
    selection = evidence_read(evidence / 'extensions.cmake').decode()
    sqlite_source = re.search(r'duckdb_extension_load\(sqlite_scanner SOURCE_DIR ([^\s()]+)\)', selection)
    ducklake_source = re.search(r'duckdb_extension_load\(ducklake SOURCE_DIR ([^\s()]+) EXTENSION_VERSION ' + re.escape(sources(repo, 'duckdb')['ducklake']['revision']) + r'\)', selection)
    if 'duckdb_extension_load(lance SOURCE_DIR ' not in selection or not sqlite_source or not ducklake_source:
        raise ValueError('source-built extension selection missing')
    if not any(c.get('file') == ducklake_source[1] + '/src/storage/ducklake_deletion_vector.cpp' for c in commands):
        raise ValueError('compiled DuckLake deletion-vector source missing')
    http = decode_json(evidence_read(evidence / 'http-link.json'))
    if set(http) != HTTP_ARCHIVES or any(set(v) != {'archive', 'sha256'} or not v['archive'].endswith('/' + name) or not re.fullmatch('[0-9a-f]{64}', v['sha256']) for name, v in http.items()):
        raise ValueError('HTTP CMake dependency bindings differ')
    for name, macro in (('lib/libcurl.a', 'CURL_LIBRARY_RELEASE'), ('lib/libssl.a', 'OPENSSL_SSL_LIBRARY'), ('lib/libcrypto.a', 'OPENSSL_CRYPTO_LIBRARY')):
        if not re.search(r'^' + macro + r':[^=]+=' + re.escape(http[name]['archive']) + r'$', cache, re.M):
            raise ValueError('HTTP CMake library selection differs: ' + macro)
    expected_deps = ';'.join(http[name]['archive'] for name in ('lib/libssl.a', 'lib/libcrypto.a', 'lib/libnghttp2.a', 'lib/libz.a'))
    if not re.search(r'^HTTPFS_STATIC_LIBRARIES:[^=]+=' + re.escape(expected_deps) + r'$', cache, re.M):
        raise ValueError('HTTP CMake transitive archive selection differs')
    for name, source_file in (('httpfs', 'src/httpfs.cpp'), ('quack', 'src/quack_client.cpp')):
        selected_source = re.search(r'duckdb_extension_load\(' + name + r' SOURCE_DIR ([^\s()]+) EXTENSION_VERSION ' + sources(repo, 'duckdb')['http'][name]['revision'] + r'\)', selection)
        if not selected_source or not any(c.get('file') == selected_source[1] + '/' + source_file for c in commands):
            raise ValueError('compiled HTTP wrapper selection missing: ' + name)
    database.check_engine(evidence, sources(repo, 'database'), cache, commands, selection, evidence_read)
    excel.check_engine(evidence, sources(repo, 'excel'), cache, commands, selection, evidence_read)
    vortex.check_engine(evidence, sources(repo, 'vortex'), cache, commands, selection, evidence_read)
    delta.check_engine(evidence, sources(repo, 'delta'), cache, commands, selection, evidence_read)
    avro.check_engine(evidence, sources(repo, 'avro'), cache, commands, selection, evidence_read)
    azure.check_engine(evidence, sources(repo, 'azure'), cache, commands, selection, evidence_read)
    roaring = decode_json(evidence_read(evidence / 'croaring-link.json'))
    if (set(roaring) != {'archive', 'sha256'} or not roaring['archive'].endswith('/lib/libroaring.a') or
            not re.fullmatch('[0-9a-f]{64}', roaring['sha256']) or
            not re.search(r'^roaring_DIR:[^=]+=' + re.escape(roaring['archive'].removesuffix('/lib/libroaring.a')) + r'/lib/cmake/roaring/?$', cache, re.M)):
        raise ValueError('DuckLake CRoaring CMake dependency binding differs')
    if not any(c.get('file', '') == sqlite_source[1] + '/src/sqlite/sqlite3.c' for c in commands):
        raise ValueError('selected SQLite amalgamation absent from CMake compilation')
    sqlite = sources(repo, 'duckdb')['sqlite']['amalgamation']
    expected_sqlite = {name: sqlite[name + 'SHA256'].removeprefix('sha256:') for name in ('sqlite3.c', 'sqlite3.h')}
    if decode_json(evidence_read(evidence / 'sqlite-source.json')) != expected_sqlite:
        raise ValueError('compiled SQLite source differs from patched recipe')


def output_hashes(component, root):
    if component == 'lance':
        paths = [root / 'lib/liblance_duckdb_ffi.a']
    elif component == 'azure':
        paths = [root / name for name in sorted(azure.ARCHIVES)]
        if any(root.rglob('*.so*')):
            raise ValueError('Azure dependencies must be static outputs')
    elif component == 'avro':
        paths = [root / name for name in sorted(avro.ARCHIVES)]
        if any(root.rglob('*.so*')):
            raise ValueError('Avro dependencies must be static outputs')
    elif component == 'vortex':
        paths = [root / 'lib/libvortex_duckdb.a']
        if list((root / 'lib').glob('*.so*')):
            raise ValueError('Vortex FFI must be a static output')
    elif component == 'delta':
        paths = [root / 'lib/libdelta_kernel_ffi.a']
        if list((root / 'lib').glob('*.so*')):
            raise ValueError('Delta FFI must be a static output')
    elif component == 'excel':
        paths = [root / name for name in sorted(excel.ARCHIVES)]
        if list((root / 'lib').glob('*.so*')):
            raise ValueError('Excel dependencies must be static outputs')
    elif component == 'database':
        paths = [root / name for name in sorted(database.ARCHIVES)]
        if list((root / 'lib').glob('*.so*')):
            raise ValueError('database dependencies must be static outputs')
    elif component == 'http':
        paths = [root / name for name in sorted(HTTP_ARCHIVES)]
        if list((root / 'lib').glob('*.so*')):
            raise ValueError('HTTP dependencies must be static outputs')
    elif component == 'croaring':
        paths = [root / 'lib/libroaring.a']
        if list((root / 'lib').glob('libroaring.so*')):
            raise ValueError('CRoaring must be a static output')
    else:
        paths = sorted((root / 'lib').glob('*.a'))
        names = {p.name for p in paths}
        if not {'libduckdb_static.a', 'liblance_extension.a', 'libsqlite_scanner_extension.a', 'libducklake_extension.a', 'libhttpfs_extension.a', 'libquack_extension.a', 'libpostgres_scanner_extension.a', 'libmysql_scanner_extension.a', 'libexcel_extension.a', 'libavro_extension.a', 'libdelta_extension.a', 'libazure_extension.a', 'libvortex_extension.a'} <= names:
            raise ValueError('source-built DuckDB/Lance/SQLite/DuckLake archive output missing')
    for path in paths:
        regular(path)
        with path.open('rb') as handle:
            if handle.read(8) != b'!<arch>\n':
                raise ValueError('native output is not a regular ar archive')
    return {str(p.relative_to(root)): digest(p) for p in paths}


def component_value(component, platform, repo, evidence, outputs):
    if platform not in PLATFORMS or component not in EVIDENCE:
        raise ValueError('unsupported native component/platform')
    if evidence.is_symlink() or {p.name for p in evidence.iterdir()} not in (EVIDENCE[component], {name + '.b64' for name in EVIDENCE[component]}):
        raise ValueError('unexpected or missing component evidence')
    hashes = {name: hashlib.sha256(evidence_read(evidence / name)).hexdigest() for name in sorted(EVIDENCE[component])}
    if sum(len(evidence_read(evidence / name)) for name in hashes) > 2 * LIMIT:
        raise ValueError('component evidence exceeds aggregate bound')
    value = {'schemaVersion': 1, 'component': component, 'platform': platform, 'scope': SCOPE,
             'recipes': recipes(repo, component), 'sources': sources(repo, component), 'evidence': hashes, 'outputs': outputs}
    if component == 'lance':
        value['compiledCargo'] = compiled_cargo(evidence, repo, platform)
    elif component == 'vortex':
        value['compiledCargo'] = vortex.check(evidence, sources(repo, component), platform, tomllib.loads(read(repo / 'nix/vortex-Cargo.lock').decode()), evidence_read)
    elif component == 'delta':
        value['compiledCargo'] = delta.check(evidence, sources(repo, component), platform, tomllib.loads(read(repo / 'nix/delta-Cargo.lock').decode()), evidence_read)
    elif component == 'azure':
        azure.check(evidence, sources(repo, component), platform, evidence_read)
    elif component == 'avro':
        avro.check(evidence, sources(repo, component), platform, evidence_read)
    elif component == 'excel':
        excel.check(evidence, sources(repo, component), platform, evidence_read)
    elif component == 'database':
        database.check(evidence, sources(repo, component), platform, evidence_read)
    elif component == 'http':
        check_http(evidence, repo, platform)
    elif component == 'croaring':
        check_croaring(evidence, repo, platform)
    else:
        check_cmake(evidence, repo, platform)
    return value


def create_component(component, platform, repo, evidence, output_root, destination):
    value = component_value(component, platform, repo, evidence, output_hashes(component, output_root))
    destination.mkdir()  # Never overwrite a previous receipt.
    retain_evidence(evidence, destination / 'evidence')
    write(destination / 'receipt.json', value)
    return value


def verify_component(directory, component, platform, repo, output_root=None):
    if directory.is_symlink() or {p.name for p in directory.iterdir()} != {'receipt.json', 'evidence'}:
        raise ValueError('unexpected component receipt directory')
    value = load(directory / 'receipt.json')
    if type(value.get('schemaVersion')) is not int or value.get('component') != component or value.get('platform') != platform:
        raise ValueError('receipt component/platform mismatch')
    if {p.name for p in (directory / 'evidence').iterdir()} != {name + '.b64' for name in EVIDENCE[component]}:
        raise ValueError('missing retained component evidence envelopes')
    outputs = value.get('outputs')
    if not isinstance(outputs, dict) or not outputs or any(
        not re.fullmatch(r'lib/lib[A-Za-z0-9_.+-]+\.a', name) or not re.fullmatch('[0-9a-f]{64}', sha)
        for name, sha in outputs.items()
    ):
        raise ValueError('invalid native output identity')
    if component == 'vortex':
        if set(outputs) != vortex.ARCHIVES:
            raise ValueError('Vortex selected archive output differs')
        expected_headers = vortex.headers(evidence_read(directory / 'evidence/headers.json'), sources(repo, 'vortex'))
        if output_root is not None and {name: digest(output_root / name) for name in expected_headers} != expected_headers:
            raise ValueError('Vortex generated header output substitution')
    if component == 'delta' and set(outputs) != delta.ARCHIVES:
        raise ValueError('unexpected Delta native outputs')
    if component == 'delta' and output_root is not None and delta.header_hashes(output_root / 'include', read) != decode_json(evidence_read(directory / 'evidence/headers.json')):
        raise ValueError('Delta output headers differ')
    if component == 'azure' and set(outputs) != azure.ARCHIVES:
        raise ValueError('unexpected Azure native outputs')
    if component == 'avro' and set(outputs) != avro.ARCHIVES:
        raise ValueError('unexpected Avro native outputs')
    if component == 'excel' and set(outputs) != excel.ARCHIVES:
        raise ValueError('unexpected Excel native outputs')
    if component == 'database' and set(outputs) != database.ARCHIVES:
        raise ValueError('unexpected database native outputs')
    if component == 'http' and set(outputs) != HTTP_ARCHIVES:
        raise ValueError('unexpected HTTP native outputs')
    if component == 'lance' and set(outputs) != {'lib/liblance_duckdb_ffi.a'}:
        raise ValueError('unexpected Lance outputs')
    if component == 'croaring' and set(outputs) != {'lib/libroaring.a'}:
        raise ValueError('unexpected CRoaring outputs')
    if component == 'duckdb' and not {'lib/libduckdb_static.a', 'lib/liblance_extension.a', 'lib/libsqlite_scanner_extension.a', 'lib/libducklake_extension.a', 'lib/libhttpfs_extension.a', 'lib/libquack_extension.a', 'lib/libpostgres_scanner_extension.a', 'lib/libmysql_scanner_extension.a', 'lib/libexcel_extension.a', 'lib/libavro_extension.a', 'lib/libdelta_extension.a', 'lib/libazure_extension.a', 'lib/libvortex_extension.a'} <= outputs.keys():
        raise ValueError('missing source-built native outputs')
    if output_root is not None and outputs != output_hashes(component, output_root):
        raise ValueError('native output substitution')
    expected = component_value(component, platform, repo, directory / 'evidence', outputs)
    if value != expected or read(directory / 'receipt.json') != canonical(value):
        raise ValueError('component receipt/evidence/recipe mismatch')
    return value


def compose(repo, platform, revision, duckdb, lance, binaries, link_inputs, evidence, destination, croaring, http, database_root, excel_root, avro_root, delta_root, azure_root, vortex_root):
    if not re.fullmatch('[0-9a-f]{40}', revision):
        raise ValueError('application source revision required')
    components = {}
    expected_inputs = {}
    for name, root in (('duckdb', duckdb), ('lance', lance), ('croaring', croaring), ('http', http), ('database', database_root), ('excel', excel_root), ('avro', avro_root), ('delta', delta_root), ('azure', azure_root), ('vortex', vortex_root)):
        directory = root / 'share/leapview/native-build'
        value = verify_component(directory, name, platform, repo, root)
        components[name] = value
        for output, sha in value['outputs'].items():
            if Path(output).name != 'libdummy_static_extension_loader.a':
                expected_inputs[str(root / output)] = sha
    if decode_json(evidence_read(duckdb / 'share/leapview/native-build/evidence/croaring-link.json')) != {'archive': str(croaring / 'lib/libroaring.a'), 'sha256': components['croaring']['outputs']['lib/libroaring.a']}:
        raise ValueError('DuckLake compiled and application-selected CRoaring differ')
    if decode_json(evidence_read(duckdb / 'share/leapview/native-build/evidence/http-link.json')) != {name: {'archive': str(http / name), 'sha256': sha} for name, sha in components['http']['outputs'].items()}:
        raise ValueError('HTTP compiled and application-selected dependencies differ')
    if decode_json(evidence_read(duckdb / 'share/leapview/native-build/evidence/database-link.json')) != {name: {'archive': str(database_root / name), 'sha256': sha} for name, sha in components['database']['outputs'].items()}:
        raise ValueError('database compiled and application-selected dependencies differ')
    if decode_json(evidence_read(duckdb / 'share/leapview/native-build/evidence/excel-link.json')) != {name: {'archive': str(excel_root / name), 'sha256': sha} for name, sha in components['excel']['outputs'].items()}:
        raise ValueError('Excel compiled and application-selected dependencies differ')
    if decode_json(evidence_read(duckdb / 'share/leapview/native-build/evidence/avro-link.json')) != {name: {'archive': str(avro_root / name), 'sha256': sha} for name, sha in components['avro']['outputs'].items()}:
        raise ValueError('Avro compiled and application-selected dependencies differ')
    vortex_link = vortex.binding(evidence_read(duckdb / 'share/leapview/native-build/evidence/vortex-link.json'), sources(repo, 'vortex'))
    if vortex_link != {'archive': str(vortex_root / 'lib/libvortex_duckdb.a'), 'sha256': components['vortex']['outputs']['lib/libvortex_duckdb.a'], 'headers': {name: digest(vortex_root / name) for name in sources(repo, 'vortex')['headers']}, 'engineHeaders': sources(repo, 'vortex')['engine']['selectedFiles']}:
        raise ValueError('Vortex selected FFI/header composition differs')
    delta_link = delta.binding(evidence_read(duckdb / 'share/leapview/native-build/evidence/delta-link.json'))
    if delta_link != {'archive': str(delta_root / 'lib/libdelta_kernel_ffi.a'), 'sha256': components['delta']['outputs']['lib/libdelta_kernel_ffi.a'], 'headers': delta.header_hashes(delta_root / 'include', read)}:
        raise ValueError('Delta compiled and application-selected FFI/header differ')
    if decode_json(evidence_read(duckdb / 'share/leapview/native-build/evidence/azure-link.json')) != {name: {'archive': str(azure_root / name), 'sha256': sha} for name, sha in components['azure']['outputs'].items()}:
        raise ValueError('Azure compiled and application-selected dependencies differ')
    azure_links = decode_json(evidence_read(azure_root / 'share/leapview/native-build/evidence/native-link.json'))
    if {name: v['sha256'] for name, v in azure_links.items()} != components['http']['outputs'] | {'lib/libxml2.a': components['azure']['outputs']['lib/libxml2.a']}:
        raise ValueError('Azure compiled HTTP/XML dependencies differ from selected archives')
    avro_links = decode_json(evidence_read(avro_root / 'share/leapview/native-build/evidence/library-link.json'))
    if {name: v['sha256'] for name, v in avro_links.items()} != components['avro']['outputs'] | {'lib/libz.a': components['http']['outputs']['lib/libz.a']}:
        raise ValueError('Avro selected library composition differs')
    if decode_json(evidence_read(excel_root / 'share/leapview/native-build/evidence/zlib-link.json'))['sha256'] != components['http']['outputs']['lib/libz.a']:
        raise ValueError('Excel and HTTP selected zlib bytes differ')
    selected = read(link_inputs).decode().splitlines()
    if len(selected) != len(set(selected)) or set(selected) != set(expected_inputs):
        raise ValueError('application selected native link inputs differ from compiled receipts')
    if {p.name for p in evidence.iterdir()} != {'go.txt', 'link-flags.txt', 'tags.txt'}:
        raise ValueError('missing application compiler/link selection evidence')
    flags = read(evidence / 'link-flags.txt').decode()
    if shlex.split(flags) != ['-Wl,--start-group', *selected, '-Wl,--end-group', '-lstdc++', '-ldl', '-lm']:
        raise ValueError('application linker flags differ from selected archives')
    if read(evidence / 'tags.txt').decode().strip() != BUILD_TAGS:
        raise ValueError('application static extension build tags differ')
    destination.mkdir()
    for name, root in (('duckdb', duckdb), ('lance', lance), ('croaring', croaring), ('http', http), ('database', database_root), ('excel', excel_root), ('avro', avro_root), ('delta', delta_root), ('azure', azure_root), ('vortex', vortex_root)):
        shutil.copytree(root / 'share/leapview/native-build', destination / name)
    retain_evidence(evidence, destination / 'application-evidence')
    value = {'schemaVersion': 1, 'scope': SCOPE, 'platform': platform, 'revision': revision,
             'recipes': recipes(repo),
             'components': {name: digest(destination / name / 'receipt.json') for name in components},
             'linkInputs': {name: {p: sha for p, sha in component['outputs'].items() if Path(p).name != 'libdummy_static_extension_loader.a'} for name, component in components.items()},
             'evidence': {p.name: digest(p) for p in sorted(evidence.iterdir())},
             'outputs': binary_hashes(binaries, platform)}
    write(destination / 'application.json', value)
    return value


def binary_hashes(binaries, platform):
    if platform not in PLATFORMS:
        raise ValueError('unsupported application platform')
    result = {}
    for name in ('leapview', 'leapviewctl'):
        path = regular(binaries / name)
        with path.open('rb') as handle:
            header = handle.read(20)
        machine = 62 if platform == 'linux/amd64' else 183
        if header[:6] != b'\x7fELF\x02\x01' or len(header) != 20 or int.from_bytes(header[18:20], 'little') != machine:
            raise ValueError('application ELF architecture differs from receipt platform')
        result[name] = digest(path)
    return result


def verify_application(directory, repo, platform, revision, binaries):
    if directory.is_symlink() or not re.fullmatch('[0-9a-f]{40}', revision):
        raise ValueError('invalid application receipt context')
    value = load(directory / 'application.json')
    if type(value.get('schemaVersion')) is not int or value.get('schemaVersion') != 1 or value.get('scope') != SCOPE or value.get('platform') != platform or value.get('revision') != revision or value.get('recipes') != recipes(repo):
        raise ValueError('application source/platform/recipe mismatch')
    if set(value) != {'schemaVersion', 'scope', 'platform', 'revision', 'recipes', 'components', 'linkInputs', 'evidence', 'outputs'}:
        raise ValueError('unexpected application receipt fields')
    if set(value['components']) != set(COMPONENTS) or set(value['linkInputs']) != set(COMPONENTS):
        raise ValueError('missing compiled component receipt')
    for name in COMPONENTS:
        if value['components'][name] != digest(directory / name / 'receipt.json'):
            raise ValueError('component receipt substitution')
        component = verify_component(directory / name, name, platform, repo)
        inputs = {p: sha for p, sha in component['outputs'].items() if Path(p).name != 'libdummy_static_extension_loader.a'}
        if value['linkInputs'][name] != inputs:
            raise ValueError('application link input substitution')
    if decode_json(evidence_read(directory / 'duckdb/evidence/croaring-link.json'))['sha256'] != value['linkInputs']['croaring']['lib/libroaring.a']:
        raise ValueError('DuckLake compiled and application-selected CRoaring differ')
    if {name: v['sha256'] for name, v in decode_json(evidence_read(directory / 'duckdb/evidence/http-link.json')).items()} != value['linkInputs']['http']:
        raise ValueError('HTTP compiled and application-selected dependencies differ')
    if {name: v['sha256'] for name, v in decode_json(evidence_read(directory / 'duckdb/evidence/database-link.json')).items()} != value['linkInputs']['database']:
        raise ValueError('database compiled and application-selected dependencies differ')
    if {name: v['sha256'] for name, v in decode_json(evidence_read(directory / 'duckdb/evidence/excel-link.json')).items()} != value['linkInputs']['excel']:
        raise ValueError('Excel compiled and application-selected dependencies differ')
    vortex_link = vortex.binding(evidence_read(directory / 'duckdb/evidence/vortex-link.json'), sources(repo, 'vortex'))
    if vortex_link['sha256'] != value['linkInputs']['vortex']['lib/libvortex_duckdb.a'] or vortex_link['headers'] != vortex.headers(evidence_read(directory / 'vortex/evidence/headers.json'), sources(repo, 'vortex')):
        raise ValueError('Vortex retained FFI/header composition differs')
    delta_link = delta.binding(evidence_read(directory / 'duckdb/evidence/delta-link.json'))
    if delta_link['sha256'] != value['linkInputs']['delta']['lib/libdelta_kernel_ffi.a'] or delta_link['headers'] != decode_json(evidence_read(directory / 'delta/evidence/headers.json')):
        raise ValueError('Delta retained FFI/header composition differs')
    if {name: v['sha256'] for name, v in decode_json(evidence_read(directory / 'duckdb/evidence/avro-link.json')).items()} != value['linkInputs']['avro']:
        raise ValueError('Avro compiled and application-selected dependencies differ')
    if {name: v['sha256'] for name, v in decode_json(evidence_read(directory / 'avro/evidence/library-link.json')).items()} != value['linkInputs']['avro'] | {'lib/libz.a': value['linkInputs']['http']['lib/libz.a']}:
        raise ValueError('Avro selected library composition differs')
    if {name: v['sha256'] for name, v in decode_json(evidence_read(directory / 'duckdb/evidence/azure-link.json')).items()} != value['linkInputs']['azure']:
        raise ValueError('Azure compiled and application-selected dependencies differ')
    if {name: v['sha256'] for name, v in decode_json(evidence_read(directory / 'azure/evidence/native-link.json')).items()} != value['linkInputs']['http'] | {'lib/libxml2.a': value['linkInputs']['azure']['lib/libxml2.a']}:
        raise ValueError('Azure compiled HTTP/XML dependencies differ from selected archives')
    if decode_json(evidence_read(directory / 'excel/evidence/zlib-link.json'))['sha256'] != value['linkInputs']['http']['lib/libz.a']:
        raise ValueError('Excel and HTTP selected zlib bytes differ')
    if set(value['evidence']) != {'go.txt', 'link-flags.txt', 'tags.txt'}:
        raise ValueError('missing application compilation evidence')
    app_evidence = directory / 'application-evidence'
    if app_evidence.is_symlink() or {p.name for p in app_evidence.iterdir()} != {name + '.b64' for name in value['evidence']}:
        raise ValueError('unexpected application compilation evidence directory')
    for name, sha in value['evidence'].items():
        if hashlib.sha256(evidence_read(directory / 'application-evidence' / name)).hexdigest() != sha:
            raise ValueError('application compilation evidence substitution')
    flags = shlex.split(evidence_read(directory / 'application-evidence/link-flags.txt').decode())
    expected_names = [Path(p).name for component in value['linkInputs'].values() for p in component]
    if (flags[:1] != ['-Wl,--start-group'] or flags[-4:] != ['-Wl,--end-group', '-lstdc++', '-ldl', '-lm'] or
            sorted(Path(p).name for p in flags[1:-4]) != sorted(expected_names)):
        raise ValueError('application compiler link selection differs from receipts')
    if evidence_read(directory / 'application-evidence/tags.txt').decode().strip() != BUILD_TAGS:
        raise ValueError('application compiler tags differ')
    if not evidence_read(directory / 'application-evidence/go.txt').decode().startswith('go version go'):
        raise ValueError('missing Go compiler identity')
    if set(value['outputs']) != {'leapview', 'leapviewctl'} or any(not re.fullmatch('[0-9a-f]{64}', sha) for sha in value['outputs'].values()):
        raise ValueError('invalid application output identities')
    if binaries is not None and value['outputs'] != binary_hashes(binaries, platform):
        raise ValueError('application output substitution')
    if read(directory / 'application.json') != canonical(value):
        raise ValueError('noncanonical application receipt')
    return {'schemaVersion': 1, 'scope': SCOPE, 'platform': platform, 'revision': revision,
            'applicationReceiptSHA256': digest(directory / 'application.json'),
            'unresolved': ['signed extension compiled closures', 'engine vendored dependency identities', 'fresh complete native vulnerability scan']}


def validate_replacements(data):
    pairs = decode_json(data)
    if not isinstance(pairs, list) or len(pairs) != 2:
        raise ValueError('runtime replacement mapping differs from recipe')
    for pair, component in zip(pairs, ('glibc', 'gcc')):
        if not isinstance(pair, dict) or set(pair) != {'old', 'new'}:
            raise ValueError('invalid runtime replacement mapping')
        pattern = r'/nix/store/[0-9a-z]{32}-' + component + r'-[^/\s]+'
        if any(not re.fullmatch(pattern, value) for value in pair.values()) or len(pair['old']) != len(pair['new']) or pair['old'] == pair['new']:
            raise ValueError('invalid same-length runtime replacement identity')
    return pairs


def runtime_rewrite(directory, repo, platform, revision, original, rewritten, destination, replacements):
    verify_application(directory, repo, platform, revision, original)
    verify_application(destination, repo, platform, revision, None)
    if digest(directory / 'application.json') != digest(destination / 'application.json'):
        raise ValueError('runtime transformation replaced its original receipt')
    data = canonical(validate_replacements(read(replacements)))
    value = {'schemaVersion': 1, 'scope': SCOPE, 'platform': platform, 'revision': revision,
             'applicationReceiptSHA256': digest(directory / 'application.json'),
             'inputOutputs': load(directory / 'application.json')['outputs'],
             'replacementMappingSHA256': hashlib.sha256(data).hexdigest(),
             'outputs': binary_hashes(rewritten, platform)}
    # The destination is the existing output of replaceDirectDependencies.
    with (destination / 'runtime-replacements.json.b64').open('xb') as output:
        output.write(base64.b64encode(data))
    with (destination / 'runtime.json').open('xb') as output:
        output.write(canonical(value))
    return verify_runtime(destination, repo, platform, revision, rewritten)


def verify_runtime(directory, repo, platform, revision, binaries=None):
    result = verify_application(directory, repo, platform, revision, None)
    value = load(directory / 'runtime.json')
    if (set(value) != {'schemaVersion', 'scope', 'platform', 'revision', 'applicationReceiptSHA256', 'inputOutputs', 'replacementMappingSHA256', 'outputs'} or
            type(value['schemaVersion']) is not int or value['schemaVersion'] != 1 or value['scope'] != SCOPE or value['platform'] != platform or value['revision'] != revision or
            value['applicationReceiptSHA256'] != result['applicationReceiptSHA256'] or
            value['inputOutputs'] != load(directory / 'application.json')['outputs']):
        raise ValueError('runtime transformation input substitution')
    if (directory / 'runtime-replacements.json').exists() or not (directory / 'runtime-replacements.json.b64').is_file():
        raise ValueError('runtime mapping requires encoded evidence')
    data = evidence_read(directory / 'runtime-replacements.json')
    if canonical(validate_replacements(data)) != data or hashlib.sha256(data).hexdigest() != value['replacementMappingSHA256']:
        raise ValueError('runtime replacement mapping substitution')
    if set(value['outputs']) != {'leapview', 'leapviewctl'} or any(not re.fullmatch('[0-9a-f]{64}', sha) for sha in value['outputs'].values()):
        raise ValueError('invalid runtime output identities')
    if binaries is not None and value['outputs'] != binary_hashes(binaries, platform):
        raise ValueError('runtime output substitution')
    if read(directory / 'runtime.json') != canonical(value):
        raise ValueError('noncanonical runtime receipt')
    result['runtimeReceiptSHA256'] = digest(directory / 'runtime.json')
    return result


def portable(directory, repo, platform, revision, original, exported, destination, interpreter, tool_version):
    runtime = verify_runtime(directory, repo, platform, revision, original)
    if not re.fullmatch(r'/lib(64)?/ld-linux-[A-Za-z0-9_-]+\.so\.[0-9]+', interpreter):
        raise ValueError('unexpected portable ELF interpreter')
    version = read(tool_version).decode().strip()
    if not version.startswith('patchelf '):
        raise ValueError('missing patchelf compiler identity')
    value = {'schemaVersion': 1, 'scope': SCOPE, 'platform': platform, 'revision': revision,
             'applicationReceiptSHA256': digest(directory / 'application.json'),
             'runtimeReceiptSHA256': runtime['runtimeReceiptSHA256'],
             'inputOutputs': load(directory / 'runtime.json')['outputs'],
             'transform': {'tool': version, 'arguments': ['--no-sort', '--set-interpreter', interpreter, '--remove-rpath']},
             'outputs': binary_hashes(exported, platform)}
    shutil.copytree(directory, destination)
    write(destination / 'portable.json', value)
    return verify_portable(destination, repo, platform, revision, exported)


def verify_portable(directory, repo, platform, revision, binaries):
    result = verify_runtime(directory, repo, platform, revision)
    value = load(directory / 'portable.json')
    if (set(value) != {'schemaVersion', 'scope', 'platform', 'revision', 'applicationReceiptSHA256', 'runtimeReceiptSHA256', 'inputOutputs', 'transform', 'outputs'} or
            type(value['schemaVersion']) is not int or value['schemaVersion'] != 1 or value['scope'] != SCOPE or value['platform'] != platform or value['revision'] != revision or
            value['applicationReceiptSHA256'] != result['applicationReceiptSHA256'] or
            value['runtimeReceiptSHA256'] != result['runtimeReceiptSHA256'] or
            value['inputOutputs'] != load(directory / 'runtime.json')['outputs']):
        raise ValueError('portable transformation input substitution')
    transform = value['transform']
    if (set(transform) != {'tool', 'arguments'} or not transform['tool'].startswith('patchelf ') or
            len(transform['arguments']) != 4 or transform['arguments'][:2] != ['--no-sort', '--set-interpreter'] or
            transform['arguments'][-1] != '--remove-rpath' or
            not re.fullmatch(r'/lib(64)?/ld-linux-[A-Za-z0-9_-]+\.so\.[0-9]+', transform['arguments'][2])):
        raise ValueError('portable transformation differs from recipe')
    if value['outputs'] != binary_hashes(binaries, platform):
        raise ValueError('portable output substitution')
    if read(directory / 'portable.json') != canonical(value):
        raise ValueError('noncanonical portable receipt')
    result['portableReceiptSHA256'] = digest(directory / 'portable.json')
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=['component', 'compose', 'verify', 'patches', 'runtime', 'verify-runtime', 'portable', 'verify-portable', 'capture-cargo', 'croaring-source', 'http-source', 'database-source'])
    parser.add_argument('--repo', type=Path, required=True)
    parser.add_argument('--platform', choices=PLATFORMS, required=True)
    parser.add_argument('--component', choices=EVIDENCE)
    for flag in ('evidence', 'output-root', 'destination', 'duckdb', 'lance', 'croaring', 'http', 'database', 'excel', 'avro', 'delta', 'azure', 'vortex', 'binaries', 'link-inputs', 'input-receipt', 'tool-version', 'replacements'):
        parser.add_argument('--' + flag, type=Path)
    parser.add_argument('--library', choices=HTTP_LIBRARIES + database.LIBRARIES)
    parser.add_argument('--revision')
    parser.add_argument('--interpreter')
    args = parser.parse_args()
    required = {
        'component': ('component', 'evidence', 'output_root', 'destination'),
        'compose': ('revision', 'duckdb', 'lance', 'croaring', 'http', 'database', 'excel', 'avro', 'delta', 'azure', 'vortex', 'binaries', 'link_inputs', 'evidence', 'destination'),
        'verify': ('revision', 'binaries', 'destination'),
        'verify-runtime': ('revision', 'binaries', 'destination'),
        'verify-portable': ('revision', 'binaries', 'destination'),
        'runtime': ('revision', 'binaries', 'input_receipt', 'output_root', 'destination', 'replacements'),
        'portable': ('revision', 'binaries', 'input_receipt', 'output_root', 'destination', 'interpreter', 'tool_version'),
        'patches': ('output_root', 'destination'),
        'database-source': ('output_root', 'destination', 'library'),
        'http-source': ('output_root', 'destination', 'library'),
        'croaring-source': ('output_root', 'destination'),
        'capture-cargo': ('destination',),
    }
    for name in required[args.command]:
        if getattr(args, name) is None:
            parser.error(f'{args.command} requires --{name.replace("_", "-")}')
    if args.command == 'capture-cargo':
        capture(sys.stdin.buffer, args.destination)
        return
    if args.command == 'patches':
        capture_patches(args.repo, args.output_root, args.destination)
        return
    if args.command == 'database-source':
        expected = sources(args.repo, 'database')['libraries'][args.library]['selectedFiles']
        actual = {name: digest(args.output_root / name) for name in expected}
        if actual != expected:
            raise ValueError('database selected source differs')
        write(args.destination, actual)
        return
    if args.command == 'http-source':
        capture_http_source(args.repo, args.output_root, args.destination, args.library)
        return
    if args.command == 'croaring-source':
        capture_croaring_source(args.repo, args.output_root, args.destination)
        return
    if args.command == 'component':
        result = create_component(args.component, args.platform, args.repo, args.evidence, args.output_root, args.destination)
    elif args.command == 'compose':
        result = compose(args.repo, args.platform, args.revision, args.duckdb, args.lance, args.binaries, args.link_inputs, args.evidence, args.destination, args.croaring, args.http, args.database, args.excel, args.avro, args.delta, args.azure, args.vortex)
    elif args.command == 'portable':
        result = portable(args.input_receipt, args.repo, args.platform, args.revision, args.binaries, args.output_root, args.destination, args.interpreter, args.tool_version)
    elif args.command == 'runtime':
        result = runtime_rewrite(args.input_receipt, args.repo, args.platform, args.revision, args.binaries, args.output_root, args.destination, args.replacements)
    elif args.command == 'verify-runtime':
        result = verify_runtime(args.destination, args.repo, args.platform, args.revision, args.binaries)
    elif args.command == 'verify-portable':
        result = verify_portable(args.destination, args.repo, args.platform, args.revision, args.binaries)
    else:
        result = verify_application(args.destination, args.repo, args.platform, args.revision, args.binaries)
    print(json.dumps(result, sort_keys=True))


if __name__ == '__main__':
    try:
        main()
    except (ValueError, OSError, KeyError, TypeError) as error:
        raise SystemExit(f'native build receipt: {error}') from error
