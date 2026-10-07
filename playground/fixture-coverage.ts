export interface FixtureControl {
  label: string
  choices: string[]
}

/** Describe authored playground controls, never production component internals. */
export function fixtureCoverage(root?: ShadowRoot | null): FixtureControl[] {
  if (!root) return []
  return Array.from(root.querySelectorAll<HTMLSelectElement | HTMLInputElement>(
    '[data-fixture-controls] select, [data-fixture-controls] input[type="checkbox"]',
  )).flatMap(control => {
    const label = control.getAttribute('aria-label') || Array.from(control.labels || [])
      .map(label => Array.from(label.childNodes)
        .filter(node => node !== control && (node.nodeType === Node.TEXT_NODE || node.nodeType === Node.ELEMENT_NODE))
        .map(node => node.textContent).join(' ')).join(' ')
    if (!label.trim()) return []
    return [{
      label: label.trim(),
      choices: control instanceof HTMLSelectElement
        ? Array.from(control.options).map(option => option.label)
        : ['Off', 'On'],
    }]
  })
}
