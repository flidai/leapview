import copy
import sys
from pathlib import Path
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import managed_recovery_inventory as inventory


class ManagedRecoveryInventoryTests(unittest.TestCase):
    def fixture(self):
        return {"locations": [{"id": 1, "name": "fsn1"}], "server_types": [{"id": 2, "name": "cx33", "architecture": "x86", "cpu_type": "shared", "cores": 4, "memory": 8, "deprecation": None}], "datacenters": [{"name": "fsn1-dc14", "location": {"id": 1, "network_zone": "eu-central"}, "server_types": {"available": [2]}}]}

    def test_only_public_eligible_capacity_is_returned(self):
        data = self.fixture()
        data["server_types"][0]["prices"] = [{"token": "must-not-export"}]
        report = inventory.summarize(data)
        self.assertFalse(report["capacityReserved"])
        self.assertFalse(report["fullManagedProfileQualified"])
        self.assertEqual(report["minimumHosts"], 2)
        self.assertEqual(report["eligible"][0]["serverTypeId"], 2)
        self.assertNotIn("must-not-export", str(report))

    def test_unavailable_deprecated_or_wrong_architecture_denied(self):
        for mutation in ("unavailable", "deprecated", "arm", "oversized", "wrong-zone"):
            with self.subTest(mutation=mutation):
                data = copy.deepcopy(self.fixture())
                if mutation == "unavailable":
                    data["datacenters"][0]["server_types"]["available"] = []
                elif mutation == "deprecated":
                    data["server_types"][0]["deprecation"] = {"announced": "today"}
                elif mutation == "arm":
                    data["server_types"][0]["architecture"] = "arm"
                elif mutation == "oversized":
                    data["server_types"][0]["memory"] = 64
                else:
                    data["datacenters"][0]["location"]["network_zone"] = "other"
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
        data = self.fixture()
        data["datacenters"][0]["server_types"]["available"] = ["2"]
        with self.assertRaises(ValueError):
            inventory.summarize(data)


if __name__ == "__main__":
    unittest.main()
