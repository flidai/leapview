"""Real Docker regression in fresh mount, network and PID namespaces.

Requires root, Docker, iproute2, iptables and curl. The private daemon uses a
unique temporary data root and a socket hidden from the host by a private /run.
The existing daemon, containers and host network remain outside this fixture.
"""

import argparse
import json
import os
from pathlib import Path
import subprocess
import sys
import time
import tempfile

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("image", type=Path)
    parser.add_argument("--workdir", type=Path, required=True)
    parser.add_argument("--docker-package", type=Path, required=True)
    args = parser.parse_args()
    POLICY = str(Path(__file__).resolve().parent.parent / "modules/docker-firewall.sh")
    IMAGE = str(args.image.resolve())
    DOCKER = str(args.docker_package.resolve() / "bin/docker")
    DOCKERD = str(args.docker_package.resolve() / "bin/dockerd")
    if not Path(IMAGE).is_file():
        raise SystemExit("A built port-probe image archive is required")

    def run(*args, check=True):
        p = subprocess.run(args, capture_output=True, text=True, timeout=30)
        if check and p.returncode:
            raise RuntimeError(f'{args}: {p.stderr}')
        return p

    if os.geteuid() != 0 or os.getpid() != 1 or {x['ifname'] for x in json.loads(run('ip', '-j', 'link', 'show').stdout)} != {'lo'}:
        raise SystemExit('Requires root in fresh mount, network and PID namespaces')
    run('mount', '--make-rprivate', '/')
    run('mount', '-t', 'tmpfs', 'tmpfs', '/run')
    Path('/run/netns').mkdir()
    run('ip', 'link', 'set', 'lo', 'up')
    for name, iface, subnet in [('public', 'eth-public', '198.18.0'), ('private', 'eth-private', '192.168.2')]:
        run('ip', 'netns', 'add', name)
        run('ip', 'link', 'add', iface, 'type', 'veth', 'peer', 'name', name + '-peer')
        run('ip', 'link', 'set', name + '-peer', 'netns', name)
        run('ip', 'addr', 'add', subnet + '.1/24', 'dev', iface)
        run('ip', 'link', 'set', iface, 'up')
        run('ip', 'netns', 'exec', name, 'ip', 'addr', 'add', subnet + '.2/24', 'dev', name + '-peer')
        run('ip', 'netns', 'exec', name, 'ip', 'link', 'set', name + '-peer', 'up')
        run('ip', 'netns', 'exec', name, 'ip', 'link', 'set', 'lo', 'up')
        run('ip', 'netns', 'exec', name, 'ip', 'route', 'add', 'default', 'via', subnet + '.1')
    scratch = tempfile.TemporaryDirectory(prefix="daemon-", dir=args.workdir.resolve())
    ROOT = Path(scratch.name)
    log = open(ROOT / 'daemon.log', 'a')
    configuration = ROOT / 'daemon.json'
    configuration.write_text('{}')
    def start():
        daemon = subprocess.Popen([DOCKERD, '--config-file=' + str(configuration), '--host=unix:///run/test-docker.sock', '--data-root=' + str(ROOT / 'data'), '--exec-root=/run/test-docker', '--pidfile=/run/test-docker.pid', '--firewall-backend=iptables', '--userland-proxy=false', '--exec-opt=native.cgroupdriver=cgroupfs'], stdout=log, stderr=log)
        deadline = time.monotonic() + 30
        while run(DOCKER, '-H', 'unix:///run/test-docker.sock', 'info', check=False).returncode:
            if time.monotonic() > deadline or daemon.poll() is not None:
                if daemon.poll() is None:
                    daemon.terminate()
                    daemon.wait(timeout=30)
                raise RuntimeError('Isolated Docker daemon did not start:\n' + (ROOT / 'daemon.log').read_text()[-8000:])
            time.sleep(.1)
        return daemon

    def docker(*args):
        return run(DOCKER, '-H', 'unix:///run/test-docker.sock', *args)

    def assert_ports():
        for namespace, address in [('public', '198.18.0.1'), ('private', '192.168.2.1')]:
            for port in [80, 443, 8080, 9090]:
                p = run('ip', 'netns', 'exec', namespace, 'curl', '--noproxy', '*', '-s', '--connect-timeout', '1', '--max-time', '2', '-o', '/dev/null', f'http://{address}:{port}', check=False)
                actual = p.returncode == 0
                expected = namespace == 'public' and port in [80, 443]
                assert actual == expected, f'{namespace}:{port}: expected {expected}, got {actual}'
                print(f'{namespace}:{port}: {"allowed" if actual else "denied"}', flush=True)

    run('bash', POLICY, 'eth-public')
    daemon = start()
    server = None
    try:
        print(docker('version', '--format', '{{.Server.Version}}').stdout, flush=True)
        docker('load', '-i', IMAGE)
        docker('network', 'create', 'probe')
        docker('run', '-d', '--name', 'port-probe', '--restart', 'always', '--network', 'probe', '-p', '80:8080', '-p', '443:8080', '-p', '8080:8080', '-p', '9090:80', 'managed-port-probe:fixture')
        deadline = time.monotonic() + 30
        while run('curl', '--noproxy', '*', '-s', '--max-time', '1', '-o', '/dev/null', 'http://127.0.0.1:8080', check=False).returncode:
            if time.monotonic() > deadline:
                raise RuntimeError('Probe did not start')
            time.sleep(.1)
        assert_ports()
        run('bash', POLICY, 'eth-public')
        assert_ports()
        daemon.terminate()
        daemon.wait(timeout=30)
        daemon = start()
        deadline = time.monotonic() + 30
        while run('curl', '--noproxy', '*', '-s', '--max-time', '1', '-o', '/dev/null', 'http://127.0.0.1:8080', check=False).returncode:
            if time.monotonic() > deadline:
                raise RuntimeError('Probe was not restored')
            time.sleep(.1)
        assert_ports()
        server = subprocess.Popen(['ip', 'netns', 'exec', 'public', sys.executable, '-m', 'http.server', '8000'], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        deadline = time.monotonic() + 10
        while run('curl', '--noproxy', '*', '-s', '--max-time', '1', '-o', '/dev/null', 'http://198.18.0.2:8000', check=False).returncode:
            if time.monotonic() > deadline:
                raise RuntimeError('Outbound-response fixture did not start')
            time.sleep(.1)
        docker('exec', 'port-probe', '/bin/wget', '-q', '-O', '/dev/null', 'http://198.18.0.2:8000')
        print('Real Docker custom-bridge ingress, policy reload, daemon restart and outbound response checks passed', flush=True)
    finally:
        if server:
            server.terminate()
            server.wait()
        if daemon.poll() is None:
            run(DOCKER, '-H', 'unix:///run/test-docker.sock', 'rm', '-f', 'port-probe', check=False)
            daemon.terminate()
            daemon.wait(timeout=30)
        log.close()
        scratch.cleanup()


if __name__ == "__main__":
    main()
