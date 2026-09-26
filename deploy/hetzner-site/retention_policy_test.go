package hetznersite_test

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRetentionHelperPurePolicy(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	program := `
import importlib.util, json, pathlib, sys, tempfile
sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location("retention", sys.argv[1])
r = importlib.util.module_from_spec(spec)
spec.loader.exec_module(r)

def digest(value):
    return "ghcr.io/flidai/leapview-site@sha256:" + value * 64

def image(value, tags=None, digests=None):
    return {"Id": "sha256:" + value * 64, "RepoTags": tags or [], "RepoDigests": digests or [digest(value)]}

active, old, candidate, stale = (digest(c) for c in "abcd")
caddy = "caddy@sha256:" + "e" * 64
state = {
    "active": active, "deployed": active, "previous": old, "caddy": caddy,
    "first_install": False, "transaction": None,
    "references": {active: [("active", active), ("deployed", active)], old: [("rollback", old)], caddy: [("configured Caddy image", caddy)]},
}
images = [image(c) for c in "abcd"] + [image("e", ["caddy:2"], [caddy]), image("f", ["foreign/app:1"], ["example.org/foreign/app@sha256:" + "f" * 64])]
container_id = "sha256:" + "f" * 64
containers = [{"Id": "sha256:" + "1" * 64, "Image": container_id, "Config": {"Image": "foreign/app:1", "Labels": {}}, "State": {"Running": False}}]
plan = r.build_plan(images, containers, state, candidate, require_candidate=True)
assert [row["id"] for row in plan["proposed_removals"]] == ["sha256:" + "d" * 64], plan
assert {row["id"] for row in plan["protected"]} >= {"sha256:" + c * 64 for c in "abce"}, plan

# A local running Compose site must agree with deployment evidence and resolve
# its configured immutable reference to the exact inspected image ID.
site_container = {"Id": "sha256:" + "2" * 64, "Image": "sha256:" + "a" * 64,
    "Config": {"Image": active, "Labels": {"com.docker.compose.project": "leapview-site", "com.docker.compose.service": "leapview-site"}},
    "State": {"Running": True}}
r.build_plan(images, [site_container], state)
bad = dict(site_container)
bad["Config"] = {"Image": old, "Labels": site_container["Config"]["Labels"]}
try:
    r.build_plan(images, [bad], state)
    raise AssertionError("contradictory running site identity was accepted")
except r.RetentionError as exc:
    assert exc.code == r.EXIT_UNSAFE
try:
    r.build_plan(images, [site_container, site_container], state)
    raise AssertionError("multiple running site containers were accepted")
except r.RetentionError as exc:
    assert exc.code == r.EXIT_UNSAFE

# Orphan scratch envs do not pin images once their transaction is gone. A
# matching scratch is recorded while its transaction is still unresolved.
with tempfile.TemporaryDirectory() as temp:
    root = pathlib.Path(temp)
    (root / "deployment.env").write_text(f"LEAPVIEW_SITE_IMAGE={active}\nCADDY_IMAGE={caddy}\n")
    (root / "deployed-image").write_text(active + "\n")
    (root / "previous-image").write_text(old + "\n")
    (root / "deployment.env.next.orphan").write_text(f"LEAPVIEW_SITE_IMAGE={candidate}\nCADDY_IMAGE={caddy}\n")
    loaded = r.load_state(root)
    r._read_transaction_scratch(root, loaded)
    assert candidate not in loaded["references"], loaded
    (root / "deployment-in-progress").write_text(f"version=1\nprevious_image={active}\ncandidate_image={candidate}\nphase=prepared\n")
    (root / "deployment.env.next.current").write_text(f"LEAPVIEW_SITE_IMAGE={candidate}\nCADDY_IMAGE={caddy}\n")
    loaded = r.load_state(root)
    r._read_transaction_scratch(root, loaded)
    assert any("current transaction recovery file" in reason for reason, _ in loaded["references"][candidate]), loaded

# References are a single canonical line; whitespace folding and extra lines
# must not turn malformed state into an accepted image identity.
with tempfile.TemporaryDirectory() as temp:
    path = pathlib.Path(temp) / "deployed-image"
    path.write_text(active + "  \n")
    try:
        r._one_reference_file(path, "deployed-image", r.SITE_DIGEST)
        raise AssertionError("trailing spaces were accepted")
    except r.RetentionError:
        pass
    path.write_text(active + "\n\n")
    try:
        r._one_reference_file(path, "deployed-image", r.SITE_DIGEST)
        raise AssertionError("extra reference line was accepted")
    except r.RetentionError:
        pass
    env = pathlib.Path(temp) / "deployment.env"
    env.write_text(f"LEAPVIEW_SITE_IMAGE={active}\nCADDY_IMAGE={caddy}\nEXTRA=value\n")
    try:
        r._deployment_environment(env)
        raise AssertionError("unexpected deployment.env setting was accepted")
    except r.RetentionError as exc:
        assert exc.code == r.EXIT_UNSAFE
    env.write_text(f"LEAPVIEW_SITE_IMAGE={active}\n\nCADDY_IMAGE={caddy}\n")
    try:
        r._deployment_environment(env)
        raise AssertionError("blank deployment.env line was accepted")
    except r.RetentionError as exc:
        assert exc.code == r.EXIT_UNSAFE

# Recovery phase/state mismatches fail closed, including the old rollback-failed
# plus candidate/candidate combination that must never be finalized as success.
with tempfile.TemporaryDirectory() as temp:
    root = pathlib.Path(temp)
    (root / "deployment.env").write_text(f"LEAPVIEW_SITE_IMAGE={candidate}\nCADDY_IMAGE={caddy}\n")
    (root / "deployed-image").write_text(candidate + "\n")
    (root / "previous-image").write_text(old + "\n")
    (root / "deployment-in-progress").write_text(f"version=1\nprevious_image={active}\ncandidate_image={candidate}\nphase=rollback-failed\n")
    try:
        r.load_state(root)
        raise AssertionError("rollback-failed candidate state was accepted")
    except r.RetentionError as exc:
        assert exc.code == r.EXIT_UNSAFE

# Docker's active image backend selects the filesystem to measure. A stale
# /var/lib/containerd directory must not override legacy DockerRootDir.
original_docker = r._docker
original_containerd = r._containerd_storage_root
original_exists, original_is_dir = pathlib.Path.exists, pathlib.Path.is_dir
try:
    pathlib.Path.exists = lambda self: True
    pathlib.Path.is_dir = lambda self: True
    r._containerd_storage_root = lambda: pathlib.Path("/var/lib/containerd")
    r._docker = lambda *args: json.dumps([["driver-type", "io.containerd.snapshotter.v1"]]) if "DriverStatus" in args[-1] else "/var/lib/docker\n"
    assert r._storage_path(None) == pathlib.Path("/var/lib/containerd")
    r._docker = lambda *args: json.dumps([["Backing Filesystem", "extfs"]]) if "DriverStatus" in args[-1] else "/mnt/docker-root\n"
    assert r._storage_path(None) == pathlib.Path("/mnt/docker-root")
finally:
    r._docker = original_docker
    r._containerd_storage_root = original_containerd
    pathlib.Path.exists, pathlib.Path.is_dir = original_exists, original_is_dir

with tempfile.TemporaryDirectory() as temp:
    config = pathlib.Path(temp) / "config.toml"
    config.write_text('root = "/mnt/containerd-data"\nimports = []\n')
    assert r._containerd_storage_root(config) == pathlib.Path("/mnt/containerd-data")
    config.write_text('root = "/mnt/containerd-data"\nimports = ["/etc/containerd/extra.toml"]\n')
    try:
        r._containerd_storage_root(config)
        raise AssertionError("containerd imported config was accepted without explicit path")
    except r.RetentionError as exc:
        assert exc.code == r.EXIT_INSPECTION
`
	command := exec.Command(python, "-c", program, filepath.Join("files", "site_image_retention.py"))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("retention helper policy: %v\n%s", err, output)
	}
}

func TestRetentionHelperDefaultsToPlan(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	program := `import importlib.util, sys
sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location("retention", sys.argv[1])
r = importlib.util.module_from_spec(spec)
spec.loader.exec_module(r)
assert r.parser().parse_args([]).operation == "plan"
`
	command := exec.Command(python, "-c", program, "files/site_image_retention.py")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("retention helper default operation: %v\n%s", err, output)
	}
}
