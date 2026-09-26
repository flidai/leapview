#!/usr/bin/env python3
"""Destructive synthetic trial, ONLY inside a fresh network+PID namespace.

This is test infrastructure, not a production deployment controller.
Run via sudo unshare --mount --net --pid --fork --mount-proc after downloading
pinned images and building fixture.go. See README.md for setup.
"""
import argparse
import errno
import hashlib
import io
import json
import os
from pathlib import Path
import shlex
import signal
import subprocess
import tarfile
import time
import threading
from urllib.request import build_opener, ProxyHandler, Request

from identity_guard import validate, validate_cleanup_scope, verified_noop

HERE = Path(__file__).resolve().parent
HTTP = build_opener(ProxyHandler({}))


def run(args, *, data=None, check=True, timeout=90, env=None):
    p = subprocess.run(args, input=data, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                       env=env, timeout=timeout)
    out = p.stdout.decode(errors='replace')
    if check and p.returncode:
        raise RuntimeError(f'{shlex.join(args)} exited {p.returncode}\n{out[-6000:]}')
    return p.returncode, out


def wait_until(fn, seconds=30):
    end = time.monotonic() + seconds
    while time.monotonic() < end:
        try:
            if fn():
                return
        except Exception:
            pass
        time.sleep(.2)
    raise RuntimeError('timeout waiting for fixture service')


