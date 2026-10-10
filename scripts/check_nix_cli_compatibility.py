#!/usr/bin/env python3
"""Check an exact controller binary's static Linux contract; never admit a release."""
import argparse
import hashlib
import json
import re
import struct
import subprocess
from pathlib import Path


def check_elf(data, arch):
    if len(data) < 64 or data[:7] != b'\x7fELF\x02\x01\x01':
        raise ValueError('controller must be a little-endian ELF64 executable')
    # A namespace prefix used to validate operator input is not a runtime
    # dependency. Reject embedded store objects, whose names begin with the
    # 32-character store hash, including data paths that need no ELF loader.
    if re.search(rb'/nix/store/[0-9a-z]{32}-', data):
        raise ValueError('controller contains a Nix store runtime path')
    header = struct.unpack_from('<HHIQQQIHHHHHH', data, 16)
    kind, machine, version = header[:3]
    offset, entry_size, count = header[4], header[8], header[9]
    if kind != 2 or machine != {'amd64': 62, 'arm64': 183}[arch] or version != 1:
        raise ValueError('controller ELF identity does not match the Linux architecture')
    if entry_size != 56 or not 0 < count < 65535 or offset < 64 or offset + count * entry_size > len(data):
        raise ValueError('invalid controller program header table')
    segments = [struct.unpack_from('<I', data, offset + index * entry_size)[0] for index in range(count)]
    if 1 not in segments or 2 in segments or 3 in segments:
        raise ValueError('controller requires a dynamic loader or runtime libraries')


def check_build_info(info, arch):
    settings = {}
    for setting in info.get('Settings', []):
        key = setting['Key']
        if key in settings:
            raise ValueError('duplicate Go build setting')
        settings[key] = setting['Value']
    if info.get('Path') != 'github.com/flidai/leapview/cmd/leapviewctl' or any(
            settings.get(key) != value for key, value in
            [('GOOS', 'linux'), ('GOARCH', arch), ('CGO_ENABLED', '0')]):
        raise ValueError('controller must be built for the declared Linux architecture with CGO disabled')
    cpu_setting, baseline = ('GOAMD64', 'v1') if arch == 'amd64' else ('GOARM64', 'v8.0')
    if settings.get(cpu_setting) != baseline:
        raise ValueError('controller requires a newer CPU than the release baseline')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('binary', type=Path)
    parser.add_argument('--arch', choices=['amd64', 'arm64'], required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    if args.binary.stat().st_size > 128 * 1024 * 1024:
        parser.error('controller exceeds 128 MiB')
    data = args.binary.read_bytes()
    try:
        check_elf(data, args.arch)
        info = json.loads(subprocess.check_output(['go', 'version', '-m', '-json', str(args.binary)], text=True))
        check_build_info(info, args.arch)
    except (ValueError, KeyError, subprocess.CalledProcessError) as error:
        parser.error(str(error))
    report = {'schemaVersion': 1, 'platform': 'linux/' + args.arch,
              'binarySHA256': 'sha256:' + hashlib.sha256(data).hexdigest(),
              'goVersion': info['GoVersion'], 'static': True, 'cgoEnabled': False,
              'releaseAdmission': False}
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, indent=2) + '\n')


if __name__ == '__main__':
    main()
