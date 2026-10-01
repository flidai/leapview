"""Prove the new CLI regression against the pre-repair implementation on a runner."""
from pathlib import Path
import subprocess

BASE = "d490a7f9d242d6edcfbed2e52d0324bc747e6fc1"
ROOT = Path("internal/app/tools/ociadmission")
paths = [ROOT / name for name in ("admission.go", "runner.go", "main.go", "main_test.go")]
saved = {path: path.read_bytes() for path in paths}
try:
    subprocess.run(["git", "fetch", "--depth=1", "origin", BASE], check=True)
    for path in paths[:-1]:
        path.write_bytes(subprocess.check_output(["git", "show", f"{BASE}:{path}"]))
    # This parser unit test names a newly added helper. Keep the CLI behavior
    # regression so the red run fails on the missing flag, rather than compilation.
    tests = saved[paths[-1]].decode()
    start = tests.index("func TestVulnerabilityReportParsesAndAccountsForExceptions(")
    end = tests.index("func TestLiveVulnerabilityReport(", start)
    paths[-1].write_text(tests[:start] + tests[end:])
    result = subprocess.run(["go", "test", "./internal/app/tools/ociadmission",
                             "-run", "^TestLiveVulnerabilityReport$", "-count=1"],
                            stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
    assert result.returncode != 0, "pre-repair implementation unexpectedly passed"
    assert "flag provided but not defined: -vulnerability-report" in result.stdout, result.stdout
    print("Red regression confirmed: pre-repair CLI rejects --vulnerability-report.")
finally:
    for path, data in saved.items():
        path.write_bytes(data)
