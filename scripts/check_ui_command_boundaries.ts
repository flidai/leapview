import ts from 'typescript'
import { relative, resolve } from 'node:path'
import { readFileSync } from 'node:fs'

export type UICommandBoundaryViolation = {
  file: string
  line: number
  message: string
}

const mutationMethods = new Set(['POST', 'PUT', 'PATCH', 'DELETE'])

export function inspectUICommandSource(file: string, source: string, generatedUIOperations?: ReadonlySet<string>): UICommandBoundaryViolation[] {
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
  const ir = JSON.parse(readFileSync(resolve(root, 'api/gen/json-ir.json'), 'utf8')) as {
	endpoints?: Array<{ operation_id?: string, command?: { ui?: unknown } }>
  }
  const generatedUIOperations = new Set((ir.endpoints ?? [])
	.filter((endpoint) => endpoint.command?.ui != null)
	.map((endpoint) => endpoint.operation_id ?? '')
	.filter(Boolean))
  const files = ts.sys.readDirectory(webRoot, ['.ts', '.tsx'], ['**/*.test.ts', '**/*.dom.test.ts', '**/generated/**', '**/benchmarks/**'])
  return files.flatMap((file) => inspectUICommandSource(relative(root, file), ts.sys.readFile(file) ?? '', generatedUIOperations))
}

function generatedOperationHeader(options: ts.ObjectLiteralExpression): string | undefined {
  let operationID: string | undefined
  const visit = (node: ts.Node) => {
	if (ts.isCallExpression(node) && ts.isPropertyAccessExpression(node.expression) &&
		node.expression.getText() === 'window.LeapViewCommand.headers') {
	  const operation = node.arguments[0]
	  if (operation && ts.isStringLiteralLike(operation) && operation.text.trim()) operationID = operation.text.trim()
	}
	ts.forEachChild(node, visit)
  }
  visit(options)
  return operationID
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
