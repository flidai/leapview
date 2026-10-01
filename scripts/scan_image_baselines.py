#!/usr/bin/env python3
"""Temporary diagnostic comparison; never produces admission/qualification receipts."""
import argparse
from contextlib import nullcontext
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile

BASELINES = (
    "ghcr.io/flidai/leapview@sha256:d53f0fa80a7637e9d61ab667fab87c9cf3703b92bdce55e45797e428975f8d21",
    "ghcr.io/flidai/leapview@sha256:d03a12efcd20e7383f8973f5ef5e32e909ca6495ca49c70860222fe926398f86",
)
RUNTIME_BASE = "gcr.io/distroless/cc-debian13:debug-nonroot@sha256:f525a9a37aed3e8a848f46cfe055999782d66ed797e9e2886928c8caaaa4fc52"
PLATFORM = "linux/amd64"


def run(args):
    result = subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                            timeout=900, check=False)
    if result.returncode:
        raise RuntimeError("diagnostic scanner command failed (raw output withheld)")
    return result.stdout


def text(value, limit=256):
    if not isinstance(value, str) or len(value) > limit:
        return "[redacted]"
    if not re.fullmatch(r"[A-Za-z0-9._+:/@~, ()=-]*", value):
        return "[redacted]"
    for key, secret in os.environ.items():
        if any(marker in key.upper() for marker in ("TOKEN", "PASSWORD", "SECRET", "CREDENTIAL", "KEY")) and len(secret) >= 4:
            value = value.replace(secret, "[redacted]")
    return value


def databases(cache):
    result = {}
    for name in ("db", "java-db"):
        raw = (cache / name / "metadata.json").read_bytes()
        metadata = json.loads(raw)
        result[name] = {key: text(str(metadata.get(key, "")), 64)
                        for key in ("Version", "UpdatedAt", "NextUpdate", "DownloadedAt")}
        result[name]["metadataSHA256"] = hashlib.sha256(raw).hexdigest()
    return result


def findings(scan):
    if not isinstance(scan, dict) or not isinstance(scan.get("Results", []), list):
        raise ValueError("invalid scanner JSON")
    result = []
    for section in scan.get("Results", []):
        for item in section.get("Vulnerabilities") or []:
            cve = item.get("VulnerabilityID", "")
            if not re.fullmatch(r"(?:CVE-[0-9]{4}-[0-9]+|GHSA-[A-Za-z0-9-]+)", cve):
                cve = "[redacted]"
            result.append(dict(cve=cve, package=text(item.get("PkgName", "")),
                               installedVersion=text(item.get("InstalledVersion", "")),
                               fixedVersion=text(item.get("FixedVersion", "")),
                               severity=text(item.get("Severity", "")),
                               severitySource=text(item.get("SeveritySource", "")),
                               dataSource=text((item.get("DataSource") or {}).get("Name", "")),
                               vendorSeverity={text(key): text(str(value)) for key, value in (item.get("VendorSeverity") or {}).items()},
                               component=text(section.get("Type", "")),
                               target=text(Path(section.get("Target", "")).name)))
    return sorted(result, key=lambda item: (item["cve"], item["package"], item["target"]))


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    policy_bytes = Path(".github/security/container-vulnerability-policy.json").read_bytes()
    policy = json.loads(policy_bytes)
    assert policy["scannerVersion"] == "0.74.0"
    assert re.fullmatch(r"aquasec/trivy:0\.74\.0@sha256:[0-9a-f]{64}", policy["scannerImage"])
    assert set(policy["severity"]) == {"HIGH", "CRITICAL"}
    assert policy["ignoreUnfixed"] is False and policy["maxUnresolved"] == 0
    report = dict(schemaVersion=1, diagnosticOnly=True, platform=PLATFORM,
                  scanner=dict(name="trivy", version=policy["scannerVersion"], image=policy["scannerImage"]),
                  policySHA256=hashlib.sha256(policy_bytes).hexdigest(), images=[])
    try:
        report["stage"] = "runtime-base-build"
        run(["docker", "buildx", "build", "--load", "--platform", PLATFORM,
             "--target", "runtime-base", "--tag", "leapview:runtime-security-base", "."])
        patched_image = json.loads(run(["docker", "image", "inspect", "leapview:runtime-security-base",
                                       "--format", "{{json .Id}}"] ))
        assert re.fullmatch(r"sha256:[0-9a-f]{64}", patched_image)
        report["stage"] = "frozen-scans"
        # The ephemeral hosted runner owns cache disposal. Scanner containers can
        # create root-owned entries; never let host cleanup change a scan outcome.
        with nullcontext(tempfile.mkdtemp(prefix="leapview-baseline-")) as directory:
            cache = Path(directory)
            command = ["docker", "run", "--rm", "--network", "host",
                       "-v", "/var/run/docker.sock:/var/run/docker.sock",
                       "-v", str(Path.home() / ".docker") + ":/root/.docker:ro",
                       "-v", str(cache) + ":/cache", policy["scannerImage"]]
            version = json.loads(run(command + ["version", "--format", "json"]))
            assert version["Version"] == policy["scannerVersion"]
            for flag in ("--download-db-only", "--download-java-db-only"):
                run(command + ["image", "--cache-dir", "/cache", flag, "--quiet"])
            frozen = databases(cache)
            report["database"] = frozen
            for image in (*BASELINES, RUNTIME_BASE, patched_image):
                if image != patched_image:
                    run(["docker", "pull", "--platform", PLATFORM, image])
                labels = json.loads(run(["docker", "image", "inspect", image,
                                         "--format", "{{json .Config.Labels}}"] )) or {}
                revision = labels.get("org.opencontainers.image.revision", "")
                if image in BASELINES:
                    assert re.fullmatch(r"[0-9a-f]{40}", revision)
                scan = json.loads(run(command + ["image", "--cache-dir", "/cache", "--quiet",
                    "--format", "json", "--exit-code", "0", "--scanners", "vuln",
                    "--platform", PLATFORM, "--severity", ",".join(policy["severity"]),
                    "--skip-db-update", "--skip-java-db-update", "--timeout", "10m", image]))
                entries = findings(scan)
                assert databases(cache) == frozen, "frozen database metadata changed"
                report["images"].append(dict(image=image, revision=revision,
                    outcome="findings" if entries else "clean", findingCount=len(entries), findings=entries))
            report["outcome"] = "compared"
            report.pop("stage", None)
    except Exception:
        report["outcome"] = "diagnostic-error"
        raise
    finally:
        output = Path(args.output)
        output.parent.mkdir(parents=True, exist_ok=True)
        output.write_text(json.dumps(report, indent=2) + "\n")


if __name__ == "__main__":
    main()
