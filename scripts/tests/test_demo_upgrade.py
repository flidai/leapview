"""Read-only classification of immutable demo upgrade candidates."""
import hashlib
import importlib.util
import pathlib
import unittest
from unittest.mock import patch
import subprocess
import tempfile

ROOT = pathlib.Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('demo_upgrade_plan', ROOT/'scripts/demo_upgrade_plan.py')
plan = importlib.util.module_from_spec(spec)
spec.loader.exec_module(plan)


class UpgradePlanTests(unittest.TestCase):
    def test_unchanged_schema_keeps_image_only_path(self):
        result = plan.classify(28, 28, {'028_previous.sql': 'same'}, {'028_previous.sql': 'same'}, [])
        self.assertEqual(result['mode'], 'image-only')
        self.assertEqual(result['pendingMigrations'], [])

    def test_28_to_29_requires_state_recovery(self):
        result = plan.classify(28, 29, {'028_previous.sql': 'same'},
                               {'028_previous.sql': 'same', '029_agent_configuration.sql': 'new'}, [])
        self.assertEqual(result['mode'], 'database-upgrade-required')
        self.assertEqual(result['pendingMigrations'], ['029_agent_configuration.sql'])
        self.assertFalse(result['imageOnlyEligible'])

    def test_28_to_30_reports_both_required_migrations(self):
        result = plan.classify(28, 30, {'028_previous.sql': 'same'},
                               {'028_previous.sql': 'same', '029_agent_configuration.sql': 'new',
                                '030_session_client_label.sql': 'newer'}, [])
        self.assertEqual(result['mode'], 'database-upgrade-required')
        self.assertEqual(result['pendingMigrations'],
                         ['029_agent_configuration.sql', '030_session_client_label.sql'])
        self.assertFalse(result['imageOnlyEligible'])
        self.assertFalse(result['migrationExecutionAuthorized'])

    def test_changed_applied_migration_is_rejected(self):
        with self.assertRaisesRegex(ValueError, 'Applied migration'):
            plan.classify(28, 29, {'028_previous.sql': 'original'},
                          {'028_previous.sql': 'changed', '029_agent_configuration.sql': 'new'}, [])

    def test_deleted_applied_migration_is_rejected(self):
        with self.assertRaisesRegex(ValueError, 'Applied migration'):
            plan.classify(28, 29, {'028_previous.sql': 'original'}, {'029_agent_configuration.sql': 'new'}, [])

    def test_downgrade_is_rejected(self):
        with self.assertRaisesRegex(ValueError, 'Downgrade'):
            plan.classify(29, 28, {}, {}, [])

    def test_engine_change_still_needs_review(self):
        result = plan.classify(28, 28, {}, {}, ['go.mod'])
        self.assertEqual(result['mode'], 'review-required')

    def test_missing_or_ambiguous_forward_migration_is_rejected(self):
        for candidate in [{}, {'029_a.sql':'a', '029_b.sql':'b'}, {'030_future.sql':'x'}]:
            with self.subTest(candidate=candidate), self.assertRaises(ValueError):
                plan.classify(28, 29, {}, candidate, [])

    def test_unexpected_historical_addition_is_rejected(self):
        with self.assertRaises(ValueError):
            plan.classify(28, 28, {}, {'027_added.sql':'x'}, [])

    def test_report_never_authorizes_migrations(self):
        result = plan.classify(28, 29, {}, {'029_agent_configuration.sql':'new'}, [])
        self.assertFalse(result['migrationExecutionAuthorized'])


def write_compatibility(root):
    (root/'go.mod').write_text('require (\n github.com/duckdb/duckdb-go/v2 v2.1.0\n github.com/riverqueue/river v0.47.0\n)\n')
    policy = root/'internal/app/postgresbaseline/baseline.go'
    policy.parent.mkdir(parents=True,exist_ok=True)
    policy.write_text('const rolePolicySQL = `existing policy`')


