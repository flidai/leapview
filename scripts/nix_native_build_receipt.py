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

PLATFORMS = {'linux/amd64': 'x86_64-unknown-linux-gnu', 'linux/arm64': 'aarch64-unknown-linux-gnu'}
COMMON_RECIPES = (
    'flake.lock', 'nix/native-receipts.nix', 'nix/native-component-lock.json',
    'internal/extension/builtin.go', 'scripts/nix_native_build_receipt.py',
)
LANCE_RECIPES = (
    'nix/lance.nix', 'nix/lance-Cargo.lock', 'nix/quick-xml-backport-lock.json',
    'nix/apply-quick-xml-backports.py',
    'nix/quick-xml/0.37.5-backport.patch', 'nix/quick-xml/0.38.4-backport.patch',
)
DUCKDB_RECIPES = ('nix/duckdb.nix', 'nix/sqlite.nix')
RECIPE_FILES = COMMON_RECIPES + LANCE_RECIPES + DUCKDB_RECIPES + ('nix/application.nix', 'nix/portable.nix')
EVIDENCE = {
    'lance': {'compiler.txt', 'cargo.txt', 'cargo.jsonl', 'patches.json'},
    'duckdb': {'compiler.txt', 'cmake-cache.txt', 'compile-commands.json', 'extensions.cmake', 'sqlite-source.json'},
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
    if component in ('lance', 'duckdb'):
        names = COMMON_RECIPES + (LANCE_RECIPES if component == 'lance' else DUCKDB_RECIPES)
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
    if component == 'lance':
        source = next(s for s in policy['sources'] if s['name'] == 'extensions/lance')
        return {'revision': source['sourceRevision'],
                'cargoLockSHA256': digest(repo / 'nix/lance-Cargo.lock'),
                'patchSHA256': digest(repo / 'nix/quick-xml-backport-lock.json')}
    sqlite = next(s for s in policy['sourceBuiltReplacements'] if s['name'] == 'sqlite')
    return {'engineRevision': policy['duckdbSourceRevision'],
            'sqlite': {key: sqlite[key] for key in ('wrapper', 'amalgamation')}}


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
    compiler = evidence_read(evidence / 'compiler.txt').decode()
    arch = 'aarch64' if platform == 'linux/arm64' else 'x86_64'
    if not re.search(r'^compiler-target: ' + arch + r'-[^\n]*linux[^\n]*$', compiler, re.M):
        raise ValueError('C/C++ compiler target differs from platform')
    commands = decode_json(evidence_read(evidence / 'compile-commands.json'))
    if not isinstance(commands, list) or not commands:
        raise ValueError('missing actual CMake compilation commands')
    selection = evidence_read(evidence / 'extensions.cmake').decode()
    sqlite_source = re.search(r'duckdb_extension_load\(sqlite_scanner SOURCE_DIR ([^\s()]+)\)', selection)
    if 'duckdb_extension_load(lance SOURCE_DIR ' not in selection or not sqlite_source:
        raise ValueError('source-built extension selection missing')
    if not any(c.get('file', '') == sqlite_source[1] + '/src/sqlite/sqlite3.c' for c in commands):
        raise ValueError('selected SQLite amalgamation absent from CMake compilation')
    sqlite = sources(repo, 'duckdb')['sqlite']['amalgamation']
    expected_sqlite = {name: sqlite[name + 'SHA256'].removeprefix('sha256:') for name in ('sqlite3.c', 'sqlite3.h')}
    if decode_json(evidence_read(evidence / 'sqlite-source.json')) != expected_sqlite:
        raise ValueError('compiled SQLite source differs from patched recipe')


def output_hashes(component, root):
    if component == 'lance':
        paths = [root / 'lib/liblance_duckdb_ffi.a']
    else:
        paths = sorted((root / 'lib').glob('*.a'))
        names = {p.name for p in paths}
        if not {'libduckdb_static.a', 'liblance_extension.a', 'libsqlite_scanner_extension.a'} <= names:
            raise ValueError('source-built DuckDB/Lance/SQLite archive output missing')
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
    if component == 'lance' and set(outputs) != {'lib/liblance_duckdb_ffi.a'}:
        raise ValueError('unexpected Lance outputs')
    if component == 'duckdb' and not {'lib/libduckdb_static.a', 'lib/liblance_extension.a', 'lib/libsqlite_scanner_extension.a'} <= outputs.keys():
        raise ValueError('missing source-built native outputs')
    if output_root is not None and outputs != output_hashes(component, output_root):
        raise ValueError('native output substitution')
    expected = component_value(component, platform, repo, directory / 'evidence', outputs)
    if value != expected or read(directory / 'receipt.json') != canonical(value):
        raise ValueError('component receipt/evidence/recipe mismatch')
    return value


def compose(repo, platform, revision, duckdb, lance, binaries, link_inputs, evidence, destination):
    if not re.fullmatch('[0-9a-f]{40}', revision):
        raise ValueError('application source revision required')
    components = {}
    expected_inputs = {}
    for name, root in (('duckdb', duckdb), ('lance', lance)):
        directory = root / 'share/leapview/native-build'
        value = verify_component(directory, name, platform, repo, root)
        components[name] = value
        for output, sha in value['outputs'].items():
            if Path(output).name != 'libdummy_static_extension_loader.a':
                expected_inputs[str(root / output)] = sha
    selected = read(link_inputs).decode().splitlines()
    if len(selected) != len(set(selected)) or set(selected) != set(expected_inputs):
        raise ValueError('application selected native link inputs differ from compiled receipts')
    if {p.name for p in evidence.iterdir()} != {'go.txt', 'link-flags.txt', 'tags.txt'}:
        raise ValueError('missing application compiler/link selection evidence')
    flags = read(evidence / 'link-flags.txt').decode()
    if shlex.split(flags) != ['-Wl,--start-group', *selected, '-Wl,--end-group', '-lstdc++', '-ldl', '-lm']:
        raise ValueError('application linker flags differ from selected archives')
    if read(evidence / 'tags.txt').decode().strip() != 'duckdb_arrow,duckdb_use_static_lib,leapview_static_lance,leapview_static_sqlite':
        raise ValueError('application static extension build tags differ')
    destination.mkdir()
    for name, root in (('duckdb', duckdb), ('lance', lance)):
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
    if set(value['components']) != {'duckdb', 'lance'} or set(value['linkInputs']) != {'duckdb', 'lance'}:
        raise ValueError('missing compiled component receipt')
    for name in ('duckdb', 'lance'):
        if value['components'][name] != digest(directory / name / 'receipt.json'):
            raise ValueError('component receipt substitution')
        component = verify_component(directory / name, name, platform, repo)
        inputs = {p: sha for p, sha in component['outputs'].items() if Path(p).name != 'libdummy_static_extension_loader.a'}
        if value['linkInputs'][name] != inputs:
            raise ValueError('application link input substitution')
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
    if evidence_read(directory / 'application-evidence/tags.txt').decode().strip() != 'duckdb_arrow,duckdb_use_static_lib,leapview_static_lance,leapview_static_sqlite':
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


def portable(directory, repo, platform, revision, original, exported, destination, interpreter, tool_version):
    verify_application(directory, repo, platform, revision, original)
    if not re.fullmatch(r'/lib(64)?/ld-linux-[A-Za-z0-9_-]+\.so\.[0-9]+', interpreter):
        raise ValueError('unexpected portable ELF interpreter')
    version = read(tool_version).decode().strip()
    if not version.startswith('patchelf '):
        raise ValueError('missing patchelf compiler identity')
    value = {'schemaVersion': 1, 'scope': SCOPE, 'platform': platform, 'revision': revision,
             'applicationReceiptSHA256': digest(directory / 'application.json'),
             'inputOutputs': load(directory / 'application.json')['outputs'],
             'transform': {'tool': version, 'arguments': ['--no-sort', '--set-interpreter', interpreter, '--remove-rpath']},
             'outputs': binary_hashes(exported, platform)}
    shutil.copytree(directory, destination)
    write(destination / 'portable.json', value)
    return verify_portable(destination, repo, platform, revision, exported)


def verify_portable(directory, repo, platform, revision, binaries):
    result = verify_application(directory, repo, platform, revision, None)
    value = load(directory / 'portable.json')
    if (set(value) != {'schemaVersion', 'scope', 'platform', 'revision', 'applicationReceiptSHA256', 'inputOutputs', 'transform', 'outputs'} or
            type(value['schemaVersion']) is not int or value['schemaVersion'] != 1 or value['scope'] != SCOPE or value['platform'] != platform or value['revision'] != revision or
            value['applicationReceiptSHA256'] != result['applicationReceiptSHA256'] or
            value['inputOutputs'] != load(directory / 'application.json')['outputs']):
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
    parser.add_argument('command', choices=['component', 'compose', 'verify', 'patches', 'portable', 'verify-portable', 'capture-cargo'])
    parser.add_argument('--repo', type=Path, required=True)
    parser.add_argument('--platform', choices=PLATFORMS, required=True)
    parser.add_argument('--component', choices=EVIDENCE)
    for flag in ('evidence', 'output-root', 'destination', 'duckdb', 'lance', 'binaries', 'link-inputs', 'input-receipt', 'tool-version'):
        parser.add_argument('--' + flag, type=Path)
    parser.add_argument('--revision')
    parser.add_argument('--interpreter')
    args = parser.parse_args()
    if args.command == 'capture-cargo':
        capture(sys.stdin.buffer, args.destination)
        return
    if args.command == 'patches':
        capture_patches(args.repo, args.output_root, args.destination)
        return
    if args.command == 'component':
        result = create_component(args.component, args.platform, args.repo, args.evidence, args.output_root, args.destination)
    elif args.command == 'compose':
        result = compose(args.repo, args.platform, args.revision, args.duckdb, args.lance, args.binaries, args.link_inputs, args.evidence, args.destination)
    elif args.command == 'portable':
        result = portable(args.input_receipt, args.repo, args.platform, args.revision, args.binaries, args.output_root, args.destination, args.interpreter, args.tool_version)
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
