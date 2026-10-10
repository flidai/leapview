"""Selected Delta Rust FFI composition; not exhaustive binary/security admission."""
import argparse
import hashlib
import json
from pathlib import Path
import re
import shlex
import tomllib

HEADERS = ('delta_kernel_ffi.h', 'delta_kernel_ffi.hpp', 'generated_delta_kernel_ffi.hpp', 'generated_inline_msvc_compat.inc')
ARCHIVES = {'lib/libdelta_kernel_ffi.a'}
FEATURES = {'arrow', 'arrow-58', 'default', 'default-engine-base', 'default-engine-rustls',
            'delta-kernel-unity-catalog', 'test-ffi', 'tracing', 'tracing-core', 'tracing-subscriber'}
TARGETS = {'linux/amd64': 'x86_64-unknown-linux-gnu', 'linux/arm64': 'aarch64-unknown-linux-gnu'}
RECIPES = ('nix/delta.nix', 'nix/delta-source-lock.json', 'nix/delta-Cargo.lock',
           'nix/delta-static-dependencies.patch', 'nix/check-extension-version.py',
           'scripts/nix_native_delta_receipt.py', 'nix/apply-quick-xml-backports.py',
           'nix/delta-quick-xml-backport-lock.json', 'nix/delta-quick-xml-smoke.rs',
           'nix/quick-xml/0.39.2-backport.patch', 'nix/quick-xml/duplicate-attributes-upstream.patch',
           'nix/quick-xml/namespace-bounds-upstream.patch')
EVIDENCE = {'compiler.txt', 'cargo.txt', 'cargo.jsonl', 'source.json', 'headers.json', 'checks.txt', 'patches.json'} | set(HEADERS)


def decode(data):
    def pairs(items):
        result = {}
        for key, value in items:
            if key in result:
                raise ValueError('duplicate Delta evidence JSON key')
            result[key] = value
        return result
    return json.loads(data, object_pairs_hook=pairs)


def compiled_cargo(data, lock, platform, *, package="delta_kernel_ffi", features=FEATURES, archive="libdelta_kernel_ffi.a"):
    locked = {(p['name'], p['version'], p.get('source', '')): p for p in lock['package']}
    artifacts, finished, root, tls = {}, False, False, False
    for line in data.splitlines():
        if not line.startswith('{'):
            continue
        item = decode(line)
        if item.get('reason') == 'build-finished':
            finished = item.get('success') is True
        if item.get('reason') != 'compiler-artifact':
            continue
        source, fragment = item['package_id'].rsplit('#', 1)
        name, version = fragment.rsplit('@', 1)
        if source.startswith('path+file:'):
            source = ''
        key = (name, version, source)
        if key not in locked or name in ('openssl-sys', 'native-tls'):
            raise ValueError('Rust FFI compiled package is unlocked or uses native TLS: ' + name)
        selected_features = sorted(item['features'])
        if name == package and 'staticlib' in item['target']['kind']:
            if set(selected_features) != set(features):
                raise ValueError('Rust FFI selected features differ')
            root |= (any(p.endswith('/' + TARGETS[platform] + '/release/' + archive) for p in item['filenames'])
                     and item['profile'].get('test') is False)
        tls |= name == 'rustls' and item['profile'].get('test') is False
        value = {'name': name, 'version': version, 'source': source, 'checksum': locked[key].get('checksum'),
                 'target': item['target']['name'], 'kinds': sorted(item['target']['kind']),
                 'features': selected_features, 'profile': item['profile']}
        artifacts[json.dumps(value, sort_keys=True)] = value
    if not finished or not root or not tls:
        raise ValueError('receipt requires successful selected Rustls FFI compilation')
    return [artifacts[key] for key in sorted(artifacts)]


def header_hashes(evidence, read):
    return {'include/' + name: hashlib.sha256(read(evidence / name)).hexdigest() for name in HEADERS}


