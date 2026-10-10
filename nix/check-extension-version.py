#!/usr/bin/env python3
"""Compile the selected wrapper and a separate macro-free extension loader."""
import argparse
import pathlib
import re
import subprocess
import tempfile


def check(source, header, compiler, name, macro, revision):
    if not re.fullmatch(r'[A-Za-z][A-Za-z0-9]*', name) or not re.fullmatch(r'EXT_VERSION_[A-Z_]+', macro) or not re.fullmatch(r'[0-9a-f]{40}', revision):
        raise ValueError('invalid selected extension identity')
    def read(path):
        data = path.read_bytes()
        if len(data) > 128 * 1024:
            raise ValueError('extension identity source exceeds bound')
        return data.decode()
    # Extract only the real Version declaration/body into a minimal class. The
    # pinned methods have no nested blocks; changed upstream code needs review.
    declarations = re.findall(r'std::string\s+Version\(\)\s+const(?:\s+override)?\s*(?:;|\{[^{}]*\})', read(header))
    definitions = re.findall(r'std::string\s+' + re.escape(name) + r'::Version\(\)\s+const\s*\{[^{}]*\}', read(source))
    if len(declarations) != 1 or len(definitions) > 1 or (';' == declarations[0][-1] and len(definitions) != 1):
        raise ValueError('selected wrapper must declare and define exactly one Version method')
    declaration = re.sub(r'\s+override\b', '', declarations[0])
    with tempfile.TemporaryDirectory(prefix='extension-version-') as directory:
        root = pathlib.Path(directory)
        (root / 'probe.hpp').write_text('#include <string>\nclass ' + name + ' { public: ' + declaration + ' };\n')
        (root / 'provider.cpp').write_text('#include "probe.hpp"\n' + '\n'.join(definitions))
        (root / 'loader.cpp').write_text('#include "probe.hpp"\n#include <iostream>\nint main() { std::cout << ' + name + '().Version(); }\n')
        # DuckDB scopes EXT_VERSION_* to the wrapper subdirectory. Its generated
        # loader includes the header without that macro: never test a single TU.
        for unit, flags in [('provider', ['-D' + macro + '="' + revision + '"']), ('loader', [])]:
            subprocess.run([compiler, '-std=c++11', '-O2', *flags, '-c', str(root / (unit + '.cpp')), '-o', str(root / (unit + '.o'))], check=True, timeout=60)
        subprocess.run([compiler, str(root / 'provider.o'), str(root / 'loader.o'), '-o', str(root / 'probe')], check=True, timeout=60)
        result = subprocess.run([str(root / 'probe')], check=True, capture_output=True, text=True, timeout=10)
        if result.stdout != revision:
            raise ValueError('compiled extension revision differs: ' + repr(result.stdout))
    print(name + ' loader reports selected source revision ' + revision)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('source', type=pathlib.Path)
    parser.add_argument('header', type=pathlib.Path)
    parser.add_argument('compiler')
    parser.add_argument('name')
    parser.add_argument('macro')
    parser.add_argument('revision')
    args = parser.parse_args()
    check(args.source, args.header, args.compiler, args.name, args.macro, args.revision)
