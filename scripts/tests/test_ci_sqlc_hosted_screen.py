import copy
import importlib.util
import json
from pathlib import Path
import sys
import tempfile
import unittest

SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
SPEC = importlib.util.spec_from_file_location("hosted", SCRIPTS / "ci_sqlc_hosted_screen.py")
hosted = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(hosted)


class SQLCHostedScreenTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.docker = (SCRIPTS.parent / "Dockerfile").read_text()
        self.script = (SCRIPTS / "generate_build_sources.sh").read_text()

    def test_baseline_retains_complete_recipe_and_original_generators(self):
        recipe, script, paths = hosted.render(self.docker, self.script, "baseline")
        self.assertTrue(recipe.startswith(self.docker))
        self.assertEqual(script, self.script)
        self.assertIn("--target", hosted.__doc__)
        self.assertIn("internal/app/config/spec/names_gen.go", paths)
        self.assertIn("internal/access/ui/signals/models.gen.go", paths)
        self.assertIn("api/gen", paths)
        self.assertIn("web/generated", paths)
        self.assertIn("docs", paths)
        self.assertIn("schemas", paths)
        self.assertIn("FROM scratch AS sqlc-screen-proof", recipe)
        self.assertNotIn(".data/map-assets", paths)

    def test_treatment_changes_only_sqlc_and_additive_stage_copy(self):
        recipe, script, _ = hosted.render(self.docker, self.script, "treatment")
        self.assertEqual(script.replace("/opt/sqlc generate --no-remote", hosted.INVOCATION), self.script)
        tool_stage = recipe[recipe.index("FROM go-deps AS sqlc-tool\n"):recipe.index("FROM go-deps AS sourcegen\n")]
        self.assertIn("source=/go/pkg/mod,sharing=locked", tool_stage)
        self.assertIn("GOTOOLCHAIN=go1.26.7 GOBIN=/out go install", tool_stage)
        restored = recipe.replace(tool_stage, "", 1).replace("COPY --from=sqlc-tool /out/sqlc /opt/sqlc\n", "", 1)
        self.assertTrue(restored.startswith(self.docker))
        self.assertEqual(script.count("/opt/sqlc"), 1)

    def test_recipe_refuses_changed_or_ambiguous_contracts(self):
        for original, script in [
            (self.docker.replace(" AS sourcegen", " AS renamed"), self.script),
            (self.docker + "\nFROM go-deps AS sourcegen\n", self.script),
            (self.docker.replace(" AS runtime\n", " AS other\n"), self.script),
            (self.docker.replace("COPY --from=sourcegen /src/api/gen ./api/gen", "COPY --from=sourcegen /outside ./api/gen"), self.script),
            (self.docker, self.script.replace(hosted.INVOCATION, "sqlc generate")),
            (self.docker, self.script + "\n" + hosted.INVOCATION),
        ]:
            with self.subTest(original=original[:20]), self.assertRaises(ValueError):
                hosted.render(original, script, "treatment")

    def test_manifest_covers_files_and_directories_and_rejects_unknowns(self):
        source = self.root / "generated"
        (source / "api/gen").mkdir(parents=True)
        (source / "api/gen/schema.json").write_text("{}")
        (source / "names.go").write_text("package spec")
        paths = ["api/gen", "names.go"]
        result = hosted.manifest(source, paths)
        self.assertEqual(set(result), {"api/gen/schema.json", "names.go"})
        (source / "extra.txt").write_text("extra")
        with self.assertRaises(ValueError):
            hosted.manifest(source, paths)
        (source / "extra.txt").unlink()
        (source / "names.go").unlink()
        with self.assertRaises(ValueError):
            hosted.manifest(source, paths)
        (source / "names.go").symlink_to(source / "api/gen/schema.json")
        with self.assertRaises(ValueError):
            hosted.manifest(source, paths)

    def test_receipt_comparison_rejects_source_or_generated_drift(self):
        left = {"status": "passed", "source": "a" * 40, "archive_sha256": "b" * 64,
                "original_dockerfile_sha256": "c" * 64, "original_script_sha256": "d" * 64,
                "paths": ["api/gen"], "manifest": {"api/gen/a.go": "e" * 64}}
        right = copy.deepcopy(left)
        hosted.compare(left, right)
        for key, value in [("status", "prepared"), ("source", "f" * 40),
                           ("paths", []), ("manifest", {}), ("manifest", {"api/gen/a.go": "f" * 64})]:
            changed = copy.deepcopy(right)
            changed[key] = value
            with self.subTest(key=key), self.assertRaises(ValueError):
                hosted.compare(left, changed)

    def test_proof_export_requires_cached_generation_without_execution(self):
        header = "#31 [sourcegen 6/7] RUN --mount=type=cache ./scripts/generate_build_sources.sh && generate_docs\n"
        good = header + "#31 CACHED\n"
        self.assertEqual(hosted.export_proof(good)["sourcegen_vertex"], "31")
        for bad in ["", header, good.replace("#31 CACHED", "#32 CACHED"),
                    good + "#31 1.0 build_phase=sqlc elapsed_seconds=1 exit_code=0\n",
                    good + header.replace("#31", "#32") + "#32 CACHED\n"]:
            with self.subTest(log=bad), self.assertRaises(ValueError):
                hosted.export_proof(bad)
        self.assertEqual(hosted.export_proof(header + good)["sourcegen_vertex"], "31")

    def test_build_log_requires_executed_sqlc_and_correct_cache_vertex(self):
        log = "#39 1.1 build_phase=sqlc elapsed_seconds=1 exit_code=0\n"
        tool = "#15 [sqlc-tool 2/2] RUN env go install " + hosted.local.SQLC + "\n#15 CACHED\n"
        result = hosted.build_proof(log + tool, "treatment", "consumer")
        self.assertTrue(result["tool_cache_proof"]["cached"])
        for bad in [tool, log + log + tool, log.replace("exit_code=0", "exit_code=1") + tool,
                    log + tool.replace("#15 CACHED", "#16 CACHED")]:
            with self.assertRaises(ValueError):
                hosted.build_proof(bad, "treatment", "consumer")


if __name__ == "__main__":
    unittest.main()
