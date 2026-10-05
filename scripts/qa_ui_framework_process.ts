const capturedOutputLimit = 48_000
const reportedOutputLimit = 8_000

export function appendCapturedOutput(output: string, chunk: string, limit = capturedOutputLimit): string {
  return `${output}${chunk}`.slice(-limit)
}

export function formatManagedStartupFailure(message: string, output: string): string {
  const diagnostics = output
    .replace(/(["']?[A-Za-z_][A-Za-z0-9_.-]*["']?)(\s*)([:=])(\s*)(?:"[^"]*"|'[^']*'|[^\s,;]+)/gi, (assignment, key: string, beforeSeparator: string, separator: string, afterSeparator: string) => {
      const normalizedKey = key.replace(/^["']|["']$/g, '').toLowerCase().replace(/[^a-z0-9]/g, '')
      return /(?:password|passwd|token|secret|apikey|accesskey|credential)s?$/.test(normalizedKey)
        ? `${key}${beforeSeparator}${separator}${afterSeparator}[REDACTED]`
        : assignment
    })
    .replace(/((?:password|passwd|token|secret|api[_-]?key|access[_-]?key|credential)s?\s*[:=]\s*)(?:"[^"]*"|'[^']*'|[^\s,;]+)/gi, '$1[REDACTED]')
    .replace(/\blv_pat_[A-Za-z0-9_-]*/g, 'lv_pat_[REDACTED]')
    .replace(/\b(Bearer\s+)[A-Za-z0-9._~+/-]+=*/gi, '$1[REDACTED]')
    .replace(/(\b(?:postgres(?:ql)?|mysql|redis):\/\/[^:\s@]+:)[^@\s]+(@)/gi, '$1[REDACTED]$2')
    .slice(-reportedOutputLimit)
    .trim()

  return diagnostics
    ? `${message}\n\nLast output from task dev:\n${diagnostics}`
    : message
}

export async function drainProcessOutput(
  readers: Promise<void>[],
  cancelReaders: () => Promise<void>,
  drainLimitMs = 500,
  cancelLimitMs = 250,
): Promise<void> {
  if (await settlesWithin(Promise.allSettled(readers), drainLimitMs)) return
  await settlesWithin(Promise.resolve().then(cancelReaders), cancelLimitMs)
}

function settlesWithin(operation: Promise<unknown>, limitMs: number): Promise<boolean> {
  return new Promise((resolve) => {
    const timer = setTimeout(() => resolve(false), limitMs)
    void operation.then(
      () => {
        clearTimeout(timer)
        resolve(true)
      },
      () => {
        clearTimeout(timer)
        resolve(true)
      },
    )
  })
}
