/** Evaluation harness JSON boundary; no filesystem access and no production use. */
import { authoringReference, candidateFragmentSchemas, candidateSchemas, encodeSourceFiles, lowerSourceFiles, type Candidate, type SourceFiles } from './prototypes'

const [operation, arm] = Bun.argv.slice(2)
try {
  if (!['A', 'B', 'C'].includes(arm)) throw new Error('Candidate must be A, B or C')
  const candidate = arm as Candidate
  let result: unknown
  if (operation === 'schema') result = candidateSchemas[candidate]
  else if (operation === 'schemas') result = { document: candidateSchemas[candidate], fragments: candidateFragmentSchemas[candidate] }
  else if (operation === 'reference') result = authoringReference(candidate)
  else if (operation === 'encode' || operation === 'lower') {
    const input: unknown = JSON.parse(await Bun.stdin.text())
    if (!input || typeof input !== 'object' || Array.isArray(input) || Object.values(input).some(value => typeof value !== 'string')) throw new Error('Expected a JSON object mapping confined source filenames to YAML text')
    const files = input as SourceFiles
    result = operation === 'encode' ? encodeSourceFiles(files, candidate) : lowerSourceFiles(files, candidate)
  } else throw new Error('Operation must be encode, lower, schema, schemas or reference')
  process.stdout.write(JSON.stringify(result) + '\n')
} catch (error) {
  process.stderr.write((error instanceof Error ? error.message : String(error)) + '\n')
  process.exitCode = 1
}
