#!/usr/bin/env python3
"""Additional isolated acceptance/retention cases; never a production adapter."""
import argparse
import json
from pathlib import Path

from lifecycle import Trial, HTTP, wait_until


def main():
    parser = argparse.ArgumentParser()
    for name in ('state', 'artifacts', 'gems', 'registry'):
        parser.add_argument('--' + name, type=Path, required=True)
    args = parser.parse_args()
    args.mitigate, args.snapshotter, args.disk_mib = True, 'overlayfs', 0
    trial = Trial(args)
    try:
        trial.setup()
        for version in ('prior', 'current'):
            trial.deploy(version)
            record = json.loads((trial.root / 'records' / f'{version}.json').read_text())
            record['status'] = 'verified'
            trial.save_record(version, record)
        trial.setup_caddy()

        # A foreign image shares every application layer with the current image,
        # but has its own config/ownership and both running and stopped containers.
        trial.docker('create', '--name', 'foreign-seed', trial.repo + ':current')
        trial.docker('commit', '--change', 'LABEL service=foreign-fixture',
                     'foreign-seed', 'foreign-fixture:keep')
        trial.docker('rm', 'foreign-seed')
        foreign = trial.inspect('foreign-fixture:keep')
        assert foreign['RootFS']['Layers'] == trial.inspect(trial.repo + ':current')['RootFS']['Layers']
        trial.docker('run', '-d', '--name', 'foreign-live', 'foreign-fixture:keep')
        trial.docker('create', '--name', 'foreign-stopped', 'foreign-fixture:keep')

        # Supported redeploy omits automatic prune, leaving the last two verified
        # versions available until the external/public acceptance result is known.
        reference = trial.image('public-rejected')
        trial.env['TRIAL_IMAGE_REFERENCE'] = reference
        trial.kamal('redeploy', '--skip-push', '--version', 'public-rejected')
        assert trial.served()['version'] == 'public-rejected'
        for version in ('prior', 'current'):
            assert trial.inspect('leapview-site-trial-web-' + version)
        # Deliberate failed public identity expectation: readiness alone is not
        # acceptance. Restore the recorded current settings before local rollback.
        assert trial.served()['version'] != 'expected-public-build'
        record = json.loads((trial.root / 'records/current.json').read_text())
        (trial.root / 'deploy.yml').write_text(record['runtime_config'])
        trial.env.update(record['runtime_env'])
        trial.kamal('rollback', 'current')
        assert trial.served()['version'] == 'current'
        assert trial.inspect('leapview-site-trial-web-prior')
        trial.report['deferred_prune_preserves_both_verified_versions_on_public_failure'] = True
        # Exact cleanup is still required: remove only this rejected attempt and
        # stopped replacement copies of the now-live, verified current version.
        allowed = {trial.report['images'][v]['id'] for v in ('public-rejected', 'current')}
        for item in trial.snapshot()['containers']:
            container = trial.inspect(item['ID'])
            if not container['State']['Running'] and container['Image'] in allowed:
                assert container['Config']['Labels']['service'] == 'leapview-site-trial'
                trial.docker('rm', container['Id'])
        trial.kamal('prune', 'all')
        assert trial.inspect('leapview-site-trial-web-prior')
        assert trial.served()['version'] == 'current'
        trial.report['exact_rejected_and_duplicate_cleanup_preserves_distinct_prior'] = True

        # Registry outage during a repeated-version pull must not stop live
        # traffic, even if Kamal has removed the selected repository tag first.
        trial.registry.terminate()
        trial.registry.wait(timeout=10)
        code, _ = trial.kamal('redeploy', '--skip-push', '--version', 'current', check=False)
        assert code != 0 and trial.served()['version'] == 'current'
        assert trial.inspect('leapview-site-trial-web-prior')
        trial.report['registry_failure_on_same_version_pull_preserves_live_and_prior'] = True
        trial.registry = trial.spawn('registry-restarted', [str(trial.registry_binary), 'serve', str(trial.root / 'registry.yml')])
        wait_until(lambda: HTTP.open('http://127.0.0.1:5000/v2/').status == 200)
        trial.docker('pull', trial.repo + ':current')
        trial.deploy('accepted')
        assert trial.served()['version'] == 'accepted'
        assert trial.inspect('leapview-site-trial-web-current')
        assert trial.inspect('foreign-fixture:keep')['Id'] == foreign['Id']
        assert trial.inspect('foreign-live')['State']['Running']
        assert trial.inspect('foreign-stopped')['State']['Status'] == 'created'
        assert trial.inspect('trial-caddy')['State']['Running']
        trial.report['foreign_images_containers_and_shared_layers_preserved'] = True
        trial.report['registry_recovery_then_healthy_deployment'] = True
        print('PASS deferred public acceptance, exact recovery cleanup, registry outage and shared-layer ownership', flush=True)
    except BaseException as exc:
        trial.report['error'] = str(exc)
        raise
    finally:
        trial.close()


if __name__ == '__main__':
    main()
