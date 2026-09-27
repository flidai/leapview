"""Prepare the exact deployed source before entering a clone-only transport."""
from contextlib import contextmanager
import os
from pathlib import Path
import re
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]


@contextmanager
def publication_source(revision):
    if not re.fullmatch(r'[0-9a-f]{40}', revision):
        raise ValueError('Publication requires the exact admitted source revision')
    dataset = os.environ.get('DEMO_DATASET', 'olist')
    assets = {'cfo': ('bootstrapfinance', '.data/cfo-demo'),
              'olist': ('bootstrapolist', '.data/olist')}
    if dataset not in assets:
        raise ValueError('DEMO_DATASET must be olist or cfo')
    tool, output = assets[dataset]
    with tempfile.TemporaryDirectory() as directory:
        root = Path(directory)/'source'
        subprocess.run(['git', 'worktree', 'add', '--detach', str(root), revision], cwd=ROOT, check=True)
        try:
            subprocess.run(['task', 'generate'], cwd=root, check=True)
            # Warm required Go build artifacts before clone-only networking is
            # installed. Publication itself must not fetch modules from a network.
            subprocess.run(['go', 'build', '-o', str(Path(directory)/'leapview'), './cmd/leapview'], cwd=root, check=True)
            # The real adapter verifies and reuses these immutable inputs.
            # Download/cache misses must happen before all clients are pinned
            # to the clone, whose proxy deliberately refuses dataset origins.
            subprocess.run(['go', 'run', './internal/app/tools/' + tool,
                            '--shared-cache', '--out', output], cwd=root, check=True)
            yield root
        finally:
            subprocess.run(['git', 'worktree', 'remove', '--force', str(root)], cwd=ROOT, check=True)


def publish(root, revision, env):
    actual = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=root, text=True).strip()
    if actual != revision:
        raise ValueError('Publication source differs from the admitted candidate')
    prepared = dict(env, DEMO_SOURCE_REVISION=revision, GOPROXY='off', GOSUMDB='off')
    subprocess.run(['bash', 'scripts/deploy_demo.sh'], cwd=root, env=prepared, check=True, timeout=1800)
