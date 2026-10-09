#!/usr/bin/env python3
"""Read only public Hetzner capacity metadata for managed two-host qualification."""

import argparse
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import re
import urllib.error
import urllib.request


API = "https://api.hetzner.cloud/v1"
MAX_BYTES = 1024 * 1024


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def read_capacity(token):
    if not token or any(character.isspace() for character in token):
        raise ValueError("explicit protected infrastructure credential required")
    client = urllib.request.build_opener(NoRedirect())
    inventory = {}
    for collection in ("server_types", "locations", "datacenters"):
        records = []
        for page in range(1, 17):
            request = urllib.request.Request(
                f"{API}/{collection}?per_page=50&page={page}",
                headers={"Authorization": "Bearer " + token},
                method="GET",
            )
            try:
                with client.open(request, timeout=20) as response:
                    raw = response.read(MAX_BYTES + 1)
            except (urllib.error.HTTPError, urllib.error.URLError):
                raise ValueError("protected infrastructure capacity read failed") from None
            if len(raw) > MAX_BYTES:
                raise ValueError("infrastructure capacity response exceeds bound")
            payload = json.loads(raw)
            if not isinstance(payload, dict) or not isinstance(payload.get(collection), list):
                raise ValueError("invalid infrastructure capacity response")
            records.extend(payload[collection])
            pagination = payload.get("meta", {}).get("pagination", {})
            next_page = pagination.get("next_page")
            if next_page is None:
                break
            if type(next_page) is not int or next_page != page + 1 or page == 16:
                raise ValueError("infrastructure pagination exceeds bound or is inconsistent")
        inventory[collection] = records
    return summarize(inventory)


def summarize(inventory):
    locations = {}
    for location in inventory["locations"]:
        if type(location.get("id")) is not int or not re.fullmatch(r"[a-z0-9-]{1,32}", location.get("name", "")):
            raise ValueError("invalid infrastructure location identity")
        locations[location["id"]] = location["name"]
    candidates = {}
    for server_type in inventory["server_types"]:
        if server_type.get("architecture") != "x86" or server_type.get("cpu_type") != "shared" or server_type.get("deprecation") is not None:
            continue
        if type(server_type.get("cores")) is not int or not 4 <= server_type["cores"] <= 16 or type(server_type.get("memory")) not in (int, float) or not 8 <= server_type["memory"] <= 32:
            continue
        if type(server_type.get("id")) is not int or not re.fullmatch(r"[a-z0-9-]{1,32}", server_type.get("name", "")):
            raise ValueError("invalid infrastructure server type identity")
        candidates[server_type["id"]] = server_type
    eligible = []
    for datacenter in inventory["datacenters"]:
        location = datacenter.get("location", {})
        if location.get("id") not in locations or location.get("network_zone") != "eu-central":
            continue
        available = datacenter.get("server_types", {}).get("available", [])
        if not isinstance(available, list) or any(type(value) is not int for value in available):
            raise ValueError("invalid available infrastructure server type identities")
        if not re.fullmatch(r"[a-z0-9-]{1,64}", datacenter.get("name", "")):
            raise ValueError("invalid datacenter identity")
        for identity in available:
            if identity in candidates:
                server_type = candidates[identity]
                eligible.append({"serverTypeId": identity, "serverType": server_type["name"], "cores": server_type["cores"], "memoryGiB": server_type["memory"], "location": locations[location["id"]], "datacenter": datacenter["name"]})
    if not eligible:
        raise ValueError("no supported disposable x86 managed-host capacity advertised")
    eligible.sort(key=lambda item: (item["memoryGiB"], item["cores"], item["serverTypeId"], item["datacenter"]))
    return {"schemaVersion": 1, "kind": "leapview/managed-recovery-capacity-inventory", "readOnly": True, "minimumHosts": 2, "capacityReserved": False, "fullManagedProfileQualified": False, "observedAt": datetime.now(timezone.utc).isoformat(), "eligible": eligible}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    try:
        report = read_capacity(os.environ.get("HCLOUD_TOKEN", ""))
    except (ValueError, TypeError, KeyError, json.JSONDecodeError):
        raise SystemExit("managed recovery capacity inventory failed; no qualification claimed") from None
    with args.output.open("x", encoding="utf-8") as output:
        json.dump(report, output, sort_keys=True)
        output.write("\n")


if __name__ == "__main__":
    main()
