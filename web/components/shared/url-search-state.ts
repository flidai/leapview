type HistoryMode = 'push' | 'replace'

export function updateURLSearchParameter(name: string, value: string, mode: HistoryMode): void {
  const next = new URL(window.location.href)
  if (value) {
    next.searchParams.set(name, value)
  } else {
    next.searchParams.delete(name)
  }
  const target = `${next.pathname}${next.search}${next.hash}`
  const current = `${window.location.pathname}${window.location.search}${window.location.hash}`
  if (target === current) return
  if (mode === 'push') {
    window.history.pushState(window.history.state, '', target)
    return
  }
  window.history.replaceState(window.history.state, '', target)
}
