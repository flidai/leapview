"""Publication inputs must exist before clone-only networking is installed."""
import pathlib
import sys
import unittest
from unittest.mock import patch

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[1]))
import demo_publication as publication


class PublicationPreparationTests(unittest.TestCase):
    def test_dataset_is_prepared_before_the_caller_can_enter_clone_networking(self):
        for dataset, tool in [('cfo', 'bootstrapfinance'), ('olist', 'bootstrapolist')]:
            with self.subTest(dataset=dataset), patch.dict('os.environ', {'DEMO_DATASET': dataset}), \
                    patch.object(publication.subprocess, 'run') as run:
                with publication.publication_source('a' * 40) as root:
                    calls = [call.args[0] for call in run.call_args_list]
                    self.assertIn(['go', 'run', './internal/app/tools/' + tool,
                                   '--shared-cache', '--out', '.data/' + ('cfo-demo' if dataset == 'cfo' else 'olist')], calls)
                    self.assertTrue(all(call.kwargs.get('cwd') == root
                                        for call in run.call_args_list[1:]))

    def test_invalid_dataset_fails_before_source_checkout(self):
        with patch.dict('os.environ', {'DEMO_DATASET': 'unknown'}), \
                patch.object(publication.subprocess, 'run') as run:
            with self.assertRaisesRegex(ValueError, 'DEMO_DATASET'):
                with publication.publication_source('a' * 40):
                    self.fail('unsupported publication dataset reached clone preparation')
            run.assert_not_called()
