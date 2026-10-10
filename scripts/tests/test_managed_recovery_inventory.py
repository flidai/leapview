import copy
import io
import json
import sys
import tempfile
from pathlib import Path
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import managed_recovery_inventory as inventory


class ManagedRecoveryInventoryTests(unittest.TestCase):
    def fixture(self):
        # Official ServerTypeLocation is flat: id/name identify the Location.
        return {"locations": [{"id": 1, "name": "fsn1", "network_zone": "eu-central"}], "server_types": [{"id": 2, "name": "cx33", "architecture": "x86", "cpu_type": "shared", "cores": 4, "memory": 8, "locations": [{"id": 1, "name": "fsn1", "available": True, "deprecation": None}]}]}

    def test_only_public_eligible_capacity_is_returned(self):
        data = self.fixture()
        data["server_types"][0]["prices"] = [{"token": "must-not-export"}]
        report = inventory.summarize(data)
        self.assertEqual(report["schemaVersion"], 2)
        self.assertFalse(report["capacityReserved"])
        self.assertFalse(report["fullManagedProfileQualified"])
        self.assertEqual(report["minimumHosts"], 2)
        self.assertEqual(report["eligible"][0]["serverTypeId"], 2)
        self.assertEqual(report["eligible"][0]["locationId"], 1)
        self.assertNotIn("datacenter", report["eligible"][0])
        self.assertNotIn("must-not-export", str(report))

    def test_unavailable_deprecated_or_wrong_architecture_denied(self):
        for mutation in ("unavailable", "deprecated", "arm", "oversized", "wrong-zone"):
            with self.subTest(mutation=mutation):
                data = copy.deepcopy(self.fixture())
                if mutation == "unavailable":
                    data["server_types"][0]["locations"][0]["available"] = False
                elif mutation == "deprecated":
                    data["server_types"][0]["locations"][0]["deprecation"] = {"announced": "today"}
                elif mutation == "arm":
                    data["server_types"][0]["architecture"] = "arm"
                elif mutation == "oversized":
                    data["server_types"][0]["memory"] = 64
                else:
                    data["locations"][0]["network_zone"] = "other"
                with self.assertRaises(ValueError):
                    inventory.summarize(data)

    def test_no_ambient_missing_credential_or_redirect_authority(self):
        with patch.object(inventory.urllib.request, "build_opener") as opener:
            for token in ("", "header\ninjection"):
                with self.assertRaises(ValueError):
                    inventory.read_capacity(token)
            opener.assert_not_called()
        self.assertIsNone(inventory.NoRedirect().redirect_request(None, None, 302, None, None, "https://foreign.example"))

    def test_invalid_capacity_identifiers_denied(self):
        for field, value in (("id", "1"), ("id", 99), ("name", "hel1"), ("available", "true")):
            with self.subTest(field=field, value=value):
                data = self.fixture()
                data["server_types"][0]["locations"][0][field] = value
                with self.assertRaises(ValueError):
                    inventory.summarize(data)

    def test_location_availability_and_deprecation_are_independent(self):
        data = self.fixture()
        data["locations"].append({"id": 3, "name": "hel1", "network_zone": "eu-central"})
        data["server_types"][0]["locations"].append({"id": 3, "name": "hel1", "available": True, "deprecation": None})
        data["server_types"][0]["locations"][0]["deprecation"] = {"announced": "2026-10-01"}
        # Removed global deprecation is not used as an authority/fallback.
        data["server_types"][0]["deprecation"] = {"announced": "legacy"}
        report = inventory.summarize(data)
        self.assertEqual([item["location"] for item in report["eligible"]], ["hel1"])

    def test_missing_modern_fields_and_ambiguous_identity_fail_closed(self):
        for mutation in ("locations", "available", "deprecation", "duplicate-location", "duplicate-type", "duplicate-pair"):
            with self.subTest(mutation=mutation):
                data = self.fixture()
                if mutation == "locations":
                    del data["server_types"][0]["locations"]
                    data["datacenters"] = [{"server_types": {"available": [2]}}]
                elif mutation in ("available", "deprecation"):
                    del data["server_types"][0]["locations"][0][mutation]
                elif mutation == "duplicate-location":
                    data["locations"].append(copy.deepcopy(data["locations"][0]))
                elif mutation == "duplicate-type":
                    data["server_types"].append(copy.deepcopy(data["server_types"][0]))
                else:
                    data["server_types"][0]["locations"].append(copy.deepcopy(data["server_types"][0]["locations"][0]))
                with self.assertRaises(ValueError):
                    inventory.summarize(data)

    def response(self, body):
        return io.BytesIO(json.dumps(body).encode())

    def test_current_endpoint_requests_are_get_only_without_removed_fallback(self):
        data = self.fixture()
        with patch.object(inventory.urllib.request, "build_opener") as opener:
            opener.return_value.open.side_effect = [self.response({"server_types": data["server_types"]}), self.response({"locations": data["locations"]})]
            report = inventory.read_capacity("credential-never-export")
        requests = [call.args[0] for call in opener.return_value.open.call_args_list]
        self.assertEqual([request.full_url for request in requests], [inventory.API + "/server_types?per_page=50&page=1", inventory.API + "/locations?per_page=50&page=1"])
        self.assertTrue(all(request.get_method() == "GET" for request in requests))
        self.assertNotIn("credential-never-export", str(report))

    def test_safe_http_diagnostic_and_no_output_on_failure(self):
        secret = "must-not-print-secret"
        error = inventory.urllib.error.HTTPError("https://foreign/" + secret, 403, secret, {"Authorization": secret}, self.response({"error": secret}))
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "report.json"
            with patch.object(inventory.urllib.request, "build_opener") as opener, patch.object(sys, "argv", ["inventory", str(output)]), patch.dict(inventory.os.environ, {"HCLOUD_TOKEN": secret}):
                opener.return_value.open.side_effect = error
                with self.assertRaises(SystemExit) as caught:
                    inventory.main()
            diagnostic = str(caught.exception)
            self.assertIn("stage=read", diagnostic)
            self.assertIn("category=http", diagnostic)
            self.assertIn("collection=server_types", diagnostic)
            self.assertIn("status=403", diagnostic)
            self.assertNotIn(secret, diagnostic)
            self.assertNotIn("https://", diagnostic)
            self.assertFalse(output.exists())

    def test_response_and_pagination_bounds_remain_closed(self):
        for body in ({"server_types": [], "meta": {"pagination": {"next_page": 3}}}, {"server_types": {}, "meta": {}}, {"server_types": [], "meta": "not-an-envelope"}):
            with self.subTest(body=body), patch.object(inventory.urllib.request, "build_opener") as opener:
                opener.return_value.open.return_value = self.response(body)
                with self.assertRaises(ValueError):
                    inventory.read_capacity("credential")
        with patch.object(inventory.urllib.request, "build_opener") as opener:
            opener.return_value.open.return_value = io.BytesIO(b" " * (inventory.MAX_BYTES + 1))
            with self.assertRaises(ValueError):
                inventory.read_capacity("credential")

    def test_valid_pagination_is_retained_and_page_limit_is_enforced(self):
        data = self.fixture()
        first = {"server_types": [], "meta": {"pagination": {"next_page": 2}}}
        second = {"server_types": data["server_types"], "meta": {"pagination": {"next_page": None}}}
        with patch.object(inventory.urllib.request, "build_opener") as opener:
            opener.return_value.open.side_effect = [self.response(first), self.response(second), self.response({"locations": data["locations"]})]
            report = inventory.read_capacity("credential")
        self.assertEqual(report["eligible"][0]["serverType"], "cx33")
        self.assertTrue(opener.return_value.open.call_args_list[1].args[0].full_url.endswith("/server_types?per_page=50&page=2"))
        with patch.object(inventory.urllib.request, "build_opener") as opener:
            opener.return_value.open.side_effect = [self.response({"server_types": [], "meta": {"pagination": {"next_page": page + 1}}}) for page in range(1, 17)]
            with self.assertRaises(inventory.InventoryFailure) as caught:
                inventory.read_capacity("credential")
            self.assertIn("category=pagination", str(caught.exception))
            self.assertEqual(opener.return_value.open.call_count, 16)

    def test_transport_and_invalid_json_diagnostics_do_not_echo_input(self):
        secret = "must-not-print-secret"
        for category, response in (("transport", inventory.urllib.error.URLError(secret)), ("json", io.BytesIO(("invalid-json-" + secret).encode()))):
            with self.subTest(category=category), patch.object(inventory.urllib.request, "build_opener") as opener:
                if isinstance(response, Exception):
                    opener.return_value.open.side_effect = response
                else:
                    opener.return_value.open.return_value = response
                with self.assertRaises(inventory.InventoryFailure) as caught:
                    inventory.read_capacity(secret)
                diagnostic = str(caught.exception)
                self.assertIn("category=" + category, diagnostic)
                self.assertNotIn(secret, diagnostic)


if __name__ == "__main__":
    unittest.main()