class SourceTransitionTests(unittest.TestCase):
    def test_only_top_level_embedded_sql_enters_source_history(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            write_compatibility(root)
            def git(*args):
                return subprocess.check_output(['git', *args], cwd=root, stderr=subprocess.PIPE)
            git('init', '-q')
            git('config', 'user.name', 'Test')
            git('config', 'user.email', 'test@example.invalid')
            migrations = root / plan.MIGRATIONS
            fixtures = migrations / 'testdata'
            fixtures.mkdir(parents=True)
            (migrations/'goose.go').write_text('const (\n CurrentRevision int64 = 2\n)\n')
            expected = {}
            for name, sql in [('001_initial.sql', 'initial SQL'), ('002_next.sql', 'next SQL')]:
                (migrations/name).write_text(sql)
                expected[name] = hashlib.sha256(sql.encode()).hexdigest()
            # Neither a distinct historical lineage nor a matching basename is
            # embedded by Goose's top-level //go:embed *.sql declaration.
            (fixtures/'001_alternate_history.sql').write_text('historical fixture')
            (fixtures/'002_next.sql').write_text('fixture with colliding basename')
            git('add', '.')
            git('commit', '-qm', 'predecessor with SQL fixtures')
            previous = git('rev-parse', 'HEAD').decode().strip()
            (fixtures/'003_future.sql').write_text('future migration fixture only')
            git('add', '.')
            git('commit', '-qm', 'test fixture only')
            candidate = git('rev-parse', 'HEAD').decode().strip()
            with patch.object(plan, 'git', side_effect=git):
                result = plan.inspect_transition(previous, candidate)
            for source in ('sourceBefore', 'sourceAfter'):
                self.assertEqual(result[source]['migrations'], expected)
                self.assertEqual(len(result[source]['migrations']), result[source]['schema'])
            self.assertEqual(result['mode'], 'image-only')
            self.assertEqual(result['pendingMigrations'], [])

    def test_sources_are_read_from_exact_commits_not_worktree(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            write_compatibility(root)
            def git(*args):
                return subprocess.check_output(['git', *args], cwd=root, stderr=subprocess.PIPE)
            git('init', '-q')
            git('config', 'user.name', 'Test')
            git('config', 'user.email', 'test@example.invalid')
            migrations = root / plan.MIGRATIONS
            migrations.mkdir(parents=True)
            (migrations/'goose.go').write_text('const (\n CurrentRevision int64 = 28\n)\n')
            (migrations/'028_previous.sql').write_text('immutable history')
            git('add', '.')
            git('commit', '-qm', 'predecessor')
            previous = git('rev-parse', 'HEAD').decode().strip()
            (migrations/'goose.go').write_text('const (\n CurrentRevision int64 = 30\n)\n')
            (migrations/'029_agent_configuration.sql').write_text('new migration')
            (migrations/'030_browser_session_client_label.sql').write_text('next migration')
            git('add', '.')
            git('commit', '-qm', 'candidate')
            candidate = git('rev-parse', 'HEAD').decode().strip()
            # A newer/dirty checkout must not supply the candidate migration set.
            (migrations/'goose.go').write_text('const (\n CurrentRevision int64 = 31\n)\n')
            (migrations/'031_unqualified.sql').write_text('unqualified')
            with patch.object(plan, 'git', side_effect=git):
                result = plan.inspect_transition(previous, candidate)
            self.assertEqual(result['currentSchema'], 28)
            self.assertEqual(result['candidateSchema'], 30)
            self.assertEqual(result['pendingMigrations'],
                             ['029_agent_configuration.sql', '030_browser_session_client_label.sql'])
            self.assertEqual(result['candidateRevision'], candidate)

    def test_product_role_policy_changes_require_review_without_schema_bump(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            write_compatibility(root)
            def git(*args):
                return subprocess.check_output(['git', *args], cwd=root, stderr=subprocess.PIPE)
            git('init', '-q')
            git('config', 'user.name', 'Test')
            git('config', 'user.email', 'test@example.invalid')
            migrations = root / plan.MIGRATIONS
            migrations.mkdir(parents=True)
            (migrations/'goose.go').write_text('const (\n CurrentRevision int64 = 30\n)\n')
            policy = root/'internal/app/postgresbaseline/baseline.go'
            policy.parent.mkdir(parents=True, exist_ok=True)
            policy.write_text('const rolePolicySQL = `old role policy`')
            git('add', '.'); git('commit', '-qm', 'predecessor')
            previous = git('rev-parse', 'HEAD').decode().strip()
            policy.write_text('const rolePolicySQL = `new role policy requiring reconciliation`')
            git('add', '.'); git('commit', '-qm', 'candidate')
            candidate = git('rev-parse', 'HEAD').decode().strip()
            with patch.object(plan, 'git', side_effect=git):
                result = plan.inspect_transition(previous, candidate)
            self.assertEqual(result['mode'], 'database-upgrade-required')
            self.assertFalse(result['imageOnlyEligible'])

    def test_unrelated_module_update_keeps_image_only_eligibility(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            write_compatibility(root)
            def git(*args):
                return subprocess.check_output(['git', *args], cwd=root, stderr=subprocess.PIPE)
            git('init', '-q'); git('config', 'user.name', 'Test'); git('config', 'user.email', 'test@example.invalid')
            migrations = root/plan.MIGRATIONS
            migrations.mkdir(parents=True)
            (migrations/'goose.go').write_text('const (\n CurrentRevision int64 = 30\n)\n')
            git('add', '.'); git('commit', '-qm', 'before')
            before = git('rev-parse', 'HEAD').decode().strip()
            with (root/'go.mod').open('a') as stream: stream.write('require github.com/example/utility v1.2.3\n')
            git('add', '.'); git('commit', '-qm', 'unrelated dependency')
            after = git('rev-parse', 'HEAD').decode().strip()
            with patch.object(plan, 'git', side_effect=git):
                result = plan.inspect_transition(before, after)
            self.assertEqual(result['mode'], 'image-only')
            self.assertEqual(result['compatibilityChanges'], [])

    def test_mutable_revision_rejected_before_git(self):
        with patch.object(plan, 'git') as git:
            with self.assertRaises(ValueError): plan.source_schema('main')
            git.assert_not_called()


if __name__ == '__main__': unittest.main()
