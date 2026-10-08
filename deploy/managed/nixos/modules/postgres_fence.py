"""Persistently fence an enrolled PostgreSQL host before replacement recovery.

Installed by the database NixOS module. The marker is also a PostgreSQL startup
condition, so a controller crash or host reboot cannot undo a completed fence.
There is deliberately no automatic unfence operation.
"""
import argparse
import fcntl
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys
import tempfile

FIELDS = {"schemaVersion", "targetID", "recoverySetID", "frontierDigest",
          "clusterIdentity", "machineID", "systemIdentifier"}
MARKER = "postgresql-fence.json"


def request(raw):
    def unique(pairs):
        value = {}
        for key, item in pairs:
            if key in value:
                raise ValueError("duplicate fence request field")
            value[key] = item
        return value
    if len(raw) > 16384:
        raise ValueError("fence request exceeds limit")
    value = json.loads(raw, object_pairs_hook=unique)
    if not isinstance(value, dict) or set(value) != FIELDS or type(value["schemaVersion"]) is not int or value["schemaVersion"] != 1:
        raise ValueError("unsupported fence request")
    for key in FIELDS - {"schemaVersion"}:
        item = value[key]
        if not isinstance(item, str) or not item or item != item.strip() or len(item) > 255 or any(ord(c) < 32 for c in item):
            raise ValueError("invalid fence identity")
    for key, pattern in {"machineID": r"[0-9a-f]{32}", "systemIdentifier": r"[1-9][0-9]{0,19}",
                         "frontierDigest": r"sha256:[0-9a-f]{64}"}.items():
        if not re.fullmatch(pattern, value[key]):
            raise ValueError("invalid fence identity")
    if int(value["systemIdentifier"]) > 2**64 - 1:
        raise ValueError("invalid PostgreSQL system identifier")
    return value


def run(*args):
    # stdout is parsed locally; PostgreSQL/systemd diagnostics are not receipts.
    return subprocess.run(args, check=True, capture_output=True, text=True, timeout=120,
                          env={"PATH": os.environ.get("PATH", ""), "LC_ALL": "C"}).stdout


class Fence:
    def __init__(self, state, data, machine, control, systemctl, execute=run):
        self.state, self.data, self.machine = Path(state), Path(data), Path(machine)
        self.control, self.systemctl, self.execute = control, systemctl, execute
        self.system, self.profiles = Path("/run/current-system"), Path("/nix/var/nix/profiles")

    def _boot_guard(self):
        # A pre-fence generation could otherwise bypass the marker on rollback.
        # Never garbage-collect or rewrite generations implicitly to fix this.
        systems = [self.system, self.profiles / "system", *self.profiles.glob("system-*-link")]
        required = "!/var/lib/leapview-recovery/postgresql-fence.json"
        checked = set()
        while systems:
            system = systems.pop().resolve(strict=True)
            if system in checked:
                continue
            checked.add(system)
            unit = system / "etc/systemd/system/postgresql.service"
            conditions = []
            for source in [unit, *sorted(unit.with_name(unit.name + ".d").glob("*.conf"))]:
                section = ""
                for raw in source.read_text().splitlines():
                    line = raw.strip()
                    if line.startswith("["):
                        section = line
                    key, separator, value = line.partition("=")
                    if section == "[Unit]" and separator and key.strip() == "ConditionPathExists":
                        if value.strip():
                            conditions.append(value.strip())
                        else:
                            conditions.clear()
            if required not in conditions:
                raise ValueError("an installed or retained generation lacks the original-writer boot guard")
            systems.extend((system / "specialisation").glob("*"))

    def _cluster(self):
        fields = {}
        for line in self.execute(self.control, str(self.data)).splitlines():
            key, separator, value = line.partition(":")
            if separator:
                fields[key.strip()] = value.strip()
        return fields

    def _identity(self, wanted):
        if self.machine.read_text().strip() != wanted["machineID"]:
            raise ValueError("fence request names another host")
        if self.data.resolve(strict=True) != self.data or not self.data.is_dir():
            raise ValueError("PostgreSQL data directory is not canonical")
        cluster = self._cluster()
        if cluster.get("Database system identifier") != wanted["systemIdentifier"]:
            raise ValueError("fence request names another PostgreSQL cluster")
        return cluster

    def _stopped(self, wanted):
        cluster = self._identity(wanted)
        properties = dict(line.split("=", 1) for line in self.execute(
            self.systemctl, "show", "postgresql.service", "--property=ActiveState,SubState,MainPID").splitlines())
        if (properties != {"ActiveState": "inactive", "SubState": "dead", "MainPID": "0"}
                or cluster.get("Database cluster state") != "shut down"
                or (self.data / "postmaster.pid").exists()):
            raise ValueError("original PostgreSQL writer is not stopped")

    def apply(self, wanted, *, check=False):
        wanted = request(json.dumps(wanted))
        self._boot_guard()
        self._identity(wanted)  # Wrong host/cluster must never create a fence.
        info = self.state.lstat()
        if (not stat.S_ISDIR(info.st_mode) or stat.S_IMODE(info.st_mode) != 0o700
                or info.st_uid != os.geteuid() or self.state.resolve(strict=True) != self.state):
            raise ValueError("fence state must be a private owned canonical directory")
        lock = os.open(self.state / "lock", os.O_WRONLY | os.O_CREAT | os.O_NOFOLLOW, 0o600)
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            self._identity(wanted)
            marker = self.state / MARKER
            if marker.exists() or marker.is_symlink():
                info = marker.lstat()
                if not stat.S_ISREG(info.st_mode) or stat.S_IMODE(info.st_mode) != 0o600 or info.st_uid != os.geteuid():
                    raise ValueError("invalid durable fence")
                if request(marker.read_bytes()) != wanted:
                    raise ValueError("another recovery operation owns the durable fence")
            elif check:
                raise ValueError("durable fence is absent")
            else:
                descriptor, temporary = tempfile.mkstemp(prefix=".fence-", dir=self.state)
                try:
                    with os.fdopen(descriptor, "w") as output:
                        json.dump(wanted, output, sort_keys=True)
                        output.write("\n")
                        output.flush()
                        os.fsync(output.fileno())
                    os.replace(temporary, marker)
                    directory = os.open(self.state, os.O_RDONLY | os.O_DIRECTORY)
                    try:
                        os.fsync(directory)
                    finally:
                        os.close(directory)
                finally:
                    if os.path.exists(temporary):
                        os.unlink(temporary)
            if not check:
                self.execute(self.systemctl, "stop", "postgresql.service")
            self._stopped(wanted)
            self._boot_guard()
            return {"identity": wanted, "postgresqlStopped": True, "restartFenced": True}
        finally:
            os.close(lock)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--state", required=True)
    parser.add_argument("--data", required=True)
    parser.add_argument("--control", required=True)
    parser.add_argument("--systemctl", required=True)
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()
    if os.geteuid() != 0:
        raise SystemExit("PostgreSQL recovery fencing requires root")
    try:
        result = Fence(args.state, args.data, "/etc/machine-id", args.control, args.systemctl).apply(
            request(sys.stdin.buffer.read(16385)), check=args.check)
    except (ValueError, OSError, subprocess.SubprocessError):
        raise SystemExit("PostgreSQL recovery fence failed; keep replacement admission closed") from None
    print(json.dumps(result, sort_keys=True))


if __name__ == "__main__":
    main()
