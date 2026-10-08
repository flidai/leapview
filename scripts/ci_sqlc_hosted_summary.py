#!/usr/bin/env python3
"""Compare the complete hosted SQLC cohort; never make an adoption decision."""

import argparse
from datetime import datetime
import hashlib
import json
from pathlib import Path
import re
import statistics

from ci_sqlc_hosted_screen import compare


def read(path):
    return json.loads(path.read_text())


def elapsed(path, clock):
    start = int((path / (clock + '-start.txt')).read_text())
    end = int((path / (clock + '-end.txt')).read_text())
    if end < start:
        raise ValueError('clock moved backwards: ' + clock)
    return end - start


def summarize(root, source, attempt=1):
    if not re.fullmatch(r'[0-9a-f]{40}', source) or attempt < 1:
        raise ValueError('exact source required')
    jobs = [job for page in read(root / 'jobs.json') for job in page['jobs']]
    samples, receipts, contexts, tools, recipes = [], [], {}, set(), {}
    for kind, pairs in [('producer', [0]), ('consumer', [1, 2, 3])]:
        for pair in pairs:
            for mode in ['baseline', 'treatment']:
                path = root / f'sqlc-sample-{attempt}-{kind}-{mode}-{pair}'
                receipt = read(path / 'receipt.json')
                if ((path / 'outcome.txt').read_text().strip() != 'success' or
                        int((path / 'pair.txt').read_text()) != pair or
                        (receipt['source'], receipt['mode'], receipt['kind']) != (source, mode, kind)):
                    raise ValueError('failed or incorrectly identified sample: ' + path.name)
                compare(receipts[0] if receipts else receipt, receipt)
                receipts.append(receipt)
                expected_marker = 'producer\n' if kind == 'producer' else 'changed-source\n'
                if receipt['marker'] != expected_marker:
                    raise ValueError('generation invalidation marker missing')
                key = (mode, kind)
                if key in contexts and contexts[key] != receipt['context_sha256']:
                    raise ValueError('same-mode consumer contexts differ')
                contexts[key] = receipt['context_sha256']
                recipe = (receipt['recipe_sha256'], receipt['script_sha256'])
                if mode in recipes and recipes[mode] != recipe:
                    raise ValueError('same-mode recipe or generator changed')
                recipes[mode] = recipe
                if mode == 'treatment':
                    if kind == 'consumer' and not receipt['tool_cache_proof']['cached']:
                        raise ValueError('tool layer did not restore')
                    tools.add(receipt['tool_identity']['binary.sha256'])
                identity = read(path / 'image-identity.json')
                if identity.get('revision') != source or identity.get('dirty') is not False:
                    raise ValueError('image identity mismatch')
                transition = read(path / 'transition.json')
                image = read(path / 'image-inspect.json')[0]['Id']
                checks = ['legacyPublication', 'legacyViewer', 'typedPolicyCaptured', 'independentApproval',
                          'publisherNoSelfApproval', 'viewerLeastPrivilege', 'realPublicationAdapter', 'subsequentDeploy']
                candidate = transition.get('candidate', {})
                if (not re.fullmatch(r'sha256:[0-9a-f]{64}', image) or transition.get('version') != 1 or
                        transition.get('status') != 'passed' or transition.get('validatorRevision') != source or
                        candidate.get('revision') != source or candidate.get('image') != image or
                        any(transition.get('checks', {}).get(check) is not True for check in checks)):
                    raise ValueError('historical qualification does not bind this image and all required checks')
                suffix = f'SQLC {kind} {mode} pair {pair}'
                matching = [job for job in jobs if job['name'] == suffix or job['name'].endswith(' / ' + suffix)]
                if len(matching) != 1 or matching[0]['conclusion'] != 'success':
                    raise ValueError('missing, ambiguous or failed job: ' + suffix)
                job = matching[0]
                seconds = (datetime.fromisoformat(job['completed_at'].replace('Z', '+00:00')) -
                           datetime.fromisoformat(job['started_at'].replace('Z', '+00:00'))).total_seconds()
                if seconds <= 0:
                    raise ValueError('invalid job duration')
                sample = dict(mode=mode, kind=kind, pair=pair, sqlc_seconds=receipt['sqlc_seconds'],
                              build_seconds=elapsed(path, 'build'), qualification_seconds=elapsed(path, 'qualification'),
                              sample_seconds=elapsed(path, 'sample'), job_seconds=seconds,
                              receipt_sha256=hashlib.sha256((path / 'receipt.json').read_bytes()).hexdigest())
                if kind == 'producer':
                    sample['storage'] = read(path / 'storage.json')
                    if sample['storage']['local_export_bytes'] <= 0:
                        raise ValueError('empty storage inventory')
                    sample['storage_diagnostic_seconds'] = elapsed(path, 'storage')
                samples.append(sample)
    if len(tools) != 1:
        raise ValueError('treatment binary identity changed across samples')
    for mode in ['baseline', 'treatment']:
        if contexts[mode, 'producer'] == contexts[mode, 'consumer']:
            raise ValueError('producer and consumer context did not change')
    modes = {}
    for mode in ['baseline', 'treatment']:
        cohort = [sample for sample in samples if sample['mode'] == mode]
        producer = next(sample for sample in cohort if sample['kind'] == 'producer')
        consumers = [sample for sample in cohort if sample['kind'] == 'consumer']
        median_job = statistics.median(sample['job_seconds'] for sample in consumers)
        modes[mode] = dict(
            producer_job_seconds=producer['job_seconds'],
            median_consumer_build_seconds=statistics.median(sample['build_seconds'] for sample in consumers),
            median_consumer_job_seconds=median_job,
            median_plus_one_third_producer_job_seconds=median_job + producer['job_seconds'] / 3,
            median_plus_entire_producer_job_seconds=median_job + producer['job_seconds'],
            local_cache_export_bytes=producer['storage']['local_export_bytes'])
    return dict(status='passed', source=source, samples=samples, modes=modes, adoption_qualified=False,
                generated_files=len(receipts[0]['manifest']), limits=[
                    'Three consumer pairs; no ten confirmations or whole-CI p95 conclusion.',
                    'Pair labels identify samples, not guaranteed execution order or identical hardware.',
                    'Local cache inventory is a storage proxy, not attributed GHA storage or billing.',
                    'Job costs include diagnostic exports and fixture setup; one-third allocation assumes three consumers.',
                    'Raw jobs preserve runner and timing context; queue and monetary billing are not inferred.'])


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--samples', required=True, type=Path)
    parser.add_argument('--source', required=True)
    parser.add_argument('--attempt', required=True, type=int)
    args = parser.parse_args()
    args.samples.mkdir(parents=True, exist_ok=True)
    result = {'status': 'failed', 'source': args.source, 'adoption_qualified': False}
    try:
        result = summarize(args.samples, args.source, args.attempt)
    except Exception as error:
        result['error'] = str(error)
        raise
    finally:
        (args.samples / 'comparison.json').write_text(json.dumps(result, indent=2) + '\n')
    print(json.dumps(result['modes'], indent=2))


if __name__ == '__main__':
    main()
