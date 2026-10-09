from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import nix_release_verification as verifier
import nix_candidate_manifest as candidate


class LiveNixVerificationTests(unittest.TestCase):
    def test_missing_or_borrowed_native_evidence_denied_before_fetch(self):
        with patch.object(verifier.inputs, 'verify') as fetch:
            for kind, native in [('application-image', None), ('site-image', '/borrowed/native')]:
                with self.assertRaisesRegex(ValueError, 'applicable embedded native'):
                    verifier.verify('/retained', '/source', kind, 123, 1, 'amd64', '/protected/verifier', '/fresh', native_directory=native)
            fetch.assert_not_called()

    def test_fresh_exact_binary_failure_never_issues_verification(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            retained, output = root / 'retained', root / 'fresh'
            (retained / 'binding').mkdir(parents=True)
            (retained / 'published').mkdir()
            source = {'revision': 'a' * 40, 'repository': 'flidai/leapview'}
            original = {'source': source, 'artifact': {'kind': 'site-image', 'platform': 'linux/amd64'}}
            signed = {'image': 'ghcr.io/flidai/leapview-site@sha256:' + 'b' * 64, 'source': source, 'kind': 'site-image'}
            (retained / 'binding/nix-site-signed.json').write_bytes(candidate.canonical_bytes(signed))
            (retained / 'published/nix-site-qualified.json').write_bytes(candidate.canonical_bytes({'verified': 'original'}))
            with patch.object(verifier.inputs, 'verify', return_value={'signerRevision': 'c' * 40}), \
                 patch.object(candidate, 'checkout_source', return_value=source), \
                 patch.object(verifier.publication, 'verified_record', return_value=original), \
                 patch.object(verifier.publication, 'bind_qualified', return_value={'verified': 'original'}), \
                 patch.object(verifier.go, 'scan', side_effect=ValueError('actual exact-binary finding')) as scan:
                with self.assertRaisesRegex(ValueError, 'actual exact-binary finding'):
                    verifier.verify(retained, '/read-only-source', 'site-image', 123, 1, 'amd64', '/protected/verifier', output)
                self.assertEqual(scan.call_args.args[0], retained / 'qualified/image.tar')
                self.assertEqual(scan.call_args.args[3], '/protected/verifier')
            self.assertFalse((output / 'verification.json').exists())

    def test_inventory_preserves_raw_bytes_and_refuses_symlinks(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / 'raw').mkdir()
            (root / 'raw/findings.json').write_bytes(b'{"actual":"findings"}')
            self.assertEqual(verifier.inventory(root), {'raw/findings.json': candidate.digest_bytes(b'{"actual":"findings"}')})
            (root / 'raw/alias.json').symlink_to(root / 'raw/findings.json')
            with self.assertRaisesRegex(ValueError, 'symlinks'):
                verifier.inventory(root)


if __name__ == '__main__':
    unittest.main()
