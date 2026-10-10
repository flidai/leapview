"""Prepare pinned test images on early hosted Linux CI only."""

import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import time

MIRROR = "https://mirror.gcr.io"
# Official publisher index: identical to the SDK's Docker Hub image. Preserve
# its stock tag so Testcontainers keeps its normal mandatory cleanup behavior.
RYUK_SOURCE = "ghcr.io/testcontainers/ryuk:0.14.0@sha256:7c1a8a9a47c780ed0f983770a662f80deb115d95cce3e2daa3d12115b8cd28f0"
RYUK_STOCK_IMAGE = "testcontainers/ryuk:0.14.0"


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


def postgres_image():
    harness = Path(__file__).resolve().parents[1] / "internal/platform/postgres/postgrestest/harness.go"
    match = re.search(r'^const PostgreSQL18Image = "([^"]+)"$', harness.read_text(), re.MULTILINE)
    if not match or not re.fullmatch(
            r"public\.ecr\.aws/docker/library/postgres:18-alpine@sha256:[a-f0-9]{64}", match[1]):
        raise ValueError("Invalid PostgreSQL image pin in conformance harness")
    return match[1]


def preload_postgres():
    # Populate the exact digest before independent application shards start.
    # An empty cache otherwise makes all four Testcontainers processes pull
    # simultaneously, exhausting the public registry's shared runner quota.
    image = postgres_image()
    for attempt, delay in enumerate([0, 5, 15]):
        if delay:
            time.sleep(delay)
        try:
            subprocess.run(["docker", "pull", image], check=True,
                           capture_output=True, text=True, timeout=180)
            break
        except subprocess.CalledProcessError as error:
            diagnostic = ((error.stdout or "") + (error.stderr or "")).lower()
            throttled = any(value in diagnostic for value in ["toomanyrequests", "too many requests", "rate exceeded"]) or re.search(r"\b429\b", diagnostic)
            if not throttled or attempt == 2:
                raise RuntimeError("Pinned PostgreSQL image pull failed") from None
            print("PostgreSQL image pull rate-limited; retrying before starting test shards", flush=True)
    image_id(image)


def active_mirror():
    mirrors = json.loads(subprocess.check_output(
        ["docker", "info", "--format", "{{json .RegistryConfig.Mirrors}}"], text=True))
    return MIRROR in [value.rstrip("/") for value in mirrors or []]


def configure(config_path=Path("/etc/docker/daemon.json"), *, postgres=False):
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
    if postgres:
        preload_postgres()


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--postgres", action="store_true", help="Preload the conformance image before PostgreSQL test shards")
    configure(postgres=parser.parse_args().postgres)
