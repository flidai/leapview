"""Bounded Linux-owned process-tree cleanup, including detached browser groups."""
import ctypes
import os
from pathlib import Path
import signal
import subprocess
import time


def process_snapshot():
    processes = {}
    for path in Path('/proc').glob('[0-9]*/stat'):
        try:
            pid = int(path.parent.name)
            fields = path.read_text().rsplit(')', 1)[1].split()
            status = (path.parent / 'status').read_text()
            rss = next((int(line.split()[1]) for line in status.splitlines() if line.startswith('VmRSS:')), 0)
            processes[pid] = {'parent': int(fields[1]), 'group': int(fields[2]),
                              'start': fields[19], 'state': fields[0], 'rssKiB': rss}
        except (FileNotFoundError, ProcessLookupError, PermissionError):
            continue
    return processes


def descendants(snapshot, root):
    owned = {root}
    while True:
        expanded = owned | {pid for pid, row in snapshot.items() if row['parent'] in owned}
        if expanded == owned:
            return expanded - {root}
        owned = expanded


def run_owned(command, *, stdout, env=None, timeout=150, peak_rss_limit_kib=4 * 1024 * 1024):
    # Chromium may create its own process group. A process-group kill alone
    # misses it when Bun exits; subreaping preserves our descendant ownership.
    if ctypes.CDLL(None, use_errno=True).prctl(36, 1, 0, 0, 0) != 0:
        raise ValueError('Linux child-subreaper ownership is required for bounded cleanup')
    process = subprocess.Popen(command, stdout=stdout, stderr=subprocess.STDOUT,
                               env=env, start_new_session=True)
    started = time.monotonic()
    owned = {}
    peak = 0
    reason = None
    while True:
        snapshot = process_snapshot()
        for pid in descendants(snapshot, os.getpid()):
            owned[pid] = snapshot[pid]['start']
        living = {pid: row for pid, row in snapshot.items() if owned.get(pid) == row['start'] and row['state'] != 'Z'}
        peak = max(peak, sum(row['rssKiB'] for row in living.values()))
        code = process.poll()
        if peak > peak_rss_limit_kib:
            reason = 'process_tree_rss_stop'
            break
        if code is not None:
            # The pre-poll snapshot may still show the immediate parent live.
            # Re-observe adopted descendants after terminal status is known.
            terminal = process_snapshot()
            for pid in descendants(terminal, os.getpid()):
                owned[pid] = terminal[pid]['start']
            survivors = {pid for pid, row in terminal.items() if pid != process.pid
                         and owned.get(pid) == row['start'] and row['state'] != 'Z'}
            if survivors:
                reason = 'owned_descendants_survived_parent'
            break
        if time.monotonic() - started >= timeout:
            reason = 'wall_timeout'
            break
        time.sleep(0.1)

    # Cleanup is bounded on normal exits too. Detached groups remain owned
    # through adoption and start-time identity; PID reuse cannot target others.
    for termination in (signal.SIGTERM, signal.SIGKILL):
        until = time.monotonic() + 1
        while True:
            snapshot = process_snapshot()
            for pid in descendants(snapshot, os.getpid()):
                owned[pid] = snapshot[pid]['start']
            living = {pid: row for pid, row in snapshot.items() if owned.get(pid) == row['start'] and row['state'] != 'Z'}
            for pid in living:
                try:
                    os.kill(pid, termination)
                except ProcessLookupError:
                    pass
            # Popen owns the immediate child; reap only adopted descendants.
            process.poll()
            for pid in owned.keys() - {process.pid}:
                try:
                    os.waitpid(pid, os.WNOHANG)
                except ChildProcessError:
                    pass
            if not living or time.monotonic() >= until:
                break
            time.sleep(0.05)
    snapshot = process_snapshot()
    residual = [pid for pid, row in snapshot.items() if owned.get(pid) == row['start'] and row['state'] != 'Z']
    if residual:
        raise ValueError(f'owned process cleanup failed: {residual}')
    code = process.wait(timeout=1)
    return {'exitCode': code, 'terminationReason': reason, 'observedPeakProcessTreeRSSKiB': peak,
            'rssStopKiB': peak_rss_limit_kib, 'cleanupComplete': True,
            'accounting': '100ms Linux process-tree RSS observation; brief peaks may fall between observations'}
