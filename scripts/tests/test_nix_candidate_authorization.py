import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))
import nix_candidate_authorization as authorization


class CandidateAuthorizationTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.protected = self.root / "protected"
        self.source = self.root / "source"
        self.revision = self._git_repo(self.protected, "main")
        subprocess.run(["git", "clone", "--quiet", str(self.protected), str(self.source)], check=True)
        self.pr_revision = None

    def tearDown(self):
        self.temp.cleanup()

    @staticmethod
    def _git_repo(root, subject):
        root.mkdir()
        subprocess.run(["git", "init", "--quiet", str(root)], check=True)
        subprocess.run(["git", "-C", str(root), "-c", "user.name=Test", "-c", "user.email=test@example.invalid",
                        "commit", "--allow-empty", "--quiet", "-m", subject], check=True)
        return subprocess.check_output(["git", "-C", str(root), "rev-parse", "HEAD"], text=True).strip()

    def _new_source_commit(self, subject):
        subprocess.run(["git", "-C", str(self.source), "-c", "user.name=Test", "-c",
                        "user.email=test@example.invalid", "commit", "--allow-empty", "--quiet", "-m", subject],
                       check=True)
        self.pr_revision = subprocess.check_output(["git", "-C", str(self.source), "rev-parse", "HEAD"], text=True).strip()
        return self.pr_revision

    def _authorize(self, *, repository="flidai/leapview", event="workflow_dispatch",
                   ref="refs/heads/main", source_revision=None, protected_revision=None,
                   source_root=None, pull_requests=None):
        return authorization.authorize_candidate(
            repository=repository,
            event=event,
            ref=ref,
            source_revision=source_revision or self.revision,
            protected_revision=protected_revision or self.revision,
            source_root=source_root or self.source,
            protected_root=self.protected,
            pull_requests=[] if pull_requests is None else pull_requests,
        )

    def test_accepts_exact_dispatched_main_snapshot_without_pr_metadata(self):
        self.assertEqual(self._authorize(), "dispatched-main")

    def test_accepts_one_exact_open_pr_head_based_on_main(self):
        self._new_source_commit("candidate")
        pull_requests = [{
            "state": "open",
            "base": {"ref": "main"},
            "head": {"sha": self.pr_revision},
        }]
        self.assertEqual(
            self._authorize(source_revision=self.pr_revision, pull_requests=pull_requests),
            "open-pr-head",
        )

    def test_rejects_stale_or_arbitrary_revision_without_open_pr(self):
        self._new_source_commit("candidate")
        with self.assertRaisesRegex(authorization.AuthorizationError, "one exact open pull-request head"):
            self._authorize(source_revision=self.pr_revision)

    def test_rejects_closed_pr_and_pr_based_on_another_branch(self):
        self._new_source_commit("candidate")
        for pull_request in (
            {"state": "closed", "base": {"ref": "main"}, "head": {"sha": self.pr_revision}},
            {"state": "open", "base": {"ref": "release"}, "head": {"sha": self.pr_revision}},
        ):
            with self.subTest(pull_request=pull_request), self.assertRaises(authorization.AuthorizationError):
                self._authorize(source_revision=self.pr_revision, pull_requests=[pull_request])

    def test_rejects_duplicate_matching_pull_requests(self):
        self._new_source_commit("candidate")
        pull_request = {"state": "open", "base": {"ref": "main"}, "head": {"sha": self.pr_revision}}
        with self.assertRaises(authorization.AuthorizationError):
            self._authorize(source_revision=self.pr_revision, pull_requests=[pull_request, pull_request.copy()])

    def test_rejects_wrong_repository_event_or_ref(self):
        for kwargs in (
            {"repository": "someone-else/leapview"},
            {"event": "pull_request"},
            {"ref": "refs/heads/feature"},
        ):
            with self.subTest(kwargs=kwargs), self.assertRaises(authorization.AuthorizationError):
                self._authorize(**kwargs)

    def test_rejects_main_input_that_is_not_the_dispatched_commit(self):
        other_revision = self._new_source_commit("stale main")
        with self.assertRaises(authorization.AuthorizationError):
            self._authorize(source_revision=other_revision)

    def test_rejects_malformed_revision_and_pull_request_metadata(self):
        for malformed_revision in ("a" * 39, "A" * 40):
            with self.subTest(revision=malformed_revision), self.assertRaisesRegex(
                authorization.AuthorizationError, "full lowercase commit SHA"
            ):
                self._authorize(source_revision=malformed_revision)
        pr_revision = self._new_source_commit("candidate")
        with self.assertRaisesRegex(authorization.AuthorizationError, "JSON array"):
            self._authorize(source_revision=pr_revision, pull_requests={"state": "open"})
        with self.assertRaisesRegex(authorization.AuthorizationError, "malformed entry"):
            self._authorize(source_revision=pr_revision, pull_requests=[None])
        with self.assertRaisesRegex(authorization.AuthorizationError, "omits base or head identity"):
            self._authorize(source_revision=pr_revision, pull_requests=[{
                "state": "open", "base": None, "head": {"sha": pr_revision}
            }])

    def test_rejects_changed_source_checkout_at_reauthorization(self):
        pr_revision = self._new_source_commit("candidate")
        self._new_source_commit("changed candidate")
        pull_requests = [{
            "state": "open",
            "base": {"ref": "main"},
            "head": {"sha": pr_revision},
        }]
        with self.assertRaisesRegex(authorization.AuthorizationError, "source checkout differs"):
            self._authorize(source_revision=pr_revision, pull_requests=pull_requests)

    def test_rejects_changed_protected_checkout(self):
        other_root = self.root / "other-protected"
        self._git_repo(other_root, "other protected")
        with self.assertRaisesRegex(authorization.AuthorizationError, "protected checkout differs"):
            authorization.authorize_candidate(
                repository="flidai/leapview",
                event="workflow_dispatch",
                ref="refs/heads/main",
                source_revision=self.revision,
                protected_revision=self.revision,
                source_root=self.source,
                protected_root=other_root,
                pull_requests=[],
            )


if __name__ == "__main__":
    unittest.main()
