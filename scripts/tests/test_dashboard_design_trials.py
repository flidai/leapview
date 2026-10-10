"""Fresh-task runner controls; fake local CLI only, no inference or grading."""
import importlib.util
import json
import pathlib
import tempfile
import unittest
from unittest.mock import patch

SCRIPT = pathlib.Path(__file__).resolve().parents[1] / 'dashboard-design-trials.py'
if not SCRIPT.exists():
    SCRIPT = pathlib.Path(__file__).with_name('dashboard-design-trials.py')


def load():
    spec = importlib.util.spec_from_file_location('dashboard_design_trials', SCRIPT)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class RunnerTests(unittest.TestCase):
    def setUp(self):
        self.runner = load()
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.base = pathlib.Path(self.temp.name)
        self.prompt = self.base / 'prompt.txt'
        self.prompt.write_text('Never execute $(touch SENTINEL); `touch SENTINEL`\n')
        self.yaml = self.base / 'seed.yaml'
        self.yaml.write_text('visuals: []\n')
        self.ref = self.base / 'ref.md'
        self.ref.write_text('public contract\n')
        self.corpus = self.base/'corpus.json'
        corpus = {'version': 1, 'tasks': [{'id': f'task-{i}', 'seedCompilerValid': i != 9,
                  'negatives': [{'id': 'wrong'}] + ([{'id': 'extra'}] if i == 0 else [])} for i in range(10)]}
        self.corpus.write_text(json.dumps(corpus))
        self.corpus_hash = self.runner.digest(self.corpus.read_bytes())
        records = []
        for task in corpus['tasks']:
            for arm in 'ABC':
                for fixture, compiler, intent in [('seed', task['seedCompilerValid'], False), ('oracle', True, True)] + [('negative:'+n['id'], True, False) for n in task['negatives']]:
                    records.append({'task': task['id'], 'candidate': arm, 'fixture': fixture, 'id': task['id'], 'prototypeAccepted': True,
                                    'parseAndSchema': True, 'compiler': compiler, 'intent': intent})
        self.qualification = self.base/'qualification.json'
        self.qualification.write_text(json.dumps({'kind': 'deterministic-prototype-qualification',
            'qualified': True, 'agentTrials': 0, 'corpusSHA256': self.corpus_hash, 'binarySHA256': 'b'*64, 'records': records}))
        self.manifest = {
            'version': 1, 'experiment_id': 'trial-test', 'protocol': {'qualified': True, 'qualification': str(self.qualification), 'frozenCorpusManifest': str(self.corpus), 'corpusSHA256': self.corpus_hash},
            'shuffle_seed': 93820261009,
            'tasks': [{'id': f'task-{i}', 'prompt_file': str(self.prompt),
                       'seed_files_by_arm': {arm: [{'source': str(self.yaml), 'destination': 'project/dashboards/test.yaml'}] for arm in 'ABC'},
                       'allowed_yaml_files': ['project/dashboards/test.yaml']} for i in range(10)],
            'arms': [{'id': arm, 'reference_files': [{'source': str(self.ref), 'destination': 'reference/contract.md'}]} for arm in 'ABC'],
            'trials': [{'id': f'task-{i}-{arm}-{rep}', 'task_id': f'task-{i}', 'arm_id': arm, 'replicate': rep}
                       for i in range(10) for arm in 'ABC' for rep in range(1, 4)]}
        self.attest()
        self.work = self.base / 'work'
        self.output = self.base / 'output'

    def attest(self):
        inputs = {t['prompt_file'] for t in self.manifest['tasks']}
        inputs.add(self.manifest['protocol']['qualification'])
        inputs.update(f['source'] for t in self.manifest['tasks'] for files in t['seed_files_by_arm'].values() for f in files)
        inputs.update(f['source'] for a in self.manifest['arms'] for f in a['reference_files'])
        self.manifest['frozen_files'] = {p: self.runner.digest(pathlib.Path(p).read_bytes()) for p in inputs}
        self.manifest['protocol']['qualification_sha256'] = self.manifest['frozen_files'][str(self.qualification)]

    def test_exact_balanced_manifest_and_deterministic_shuffle(self):
        first = self.runner.validate_manifest(self.manifest)
        second = self.runner.validate_manifest(self.manifest)
        self.assertEqual(first, second)
        self.assertEqual(len(first), 90)
        self.assertNotEqual(first, self.manifest['trials'])

    def test_missing_task_trial_and_duplicate_cell_rejected(self):
        for key in ['tasks', 'trials']:
            broken = dict(self.manifest, **{key: self.manifest[key][:-1]})
            with self.subTest(key=key), self.assertRaises(ValueError):
                self.runner.validate_manifest(broken)
        broken = dict(self.manifest, trials=self.manifest['trials'][:-1] + [dict(self.manifest['trials'][0], id='other')])
        with self.assertRaises(ValueError):
            self.runner.validate_manifest(broken)

    def test_unqualified_manifest_rejected(self):
        with self.assertRaises(ValueError):
            self.runner.validate_manifest(dict(self.manifest, protocol={'qualified': False}))

    def test_unsafe_seed_destinations_and_symlink_sources_rejected(self):
        for destination in ['../oracle.yaml', '/tmp/oracle.yaml', 'expected/output.yaml', 'project/dashboards/../../oracle.yaml']:
            self.manifest['tasks'][0]['seed_files_by_arm']['A'][0]['destination'] = destination
            with self.subTest(destination=destination), self.assertRaises(ValueError):
                self.runner.validate_manifest(self.manifest)
        self.manifest['tasks'][0]['seed_files_by_arm']['A'][0]['destination'] = 'project/dashboards/test.yaml'
        link = self.base / 'symlink.yaml'
        link.symlink_to(self.yaml)
        self.manifest['tasks'][0]['seed_files_by_arm']['A'][0]['source'] = str(link)
        with self.assertRaises(ValueError):
            self.runner.validate_manifest(self.manifest)

    def fake_cli(self, body):
        path = self.base / 'fake-codex'
        path.write_text('#!/usr/bin/env python3\nimport sys,json,time,pathlib\n' + body)
        path.chmod(0o700)
        return str(path)

    def run_fake(self, body, **kwargs):
        cli = self.fake_cli(body)
        self.runner.validate_manifest(self.manifest)
        trial = self.manifest['trials'][0]
        return self.runner.run_trial(self.manifest, trial, self.work, self.output,
                                     {'cli_path': cli, 'test_identity': True}, **kwargs)

    def test_stdin_argument_vector_and_successful_freeze(self):
        result = self.run_fake(
            "prompt=sys.stdin.read()\n"
            "root=pathlib.Path(sys.argv[sys.argv.index('-C')+1])\n"
            "assert (root/'TASK.md').read_text() == prompt\n"
            "(root/'project/dashboards/test.yaml').write_text('visuals: [authored]\\n')\n"
            "out=pathlib.Path(sys.argv[sys.argv.index('-o')+1]); out.write_text(prompt)\n"
            "print(json.dumps({'type':'thread.started','thread_id':'unique'}))\n"
            "print(json.dumps({'type':'turn.started'}))\n"
            "print(json.dumps({'type':'item.completed','item':{'id':'msg','type':'agent_message','text':'done'}}))\n"
            "print(json.dumps({'type':'turn.completed','usage':{'input_tokens':1,'output_tokens':1}}))\n")
        self.assertTrue(result['complete'])
        directory = self.output / self.manifest['trials'][0]['id']
        self.assertEqual((directory/'frozen/project/dashboards/test.yaml').read_text(), 'visuals: [authored]\n')
        self.assertEqual((directory/'last-message.txt').read_text(), 'done')
        self.assertEqual((directory/'frozen/TASK.md').read_text(), self.prompt.read_text())
        self.assertIn('TASK.md', json.loads((directory/'input-inventory.json').read_text()))
        self.assertNotIn(self.prompt.read_text(), result['argv'])
        self.assertFalse((self.work/'task-0-A-1/SENTINEL').exists())
        self.assertEqual(result['thread_id'], 'unique')
        self.assertTrue((directory/'events.jsonl').exists())
        self.assertEqual(result['usage']['input_tokens'], 1)

    def test_first_attempt_cannot_replace_frozen_or_reserved_state(self):
        self.run_fake("print(json.dumps({'type':'turn.completed','usage':{}}))\n")
        frozen = self.output / 'task-0-A-1' / 'result.json'
        previous = frozen.read_bytes()
        with self.assertRaises(FileExistsError):
            self.run_fake("raise SystemExit(0)\n")
        self.assertEqual(frozen.read_bytes(), previous)

    def test_launch_failure_keeps_partial_source_and_denominator(self):
        self.runner.validate_manifest(self.manifest)
        result = self.runner.run_trial(self.manifest, self.manifest['trials'][0], self.work, self.output,
                                       {'cli_path': str(self.base/'missing-cli')})
        self.assertFalse(result['complete'])
        self.assertEqual(result['failure'], 'launch_error')
        self.assertTrue((self.output/'task-0-A-1/frozen/project/dashboards/test.yaml').exists())
        report = self.runner.summarize([result], expected=90)
        self.assertEqual(report['denominator'], 90)
        self.assertEqual(report['failures'], 90)

    def test_timeout_is_failure_and_freezes_partial_source(self):
        result = self.run_fake("time.sleep(10)\n", wall_seconds=0.03)
        self.assertEqual(result['failure'], 'timeout')
        self.assertFalse(result['complete'])
        self.assertIsNotNone(result['cli_exit_code'])
        self.assertTrue((self.output/'task-0-A-1/INCOMPLETE').exists())

    def test_tool_count_deduplicates_started_completed_and_limit_fails(self):
        body = "\nfor i in range(13):\n for kind in ['item.started','item.completed']:\n  print(json.dumps({'type':kind,'item':{'id':str(i),'type':'command_execution','command':'cat project/dashboards/test.yaml'}}),flush=True)\ntime.sleep(10)\n"
        result = self.run_fake(body)
        self.assertEqual(result['failure'], 'tool_budget')
        self.assertEqual(result['tool_calls'], 13)

    def test_missing_terminal_event_and_cli_error_are_failures(self):
        for body, expected in [("raise SystemExit(7)\n", 'cli_error'), ("raise SystemExit(0)\n", 'incomplete')]:
            with self.subTest(expected=expected):
                self.work = self.base / ('work-'+expected)
                self.output = self.base / ('output-'+expected)
                result = self.run_fake(body)
                self.assertEqual(result['failure'], expected)

    def test_no_followup_after_first_turn_and_usage_is_preserved(self):
        result = self.run_fake("print(json.dumps({'type':'turn.completed','usage':{'input_tokens':99}}),flush=True)\ntime.sleep(10)\n")
        self.assertEqual(result['failure'], 'incomplete')
        self.assertEqual(result['usage']['input_tokens'], 99)

    def test_completed_author_turn_freezes_before_hanging_cli_teardown(self):
        result = self.run_fake("sys.stdin.read(); root=pathlib.Path(sys.argv[sys.argv.index('-C')+1]); (root/'project/dashboards/test.yaml').write_text('completed source\\n')\nprint(json.dumps({'type':'thread.started','thread_id':'fresh'}),flush=True)\nprint(json.dumps({'type':'turn.started'}),flush=True)\nprint(json.dumps({'type':'item.completed','item':{'id':'message','type':'agent_message','text':'DONE'}}),flush=True)\nprint(json.dumps({'type':'turn.completed','usage':{'input_tokens':123,'output_tokens':2}}),flush=True)\ntime.sleep(20)\n")
        self.assertTrue(result['complete'])
        self.assertTrue(result['first_terminal_freeze'])
        self.assertTrue(result['controlled_terminal_shutdown'])
        self.assertEqual(result['cli_exit_code'], -15)
        self.assertEqual(result['usage']['input_tokens'], 123)
        self.assertEqual((self.output/'task-0-A-1/last-message.txt').read_text(), 'DONE')
        self.assertEqual((self.output/'task-0-A-1/frozen/project/dashboards/test.yaml').read_text(), 'completed source\n')
        self.assertLess(result['elapsed_seconds'], 3)
        self.assertGreaterEqual(result['freeze_completed_at'], result['frozen_at'])

    def test_tool_activity_after_terminal_is_rejected(self):
        result = self.run_fake("print(json.dumps({'type':'item.completed','item':{'id':'message','type':'agent_message','text':'DONE'}}))\nprint(json.dumps({'type':'turn.completed','usage':{}}))\nprint(json.dumps({'type':'item.started','item':{'id':'late','type':'command_execution','command':'cat TASK.md'}}))\n")
        self.assertEqual(result['failure'], 'activity_after_terminal')
        self.assertTrue(result['first_terminal_freeze'])

    def test_unauthorized_changes_are_visible_failures(self):
        result = self.run_fake("root=pathlib.Path(sys.argv[sys.argv.index('-C')+1]); (root/'unexpected.txt').write_text('changed')\nprint(json.dumps({'type':'turn.completed','usage':{}}))\n")
        self.assertEqual(result['failure'], 'unauthorized_change')
        self.assertIn('unexpected.txt', result['changed_files'])

    def test_staged_task_prompt_is_preserved_as_uneditable_input(self):
        result = self.run_fake("root=pathlib.Path(sys.argv[sys.argv.index('-C')+1]); (root/'TASK.md').write_text('changed task')\nprint(json.dumps({'type':'turn.completed','usage':{}}))\n")
        self.assertEqual(result['failure'], 'unauthorized_change')
        self.assertIn('TASK.md', result['unauthorized_changes'])

    def test_source_drift_after_preflight_is_failure(self):
        self.runner.validate_manifest(self.manifest)
        self.yaml.write_text('changed seed\n')
        result = self.runner.run_trial(self.manifest, self.manifest['trials'][0], self.work, self.output,
                                       {'cli_path': str(self.base/'missing-cli')})
        self.assertEqual(result['failure'], 'runner_error')
        self.assertIsNone(result['cli_exit_code'])

    def test_missing_extra_or_empty_arm_seed_lists_are_rejected(self):
        for seeds in [{'A': [], 'B': []}, {'A': [], 'B': [], 'C': [], 'D': []}, {'A': [], 'B': [], 'C': []}]:
            self.manifest['tasks'][0]['seed_files_by_arm'] = seeds
            with self.subTest(seeds=seeds), self.assertRaises(ValueError):
                self.runner.validate_manifest(self.manifest)

    def test_only_assigned_candidate_seed_is_staged(self):
        other = self.base / 'other.yaml'
        other.write_text('different candidate\n')
        self.manifest['tasks'][0]['seed_files_by_arm']['B'][0]['source'] = str(other)
        self.attest()
        self.run_fake("print(json.dumps({'type':'turn.completed','usage':{}}))\n")
        self.assertEqual((self.output/'task-0-A-1/frozen/project/dashboards/test.yaml').read_text(), 'visuals: []\n')

    def test_symlink_output_is_not_read_or_followed(self):
        outside = self.base / 'private.txt'
        outside.write_text('outside private content')
        result = self.run_fake("root=pathlib.Path(sys.argv[sys.argv.index('-C')+1]); p=root/'project/dashboards/test.yaml'; p.unlink(); p.symlink_to(" + repr(str(outside)) + ")\nprint(json.dumps({'type':'turn.completed','usage':{}}))\n")
        self.assertEqual(result['failure'], 'unsafe_or_oversized_output')
        self.assertEqual(result['omitted_freeze_files'], ['project/dashboards/test.yaml'])
        self.assertFalse((self.output/'task-0-A-1/frozen/project/dashboards/test.yaml').exists())

    def test_large_stdin_does_not_block_timeout_when_child_does_not_read(self):
        self.prompt.write_text('x' * 200000)
        self.attest()
        result = self.run_fake("time.sleep(10)\n", wall_seconds=0.03)
        self.assertEqual(result['failure'], 'timeout')
        self.assertLess(result['elapsed_seconds'], 3)

    def test_multiple_turns_are_a_visible_failure(self):
        result = self.run_fake("print(json.dumps({'type':'turn.started'}))\nprint(json.dumps({'type':'turn.started'}))\nprint(json.dumps({'type':'turn.completed','usage':{}}))\n")
        self.assertEqual(result['failure'], 'multiple_turns')

    def test_frozen_files_missing_extra_or_wrong_hash_rejected(self):
        baseline = self.manifest['frozen_files'].copy()
        for frozen in [{}, dict(baseline, extra='0'*64), dict(baseline, **{str(self.yaml): '0'*64})]:
            self.manifest['frozen_files'] = frozen
            with self.subTest(frozen=frozen), self.assertRaises(ValueError):
                self.runner.validate_manifest(self.manifest)

    def test_reference_drift_after_preflight_is_detected(self):
        self.runner.validate_manifest(self.manifest)
        self.ref.write_text('changed contract\n')
        with self.assertRaisesRegex(ValueError, 'frozen input changed'):
            self.runner.verify_frozen_files(self.manifest)

    def test_unstarted_failure_retains_denominator_and_exclusive_state(self):
        self.output.mkdir()
        trial = self.manifest['trials'][0]
        result = self.runner.skipped_trial(trial, self.output, {}, 'input_drift')
        report = self.runner.summarize([result], 90)
        self.assertEqual(report['failures'], 90)
        self.assertEqual(report['attempts_recorded'], 0)
        with self.assertRaises(FileExistsError):
            self.runner.skipped_trial(trial, self.output, {}, 'input_drift')

    def test_scheduler_stops_on_input_drift_and_keeps_exact_manifest_bytes(self):
        manifest_path = self.base/'experiment.json'
        raw = json.dumps(self.manifest, indent=3).encode()
        manifest_path.write_bytes(raw)
        cli = self.fake_cli("sys.stdin.read(); pathlib.Path(sys.argv[sys.argv.index('-o')+1]).write_text('done')\nprint(json.dumps({'type':'thread.started','thread_id':'coordinator-fresh'}))\nprint(json.dumps({'type':'turn.started'}))\nprint(json.dumps({'type':'item.completed','item':{'id':'msg','type':'agent_message','text':'done'}}))\nprint(json.dumps({'type':'turn.completed','usage':{}}))\n")
        original = self.runner.run_trial
        def mutate_after_freeze(*args, **kwargs):
            result = original(*args, **kwargs)
            self.ref.write_text('changed after first freeze\n')
            return result
        argv = ['runner', '--manifest', str(manifest_path), '--work-root', str(self.work),
                '--output-root', str(self.output), '--execute']
        with patch('sys.argv', argv), patch.object(self.runner, 'cli_identity', return_value={'cli_path': cli}), patch.object(self.runner, 'run_trial', side_effect=mutate_after_freeze), patch('builtins.print'):
            self.runner.main()
        summary = json.loads((self.output/'summary.json').read_text())
        self.assertEqual(summary['denominator'], 90)
        self.assertEqual(summary['completed'], 1)
        self.assertEqual(summary['failures'], 89)
        self.assertEqual(summary['attempts_recorded'], 1)
        self.assertEqual(summary['outcomes_recorded'], 90)
        self.assertEqual((self.output/'manifest.json').read_bytes(), raw)
        self.assertEqual(sum((d/'INCOMPLETE').exists() for d in self.output.iterdir() if d.is_dir()), 89)

    def test_exact_protocol_requires_one_thread_and_started_turn(self):
        for missing in ['thread', 'start']:
            self.work = self.base / ('work-'+missing)
            self.output = self.base / ('output-'+missing)
            body = ("print(json.dumps({'type':'thread.started','thread_id':'fresh'}))\n" if missing != 'thread' else '')
            body += ("print(json.dumps({'type':'turn.started'}))\n" if missing != 'start' else '')
            body += "print(json.dumps({'type':'item.completed','item':{'id':'message','type':'agent_message','text':'DONE'}}))\nprint(json.dumps({'type':'turn.completed','usage':{}}))\n"
            result = self.run_fake(body)
            with self.subTest(missing=missing):
                self.assertFalse(result['complete'])
                self.assertEqual(result['failure'], 'invalid_protocol_events')

    def test_multiple_thread_start_events_are_rejected(self):
        result = self.run_fake("print(json.dumps({'type':'thread.started','thread_id':'first'}))\nprint(json.dumps({'type':'thread.started','thread_id':'second'}))\nprint(json.dumps({'type':'turn.started'}))\nprint(json.dumps({'type':'item.completed','item':{'id':'message','type':'agent_message','text':'DONE'}}))\nprint(json.dumps({'type':'turn.completed','usage':{}}))\n")
        self.assertFalse(result['complete'])
        self.assertEqual(result['failure'], 'multiple_threads')

    def test_duplicate_qualification_fixture_and_wrong_expected_outcomes_rejected(self):
        original = json.loads(self.qualification.read_text())
        duplicate = json.loads(self.qualification.read_text())
        duplicate['records'][-1] = duplicate['records'][0]
        wrong = json.loads(self.qualification.read_text())
        next(r for r in wrong['records'] if r['task'] == 'task-9' and r['fixture'] == 'seed')['compiler'] = True
        for report in [duplicate, wrong]:
            self.qualification.write_text(json.dumps(report)); self.attest()
            with self.assertRaises(ValueError):
                self.runner.validate_manifest(self.manifest)
        self.qualification.write_text(json.dumps(original)); self.attest()
        self.runner.validate_manifest(self.manifest)

    def test_unknown_manifest_field_is_rejected(self):
        with self.assertRaisesRegex(ValueError, 'exactly the known'):
            self.runner.validate_manifest(dict(self.manifest, model_override='other'))

    def test_qualification_hash_mismatch_or_mutation_rejected(self):
        self.manifest['protocol']['qualification_sha256'] = '0'*64
        with self.assertRaisesRegex(ValueError, 'qualification hash'):
            self.runner.validate_manifest(self.manifest)
        self.attest()
        self.qualification.write_text('{}')
        with self.assertRaisesRegex(ValueError, 'frozen input changed'):
            self.runner.validate_manifest(self.manifest)

    def test_fake_true_qualification_with_wrong_records_or_agent_trials_rejected(self):
        original = json.loads(self.qualification.read_text())
        for changes in [{'agentTrials': 1}, {'records': original['records'][:-1]},
                        {'records': [dict(prototypeAccepted=False)] + original['records'][1:]}]:
            self.qualification.write_text(json.dumps(dict(original, **changes)))
            self.attest()
            with self.subTest(changes=changes), self.assertRaisesRegex(ValueError, '93 accepted prototypes'):
                self.runner.validate_manifest(self.manifest)


if __name__ == '__main__':
    unittest.main()
