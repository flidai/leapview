import importlib.util
from pathlib import Path
import unittest

SCRIPT = Path(__file__).resolve().parents[1] / "performance_capacity.py"
spec = importlib.util.spec_from_file_location("performance_capacity", SCRIPT)
capacity = importlib.util.module_from_spec(spec)
spec.loader.exec_module(capacity)


class CapacityEvidenceTest(unittest.TestCase):
    def row(self, readers=10, mode="shared", count=1, ns="42"):
        return f"BenchmarkSignalStreamHTTPFanoutCapacity/{mode}/readers={readers}-2\t{count}\t{ns} ns/op\t12 B/op\t2 allocs/op\n"

    def valid(self):
        return self.row() + self.row(mode="independent") + "PASS\n"

    def check(self, text):
        return capacity.validate_rows(text, "BenchmarkSignalStreamHTTPFanoutCapacity", 10, 2)

    def test_admits_complete_independent_rows(self):
        self.assertEqual(len(self.check(self.valid())), 2)

    def test_rejects_missing_duplicate_or_wrong_level(self):
        for value in (self.row() + "PASS\n", self.row() * 2 + "PASS\n",
                      self.row(readers=100) + self.row(mode="independent") + "PASS\n"):
            with self.subTest(value=value), self.assertRaises(ValueError):
                self.check(value)

    def test_rejects_wrong_batch_cpu_and_failed_cleanup(self):
        for value in (self.valid().replace("-2", "-4"), self.valid().replace("\t1\t", "\t2\t"),
                      self.valid() + "FAIL cleanup did not drain\n", self.valid().replace("PASS", "")):
            with self.subTest(value=value), self.assertRaises(ValueError):
                self.check(value)

    def test_rejects_nonfinite_or_negative_measurements(self):
        for value in ("NaN", "inf", "-2"):
            with self.subTest(value=value), self.assertRaises(ValueError):
                self.check(self.valid().replace("42 ns/op", value + " ns/op"))


if __name__ == "__main__":
    unittest.main()
