#!/usr/bin/env python3
"""Run a qualified 90-cell dashboard authoring experiment, with one CLI turn per cell.

No grading or query execution happens here. Filesystem confinement is instructional:
workspace-write is not a claim of enforced read isolation from host sibling files.
"""
import argparse
from collections import deque
import concurrent.futures
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import random
import re
import selectors
import shutil
import signal
import subprocess
import tempfile
import time
import tomllib

SEED = 93820261009
ID = re.compile(r'^[A-Za-z0-9][A-Za-z0-9_-]{0,95}$')
MODEL_KEYS = ('model', 'model_reasoning_effort', 'service_tier')
MAX_FILE_BYTES = 10 * 1024 * 1024


def digest(data):
    return hashlib.sha256(data).hexdigest()


def write_json(path, value):
    with path.open('x') as stream:
        json.dump(value, stream, indent=2, sort_keys=True)
        stream.write('\n')


def relative_path(value):
    if not isinstance(value, str) or '\\' in value:
        raise ValueError('destination must be a POSIX relative path')
    path = PurePosixPath(value)
    if path.is_absolute() or not path.parts or any(p in ('..', '.') for p in value.split('/')):
        raise ValueError('unsafe relative destination')
    return path


def allowed_seed(value):
    path = relative_path(value)
    if value in ('AGENTS.md', 'TASK.md'):
        return
    if len(path.parts) >= 2 and path.parts[0] == 'reference' and path.suffix in ('.md', '.json'):
        return
    if (len(path.parts) >= 3 and path.parts[0] == 'project'
            and path.parts[1] in ('connections', 'sources', 'models', 'semantic-models', 'dashboards')
            and path.suffix in ('.yaml', '.yml')):
        return
    raise ValueError('seed destination is outside the fixture allowlist')


def source_bytes(value):
    path = Path(value)
    if not path.is_absolute() or any(p.is_symlink() for p in (path, *path.parents)):
        raise ValueError('source must be an absolute regular non-symlink file')
    if not path.is_file() or path.stat().st_size > MAX_FILE_BYTES:
        raise ValueError('source is missing, nonregular or too large')
    return path.read_bytes()


