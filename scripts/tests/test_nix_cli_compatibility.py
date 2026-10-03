import importlib.util
import struct
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('nix_cli_compatibility', ROOT / 'scripts/check_nix_cli_compatibility.py')
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


def elf(machine=62, segments=(1,)):
    identification = b'\x7fELF' + bytes([2, 1, 1]) + bytes(9)
    header = struct.pack('<HHIQQQIHHHHHH', 2, machine, 1, 0, 64, 0, 0, 64, 56, len(segments), 0, 0, 0)
    return identification + header + b''.join(struct.pack('<IIQQQQQQ', kind, 5, 0, 0, 0, 0, 0, 1) for kind in segments)


class CompatibilityTests(unittest.TestCase):
    def test_accepts_both_supported_static_architectures(self):
        m.check_elf(elf(), 'amd64')
        m.check_elf(elf(183), 'arm64')

    def test_rejects_loader_and_dynamic_segments(self):
        for segment in (2, 3):
            with self.subTest(segment=segment), self.assertRaisesRegex(ValueError, 'dynamic'):
                m.check_elf(elf(segments=(1, segment)), 'amd64')

    def test_rejects_store_data_dependencies_even_without_cgo(self):
        with self.assertRaisesRegex(ValueError, 'Nix store'):
            m.check_elf(elf() + b'/nix/store/0123456789abcdefghijklmnopqrstuvwxyz-tzdata/share/zoneinfo', 'amd64')

    def test_rejects_wrong_architecture_and_malformed_headers(self):
        for data in (elf(183), b'not ELF', elf()[:63], elf()[:-1], elf(segments=()),
                     elf()[:4] + b'\x01' + elf()[5:]):
            with self.subTest(data=data[:20]), self.assertRaises(ValueError):
                m.check_elf(data, 'amd64')

    def build_info(self):
        return {'GoVersion': 'go1.26.8', 'Path': 'github.com/flidai/leapview/cmd/leapviewctl',
                'Settings': [{'Key': key, 'Value': value} for key, value in
                             [('GOOS', 'linux'), ('GOARCH', 'amd64'), ('CGO_ENABLED', '0'), ('GOAMD64', 'v1')]]}

    def test_preserves_each_architectures_release_cpu_baseline(self):
        for arch, key, value in [('amd64', 'GOAMD64', 'v1'), ('arm64', 'GOARM64', 'v8.0')]:
            info = self.build_info()
            info['Settings'][1]['Value'] = arch
            info['Settings'][-1] = {'Key': key, 'Value': value}
            m.check_build_info(info, arch)
            info['Settings'][-1]['Value'] = 'v3' if arch == 'amd64' else 'v9.0'
            with self.subTest(arch=arch), self.assertRaisesRegex(ValueError, 'CPU'):
                m.check_build_info(info, arch)

    def test_rejects_cgo_foreign_platform_and_wrong_program(self):
        m.check_build_info(self.build_info(), 'amd64')
        for key, value in [('CGO_ENABLED', '1'), ('GOARCH', 'arm64'), ('GOOS', 'darwin')]:
            info = self.build_info()
            next(setting for setting in info['Settings'] if setting['Key'] == key)['Value'] = value
            with self.subTest(key=key), self.assertRaises(ValueError):
                m.check_build_info(info, 'amd64')
        info = self.build_info()
        info['Path'] = 'example.org/other'
        with self.assertRaises(ValueError):
            m.check_build_info(info, 'amd64')

    def test_rejects_missing_or_duplicate_build_settings(self):
        for settings in ([], self.build_info()['Settings'] * 2):
            info = self.build_info()
            info['Settings'] = settings
            with self.assertRaises(ValueError):
                m.check_build_info(info, 'amd64')


if __name__ == '__main__':
    unittest.main()
