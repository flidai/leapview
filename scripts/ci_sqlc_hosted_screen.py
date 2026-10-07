#!/usr/bin/env python3
"""Prepare full-image SQLC experiments; build --target runtime, then export proof."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import sys
import tarfile

import ci_sqlc_layer_screen as local

INVOCATION = f"go run {local.SQLC} generate --no-remote"
SOURCEGEN = "FROM go-deps AS sourcegen\n"


def digest(data):
    return hashlib.sha256(data).hexdigest()


def sourcegen_paths(original):
    stage, paths = None, []
    for line in original.splitlines():
        if line.startswith("FROM "):
            stage = line.split(" AS ")[-1]
        if stage in ("build", "web") and line.startswith("COPY --from=sourcegen "):
            match = re.fullmatch(r"COPY --from=sourcegen /src/([A-Za-z0-9_./-]+) \S+", line)
            if not match or any(p in ("", ".", "..") for p in match[1].split("/")):
                raise ValueError("unsupported sourcegen COPY contract: " + line)
            paths.append(match[1])
    if not paths:
        raise ValueError("no generated inputs discovered")
    return sorted(set(paths))


def render(original, script, mode):
    if (mode not in ("baseline", "treatment") or original.count(SOURCEGEN) != 1 or
            original.count("FROM runtime-base AS runtime\n") != 1 or script.count(INVOCATION) != 1 or
            f"{local.ENV} {INVOCATION}" not in script or "AS sqlc-tool" in original or
            "AS sqlc-screen-proof" in original):
        raise ValueError("production build anchors/pins changed or are ambiguous")
    if not re.search(r"^FROM golang:[^\s]+@sha256:[a-f0-9]{64} AS go-deps$", original, re.M):
        raise ValueError("Go base must remain digest-pinned")
    paths = sourcegen_paths(original)
    recipe = original
    if mode == "treatment":
        stage = f"""FROM go-deps AS sqlc-tool
WORKDIR /tool
RUN --mount=type=cache,target=/root/.cache/go-build \\
    --mount=type=cache,id=leapview-go-mod,target=/go/pkg/mod,from=go-deps,source=/go/pkg/mod,sharing=locked \\
    {local.ENV} GOBIN=/out go install {local.SQLC} && \\
    mkdir -p /identity && go version -m /out/sqlc > /identity/build.txt && \\
    sha256sum /out/sqlc > /identity/binary.sha256 && \\
    {local.ENV} go env -json GOOS GOARCH CGO_ENABLED GOFLAGS GOVERSION > /identity/environment.json && \\
    /out/sqlc version > /identity/version.txt