def validate_manifest(manifest):
    keys = {'version', 'experiment_id', 'shuffle_seed', 'tasks', 'arms', 'trials', 'frozen_files', 'protocol'}
    if set(manifest) != keys:
        raise ValueError('manifest must contain exactly the known v1 fields')
    if manifest.get('version') != 1 or manifest.get('protocol', {}).get('qualified') is not True or manifest.get('shuffle_seed') != SEED:
        raise ValueError('manifest must be qualified v1 with shuffle_seed 93820261009')
    if not ID.fullmatch(manifest.get('experiment_id', '')):
        raise ValueError('invalid experiment id')
    tasks, arms, trials = [manifest.get(k, []) for k in ('tasks', 'arms', 'trials')]
    task_ids = [t.get('id', '') for t in tasks]
    arm_ids = [a.get('id', '') for a in arms]
    if len(tasks) != 10 or len(set(task_ids)) != 10 or any(not ID.fullmatch(t) for t in task_ids):
        raise ValueError('exactly ten unique tasks are required')
    if len(arms) != 3 or set(arm_ids) != set('ABC'):
        raise ValueError('exactly arms A B C are required')
    expected_inputs = {t['prompt_file'] for t in tasks}
    qualification = manifest['protocol'].get('qualification')
    if not isinstance(qualification, str):
        raise ValueError('qualification report path required')
    expected_inputs.add(qualification)
    expected_inputs.update(f['source'] for t in tasks for files in t.get('seed_files_by_arm', {}).values() for f in files)
    expected_inputs.update(f['source'] for a in arms for f in a.get('reference_files', []))
    frozen = manifest['frozen_files']
    if not isinstance(frozen, dict) or set(frozen) != expected_inputs:
        raise ValueError('frozen_files must attest exactly every prompt, seed and reference source')
    verify_frozen_files(manifest)
    qualification_record(manifest)
    for task in tasks:
        prompt_hash = digest(source_bytes(task['prompt_file']))
        if task.get('prompt_sha256') is not None and task['prompt_sha256'] != prompt_hash:
            raise ValueError('prompt attestation does not match')
        task['prompt_sha256'] = prompt_hash
        seeds = task.get('seed_files_by_arm', {})
        if not isinstance(seeds, dict) or set(seeds) != set('ABC') or 'seed_files' in task:
            raise ValueError('exactly A B C candidate seed lists are required')
        outputs = task.get('allowed_yaml_files', [])
        if not outputs or len(set(outputs)) != len(outputs):
            raise ValueError('unique allowed dashboard YAML paths required')
        for output in outputs:
            path = relative_path(output)
            if len(path.parts) < 3 or path.parts[:2] != ('project', 'dashboards') or path.suffix not in ('.yaml', '.yml'):
                raise ValueError('only project/dashboard YAML output paths are permitted')
        for arm in arms:
            destinations = set()
            if not isinstance(seeds[arm['id']], list) or not seeds[arm['id']]:
                raise ValueError('each candidate needs a nonempty seed file list')
            for file in seeds[arm['id']] + arm.get('reference_files', []):
                allowed_seed(file['destination'])
                if file['destination'] in destinations:
                    raise ValueError('duplicate seed destination')
                destinations.add(file['destination'])
                content = source_bytes(file['source'])
                if file.get('sha256') is not None and digest(content) != file['sha256']:
                    raise ValueError('seed attestation does not match')
                file['sha256'] = digest(content)
    cells, ids = set(), set()
    for trial in trials:
        trial_id = trial.get('id', '')
        cell = (trial.get('task_id'), trial.get('arm_id'), trial.get('replicate'))
        if not ID.fullmatch(trial_id) or trial_id in ids or cell in cells:
            raise ValueError('duplicate or invalid trial identity')
        if cell[0] not in task_ids or cell[1] not in arm_ids or type(cell[2]) is not int or cell[2] not in (1, 2, 3):
            raise ValueError('unknown task, arm or replicate')
        ids.add(trial_id)
        cells.add(cell)
    expected = {(t, a, r) for t in task_ids for a in arm_ids for r in (1, 2, 3)}
    if len(trials) != 90 or cells != expected:
        raise ValueError('all 90 task x arm x replicate cells are required')
    order = sorted(trials, key=lambda t: t['id'])
    random.Random(SEED).shuffle(order)
    return order


def verify_frozen_files(manifest):
    for source, expected_hash in manifest['frozen_files'].items():
        if not isinstance(expected_hash, str) or not re.fullmatch('[0-9a-f]{64}', expected_hash):
            raise ValueError('invalid frozen SHA256')
        if digest(source_bytes(source)) != expected_hash:
            raise ValueError('frozen input changed')


