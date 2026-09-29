#!/usr/bin/env python3
"""Offline rollback from a controller with the host filesystem hidden.

Test-only fixed topology: the SSH daemon remains in its original mount namespace.
The new controller has only transport credentials; it retrieves application
records over SSH and cannot read the host's records or Docker socket locally.
"""
import argparse
import json
import os
from pathlib import Path
import shlex
import shutil

from lifecycle import HERE, Trial, run


def controller(args):
    run(['mount', '--make-rprivate', '/'])
    run(['mount', '-t', 'tmpfs', 'tmpfs', str(args.host)])
    assert not (args.host / 'docker.sock').exists()
    assert not (args.host / 'records').exists()
    ssh = ['ssh', '-F', str(args.runner / 'ssh_config'), '127.0.0.1']
    _, raw = run(ssh + ['cat ' + shlex.quote(str(args.host / 'records/prior.json'))])
    record = json.loads(raw)
    assert record['version'] == 'prior' and record['status'] == 'verified'
    # Replace only the fixed fixture's transport/hook paths, never the recorded
    # application command or environment. Production needs a typed equivalent.
    config = record['runtime_config']
    for name in ('client_key', 'ssh_config', 'hooks'):
        config = config.replace(str(args.host / name), str(args.runner / name))
    (args.runner / 'deploy.yml').write_text(config)
    (args.runner / 'hooks').mkdir()
    hook = args.runner / 'hooks/pre-app-boot'
    remote_guard = shlex.join(['python3', str(HERE / 'identity_guard.py'),
                              '--record', str(args.host / 'records/prior.json'),
                              '--version', 'prior', '--rollback'])
    hook.write_text('#!/bin/sh\nset -eu\nexec ' + shlex.join(ssh + [remote_guard]) + '\n')
    hook.chmod(0o700)
    env = dict(os.environ, HOME=str(args.runner), GEM_HOME=str(args.gems),
               GEM_PATH=str(args.gems), BUNDLE_GEMFILE=str(HERE / 'Gemfile'),
               TRIAL_IMAGE_REFERENCE='wrong-candidate-settings')
    for key in ('DOCKER_HOST', 'DOCKER_CONFIG', 'DOCKER_CONTEXT', 'DOCKER_TLS_VERIFY', 'DOCKER_CERT_PATH', 'TRIAL_ROOT'):
        env.pop(key, None)
    env.update(record['runtime_env'])
    run([str(args.gems / 'bin/bundle'), 'exec', 'kamal', 'rollback', 'prior',
         '-c', str(args.runner / 'deploy.yml')], env=env, timeout=150)
    _, actual = run(ssh + ['docker inspect leapview-site-trial-web-prior'])
    actual = json.loads(actual)[0]
    assert actual['State']['Running'] and actual['Image'] == record['image_id']
    assert actual['Config']['Cmd'] == ['/fixture']
    assert 'TRIAL_IMAGE_REFERENCE=' + record['reference'] in actual['Config']['Env']
    print('PASS fresh controller uses only SSH to restore offline prior image/configuration')


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--controller', action='store_true')
    for name in ('state', 'artifacts', 'gems', 'registry', 'host', 'runner'):
        parser.add_argument('--' + name, type=Path)
    args = parser.parse_args()
    if args.controller:
        controller(args)
        return
    if not all((args.state, args.artifacts, args.gems, args.registry)):
        parser.error('state, artifacts, gems and registry are required')
    args.mitigate, args.snapshotter, args.disk_mib = True, 'overlayfs', 0
    trial = Trial(args)
    try:
        trial.setup()
        trial.deploy('prior')
        record = json.loads((trial.root / 'records/prior.json').read_text())
        record['status'] = 'verified'
        trial.save_record('prior', record)
        trial.deploy('current')
        trial.registry.terminate()
        trial.registry.wait(timeout=10)
        runner = trial.root.with_name(trial.root.name + '-controller')
        runner.mkdir(mode=0o700)
        for name in ('client_key', 'known_hosts'):
            shutil.copyfile(trial.root / name, runner / name)
            (runner / name).chmod(0o600)
        (runner / 'ssh_config').write_text((trial.root / 'ssh_config').read_text().replace(str(trial.root), str(runner)))
        _, output = run(['unshare', '--mount', 'python3', '-B', str(Path(__file__).resolve()),
                        '--controller', '--host', str(trial.root), '--runner', str(runner),
                        '--gems', str(args.gems.resolve())], timeout=180)
        assert trial.served()['version'] == 'prior'
        assert trial.served()['image_reference'] == record['reference']
        trial.report['fresh_runner_host_filesystem_hidden'] = True
        trial.report['fresh_runner_registry_offline_rollback'] = True
        print(output, end='')
    except BaseException as exc:
        trial.report['error'] = str(exc)
        raise
    finally:
        trial.close()


if __name__ == '__main__':
    main()
