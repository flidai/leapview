"""Selected Vortex Rust/C++ bridge composition, not exhaustive native admission."""
import base64
import hashlib
import json
from pathlib import Path
import re
import shlex
import nix_native_delta_receipt as rust

ARCHIVES = {'lib/libvortex_duckdb.a'}
LIMIT = 64 * 1024 * 1024
EVIDENCE = {'compiler.txt', 'cargo.txt', 'cargo.jsonl', 'source.json', 'patches.json',
            'engine-source.json', 'engine-path.txt', 'headers.json', 'cpp.rs', 'cpp-build.txt',
            'checks.txt', 'cxx.txt', 'clang.txt'}
RECIPES = ('nix/vortex.nix', 'nix/vortex-source-lock.json', 'nix/vortex-Cargo.lock',
           'nix/vortex-static-dependencies.patch', 'nix/vortex-selected-engine.patch',
           'nix/check-extension-version.py', 'nix/vortex-quick-xml-backport-lock.json',
           'nix/apply-quick-xml-backports.py', 'nix/delta-quick-xml-smoke.rs',
           'nix/quick-xml/0.39.2-backport.patch', 'nix/quick-xml/duplicate-attributes-upstream.patch',
           'nix/quick-xml/namespace-bounds-upstream.patch', 'scripts/nix_native_delta_receipt.py',
           'scripts/nix_native_vortex_receipt.py')


def compiled(data, lock, platform):
    return rust.compiled_cargo(data, lock, platform, package='vortex-duckdb', features=(), archive='libvortex_duckdb.a')


def headers(data, policy):
    encoded = rust.decode(data)
    if not isinstance(encoded, dict) or set(encoded) != set(policy['headers']):
        raise ValueError('Vortex generated/public header set differs')
    hashes, total = {}, 0
    for name, text in encoded.items():
        if not isinstance(text, str) or not re.fullmatch(r'include/[A-Za-z0-9_./-]+\.h', name) or '..' in Path(name).parts:
            raise ValueError('Vortex header path/encoding differs')
        decoded = base64.b64decode(text, validate=True)
        total += len(decoded)
        if total > LIMIT or base64.b64encode(decoded).decode() != text:
            raise ValueError('Vortex bounded canonical header encoding differs')
        hashes[name] = hashlib.sha256(decoded).hexdigest()
    return hashes


def bridge(data, policy, engine):
    if '-march=native' in data or '-mcpu=native' in data or 'target-cpu=native' in data:
        raise ValueError('Vortex bridge uses host-specific instructions')
    commands = [shlex.split(line.removeprefix('running: ')) for line in data.splitlines() if line.startswith('running: ')]
    for name in policy['bridgeSources']:
        if not any(name in command and '-c' in command and '-std=c++20' in command and '-fPIC' in command and
                   any(command[i:i + 2] == ['-isystem', engine + '/src/include'] for i in range(len(command))) for command in commands):
            raise ValueError('Vortex selected C++ bridge/engine compilation missing: ' + name)


def check(evidence, policy, platform, lock, read):
    if rust.decode(read(evidence / 'source.json')) != policy['rust']['selectedFiles']:
        raise ValueError('Vortex selected Rust/bridge source differs')
    if rust.decode(read(evidence / 'engine-source.json')) != policy['engine']['selectedFiles']:
        raise ValueError('Vortex selected engine source differs')
    if rust.decode(read(evidence / 'patches.json')) != policy['patchedFiles']:
        raise ValueError('Vortex selected XML patch source differs')
    compiler = read(evidence / 'compiler.txt').decode()
    if 'rustc 1.98.1' not in compiler or 'host: ' + rust.TARGETS[platform] not in compiler or not read(evidence / 'cargo.txt').decode().startswith('cargo 1.98.1'):
        raise ValueError('Vortex Rust compiler/platform differs')
    arch = 'aarch64' if platform == 'linux/arm64' else 'x86_64'
    if not re.search(r'^compiler-target: ' + arch + r'-[^\n]*linux[^\n]*$', read(evidence / 'cxx.txt').decode(), re.M):
        raise ValueError('Vortex C++ compiler/platform differs')
    if 'clang version' not in read(evidence / 'clang.txt').decode():
        raise ValueError('Vortex bindgen compiler identity missing')
    engine = read(evidence / 'engine-path.txt').decode().strip()
    if not re.fullmatch(r'/nix/store/[a-z0-9]{32}-[^\s/]+', engine):
        raise ValueError('Vortex selected engine source path differs')
    bridge(read(evidence / 'cpp-build.txt').decode(), policy, engine)
    headers(read(evidence / 'headers.json'), policy)
    if b'duckdb_' not in read(evidence / 'cpp.rs'):
        raise ValueError('Vortex actual generated Rust bindings missing')
    if read(evidence / 'checks.txt') != b'Vortex file upstream unit tests and selected XML boundary passed\n':
        raise ValueError('Vortex selected tests missing')
    return compiled(read(evidence / 'cargo.jsonl').decode(), lock, platform)


