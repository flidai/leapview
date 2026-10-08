#!/usr/bin/env python3
"""Qualify a versioned Desktop upgrade and retained offline rollback on a disposable runner."""

import argparse
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile

import nix_candidate_manifest as candidate
import nix_desktop_lifecycle_inputs as inputs
import nix_desktop_qualification as desktop

PROFILE = {'schemaVersion': 2, 'profiles': [{
    'id': 'profile_' + '1' * 32, 'canonicalOrigin': 'https://qualification.invalid',
    'instanceId': 'instance_' + '2' * 32, 'displayName': 'Disposable offline fixture',
    'lastSafePath': '/dashboards/sales', 'partitionVersion': 1, 'label': 'seed'}]}


def version_less(old, new):
    if any(not isinstance(value, str) or not re.fullmatch(r'[0-9][A-Za-z0-9.+:~\-]*', value) for value in (old, new)):
        raise ValueError('Desktop Debian version is invalid')
    result = subprocess.run(['dpkg', '--compare-versions', old, 'lt', new], check=False,
                            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=10)
    if result.returncode not in (0, 1):
        raise ValueError('Desktop Debian version comparison failed')
    return result.returncode == 0


def validate_pair(old, new):
    before, after = old['binding']['archive'], new['binding']['archive']
    if (old['binding']['releaseAdmission'] is not False or new['binding']['releaseAdmission'] is not False
            or before['platform'] != 'linux/amd64' or after['platform'] != 'linux/amd64'
            or before['sha256'] == after['sha256']
            or old['producer']['artifacts'][0]['runId'] == new['producer']['artifacts'][0]['runId']):
        raise ValueError('Desktop lifecycle requires two independently authenticated exact candidate archives')
    if not version_less(before['version'], after['version']):
        raise ValueError('Desktop upgrade requires a strictly newer Debian version; reinstall is not upgrade evidence')


def check_probe(result, writes, termination):
    expected = {'schemaVersion': 1, 'trustedShellReadback': True, 'acknowledgedWrite': writes,
                'durableReadback': True, 'termination': termination, 'mainProcessExited': True, 'processGroupStopped': True}
    if candidate.canonical_bytes(result) != candidate.canonical_bytes(expected):
        raise ValueError('Desktop process probe did not prove the required write/readback/termination')


def exercise(runtime):
    runtime.install('predecessor')
    runtime.probe('seed', 'before-upgrade', 'graceful')
    runtime.install('candidate')
    runtime.probe('before-upgrade', 'after-upgrade', 'crash')
    runtime.probe('after-upgrade', '-', 'graceful')
    runtime.install('predecessor')
    runtime.probe('after-upgrade', 'after-rollback', 'graceful')
    runtime.probe('after-rollback', '-', 'graceful')


class Runtime:
    def __init__(self, state):
        self.state = state
        self.results = []

    def install(self, role):
        value = self.state['packages'][role]
        archive = Path(value['archive'])
        if desktop.digest_file(archive) != value['sha256']:
            raise ValueError('retained Desktop archive bytes changed')
        desktop.validate_package_paths(archive)
        desktop.reject_maintainer_scripts(archive, Path(self.state['work']))
        desktop.run_command(['sudo', 'apt-get', 'install', '--no-download', '--yes', '--allow-downgrades', str(archive)])
        version = desktop.command_output(['dpkg-query', '-W', '-f=${Version}', desktop.PACKAGE_NAME])
        if version != value['version']:
            raise ValueError('installed package version differs from selected exact archive')
        desktop.installed_executable(value['staged'], desktop.APP_EXECUTABLE)
        self.results.append({'phase': 'install', 'role': role, 'version': version,
                             'archiveSHA256': value['sha256'], 'payloadMatched': True})

    def probe(self, expected, update, termination):
        result = Path(self.state['work']) / f'probe-{len(self.results)}.json'
        desktop.run_command(['xvfb-run', '--auto-servernum', self.state['node'], self.state['probe'],
            str(Path('/') / desktop.APP_EXECUTABLE), self.state['profile'], expected, update, termination, str(result)])
        receipt = candidate.read_json_file(result)
        check_probe(receipt, update != '-', termination)
        self.results.append({'phase': 'profile', **receipt})


def execute_offline(state_path):
    # The caller created a new network namespace; never touch the host interface.
    if os.getuid() == 0 or os.environ.get('NIX_DESKTOP_LIFECYCLE_OFFLINE') != '1':
        raise ValueError('offline executor requires the isolated nonroot launcher')
    state = candidate.read_json_file(state_path)
    links = candidate.read_json(desktop.command_output(['ip', '-j', 'link']).encode())
    if len(links) != 1 or links[0].get('ifname') != 'lo':
        raise ValueError('offline executor requires a fresh loopback-only namespace')
    desktop.run_command(['sudo', 'ip', 'link', 'set', 'lo', 'up'])
    desktop.refuse_preexisting_installation()
    runtime = Runtime(state)
    try:
        exercise(runtime)
    finally:
        desktop.remove_candidate_installation(state['work'])
    for role, value in state['packages'].items():
        if desktop.digest_file(value['archive']) != value['sha256']:
            raise ValueError('retained rollback archive changed during lifecycle')
    desktop.write_json(Path(state['work']) / 'offline-result.json', {
        'networkIsolated': True, 'packageRemoved': True, 'phases': runtime.results})


