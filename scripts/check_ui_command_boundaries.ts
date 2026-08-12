import ts from 'typescript'
import { relative, resolve } from 'node:path'

export type UICommandBoundaryViolation = {
  file: string
  line: number
  message: string
}

const mutationMethods = new Set(['POST', 'PUT', 'PATCH', 'DELETE'])

export function inspectUICommandSource(file: string, source: string): UICommandBoundaryViolation[] {
  const parsed = ts.createSourceFile(file, source, ts.ScriptTarget.Latest, true)
  const violations: UICommandBoundaryViolation[] = []
  const report = (node: ts.Node, message: string) => {
    const line = parsed.getLineAndCharacterOfPosition(node.getStart(parsed)).line + 1
    violations.push({ file, line, message })
  }
  const visit = (node: ts.Node) => {
	if (ts.isCallExpression(node) && ts.isIdentifier(node.expression) && node.expression.text === 'fetch') {
	  const options = node.arguments[1]
	  if (options && !ts.isObjectLiteralExpression(options)) {
		report(options, 'fetch options are dynamic, so the UI boundary cannot prove the request is read-only')
	  } else if (options) {
		const method = options.properties.find((property) =>
		  property.name?.getText(parsed).replaceAll(/['"]/g, '') === 'method')
        if (method) {
		  const initializer = ts.isPropertyAssignment(method) ? method.initializer : undefined
		  if (!initializer || !ts.isStringLiteralLike(initializer) || mutationMethods.has(initializer.text.toUpperCase())) {
            report(method, 'direct mutating fetch bypasses the generated UI command transport')
          }
        }
      }
    }
    if (ts.isStringLiteralLike(node)) {
      if (node.text.includes('@post(') || node.text.includes('@patch(')) {
        report(node, 'direct Datastar mutation expression bypasses a classified UI action helper')
      }
      if (node.text.toLowerCase() === 'x-leapview-operation-id' && !file.endsWith('web/components/shared/command.ts')) {
        report(node, 'operation identity headers must be authored by the shared command transport')
      }
    }
    if (ts.isBinaryExpression(node) && node.operatorToken.kind === ts.SyntaxKind.EqualsToken &&
      ts.isPropertyAccessExpression(node.left) && node.left.expression.getText(parsed) === 'window' && node.left.name.text === 'LeapViewCommand' &&
      !file.endsWith('web/components/shared/command.ts')) {
      report(node, 'the LeapView command transport may only be installed by the shared command module')
    }
    ts.forEachChild(node, visit)
  }
  visit(parsed)
  return violations
}

export function checkUICommandBoundaries(root = process.cwd()): UICommandBoundaryViolation[] {
  const webRoot = resolve(root, 'web')
  const files = ts.sys.readDirectory(webRoot, ['.ts', '.tsx'], ['**/*.test.ts', '**/*.dom.test.ts', '**/generated/**', '**/benchmarks/**'])
  return files.flatMap((file) => inspectUICommandSource(relative(root, file), ts.sys.readFile(file) ?? ''))
}

if (import.meta.main) {
  const violations = checkUICommandBoundaries()
  if (violations.length > 0) {
    for (const violation of violations) {
      console.error(`${violation.file}:${violation.line}: ${violation.message}`)
    }
    process.exit(1)
  }
}
