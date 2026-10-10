import copy
import importlib.util
from pathlib import Path
import sys
import unittest

SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
spec = importlib.util.spec_from_file_location("performance_browser_study", SCRIPTS / "performance_browser_study.py")
study = importlib.util.module_from_spec(spec)
spec.loader.exec_module(study)


class BrowserStudyEvidenceTest(unittest.TestCase):
    def report(self):
        return {"iterations": 20, "assertions": {"deterministicFailures": [],
                "thresholds": {"enabled": True, "failures": []}},
                "samples": [{"interaction": "filter", "iteration": index + 1, "refreshId": str(index),
                             **{metric: 1 for metric in study.METRICS}} for index in range(20)],
                "observability": {"refreshSummariesExpected": 20, "refreshSummariesFound": 20}}

    def test_admits_complete_session(self):
        self.assertEqual(len(study.validate_session(self.report(), ["filter"])), 20)

    def test_rejects_missing_duplicate_nonfinite_and_boolean_samples(self):
        invalid = []
        missing = self.report()
        missing["samples"].pop()
        invalid.append(missing)
        duplicate = self.report()
        duplicate["samples"][1]["iteration"] = 1
        invalid.append(duplicate)
        for value in (float("nan"), float("inf"), -1, True, None):
            report = self.report()
            report["samples"][0]["allTargetSettlementMs"] = value
            invalid.append(report)
        for report in invalid:
            with self.subTest(report=report), self.assertRaises(ValueError):
                study.validate_session(report, ["filter"])

    def test_rejects_missing_observability_correctness_and_disabled_guards(self):
        base = self.report()
        for key, value in (("refreshSummariesFound", 19), ("refreshSummariesExpected", 19)):
            report = copy.deepcopy(base)
            report["observability"][key] = value
            with self.assertRaises(ValueError):
                study.validate_session(report, ["filter"])
        for key, value in (("enabled", False), ("failures", ["latency exceeded"])):
            report = copy.deepcopy(base)
            report["assertions"]["thresholds"][key] = value
            with self.assertRaises(ValueError):
                study.validate_session(report, ["filter"])
        base["assertions"]["deterministicFailures"] = ["missing target"]
        with self.assertRaises(ValueError):
            study.validate_session(base, ["filter"])


if __name__ == "__main__":
    unittest.main()
