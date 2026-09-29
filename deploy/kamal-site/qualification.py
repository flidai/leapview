#!/usr/bin/env python3
"""Synthetic qualification in a private PID/network/mount namespace ONLY.

Uses the pinned #751 fixture harness, but invokes this adapter and supervisor.
Test bindings are confined to a generated copy in the disposable namespace.
"""
import argparse
import base64
import fcntl
import hashlib
import gzip
import importlib.util
import io
import json
import os
from pathlib import Path
import shlex
import signal
import socket
import sys
import subprocess
import threading
import tempfile
import time
import traceback
import tarfile
from urllib.request import urlopen, Request

from qualification_topology import verify_topology

HERE = Path(__file__).resolve().parent


def host_command(adapter, operation, **values):
    source = (adapter / 'contract.py').read_text() + '\n' + '\n'.join(
        line for line in (adapter / 'host.py').read_text().splitlines()
        if not line.startswith('from contract import '))
    payload = base64.b64encode(json.dumps({'operation': operation, **values}).encode()).decode()
    source = 'import io,base64,sys; sys.stdin=io.StringIO(base64.b64decode(' + repr(payload) + ').decode())\n' + source
    return shlex.join(['python3', '-c', source])


def drop_rpc_reply(state, attempt, command):
    connection = socket.socket(socket.AF_UNIX)
    connection.connect(str(state / 'operator.sock'))
    connection.sendall(json.dumps({'attempt': attempt, 'command': command}).encode() + b'\n')
    connection.close()


def process_descendants(pid):
    found, pending = set(), [pid]
    while pending:
        parent = pending.pop()
        path = Path('/proc') / str(parent) / 'task' / str(parent) / 'children'
        try:
            children = [int(value) for value in path.read_text().split()]
        except (FileNotFoundError, ProcessLookupError, PermissionError):
            continue
        for child in children:
            if child not in found:
                found.add(child); pending.append(child)
    return found


