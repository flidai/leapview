"""Restore only the module's PostgreSQL directory from its root-owned provider.

No recovery-owner-owned tree, path, tool or provider configuration is accepted.
The durable intent owns a fresh staging inode before pgBackRest runs as postgres.
Readback is paused and loopback-only; every completed restore is stopped.
"""
import argparse
import datetime
import fcntl
import json
import os
from pathlib import Path
import pwd
import re
import shlex
import signal
import stat
import subprocess
import sys
import tempfile
import time

import postgres_promote as promotion

digest = promotion.digest
INTENT = "postgresql-restore-intent.json"
RECEIPT = "postgresql-restore.json"
MATERIALIZED = "postgresql-restore-materialized"
FIELDS = promotion.FIELDS | {"backupSet", "occurrenceID", "operationID"}


def request(raw):
    value = promotion.decode(raw)
    if not isinstance(value, dict) or set(value) != FIELDS:
        raise ValueError("unsupported module restore request")
    promotion.request(json.dumps({key: value[key] for key in promotion.FIELDS}))
    if not isinstance(value["backupSet"], str) or not re.fullmatch(r"[0-9]{8}-[0-9]{6}F(_[0-9]{8}-[0-9]{6}[DI])?", value["backupSet"]):
        raise ValueError("exact backup set required")
    for key in ("occurrenceID", "operationID"):
        if not isinstance(value[key], str) or not re.fullmatch(r"[A-Za-z0-9_.:-]{1,255}", value[key]):
            raise ValueError("exact restore operation required")
    return value


