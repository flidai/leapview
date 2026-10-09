"""Configure only an early GitHub-hosted Linux CI daemon, preserving image pins."""

import json
import os
from pathlib import Path
import subprocess

MIRROR = "https://mirror.gcr.io"


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


if __name__ == "__main__":
    configure()
