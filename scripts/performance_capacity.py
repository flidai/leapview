#!/usr/bin/env python3
"""Bounded, serial characterization; never an optimization adoption decision."""
import hashlib
import json
import os
from pathlib import Path
import re
import resource
import signal
import subprocess
import sys
import time
from performance_process import run_owned

LADDER = (1, 10, 20, 100)
FAMILIES = {
    "compiler": ("./internal/project/compiler", "BenchmarkCompileSourceRootCapacity", "readers=", 1),
    "authorization": ("./internal/access/snapshot", "BenchmarkAuthorizationSnapshotCapacity", "readers=", 1),
    "sse": ("./pkg/pagestream", "BenchmarkSignalStreamHTTPFanoutCapacity", "readers=", 2),
    "managed": ("./internal/manageddata/runtimeview", "BenchmarkManagedRevisionCapacity", "readers=", 2),
    "query": ("./internal/dashboard/http", "BenchmarkDashboardWarmCacheConcurrency", "users_", 5),
}


def digest(path):
    return hashlib.file_digest(Path(path).open("rb"), "sha256").hexdigest()


def git(*args):
    return subprocess.check_output(["git", *args], text=True).strip()


def identity():
    if git("status", "--porcelain", "--untracked-files=no"):
        raise ValueError("tracked source must be clean before qualification")
    unexpected = [path for path in subprocess.check_output(
        ["git", "ls-files", "--others", "--exclude-standard", "-z"], text=True).split("\0") if path.endswith(".go")]
    if unexpected:
        raise ValueError("unexpected untracked Go sources")
    return {"commit": git("rev-parse", "HEAD"), "tree": git("rev-parse", "HEAD^{tree}")}


def build_environment():
    return {**os.environ, "GOFLAGS": "", "GOENV": "off", "GOWORK": "off", "GOTOOLCHAIN": "local", "CGO_ENABLED": "1"}


def build_inputs():
    return json.loads(subprocess.check_output(["node", "scripts/performance_build_inputs.mjs"],
                      env=build_environment(), text=True, timeout=180))


def write(path, value):
    with Path(path).open("x") as output:
        json.dump(value, output, indent=2, allow_nan=False)
        output.write("\n")


def validate_rows(text, benchmark, readers, expected):
    prefix = "users_" if benchmark == FAMILIES["query"][1] else "readers="
    rows = [line for line in text.splitlines() if line.startswith(benchmark + "/") and re.search(r"\s+ns/op\b", line)]
    if len(rows) != expected:
        raise ValueError(f"expected {expected} benchmark rows, got {len(rows)}")
    if not re.search(r"^PASS$", text, re.M) or re.search(r"^FAIL", text, re.M):
        raise ValueError("benchmark did not finish PASS")
    names = []
    for row in rows:
        fields = row.split()
        if not re.search(rf"/{prefix}{readers}-2$", fields[0]) or fields[1] != "1":
            raise ValueError("wrong capacity, CPU setting or batch count")
        if fields[0] in names:
            raise ValueError("duplicate benchmark row")
        names.append(fields[0])
        for unit in ("ns/op", "B/op", "allocs/op"):
            index = fields.index(unit)
            value = float(fields[index - 1])
            if not (0 <= value < float("inf")):
                raise ValueError(f"invalid {unit}")
    modes = ("cold/", "warm/") if benchmark == FAMILIES["managed"][1] else (("shared/", "independent/") if expected == 2 else ("",))
    if benchmark == FAMILIES["query"][1]:
        modes = tuple(name + "/" for name in ("kpi", "chart_bundle", "wide_chart", "table_window_json", "table_window_arrow"))
    if set(names) != {f"{benchmark}/{mode}{prefix}{readers}-2" for mode in modes}:
        raise ValueError("unexpected workload names")
    return rows


def resources():
    result = {"logicalCPUs": os.cpu_count()}
    for name, path in {
        "memoryEvents": "/sys/fs/cgroup/memory.events",
        "memoryMax": "/sys/fs/cgroup/memory.max",
        "cpuMax": "/sys/fs/cgroup/cpu.max",
        "load": "/proc/loadavg",
        "memory": "/proc/meminfo",
    }.items():
        try:
            result[name] = Path(path).read_text()
        except FileNotFoundError:
            result[name] = None
    return result


def oom_count(snapshot):
    if snapshot["memoryEvents"] is None:
        return None
    return dict(line.split() for line in snapshot["memoryEvents"].splitlines()).get("oom_kill")


def measure(binary, benchmark, readers, output):
    """Runs in a fresh Python process so child CPU/RSS accounting is per sample."""
    levels = "/^(shared|independent)$" if benchmark == FAMILIES["sse"][1] else ("/^(cold|warm)$" if benchmark == FAMILIES["managed"][1] else "")
    prefix = "readers="
    if benchmark == FAMILIES["query"][1]:
        levels, prefix = "/^(kpi|chart_bundle|wide_chart|table_window_json|table_window_arrow)$", "users_"
    command = [binary, "-test.run=^$", f"-test.bench=^{benchmark}${levels}/^{prefix}{readers}$",
               "-test.benchmem", "-test.benchtime=1x", "-test.count=1", "-test.cpu=2",
               "-test.timeout=120s", "-test.v"]
    before = resources()
    started = time.monotonic()
    with Path(output + ".log").open("xb") as log:
        execution = run_owned(command, stdout=log, env=build_environment(), timeout=150)
    usage = resource.getrusage(resource.RUSAGE_CHILDREN)
    after = resources()
    write(output + ".json", {
        "command": command, **execution,
        "wallSeconds": time.monotonic() - started, "userCPUSeconds": usage.ru_utime,
        "systemCPUSeconds": usage.ru_stime, "maxResidentMemoryKiB": usage.ru_maxrss,
        "resourceBefore": before, "resourceAfter": after,
        "oomAccountingAvailable": oom_count(before) is not None and oom_count(after) is not None,
        "oomKillObserved": None if oom_count(before) is None or oom_count(after) is None else oom_count(before) != oom_count(after),
        "logSHA256": digest(output + ".log"),
    })
    return execution["exitCode"] if execution["terminationReason"] is None else 1


