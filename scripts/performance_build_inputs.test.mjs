import test from 'node:test'
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fingerprintGoBuildInputs } from './go_receipts.mjs'

test('unexpected untracked Go code cannot inherit the clean HEAD build identity', () => {
  const root = mkdtempSync(join(tmpdir(), 'capacity-source-admission-'))
  try {
    execFileSync('git', ['init', '-q', root])
    writeFileSync(join(root, 'injected_test.go'), 'package injected\n')
    // Rejection precedes go list/compiler invocation, including without Go.
    assert.throws(() => fingerprintGoBuildInputs(root), /unexpected untracked Go sources: injected_test.go/)
  } finally { rmSync(root, { recursive: true, force: true }) }
})