def qualification_record(manifest):
    protocol = manifest['protocol']
    path = protocol['qualification']
    if protocol.get('qualification_sha256') != manifest['frozen_files'].get(path):
        raise ValueError('qualification hash differs from frozen attestation')
    report = json.loads(source_bytes(path))
    records = report.get('records')
    if (report.get('kind') != 'deterministic-prototype-qualification'
            or report.get('qualified') is not True or type(report.get('agentTrials')) is not int
            or report['agentTrials'] != 0 or not isinstance(records, list) or len(records) != 93
            or any(r.get('prototypeAccepted') is not True for r in records)):
        raise ValueError('qualification must report zero agent trials and 93 accepted prototypes')
    corpus_path = protocol.get('frozenCorpusManifest')
    if not isinstance(corpus_path, str) or not re.fullmatch('[0-9a-f]{64}', str(report.get('binarySHA256', ''))):
        raise ValueError('qualification requires frozen corpus path and binary SHA256')
    corpus_bytes = source_bytes(corpus_path)
    if digest(corpus_bytes) != protocol.get('corpusSHA256') or protocol.get('corpusSHA256') != report.get('corpusSHA256'):
        raise ValueError('qualification corpus SHA256 mismatch')
    corpus = json.loads(corpus_bytes)
    expected = {}
    for task in corpus.get('tasks', []):
        if type(task.get('seedCompilerValid')) is not bool:
            raise ValueError('qualification corpus seed compiler expectation missing')
        for arm in 'ABC':
            expected[(task['id'], arm, 'seed')] = (task['seedCompilerValid'], False)
            expected[(task['id'], arm, 'oracle')] = (True, True)
            for negative in task.get('negatives', []):
                expected[(task['id'], arm, 'negative:'+negative['id'])] = (True, False)
    if len(expected) != 93:
        raise ValueError('qualification corpus must define 31 fixtures across three candidates')
    seen = set()
    for record in records:
        key = (record.get('task'), record.get('candidate'), record.get('fixture'))
        outcomes = expected.get(key)
        if (outcomes is None or key in seen or record.get('id') != key[0]
                or record.get('parseAndSchema') is not True
                or record.get('compiler') is not outcomes[0] or record.get('intent') is not outcomes[1]):
            raise ValueError('qualification fixture identity or expected outcomes mismatch')
        seen.add(key)
    if seen != set(expected):
        raise ValueError('qualification fixture coverage is incomplete')
    return {'qualified': report['qualified'], 'agentTrials': report['agentTrials'], 'records': len(records),
            'sha256': manifest['frozen_files'][path], 'corpusSHA256': report.get('corpusSHA256'),
            'binarySHA256': report.get('binarySHA256')}


def inventory(root):
    result = {}
    for base, dirs, files in os.walk(root, followlinks=False):
        for name in dirs + files:
            path = Path(base) / name
            relative = path.relative_to(root).as_posix()
            if path.is_symlink():
                result[relative] = {'kind': 'symlink'}
            elif path.is_file():
                size = path.stat().st_size
                result[relative] = {'kind': 'file', 'bytes': size,
                                    'sha256': digest(path.read_bytes()) if size <= MAX_FILE_BYTES else None}
            elif not path.is_dir():
                result[relative] = {'kind': 'nonregular'}
    return result


def freeze(workspace, destination, files):
    destination.mkdir()
    omitted = []
    for relative, info in files.items():
        if info['kind'] != 'file' or info['sha256'] is None:
            omitted.append(relative)
            continue
        source = workspace / relative
        if any(p.is_symlink() for p in (source, *source.parents)):
            omitted.append(relative)
            continue
        target = destination / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        with target.open('xb') as output:
            output.write(source.read_bytes())
    return omitted


def cli_identity():
    executable = shutil.which('codex')
    if not executable:
        raise RuntimeError('codex CLI unavailable')
    executable = str(Path(executable).resolve())
    codex_dir = Path(os.environ.get('CODEX_HOME', str(Path.home() / '.codex')))
    config_path = codex_dir / 'config.toml'
    raw = config_path.read_bytes()
    config = tomllib.loads(raw.decode())
    selected = {k: config.get(k) for k in MODEL_KEYS}
    new_thread = config.get('models', {}).get('new_thread', {})
    version = subprocess.run([executable, '--version'], capture_output=True, text=True, timeout=10, check=True).stdout.strip()
    return {'cli_path': executable, 'cli_sha256': digest(Path(executable).read_bytes()),
            'cli_version': version, 'config_sha256': digest(raw), 'model_settings': selected,
            'new_thread_settings': {k: new_thread.get(k) for k in MODEL_KEYS if k in new_thread},
            'configured_profile': config.get('profile'),
            'resolved_model_source': 'normal user config; not emitted by exec JSONL',
            'feature_booleans': {k: v for k, v in config.get('features', {}).items() if isinstance(v, bool)},
            'mcp_servers': {k: {'enabled': v.get('enabled', 'default'), 'required': v.get('required', 'default')}
                            for k, v in config.get('mcp_servers', {}).items()}}


