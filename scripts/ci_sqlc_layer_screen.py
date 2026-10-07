#!/usr/bin/env python3
"""Local SQLC immutable-layer experiment; never changes production build policy."""

import argparse
import hashlib
import json
from pathlib import Path
import re
import subprocess
import sys
import tarfile
import time
import uuid

SQLC = "github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1"
ENV = "GODEBUG=http2client=0 GOTOOLCHAIN=go1.26.7"
PIN = r"[a-zA-Z0-9./:_-]+@sha256:[a-f0-9]{64}"


def validate_inputs(repo, source, output, image, pairs):
    if not re.fullmatch(r"[a-f0-9]{40}", source):
        raise ValueError("source must be an exact 40-character commit SHA")
    if not re.fullmatch(PIN, image) or not 1 <= pairs <= 10:
        raise ValueError("immutable builder image and 1..10 pairs required")
    if output.exists() or output.is_relative_to(repo) or any(c in str(output) for c in ",\n\r"):
        raise ValueError("output must be a new directory outside the source checkout, without CSV delimiters")


def output_paths(text):
    paths = []
    for line in text.splitlines():
        if re.match(r"\s*out\s*:", line):
            match = re.fullmatch(r'        out: "(internal/[A-Za-z0-9_/-]+/internal/db)"', line)
            if not match or any(part in (".", "..", "") for part in match[1].split("/")):
                raise ValueError("unsupported SQLC output declaration: " + line)
            paths.append(match[1])
    if not paths or len(set(paths)) != len(paths):
        raise ValueError("missing or duplicate SQLC outputs")
    return sorted(paths)


def manifest(root, outputs):
    result = {}
    for path in root.rglob("*"):
        relative = path.relative_to(root).as_posix()
        if path.is_symlink():
            raise ValueError("generated output contains symlink: " + relative)
        if path.is_file():
            if path.suffix != ".go" or not any(relative.startswith(p + "/") for p in outputs):
                raise ValueError("unexpected generated output: " + relative)
            result[relative] = hashlib.sha256(path.read_bytes()).hexdigest()
    if any(not any(name.startswith(p + "/") for name in result) for p in outputs):
        raise ValueError("missing or empty generated output directory")
    return dict(sorted(result.items()))


def require_equal(left, right):
    if left != right:
        raise ValueError("generated output path/content manifests differ")


def context_digest(context):
    digest = hashlib.sha256()
    for path in sorted(context.rglob("*")):
        content = (str(path.readlink()) if path.is_symlink() else
                   hashlib.sha256(path.read_bytes()).hexdigest() if path.is_file() else "directory")
        digest.update(f"{path.relative_to(context)}\0{path.lstat().st_mode:o}\0{content}\n".encode())
    return digest.hexdigest()


def tool_cache_proof(log):
    headers = re.findall(r"^#(\d+) (\[sqlc-tool [^\]]+\] RUN [^\n]*go install [^\n]+)$", log, re.M)
    # Plain progress repeats a vertex header while imported layers are extracted.
    # Collapse identical observations, while rejecting distinct install vertices.
    headers = list(dict.fromkeys(headers))
    if len(headers) != 1 or SQLC not in headers[0][1]:
        raise ValueError("missing or ambiguous SQLC tool install vertex")
    vertex, header = headers[0]
    return {"vertex": vertex, "header": header,
            "cached": bool(re.search(r"^#" + vertex + r" CACHED\s*$", log, re.M))}


def dockerfile(original, outputs):
    syntax = re.search(r"^# syntax=(docker/dockerfile:[^\s]+@sha256:[a-f0-9]{64})$", original, re.M)
    base = re.search(r"^FROM (golang:[^\s]+@sha256:[a-f0-9]{64}) AS go-deps$", original, re.M)
    if not syntax or not base:
        raise ValueError("production Dockerfile must pin frontend and Go base by digest")
    mounts = ("--mount=type=cache,target=/root/.cache/go-build "
              "--mount=type=cache,id=leapview-go-mod,target=/go/pkg/mod,from=go-deps,source=/go/pkg/mod,sharing=locked")
    copy = " && ".join("cp -a --parents " + p + " /screen/generated" for p in outputs)
    identity = f"(cd /tmp && {ENV} go env -json GOOS GOARCH CGO_ENABLED GOFLAGS GOVERSION)"
    text = f"""# syntax={syntax[1]}
FROM {base[1]} AS go-deps
WORKDIR /src
COPY go.mod go.sum ./
COPY pkg/apigen/go.mod pkg/apigen/go.sum ./pkg/apigen/
RUN go mod download || (sleep 5 && go mod download) || (sleep 5 && go mod download)

FROM go-deps AS sqlc-tool
WORKDIR /tool
RUN {mounts} {ENV} GOBIN=/out go install {SQLC} && mkdir -p /identity && go version -m /out/sqlc > /identity/build.txt && sha256sum /out/sqlc > /identity/binary.sha256 && {identity} > /identity/environment.json

FROM go-deps AS inputs
COPY . .
"""
    for mode, command in [("baseline", f"go run {SQLC}"), ("treatment", "/opt/sqlc")]:
        text += f"\nFROM inputs AS {mode}\n"
        if mode == "treatment":
            text += "COPY --from=sqlc-tool /out/sqlc /opt/sqlc\nCOPY --from=sqlc-tool /identity /screen/tool\n"
        text += (f"RUN {mounts} ./scripts/time_build_phase.sh sqlc env {ENV} {command} generate --no-remote"
                 f" && mkdir -p /screen/generated /screen/tool && {copy} && {ENV} {command} version > /screen/tool/version.txt")
        if mode == "baseline":
            text += f" && {identity} > /screen/tool/environment.json"
        text += "\n"
        text += f"\nFROM scratch AS {mode}-output\nCOPY --from={mode} /screen /\n"
    return text


