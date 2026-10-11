"""Opt-in real APT regression; no package installation occurs on the test host.

Pull UBUNTU_IMAGE, then set LEAPVIEW_TEST_DESKTOP_APT=1 for unittest discovery.
The tiny packages exercise acquisition/version changes, not Electron admission.
"""

import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import nix_desktop_lifecycle as lifecycle

UBUNTU_IMAGE = 'ubuntu:22.04@sha256:5ec03bb3441e8b0bf3b4f9cd4629a1ae763010dc3035bb8da3ae6cf026486401'


@unittest.skipUnless(os.environ.get('LEAPVIEW_TEST_DESKTOP_APT') == '1',
                     'requires explicitly enabled Docker Ubuntu 22.04 APT regression')
class LifecycleAPTTests(unittest.TestCase):
    def test_exact_local_archives_install_upgrade_and_rollback_without_network(self):
        temporary = tempfile.TemporaryDirectory(prefix='desktop-lifecycle-apt-')
        self.addCleanup(temporary.cleanup)
        work = Path(temporary.name)
        container = subprocess.run([
            'docker', 'run', '--rm', '--pull=never', '--network=none', '--detach',
            '--mount', f'type=bind,source={work},target={work}', UBUNTU_IMAGE,
            'sleep', '120'], check=True, capture_output=True, text=True, timeout=30).stdout.strip()
        self.addCleanup(subprocess.run, ['docker', 'rm', '--force', container],
                        check=False, capture_output=True, timeout=30)
        self.addCleanup(subprocess.run, ['docker', 'exec', container, 'chown', '-R',
                        f'{os.getuid()}:{os.getgid()}', str(work)],
                        check=False, capture_output=True, timeout=30)
        packages = {role: {
            'archive': str(work / role / 'leapview-desktop-linux-x64.deb'),
            'staged': str(work / role / 'staged'), 'version': version,
        } for role, version in [('predecessor', '0.1.0'), ('candidate', '0.1.1')]}
        runtime = lifecycle.Runtime({'work': str(work), 'packages': packages})
        script = [
            'set -euo pipefail',
            'umask 022',
            'test "$(ls /sys/class/net)" = lo',
        ]
        for role, value in packages.items():
            root = work / role / 'package'
            script += [
                f'mkdir -p {root}/DEBIAN {root}/usr/share/leapview-desktop-regression',
                f'cat > {root}/DEBIAN/control <<EOF',
                'Package: leapview-desktop',
                f'Version: {value["version"]}',
                'Architecture: amd64',
                'Maintainer: Qualification <qualification@example.invalid>',
                'Description: Disposable APT lifecycle regression',
                'EOF',
                f'echo {value["version"]} > {root}/usr/share/leapview-desktop-regression/version',
                f'dpkg-deb --build {root} {value["archive"]}',
                f'apt-get install --download-only --yes {value["archive"]}',
            ]
        built = subprocess.run(['docker', 'exec', '--interactive', container, 'bash'],
                               input='\n'.join(script) + '\n', text=True,
                               stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=60, check=False)
        self.assertEqual(built.returncode, 0, built.stdout)
        for value in packages.values():
            value['sha256'] = lifecycle.desktop.digest_file(value['archive'])

        # Route real APT and dpkg-query into the disposable container, retaining
        # real digest/command/version checks. Stub only Electron-specific checks.
        execute, query = lifecycle.desktop.run_command, lifecycle.desktop.command_output
        def run(arguments):
            self.assertEqual(arguments[0], 'sudo')
            execute(['docker', 'exec', container, *arguments[1:]])
        def output(arguments):
            return query(['docker', 'exec', container, *arguments])
        with patch.object(lifecycle.desktop, 'validate_package_paths'), \
             patch.object(lifecycle.desktop, 'reject_maintainer_scripts'), \
             patch.object(lifecycle.desktop, 'installed_executable'), \
             patch.object(lifecycle.desktop, 'command_output', side_effect=output), \
             patch.object(lifecycle.desktop, 'run_command', side_effect=run):
            for role in ('predecessor', 'candidate', 'predecessor'):
                runtime.install(role)
                self.assertEqual(output(['cat', '/usr/share/leapview-desktop-regression/version']),
                                 packages[role]['version'])
                self.assertEqual(lifecycle.desktop.digest_file(packages[role]['archive']),
                                 packages[role]['sha256'])
        self.assertEqual([item['version'] for item in runtime.results], ['0.1.0', '0.1.1', '0.1.0'])


if __name__ == '__main__':
    unittest.main()