def stop_process(process):
    try:
        os.killpg(process.pid, signal.SIGTERM)
        process.wait(timeout=1)
    except ProcessLookupError:
        pass
    except subprocess.TimeoutExpired:
        pass
    # An exited parent may leave descendants holding its stdout pipe open.
    try:
        os.killpg(process.pid, signal.SIGKILL)
    except ProcessLookupError:
        pass
    process.wait(timeout=5)


def observe(event, state):
    kind = event.get('type')
    if state['terminal'] and kind in ('turn.started', 'turn.completed', 'item.started', 'item.completed'):
        state['failure'] = state['failure'] or 'activity_after_terminal'
    if kind == 'thread.started':
        state['thread_starts'] += 1
        if state['thread_starts'] > 1:
            state['failure'] = state['failure'] or 'multiple_threads'
        state['thread_id'] = event.get('thread_id')
    if kind == 'turn.started':
        state['turns'] += 1
        if state['turns'] > 1:
            state['failure'] = 'multiple_turns'
    if kind in ('turn.failed', 'error'):
        state['failure'] = 'cli_event_error'
    if kind == 'turn.completed':
        state['terminal_events'] += 1
        state['terminal'] = True
        if state['terminal_events'] == 1:
            state['usage'] = event.get('usage', {})
    item = event.get('item', {})
    item_kind = item.get('type')
    if kind == 'item.completed' and item_kind == 'agent_message':
        state['messages'] += 1
        if not state['terminal']:
            state['last_message'] = item.get('text', '')
    if kind in ('item.started', 'item.completed') and item_kind not in (None, 'agent_message', 'reasoning', 'plan', 'todo_list'):
        item_id = item.get('id')
        key = (item_kind, item_id) if item_id is not None else ('event', state['events'])
        state['tools'].add(key)
        command = item.get('command', '')
        if item_kind == 'command_execution' and re.search(r'\b(leapview\s+(validate|plan)|task\s+|go\s+test|bun\s+test|pytest|codex\s+exec)\b', command):
            state['review_flags'].add('observed_validation_or_delegation_command')
        if item_kind in ('mcp_tool_call', 'web_search', 'web_search_call'):
            state['review_flags'].add('observed_external_tool')


