"""Promote one exact restored PostgreSQL frontier into a loopback-only service.

Only the database module installs this root helper. It uses the module's pinned
tools and reviewed configuration, never a restored PostgreSQL configuration.
The durable intent precedes promotion and permits reconciliation after a crash;
neither its receipt nor promotion admits application traffic.
"""
import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import pwd
import shlex
import stat
import subprocess
import sys
import tempfile
import time

FIELDS = {"schemaVersion", "targetID", "recoverySetID", "frontierDigest",
          "sourceMachineID", "replacementMachineID", "systemIdentifier", "targetLSN", "timeline", "dataDirectoryDigest"}
INTENT = "postgresql-promotion-intent.json"
RECEIPT = "postgresql-promotion.json"


def decode(raw):
    def unique(pairs):
        value = {}
        for key, item in pairs:
            if key in value:
                raise ValueError("duplicate promotion field")
            value[key] = item
        return value
    if len(raw) > 16384:
        raise ValueError("promotion input exceeds limit")
    return json.loads(raw, object_pairs_hook=unique)


def request(raw):
    value = decode(raw)
    if not isinstance(value, dict) or set(value) != FIELDS or type(value["schemaVersion"]) is not int or value["schemaVersion"] != 1:
        raise ValueError("unsupported promotion request")
    for key in FIELDS - {"schemaVersion", "timeline"}:
        item = value[key]
        if not isinstance(item, str) or not item or item != item.strip() or len(item) > 255 or any(ord(c) < 32 for c in item):
            raise ValueError("invalid promotion identity")
    for key, pattern in {"sourceMachineID": r"[0-9a-f]{32}", "replacementMachineID": r"[0-9a-f]{32}",
                         "systemIdentifier": r"[1-9][0-9]{0,19}", "frontierDigest": r"sha256:[0-9a-f]{64}",
                         "dataDirectoryDigest": r"sha256:[0-9a-f]{64}",
                         "targetLSN": r"[0-9A-F]{1,8}/[0-9A-F]{1,8}"}.items():
        if not re.fullmatch(pattern, value[key]):
            raise ValueError("invalid promotion identity")
    if (int(value["systemIdentifier"]) > 2**64 - 1 or value["sourceMachineID"] == value["replacementMachineID"]
            or "0" * 32 in (value["sourceMachineID"], value["replacementMachineID"])
            or type(value["timeline"]) is not int or not 0 < value["timeline"] < 2**32):
        raise ValueError("invalid promotion identity")
    return value


def digest(domain, value):
    return "sha256:" + hashlib.sha256((domain + value).encode()).hexdigest()


def run(*args):
    # No provider stderr or inherited PGOPTIONS/PGHOST enters a public receipt.
    return subprocess.run(args, check=True, capture_output=True, text=True, timeout=120,
                          env={"PATH": os.environ.get("PATH", ""), "LC_ALL": "C"}).stdout