def binding(data, policy):
    value = rust.decode(data)
    if (not isinstance(value, dict) or set(value) != {'archive', 'sha256', 'headers', 'engineHeaders'}
            or not isinstance(value['archive'], str) or not value['archive'].endswith('/lib/libvortex_duckdb.a')
            or not isinstance(value['sha256'], str) or not re.fullmatch('[0-9a-f]{64}', value['sha256'])
            or not isinstance(value['headers'], dict) or set(value['headers']) != set(policy['headers'])
            or any(not isinstance(sha, str) or not re.fullmatch('[0-9a-f]{64}', sha) for sha in value['headers'].values())
            or value['engineHeaders'] != policy['engine']['selectedFiles']):
        raise ValueError('Vortex archive/generated/engine header binding differs')
    return value


def check_engine(evidence, policy, cache, commands, selection, read):
    linked = binding(read(evidence / 'vortex-link.json'), policy)
    root = str(Path(linked['archive']).parent.parent)
    for macro, value in (('VORTEX_FFI_LIBRARY', linked['archive']), ('VORTEX_FFI_INCLUDE_DIR', root + '/include')):
        if not re.search(r'^' + macro + r':[^=]+=' + re.escape(value) + r'$', cache, re.M):
            raise ValueError('Vortex CMake dependency selection differs')
    wrapper = re.search(r'duckdb_extension_load\(vortex SOURCE_DIR ([^\s()]+) EXTENSION_VERSION ' + policy['wrapper']['revision'] + r'\)', selection)
    if not wrapper or not any(c.get('file') == wrapper[1] + '/src/vortex_extension.cpp' and
            ('-I' + root + '/include') in shlex.split(c.get('command', '')) for c in commands):
        raise ValueError('Vortex compiled wrapper/header selection missing')
    if rust.decode(read(evidence / 'vortex-engine-source.json')) != policy['engine']['selectedFiles']:
        raise ValueError('Vortex FFI and final engine source headers differ')


def main():
    import argparse
    import nix_native_build_receipt as receipt
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=['source', 'engine-source', 'patches', 'headers', 'binding'])
    for name in ('repo', 'output-root', 'destination'):
        parser.add_argument('--' + name, type=Path, required=True)
    args = parser.parse_args()
    policy = receipt.load(args.repo / 'nix/vortex-source-lock.json')
    if args.command in ('source', 'engine-source', 'patches'):
        expected = policy['patchedFiles'] if args.command == 'patches' else policy['engine' if args.command == 'engine-source' else 'rust']['selectedFiles']
        value = {name: receipt.digest(args.output_root / name) for name in expected}
        if value != expected:
            raise ValueError('Vortex selected source identity differs: ' + args.command)
    elif args.command == 'headers':
        value = {name: base64.b64encode(receipt.read(args.output_root / name)).decode() for name in policy['headers']}
        headers(json.dumps(value), policy)
    else:
        retained = args.output_root / 'share/leapview/native-build/evidence'
        value = {'archive': str(args.output_root / 'lib/libvortex_duckdb.a'),
                 'sha256': receipt.digest(args.output_root / 'lib/libvortex_duckdb.a'),
                 'headers': {name: receipt.digest(args.output_root / name) for name in policy['headers']},
                 'engineHeaders': rust.decode(receipt.evidence_read(retained / 'engine-source.json'))}
        binding(json.dumps(value), policy)
    receipt.write(args.destination, value)


if __name__ == '__main__':
    main()
