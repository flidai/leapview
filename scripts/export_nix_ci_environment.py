#!/usr/bin/env python3
"""Carry the locked compiler/browser environment into later Actions steps.

Only toolchain variables are copied. The inherited runner environment may contain
credentials, so exporting all of `env` or `nix print-dev-env` is inappropriate.
"""

import os
from pathlib import Path

VARIABLES = (
    'GOTOOLCHAIN', 'CC', 'CXX', 'AR', 'LD', 'STRIP',
    'NIX_CC', 'NIX_BINTOOLS', 'NIX_CFLAGS_COMPILE', 'NIX_LDFLAGS',
    'NIX_ENFORCE_PURITY', 'NIX_HARDENING_ENABLE',
    'PKG_CONFIG_PATH', 'PKG_CONFIG_FOR_BUILD', 'LD_LIBRARY_PATH',
    'FONTCONFIG_FILE', 'PLAYWRIGHT_BROWSERS_PATH',
    'LEAPVIEW_TEST_NIX_PLAYWRIGHT_VERSION', 'BUN_FEATURE_FLAG_NO_ORPHANS',
)


def single_line(value):
    if any(character in value for character in ('\r', '\n', '\x00')):
        raise ValueError('toolchain values must be single lines')
    return value


def render(environment):
    for name in ('PATH', 'GOTOOLCHAIN', 'FONTCONFIG_FILE', 'PLAYWRIGHT_BROWSERS_PATH',
                 'LEAPVIEW_TEST_NIX_PLAYWRIGHT_VERSION'):
        if not environment.get(name):
            raise ValueError(f'missing locked toolchain variable: {name}')
    if environment['GOTOOLCHAIN'] != 'local':
        raise ValueError('the locked compiler must disable automatic Go downloads')
    path = single_line(environment['PATH'])
    if not path.startswith('/nix/store/'):
        raise ValueError('the locked toolchain must precede runner tools in PATH')
    for name in ('FONTCONFIG_FILE', 'PLAYWRIGHT_BROWSERS_PATH'):
        if not environment[name].startswith('/nix/store/'):
            raise ValueError(f'{name} must come from the Nix store')
    lines = [f'{name}={single_line(environment[name])}\n'
             for name in VARIABLES if name in environment]
    lines.append('PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1\n')
    return ''.join(lines), path + '\n'


def export(environment, env_file, path_file):
    env, path = render(environment)
    with Path(env_file).open('a') as output:
        output.write(env)
    with Path(path_file).open('a') as output:
        output.write(path)


if __name__ == '__main__':
    export(os.environ, os.environ['GITHUB_ENV'], os.environ['GITHUB_PATH'])