class ModuleRestore(promotion.Promotion):
    def __init__(self, state, data, machine, generation, config, bin_dir, systemctl, runuser, pgbackrest, port,
                 provider_config, credentials, execute=promotion.run, service_uid=None, service_gid=None):
        super().__init__(state, data, machine, generation, config, bin_dir, systemctl, runuser, pgbackrest, port,
                         execute=execute, service_uid=service_uid, service_gid=service_gid)
        self.provider_config, self.credentials = Path(provider_config), Path(credentials)

    def _provider_binding(self):
        config = self.provider_config.resolve(strict=True)
        if not str(config).startswith("/nix/store/") or not config.is_file():
            raise ValueError("immutable module provider configuration required")
        info = self.credentials.lstat()
        if (not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_nlink != 1
                or stat.S_IMODE(info.st_mode) != 0o640 or not 0 < info.st_size <= 65536):
            raise ValueError("root-owned retained provider credentials required")
        for parent in self.credentials.parents:
            info = parent.lstat()
            if not stat.S_ISDIR(info.st_mode) or info.st_uid != 0 or info.st_mode & 0o022:
                raise ValueError("untrusted provider credential parent")
        if [path for path in self.credentials.parent.glob("*.conf")] != [self.credentials]:
            raise ValueError("exact retained provider credential include required")
        # The digest stays inside the private root intent, never in receipts.
        return digest("leapview-module-postgresql-provider:", config.read_text() + "\x00" + self.credentials.read_text())

    def _location(self, wanted, create_parent=False, allow_missing_parent=False):
        if (self.machine.read_text().strip() != wanted["replacementMachineID"]
                or wanted["dataDirectoryDigest"] != digest("leapview-managed-recovery-data:", str(self.data))
                or not self.data.is_absolute() or self.data.parent.resolve() != self.data.parent):
            raise ValueError("restore differs from configured replacement and data directory")
        for parent in self.data.parents:
            if parent == self.data.parent and not parent.exists() and not parent.is_symlink() and (create_parent or allow_missing_parent):
                continue
            info = parent.lstat()
            if not stat.S_ISDIR(info.st_mode) or info.st_uid not in (0, os.geteuid(), self.service_uid) or info.st_mode & 0o022:
                raise ValueError("module-owned PostgreSQL parent required")
        if create_parent and not self.data.parent.exists():
            if self._properties() != {"ActiveState": "inactive", "SubState": "dead", "MainPID": "0"}:
                raise ValueError("fresh stopped module parent required")
            # Conditions run before systemd's default StateDirectory creation.
            # Create only this new empty module-selected parent, never import or
            # change ownership/permissions on an existing directory or tree.
            self.data.parent.mkdir(mode=0o700)
            os.chown(self.data.parent, self.service_uid, self.service_gid)

    def _auto_config(self, wanted):
        original = super()._auto_config(wanted)
        command = shlex.join([self.pgbackrest, "--config=" + str(self.provider_config),
                              "--config-include-path=" + str(self.credentials.parent),
                              "--stanza=default", "--pg1-path=" + str(self.data), "archive-get"]) + " '%f' '%p'"
        return "".join(line if not line.startswith("restore_command = ") else
                       "restore_command = '" + command.replace("'", "''") + "'\n" for line in original.splitlines(keepends=True))

    def _revalidate(self, binding, allow_missing_parent=False):
        self._location(binding["request"], allow_missing_parent=allow_missing_parent)
        if (self._module_identity() != binding["moduleGeneration"]
                or self._provider_binding() != binding["providerDigest"]):
            raise ValueError("module generation or provider changed during restore")

    def _stage(self, binding):
        if (self.state / INTENT).exists() or (self.state / INTENT).is_symlink():
            retained = promotion.decode(self._owned(self.state / INTENT, 0o600))
            if set(retained) != {"binding", "stage", "device", "inode"} or retained["binding"] != binding:
                raise ValueError("another operation, provider or module owns restore intent")
            stage = Path(retained["stage"])
            if stage.parent != self.data.parent or not stage.name.startswith(".leapview-module-restore-"):
                raise ValueError("invalid root-owned restore staging intent")
            existing = self.data if self.data.exists() or self.data.is_symlink() else stage
            info = existing.lstat()
            modes = (0o700, 0o750) if existing == self.data else (0o700,)
            if (not stat.S_ISDIR(info.st_mode) or info.st_uid != self.service_uid or info.st_gid != self.service_gid
                    or stat.S_IMODE(info.st_mode) not in modes
                    or (info.st_dev, info.st_ino) != (retained["device"], retained["inode"])):
                raise ValueError("root-controlled staging identity changed")
            return stage
        if self.data.exists() or self.data.is_symlink() or self._properties() != {"ActiveState": "inactive", "SubState": "dead", "MainPID": "0"}:
            raise ValueError("fresh stopped module with absent PostgreSQL data required")
        stage = Path(tempfile.mkdtemp(prefix=".leapview-module-restore-", dir=self.data.parent))
        os.chown(stage, self.service_uid, self.service_gid)
        info = stage.stat()
        self._write(self.state / INTENT, json.dumps({"binding": binding, "stage": str(stage), "device": info.st_dev, "inode": info.st_ino}, sort_keys=True) + "\n", 0o600)
        return stage

    def _receipt(self, binding):
        value = promotion.decode(self._owned(self.state / RECEIPT, 0o640))
        expected = self._public(binding)
        if (set(value) != set(expected) | {"replayedThroughLSN", "restoredAt"}
                or any(type(value[key]) is not type(item) or value[key] != item for key, item in expected.items())
                or not isinstance(value["replayedThroughLSN"], str)
                or not re.fullmatch(r"[0-9A-F]{1,8}/[0-9A-F]{1,8}", value["replayedThroughLSN"])
                or not isinstance(value["restoredAt"], str)):
            raise ValueError("root restore receipt differs from exact provider intent")
        datetime.datetime.strptime(value["restoredAt"], "%Y-%m-%dT%H:%M:%SZ")
        return value

    def _public(self, binding):
        wanted = binding["request"]
        return {"schemaVersion": 1, "kind": "leapview/module-postgresql-restore", "status": "restored-stopped",
                "targetID": wanted["targetID"], "recoverySetID": wanted["recoverySetID"], "frontierDigest": wanted["frontierDigest"],
                "occurrenceID": wanted["occurrenceID"], "operationID": wanted["operationID"], "backupSet": wanted["backupSet"],
                "replacementMachineIDDigest": digest("leapview-managed-recovery-qualification:", wanted["replacementMachineID"]),
                "systemIdentifier": wanted["systemIdentifier"], "targetLSN": wanted["targetLSN"], "timeline": wanted["timeline"],
                "dataDirectoryDigest": wanted["dataDirectoryDigest"], "port": self.port,
                "moduleGenerationDigest": digest("leapview-managed-recovery-module:", binding["moduleGeneration"]),
                "configurationDigest": digest("leapview-managed-recovery-config:", self.config.read_text()),
                "activationQualified": False, "fullManagedProfileQualified": False}

    def _provider(self, arguments, lock):
        # A root child supervises the provider and retains the inherited flock
        # if the invoking controller/helper dies. A retry cannot race its writes.
        child = os.fork()
        if child == 0:
            result = 1
            try:
                # SSH/controller process-group teardown must not kill this
                # lock holder while its separately-sessioned provider writes.
                os.setsid()
                process = subprocess.Popen(arguments, start_new_session=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                                           env={"PATH": os.environ.get("PATH", ""), "LC_ALL": "C"})
                try:
                    result = process.wait(timeout=1800)
                except BaseException:
                    os.killpg(process.pid, signal.SIGKILL)
                    process.wait()
                    raise
            except BaseException:
                result = 1
            finally:
                os.close(lock)
                os._exit(0 if result == 0 else 1)
        _, status = os.waitpid(child, 0)
        if not os.WIFEXITED(status) or os.WEXITSTATUS(status) != 0:
            raise subprocess.CalledProcessError(1, arguments)

    def apply(self, wanted, *, action="restore"):
        wanted = request(json.dumps(wanted))
        if action not in ("fresh", "restore", "check", "start-readback", "stop-readback"):
            raise ValueError("unsupported fixed restore action")
        info = self.state.lstat()
        if (not stat.S_ISDIR(info.st_mode) or stat.S_IMODE(info.st_mode) != 0o750
                or info.st_uid != os.geteuid() or self.state.resolve(strict=True) != self.state):
            raise ValueError("root-owned restore state required")
        binding = {"request": wanted, "moduleGeneration": self._module_identity(), "providerDigest": self._provider_binding(),
                   "dataDirectory": str(self.data), "configuration": str(self.config), "port": self.port}
        # Share the promotion lock: restore/readback and promotion cannot race.
        lock = os.open(self.state / "lock", os.O_WRONLY | os.O_CREAT | os.O_NOFOLLOW, 0o600)
        started = False
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            fresh = not any((self.state / name).exists() or (self.state / name).is_symlink()
                            for name in (INTENT, RECEIPT, MATERIALIZED))
            self._location(wanted, create_parent=action == "restore" and fresh, allow_missing_parent=action == "fresh")
            if action == "fresh":
                if (not fresh or self.data.exists() or self.data.is_symlink()
                        or self._properties() != {"ActiveState": "inactive", "SubState": "dead", "MainPID": "0"}):
                    raise ValueError("fresh absent stopped module restore required")
                self._revalidate(binding, allow_missing_parent=True)
                return self._public(binding) | {"status": "absent-stopped"}
            if action != "restore" and not (self.state / RECEIPT).is_file():
                raise ValueError("exact completed root restore receipt required")
            stage = self._stage(binding)
            if not self.data.exists():
                if action != "restore" or self._properties() != {"ActiveState": "inactive", "SubState": "dead", "MainPID": "0"}:
                    raise ValueError("root restore cannot replace running or missing completed data")
                self._provider([self.runuser, "-u", "postgres", "--", self.pgbackrest, "--config=" + str(self.provider_config),
                             "--config-include-path=" + str(self.credentials.parent), "--stanza=default", "--set=" + wanted["backupSet"],
                             "--pg1-path=" + str(stage), "--type=lsn", "--target=" + wanted["targetLSN"],
                             "--target-timeline=" + str(wanted["timeline"]), "--target-action=pause", "--no-link-all", "--delta",
                             "--tablespace-map-all=" + str(stage / ".managed-tablespaces"), "--log-level-console=off", "--log-level-file=off", "restore"], lock)
                self._revalidate(binding)
                # pgBackRest-created data is service-owned; no caller-owned tree
                # or path is imported into this privileged boundary.
                original = self.data
                try:
                    self.data = stage
                    self._identity(wanted | {"dataDirectoryDigest": digest("leapview-managed-recovery-data:", str(stage))})
                finally:
                    self.data = original
                os.rename(stage, self.data)
                descriptor = os.open(self.data.parent, os.O_RDONLY | os.O_DIRECTORY)
                try:
                    os.fsync(descriptor)
                finally:
                    os.close(descriptor)
            self._identity(wanted)
            completed = self._receipt(binding) if (self.state / RECEIPT).exists() or (self.state / RECEIPT).is_symlink() else None
            if action == "check":
                if self._properties() != {"ActiveState": "inactive", "SubState": "dead", "MainPID": "0"}:
                    raise ValueError("completed module restore must remain stopped")
                self._revalidate(binding)
                return completed
            if action == "stop-readback":
                if self._properties().get("ActiveState") == "active":
                    self._observed(wanted, completed["replayedThroughLSN"])
                    self.execute(self.systemctl, "stop", "postgresql.service")
                if self._properties() != {"ActiveState": "inactive", "SubState": "dead", "MainPID": "0"}:
                    raise ValueError("readback stop did not complete")
                self._revalidate(binding)
                return completed
            if self._properties().get("ActiveState") == "inactive":
                self._revalidate(binding)
                self._write(self.data / "postgresql.auto.conf", self._auto_config(wanted), 0o644)
                # The opt-in module condition blocks upstream auto-initdb until
                # trusted provider materialization and safe configuration exist.
                self._write(self.state / MATERIALIZED, "materialized\n", 0o600)
                started = True
                self.execute(self.systemctl, "start", "postgresql.service")
            observed = self._observed(wanted, completed["replayedThroughLSN"] if completed else None)
            replay = tuple(int(part, 16) for part in observed["lsn"].split("/")) if observed["lsn"] else ()
            target = tuple(int(part, 16) for part in wanted["targetLSN"].split("/"))
            if observed["recovery"] is not True or replay < target:
                raise ValueError("root restored readback requires exact paused recovery")
            if action == "start-readback":
                self._revalidate(binding)
                return completed
            self.execute(self.systemctl, "stop", "postgresql.service")
            started = False
            if self._properties() != {"ActiveState": "inactive", "SubState": "dead", "MainPID": "0"}:
                raise ValueError("root restore did not leave PostgreSQL stopped")
            if completed:
                self._revalidate(binding)
                return completed
            value = self._public(binding) | {"replayedThroughLSN": observed["lsn"], "restoredAt": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())}
            self._revalidate(binding)
            self._write(self.state / RECEIPT, json.dumps(value, sort_keys=True) + "\n", 0o640)
            self._revalidate(binding)
            return value
        except BaseException:
            if started:
                try:
                    self.execute(self.systemctl, "stop", "postgresql.service")
                except BaseException:
                    pass
            raise
        finally:
            os.close(lock)


def main():
    parser = argparse.ArgumentParser()
    for name in ("state", "data", "config", "bin", "systemctl", "runuser", "pgbackrest", "provider-config", "credentials"):
        parser.add_argument("--" + name, required=True)
    parser.add_argument("--port", type=int, required=True)
    parser.add_argument("--action", choices=("fresh", "restore", "check", "start-readback", "stop-readback"), default="restore")
    args = parser.parse_args()
    if os.geteuid() != 0:
        raise SystemExit("module restore requires the installed root helper")
    try:
        user = pwd.getpwnam("postgres")
        value = ModuleRestore(args.state, args.data, "/etc/machine-id", "/run/current-system", args.config,
                              args.bin, args.systemctl, args.runuser, args.pgbackrest, args.port,
                              args.provider_config, args.credentials, service_uid=user.pw_uid, service_gid=user.pw_gid).apply(
                                  request(sys.stdin.buffer.read(16385)), action=args.action)
        print(json.dumps(value, sort_keys=True))
    except Exception:
        raise SystemExit("configured PostgreSQL restore is unqualified; keep application activation closed") from None


if __name__ == "__main__":
    main()
