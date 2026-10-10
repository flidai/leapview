"""Transport checks only; fixture tests grant no installed-host authority."""
import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from urllib.parse import parse_qs, urlsplit

spec = importlib.util.spec_from_file_location("coordinator_fixture", Path(__file__).with_name("managed-coordinator-fixture.py"))
fixture = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fixture)


class CoordinatorFixtureTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.producer = self.root / "producer"
        self.producer.write_bytes(b"actual fixture executable bytes")
        self.export = self.root / "export"
        self.export.mkdir(mode=0o700)
        self.value = {"schemaVersion": 1, "scope": "disposable-installed-coordinator-component",
                      "sourceRevision": "0123456789abcdef0123456789abcdef01234567",
                      "producerSHA256": fixture.sha256_file(self.producer),
                      "activationQualified": False, "releaseAdmissionQualified": False,
                      "fullManagedProfileQualified": False}
        self.write("keyring", b"private customer keyring")
        self.refresh()

    def write(self, name, data):
        path = self.export / name
        path.write_bytes(data)
        path.chmod(0o600)

    def refresh(self):
        payload = json.dumps(self.value).encode()
        self.write("export.json", payload)
        self.write("export.sha256", ("sha256:" + hashlib.sha256(payload).hexdigest()).encode())
        files = [{"path": p.name, "size": p.stat().st_size, "sha256": fixture.sha256_file(p)[7:]}
                 for p in sorted(self.export.iterdir()) if p.name not in ("bundle-manifest.json", "bundle.sha256")]
        manifest = json.dumps({"schemaVersion": 1, "files": files}).encode()
        self.write("bundle-manifest.json", manifest)
        self.write("bundle.sha256", ("sha256:" + hashlib.sha256(manifest).hexdigest()).encode())

    def test_exact_private_fixture_bytes_and_producer_verify(self):
        self.assertEqual(fixture.verified_export(self.export, self.producer), self.value)

    def test_substituted_producer_is_rejected(self):
        self.producer.write_bytes(b"another executable")
        with self.assertRaises(ValueError):
            fixture.verified_export(self.export, self.producer)

    def test_changed_or_extra_file_is_rejected(self):
        for kind in ("changed", "extra"):
            with self.subTest(kind=kind):
                self.refresh()
                if kind == "changed":
                    self.write("keyring", b"wrong same source")
                else:
                    self.write("foreign", b"unretained source")
                with self.assertRaises(ValueError):
                    fixture.verified_export(self.export, self.producer)

    def test_symlink_or_public_input_is_rejected(self):
        original = self.export / "export.json"
        original.chmod(0o644)
        with self.assertRaises(ValueError):
            fixture.verified_export(self.export, self.producer)
        original.chmod(0o600)
        original.rename(self.export / "aside")
        original.symlink_to("aside")
        with self.assertRaises(ValueError):
            fixture.verified_export(self.export, self.producer)

    def test_manifest_path_cannot_escape_private_tree(self):
        manifest = json.loads((self.export / "bundle-manifest.json").read_bytes())
        manifest["files"][0]["path"] = "../producer"
        data = json.dumps(manifest).encode()
        self.write("bundle-manifest.json", data)
        self.write("bundle.sha256", ("sha256:" + hashlib.sha256(data).hexdigest()).encode())
        with self.assertRaises(ValueError):
            fixture.verified_export(self.export, self.producer)

    def test_export_cannot_claim_any_release_or_activation_gate(self):
        for key in ("activationQualified", "releaseAdmissionQualified", "fullManagedProfileQualified"):
            with self.subTest(key=key):
                self.value[key] = True
                self.refresh()
                with self.assertRaises(ValueError):
                    fixture.verified_export(self.export, self.producer)
                self.value[key] = False

    def test_retargeting_preserves_credentials_and_strict_server_tls(self):
        raw = "postgresql://runtime:a%40b%2Fc@old.example:5544/leapview_control?sslmode=require&sslrootcert=old&options=foreign"
        result = urlsplit(fixture.tls_url(raw, "database.local:5432", "/private/ca.crt"))
        self.assertEqual(result.netloc, "runtime:a%40b%2Fc@database.local:5432")
        self.assertEqual(result.path, "/leapview_control")
        self.assertEqual(parse_qs(result.query), {"sslmode": ["verify-full"], "sslrootcert": ["/private/ca.crt"]})
        with self.assertRaises(ValueError):
            fixture.tls_url("postgres://runtime@old/db", "replacement")

    def test_receipt_requires_real_original_fence_and_keeps_profile_closed(self):
        receipt = {"schemaVersion": 1, "kind": "leapview/managed-recovery-preactivation-qualification",
                   "scope": "fresh-managed-coordinator-replay-admission", "activationQualified": False,
                   "fullManagedProfileQualified": False, "admission": {"originalsFenced": True, "activationQualified": False}}
        self.assertEqual(fixture.verify_component_receipt(receipt), receipt)
        receipt["admission"]["originalsFenced"] = False
        with self.assertRaises(ValueError):
            fixture.verify_component_receipt(receipt)


if __name__ == "__main__":
    unittest.main()