def prepare(directory):
    directory.mkdir(parents=True, exist_ok=False)
    source = identity()
    inputs = build_inputs()
    binaries = {}
    for name, (package, _, _, _) in FAMILIES.items():
        binary = directory / (name + ".test")
        command = ["go", "test", "-mod=readonly", "-tags=duckdb_arrow", "-p", "1", "-c", "-o", str(binary), package]
        with (directory / (name + "-build.log")).open("xb") as log:
            subprocess.run(command, stdout=log, stderr=subprocess.STDOUT, env=build_environment(), timeout=600, check=True)
        binaries[name] = {"path": str(binary), "sha256": digest(binary), "package": package, "command": command}
    if identity() != source or build_inputs() != inputs:
        raise ValueError("source or selected generated/native/embed inputs changed during preparation")
    write(directory / "build-inputs.json", inputs)
    write(directory / "prepared.json", {"schemaVersion": 1, "source": source, "binaries": binaries,
        "goVersion": subprocess.check_output(["go", "version"], text=True).strip(),
        "nixLockSHA256": digest("flake.lock"), "buildInputsSHA256": digest(directory / "build-inputs.json")})


def run(directory):
    prepared = json.loads((directory / "prepared.json").read_text())
    source = identity()
    original_inputs = json.loads((directory / "build-inputs.json").read_text())
    if digest(directory / "build-inputs.json") != prepared["buildInputsSHA256"] or build_inputs() != original_inputs:
        raise ValueError("prepared actual Go/generated/native/embed inputs or settings changed")
    if source != prepared["source"] or digest("flake.lock") != prepared["nixLockSHA256"]:
        raise ValueError("prepared source/toolchain differs from current source")
    for binary in prepared["binaries"].values():
        if digest(binary["path"]) != binary["sha256"]:
            raise ValueError("prepared binary bytes changed")
    output = directory / "measurement"
    output.mkdir(exist_ok=False)
    protocol = {"schemaVersion": 1, "kind": "current-source-capacity-characterization",
        "source": source, "ladder": LADDER, "freshProcessesPerStep": 3, "cpu": 2,
        "batchOperationsPerProcess": 1, "timeoutSeconds": 150,
        "decision": "no optimization candidate; no adoption or user-p95 claim",
        "resourceAccounting": "whole process, including fixture setup and cleanup; RSS in Linux KiB",
        "resourceStop": "4GiB observed process-tree RSS, polled every100ms; prospective experiment safety bound on32GiB host, not product budget",
        "stop": "first exit failure, timeout, OOM kill, missing/wrong row, source drift or cleanup failure",
        "families": FAMILIES, "resources": resources()}
    write(output / "protocol.json", protocol)
    results = []
    failure = None
    try:
        for readers in LADDER:
            for name, (_, benchmark, _, expected) in FAMILIES.items():
                for repetition in range(1, 4):
                    if identity() != source:
                        raise ValueError("source changed during measurement")
                    prefix = str(output / f"{name}-{readers}-{repetition}")
                    command = [sys.executable, str(Path(__file__).resolve()), "_measure",
                               prepared["binaries"][name]["path"], benchmark, str(readers), prefix]
                    subprocess.run(command, check=True)
                    receipt = json.loads(Path(prefix + ".json").read_text())
                    if receipt["terminationReason"] or receipt["oomKillObserved"]:
                        raise ValueError("resource/timeout stop condition")
                    rows = validate_rows(Path(prefix + ".log").read_text(), benchmark, readers, expected)
                    results.append({"family": name, "readers": readers, "repetition": repetition,
                                    "rows": rows, "receiptSHA256": digest(prefix + ".json")})
        if identity() != source or build_inputs() != original_inputs:
            raise ValueError("source or actual build inputs changed before final receipt")
    except (ValueError, subprocess.SubprocessError) as error:
        failure = str(error)
    write(output / "decision.json", {"source": source, "result": "complete" if failure is None else "stopped",
        "failure": failure, "samples": results, "adoption": "none",
        "limitation": "three fresh one-batch samples characterize this fixture only; no stable production capacity or p95 inference"})
    return 0 if failure is None else 1


if __name__ == "__main__":
    if len(sys.argv) == 6 and sys.argv[1] == "_measure":
        sys.exit(measure(sys.argv[2], sys.argv[3], int(sys.argv[4]), sys.argv[5]))
    if len(sys.argv) != 3 or sys.argv[1] not in ("prepare", "run"):
        sys.exit("usage: performance_capacity.py <prepare|run> NEW_ABSOLUTE_EVIDENCE_DIR")
    directory = Path(sys.argv[2])
    if not directory.is_absolute():
        sys.exit("evidence directory must be absolute")
    sys.exit(prepare(directory) if sys.argv[1] == "prepare" else run(directory))
