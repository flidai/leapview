import { css, html, type TemplateResult } from 'lit'

/** Playground-only disclosure; production previews keep their own markup and styles. */
export function exampleDetails(content: TemplateResult) {
  return html`<details class="example-details"><summary>Usage & events</summary><div class="inspector-content">${content}</div></details>`
}

export const exampleChromeStyles = css`
  .example-details { margin-top: var(--base-size-16); border: var(--lv-border-muted); border-radius: var(--lv-radius-default); padding: 0; background: var(--lv-bg-panel); }
  .example-details > summary { margin: 0; padding: var(--base-size-12) var(--base-size-16); color: var(--lv-fg-muted); cursor: pointer; font: var(--lv-type-body-compact); }
  .example-details > summary:hover { color: var(--lv-fg-default); background: var(--lv-bg-panel-muted); }
  .example-details > summary:focus-visible { outline: var(--borderWidth-thick) solid var(--focus-outlineColor); outline-offset: var(--base-size-2); }
  :host > .stack > .example-details { margin-top: 0; }
  .example-details[open] > summary { border-bottom: var(--lv-border-muted); }
  .inspector-content { display: grid; gap: var(--base-size-16); min-width: 0; padding: var(--base-size-16); }
  .inspector-content .documentation { margin-top: 0; }
  .inspector-content p { margin-block: 0; }
  :host([preview-only]) .example-details { display: none; }
  :host > .controls, :host > .stack > .controls, :host > .stack > .state-panel {
    display: flex; flex-wrap: wrap; align-items: end; gap: var(--base-size-12);
    box-sizing: border-box; margin: 0 0 var(--base-size-16); padding: var(--base-size-12);
    border: var(--lv-border-muted); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel-muted);
  }
  :host > .stack > .controls, :host > .stack > .state-panel { margin-bottom: 0; }
  :host([preview-only]) .controls, :host([preview-only]) .state-panel { display: none; }
  @media (max-width: 500px) { .inspector-content { padding: var(--base-size-12); } }
`
