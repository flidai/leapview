"""Mask ambient hosted-runner registry auth before container diagnostics run."""

import base64
import binascii
import json
import os
from pathlib import Path
import sys

MAX_BYTES = 1024 * 1024
SECRET_FIELDS = {"auth", "password", "identitytoken", "registrytoken"}


def _credentials(raw):
    if len(raw) > MAX_BYTES:
        raise ValueError("configuration too large")
    config = json.loads(raw)
    if not isinstance(config, dict) or not isinstance(config.get("auths", {}), dict):
        raise ValueError("configuration must contain registry objects")
    values = set()
    for entry in config.get("auths", {}).values():
        if not isinstance(entry, dict):
            raise ValueError("registry credentials must be objects")
        for key, value in entry.items():
            if key.lower() not in SECRET_FIELDS:
                continue
            if not isinstance(value, str):
                raise ValueError("credential must be a string")
            if not value:
                continue
            values.add(value)
            if key.lower() == "auth":
                try:
                    decoded = base64.b64decode(value, validate=True).decode("utf-8")
                except (binascii.Error, UnicodeError):
                    continue
                _, separator, password = decoded.partition(":")
                if separator and password:
                    values.add(password)
    return values


def mask_credentials(environment=None, output=None):
    environment = os.environ if environment is None else environment
    output = sys.stdout if output is None else output
    if (environment.get("GITHUB_ACTIONS") != "true" or environment.get("RUNNER_OS") != "Linux"
            or environment.get("RUNNER_ENVIRONMENT") != "github-hosted"):
        raise ValueError("Docker credential masking requires a GitHub-hosted Linux runner")
    values = set()
    try:
        # Testcontainers can read either source. Mask both without changing the
        # pull credentials, including encoded auth that Actions cannot infer.
        if environment.get("DOCKER_AUTH_CONFIG"):
            values.update(_credentials(environment["DOCKER_AUTH_CONFIG"]))
        directory = Path(environment.get("DOCKER_CONFIG") or Path.home() / ".docker")
        try:
            with (directory / "config.json").open("rb") as config:
                values.update(_credentials(config.read(MAX_BYTES + 1)))
        except FileNotFoundError:
            pass
    except (OSError, ValueError, UnicodeError):
        # Never include parser input, paths or original exceptions in logs.
        raise ValueError("Docker credential masking failed; container tests must not start") from None
    values.update(json.dumps(value, ensure_ascii=False)[1:-1] for value in tuple(values))
    for value in sorted(values):
        escaped = value.replace("%", "%25").replace("\r", "%0D").replace("\n", "%0A")
        print("::add-mask::" + escaped, file=output, flush=True)


if __name__ == "__main__":
    try:
        mask_credentials()
    except ValueError as error:
        raise SystemExit(str(error)) from None