class Runner:
    def __init__(self, directory, receipt):
        self.directory, self.receipt = directory, receipt

    def save(self):
        temporary = self.directory / "receipt.tmp"
        temporary.write_text(json.dumps(self.receipt, indent=2) + "\n")
        temporary.replace(self.directory / "receipt.json")

    def run(self, args, label, check=True):
        record = {"command": [str(a) for a in args], "label": label,
                  "log": f"{len(self.receipt['commands']):03d}-{label}.log"}
        self.receipt["commands"].append(record)
        self.save()
        started = time.monotonic()
        try:
            with (self.directory / record["log"]).open("wb") as log:
                result = subprocess.run(record["command"], stdout=log, stderr=subprocess.STDOUT)
            record["exit_code"] = result.returncode
        finally:
            record["seconds"] = time.monotonic() - started
            self.save()
        if check and result.returncode:
            raise RuntimeError(f"{label} failed ({result.returncode}); see {record['log']}")
        return record


def build(runner, context, recipe, image, mode, kind, cache_in, cache_out):
    name = "leapview-sqlc-screen-" + uuid.uuid4().hex[:16]
    label = mode + "-" + kind
    output = runner.directory / (label + "-output")
    sample = {"mode": mode, "kind": kind, "builder": name, "output": str(output)}
    sample["context_sha256"] = context_digest(context)
    for key, path in [("marker", context / "ci-sqlc-screen-marker"),
                      ("query_sha256", context / "internal/project/postgres/queries/project.sql")]:
        if path.exists():
            sample[key] = path.read_text() if key == "marker" else hashlib.sha256(path.read_bytes()).hexdigest()
    runner.receipt.setdefault("samples", []).append(sample)
    started = time.monotonic()
    try:
        runner.run(["docker", "buildx", "create", "--name", name, "--driver", "docker-container",
                    "--driver-opt", "image=" + image], label + "-create")
        runner.run(["docker", "buildx", "inspect", "--bootstrap", name], label + "-bootstrap")
        args = ["docker", "buildx", "build", "--builder", name, "--platform", "linux/amd64",
                "--progress", "plain", "--file", recipe, "--target", mode + "-output",
                "--metadata-file", runner.directory / (label + "-metadata.json"),
                "--output", "type=local,dest=" + str(output)]
        if cache_in:
            args += ["--cache-from", "type=local,src=" + str(cache_in)]
        if cache_out:
            args += ["--cache-to", "type=local,mode=max,dest=" + str(cache_out)]
        record = runner.run(args + [context], label + "-build")
        sample["build_including_export_seconds"] = record["seconds"]
        log = (runner.directory / record["log"]).read_text(errors="replace")
        if mode == "treatment":
            sample["tool_cache_proof"] = tool_cache_proof(log)
            if cache_in and not sample["tool_cache_proof"]["cached"]:
                raise ValueError("restored treatment did not reuse the immutable SQLC tool layer")
        phases = re.findall(r"build_phase=sqlc elapsed_seconds=(\d+) exit_code=(\d+)", log)
        if len(phases) != 1 or phases[0][1] != "0":
            raise ValueError("SQLC must execute exactly once, successfully; cached/missing phase rejected")
        sample["sqlc_seconds"] = int(phases[0][0])
        cache = cache_out or cache_in
        sample["cache_bytes"] = sum(p.stat().st_size for p in cache.rglob("*") if p.is_file())
        sample["tool_identity"] = {p.name: p.read_text() for p in sorted((output / "tool").iterdir())}
        identity = json.loads(sample["tool_identity"]["environment.json"])
        if (sample["tool_identity"]["version.txt"].strip() != "v1.31.1" or
                identity != {"CGO_ENABLED": "1", "GOARCH": "amd64", "GOFLAGS": "", "GOOS": "linux", "GOVERSION": "go1.26.7"}):
            raise ValueError("SQLC version or compilation environment differs from expected Docker contract")
        return sample
    finally:
        failed = sys.exc_info()[0] is not None
        cleanup = runner.run(["docker", "buildx", "rm", "--force", name], label + "-cleanup", check=False)
        sample["total_seconds_including_builder_lifecycle"] = time.monotonic() - started
        runner.save()
        if cleanup["exit_code"] and not failed:
            raise RuntimeError("experiment builder cleanup failed: " + name)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", type=Path, default=Path.cwd())
    parser.add_argument("--source", required=True)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--builder-image", required=True)
    parser.add_argument("--pairs", type=int, default=3)
    parser.add_argument("--query-mutation", action="store_true", help="extra correctness-only query-rename comparison")
    args = parser.parse_args()
    repo, output = args.repo.resolve(), args.output.resolve()
    validate_inputs(repo, args.source, output, args.builder_image, args.pairs)
    output.mkdir(parents=True)
    receipt = {"source": args.source, "builder_image": args.builder_image, "platform": "linux/amd64",
               "scope": "local SQLC-only screen; no hosted cost, full-image, adoption or SLO conclusion", "commands": []}
    runner = Runner(output, receipt)
    try:
        record = runner.run(["git", "-C", repo, "rev-parse", "--verify", args.source + "^{commit}"], "source")
        if (output / record["log"]).read_text().strip() != args.source:
            raise ValueError("source is not the exact requested commit")
        archive = output / "source.tar"
        runner.run(["git", "-C", repo, "archive", "--format=tar", "--output", archive, args.source], "archive")
        receipt["source_archive_sha256"] = hashlib.sha256(archive.read_bytes()).hexdigest()
        context = output / "context"
        context.mkdir()
        with tarfile.open(archive) as source:
            source.extractall(context, filter="data")
        script = (context / "scripts/generate_build_sources.sh").read_text()
        if f"{ENV} go run {SQLC} generate --no-remote" not in script:
            raise ValueError("source SQLC invocation differs from screened pin/transport")
        outputs = output_paths((context / "sqlc.yaml").read_text())
        receipt["output_paths"] = outputs
        recipe = output / "Dockerfile.screen"
        recipe.write_text(dockerfile((context / "Dockerfile").read_text(), outputs))
        receipt["dockerfile_sha256"] = hashlib.sha256(recipe.read_bytes()).hexdigest()
        runner.run(["docker", "version"], "docker-version")
        runner.run(["docker", "buildx", "version"], "buildx-version")
        marker = context / "ci-sqlc-screen-marker"
        if marker.exists():
            raise ValueError("experiment marker collides with source file")
        expected = None
        for kind, order in [("producer", ["baseline", "treatment"])] + [
                (f"pair-{i + 1}", ["baseline", "treatment"] if i % 2 == 0 else ["treatment", "baseline"])
                for i in range(args.pairs)]:
            marker.write_text("producer\n" if kind == "producer" else "changed-source\n")
            for mode in order:
                cache = output / (mode + "-cache")
                sample = build(runner, context, recipe, args.builder_image, mode, kind,
                               None if kind == "producer" else cache, cache if kind == "producer" else None)
                sample["manifest"] = manifest(Path(sample["output"]) / "generated", outputs)
                if expected is None:
                    expected = sample["manifest"]
                require_equal(expected, sample["manifest"])
                runner.save()
        if args.query_mutation:
            query = context / "internal/project/postgres/queries/project.sql"
            text = query.read_text()
            before = "-- name: LockContractPublication :exec"
            if text.count(before) != 1:
                raise ValueError("correctness probe query changed")
            query.write_text(text.replace(before, "-- name: ScreenLockContractPublication :exec"))
            probe = []
            for mode in ["baseline", "treatment"]:
                sample = build(runner, context, recipe, args.builder_image, mode, "query-probe", output / (mode + "-cache"), None)
                sample["manifest"] = manifest(Path(sample["output"]) / "generated", outputs)
                probe.append(sample["manifest"])
                runner.save()
            require_equal(*probe)
            if probe[0] == expected:
                raise ValueError("query mutation did not change generated output")
        receipt["status"] = "passed"
    except BaseException as error:
        receipt["status"], receipt["error"] = "failed", str(error)
        raise
    finally:
        runner.save()


if __name__ == "__main__":
    main()
