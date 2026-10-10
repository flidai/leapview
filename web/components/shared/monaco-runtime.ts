import * as monaco from 'monaco-editor-core/esm/vs/editor/editor.api'
import { shikiToMonaco, textmateThemeToMonacoTheme } from '@shikijs/monaco'
import { createHighlighterCore, type HighlighterCore } from 'shiki/core'
import { createJavaScriptRegexEngine } from 'shiki/engine/javascript'
import markdown from '@shikijs/langs/md'
import yaml from '@shikijs/langs/yaml'
import json from '@shikijs/langs/json'
import sql from '@shikijs/langs/sql'
import githubDark from '@shikijs/themes/github-dark'
import githubLight from '@shikijs/themes/github-light'

type MonacoTheme = 'github-light' | 'github-dark'

let runtimePromise: Promise<typeof monaco> | null = null
let highlighter: HighlighterCore | null = null
let themeListenerRegistered = false

export function loadMonacoRuntime(): Promise<typeof monaco> {
  runtimePromise ??= initializeMonaco()
  return runtimePromise
}

async function initializeMonaco(): Promise<typeof monaco> {
  registerWorker()
  registerLanguages()
  highlighter = await createHighlighterCore({
    themes: [githubLight, githubDark],
    langs: [markdown, yaml, json, sql],
    engine: createJavaScriptRegexEngine(),
  })
  shikiToMonaco(highlighter, monaco, {
    tokenizeMaxLineLength: 20000,
    tokenizeTimeLimit: 500,
  })
  applyTheme()
  registerThemeListener()
  return monaco
}

function registerWorker(): void {
  globalThis.MonacoEnvironment = {
    getWorker() {
      return new Worker('/static/monaco-editor-worker.js', { type: 'module' })
    },
  }
}

function registerLanguages(): void {
  for (const id of ['markdown', 'yaml', 'json', 'sql', 'text']) {
    if (!monaco.languages.getLanguages().some((language) => language.id === id)) {
      monaco.languages.register({ id })
    }
  }
}

function registerThemeListener(): void {
  if (themeListenerRegistered) return
  themeListenerRegistered = true
  document.addEventListener('leapview-theme-applied', applyTheme)
}

function applyTheme(): void {
  if (!highlighter) return
  const theme = currentTheme()
  const definition = textmateThemeToMonacoTheme(highlighter.getTheme(theme))
  const colors = currentThemeColors()
  // Resolve product aliases after the actual root theme changes. A nested dark
  // probe under a light root inherits aliases already resolved to light values.
  monaco.editor.defineTheme(theme, {
    ...definition,
    colors: {
      ...definition.colors,
      'editor.background': colors.background,
      'editorGutter.background': colors.background,
      'editorLineNumber.foreground': colors.lineNumber,
    },
  })
  highlighter.setTheme(theme)
  monaco.editor.setTheme(theme)
}

function currentTheme(): MonacoTheme {
  if (document.documentElement.style.colorScheme === 'dark') return 'github-dark'
  return 'github-light'
}

function currentThemeColors(): { background: string; lineNumber: string } {
  const probe = document.createElement('span')
  probe.style.position = 'absolute'
  probe.style.visibility = 'hidden'
  probe.style.pointerEvents = 'none'
  ;(document.body || document.documentElement).append(probe)
  try {
    return {
      background: cssColorToken(probe, '--lv-bg-panel'),
      lineNumber: cssColorToken(probe, '--lv-fg-muted'),
    }
  } finally {
    probe.remove()
  }
}

function cssColorToken(element: HTMLElement, token: string): string {
  if (!getComputedStyle(element).getPropertyValue(token).trim()) {
    throw new Error(`${token} is not defined`)
  }
  element.style.backgroundColor = `var(${token})`
  const color = getComputedStyle(element).backgroundColor
  element.style.backgroundColor = ''
  return colorToHex(color, token)
}

function colorToHex(color: string, token: string): string {
  const channels = color.match(/^rgba?\((\d+),\s*(\d+),\s*(\d+)(?:,\s*([\d.]+))?\)$/)
  if (!channels) throw new Error(`${token} did not resolve to an RGB color`)
  const [, red, green, blue, alpha] = channels
  return [
    Number(red),
    Number(green),
    Number(blue),
    alpha === undefined ? null : Math.round(Number(alpha) * 255),
  ]
    .filter((value): value is number => value !== null)
    .map((value) => value.toString(16).padStart(2, '0'))
    .join('')
    .replace(/^/, '#')
}
