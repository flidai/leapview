#!/usr/bin/env python3
"""Synthetic qualification in a private PID/network/mount namespace ONLY.

Uses the pinned #751 fixture harness, but invokes this adapter and supervisor.
Test bindings are confined to a generated copy in the disposable namespace.
"""
import argparse
import fcntl
import hashlib
import gzip
import importlib.util
import io
import json
import os
from pathlib import Path
import shlex
import sys
import subprocess
import time
import tarfile
from urllib.request import urlopen, Request

HERE = Path(__file__).resolve().parent


def main():
    p = argparse.ArgumentParser()
    for name in ('trial', 'state', 'artifacts', 'gems', 'registry'):
        p.add_argument('--' + name, type=Path, required=True)
    args = p.parse_args()
    sys.path.insert(0, str(args.trial))
    from lifecycle import Trial, run, wait_until
    args.mitigate, args.snapshotter, args.disk_mib = False, 'overlayfs', 0
    t = Trial(args)  # Verifies PID 1, root, and an isolated network before mutation.
    t.report['adapter_source_sha256'] = {name: hashlib.sha256((HERE / name).read_bytes()).hexdigest() for name in ('contract.py', 'deploy.py', 'host.py', 'supervisor.py', 'guard.rb', 'qualification.py')}
    spawn = t.spawn
    def fixture_spawn(name, argv):
        # Port publication needs userspace forwarding in this no-iptables namespace.
        return spawn(name, [arg.replace('--userland-proxy=false', '--userland-proxy=true') for arg in argv])
    t.spawn = fixture_spawn
    try:
        run(['mount', '--make-rprivate', '/'])
        disk = t.root.parent / (t.root.name + '.ext4')
        with disk.open('wb') as stream: stream.truncate(5 * 1024**3)
        run(['mkfs.ext4', '-q', '-F', '-m', '0', str(disk)])
        run(['mount', '-o', 'loop', str(disk), str(t.root)])
        t.setup()
        adapter = t.root / 'adapter'; adapter.mkdir()
        state = t.root / 'state'; state.mkdir(mode=0o700)
        for name in ('contract.py', 'host.py', 'deploy.py', 'supervisor.py', 'guard.rb', 'Gemfile', 'Gemfile.lock'):
            source = (HERE / name).read_text()
            source = source.replace('ghcr.io/flidai/leapview-site', t.repo)
            source = source.replace("HOST = '100.73.220.23'", "HOST = '127.0.0.1'")
            source = source.replace("Path('/var/lib/leapview-site/kamal')", 'Path(' + repr(str(state)) + ')')
            source = source.replace("Path('/opt/leapview-site/reconcile.lock')", 'Path(' + repr(str(state / 'reconcile.lock')) + ')')
            source = source.replace("Path('/opt/leapview-site/deploy.lock')", 'Path(' + repr(str(state / 'deploy.lock')) + ')')
            if name == 'host.py':
                start = source.index('    for unit in '); end = source.index("    state = protected_read", start)
                source = source[:start] + source[end:]
                source = source.replace('    topology_ready(ready, proxy)\n', '')
                source = source.replace("'/var/lib/containerd'", repr(str(t.root / 'containerd')))
            (adapter / name).write_text(source)
        sys.path.insert(0, str(adapter))
        import deploy, host
        os.environ.update(t.env)
        os.environ['PATH'] = str(args.gems / 'bin') + ':' + os.environ['PATH']
        os.environ['SITE_SSH_CONFIG'] = str(t.root / 'ssh_config')
        original_configure = deploy.configure
        def configure(directory, record):
            path = original_configure(directory, record)
            config = json.loads(path.read_text())
            config['ssh']['port'] = 22222
            config['ssh']['keys'] = [str(t.root / 'client_key')]
            config['deploy_timeout'] = 5
            config['drain_timeout'] = 1
            config['stop_timeout'] = 1
            path.write_text(json.dumps(config))
            return path
        deploy.configure = configure
        def public_check(record):
            ip = t.inspect('kamal-proxy')['NetworkSettings']['Networks']['kamal']['IPAddress']
            req = Request('http://' + ip + '/', headers={'Host': 'leapview.dev'})
            with urlopen(req, timeout=5) as response:
                if json.load(response)['version'] != record['revision']: raise ValueError('wrong proxy target')
        deploy.public_check = public_check
        def make_image(n, unhealthy=False):
            revision = f'{n:040x}'
            def layer(files):
                output = io.BytesIO()
                with tarfile.open(fileobj=output, mode='w') as tar:
                    for name, data in files:
                        info = tarfile.TarInfo(name); info.size = len(data); info.mode = 0o755
                        tar.addfile(info, io.BytesIO(data))
                raw = output.getvalue()
                return raw, gzip.compress(raw, mtime=0)
            base_raw, base = layer([('leapview-site', (args.artifacts / 'fixture').read_bytes())])
            delta_raw, delta = layer([('version', revision.encode()), ('payload', os.urandom(1024 * 128))])
            blobs = {}
            def blob(data, media_type):
                digest = hashlib.sha256(data).hexdigest(); blobs[digest] = data
                return {'mediaType': media_type, 'digest': 'sha256:' + digest, 'size': len(data)}
            layers = [blob(data, 'application/vnd.oci.image.layer.v1.tar+gzip') for data in (base, delta)]
            t.report['shared_fixture_layer'] = layers[0]['digest']
            config = {'architecture': 'amd64', 'os': 'linux',
                'config': {'Entrypoint': ['/leapview-site'], 'User': '65532:65532',
                    'Env': ['TRIAL_UNHEALTHY=' + str(int(unhealthy))],
                    'Labels': {'service': 'leapview-site', 'org.opencontainers.image.revision': revision}},
                'rootfs': {'type': 'layers', 'diff_ids': ['sha256:' + hashlib.sha256(raw).hexdigest() for raw in (base_raw, delta_raw)]},
                'history': [{'created_by': 'shared fixture base'}, {'created_by': 'fixture version'}]}
            config_descriptor = blob(json.dumps(config).encode(), 'application/vnd.oci.image.config.v1+json')
            manifest = {'schemaVersion': 2, 'mediaType': 'application/vnd.oci.image.manifest.v1+json',
                        'config': config_descriptor, 'layers': layers}
            manifest_descriptor = blob(json.dumps(manifest).encode(), manifest['mediaType'])
            tag = t.repo + ':fixture-' + str(n)
            manifest_descriptor['annotations'] = {'io.containerd.image.name': tag, 'org.opencontainers.image.ref.name': tag}
            index = {'schemaVersion': 2, 'manifests': [manifest_descriptor]}
            archive = io.BytesIO()
            with tarfile.open(fileobj=archive, mode='w') as tar:
                files = [('oci-layout', b'{"imageLayoutVersion":"1.0.0"}'), ('index.json', json.dumps(index).encode())]
                files += [('blobs/sha256/' + digest, data) for digest, data in blobs.items()]
                for name, data in files:
                    info = tarfile.TarInfo(name); info.size = len(data); info.mode = 0o644
                    tar.addfile(info, io.BytesIO(data))
            t.docker('load', data=archive.getvalue())
            t.docker('push', tag)
            image = t.inspect(tag); ref = image['RepoDigests'][0]; digest = ref.split('@')[1]
            manifest = json.load(urlopen(Request('http://127.0.0.1:5000/v2/site/manifests/' + digest, headers={'Accept': 'application/vnd.oci.image.manifest.v1+json'})))
            record = {'schema': 1, 'version': 'k' + digest.split(':')[1], 'image': ref, 'revision': revision,
                      'platform': digest, 'config': manifest['config']['digest'], 'runtime': deploy.RUNTIME,
                      'kamal': '2.12.0', 'release': {}, 'compressed_bytes': sum(l['size'] for l in manifest['layers'])}
            t.docker('image', 'rm', tag)
            return record
        first = make_image(1)
        ready = {'schema': 1, 'controller': 'kamal', 'handover_verified': True, 'capacity': {}}
        # Synthetic bytes/inodes bounds, not production capacity qualification.
        for device in host.inventory()['disks']:
            ready['capacity'][device] = dict(paths=host.inventory()['disks'][device]['paths'], measured_peak_bytes=1024**2, measured_peak_inodes=100,
                candidate_headroom_bytes=2*1024**2, reserve_bytes=10**12, reserve_inodes=10000,
                qualified_compressed_bytes=100*1024**2)
            ready['capacity'][device]['reserve_bytes'] = max(2*1024**3, (host.inventory()['disks'][device]['capacity_bytes']+9)//10)
        (state / 'ready.json').write_text(json.dumps(ready))
        (state / 'state.json').write_text(json.dumps({'schema': 1, 'active': first['version'], 'pending': first['version'],
            'records': {first['version']: dict(first, verified=True)}}))
        config = configure(adapter, first)
        with deploy.ownership():
            deploy.remote('pull', version=first['version']); deploy.remote('image', version=first['version'])
            deploy.kamal(config, 'proxy', 'boot')
            deploy.kamal(config, 'app', 'boot', '--version', first['version'])
            deploy.remote('verify', version=first['version']); public_check(first)
            deploy.remote('restored', version=first['version'])
            deploy.remote('cleanup'); deploy.kamal(config, 'prune', 'all'); deploy.remote('maintained')
        print('PASS supervised boot/upload/identity/public proxy verification', flush=True)
        for n in range(2, 12):
            candidate = make_image(n)
            with deploy.ownership():
                before = deploy.remote('preflight', record=candidate)['state']
                deploy.remote('begin', record=candidate)
                deploy.transition(adapter, candidate, before['records'][before['active']], pull=True)
            snapshot = deploy.remote('state')
            assert len(snapshot['records']) == 2
            images = t.docker('image', 'ls', '--filter', 'label=service=leapview-site', '--quiet', '--no-trunc')[1].split()
            assert len(set(images)) == 2, images
            t.report['cycles'].append({'n': n, 'active': snapshot['active'], 'prior': snapshot['prior'], 'retained_images': len(set(images)), 'disk': t.disk_usage()})
        print('PASS ten updates through the revised adapter', flush=True)
        candidate = make_image(100, unhealthy=True)
        with deploy.ownership():
            snapshot = deploy.remote('state')
            image_ids = set(t.docker('image', 'ls', '--quiet', '--no-trunc')[1].split())
            original_ready = (state / 'ready.json').read_text()
            for field in ('candidate_headroom_bytes', 'reserve_inodes', 'qualified_compressed_bytes'):
                changed = json.loads(original_ready)
                for budget in changed['capacity'].values(): budget[field] = 10**15 if field != 'qualified_compressed_bytes' else 1
                (state / 'ready.json').write_text(json.dumps(changed))
                try:
                    deploy.remote('preflight', record=candidate)
                    raise AssertionError('capacity/envelope guard did not reject')
                except RuntimeError: pass
                finally: (state / 'ready.json').write_text(original_ready)
                assert set(t.docker('image', 'ls', '--quiet', '--no-trunc')[1].split()) == image_ids
                assert deploy.remote('state') == snapshot
            original_state = (state / 'state.json').read_text()
            corrupt = json.loads(original_state)
            corrupt['records'][corrupt['prior']]['runtime']['port'] = 9999
            (state / 'state.json').write_text(json.dumps(corrupt))
            try:
                deploy.remote('rollback-begin')
                raise AssertionError('corrupt recovery accepted')
            except RuntimeError: pass
            finally: (state / 'state.json').write_text(original_state)
            t.docker('rm', 'leapview-site-web-' + snapshot['prior'])
            try:
                deploy.remote('rollback-begin')
                raise AssertionError('missing recovery container accepted')
            except RuntimeError: pass
            assert deploy.remote('state') == snapshot
            # Restore only the intentionally removed fixture material.
            repair = dict(snapshot, maintenance_pending=True)
            (state / 'state.json').write_text(json.dumps(repair))
            deploy.remote('preserve-prior')
            (state / 'state.json').write_text(original_state)
            t.docker('tag', snapshot['records'][snapshot['active']]['local_id'], 'foreign-fixture:keep')
            try:
                deploy.remote('preflight', record=candidate)
                raise AssertionError('foreign alias accepted')
            except RuntimeError: pass
            assert t.inspect('foreign-fixture:keep')['Id'] == snapshot['records'][snapshot['active']]['local_id']
            t.docker('image', 'rm', 'foreign-fixture:keep')
        t.report['capacity_recovery_and_foreign_alias_guards'] = True
        print('PASS bytes/inodes/envelope gates, corrupt/missing recovery rejection and foreign-alias preservation', flush=True)
        with deploy.ownership():
            before = deploy.remote('preflight', record=candidate)['state']
            deploy.remote('begin', record=candidate)
            try:
                deploy.transition(adapter, candidate, before['records'][before['active']], pull=True)
                raise AssertionError('unhealthy candidate accepted')
            except RuntimeError:
                public_check(before['records'][before['active']])
                assert deploy.remote('state')['pending'] is None
        print('PASS unhealthy rejection restores verified active', flush=True)
        candidate = make_image(101)
        with deploy.ownership():
            before = deploy.remote('preflight', record=candidate)['state']
            deploy.remote('begin', record=candidate)
            def reject(record):
                if record['version'] == candidate['version']: raise ValueError('synthetic public acceptance failure')
                public_check(record)
            deploy.public_check = reject
            try:
                deploy.transition(adapter, candidate, before['records'][before['active']], pull=True)
                raise AssertionError('failed public acceptance accepted')
            except ValueError:
                assert deploy.remote('state')['active'] == before['active']
            finally: deploy.public_check = public_check
        print('PASS failed public acceptance restores verified active', flush=True)
        candidate = make_image(102)
        original_remote = deploy.remote
        def fail_cleanup(operation, **values):
            if operation == 'cleanup': raise RuntimeError('injected cleanup failure after acceptance')
            return original_remote(operation, **values)
        try:
            with deploy.ownership() as lease:
                before = deploy.remote('preflight', record=candidate)['state']
                deploy.remote('begin', record=candidate)
                deploy.remote = fail_cleanup
                deploy.transition(adapter, candidate, before['records'][before['active']], pull=True)
                raise AssertionError('cleanup failure not surfaced')
        except RuntimeError as exc:
            assert 'injected cleanup failure' in str(exc)
        finally: deploy.remote = original_remote
        lease.wait(timeout=15)
        accepted = deploy.remote('state')
        assert accepted['active'] == candidate['version'] and accepted['maintenance_pending']
        public_check(candidate)
        # Explicit fixture recovery follows the runbook: old controller ended,
        # every remote job completed, both locks held, actual route inspected.
        held = []
        for filename in ('reconcile.lock', 'deploy.lock'):
            fd = os.open(state / filename, os.O_RDWR); held.append(fd)
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        journal = json.loads((state / 'owner.json').read_text())
        assert journal['status'] == 'unresolved'
        assert all(work['exit_code'] is not None for work in journal['work'])
        public_check(candidate)
        t.inspect('kamal-proxy'); t.inspect('leapview-site-web-' + candidate['version'])
        (state / 'owner.json').rename(state / 'audited-cleanup-incident.json')
        for fd in held: os.close(fd)
        connect = deploy.connect; argv = sys.argv
        deploy.connect = lambda directory: None
        try:
            sys.argv = ['deploy.py', 'maintain']
            deploy.main()
        finally: deploy.connect = connect; sys.argv = argv
        assert not deploy.remote('state')['maintenance_pending']
        public_check(candidate)
        t.report['cleanup_failure_then_explicit_maintenance'] = True
        print('PASS accepted version survives cleanup failure and explicit maintenance recovery', flush=True)
        # Registry process is offline for a fresh ownership session.
        t.registry.terminate(); t.registry.wait(timeout=5)
        current = deploy.remote('state')
        saved = {k: v for k, v in current['records'][current['active']].items() if k not in ('local_id', 'verified')}
        prepared = adapter / 'same-image.json'; prepared.write_text(json.dumps(saved)); prepared.chmod(0o600)
        container_before = t.inspect('leapview-site-web-' + current['active'])['Id']
        deploy.deploy(adapter, prepared)
        assert t.inspect('leapview-site-web-' + current['active'])['Id'] == container_before
        t.report['same_image_offline_noop'] = True
        print('PASS same-image no-op with registry offline and unchanged container identity', flush=True)
        with deploy.ownership():
            before = deploy.remote('state')
            t.docker('stop', 'leapview-site-web-' + before['active'])
            before = deploy.remote('rollback-begin')
            deploy.transition(adapter, before['records'][before['prior']], before['records'][before['active']], pull=False)
        with deploy.ownership():
            before = deploy.remote('state')
            t.docker('rm', '--force', 'leapview-site-web-' + before['active'])
            before = deploy.remote('rollback-begin')
            deploy.transition(adapter, before['records'][before['prior']], before['records'][before['active']], pull=False)
        print('PASS offline rollback with missing current preserves recovery material', flush=True)
        with deploy.ownership():
            before = deploy.remote('state')
            name = 'leapview-site-web-' + before['active']
            broken = t.inspect(name)
            t.docker('rm', '--force', name)
            config = broken['Config']
            config['Env'] = [e for e in config['Env'] if not e.startswith('TRIAL_UNHEALTHY=')] + ['TRIAL_UNHEALTHY=1']
            spec = adapter / 'unhealthy-current.json'
            spec.write_text(json.dumps({**config, 'HostConfig': broken['HostConfig']}))
            run(['curl', '--fail', '--silent', '--show-error', '--unix-socket', str(t.root / 'docker.sock'),
                 '-H', 'Content-Type: application/json', '--data-binary', '@' + str(spec),
                 'http://localhost/v1.52/containers/create?name=' + name])
            t.docker('start', name)
            before = deploy.remote('rollback-begin')
            deploy.transition(adapter, before['records'][before['prior']], before['records'][before['active']], pull=False)
        t.report['unhealthy_current_rollback'] = True
        print('PASS offline rollback with unhealthy current', flush=True)
        topology = t.root / 'topology'; topology.mkdir()
        compose = (HERE / 'topology/compose.yaml').read_text().replace('/var/lib/leapview-site/', str(topology) + '/')
        (topology / 'compose.yaml').write_text(compose)
        caddy = (HERE / 'topology/Caddyfile').read_text().replace('leapview.dev {', 'leapview.dev {\n tls internal')
        (topology / 'Caddyfile').write_text(caddy)
        (topology / 'deployment.env').write_text('CADDY_IMAGE=' + t.inspect('caddy:2.10.2-alpine')['Id'] + '\n')
        compose_args = ['compose', '--env-file', str(topology / 'deployment.env'), '-f', str(topology / 'compose.yaml')]
        def caddy_check():
            code, out = run(['curl', '--silent', '--show-error', '--fail', '--insecure', '--noproxy', '*',
                '--resolve', 'leapview.dev:443:127.0.0.1', 'https://leapview.dev/'], check=False)
            return code == 0 and json.loads(out)['version'] == deploy.remote('state')['records'][deploy.remote('state')['active']]['revision']
        t.docker(*compose_args, 'up', '-d', 'caddy')
        wait_until(caddy_check)
        t.docker(*compose_args, 'up', '-d', '--force-recreate', 'caddy')
        wait_until(caddy_check)
        services = t.docker(*compose_args, 'config', '--services')[1].split()
        assert services == ['caddy']
        legacy = t.root / 'protected-legacy'; legacy.mkdir(mode=0o700)
        old_compose = (HERE.parent / 'hetzner-site/files/compose.yaml').read_text().replace('/var/lib/leapview-site/', str(topology) + '/')
        (legacy / 'compose.yaml').write_text(old_compose)
        old_caddy = (HERE.parent / 'hetzner-site/files/Caddyfile').read_text().replace('leapview.dev {', 'leapview.dev {\n tls internal')
        (legacy / 'Caddyfile').write_text(old_caddy)
        active = deploy.remote('state')
        active_record = active['records'][active['active']]
        (legacy / 'deployment.env').write_text((topology / 'deployment.env').read_text() +
            'LEAPVIEW_SITE_IMAGE=localhost:5555/leapview-site@' + active_record['image'].split('@')[1] + '\n')
        legacy_args = ['compose', '--env-file', str(legacy / 'deployment.env'), '-f', str(legacy / 'compose.yaml')]
        t.docker(*legacy_args, 'up', '-d', '--force-recreate')
        wait_until(caddy_check)
        t.docker(*compose_args, 'up', '-d', '--force-recreate', 'caddy')
        wait_until(caddy_check)
        t.docker(*legacy_args, 'rm', '--stop', '--force', 'leapview-site')
        t.report['protected_compose_restoration'] = True
        print('PASS protected legacy Compose restoration and return to Kamal topology', flush=True)
        t.report['caddy_recreation'] = True
        print('PASS Caddy-only Compose recreation with declarative external Kamal network and HTTPS', flush=True)
        # Restart both storage/engine daemons inside the disposable namespace.
        # A real host reboot remains a production migration acceptance gate.
        daemons = [p for p in t.processes if p.args[0] in ('dockerd', 'containerd')]
        for process in reversed(daemons): process.terminate(); process.wait(timeout=30)
        for process in daemons:
            t.spawn(process.args[0] + '-restarted', process.args)
            if process.args[0] == 'containerd':
                wait_until(lambda: (t.root / 'containerd.sock').exists())
        wait_until(lambda: t.docker('info', check=False)[0] == 0)
        wait_until(caddy_check, seconds=60)
        t.report['docker_restart'] = True
        print('PASS Docker/containerd restart restores HTTPS topology without the legacy app', flush=True)
        supervisor_source = (adapter / 'supervisor.py').read_text()
        contender_args = ['ssh', '-F', str(t.root / 'ssh_config'), '127.0.0.1',
                          shlex.join(['python3', '-c', supervisor_source, 'serve', 'f' * 32])]
        try:
            with deploy.ownership() as connection:
                attempt = os.environ['SITE_ATTEMPT']
                work_args = ['ssh', '-F', str(t.root / 'ssh_config'), '127.0.0.1',
                             shlex.join(['python3', '-c', supervisor_source, 'exec', attempt,
                                         'sleep 3; touch ' + shlex.quote(str(state / 'work-finished'))])]
                worker = subprocess.Popen(work_args, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
                wait_until(lambda: bool(json.loads((state / 'owner.json').read_text())['work']))
                connection.stdin.close()
                time.sleep(.2)
                assert subprocess.run(contender_args, input=b'finish\n', capture_output=True).returncode != 0
                assert not (state / 'work-finished').exists()
                worker.communicate(timeout=10)
                assert worker.returncode == 0
                assert (state / 'work-finished').exists()
                connection.wait(timeout=10)
        except RuntimeError as exc:
            assert 'lock connection lost' in str(exc)
        assert subprocess.run(contender_args, input=b'finish\n', capture_output=True).returncode != 0
        assert json.loads((state / 'owner.json').read_text())['status'] == 'unresolved'
        t.report['ssh_lock_loss_blocks_until_explicit_recovery'] = True
        print('PASS real SSH lock-connection loss while separate remote mutation survives; takeover remains blocked', flush=True)
        t.report['capacity_samples'] = [json.loads(path.read_text()).get('capacity_observed', {}) for path in sorted((state / 'attempts').glob('*.json'))]
        t.report['offline_broken_current_rollback'] = True
        print('PASS offline rollback with stopped current and fresh operator ownership', flush=True)
    except BaseException as exc:
        t.report['error'] = str(exc); raise
    finally:
        t.close()
        (t.root.parent / (t.root.name + '-report.json')).write_text(json.dumps(t.report, indent=2))


if __name__ == '__main__': main()