class Promotion:
    def __init__(self, state, data, machine, generation, config, bin_dir, systemctl, runuser, pgbackrest, port, execute=run, service_uid=None, service_gid=None):
        self.state, self.data, self.machine = Path(state), Path(data), Path(machine)
        self.generation, self.config = Path(generation), Path(config)
        self.bin, self.systemctl, self.runuser, self.pgbackrest = Path(bin_dir), systemctl, runuser, pgbackrest
        self.port, self.execute = port, execute
        self.service_uid = os.geteuid() if service_uid is None else service_uid
        self.service_gid = os.getegid() if service_gid is None else service_gid

    def _owned(self, path, mode):
        info = path.lstat()
        if (not stat.S_ISREG(info.st_mode) or stat.S_IMODE(info.st_mode) != mode
                or info.st_uid != os.geteuid() or info.st_nlink != 1):
            raise ValueError("untrusted promotion state")
        return path.read_bytes()

    def _write(self, path, value, mode):
        descriptor, temporary = tempfile.mkstemp(prefix=".promotion-", dir=path.parent)
        try:
            with os.fdopen(descriptor, "w") as output:
                os.fchmod(output.fileno(), mode)
                os.fchown(output.fileno(), os.geteuid(), self.state.stat().st_gid)
                output.write(value)
                output.flush()
                os.fsync(output.fileno())
            os.replace(temporary, path)
            directory = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
            try:
                os.fsync(directory)
            finally:
                os.close(directory)
        finally:
            if os.path.exists(temporary):
                os.unlink(temporary)

    def _identity(self, wanted):
        if self.machine.read_text().strip() != wanted["replacementMachineID"]:
            raise ValueError("promotion names another replacement host")
        if self.data.resolve(strict=True) != self.data or not self.data.is_dir():
            raise ValueError("noncanonical configured data directory")
        info = self.data.stat()
        # NixOS PostgreSQL >=11 uses 0750 for its trusted postgres group;
        # neither writable group access nor any other group is admissible.
        if (info.st_uid != self.service_uid or info.st_gid != self.service_gid
                or stat.S_IMODE(info.st_mode) not in (0o700, 0o750)):
            raise ValueError("restored data directory requires the configured private service owner")
        if wanted["dataDirectoryDigest"] != digest("leapview-managed-recovery-data:", str(self.data)):
            raise ValueError("promotion request names another configured data directory")
        # The provider restore already rejects links. Recheck before the first
        # production service start; the one module-owned config link is allowed
        # only when it points exactly at the reviewed immutable configuration.
        for root, directories, files in os.walk(self.data, followlinks=False):
            for name in directories + files:
                path = Path(root) / name
                if path.is_symlink() and (path != self.data / "postgresql.conf" or path.resolve(strict=True) != self.config):
                    raise ValueError("restored layout contains an untrusted link")
        if list((self.data / "pg_tblspc").iterdir()):
            raise ValueError("nondefault restored tablespace layout")
        fields = {}
        for line in self.execute(str(self.bin / "pg_controldata"), str(self.data)).splitlines():
            key, separator, value = line.partition(":")
            if separator:
                fields[key.strip()] = value.strip()
        if fields.get("Database system identifier") != wanted["systemIdentifier"]:
            raise ValueError("promotion names another restored cluster")
        return fields

    def _properties(self):
        return dict(line.split("=", 1) for line in self.execute(
            self.systemctl, "show", "postgresql.service", "--property=ActiveState,SubState,MainPID").splitlines())

    def _sql(self, sql):
        return self.execute(self.runuser, "-u", "postgres", "--", str(self.bin / "psql"),
                            "-X", "-A", "-t", "-v", "ON_ERROR_STOP=1", "-d",
                            "host=/run/postgresql port=" + str(self.port) + " dbname=postgres "
                            "options='-c search_path=pg_catalog -c session_preload_libraries= -c local_preload_libraries='",
                            "-c", sql).strip()

    def _observed(self, wanted, paused_lsn=None):
        # systemd reapplies StateDirectoryMode on start. Authenticate its actual
        # resulting ownership and permissions before observing/promoting SQL.
        self._identity(wanted)
        properties = self._properties()
        if (properties.get("ActiveState") != "active" or properties.get("SubState") != "running"
                or not properties.get("MainPID", "").isdigit() or int(properties["MainPID"]) <= 0):
            raise ValueError("configured PostgreSQL service is not running")
        # This peer-authenticated connection is confined to the module service's
        # Unix socket. Also prove its actual config/data/ingress identities.
        raw = self._sql("SELECT json_build_object('system', (pg_control_system()).system_identifier::text, "
                        "'recovery', pg_is_in_recovery(), 'paused', CASE WHEN pg_is_in_recovery() THEN pg_is_wal_replay_paused() ELSE false END, "
                        "'lsn', pg_last_wal_replay_lsn()::text, 'timeline', (pg_control_checkpoint()).timeline_id, "
                        "'target', current_setting('recovery_target_lsn'), 'targetTimeline', current_setting('recovery_target_timeline'), "
                        "'data', current_setting('data_directory'), 'config', current_setting('config_file'), "
                        "'listen', current_setting('listen_addresses'), 'shared', current_setting('shared_preload_libraries'), "
                        "'session', current_setting('session_preload_libraries'), 'local', current_setting('local_preload_libraries'), "
                        "'unsafeRoleSettings', EXISTS (SELECT 1 FROM pg_db_role_setting s CROSS JOIN LATERAL unnest(s.setconfig) v "
                        "WHERE split_part(v,'=',1) IN ('session_preload_libraries','local_preload_libraries') AND split_part(v,'=',2) <> ''))")
        value = decode(raw.encode())
        if (value.get("system") != wanted["systemIdentifier"] or value.get("data") != str(self.data)
                or Path(value.get("config", "")).resolve(strict=True) != self.config
                or value.get("listen") != "127.0.0.1" or value.get("unsafeRoleSettings") is not False
                or any(value.get(key) != "" for key in ("shared", "session", "local"))):
            raise ValueError("configured service differs from exact private restored frontier")
        if value.get("recovery") is True:
            if (value.get("paused") is not True or value.get("timeline") != wanted["timeline"]
                    or value.get("target") != wanted["targetLSN"] or value.get("targetTimeline") != str(wanted["timeline"])
                    or not isinstance(value.get("lsn"), str) or not re.fullmatch(r"[0-9A-F]{1,8}/[0-9A-F]{1,8}", value["lsn"])):
                raise ValueError("restored recovery is not paused at the selected timeline")
            if paused_lsn is not None and value["lsn"] != paused_lsn:
                raise ValueError("restored paused replay position changed")
        else:
            if (value.get("recovery") is not False or type(value.get("timeline")) is not int
                    or value["timeline"] != wanted["timeline"] + 1 or paused_lsn is None
                    or value.get("lsn") not in (None, paused_lsn)):
                raise ValueError("restored service lacks an authenticated promotion boundary")
            # PostgreSQL forgets pg_last_wal_replay_lsn after a primary restart.
            # The new timeline history retains the actual fork point, including
            # the record-end replay LSN which can differ from target_lsn.
            history = self.data / "pg_wal" / (format(value["timeline"], "08X") + ".history")
            info = history.lstat()
            if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or info.st_size > 16384:
                raise ValueError("promotion ancestry is not a bounded regular history file")
            entries = [line.split() for line in history.read_text().splitlines() if line.strip() and not line.lstrip().startswith("#")]
            if not entries or len(entries[-1]) < 2 or entries[-1][:2] != [str(wanted["timeline"]), paused_lsn]:
                raise ValueError("actual promotion ancestry differs from the authenticated paused frontier")
        return value

    def _auto_config(self, wanted):
        # No restored include/preload/archive command is carried into service.
        # pgBackRest and its configuration are the database module's reviewed
        # installation, not an operator-supplied executable or shell fragment.
        command = shlex.join([self.pgbackrest, "--stanza=default", "--pg1-path=" + str(self.data), "archive-get"]) + " '%f' '%p'"
        quote = lambda value: "'" + str(value).replace("'", "''") + "'"
        settings = {"listen_addresses": "127.0.0.1", "shared_preload_libraries": "", "session_preload_libraries": "",
                    "local_preload_libraries": "", "max_logical_replication_workers": "0", "recovery_target_lsn": wanted["targetLSN"],
                    "recovery_target_timeline": wanted["timeline"], "recovery_target_action": "pause", "restore_command": command}
        return "# root-owned exact recovery promotion; application ingress remains closed\n" + "".join(
            key + " = " + quote(value) + "\n" for key, value in settings.items())

    def _module_identity(self):
        generation = str(self.generation.resolve(strict=True))
        if (not generation.startswith("/nix/store/") or not str(self.config).startswith("/nix/store/")
                or self.config.resolve(strict=True) != self.config):
            raise ValueError("promotion requires the reviewed immutable module generation")
        return generation

    def apply(self, wanted, *, check=False):
        wanted = request(json.dumps(wanted))
        info = self.state.lstat()
        if (not stat.S_ISDIR(info.st_mode) or stat.S_IMODE(info.st_mode) != 0o750
                or info.st_uid != os.geteuid() or self.state.resolve(strict=True) != self.state):
            raise ValueError("promotion state requires a canonical root-owned directory")
        generation = self._module_identity()
        self._identity(wanted)
        binding = {"request": wanted, "moduleGeneration": generation, "dataDirectory": str(self.data), "configuration": str(self.config), "port": self.port}
        lock = os.open(self.state / "lock", os.O_WRONLY | os.O_CREAT | os.O_NOFOLLOW, 0o600)
        owns_intent = False
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            intent, receipt = self.state / INTENT, self.state / RECEIPT
            if intent.exists() or intent.is_symlink():
                retained_intent = decode(self._owned(intent, 0o600))
                paused_lsn = retained_intent.pop("pausedReplayLSN", None)
                if retained_intent != binding or (paused_lsn is not None and (not isinstance(paused_lsn, str) or not re.fullmatch(r"[0-9A-F]{1,8}/[0-9A-F]{1,8}", paused_lsn))):
                    raise ValueError("another frontier or module generation owns promotion intent")
            elif check:
                raise ValueError("authenticated promotion intent is absent")
            else:
                cluster, properties = self._identity(wanted), self._properties()
                if (properties != {"ActiveState": "inactive", "SubState": "dead", "MainPID": "0"}
                        or cluster.get("Database cluster state") != "shut down in recovery"
                        or (self.data / "postmaster.pid").exists() or not (self.data / "recovery.signal").is_file()):
                    raise ValueError("first promotion requires a stopped restored recovery cluster")
                self._write(intent, json.dumps(binding, sort_keys=True) + "\n", 0o600)
                paused_lsn = None
            owns_intent = True
            if not check and self._properties().get("ActiveState") == "inactive":
                self._write(self.data / "postgresql.auto.conf", self._auto_config(wanted), 0o644)
                self.execute(self.systemctl, "start", "postgresql.service")
            if not check and paused_lsn is not None and self._sql("SELECT pg_catalog.pg_is_in_recovery()") == "f":
                self._sql("CHECKPOINT")
            observed = self._observed(wanted, paused_lsn)
            if observed["recovery"]:
                if check:
                    raise ValueError("exact restored service is not promoted")
                paused_lsn = observed["lsn"]
                self._write(intent, json.dumps(dict(binding, pausedReplayLSN=paused_lsn), sort_keys=True) + "\n", 0o600)
                self.execute(self.runuser, "-u", "postgres", "--", str(self.bin / "pg_ctl"), "-D", str(self.data), "-w", "promote")
                self._sql("CHECKPOINT")
                observed = self._observed(wanted, paused_lsn)
                if observed["recovery"]:
                    raise ValueError("exact restored service remains in recovery")
            if self._module_identity() != generation:
                raise ValueError("module generation changed during promotion")
            expected = {"schemaVersion": 1, "kind": "leapview/managed-postgresql-promotion", "status": "promoted-loopback-only",
                        "targetID": wanted["targetID"], "recoverySetID": wanted["recoverySetID"], "frontierDigest": wanted["frontierDigest"],
                        "replacementMachineIDDigest": digest("leapview-managed-recovery-qualification:", wanted["replacementMachineID"]),
                        "systemIdentifier": wanted["systemIdentifier"], "targetLSN": wanted["targetLSN"], "timeline": wanted["timeline"],
                        "dataDirectoryDigest": digest("leapview-managed-recovery-data:", str(self.data)),
                        "moduleGenerationDigest": digest("leapview-managed-recovery-module:", generation), "port": self.port,
                        "configurationDigest": digest("leapview-managed-recovery-configuration:", str(self.config)),
                        "replayedThroughLSN": paused_lsn, "promotedTimeline": observed["timeline"],
                        "activationQualified": False, "fullManagedProfileQualified": False}
            if receipt.exists() or receipt.is_symlink():
                retained = decode(self._owned(receipt, 0o640))
                timestamp = retained.pop("promotedAt", None)
                if retained != expected or not isinstance(timestamp, str):
                    raise ValueError("promotion receipt differs from exact service intent")
                expected["promotedAt"] = timestamp
            elif check:
                raise ValueError("promotion receipt is absent; explicit reconciliation required")
            else:
                expected["promotedAt"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
                self._write(receipt, json.dumps(expected, sort_keys=True) + "\n", 0o640)
            return expected
        except (ValueError, OSError, subprocess.SubprocessError):
            # No failure is an application activation. Preserve authenticated
            # intent for an explicit exact retry and leave the writer stopped.
            if not check and owns_intent:
                try:
                    self.execute(self.systemctl, "stop", "postgresql.service")
                except (OSError, subprocess.SubprocessError):
                    pass
            raise
        finally:
            os.close(lock)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("state", "data", "config", "bin", "systemctl", "runuser", "pgbackrest"):
        parser.add_argument("--" + name, required=True)
    parser.add_argument("--port", required=True, type=int)
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()
    if os.geteuid() != 0:
        raise SystemExit("PostgreSQL recovery promotion requires root")
    try:
        user = pwd.getpwnam("postgres")
        result = Promotion(args.state, args.data, "/etc/machine-id", "/run/current-system", args.config,
                           args.bin, args.systemctl, args.runuser, args.pgbackrest, args.port,
                           service_uid=user.pw_uid, service_gid=user.pw_gid).apply(
                               request(sys.stdin.buffer.read(16385)), check=args.check)
    except (ValueError, OSError, subprocess.SubprocessError):
        raise SystemExit("PostgreSQL recovery promotion failed; keep application activation closed") from None
    print(json.dumps(result, sort_keys=True))


if __name__ == "__main__":
    main()
