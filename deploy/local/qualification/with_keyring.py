#!/usr/bin/env python3
"""Run Linux lifecycle qualification with a disposable native Secret Service."""

from __future__ import annotations

import os
from contextlib import contextmanager
from pathlib import Path
import secrets
import signal
import subprocess
import sys
import tempfile
import time


@contextmanager
def graceful_interruptions(request: Path):
    """Keep the bus and keyring alive until the qualification has cleaned up."""
    previous = {}
    for signum in (signal.SIGINT, signal.SIGTERM):
        previous[signum] = signal.signal(signum, lambda _signum, _frame: request.touch())
    try:
        yield
    finally:
        for signum, handler in previous.items():
            signal.signal(signum, handler)


def stop(process: subprocess.Popen, process_group: bool = False) -> None:
    if process_group:
        # A reaped leader does not imply its command's descendants have exited.
        # Address the owned group even after poll() reports leader completion.
        try:
            os.killpg(process.pid, signal.SIGTERM)
        except ProcessLookupError:
            process.wait()
            return
        deadline = time.monotonic() + 5
        while time.monotonic() < deadline:
            process.poll()
            try:
                os.killpg(process.pid, 0)
            except ProcessLookupError:
                process.wait()
                return
            time.sleep(0.05)
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        process.wait()
        return
    if process.poll() is None:
        process.terminate()
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait()


def run_qualification(command: list[str], request: Path) -> int:
    process = subprocess.Popen(command, start_new_session=True)
    deadline = None
    try:
        while process.poll() is None:
            if request.exists() and deadline is None:
                # Python handles SIGINT with KeyboardInterrupt, allowing the
                # harness finally block to reset its runtime and retain evidence.
                os.killpg(process.pid, signal.SIGINT)
                deadline = time.monotonic() + 30
            if deadline is not None and time.monotonic() >= deadline:
                stop(process, process_group=True)
                break
            try:
                process.wait(timeout=0.1)
            except subprocess.TimeoutExpired:
                pass
        code = process.returncode
        return code if code >= 0 else 128 - code
    finally:
        stop(process, process_group=True)


def session(command: list[str], request: Path) -> int:
    control = Path(os.environ["XDG_RUNTIME_DIR"]) / "keyring"
    control.mkdir(mode=0o700)
    # Keep the daemon in the foreground so we own and reap its exact process.
    # --unlock creates an unlocked login keyring without any desktop prompt.
    daemon = subprocess.Popen(
        ["gnome-keyring-daemon", "--foreground", "--unlock", "--control-directory", str(control)],
        stdin=subprocess.PIPE,
        stdout=subprocess.DEVNULL,
    )
    try:
        assert daemon.stdin is not None
        daemon.stdin.write(secrets.token_hex(32).encode())
        daemon.stdin.close()
        deadline = time.monotonic() + 10
        while not (control / "control").exists():
            if daemon.poll() is not None or time.monotonic() >= deadline:
                raise RuntimeError("temporary native keyring did not become ready")
            time.sleep(0.05)
        subprocess.run(
            ["gnome-keyring-daemon", "--start", "--components=secrets", "--control-directory", str(control)],
            check=True,
            stdout=subprocess.DEVNULL,
            timeout=10,
        )
        if request.exists():
            return 130
        return run_qualification(command, request)
    finally:
        stop(daemon)


def main(command: list[str]) -> int:
    if not command:
        raise RuntimeError("usage: with_keyring.py <command> [arguments ...]")
    if command[0] == "--session":
        request = Path(os.environ["HOME"]) / "interrupt-requested"
        with graceful_interruptions(request):
            return session(command[1:], request)
    with tempfile.TemporaryDirectory(prefix="leapview-qualification-keyring-") as directory:
        root = Path(directory)
        environment = dict(os.environ)
        for key in ("DBUS_SESSION_BUS_ADDRESS", "DBUS_SESSION_BUS_PID", "DBUS_STARTER_ADDRESS", "DBUS_STARTER_BUS_TYPE", "GNOME_KEYRING_CONTROL", "GNOME_KEYRING_PID", "SSH_AUTH_SOCK"):
            environment.pop(key, None)
        environment["HOME"] = directory
        for key, name in (("XDG_CONFIG_HOME", "config"), ("XDG_DATA_HOME", "data"), ("XDG_CACHE_HOME", "cache"), ("XDG_RUNTIME_DIR", "runtime")):
            child = root / name
            child.mkdir(mode=0o700)
            environment[key] = str(child)
        with graceful_interruptions(root / "interrupt-requested"):
            process = subprocess.Popen(
                ["dbus-run-session", "--", sys.executable, str(Path(__file__).resolve()), "--session", *command],
                env=environment,
                start_new_session=True,
            )
            try:
                return process.wait()
            finally:
                stop(process, process_group=True)


if __name__ == "__main__":
    try:
        sys.exit(main(sys.argv[1:]))
    except (OSError, RuntimeError, subprocess.SubprocessError) as error:
        print(f"isolated qualification keyring: {error}", file=sys.stderr)
        sys.exit(1)
