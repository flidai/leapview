"""Host-local command ownership. EOF revokes a controller; it never proves quiescence.

No stale takeover: an unresolved owner journal needs explicit audited recovery.
All subprocesses belong to this supervisor, independently of their SSH clients.
"""
import fcntl
import json
import os
from pathlib import Path
import re
import select
import signal
import socket
import stat
import subprocess
import sys
import threading
import time

ROOT = Path('/var/lib/leapview-site/kamal')
LOCKS = [Path('/opt/leapview-site/reconcile.lock'), Path('/opt/leapview-site/deploy.lock')]


def write_json(path, value):
    temporary = path.with_suffix('.next')
    fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, 'w') as stream:
        json.dump(value, stream); stream.flush(); os.fsync(stream.fileno())
    temporary.replace(path)
    fd = os.open(path.parent, os.O_DIRECTORY)
    try: os.fsync(fd)
    finally: os.close(fd)


def serve(root, attempt, locks=LOCKS):
    if not re.fullmatch('[a-f0-9]{32}', attempt): raise ValueError('invalid attempt')
    metadata = root.lstat()
    if not stat.S_ISDIR(metadata.st_mode) or metadata.st_uid != os.getuid() or metadata.st_mode & 0o077:
        raise ValueError('protected operator state directory required')
    held = []
    for path in locks:
        fd = os.open(path, os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
        held.append(fd)
        fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    journal = root / 'owner.json'
    if journal.exists():
        raise ValueError('unresolved owner: explicit recovery required; never clear state on SSH disconnect')
    state = {'attempt': attempt, 'controller': os.environ.get('SSH_CONNECTION', 'local qualification'),
             'pid': os.getpid(), 'boot_id': Path('/proc/sys/kernel/random/boot_id').read_text().strip(),
             'status': 'active', 'work': [], 'started': time.time()}
    capacity = {}
    ready_path = root / 'ready.json'
    if ready_path.exists():
        for device, budget in json.loads(ready_path.read_text()).get('capacity', {}).items():
            paths = budget.get('paths', [])
            if paths:
                stats = os.statvfs(paths[0])
                capacity[device] = {'path': paths[0], 'start_free_bytes': stats.f_bavail * stats.f_frsize,
                                    'start_free_inodes': stats.f_favail, 'incremental_peak_bytes': 0,
                                    'incremental_peak_inodes': 0}
    state['capacity_observed'] = capacity
    write_json(journal, state)
    endpoint = root / 'operator.sock'
    endpoint.unlink(missing_ok=True)
    server = socket.socket(socket.AF_UNIX)
    server.bind(str(endpoint)); os.chmod(endpoint, 0o600); server.listen()
    mutex = threading.Lock()
    revoked = threading.Event()
    workers = []

    def worker(client, request):
        with mutex:
            if any(work['exit_code'] is None for work in state['work']):
                revoked.set(); client.close(); return
            if revoked.is_set() or request.get('attempt') != attempt:
                client.close(); return
            entry = {'command': request['command'], 'started': time.time(), 'exit_code': None}
            state['work'].append(entry)
            # Journal before spawn: a crash in between requires explicit inspection.
            write_json(journal, state)
            process = subprocess.Popen(['bash', '-c', request['command']], stdout=subprocess.PIPE,
                                       stderr=subprocess.PIPE, start_new_session=True, pass_fds=tuple(held))
            entry['pid'] = process.pid
            entry['start_ticks'] = Path('/proc/' + str(process.pid) + '/stat').read_text().split()[21]
            write_json(journal, state)
        stdout, stderr = process.communicate()
        with mutex:
            entry.update(exit_code=process.returncode, finished=time.time())
            write_json(journal, state)
        import base64
        try:
            client.sendall(json.dumps({'code': process.returncode,
                'stdout': base64.b64encode(stdout).decode(), 'stderr': base64.b64encode(stderr).decode()}).encode() + b'\n')
        except OSError:
            # The work reply was lost; even success now needs state reconciliation.
            revoked.set()
        finally: client.close()

    # SIGHUP must revoke, not kill the lock owner while a Docker client survives.
    signal.signal(signal.SIGHUP, lambda *_: revoked.set())
    signal.signal(signal.SIGTERM, lambda *_: revoked.set())
    print(json.dumps({'attempt': attempt}), flush=True)
    clean = False
    try:
        while not revoked.is_set():
            ready, _, _ = select.select([sys.stdin, server], [], [], .2)
            with mutex:
                for measurement in capacity.values():
                    stats = os.statvfs(measurement['path'])
                    measurement['incremental_peak_bytes'] = max(measurement['incremental_peak_bytes'], measurement['start_free_bytes'] - stats.f_bavail * stats.f_frsize)
                    measurement['incremental_peak_inodes'] = max(measurement['incremental_peak_inodes'], measurement['start_free_inodes'] - stats.f_favail)
            if sys.stdin in ready:
                line = sys.stdin.readline()
                if line.strip() == 'finish': clean = True
                revoked.set(); break
            if server in ready:
                client, _ = server.accept()
                client.settimeout(10)
                try:
                    request = json.loads(client.makefile('rb').readline(1024 * 1024))
                    if request.get('attempt') != attempt: raise ValueError('revoked controller')
                    client.settimeout(None)
                    thread = threading.Thread(target=worker, args=(client, request))
                    workers.append(thread); thread.start()
                except Exception:
                    client.close(); raise
    finally:
        revoked.set(); server.close(); endpoint.unlink(missing_ok=True)
        with mutex:
            state['status'] = 'unresolved'; write_json(journal, state)
        for thread in workers: thread.join()
        # Even after all clients exit, Docker daemon state may be uncertain.
        # Only a normal controller finish removes ownership, never an EOF.
        if clean:
            archive = root / 'attempts'; archive.mkdir(mode=0o700, exist_ok=True)
            state['status'] = 'finished'; write_json(archive / (attempt + '.json'), state)
            journal.unlink()
        for fd in held: os.close(fd)


def client(root, attempt, command):
    import base64
    with socket.socket(socket.AF_UNIX) as connection:
        connection.connect(str(root / 'operator.sock'))
        connection.sendall(json.dumps({'attempt': attempt, 'command': command}).encode() + b'\n')
        response = json.loads(connection.makefile('rb').readline())
    sys.stdout.buffer.write(base64.b64decode(response['stdout']))
    sys.stderr.buffer.write(base64.b64decode(response['stderr']))
    return response['code']


if __name__ == '__main__':
    if sys.argv[1] == 'serve': serve(ROOT, sys.argv[2])
    elif sys.argv[1] == 'exec': sys.exit(client(ROOT, sys.argv[2], sys.argv[3]))
    else: raise ValueError('unsupported supervisor operation')