def run_trial(manifest, trial, work_root, output_root, identity, wall_seconds=240, tool_limit=12):
    output = Path(output_root) / trial['id']
    output.parent.mkdir(parents=True, exist_ok=True)
    output.mkdir()  # Reservation is exclusive and never reused, including a crashed attempt.
    workspace = Path(work_root) / trial['id']
    workspace.parent.mkdir(parents=True, exist_ok=True)
    workspace.mkdir()
    task = next(t for t in manifest['tasks'] if t['id'] == trial['task_id'])
    arm = next(a for a in manifest['arms'] if a['id'] == trial['arm_id'])
    state = {'thread_id': None, 'thread_starts': 0, 'terminal_events': 0, 'usage': {}, 'tools': set(), 'events': 0, 'turns': 0,
             'messages': 0, 'terminal': False, 'failure': None, 'review_flags': set(), 'last_message': None}
    started = time.time()
    monotonic_start = time.monotonic()
    process, before, argv, prompt_hash = None, {}, [], None
    terminal_inventory, omitted = None, []
    terminal_observed_at, terminal_elapsed, freeze_completed_at = None, None, None
    controlled_terminal_shutdown = False
    write_json(output/'reservation.json', {'trial': trial, 'started': started, 'identity': identity})
    try:
        for file in task['seed_files_by_arm'][arm['id']] + arm.get('reference_files', []):
            content = source_bytes(file['source'])
            if file.get('sha256') is not None and digest(content) != file['sha256']:
                raise ValueError('seed changed after preflight')
            destination = workspace/file['destination']
            destination.parent.mkdir(parents=True, exist_ok=True)
            with destination.open('xb') as stream:
                stream.write(content)
        prompt = source_bytes(task['prompt_file'])
        prompt_hash = digest(prompt)
        if task.get('prompt_sha256') is not None and prompt_hash != task['prompt_sha256']:
            raise ValueError('prompt changed after preflight')
        if len(prompt) > 1024 * 1024:
            raise ValueError('prompt exceeds one MiB')
        task_document = workspace/'TASK.md'
        if task_document.exists():
            if task_document.read_bytes() != prompt:
                raise ValueError('seed TASK.md differs from frozen stdin prompt')
        else:
            with task_document.open('xb') as stream:
                stream.write(prompt)
        before = inventory(workspace)
        write_json(output/'input-inventory.json', before)
        argv = [identity['cli_path'], 'exec', '--ephemeral', '--skip-git-repo-check', '--sandbox',
                'workspace-write', '--json', '-C', str(workspace), '-o', str(output/'last-message.txt'), '-']
        with (output/'events.jsonl').open('xb') as events, (output/'stderr.txt').open('xb') as errors:
            try:
                with tempfile.TemporaryFile() as stdin:
                    stdin.write(prompt)
                    stdin.seek(0)
                    process = subprocess.Popen(argv, stdin=stdin, stdout=subprocess.PIPE,
                                               stderr=errors, start_new_session=True)
            except OSError:
                state['failure'] = 'launch_error'
            if process:
                selector = selectors.DefaultSelector()
                selector.register(process.stdout, selectors.EVENT_READ)
                pending = b''
                try:
                    while selector.get_map():
                        if terminal_inventory is None and time.monotonic() - monotonic_start >= wall_seconds:
                            state['failure'] = 'timeout'
                            stop_process(process)
                        if len(state['tools']) > tool_limit:
                            state['failure'] = 'tool_budget'
                            stop_process(process)
                        if state['failure']:
                            stop_process(process)
                        for key, _ in selector.select(timeout=0.05):
                            data = os.read(key.fileobj.fileno(), 65536)
                            if not data:
                                selector.unregister(key.fileobj)
                                continue
                            events.write(data)
                            events.flush()
                            pending += data
                            while b'\n' in pending:
                                line, pending = pending.split(b'\n', 1)
                                if line.strip():
                                    try:
                                        event = json.loads(line)
                                        state['events'] += 1
                                        observe(event, state)
                                        if state['terminal'] and terminal_inventory is None:
                                            terminal_observed_at = time.time()
                                            terminal_elapsed = time.monotonic() - monotonic_start
                                            terminal_inventory = inventory(workspace)
                                            omitted = freeze(workspace, output/'frozen', terminal_inventory)
                                            freeze_completed_at = time.time()
                                            controlled_terminal_shutdown = process.poll() is None
                                            # Completion is author success; owned CLI/MCP teardown is infrastructure.
                                            stop_process(process)
                                    except (ValueError, TypeError, AttributeError):
                                        state['failure'] = 'invalid_jsonl'
                            if len(pending) > MAX_FILE_BYTES:
                                state['failure'] = 'oversized_jsonl'
                                pending = b''
                    if pending.strip():
                        state['failure'] = state['failure'] or 'truncated_jsonl'
                    try:
                        process.wait(timeout=0.5)
                    except subprocess.TimeoutExpired:
                        state['failure'] = state['failure'] or 'continued_after_terminal'
                        stop_process(process)
                finally:
                    selector.close()
                    stop_process(process)
                    process.stdout.close()
    except Exception as exc:
        state['failure'] = state['failure'] or 'runner_error'
        state['review_flags'].add('runner_exception:' + type(exc).__name__)
    finally:
        if process:
            stop_process(process)
    after_shutdown = inventory(workspace)
    after = terminal_inventory if terminal_inventory is not None else after_shutdown
    if terminal_inventory is not None and after_shutdown != terminal_inventory:
        state['failure'] = state['failure'] or 'post_terminal_mutation'
    if terminal_inventory is None:
        terminal_observed_at = time.time()
        omitted = freeze(workspace, output/'frozen', after)
        freeze_completed_at = time.time()
    if inventory(output/'frozen') != {p: info for p, info in after.items() if p not in omitted}:
        state['failure'] = state['failure'] or 'freeze_race'
    if state['terminal'] and state['last_message'] is not None:
        # exec -o can be delayed until teardown. Preserve the final JSONL message instead.
        (output/'last-message.txt').write_text(state['last_message'])
    if len(state['tools']) > tool_limit:
        state['failure'] = state['failure'] or 'tool_budget'
    changed = sorted(p for p in set(before) | set(after) if before.get(p) != after.get(p))
    unauthorized = sorted(set(changed) - set(task['allowed_yaml_files']))
    if unauthorized:
        state['failure'] = state['failure'] or 'unauthorized_change'
    if omitted:
        state['failure'] = state['failure'] or 'unsafe_or_oversized_output'
    exit_code = process.returncode if process else None
    if not state['failure']:
        if exit_code != 0 and not (controlled_terminal_shutdown and state['terminal'] and exit_code in (-signal.SIGTERM, -signal.SIGKILL)):
            state['failure'] = 'cli_error'
        elif not state['terminal'] or not state['messages'] or not (output/'last-message.txt').is_file():
            state['failure'] = 'incomplete'
    if not state['failure'] and (state['thread_starts'] != 1 or state['turns'] != 1
            or state['terminal_events'] != 1 or not isinstance(state['thread_id'], str)
            or not state['thread_id'].strip()):
        state['failure'] = 'invalid_protocol_events'
    result = {'trial': trial, 'complete': state['failure'] is None, 'failure': state['failure'],
              'cli_exit_code': exit_code, 'thread_id': state['thread_id'], 'usage': state['usage'],
              'tool_calls': len(state['tools']), 'tool_limit': tool_limit, 'wall_limit_seconds': wall_seconds,
              'protocol_events': {'thread_starts': state['thread_starts'], 'turn_starts': state['turns'], 'turn_completions': state['terminal_events']},
              'elapsed_seconds': terminal_elapsed if terminal_elapsed is not None else time.monotonic()-monotonic_start,
              'process_elapsed_seconds': time.monotonic()-monotonic_start, 'started': started,
              'frozen_at': terminal_observed_at, 'freeze_completed_at': freeze_completed_at,
              'first_terminal_freeze': terminal_inventory is not None,
              'controlled_terminal_shutdown': controlled_terminal_shutdown,
              'last_message_source': 'terminal JSONL' if state['terminal'] and state['last_message'] is not None else 'CLI output file',
              'changed_files': changed, 'unauthorized_changes': unauthorized, 'omitted_freeze_files': omitted,
              'input_inventory_sha256': digest(json.dumps(before, sort_keys=True).encode()),
              'stdin_sha256': prompt_hash,
              'output_inventory': after, 'argv': argv, 'identity': identity,
              'artifact_sha256': {name: digest((output/name).read_bytes()) for name in
                                  ('events.jsonl', 'stderr.txt', 'last-message.txt') if (output/name).is_file()},
              'review_flags': sorted(state['review_flags']), 'physical_read_isolation_enforced': False,
              'tool_budget_enforcement': 'observed JSONL items; a tool may start before cancellation',
              'token_budget_enforced': False,
              'freeze_boundary': 'snapshot at first turn.completed, before controlled owned-process shutdown; otherwise first process failure',
              'query_execution_scored': False}
    write_json(output/'result.json', result)
    if not result['complete']:
        (output/'INCOMPLETE').write_text(state['failure']+'\n')
    return result


