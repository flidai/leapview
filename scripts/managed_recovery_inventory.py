#!/usr/bin/env python3
"""Read only public Hetzner capacity metadata for managed two-host qualification."""

import argparse
from datetime import datetime, timezone
import json
import math
import os
from pathlib import Path
import re
import urllib.error
import urllib.request


API = "https://api.hetzner.cloud/v1"
MAX_BYTES = 1024 * 1024
COLLECTIONS = ("server_types", "locations")


class InventoryFailure(ValueError):
    """Only fixed local categories and a numeric HTTP status may reach logs."""

    def __init__(self, stage, category, collection=None, status=None):
        if stage not in ("authenticate", "read", "summarize") or category not in ("credential", "http", "transport", "size", "json", "schema", "pagination", "no_eligible") or collection not in (*COLLECTIONS, None):
            raise ValueError("invalid inventory diagnostic category")
        diagnostic = f"stage={stage} category={category}"
        if collection is not None:
            diagnostic += f" collection={collection}"
        if type(status) is int and 100 <= status <= 599:
            diagnostic += f" status={status}"
        super().__init__(diagnostic)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def read_capacity(token):
    if not token or any(character.isspace() for character in token):
        raise InventoryFailure("authenticate", "credential")
    client = urllib.request.build_opener(NoRedirect())
    inventory = {}
    # Datacenter endpoints were removed on 2026-10-01. Availability and
    # deprecation are now authoritative per ServerType.locations entry.
    # https://docs.hetzner.cloud/changelog
    for collection in COLLECTIONS:
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
            except urllib.error.HTTPError as error:
                status = error.code
                error.close()
                raise InventoryFailure("read", "http", collection, status) from None
            except urllib.error.URLError:
                raise InventoryFailure("read", "transport", collection) from None
            if len(raw) > MAX_BYTES:
                raise InventoryFailure("read", "size", collection)
            try:
                payload = json.loads(raw)
            except (json.JSONDecodeError, UnicodeError):
                raise InventoryFailure("read", "json", collection) from None
            if not isinstance(payload, dict) or not isinstance(payload.get(collection), list):
                raise InventoryFailure("read", "schema", collection)
            records.extend(payload[collection])
            meta = payload.get("meta", {})
            if not isinstance(meta, dict) or not isinstance(meta.get("pagination", {}), dict):
                raise InventoryFailure("read", "pagination", collection)
            pagination = meta.get("pagination", {})
            next_page = pagination.get("next_page")
            if next_page is None:
                break
            if type(next_page) is not int or next_page != page + 1 or page == 16:
                raise InventoryFailure("read", "pagination", collection)
        inventory[collection] = records
    return summarize(inventory)


def summarize(inventory):
    locations = {}
    names = set()
    for location in inventory["locations"]:
        if not isinstance(location, dict) or type(location.get("id")) is not int or location["id"] <= 0 or not isinstance(location.get("name"), str) or not re.fullmatch(r"[a-z0-9-]{1,32}", location["name"]) or not isinstance(location.get("network_zone"), str) or not re.fullmatch(r"[a-z0-9-]{1,32}", location["network_zone"]):
            raise InventoryFailure("summarize", "schema", "locations")
        if location["id"] in locations or location["name"] in names:
            raise InventoryFailure("summarize", "schema", "locations")
        locations[location["id"]] = location
        names.add(location["name"])
    seen_types = set()
    eligible = []
    for server_type in inventory["server_types"]:
        if not isinstance(server_type, dict) or type(server_type.get("id")) is not int or server_type["id"] <= 0 or not isinstance(server_type.get("name"), str) or not re.fullmatch(r"[a-z0-9-]{1,32}", server_type["name"]) or server_type["id"] in seen_types or not isinstance(server_type.get("locations"), list):
            raise InventoryFailure("summarize", "schema", "server_types")
        seen_types.add(server_type["id"])
        supported_size = type(server_type.get("cores")) is int and 4 <= server_type["cores"] <= 16 and type(server_type.get("memory")) in (int, float) and math.isfinite(server_type["memory"]) and 8 <= server_type["memory"] <= 32
        seen_locations = set()
        for entry in server_type["locations"]:
            if not isinstance(entry, dict) or type(entry.get("id")) is not int or entry["id"] not in locations or entry["id"] in seen_locations or entry.get("name") != locations[entry["id"]]["name"] or type(entry.get("available")) is not bool or "deprecation" not in entry or (entry["deprecation"] is not None and not isinstance(entry["deprecation"], dict)):
                raise InventoryFailure("summarize", "schema", "server_types")
            seen_locations.add(entry["id"])
            location = locations[entry["id"]]
            if server_type.get("architecture") != "x86" or server_type.get("cpu_type") != "shared" or not supported_size or location["network_zone"] != "eu-central" or entry["available"] is not True or entry["deprecation"] is not None:
                continue
            eligible.append({"serverTypeId": server_type["id"], "serverType": server_type["name"], "cores": server_type["cores"], "memoryGiB": server_type["memory"], "locationId": location["id"], "location": location["name"], "networkZone": location["network_zone"]})
    if not eligible:
        raise InventoryFailure("summarize", "no_eligible")
    eligible.sort(key=lambda item: (item["memoryGiB"], item["cores"], item["serverTypeId"], item["locationId"]))
    return {"schemaVersion": 2, "kind": "leapview/managed-recovery-capacity-inventory", "readOnly": True, "minimumHosts": 2, "capacityReserved": False, "fullManagedProfileQualified": False, "observedAt": datetime.now(timezone.utc).isoformat(), "eligible": eligible}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    try:
        report = read_capacity(os.environ.get("HCLOUD_TOKEN", ""))
    except InventoryFailure as error:
        raise SystemExit(f"managed recovery capacity inventory failed ({error}); no qualification claimed") from None
    except (ValueError, TypeError, KeyError):
        raise SystemExit("managed recovery capacity inventory failed; no qualification claimed") from None
    with args.output.open("x", encoding="utf-8") as output:
        json.dump(report, output, sort_keys=True)
        output.write("\n")


if __name__ == "__main__":
    main()