"""
        recipe = recipe.replace(SOURCEGEN, stage + SOURCEGEN + "COPY --from=sqlc-tool /out/sqlc /opt/sqlc\n", 1)
        script = script.replace(INVOCATION, "/opt/sqlc generate --no-remote", 1)
    copy = " && ".join("cp -a --parents " + path + " /screen-proof/sourcegen" for path in paths)
    recipe += "\nFROM sourcegen AS sqlc-screen-proof-build\n"
    recipe += "RUN mkdir -p /screen-proof/sourcegen /screen-proof/tool && " + copy + "\n"
    if mode == "treatment":
        recipe += "COPY --from=sqlc-tool /identity /screen-proof/tool\n"
    recipe += "\nFROM scratch AS sqlc-screen-proof\nCOPY --from=sqlc-screen-proof-build /screen-proof /\n"
    return recipe, script, paths


def manifest(root, paths):
    result = {}
    if root.is_symlink():
        raise ValueError("generated root is a symlink")
    for item in sorted(root.rglob("*")):
        name = item.relative_to(root).as_posix()
        if item.is_symlink():
            raise ValueError("generated symlink: " + name)
        if item.is_file():
            if not any(name == p or name.startswith(p + "/") for p in paths):
                raise ValueError("unexpected generated file: " + name)
            result[name] = digest(item.read_bytes())
    if not paths or any(not any(n == p or n.startswith(p + "/") for n in result) for p in paths):
        raise ValueError("missing generated input")
    return result


def build_proof(log, mode, kind):
    phases = re.findall(r"build_phase=sqlc elapsed_seconds=(\d+) exit_code=(\d+)", log)
    if len(phases) != 1 or phases[0][1] != "0":
        raise ValueError("SQLC must execute exactly once successfully in the measured full-image build")
    proof = {"sqlc_seconds": int(phases[0][0]), "build_log_sha256": digest(log.encode())}
    if mode == "treatment":
        proof["tool_cache_proof"] = local.tool_cache_proof(log)
        if kind == "consumer" and not proof["tool_cache_proof"]["cached"]:
            raise ValueError("treatment consumer did not reuse its producer's SQLC tool layer")
    return proof


def compare(left, right):
    keys = ("source", "archive_sha256", "original_dockerfile_sha256", "original_script_sha256", "paths", "manifest")
    if any(receipt.get("status") != "passed" or not receipt.get("manifest") for receipt in (left, right)):
        raise ValueError("only completed, nonempty receipts can be compared")
    for key in keys:
        if key not in left or key not in right or left[key] != right[key]:
            raise ValueError("receipt mismatch: " + key)


def export_proof(log):
    vertices = set(re.findall(
        r"^#(\d+) \[sourcegen [^\]]+\] RUN [^\n]*scripts/generate_build_sources\.sh[^\n]*$", log, re.M))
    if len(vertices) != 1 or re.search(r"build_phase=\S+ elapsed_seconds=\d+ exit_code=\d+", log):
        raise ValueError("proof export must identify cached source generation without executing generators")
    vertex = vertices.pop()
    if not re.search(r"^#" + vertex + r" CACHED$", log, re.M):
        raise ValueError("proof export reran source generation instead of extracting the measured build")
    return {"sourcegen_vertex": vertex, "cached": True, "log_sha256": digest(log.encode())}


def prepare(args):
    repo, output = args.repo.resolve(), args.output.resolve()
    if (not re.fullmatch(r"[a-f0-9]{40}", args.source) or output.exists() or
            output.is_relative_to(repo) or any(c in str(output) for c in ",\n\r")):
        raise ValueError("exact commit and new external output directory required")
    output.mkdir(parents=True)
    receipt = {"status": "preparing", "source": args.source, "mode": args.mode, "kind": args.kind,
               "scope": "hosted full-image experiment; no production adoption or SLO acceptance", "commands": []}
    runner = local.Runner(output, receipt)
    record = runner.run(["git", "-C", repo, "rev-parse", "--verify", args.source + "^{commit}"], "source")
    if (output / record["log"]).read_text().strip() != args.source:
        raise ValueError("source did not resolve to the exact requested commit")
    record = runner.run(["git", "-C", repo, "show", "-s", "--format=%cI", args.source], "build-time")
    receipt["build_time"] = (output / record["log"]).read_text().strip()
    archive, context = output / "source.tar", output / "context"
    runner.run(["git", "-C", repo, "archive", "--format=tar", "--output", archive, args.source], "archive")
    receipt["archive_sha256"] = digest(archive.read_bytes())
    context.mkdir()
    with tarfile.open(archive) as source:
        source.extractall(context, filter="data")
    original = (context / "Dockerfile").read_text()
    script_path = context / "scripts/generate_build_sources.sh"
    script = script_path.read_text()
    receipt["original_dockerfile_sha256"] = digest(original.encode())
    receipt["original_script_sha256"] = digest(script.encode())
    recipe, changed_script, receipt["paths"] = render(original, script, args.mode)
    script_path.write_text(changed_script)
    marker = context / "ci-sqlc-screen-marker"
    if marker.exists():
        raise ValueError("experiment marker collides with source")
    receipt["marker"] = "producer\n" if args.kind == "producer" else "changed-source\n"
    marker.write_text(receipt["marker"])
    recipe_path = output / "Dockerfile.screen"
    recipe_path.write_text(recipe)
    receipt.update({"recipe_sha256": digest(recipe.encode()), "script_sha256": digest(changed_script.encode()),
                    "context_sha256": local.context_digest(context), "status": "prepared"})
    runner.save()
    values = {"context": str(context), "recipe": str(recipe_path), "revision": args.source,
              "build_time": receipt["build_time"], "proof_dir": str(output / "proof"),
              "receipt": str(output / "receipt.json"), "image_target": "runtime", "proof_target": "sqlc-screen-proof"}
    if os.getenv("GITHUB_OUTPUT"):
        with open(os.environ["GITHUB_OUTPUT"], "a") as target:
            target.write("".join(f"{key}={value}\n" for key, value in values.items()))
    print(json.dumps(values))


def validate(output):
    receipt = json.loads((output / "receipt.json").read_text())
    if receipt["mode"] not in ("baseline", "treatment") or receipt["kind"] not in ("producer", "consumer"):
        raise ValueError("invalid experiment mode/kind")
    receipt.update(build_proof((output / "build.log").read_text(), receipt["mode"], receipt["kind"]))
    receipt["export_proof"] = export_proof((output / "proof.log").read_text())
    receipt["manifest"] = manifest(output / "proof/sourcegen", receipt["paths"])
    receipt["tool_identity"] = {}
    for path in sorted((output / "proof/tool").iterdir()):
        if not path.is_file() or path.is_symlink():
            raise ValueError("unexpected tool identity entry")
        receipt["tool_identity"][path.name] = path.read_text()
    if receipt["mode"] == "treatment":
        tool = receipt["tool_identity"]
        if (json.loads(tool["environment.json"]) != {"CGO_ENABLED": "1", "GOARCH": "amd64", "GOFLAGS": "", "GOOS": "linux", "GOVERSION": "go1.26.7"}
                or tool["version.txt"].strip() != "v1.31.1" or "go1.26.7" not in tool["build.txt"]
                or not re.fullmatch(r"[0-9a-f]{64}  /out/sqlc\n", tool["binary.sha256"])):
            raise ValueError("SQLC executable identity differs from the pinned contract")
    receipt["status"] = "passed"
    local.Runner(output, receipt).save()
    print(json.dumps({"status": "passed", "generated_files": len(receipt["manifest"])}))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    command = commands.add_parser("prepare")
    command.add_argument("--repo", type=Path, default=Path.cwd())
    command.add_argument("--source", required=True)
    command.add_argument("--mode", choices=["baseline", "treatment"], required=True)
    command.add_argument("--kind", choices=["producer", "consumer"], required=True)
    command.add_argument("--output", type=Path, required=True)
    commands.add_parser("validate").add_argument("--output", type=Path, required=True)
    command = commands.add_parser("compare")
    command.add_argument("--baseline", type=Path, required=True)
    command.add_argument("--treatment", type=Path, required=True)
    args = parser.parse_args()
    try:
        if args.command == "prepare":
            prepare(args)
        elif args.command == "validate":
            validate(args.output.resolve())
        else:
            receipts = [p / "receipt.json" if p.is_dir() else p for p in (args.baseline, args.treatment)]
            compare(*(json.loads(path.read_text()) for path in receipts))
            print("generated outputs and original source identity match")
    except Exception as error:
        if hasattr(args, "output") and (args.output / "receipt.json").exists():
            receipt = json.loads((args.output / "receipt.json").read_text())
            receipt.update(status="failed", error=str(error))
            local.Runner(args.output, receipt).save()
        raise


if __name__ == "__main__":
    main()
