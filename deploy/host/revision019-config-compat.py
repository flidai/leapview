#!/usr/bin/env python3
"""Translate the one admitted revision-019 host config and bind its target."""

import argparse
import json
import os
import re
import stat
import tempfile


PREDECESSOR = (
    "ghcr.io/flidai/leapview@sha256:"
    "4a4455ff0048704acf0df1a9308a39a09b4c786f801fe7f3a383ada089d21368"
)
LEGACY_FIELDS = {"schemaVersion", "domain", "adminEmail", "environment", "image", "https"}
CURRENT_FIELDS = LEGACY_FIELDS | {"targetId"}


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate configuration field")
        result[key] = value
    return result


def read_private_json(path):
    info = os.lstat(path)
    if not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid() or info.st_mode & 0o077:
        raise ValueError("configuration or marker must be an owner-only regular file")
    with open(path, "r", encoding="utf-8") as source:
        return json.load(source, object_pairs_hook=unique_object)


def validate_current(config, image):
    if not isinstance(config, dict) or set(config) != CURRENT_FIELDS:
        raise ValueError("bootstrap configuration fields do not match the current schema")
    if image != PREDECESSOR or config["image"] != image:
        raise ValueError("revision-019 compatibility requires the exact admitted predecessor image")
    if type(config["schemaVersion"]) is not int or config["schemaVersion"] != 1:
        raise ValueError("unsupported bootstrap schema")
    if type(config["https"]) is not bool:
        raise ValueError("https must be a boolean")
    for field in ("domain", "adminEmail", "environment"):
        if not isinstance(config[field], str) or not config[field] or config[field] != config[field].strip():
            raise ValueError(f"{field} must be a nonempty canonical string")
    target = config["targetId"]
    if (not isinstance(target, str) or not target or len(target) > 512
            or target != target.strip() or any(ord(char) < 32 for char in target)):
        raise ValueError("targetId must be a nonempty canonical single-line identifier")
    legacy = {field: config[field] for field in LEGACY_FIELDS}
    domain = legacy["domain"].lower().removesuffix(".")
    if (not domain or len(domain) > 253 or
            any(not re.fullmatch(r"[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?", label)
                for label in domain.split("."))):
        raise ValueError("domain is not a valid public host")
    legacy["domain"] = domain
    return legacy


def write_private_json(path, value):
    directory = os.path.dirname(os.path.abspath(path))
    if not os.path.lexists(directory):
        os.mkdir(directory, 0o700)
    directory_info = os.lstat(directory)
    if not stat.S_ISDIR(directory_info.st_mode) or directory_info.st_uid != os.geteuid():
        raise ValueError("output directory must be owned by the bootstrap user")
    if os.path.lexists(path) and not stat.S_ISREG(os.lstat(path).st_mode):
        raise ValueError("output path must be a regular file")
    fd, temporary = tempfile.mkstemp(prefix=".leapview-config-", dir=directory)
    try:
        os.fchmod(fd, 0o600)
        with os.fdopen(fd, "w", encoding="utf-8") as output:
            json.dump(value, output, indent=2, sort_keys=True)
            output.write("\n")
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary, path)
        directory_fd = os.open(directory, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(directory_fd)
        finally:
            os.close(directory_fd)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("action", choices=("prepare", "verify"))
    parser.add_argument("--config", required=True)
    parser.add_argument("--image", required=True)
    parser.add_argument("--translated", required=True)
    parser.add_argument("--binding", required=True)
    parser.add_argument("--marker")
    args = parser.parse_args()
    current = read_private_json(args.config)
    legacy = validate_current(current, args.image)
    if args.action == "prepare":
        if not args.marker:
            parser.error("prepare requires the installed marker path")
        binding = {"schemaVersion": 1, "image": args.image, "targetId": current["targetId"], "legacyConfig": legacy}
        if os.path.lexists(args.marker) and not os.path.lexists(args.binding):
            raise ValueError("existing unbound predecessor installation cannot be rebound")
        if os.path.lexists(args.binding) and read_private_json(args.binding) != binding:
            raise ValueError("host is already bound to another predecessor or target")
        if os.path.lexists(args.marker) and read_private_json(args.marker) != legacy:
            raise ValueError("existing predecessor marker does not match provisioned target binding")
        write_private_json(args.translated, legacy)
        write_private_json(args.binding, binding)
        return
    if not args.marker:
        parser.error("verify requires the installed marker")
    if read_private_json(args.translated) != legacy:
        raise ValueError("translated configuration changed before target binding")
    if read_private_json(args.binding) != {"schemaVersion": 1, "image": args.image,
                                          "targetId": current["targetId"], "legacyConfig": legacy}:
        raise ValueError("provisioned target binding changed before installation completed")
    marker = read_private_json(args.marker)
    if not isinstance(marker, dict) or set(marker) != LEGACY_FIELDS:
        raise ValueError("installed marker fields do not match the revision-019 schema")
    if marker != legacy:
        raise ValueError("installed marker does not match the exact predecessor configuration")


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, json.JSONDecodeError) as error:
        raise SystemExit(f"revision-019 host compatibility: {error}") from None
