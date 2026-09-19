const datastarRuntimePath = '/static/vendor/datastar-1.0.2.js'
export const datastarRuntimeURL = `${datastarRuntimePath}?v=dev`

export type DatastarEffect = () => void
export type DatastarRuntime = {
  actions: Record<string, unknown>
  effect(fn: () => void): DatastarEffect
  getPath<T = unknown>(path: string): T | undefined
  mergePatch(patch: Record<string, unknown>, options?: Record<string, unknown>): void
  mergePaths(paths: [string, unknown][], options?: Record<string, unknown>): void
  root: Record<string, unknown>
}

let runtimePromise: Promise<DatastarRuntime> | null = null

export function loadDatastarRuntime(): Promise<DatastarRuntime> {
  runtimePromise ??= import(resolveDatastarRuntimeURL()) as Promise<DatastarRuntime>
  return runtimePromise
}

function resolveDatastarRuntimeURL(): string {
  if (typeof document === 'undefined') return datastarRuntimeURL

  // The page shell imports Datastar with the asset resolver's cache-busting
  // query. Reuse that exact URL so the Lit bridge joins the existing module
  // instance instead of initializing a second Datastar runtime.
  const script = Array.from(document.querySelectorAll<HTMLScriptElement>('script[src]'))
    .find((candidate) => {
      try {
        return new URL(candidate.src, document.baseURI).pathname === datastarRuntimePath
      } catch {
        return false
      }
    })
  return script?.src || datastarRuntimeURL
}
