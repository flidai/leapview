import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location(
    "screen", Path(__file__).resolve().parents[1] / "ci_sqlc_layer_screen.py")
screen = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(screen)


class SQLCLayerScreenTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.outputs = ["internal/project/postgres/internal/db"]

    def generated(self, name, content=b"package db\n"):
        root = self.root / name
        path = root / self.outputs[0] / "queries.sql.go"
        path.parent.mkdir(parents=True)
        path.write_bytes(content)
        return root

    def test_manifests_compare_content_and_complete_path_set(self):
        left, right = self.generated("left"), self.generated("right")
        expected = screen.manifest(left, self.outputs)
        screen.require_equal(expected, screen.manifest(right, self.outputs))
        file = right / self.outputs[0] / "queries.sql.go"
        file.write_text("package changed\n")
        with self.assertRaises(ValueError):
            screen.require_equal(expected, screen.manifest(right, self.outputs))
        file.unlink()
        with self.assertRaises(ValueError):
            screen.manifest(right, self.outputs)
        file.write_text("package db\n")
        file.with_name("extra.go").write_text("package db\n")
        with self.assertRaises(ValueError):
            screen.require_equal(expected, screen.manifest(right, self.outputs))

    def test_manifest_rejects_escape_symlink_and_unexpected_file(self):
        for name, kind in [("symlink", "link"), ("extra", "file")]:
            root = self.generated(name)
            bad = root / "unexpected"
            if kind == "link":
                bad.symlink_to(self.root)
            else:
                bad.write_text("unexpected")
            with self.assertRaises(ValueError):
                screen.manifest(root, self.outputs)

    def test_output_inventory_is_narrow_and_fail_closed(self):
        valid = '        out: "internal/project/postgres/internal/db"\n'
        self.assertEqual(screen.output_paths(valid), self.outputs)
        for value in ["", valid + valid, '        out: "../escape"\n',
                      '        out: "/absolute/internal/db"\n',
                      '        out: "internal/a/../internal/db"\n',
                      '        out: unquoted\n', '        out: "internal/a/internal/db" # unexpected\n']:
            with self.subTest(value=value), self.assertRaises(ValueError):
                screen.output_paths(value)

    def test_inputs_require_immutable_pins_and_new_external_output(self):
        image = "moby/buildkit:v0.33.0@sha256:" + "a" * 64
        screen.validate_inputs(self.root, "a" * 40, self.root.parent / "fresh-screen", image, 3)
        for source, output, pin, pairs in [
            ("main", self.root.parent / "fresh-screen", image, 3),
            ("a" * 40, self.root, image, 3),
            ("a" * 40, self.root / "inside", image, 3),
            ("a" * 40, self.root.parent / "fresh-screen", "moby/buildkit:latest", 3),
            ("a" * 40, self.root.parent / "fresh-screen", image, 0),
        ]:
            with self.subTest(source=source, output=output, pin=pin), self.assertRaises(ValueError):
                screen.validate_inputs(self.root, source, output, pin, pairs)

    def test_dockerfile_keeps_tool_layer_independent_and_versioned(self):
        docker = "# syntax=docker/dockerfile:1.7@sha256:" + "a" * 64 + "\n"
        docker += "FROM golang:1.27.2-bookworm@sha256:" + "b" * 64 + " AS go-deps\n"
        rendered = screen.dockerfile(docker, self.outputs)
        self.assertIn("GODEBUG=http2client=0 GOTOOLCHAIN=go1.26.9 GOBIN=/out go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1", rendered)
        self.assertLess(rendered.index("AS sqlc-tool"), rendered.index("COPY . ."))
        self.assertIn("COPY --from=sqlc-tool /out/sqlc /opt/sqlc", rendered)
        self.assertIn("go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate --no-remote", rendered)
        self.assertIn("/opt/sqlc generate --no-remote", rendered)
        self.assertIn("FROM go-deps AS sqlc-tool", rendered)
        tool = rendered.split("AS sqlc-tool\n", 1)[1].split("FROM go-deps AS inputs", 1)[0]
        self.assertIn("source=/go/pkg/mod,sharing=locked", tool)
        self.assertIn("/identity/environment.json", tool)
        treatment = rendered.split("FROM inputs AS treatment\n", 1)[1]
        self.assertNotIn("go env", treatment)
        with self.assertRaises(ValueError):
            screen.dockerfile(docker.replace("@sha256:", "@invalid:"), self.outputs)

    def test_cache_proof_binds_the_actual_tool_install_vertex(self):
        header = "#17 [sqlc-tool 2/2] RUN env GOBIN=/out go install " + screen.SQLC
        self.assertTrue(screen.tool_cache_proof(header + "\n#17 CACHED\n")["cached"])
        self.assertFalse(screen.tool_cache_proof(header + "\n#18 CACHED\n#17 DONE 43.1s\n")["cached"])
        with self.assertRaises(ValueError):
            screen.tool_cache_proof("#17 [inputs 1/1] COPY . .\n#17 CACHED\n")

    def test_cache_proof_accepts_repeated_progress_for_one_vertex_only(self):
        header = "#14 [sqlc-tool 2/2] RUN env GOBIN=/out go install " + screen.SQLC
        log = header + "\n#14 extracting layer\n#14 ...\n" + header + "\n#14 CACHED\n"
        proof = screen.tool_cache_proof(log)
        self.assertEqual(proof["vertex"], "14")
        self.assertTrue(proof["cached"])
        with self.assertRaises(ValueError):
            screen.tool_cache_proof(log + header.replace("#14", "#15") + "\n#15 CACHED\n")

    def test_context_digest_tracks_source_content_and_executable_mode(self):
        context = self.root / "context"
        context.mkdir()
        source = context / "source"
        source.write_text("first")
        before = screen.context_digest(context)
        source.write_text("second")
        self.assertNotEqual(before, screen.context_digest(context))
        before = screen.context_digest(context)
        source.chmod(0o755)
        self.assertNotEqual(before, screen.context_digest(context))

    def test_build_failure_retains_receipt_and_removes_only_owned_builder(self):
        receipt = {"commands": []}
        runner = screen.Runner(self.root, receipt)
        invocations = []

        def execute(args, **kwargs):
            invocations.append(args)
            return type("Result", (), {"returncode": 19 if "build" in args else 0})()

        with patch.object(screen.subprocess, "run", side_effect=execute):
            with self.assertRaises(RuntimeError):
                screen.build(runner, self.root, self.root / "Dockerfile", "moby/buildkit@sha256:" + "a" * 64,
                             "baseline", "producer", None, self.root / "cache")
        create, cleanup = invocations[0], invocations[-1]
        name = create[create.index("--name") + 1]
        self.assertTrue(name.startswith("leapview-sqlc-screen-"))
        self.assertEqual(cleanup, ["docker", "buildx", "rm", "--force", name])
        self.assertFalse(any("prune" in command for command in invocations))
        persisted = json.loads((self.root / "receipt.json").read_text())
        self.assertEqual(persisted["commands"][-2]["exit_code"], 19)
        self.assertEqual(persisted["commands"][-1]["exit_code"], 0)


if __name__ == "__main__":
    unittest.main()