def qualify(args):
    if (os.environ.get('GITHUB_ACTIONS') != 'true' or os.environ.get('GITHUB_REPOSITORY') != inputs.REPOSITORY
            or os.environ.get('GITHUB_REF') != 'refs/heads/main' or os.getuid() == 0):
        raise ValueError('Desktop lifecycle runs only on a nonroot disposable protected GitHub runner')
    host = desktop.require_host()
    desktop.refuse_preexisting_installation()
    args.output.mkdir(mode=0o700)
    verified = {}
    for role in ('predecessor', 'candidate'):
        verified[role] = inputs.verify(args.inputs / role, getattr(args, role + '_source'), getattr(args, role + '_signer'), getattr(args, role + '_run'), getattr(args, role + '_attempt'))
    validate_pair(verified['predecessor'], verified['candidate'])
    verifier = candidate.checkout_source(Path(__file__).resolve().parents[1])
    # Download dependencies before isolation. No candidate code runs here.
    with tempfile.TemporaryDirectory(prefix='leapview-desktop-lifecycle-') as temporary:
        work = Path(temporary)
        state = {'work': str(work), 'profile': str(work / 'profile'), 'packages': {},
                 'probe': str(Path(__file__).resolve().parents[1] / 'desktop/scripts/qualify-lifecycle-linux.mjs'),
                 'node': shutil.which('node')}
        if not state['node']:
            raise ValueError('locked Node runtime unavailable')
        (work / 'profile').mkdir(mode=0o700)
        desktop.write_json(work / 'profile/profiles.json', PROFILE)
        (work / 'home').mkdir(mode=0o700)
        try:
            for role in ('predecessor', 'candidate'):
                archive = args.inputs / role / 'candidate' / inputs.ARCHIVE
                copied, staged, relative = desktop.copy_exact_payload(archive, work / role)
                identity = verified[role]['binding']['archive']
                if relative != desktop.APP_EXECUTABLE or desktop.digest_file(copied) != identity['sha256']:
                    raise ValueError('Desktop staged payload differs from authenticated artifact')
                state['packages'][role] = {'archive': str(copied), 'staged': str(staged),
                                          'version': identity['version'], 'sha256': identity['sha256']}
                desktop.run_command(['sudo', 'apt-get', 'install', '--download-only', '--yes', str(copied)])
            desktop.write_json(work / 'state.json', state)
            # The candidate receives no GitHub credentials or ambient application
            # configuration. It cannot reach registry, identity, provider or internet.
            command = ['sudo', 'unshare', '--net', '--fork', '--kill-child=SIGKILL',
                'sudo', '-u', f'#{os.getuid()}', 'env', '-i', f'PATH={os.environ["PATH"]}',
                f'HOME={work / "home"}', f'USER={os.environ.get("USER", "runner")}',
                'NIX_DESKTOP_LIFECYCLE_OFFLINE=1', 'PYTHONDONTWRITEBYTECODE=1',
                sys.executable, str(Path(__file__).resolve()), 'exercise', '--state', str(work / 'state.json')]
            desktop.run_command(command)
            result = candidate.read_json_file(work / 'offline-result.json')
            if result.get('networkIsolated') is not True or result.get('packageRemoved') is not True:
                raise ValueError('Desktop offline lifecycle or package cleanup incomplete')
        finally:
            desktop.remove_candidate_installation(work)
            # Exact extracted root-owned Electron payloads are isolated beneath
            # this newly created work directory; remove only those owned paths.
            for role in ('predecessor', 'candidate'):
                out = work / role / 'desktop/out'
                if out.exists():
                    desktop.run_command(['sudo', 'rm', '-rf', '--', str(out)])
        for role in ('predecessor', 'candidate'):
            if desktop.digest_file(args.inputs / role / 'candidate' / inputs.ARCHIVE) != verified[role]['binding']['archive']['sha256']:
                raise ValueError('authenticated input changed during lifecycle')
    # Publish only after process, package and private-profile cleanup succeeds.
    report = {'schemaVersion': 1, 'result': 'success', 'host': host, 'inputs': verified,
        'verifierRevision': verifier['revision'], 'verifierFiles': [
            {'path': path, 'sha256': desktop.digest_file(Path(__file__).resolve().parents[1] / path)}
            for path in ('scripts/nix_desktop_lifecycle.py', 'scripts/nix_desktop_lifecycle_inputs.py',
                         'desktop/scripts/qualify-lifecycle-linux.mjs', 'desktop/scripts/lifecycle-probe-policy.mjs')],
        'lifecycle': result, 'privateProfileRemoved': not work.exists(), 'releaseAdmission': False,
        'scope': 'linux-amd64-preview-saved-profile-version-upgrade-crash-restart-offline-rollback',
        'pending': ['remote-authenticated-session', 'server-data-recovery', 'os-code-signing',
                    'profile-observation', 'production-promotion']}
    desktop.write_json(args.output / 'desktop-lifecycle.json', report)
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest='operation', required=True)
    offline = commands.add_parser('exercise')
    offline.add_argument('--state', type=Path, required=True)
    run = commands.add_parser('qualify')
    run.add_argument('--inputs', type=Path, required=True)
    run.add_argument('--output', type=Path, required=True)
    for role in ('predecessor', 'candidate'):
        run.add_argument('--' + role + '-run', type=int, required=True)
        run.add_argument('--' + role + '-attempt', type=int, required=True)
        run.add_argument('--' + role + '-source', type=Path, required=True)
        run.add_argument('--' + role + '-signer', type=Path, required=True)
    args = parser.parse_args()
    os.umask(0o077)
    if args.operation == 'exercise':
        execute_offline(args.state)
    else:
        qualify(args)


if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError, TypeError, KeyError, subprocess.SubprocessError):
        raise SystemExit('Desktop lifecycle qualification failed; no successful lifecycle receipt issued') from None
