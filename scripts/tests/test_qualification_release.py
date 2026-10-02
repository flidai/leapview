import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location(
    'qualification_release', Path(__file__).resolve().parents[1] / 'qualification_release.py')
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


def release(tag, published='2026-09-30T12:00:00Z', **extra):
    return dict(tag_name=tag, draft=False, published_at=published, id=1, **extra)


class ReleaseTests(unittest.TestCase):
    def test_exact_requested_release_never_lists_or_substitutes(self):
        calls = []
        def fetch(path):
            calls.append(path)
            return release('v1.2.3-rc.1')
        self.assertEqual(m.resolve('v1.2.3-rc.1', fetch), 'v1.2.3-rc.1')
        self.assertEqual(calls, ['/releases/tags/v1.2.3-rc.1'])

    def test_invalid_unavailable_draft_or_mismatched_exact_release_fails(self):
        for tag in ['', 'desktop-v1.2.3', 'v1.2', 'v1.2.3/evil', 'v1.2.3\n',
                    'v01.2.3', 'v1.2.3-01', 'v1.2.3-rc..1', 'v1.2.3+']:
            with self.subTest(tag=tag), self.assertRaises(ValueError):
                m.resolve(tag, lambda _: self.fail('invalid tag reached API'))
        for result in [release('v9.9.9'), release('v1.2.3') | {'draft': True},
                       release('v1.2.3') | {'published_at': None}]:
            with self.subTest(result=result), self.assertRaises(ValueError):
                m.resolve('v1.2.3', lambda _: result)
        def unavailable(_):
            raise OSError('404')
        with self.assertRaises(OSError):
            m.resolve('v1.2.3', unavailable)

    def test_schedule_paginates_and_selects_publication_time_including_prereleases(self):
        calls = []
        pages = [[release('desktop-v8.0.0')] * 99 +
                 [release('v2.0.0', '2026-09-01T00:00:00Z')],
                 [release('v1.0.0-rc.2', '2026-10-01T00:00:00Z'),
                  release('v3.0.0', '2026-10-02T00:00:00Z') | {'draft': True}]]
        def fetch(path):
            calls.append(path)
            return pages[len(calls)-1]
        self.assertEqual(m.resolve(None, fetch), 'v1.0.0-rc.2')
        self.assertEqual(calls, ['/releases?per_page=100&page=1', '/releases?per_page=100&page=2'])

    def test_missing_assets_do_not_select_an_older_release(self):
        releases = [release('v1.0.0', '2026-09-01T00:00:00Z') | {'assets': ['old']},
                    release('v2.0.0') | {'assets': []}]
        self.assertEqual(m.resolve(None, lambda _: releases), 'v2.0.0')

    def test_no_published_server_release_fails(self):
        with self.assertRaises(ValueError):
            m.resolve(None, lambda _: [release('desktop-v1.0.0')])


if __name__ == '__main__':
    unittest.main()
