"""Prepare Docker cache and the pinned reaper on early hosted Linux CI only."""

import json
import os
from pathlib import Path
import re
import subprocess

MIRROR = "https://mirror.gcr.io"
# Official publisher index: identical to the SDK's Docker Hub image. Preserve
# its stock tag so Testcontainers keeps its normal mandatory cleanup behavior.
RYUK_SOURCE = "ghcr.io/testcontainers/ryuk:0.13.0@sha256:31b31269d06603366cbfd0284708dcd2e281e8a4188e53fce3d3304439d0df3d"
RYUK_STOCK_IMAGE = "testcontainers/ryuk:0.13.0"


def image_id(image):
    identity = subprocess.check_output(
        ["docker", "image", "inspect", image, "--format", "{{.Id}}"], text=True).strip()
    if not re.fullmatch(r"sha256:[a-f0-9]{64}", identity):
        raise RuntimeError("Invalid Docker image identity")
    return identity


def preload_ryuk():
    subprocess.run(["docker", "pull", RYUK_SOURCE], check=True)
    source_id = image_id(RYUK_SOURCE)
    subprocess.run(["docker", "tag", RYUK_SOURCE, RYUK_STOCK_IMAGE], check=True)
    if image_id(RYUK_STOCK_IMAGE) != source_id:
        raise RuntimeError("Ryuk image identity differs after assigning the SDK tag")


def active_mirror():
    mirrors = json.loads(subprocess.check_output(
        ["docker", "info", "--format", "{{json .RegistryConfig.Mirrors}}"], text=True))
    return MIRROR in [value.rstrip("/") for value in mirrors or []]


def configure(config_path=Path("/etc/docker/daemon.json")):
    if (os.environ.get("GITHUB_ACTIONS") != "true"
            or os.environ.get("RUNNER_OS") != "Linux"
            or os.environ.get("RUNNER_ENVIRONMENT") != "github-hosted"):
        raise RuntimeError("Docker mirror setup requires a GitHub-hosted Linux runner")
    config = json.loads(config_path.read_text()) if config_path.exists() else {}
    if not isinstance(config, dict):
        raise ValueError("Docker daemon configuration must be a JSON object")
    mirrors = config.get("registry-mirrors", [])
    if not isinstance(mirrors, list) or not all(isinstance(value, str) for value in mirrors):
        raise ValueError("Docker registry-mirrors must be a list of strings")
    merged = [MIRROR] + [value for value in mirrors if value.rstrip("/") != MIRROR]
    changed = mirrors != merged
    if changed:
        config["registry-mirrors"] = merged
        config_path.parent.mkdir(parents=True, exist_ok=True)
        config_path.write_text(json.dumps(config, indent=2) + "\n")
    if changed or not active_mirror():
        subprocess.run(["systemctl", "restart", "docker"], check=True)
    if not active_mirror():
        raise RuntimeError("Docker daemon did not activate the CI registry mirror")
    preload_ryuk()


if __name__ == "__main__":
    configure()