def main():
    p = argparse.ArgumentParser()
    for name in ('trial', 'state', 'artifacts', 'gems', 'registry'):
        p.add_argument('--' + name, type=Path, required=True)
    args = p.parse_args()
    sys.path.insert(0, str(args.trial))
    from lifecycle import Trial, run, wait_until
    args.mitigate, args.snapshotter, args.disk_mib = False, 'overlayfs', 0
    t = Trial(args)  # Verifies PID 1, root, and an isolated network before mutation.
    t.report['adapter_source_sha256'] = {name: hashlib.sha256((HERE / name).read_bytes()).hexdigest() for name in ('contract.py', 'deploy.py', 'host.py', 'supervisor.py', 'guard.rb', 'qualification.py', 'qualification_topology.py')}
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
        def public_revision():
            ip = t.inspect('kamal-proxy')['NetworkSettings']['Networks']['kamal']['IPAddress']
            req = Request('http://' + ip + '/', headers={'Host': 'leapview.dev'})
            with urlopen(req, timeout=5) as response:
                return json.load(response)['version']
        def run_fixture_operation(operation):
            connect = deploy.connect
            argv = sys.argv
            deploy.connect = lambda directory: None
            try:
                sys.argv = ['deploy.py', operation]
                deploy.main()
            finally:
                deploy.connect = connect
                sys.argv = argv
        def settle_kamal_lock(record):
            with tempfile.TemporaryDirectory(prefix='kamal-lock-audit-') as directory:
                config = deploy.configure(Path(directory), record)
                with deploy.ownership():
                    status = deploy.kamal(config, 'lock', 'status').decode(errors='replace')
                    if 'There is no deploy lock' in status:
                        return False
                    if 'Automatic deploy lock' not in status or 'Version:' not in status:
                        raise AssertionError('Kamal lock owner is not the interrupted qualified deployment: ' + status)
                    deploy.kamal(config, 'lock', 'release')
                    return True
        def archive_unresolved_owner(label, active, pending, route_revision):
            journal_path = state / 'owner.json'
            journal = json.loads(journal_path.read_text())
            assert journal['status'] == 'unresolved', journal
            assert journal['work'] and all(w['exit_code'] is not None for w in journal['work']), journal['work']
            snapshot = json.loads((state / 'state.json').read_text())
            assert snapshot['active'] == active and snapshot.get('pending') == pending, snapshot
            assert public_revision() == route_revision
            proxy = t.inspect('kamal-proxy')
            active_record = snapshot['records'][active]
            active_container = t.inspect('leapview-site-web-' + active)
            host.validate_container(active_record, active_container, require_running=False)
            assert active_container['Image'] == host.local_image(active_record)['Id']
            pending_container = None
            if pending:
                result, raw = t.docker('inspect', 'leapview-site-web-' + pending, check=False)
                if result == 0:
                    pending_container = json.loads(raw)[0]
            held = []
            try:
                for filename in ('reconcile.lock', 'deploy.lock'):
                    fd = os.open(state / filename, os.O_RDWR)
                    fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
                    held.append(fd)
                evidence = {
                    'attempt': journal['attempt'],
                    'status': journal['status'],
                    'completed_work_exit_codes': [w['exit_code'] for w in journal['work']],
                    'active': active,
                    'pending': pending,
                    'route_revision': route_revision,
                    'saved_active_container_id': active_container['Id'],
                    'saved_active_running': active_container['State']['Running'],
                    'candidate_container_id': pending_container['Id'] if pending_container else None,
                    'proxy_container_id': proxy['Id'],
                    'saved_active_revision': active_record['revision'],
                }
                journal_path.rename(state / (label + '-owner.json'))
            finally:
                for fd in held: os.close(fd)
            t.report.setdefault('failure_matrix', {})[label] = evidence
            return evidence
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
            # Explicit synthetic fixture binding only. Production must measure
            # each exact image on the matching runtime before recording it.
            ready_file = state / 'ready.json'
            if ready_file.exists():
                fixture_ready = json.loads(ready_file.read_text())
                for budget in fixture_ready['capacity'].values():
                    budget['qualified_images'].append(record['image'])
                ready_file.write_text(json.dumps(fixture_ready))
            return record
        first = make_image(1)
        ready = {'schema': 1, 'controller': 'kamal', 'handover_verified': True, 'capacity': {}}
        # Synthetic bytes/inodes bounds, not production capacity qualification.
        for device in host.inventory()['disks']:
            ready['capacity'][device] = dict(paths=host.inventory()['disks'][device]['paths'], measured_peak_bytes=1024**2, measured_peak_inodes=100,
                candidate_headroom_bytes=2*1024**2, reserve_bytes=10**12, reserve_inodes=10000,
                qualified_compressed_bytes=100*1024**2, qualified_images=[first['image']])
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
            # Bootstrap is a verified initial handover, not recovery of a
            # distinct interrupted candidate. Seed its protected fixture state.
            initial = deploy.remote('state')
            initial['pending'] = None
            initial['maintenance_pending'] = True
            host.save(initial)
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
            for field in ('candidate_headroom_bytes', 'reserve_inodes', 'qualified_compressed_bytes', 'qualified_images'):
                changed = json.loads(original_ready)
                for budget in changed['capacity'].values():
                    budget[field] = [] if field == 'qualified_images' else (1 if field == 'qualified_compressed_bytes' else 10**15)
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
        t.registry = t.spawn('registry-restarted-for-topology', list(t.registry.args))
        wait_until(lambda: urlopen('http://127.0.0.1:5000/v2/').status == 200)
        topology = t.root / 'topology'; topology.mkdir()
        compose = (HERE / 'topology/compose.yaml').read_text().replace('/var/lib/leapview-site/', str(topology) + '/')
        (topology / 'compose.yaml').write_text(compose)
        caddy = (HERE / 'topology/Caddyfile').read_text().replace('leapview.dev {', 'leapview.dev {\n tls internal')
        (topology / 'Caddyfile').write_text(caddy)
        caddy_tag = t.repo + ':caddy-topology-fixture'
        t.docker('tag', 'caddy:2.10.2-alpine', caddy_tag)
        t.docker('push', caddy_tag)
        caddy_image = t.inspect(caddy_tag)
        caddy_reference = caddy_image['RepoDigests'][0]
        (topology / 'deployment.env').write_text('CADDY_IMAGE=' + caddy_reference + '\n')
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
        verify_topology(HERE, t, state, topology)
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
        verify_topology(HERE, t, state, topology)

        # Real supervised pull interrupted while the Docker client is waiting
        # on the private registry. Losing only the lock transport must leave
        # the remote mutation journaled and block a second owner.
        supervisor_source = (adapter / 'supervisor.py').read_text()
        contender_args = ['ssh', '-F', str(t.root / 'ssh_config'), '127.0.0.1',
                          shlex.join(['python3', '-c', supervisor_source, 'serve', 'f' * 32])]
        pull_candidate = make_image(201)
        previous_state = deploy.remote('state')
        previous_record = previous_state['records'][previous_state['active']]
        pull_outcome = {}
        pull_thread = None
        lock_process = None
        try:
            with deploy.ownership() as lock_process:
                deploy.remote('preflight', record=pull_candidate)
                deploy.remote('begin', record=pull_candidate)
                baseline = len(json.loads((state / 'owner.json').read_text())['work'])
                t.registry.send_signal(signal.SIGSTOP)

                def execute_pull():
                    try: pull_outcome['result'] = deploy.remote('pull', version=pull_candidate['version'])
                    except BaseException as exc: pull_outcome['error'] = exc

                pull_thread = threading.Thread(target=execute_pull, name='interrupted-site-pull')
                pull_thread.start()

                def docker_pull_running():
                    journal = json.loads((state / 'owner.json').read_text())
                    if len(journal['work']) <= baseline or journal['work'][-1]['exit_code'] is not None:
                        return False
                    root_pid = journal['work'][-1]['pid']
                    for pid in (root_pid, *process_descendants(root_pid)):
                        try:
                            argv = (Path('/proc') / str(pid) / 'cmdline').read_bytes().decode(errors='ignore').split('\0')
                        except (FileNotFoundError, ProcessLookupError, PermissionError):
                            continue
                        if Path(argv[0]).name == 'docker' and 'pull' in argv:
                            return True
                    return False

                wait_until(docker_pull_running, seconds=15)
                lock_process.stdin.close()
                wait_until(lambda: json.loads((state / 'owner.json').read_text())['status'] == 'unresolved', seconds=15)
                pull_journal = json.loads((state / 'owner.json').read_text())
                assert pull_journal['status'] == 'unresolved'
                assert pull_journal['work'][-1]['exit_code'] is None
                assert subprocess.run(contender_args, input=b'finish\n', capture_output=True).returncode != 0
                t.registry.send_signal(signal.SIGCONT)
                t.registry.terminate(); t.registry.wait(timeout=10)
                pull_thread.join(timeout=60)
                assert not pull_thread.is_alive()
                assert 'error' in pull_outcome, pull_outcome
        except RuntimeError as exc:
            assert 'lock connection lost' in str(exc), str(exc)
        assert lock_process is not None
        lock_process.wait(timeout=20)
        assert subprocess.run(contender_args, input=b'finish\n', capture_output=True).returncode != 0
        assert t.docker('image', 'inspect', host.LOCAL_REPOSITORY + ':' + pull_candidate['version'], check=False)[0] != 0
        pull_state = json.loads((state / 'state.json').read_text())
        assert pull_state['active'] == previous_record['version']
        assert pull_state['pending'] == pull_candidate['version']
        assert public_revision() == previous_record['revision']
        archive_unresolved_owner('interrupted_pull', previous_record['version'], pull_candidate['version'],
                                 previous_record['revision'])
        run_fixture_operation('recover')
        recovered = deploy.remote('state')
        assert recovered['active'] == previous_record['version'] and recovered.get('pending') is None
        assert public_revision() == previous_record['revision']
        t.report['failure_matrix']['interrupted_pull']['recovered_without_registry_pull'] = True
        print('PASS interrupted real digest pull stays pending, records remote failure, blocks takeover, then recovers locally', flush=True)
        t.registry = t.spawn('registry-restarted-after-pull', list(t.registry.args))
        wait_until(lambda: urlopen('http://127.0.0.1:5000/v2/').status == 200)

        # Kill the local Kamal client after the real private proxy starts
        # serving the candidate. The remote work is supervised; acceptance
        # remains pending until the explicit recovery command restores active.
        switch_candidate = make_image(202)
        previous_record = None
        original_run = deploy.run
        interrupted_kamal = {}

        def terminate_after_actual_switch(args, **kwargs):
            if (args and args[0] == 'bundle' and 'app' in args and 'boot' in args
                    and switch_candidate['version'] in args):
                log_path = state / 'interrupted-kamal-client.log'
                with log_path.open('wb') as log:
                    process = subprocess.Popen(args, stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
                    try:
                        wait_until(lambda: public_revision() == switch_candidate['revision'], seconds=90)
                        interrupted_kamal['route_revision'] = public_revision()
                        if process.poll() is not None:
                            raise AssertionError('Kamal finished before interruption; no live-client failure evidence')
                        lock_process.stdin.close()
                    except BaseException:
                        if process.poll() is None:
                            os.killpg(process.pid, signal.SIGTERM)
                            process.wait(timeout=15)
                        raise
                    if process.poll() is None:
                        os.killpg(process.pid, signal.SIGTERM)
                    process.wait(timeout=15)
                assert process.returncode != 0, 'Kamal finished normally before interruption'
                interrupted_kamal['client_returncode'] = process.returncode
                raise ConnectionError('injected local Kamal interruption after actual proxy switch')
            return original_run(args, **kwargs)

        deploy.run = terminate_after_actual_switch
        switch_error = None
        lock_process = None
        try:
            with deploy.ownership() as lock_process:
                previous_state = deploy.remote('preflight', record=switch_candidate)['state']
                previous_record = previous_state['records'][previous_state['active']]
                deploy.remote('begin', record=switch_candidate)
                try:
                    deploy.transition(adapter, switch_candidate, previous_record, pull=True)
                    raise AssertionError('interrupted switch unexpectedly accepted')
                except ConnectionError as exc:
                    switch_error = str(exc)
        except RuntimeError as exc:
            assert 'lock connection lost' in str(exc), str(exc)
        finally:
            deploy.run = original_run
        assert switch_error and 'interruption' in switch_error
        assert interrupted_kamal.get('route_revision') == switch_candidate['revision']
        lock_process.wait(timeout=30)
        assert subprocess.run(contender_args, input=b'finish\n', capture_output=True).returncode != 0
        switch_state = json.loads((state / 'state.json').read_text())
        assert switch_state['active'] == previous_record['version']
        assert switch_state['pending'] == switch_candidate['version']
        assert public_revision() == switch_candidate['revision']
        archive_unresolved_owner('interrupted_switch', previous_record['version'], switch_candidate['version'],
                                 switch_candidate['revision'])
        stale_kamal_lock = settle_kamal_lock(switch_candidate)
        t.report['failure_matrix']['interrupted_switch']['stale_kamal_lock_released_after_audit'] = stale_kamal_lock
        run_fixture_operation('recover')
        recovered = deploy.remote('state')
        assert recovered['active'] == previous_record['version'] and recovered.get('pending') is None
        assert public_revision() == previous_record['revision']
        t.report['failure_matrix']['interrupted_switch']['recovered_to_saved_active'] = True
        t.report['failure_matrix']['interrupted_switch']['kamal_client_returncode'] = interrupted_kamal['client_returncode']
        print('PASS interrupted actual Kamal switch leaves prior+pending state and candidate route, then explicit recover restores prior', flush=True)

        # Let the host commit acceptance and close the real supervisor RPC
        # socket before its reply. This verifies route, durable state and work
        # journal after an actually committed but unacknowledged acceptance.
        acceptance_candidate = make_image(203)
        previous_record = None
        original_remote = deploy.remote
        acceptance_attempt = {}

        def drop_acceptance_reply(operation, **values):
            if operation != 'accept': return original_remote(operation, **values)
            attempt = os.environ['SITE_ATTEMPT']
            acceptance_attempt['id'] = attempt
            before = len(json.loads((state / 'owner.json').read_text())['work'])
            drop_rpc_reply(state, attempt, host_command(adapter, 'accept', version=acceptance_candidate['version']))
            wait_until(lambda: (len(json.loads((state / 'owner.json').read_text())['work']) > before
                               and json.loads((state / 'owner.json').read_text())['work'][-1]['exit_code'] is not None),
                       seconds=15)
            wait_until(lambda: lock_process.poll() is not None, seconds=15)
            raise ConnectionError('accept response lost after host commit')

        deploy.remote = drop_acceptance_reply
        acceptance_error = None
        lock_process = None
        try:
            with deploy.ownership() as lock_process:
                previous_state = original_remote('preflight', record=acceptance_candidate)['state']
                previous_record = previous_state['records'][previous_state['active']]
                original_remote('begin', record=acceptance_candidate)
                try:
                    deploy.transition(adapter, acceptance_candidate, previous_record, pull=True)
                    raise AssertionError('lost acceptance reply unexpectedly completed maintenance')
                except ConnectionError as exc:
                    acceptance_error = str(exc)
        except RuntimeError as exc:
            assert 'lock connection lost' in str(exc), str(exc)
        finally:
            deploy.remote = original_remote
        assert acceptance_error and 'after host commit' in acceptance_error
        lock_process.wait(timeout=30)
        assert subprocess.run(contender_args, input=b'finish\n', capture_output=True).returncode != 0
        accepted_state = json.loads((state / 'state.json').read_text())
        assert accepted_state['active'] == acceptance_candidate['version']
        assert accepted_state.get('pending') is None and accepted_state.get('maintenance_pending') is True
        assert public_revision() == acceptance_candidate['revision']
        archive_unresolved_owner('lost_acceptance_reply', acceptance_candidate['version'], None,
                                 acceptance_candidate['revision'])
        run_fixture_operation('maintain')
        maintained = deploy.remote('state')
        assert maintained['active'] == acceptance_candidate['version'] and not maintained.get('maintenance_pending')
        assert public_revision() == acceptance_candidate['revision']
        t.report['failure_matrix']['lost_acceptance_reply']['maintenance_completed'] = True
        t.report['failure_matrix']['lost_acceptance_reply']['host_accept_exit_code'] = 0
        print('PASS host acceptance commits despite dropped reply; journal remains unresolved until audit and maintenance', flush=True)

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
        t.report['error'] = str(exc)
        t.report['error_traceback'] = traceback.format_exc()
        raise
    finally:
        t.close()
        (t.root.parent / (t.root.name + '-report.json')).write_text(json.dumps(t.report, indent=2))


if __name__ == '__main__': main()