def summarize(results, expected=90):
    complete = sum(r['complete'] for r in results)
    return {'denominator': expected, 'attempts_recorded': sum(r.get('attempted', True) for r in results),
            'outcomes_recorded': len(results), 'completed': complete,
            'failures': expected-complete, 'pending': expected-len(results),
            'query_execution_scored': False}


def skipped_trial(trial, output_root, identity, failure):
    output = output_root/trial['id']
    output.mkdir()
    result = {'trial': trial, 'attempted': False, 'complete': False, 'failure': failure,
              'cli_exit_code': None, 'identity': identity, 'usage': {}, 'tool_calls': 0,
              'output_inventory': {}, 'frozen_at': time.time(), 'query_execution_scored': False}
    (output/'frozen').mkdir()
    (output/'INCOMPLETE').write_text(failure+'\n')
    write_json(output/'result.json', result)
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--manifest', type=Path, required=True)
    parser.add_argument('--work-root', type=Path, required=True)
    parser.add_argument('--output-root', type=Path, required=True)
    parser.add_argument('--concurrency', type=int, choices=(1, 2, 3), default=1)
    parser.add_argument('--execute', action='store_true', help='dispatch only after root explicitly authorizes this qualified manifest')
    args = parser.parse_args()
    manifest_raw = args.manifest.read_bytes()
    manifest = json.loads(manifest_raw)
    order = validate_manifest(manifest)
    work_root, output_root = args.work_root.resolve(), args.output_root.resolve()
    if work_root == output_root or work_root in output_root.parents or output_root in work_root.parents:
        parser.error('controller output and work roots must be separate sibling trees')
    if args.manifest.resolve() == work_root or work_root in args.manifest.resolve().parents:
        parser.error('manifest must be outside author workspaces')
    print(json.dumps({'qualified': True, 'count': len(order), 'order': [t['id'] for t in order],
                      'concurrency': args.concurrency, 'execute': args.execute}))
    if not args.execute:
        return
    identity = cli_identity()
    for trial in order:
        if (output_root/trial['id']).exists() or (work_root/trial['id']).exists():
            parser.error('existing first-attempt state cannot be replaced')
    output_root.mkdir(parents=True, exist_ok=True)
    with (output_root/'manifest.json').open('xb') as stream:
        stream.write(manifest_raw)
    write_json(output_root/'experiment.json', {'manifest': manifest, 'identity': identity,
                                             'qualification': qualification_record(manifest),
                                             'manifest_sha256': digest(manifest_raw),
                                             'order': [t['id'] for t in order]})
    results = []
    remaining = deque(order)
    stop_reason = None
    with concurrent.futures.ThreadPoolExecutor(max_workers=args.concurrency) as pool:
        pending = {}
        def submit_next():
            nonlocal stop_reason
            if remaining and stop_reason is None:
                try:
                    if args.manifest.read_bytes() != manifest_raw:
                        raise ValueError('manifest changed')
                    verify_frozen_files(manifest)
                except (OSError, ValueError):
                    stop_reason = 'input_drift'
                    return
                try:
                    current_identity = cli_identity()
                except (OSError, RuntimeError, subprocess.SubprocessError, ValueError):
                    stop_reason = 'configuration_drift'
                    return
                if current_identity != identity:
                    stop_reason = 'configuration_drift'
                    return
                trial = remaining.popleft()
                pending[pool.submit(run_trial, manifest, trial, work_root, output_root, identity)] = trial
        for _ in range(args.concurrency):
            submit_next()
        while pending:
            done, _ = concurrent.futures.wait(pending, return_when=concurrent.futures.FIRST_COMPLETED)
            for future in done:
                del pending[future]
                result = future.result()
                results.append(result)
                print(json.dumps({'trial_id': result['trial']['id'], 'complete': result['complete'],
                                  'failure': result['failure'], 'progress': len(results)}), flush=True)
                submit_next()
    for trial in remaining:
        results.append(skipped_trial(trial, output_root, identity, stop_reason or 'not_dispatched'))
    write_json(output_root/'summary.json', summarize(results))


if __name__ == '__main__':
    main()
