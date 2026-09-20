export async function avatarResponseError(response: Response, fallback: string): Promise<Error> {
  try {
    const problem = await response.json() as { detail?: unknown }
    if (typeof problem.detail === 'string' && problem.detail.trim()) return new Error(problem.detail)
  } catch {
    // Network proxies and non-JSON responses use the status-bearing fallback.
  }
  return new Error(`${fallback} (${response.status})`)
}
