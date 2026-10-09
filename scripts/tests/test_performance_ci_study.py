import unittest
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from performance_ci_study import pattern, validate_coverage


class CompileOnceCoverageTests(unittest.TestCase):
    def fixture(self):
        patterns = ['^(?:TestA|TestMinIOParquetSourceRefreshContract)$', '^(?:TestB)$',
                    '^(?:TestC)$', '^(?:TestD)$']
        rows = [[{'test': 'TestA', 'outcome': 'passed', 'skipOutput': []}],
                [{'test': 'TestB', 'outcome': 'skipped', 'skipOutput': ['dedicated PG lane']}],
                [{'test': 'TestC', 'outcome': 'passed', 'skipOutput': []}],
                [{'test': 'TestD', 'outcome': 'passed', 'skipOutput': []}]]
        return patterns, rows

    def test_dedicated_lane_exclusion_does_not_hide_other_missing_execution(self):
        patterns, rows = self.fixture()
        self.assertEqual(len(validate_coverage(patterns, rows)), 4)
        rows[1] = []
        with self.assertRaisesRegex(ValueError, 'actual named execution'):
            validate_coverage(patterns, rows)

    def test_selected_names_cannot_overlap_or_introduce_regex_selection(self):
        patterns, rows = self.fixture()
        patterns[3], rows[3] = patterns[0], rows[0]
        with self.assertRaisesRegex(ValueError, 'duplicate'):
            validate_coverage(patterns, rows)
        patterns, rows = self.fixture()
        patterns[1] = '^(?:Test.*)$'
        with self.assertRaisesRegex(ValueError, 'inventory'):
            validate_coverage(patterns, rows)

    def test_discovery_warnings_or_empty_pattern_cannot_be_inventory(self):
        for text in ('', 'warning\n^(?:TestA)$', '^TestA$', '^(?:TestA)$\nwarning'):
            with self.assertRaises(ValueError):
                pattern(text)


if __name__ == '__main__':
    unittest.main()
