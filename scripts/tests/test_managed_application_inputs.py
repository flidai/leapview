import copy
import io
import json
import os
from pathlib import Path
import sys
import unittest
from unittest.mock import patch
import zipfile

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import managed_application_inputs as inputs


class ManagedApplicationInputsTest(unittest.TestCase):
    def test_source_history_uses_exact_checkout_without_ambient_git_redirect(self):
        source = Path(__file__).resolve().parents[2]
        previous_cwd = Path.cwd()
        def inspect(*args, **kwargs):
            self.assertEqual(Path.cwd(), source)
            self.assertNotIn("GIT_DIR", os.environ)
            self.assertEqual(os.environ["GIT_CONFIG_VALUE_0"], str(source))
            raise inputs.InputError("stop before import")
        with patch.dict(os.environ, {"GIT_DIR": "/unrelated", "GIT_CONFIG_COUNT": "5"}):
            with patch.object(inputs, "_prepare", side_effect=inspect), self.assertRaises(inputs.InputError):
                inputs.prepare({}, root="/unused", verifier="/unused", source_root=source)
            self.assertEqual(os.environ["GIT_DIR"], "/unrelated")
            self.assertEqual(os.environ["GIT_CONFIG_COUNT"], "5")
        self.assertEqual(Path.cwd(), previous_cwd)

    def selection(self, revision, digest):
        return {"sourceRevision": revision * 40,
                "image": "ghcr.io/flidai/leapview@sha256:" + digest * 64}

    def transition(self, before, after):
        source = {"schema": 58, "migrations": {"001.sql": "a" * 64},
                  "engines": {"duckdb": "v1"}, "rolePolicy": "b" * 64,
                  "permissionProfile": "c" * 64}
        return {"mode": "image-only", "pendingMigrations": [], "imageOnlyEligible": True, "sourceBefore": source,
                "sourceAfter": copy.deepcopy(source)}

    def test_bootstrap_and_both_handoffs_must_have_identical_source_contracts(self):
        selections = {key: self.selection(revision, digest) for key, revision, digest in
                      (("bootstrap", "a", "1"), ("predecessor", "b", "2"), ("candidate", "c", "3"))}
        calls = []
        def inspect(before, after):
            calls.append((before, after))
            return self.transition(before, after)
        result = inputs.compatible_sources(selections, inspect=inspect)
        self.assertEqual(len(calls), 2)
        self.assertEqual(calls[0], ("a" * 40, "b" * 40))
        self.assertEqual(calls[1], ("b" * 40, "c" * 40))
        self.assertEqual(result["sourceBefore"], result["sourceAfter"])
        for change in ("schema", "migrations", "engines", "rolePolicy", "permissionProfile"):
            def changed(before, after):
                transition = self.transition(before, after)
                transition["sourceAfter"][change] = "changed"
                return transition
            with self.subTest(change=change), self.assertRaises(inputs.InputError):
                inputs.compatible_sources(selections, inspect=changed)

    def test_distinct_handoff_images_and_revisions_are_required(self):
        selections = {key: self.selection("a", "1") for key in ("bootstrap", "predecessor", "candidate")}
        with self.assertRaises(inputs.InputError):
            inputs.compatible_sources(selections, inspect=self.transition)
        selections["candidate"]["image"] = self.selection("b", "2")["image"]
        with self.assertRaises(inputs.InputError):
            inputs.compatible_sources(selections, inspect=self.transition)

    def test_image_discovery_requires_authenticated_archive_bytes(self):
        image = self.selection("a", "1")["image"]
        archive = io.BytesIO()
        with zipfile.ZipFile(archive, "w") as bundle:
            bundle.writestr("binding.json", json.dumps({"image": image}))
        data = archive.getvalue()
        import hashlib
        digest = "sha256:" + hashlib.sha256(data).hexdigest()
        self.assertEqual(inputs.discover_image(data, digest), image)
        with self.assertRaises(inputs.InputError):
            inputs.discover_image(data + b"changed", digest)
        with self.assertRaises(inputs.InputError):
            inputs.discover_image(data, "sha256:" + "0" * 64)

    def test_mutable_or_external_repository_images_are_rejected(self):
        for image in ("ghcr.io/flidai/leapview:main", "ghcr.io/other/app@sha256:" + "a" * 64):
            archive = io.BytesIO()
            with zipfile.ZipFile(archive, "w") as bundle:
                bundle.writestr("binding.json", json.dumps({"image": image}))
            data = archive.getvalue()
            import hashlib
            with self.subTest(image=image), self.assertRaises(inputs.InputError):
                inputs.discover_image(data, "sha256:" + hashlib.sha256(data).hexdigest())


if __name__ == "__main__":
    unittest.main()