def binding(data):
    value = decode(data)
    if (not isinstance(value, dict) or set(value) != {'archive', 'sha256', 'headers'}
            or not isinstance(value['archive'], str) or not value['archive'].endswith('/lib/libdelta_kernel_ffi.a')
            or not isinstance(value['sha256'], str) or not re.fullmatch('[0-9a-f]{64}', value['sha256'])
            or not isinstance(value['headers'], dict) or set(value['headers']) != {'include/' + name for name in HEADERS}
            or any(not isinstance(sha, str) or not re.fullmatch('[0-9a-f]{64}', sha) for sha in value['headers'].values())):
        raise ValueError('Delta archive/header binding differs')
    return value


def check(evidence, policy, platform, lock, read):
    compiler = read(evidence / 'compiler.txt').decode()
    if 'rustc 1.98.1' not in compiler or 'host: ' + TARGETS[platform] not in compiler:
        raise ValueError('Delta Rust compiler/platform differs')
    if not read(evidence / 'cargo.txt').decode().startswith('cargo 1.98.1'):
        raise ValueError('Delta Cargo compiler differs')
    if decode(read(evidence / 'source.json')) != policy['kernel']['selectedFiles']:
        raise ValueError('Delta selected source differs')
    expected_patches = policy['patchedFiles']
    if decode(read(evidence / 'patches.json')) != expected_patches:
        raise ValueError('Delta selected XML patch source differs')
    headers = header_hashes(evidence, read)
    if decode(read(evidence / 'headers.json')) != headers:
        raise ValueError('Delta generated headers differ')
    for name in HEADERS[:3]:
        if b'kernel_' not in read(evidence / name):
            raise ValueError('Delta generated FFI header missing')
    if read(evidence / 'checks.txt') != b'Delta selected FFI upstream unit tests passed\n':
        raise ValueError('Delta upstream FFI tests missing')
    return compiled_cargo(read(evidence / 'cargo.jsonl').decode(), lock, platform)


def check_engine(evidence, policy, cache, commands, selection, read):
    linked = binding(read(evidence / 'delta-link.json'))
    root = str(Path(linked['archive']).parent.parent)
    for macro, value in (('DELTA_KERNEL_LIBRARY', linked['archive']), ('DELTA_KERNEL_INCLUDE_DIR', root + '/include')):
        if not re.search(r'^' + macro + r':[^=]+=' + re.escape(value) + r'$', cache, re.M):
            raise ValueError('Delta CMake dependency selection differs')
    wrapper = re.search(r'duckdb_extension_load\(delta SOURCE_DIR ([^\s()]+) EXTENSION_VERSION ' + policy['wrapper']['revision'] + r'\)', selection)
    for name in ('src/delta_extension.cpp', 'src/functions/delta_scan/delta_scan.cpp'):
        if not wrapper or not any(c.get('file') == wrapper[1] + '/' + name and
                ('-I' + root + '/include') in shlex.split(c.get('command', '')) for c in commands):
            raise ValueError('Delta compiled wrapper/header selection missing')


def main():
    import nix_native_build_receipt as receipt
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=['source', 'headers', 'binding', 'patches'])
    for name in ('repo', 'output-root', 'destination'):
        parser.add_argument('--' + name, type=Path, required=True)
    args = parser.parse_args()
    if args.command == 'source':
        expected = receipt.load(args.repo / 'nix/delta-source-lock.json')['kernel']['selectedFiles']
        value = {name: receipt.digest(args.output_root / name) for name in expected}
        if value != expected:
            raise ValueError('Delta selected source identity differs')
    elif args.command == 'patches':
        expected = receipt.load(args.repo / 'nix/delta-source-lock.json')['patchedFiles']
        value = {name: receipt.digest(args.output_root / name) for name in expected}
        if value != expected:
            raise ValueError('Delta compiled XML patch source differs')
    elif args.command == 'headers':
        value = header_hashes(args.output_root, receipt.read)
    else:
        value = {'archive': str(args.output_root / 'lib/libdelta_kernel_ffi.a'),
                 'sha256': receipt.digest(args.output_root / 'lib/libdelta_kernel_ffi.a'),
                 'headers': header_hashes(args.output_root / 'include', receipt.read)}
        binding(json.dumps(value))
    receipt.write(args.destination, value)


if __name__ == '__main__':
    main()