class Trial:
    def __init__(self, args):
        self.root, self.artifacts = args.state.resolve(), args.artifacts.resolve()
        # A private PID namespace makes process 1 our launcher; network has only lo.
        if os.geteuid() != 0 or os.getpid() != 1:
            raise RuntimeError('run under sudo unshare --mount --net --pid --fork --mount-proc')
        if {link['ifname'] for link in json.loads(run(['ip', '-json', 'link'])[1])} != {'lo'}:
            raise RuntimeError('refusing non-isolated network namespace')
        self.root.mkdir(mode=0o700, parents=True, exist_ok=False)
        self.processes = []
        self.env = dict(os.environ, HOME=str(self.root), DOCKER_HOST=f'unix://{self.root}/docker.sock',
                        DOCKER_CONFIG=str(self.root / 'docker-config'),
                        GEM_HOME=str(args.gems.resolve()), GEM_PATH=str(args.gems.resolve()),
                        BUNDLE_GEMFILE=str(HERE / 'Gemfile'), TRIAL_ROOT=str(self.root))
        for key in ('DOCKER_CONTEXT', 'DOCKER_TLS_VERIFY', 'DOCKER_CERT_PATH'):
            self.env.pop(key, None)
        self.bundle = str(args.gems.resolve() / 'bin/bundle')
        self.repo = '127.0.0.1:5000/site'
        self.report = {'synthetic': True, 'production_changed': False, 'observations': [], 'cycles': [],
                       'harness_sha256': hashlib.sha256(Path(__file__).read_bytes()).hexdigest()}
        self.registry_binary = args.registry.resolve()
        self.mitigate = args.mitigate
        self.snapshotter = args.snapshotter
        self.disk_mib = args.disk_mib
        self.stop_sampling = threading.Event()
        self.sampler = None

    def spawn(self, name, args):
        with (self.root / f'{name}.log').open('wb') as log:
            p = subprocess.Popen(args, stdout=log, stderr=subprocess.STDOUT, env=self.env)
        self.processes.append(p)
        return p

    def docker(self, *args, **kw):
        return run(['docker', *args], env=self.env, **kw)

    def inspect(self, name):
        return json.loads(self.docker('inspect', name)[1])[0]

    def kamal(self, *args, check=True):
        code, out = run([self.bundle, 'exec', 'kamal', *args, '-c', str(self.root / 'deploy.yml')],
                        env=self.env, check=False, timeout=150)
        with (self.root / 'kamal.log').open('a') as f:
            f.write(f'\nCOMMAND {shlex.join(args)}\nEXIT {code}\n{out}')
        if check and code:
            raise RuntimeError(out[-6000:])
        return code, out

    def setup(self):
        run(['mount', '--make-rprivate', '/'])
        run(['ip', 'link', 'set', 'lo', 'up'])
        if self.disk_mib:
            if self.disk_mib < 1024 or self.disk_mib > 4096:
                raise RuntimeError('disposable disk must be 1024..4096 MiB')
            with (self.root / 'storage.ext4').open('wb') as f:
                f.truncate(self.disk_mib * 1024 * 1024)
            run(['mkfs.ext4', '-q', '-F', '-m', '0', str(self.root / 'storage.ext4')])
            (self.root / 'containerd').mkdir()
            run(['mount', '-o', 'loop', str(self.root / 'storage.ext4'), str(self.root / 'containerd')])
        (self.root / 'daemon.json').write_text('{}')
        (self.root / 'containerd.toml').write_text('version = 3\ndisabled_plugins = ["io.containerd.cri.v1.runtime", "io.containerd.cri.v1.images"]\n')
        self.spawn('containerd', ['containerd', '--config', str(self.root / 'containerd.toml'),
                  '--root', str(self.root / 'containerd'), '--state', str(self.root / 'ctrd-state'),
                  '--address', str(self.root / 'containerd.sock')])
        wait_until(lambda: (self.root / 'containerd.sock').exists())
        self.spawn('dockerd', ['dockerd', '--config-file', str(self.root / 'daemon.json'),
                   '--host', self.env['DOCKER_HOST'], '--data-root', str(self.root / 'docker'),
                   '--exec-root', str(self.root / 'docker-exec'), '--pidfile', str(self.root / 'dockerd.pid'),
                   '--containerd', str(self.root / 'containerd.sock'), f'--storage-driver={self.snapshotter}',
                   '--exec-opt=native.cgroupdriver=cgroupfs', '--cgroup-parent=/leapview-kamal-trial',
                   '--bridge=none', '--iptables=false', '--ip6tables=false', '--ip-forward=false',
                   '--ip-masq=false', '--userland-proxy=false', '--insecure-registry=127.0.0.1:5000'])
        wait_until(lambda: self.docker('info', check=False)[0] == 0)
        info = json.loads(self.docker('info', '--format', '{{json .}}')[1])
        assert info['DockerRootDir'] == str(self.root / 'docker')
        assert info['ServerVersion'].startswith('29.')
        assert ['driver-type', 'io.containerd.snapshotter.v1'] in info['DriverStatus']
        self.report['docker'] = {k: info[k] for k in ('ServerVersion', 'Driver', 'DriverStatus')}
        self.report['kamal'] = self.kamal_version()
        self.docker('network', 'create', '--subnet=172.30.0.0/24', 'kamal')
        for name in ('proxy', 'caddy'):
            self.docker('load', '-i', str(self.artifacts / f'{name}.tar'))
        (self.root / 'registry.yml').write_text(
            f'version: 0.1\nlog:\n  level: error\nstorage:\n  filesystem:\n    rootdirectory: {self.root}/registry\nhttp:\n  addr: 127.0.0.1:5000\n')
        self.registry = self.spawn('registry', [str(self.registry_binary), 'serve', str(self.root / 'registry.yml')])
        wait_until(lambda: HTTP.open('http://127.0.0.1:5000/v2/').status == 200)
        self.setup_ssh()
        self.write_config()
        if self.mitigate:
            self.install_identity_hook()
        self.docker('run', '--rm', '--network', 'none', 'caddy:2.10.2-alpine', 'caddy', 'version')
        print('PASS isolated Docker/containerd, SSH and container execution', flush=True)

    def kamal_version(self):
        return run([self.bundle, 'exec', 'kamal', 'version'], env=self.env)[1].strip()

    def setup_ssh(self):
        for key in ('host_key', 'client_key'):
            run(['ssh-keygen', '-q', '-t', 'ed25519', '-N', '', '-f', str(self.root / key)])
        wrapper = self.root / 'remote-shell'
        wrapper.write_text('#!/bin/bash\nset -eu\n' + ''.join(
            f'export {k}={shlex.quote(self.env[k])}\n' for k in ('HOME', 'DOCKER_HOST', 'DOCKER_CONFIG')) +
            'unset DOCKER_CONTEXT DOCKER_TLS_VERIFY DOCKER_CERT_PATH\ncd "$HOME"\nexec /bin/bash -c "$SSH_ORIGINAL_COMMAND"\n')
        wrapper.chmod(0o700)
        (self.root / 'authorized_keys').write_text((self.root / 'client_key.pub').read_text())
        (self.root / 'sshd_config').write_text(f'''Port 22222
ListenAddress 127.0.0.1
HostKey {self.root}/host_key
PidFile {self.root}/sshd.pid
AuthorizedKeysFile {self.root}/authorized_keys
StrictModes no
PermitRootLogin prohibit-password
PasswordAuthentication no
KbdInteractiveAuthentication no
UsePAM no
AllowUsers root
ForceCommand {wrapper}
''')
        Path('/run/sshd').mkdir(exist_ok=True)
        self.spawn('sshd', ['/usr/sbin/sshd', '-D', '-e', '-f', str(self.root / 'sshd_config')])
        pub = (self.root / 'host_key.pub').read_text().split()
        (self.root / 'known_hosts').write_text(f'[127.0.0.1]:22222 {pub[0]} {pub[1]}\n')
        (self.root / 'ssh_config').write_text(f'''Host 127.0.0.1
  User root
  Port 22222
  IdentityFile {self.root}/client_key
  IdentitiesOnly yes
  StrictHostKeyChecking yes
  UserKnownHostsFile {self.root}/known_hosts
''')
        wait_until(lambda: run(['ssh', '-F', str(self.root / 'ssh_config'), '127.0.0.1', 'docker info --format {{.DockerRootDir}}'], check=False)[0] == 0)
        _, remote_pwd = run(['ssh', '-F', str(self.root / 'ssh_config'), '127.0.0.1', 'pwd'])
        if remote_pwd.strip() != str(self.root):
            raise RuntimeError('SSH working directory escaped the private fixture')
        self.report['private_remote_working_directory_verified'] = True

    def write_config(self):
        (self.root / 'deploy.yml').write_text(f'''service: leapview-site-trial
image: site
minimum_version: 2.12.0
servers:
  web:
    hosts: [127.0.0.1]
    options:
      read-only: true
      security-opt: no-new-privileges=true
      cap-drop: ALL
      tmpfs: /tmp:rw,noexec,nosuid,size=16m
registry:
  server: 127.0.0.1:5000
  username: trial
  password: trial-not-a-secret
builder:
  arch: amd64
ssh:
  user: root
  port: 22222
  keys: [{self.root}/client_key]
  keys_only: true
  config: [{self.root}/ssh_config]
proxy:
  app_port: 8081
  host: leapview.test
  forward_headers: true
  healthcheck:
    path: /readyz
    interval: 1
    timeout: 1
  run:
    version: v0.9.2
    publish: false
retain_containers: 1
hooks_path: {self.root}/hooks
deploy_timeout: 5
drain_timeout: 1
stop_timeout: 1
logging:
  options:
    max-size: 1m
    max-file: 2
env:
  clear:
    TRIAL_IMAGE_REFERENCE: <%= ENV.fetch("TRIAL_IMAGE_REFERENCE", "unset").inspect %>
''')

    def image(self, version, unhealthy=False, arch='amd64', service='leapview-site-trial'):
        archive = io.BytesIO()
        with tarfile.open(fileobj=archive, mode='w') as tar:
            for name, data in [('fixture', (self.artifacts / 'fixture').read_bytes()),
                               ('version', version.encode()), ('payload', os.urandom(1024 * 1024))]:
                info = tarfile.TarInfo(name)
                info.size, info.mode, info.mtime = len(data), (0o755 if name == 'fixture' else 0o444), 0
                tar.addfile(info, io.BytesIO(data))
        tag = f'{self.repo}:{version}'
        self.docker('import', '--platform', f'linux/{arch}', '--change', f'LABEL service={service}', '--change', 'CMD ["/fixture"]',
                    '--change', 'USER 65532:65532', '--change', f'ENV TRIAL_UNHEALTHY={int(unhealthy)}', '-', tag,
                    data=archive.getvalue())
        self.docker('push', tag)
        identity = self.inspect(tag)
        self.report.setdefault('images', {})[version] = {'id': identity['Id'], 'digests': identity['RepoDigests']}
        record = {'schema': 1, 'version': version, 'service': 'leapview-site-trial', 'status': 'candidate',
                  'image_id': identity['Id'], 'reference': identity['RepoDigests'][0]}
        (self.root / 'records').mkdir(exist_ok=True)
        self.save_record(version, record)
        self.docker('image', 'rm', tag)
        return identity['RepoDigests'][0]

    def save_record(self, version, record):
        record.setdefault('runtime_config', (self.root / 'deploy.yml').read_text())
        record.setdefault('runtime_env', {'TRIAL_IMAGE_REFERENCE': record['reference']})
        record.setdefault('kamal_version', '2.12.0')
        path = self.root / 'records' / f'{version}.json'
        temp = path.with_suffix('.tmp')
        temp.write_text(json.dumps(record))
        temp.chmod(0o600)
        temp.replace(path)

    def install_identity_hook(self):
        (self.root / 'hooks').mkdir()
        hook = self.root / 'hooks/pre-app-boot'
        hook.write_text('#!/bin/bash\nset -eu\nexec python3 ' + shlex.quote(str(HERE / 'identity_guard.py')) +
                       ' --record "$TRIAL_ROOT/records/$KAMAL_VERSION.json" --version "$KAMAL_VERSION"\n')
        hook.chmod(0o700)
        for stage in ('pre-app-boot', 'post-app-boot'):
            path = self.root / 'hooks' / stage
            original = path.read_text() if path.exists() else '#!/bin/bash\nset -eu\n'
            # Pause before the guard for pre-boot, after traffic switch for post-boot.
            pause = (f'if test -f "$TRIAL_ROOT/pause-{stage}"; then\n'
                     f'  touch "$TRIAL_ROOT/reached-{stage}"\n'
                     '  while true; do sleep 1; done\nfi\n')
            path.write_text(original.replace('set -eu\n', 'set -eu\n' + pause, 1))
            path.chmod(0o700)

    def deploy(self, version, *, unhealthy=False):
        identity = self.image(version, unhealthy)
        self.env['TRIAL_IMAGE_REFERENCE'] = identity
        code, output = self.kamal('deploy', '--skip-push', '--version', version, check=False)
        if unhealthy:
            assert code != 0, f'unhealthy {version} unexpectedly deployed'
        elif code:
            raise RuntimeError(output[-7000:])
        return identity

    def served(self):
        ip = self.inspect('kamal-proxy')['NetworkSettings']['Networks']['kamal']['IPAddress']
        with HTTP.open(Request(f'http://{ip}/build.json', headers={'Host': 'leapview.test'}), timeout=5) as r:
            return json.load(r)

    def snapshot(self):
        containers = [json.loads(x) for x in self.docker('ps', '-a', '--filter', 'label=service=leapview-site-trial',
                                                        '--format', '{{json .}}')[1].splitlines()]
        usage = self.disk_usage()
        stat = os.statvfs(self.root)
        return {'containers': containers, 'runtime_allocated_bytes': usage,
                'filesystem_available_bytes': stat.f_bavail * stat.f_frsize,
                'filesystem_available_inodes': stat.f_favail,
                'containerd_filesystem_available_bytes': os.statvfs(self.root / 'containerd').f_bavail * os.statvfs(self.root / 'containerd').f_frsize}

    def disk_usage(self):
        return sum(int(line.split()[0]) for line in run(
            ['du', '-sx', '--block-size=1', str(self.root / 'containerd'), str(self.root / 'docker')])[1].splitlines())

    def sample(self):
        while not self.stop_sampling.wait(.5):
            try:
                used = self.disk_usage()
                self.report['peak_allocated_bytes'] = max(self.report.get('peak_allocated_bytes', 0), used)
            except Exception as exc:
                self.report.setdefault('sampling_errors', []).append(str(exc))

    def setup_caddy(self, check_fixture=True):
        (self.root / 'Caddyfile').write_text("""{
 admin off
 auto_https disable_redirects
}
https://leapview.test {
 tls internal
 encode gzip
 reverse_proxy kamal-proxy:80
}
https://www.leapview.test {
 tls internal
 redir https://leapview.test{uri} permanent
}
""")
        self.docker('run', '-d', '--name', 'trial-caddy', '--network', 'kamal',
                    '--log-opt', 'max-size=1m', '--log-opt', 'max-file=2',
                    '-v', f'{self.root}/Caddyfile:/etc/caddy/Caddyfile:ro', 'caddy:2.10.2-alpine')
        wait_until(lambda: self.docker('cp', 'trial-caddy:/data/caddy/pki/authorities/local/root.crt',
                                       str(self.root / 'ca.crt'), check=False)[0] == 0)
        ip = self.inspect('trial-caddy')['NetworkSettings']['Networks']['kamal']['IPAddress']
        command = ['curl', '--noproxy', '*', '--fail', '--silent', '--show-error', '--cacert', str(self.root / 'ca.crt')]
        probe = command + ['--resolve', f'leapview.test:443:{ip}', 'https://leapview.test/build.json']
        # A root CA file can exist before the leaf certificate is ready.
        wait_until(lambda: run(probe, check=False)[0] == 0)
        code, body = run(probe)
        data = json.loads(body)
        if check_fixture:
            assert data['host'] == 'leapview.test' and data['forwarded_proto'] == 'https', data
        _, headers = run(command + ['--head', '--resolve', f'www.leapview.test:443:{ip}', 'https://www.leapview.test/docs'])
        assert '301' in headers and 'location: https://leapview.test/docs' in headers.lower(), headers
        self.report['caddy_tls_www'] = True
        if check_fixture:
            self.report['caddy_tls_host_forwarding_www'] = True
        print('PASS Caddy TLS and www redirect through private proxy', flush=True)
        return ip

    def exercise(self):
        self.sampler = threading.Thread(target=self.sample, daemon=True)
        self.sampler.start()
        for index in range(1, 11):
            version = f'good-{index:02d}'
            identity = self.deploy(version)
            actual = self.served()
            assert actual['version'] == version and actual['image_reference'] == identity, actual
            record = json.loads((self.root / 'records' / f'{version}.json').read_text())
            record['status'] = 'verified'
            self.save_record(version, record)
            time.sleep(1)
            snap = self.snapshot()
            assert len(snap['containers']) == min(index, 2), snap
            self.report['cycles'].append({'version': version, **snap})
            print(f'PASS healthy deployment {index}/10; {len(snap["containers"])} site containers retained', flush=True)
        self.setup_caddy()
        if self.mitigate:
            self.identity_rejections()
        for index in range(1, 4):
            self.deploy(f'bad-{index:02d}', unhealthy=True)
            assert self.served()['version'] == 'good-10'
            print(f'PASS failed candidate {index}/3 leaves good-10 serving', flush=True)
            if self.mitigate:
                # Experimental narrow remedy: exact recorded attempt, never a broad prune.
                name = f'leapview-site-trial-web-bad-{index:02d}'
                failed = self.inspect(name)
                assert not failed['State']['Running'] and failed['State']['Status'] == 'exited'
                assert failed['Config']['Labels']['service'] == 'leapview-site-trial'
                assert failed['Image'] == self.report['images'][f'bad-{index:02d}']['id']
                self.docker('rm', failed['Id'])

        self.report['before_native_prune'] = self.snapshot()
        self.kamal('prune', 'all')
        self.report['after_native_prune'] = self.snapshot()
        containers = self.report['after_native_prune']['containers']
        prior_kept = any('good-09' in c['Names'] for c in containers)
        self.report['observations'].append({'case': 'native-prune-preserves-verified-prior', 'passed': prior_kept})
        print(f'RESULT native prune preserves verified prior good-09: {prior_kept}', flush=True)
        assert self.served()['version'] == 'good-10'
        if self.mitigate:
            if self.disk_mib:
                self.disk_full_case()
                prior_version = 'good-10'
            else:
                prior_version = 'good-09'
            assert prior_kept, 'narrow failed-attempt cleanup did not preserve prior'
            self.registry.terminate()
            self.registry.wait(timeout=10)
            # Native rollback reuses the NEW runner's config, including its identity.
            self.env['TRIAL_IMAGE_REFERENCE'] = 'deliberately-wrong-candidate-settings'
            config_file = self.root / 'deploy.yml'
            config_file.write_text(config_file.read_text().replace('    options:', '    cmd: /fixture candidate-settings\n    options:', 1))
            self.kamal('rollback', prior_version)
            assert self.served()['version'] == prior_version
            self.report['native_rollback_uses_candidate_settings'] = self.served()['image_reference'] == 'deliberately-wrong-candidate-settings'
            # Reconstruct from the HOST record over SSH, not controller memory or registry.
            _, serialized = run(['ssh', '-F', str(self.root / 'ssh_config'), '127.0.0.1',
                                 'cat ' + shlex.quote(str(self.root / 'records' / f'{prior_version}.json'))])
            record = json.loads(serialized)
            validate(record, self.inspect(f'{self.repo}:{prior_version}'), rollback=True)
            config_file.write_text(record['runtime_config'])
            self.env.pop('TRIAL_IMAGE_REFERENCE', None)
            self.env.update(record['runtime_env'])
            self.kamal('rollback', prior_version)
            served = self.served()
            assert served['version'] == prior_version and served['image_reference'] == record['reference']
            running = self.inspect(f'leapview-site-trial-web-{prior_version}')
            assert running['Image'] == record['image_id'] and running['State']['Running']
            assert running['Config']['Cmd'] == ['/fixture']
            self.report['offline_rollback_reloads_host_runtime_record'] = True
            self.report['offline_rollback_with_prior_record'] = True
            print('PASS registry-offline rollback with prior version identity restored', flush=True)
            self.registry = self.spawn('registry-restarted', [str(self.registry_binary), 'serve', str(self.root / 'registry.yml')])
            wait_until(lambda: HTTP.open('http://127.0.0.1:5000/v2/').status == 200)
            self.interruption_cases()
            self.cleanup_failure_case()
            self.foreign_alias_case()
            self.same_version_case()


    def identity_rejections(self):
        before = self.served()
        for version, arch, service in [('wrong-arch', 'arm64', 'leapview-site-trial'),
                                       ('wrong-service', 'amd64', 'different-service')]:
            reference = self.image(version, arch=arch, service=service)
            self.env['TRIAL_IMAGE_REFERENCE'] = reference
            code, _ = self.kamal('deploy', '--skip-push', '--version', version, check=False)
            assert code != 0 and self.served() == before
        reference = self.image('wrong-identity')
        path = self.root / 'records/wrong-identity.json'
        original = json.loads(path.read_text())
        self.save_record('wrong-identity', {**original, 'reference': '127.0.0.1:5000/site@sha256:' + '0' * 64})
        self.env['TRIAL_IMAGE_REFERENCE'] = reference
        code, _ = self.kamal('deploy', '--skip-push', '--version', 'wrong-identity', check=False)
        assert code != 0 and self.served() == before
        path.unlink()
        code, _ = self.kamal('app', 'boot', '--version', 'wrong-identity', check=False)
        assert code != 0 and self.served() == before
        self.save_record('wrong-identity', original)
        self.docker('tag', f'{self.repo}:good-09', f'{self.repo}:wrong-identity')
        code, _ = self.kamal('app', 'boot', '--version', 'wrong-identity', check=False)
        assert code != 0 and self.served() == before
        self.report['identity_negative_cases'] = ['wrong-architecture', 'wrong-service', 'mismatched-record', 'missing-record', 'changed-tag']
        print('PASS wrong platform/service, corrupt/missing identity record and changed tag blocked before boot', flush=True)

    def interruption_cases(self):
        for stage in ('pre-app-boot', 'post-app-boot'):
            version = 'interrupted-' + stage
            reference = self.image(version)
            self.env['TRIAL_IMAGE_REFERENCE'] = reference
            before = self.served()
            pause = self.root / f'pause-{stage}'
            reached = self.root / f'reached-{stage}'
            pause.touch()
            with (self.root / f'{version}.log').open('wb') as log:
                process = subprocess.Popen([self.bundle, 'exec', 'kamal', 'deploy', '--skip-push', '--version', version,
                                            '-c', str(self.root / 'deploy.yml')], env=self.env, stdout=log,
                                           stderr=subprocess.STDOUT, start_new_session=True)
            self.processes.append(process)
            wait_until(lambda: reached.exists(), seconds=45)
            if stage == 'pre-app-boot':
                assert self.served() == before
                self.image('concurrent-attempt')
                code, output = self.kamal('deploy', '--skip-push', '--version', 'concurrent-attempt', check=False)
                assert code != 0 and 'lock' in output.lower() and self.served() == before
                assert self.inspect(f'{self.repo}:concurrent-attempt')
                self.report['native_lock_blocks_activation_but_not_pull'] = True
            else:
                assert self.served()['version'] == version
            os.killpg(process.pid, signal.SIGKILL)
            process.wait(timeout=10)
            assert process.returncode == -signal.SIGKILL
            pause.unlink()
            # This harness created and killed the sole controller, at a known local
            # hook boundary. Never blindly release an unknown production lock.
            _, status = self.kamal('lock', 'status')
            assert version in status
            self.kamal('lock', 'release')
            if stage == 'pre-app-boot':
                self.kamal('deploy', '--skip-push', '--version', version)
            else:
                # Observe/accept the already-switched version; do not boot it again.
                actual = self.inspect(f'leapview-site-trial-web-{version}')
                assert actual['Image'] == self.report['images'][version]['id']
                assert self.served()['image_reference'] == reference
                record = json.loads((self.root / 'records' / f'{version}.json').read_text())
                record['status'] = 'verified'
                self.save_record(version, record)
                self.kamal('prune', 'all')
            assert self.served()['version'] == version
            self.report.setdefault('interruption_recovery', []).append(stage)
            print(f'PASS interrupted {stage}: inspect state, release only known-dead owner, recover', flush=True)

    def cleanup_failure_case(self):
        reference = self.image('maintenance-failure')
        self.env['TRIAL_IMAGE_REFERENCE'] = reference
        wrapper = self.root / 'remote-shell'
        original = wrapper.read_text()
        wrapper.write_text(original.replace('exec /bin/bash',
            'if [[ "$SSH_ORIGINAL_COMMAND" == *"docker image prune"* ]]; then echo "injected cleanup failure" >&2; exit 74; fi\nexec /bin/bash'))
        try:
            code, output = self.kamal('deploy', '--skip-push', '--version', 'maintenance-failure', check=False)
            assert code != 0 and 'injected cleanup failure' in output
            assert self.served()['version'] == 'maintenance-failure'
            self.report['cleanup_failure_reports_live_new_version'] = True
        finally:
            wrapper.write_text(original)
        self.kamal('prune', 'all')
        assert self.served()['version'] == 'maintenance-failure'
        print('PASS cleanup failure after activation: new site stays live; maintenance retry succeeds', flush=True)

    def foreign_alias_case(self):
        alias = '127.0.0.1:5000/foreign:keep'
        current = f'{self.repo}:maintenance-failure'
        self.docker('tag', current, alias)
        inventory = json.loads(self.docker('image', 'inspect', current)[1])
        try:
            validate_cleanup_scope(inventory)
        except ValueError:
            self.report['foreign_alias_guard_blocks_cleanup'] = True
        else:
            raise AssertionError('ambiguous foreign alias was not rejected')
        assert self.inspect(alias)
        # Demonstrate native scope behavior on a disposable alias, not a real service.
        self.kamal('prune', 'all')
        retained = self.docker('image', 'inspect', alias, check=False)[0] == 0
        self.report['native_prune_preserves_foreign_alias'] = retained
        assert self.inspect('trial-caddy')['State']['Running']
        assert self.served()['version'] == 'maintenance-failure'
        print(f'RESULT native cleanup preserves foreign alias of service-labeled image: {retained}', flush=True)

    def same_version_case(self):
        prior = 'interrupted-post-app-boot'
        self.env['TRIAL_IMAGE_REFERENCE'] = self.report['images']['maintenance-failure']['digests'][0]
        record = json.loads((self.root / 'records/maintenance-failure.json').read_text())
        record['status'] = 'verified'
        self.save_record('maintenance-failure', record)
        image = self.inspect(f'{self.repo}:maintenance-failure')
        container = self.inspect('leapview-site-trial-web-maintenance-failure')
        def identities():
            return sorted((c['ID'], c['Image'], c['Names'], c['State']) for c in self.snapshot()['containers'])
        before = identities()
        assert verified_noop(record, image, container, self.served())
        assert identities() == before
        assert self.inspect(f'leapview-site-trial-web-{prior}')
        self.report['verified_same_version_noop_preserves_prior'] = True
        # Compare the no-op policy with invoking native deploy again.
        self.kamal('deploy', '--skip-push', '--version', 'maintenance-failure')
        assert self.served()['version'] == 'maintenance-failure'
        kept = self.docker('inspect', f'leapview-site-trial-web-{prior}', check=False)[0] == 0
        self.report['native_same_version_keeps_distinct_prior'] = kept
        print(f'RESULT native same-version deployment retains distinct prior: {kept}', flush=True)

    def disk_full_case(self):
        reference = self.image('after-enospc')
        filler = self.root / 'containerd/intentional-enospc-fixture'
        before = self.served()
        try:
            with filler.open('wb', buffering=0) as f:
                chunk = b'X' * (1024 * 1024)
                while True:
                    f.write(chunk)
        except OSError as exc:
            if exc.errno != errno.ENOSPC:
                raise
        try:
            self.env['TRIAL_IMAGE_REFERENCE'] = reference
            code, output = self.kamal('deploy', '--skip-push', '--version', 'after-enospc', check=False)
            assert code != 0 and ('no space left' in output.lower() or 'insufficient' in output.lower()), output[-2000:]
            assert self.served() == before
            self.report['enospc_kept_live'] = True
        finally:
            filler.unlink()
        self.kamal('deploy', '--skip-push', '--version', 'after-enospc')
        assert self.served()['version'] == 'after-enospc'
        self.report['enospc_retry_succeeded'] = True
        print('PASS real ENOSPC on disposable ext4 image; live survives and retry succeeds', flush=True)

    def close(self):
        self.stop_sampling.set()
        if self.sampler:
            self.sampler.join(timeout=10)
        (self.root / 'report.json').write_text(json.dumps(self.report, indent=2) + '\n')
        # Namespace init exit will reap all descendants; terminate daemons politely first.
        for process in reversed(self.processes):
            if process.poll() is None:
                process.terminate()
        for process in reversed(self.processes):
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()


def main():
    p = argparse.ArgumentParser()
    for name in ('state', 'artifacts', 'gems', 'registry'):
        p.add_argument('--' + name, type=Path, required=True)
    p.add_argument('--mitigate', action='store_true')
    p.add_argument('--snapshotter', choices=('overlayfs', 'native'), default='overlayfs')
    p.add_argument('--disk-mib', type=int, default=0)
    t = Trial(p.parse_args())
    try:
        t.setup()
        t.exercise()
    except BaseException as exc:
        t.report['error'] = str(exc)
        raise
    finally:
        t.close()


if __name__ == '__main__':
    main()
