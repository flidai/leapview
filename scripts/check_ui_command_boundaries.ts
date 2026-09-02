import { parse } from '@babel/parser'
import { relative, resolve } from 'node:path'
import { readFileSync } from 'node:fs'

export type UICommandBoundaryViolation = {
  file: string
  line: number
  message: string
}

type ASTNode = {
  type: string
  loc?: { start: { line: number } } | null
  [key: string]: unknown
}

const mutationMethods = new Set(['POST', 'PUT', 'PATCH', 'DELETE'])

export function inspectUICommandSource(file: string, source: string, generatedUIOperations?: ReadonlySet<string>): UICommandBoundaryViolation[] {
  let parsed: ASTNode
  try {
    parsed = parse(source, { sourceType: 'module', plugins: ['typescript', 'jsx', 'decorators'] }) as unknown as ASTNode
  } catch (error) {
    const line = parserErrorLine(error)
    return [{ file, line, message: 'UI boundary source could not be parsed safely' }]
  }
  const violations: UICommandBoundaryViolation[] = []
  const report = (node: ASTNode, message: string) => violations.push({ file, line: node.loc?.start.line ?? 1, message })
  const visit = (node: ASTNode) => {
    if (node.type === 'CallExpression' && identifierName(node.callee) === 'fetch') {
      const options = nodeArguments(node)[1]
      if (options && options.type !== 'ObjectExpression') {
        report(options, 'fetch options are dynamic, so the UI boundary cannot prove the request is read-only')
      } else if (options) {
        const method = nodeArray(options.properties).find((property) => objectPropertyName(property) === 'method')
        if (method) {
          const initializer = method.type === 'ObjectProperty' ? asNode(method.value) : undefined
          const methodName = initializer ? stringValue(initializer) : undefined
          if (!methodName || mutationMethods.has(methodName.toUpperCase())) {
            const operationID = generatedOperationHeader(options)
            if (!operationID) {
              report(method, 'direct mutating fetch bypasses the generated UI command transport')
            } else if (generatedUIOperations && !generatedUIOperations.has(operationID)) {
              report(method, `direct mutating fetch references non-generated UI operation ${JSON.stringify(operationID)}`)
            }
          }
        }
      }
    }
    const literal = stringValue(node)
    if (literal !== undefined) {
      if (literal.includes('@post(') || literal.includes('@patch(')) {
        report(node, 'direct Datastar mutation expression bypasses a classified UI action helper')
      }
      if (literal.toLowerCase() === 'x-leapview-operation-id' && !file.endsWith('web/components/shared/command.ts')) {
        report(node, 'operation identity headers must be authored by the shared command transport')
      }
    }
    if (node.type === 'AssignmentExpression' && node.operator === '=' && memberPath(node.left) === 'window.LeapViewCommand' &&
      !file.endsWith('web/components/shared/command.ts')) {
      report(node, 'the LeapView command transport may only be installed by the shared command module')
    }
    for (const child of childNodes(node)) visit(child)
  }
  visit(parsed)
  return violations
}

export async function checkUICommandBoundaries(root = process.cwd()): Promise<UICommandBoundaryViolation[]> {
  const webRoot = resolve(root, 'web')
  const ir = JSON.parse(readFileSync(resolve(root, 'api/gen/json-ir.json'), 'utf8')) as {
    endpoints?: Array<{ operation_id?: string, command?: { ui?: unknown } }>
  }
  const generatedUIOperations = new Set((ir.endpoints ?? [])
    .filter((endpoint) => endpoint.command?.ui != null)
    .map((endpoint) => endpoint.operation_id ?? '')
    .filter(Boolean))
  const glob = new Bun.Glob('**/*.{ts,tsx}')
  const files: string[] = []
  for await (const file of glob.scan({ cwd: webRoot, onlyFiles: true })) {
    if (/\.(?:dom\.)?test\.tsx?$/.test(file) || file.includes('/generated/') || file.startsWith('generated/') || file.includes('/benchmarks/')) continue
    files.push(file)
  }
  files.sort()
  const results = await Promise.all(files.map(async (file) => inspectUICommandSource(
    relative(root, resolve(webRoot, file)),
    await Bun.file(resolve(webRoot, file)).text(),
    generatedUIOperations,
  )))
  return results.flat()
}

function generatedOperationHeader(options: ASTNode): string | undefined {
  let operationID: string | undefined
  const visit = (node: ASTNode) => {
    if (node.type === 'CallExpression' && memberPath(node.callee) === 'window.LeapViewCommand.headers') {
      const operation = nodeArguments(node)[0]
      const value = operation ? stringValue(operation)?.trim() : undefined
      if (value) operationID = value
    }
    for (const child of childNodes(node)) visit(child)
  }
  visit(options)
  return operationID
}

function childNodes(node: ASTNode): ASTNode[] {
  const children: ASTNode[] = []
  for (const value of Object.values(node)) {
    if (isNode(value)) children.push(value)
    else if (Array.isArray(value)) for (const item of value) if (isNode(item)) children.push(item)
  }
  return children
}

function isNode(value: unknown): value is ASTNode {
  return Boolean(value && typeof value === 'object' && typeof (value as { type?: unknown }).type === 'string')
}

function asNode(value: unknown): ASTNode | undefined { return isNode(value) ? value : undefined }

function nodeArray(value: unknown): ASTNode[] {
  return Array.isArray(value) ? value.filter(isNode) : []
}

function nodeArguments(node: ASTNode): ASTNode[] { return nodeArray(node.arguments) }

function identifierName(value: unknown): string | undefined {
  const node = asNode(value)
  return node?.type === 'Identifier' && typeof node.name === 'string' ? node.name : undefined
}

function objectPropertyName(node: ASTNode): string | undefined {
  if (node.type !== 'ObjectProperty' && node.type !== 'ObjectMethod') return undefined
  const key = asNode(node.key)
  return key ? identifierName(key) ?? stringValue(key) : undefined
}

function stringValue(node: ASTNode): string | undefined {
  if (node.type === 'StringLiteral' && typeof node.value === 'string') return node.value
  if (node.type === 'TemplateLiteral' && nodeArray(node.expressions).length === 0) {
    const quasi = nodeArray(node.quasis)[0]
    const value = quasi?.value
    if (value && typeof value === 'object' && typeof (value as { cooked?: unknown }).cooked === 'string') return (value as { cooked: string }).cooked
  }
  return undefined
}

function memberPath(value: unknown): string | undefined {
  const node = asNode(value)
  if (!node) return undefined
  const identifier = identifierName(node)
  if (identifier) return identifier
  if (node.type !== 'MemberExpression' && node.type !== 'OptionalMemberExpression') return undefined
  const object = memberPath(node.object)
  const property = asNode(node.property)
  const name = node.computed ? property && stringValue(property) : property && identifierName(property)
  return object && name ? `${object}.${name}` : undefined
}

function parserErrorLine(error: unknown): number {
  if (!error || typeof error !== 'object') return 1
  const loc = (error as { loc?: { line?: unknown } }).loc
  return typeof loc?.line === 'number' ? loc.line : 1
}

if (import.meta.main) {
  const violations = await checkUICommandBoundaries()
  if (violations.length > 0) {
    for (const violation of violations) console.error(`${violation.file}:${violation.line}: ${violation.message}`)
    process.exit(1)
  }
}
