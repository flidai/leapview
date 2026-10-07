import { execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { lstatSync, readFileSync, readlinkSync } from 'node:fs'
import { join } from 'node:path'

export const sha256 = bytes => createHash('sha256').update(bytes).digest('hex')
export const git = (root, ...args) => execFileSync('git', ['-C', root, ...args], { encoding: 'utf8', maxBuffer: 32 * 1024 * 1024 })

export function inventoryFileRows(root, paths, classify = () => ({})) {
  return [...new Set(paths)].sort().map(path => {
    let bytes, state
    try {
      const target = join(root, path), stat = lstatSync(target)
      if (stat.isSymbolicLink()) { state = 'symlink'; bytes = Buffer.from(readlinkSync(target)) }
      else if (stat.isFile()) { state = 'present'; bytes = readFileSync(target) }
      else state = 'non_file'
    } catch (error) {
      if (error.code !== 'ENOENT') throw error
      state = 'missing'
    }
    return { path, ...classify(path, bytes?.toString('utf8', 0, 400) ?? ''), state,
      bytes: bytes?.length ?? null, sha256: bytes ? sha256(bytes) : null, review: 'inventory-only' }
  })
}

export function sourceFilesSHA256(root) {
  const paths = git(root, 'ls-files', '--cached', '--others', '--exclude-standard', '-z').split('\0').filter(Boolean)
  const rows = inventoryFileRows(root, paths).sort((a, b) => Buffer.compare(Buffer.from(a.path), Buffer.from(b.path)))
  return sha256(rows.map(row => `${row.path}\0${row.state}\0${row.sha256 ?? ''}\n`).join(''))
}

export function checkoutSnapshot(root) {
  return { commit: git(root, 'rev-parse', 'HEAD').trim(), workingTreeStatus: git(root, 'status', '--porcelain=v1', '-z'),
    trackedDiffSHA256: sha256(git(root, 'diff', '--no-ext-diff', '--binary', 'HEAD')), sourceFilesSHA256: sourceFilesSHA256(root) }
}
