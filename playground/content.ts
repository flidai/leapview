import { previewCode } from './example-code'
import { readState } from './example-state'
import { LitElement, css, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import { keyed } from 'lit/directives/keyed.js'
import { exampleDetails, exampleChromeStyles } from './example-chrome'
import { settingsLayoutStyles } from '../web/components/shared/settings-layout'
import '../web/components/shared/code-editor'
import '../web/components/shared/code-block'
import '../web/components/shared/config-viewer'
import '../web/components/shared/markdown-view'
import '../web/components/shared/visual-artifact'
import '../web/components/dashboard/visual-modal'
import '../web/components/chat/chat-composer'
import { matchesReferenceQuery, referenceIdentity, type ChatContextReference, type ChatReferenceSearchDetail, type ChatReferencesChangeDetail } from '../web/components/chat/reference'
import { codeFixtures, composerReferences, configurationJSON, configurationYAML, createArtifactFixture, markdownFixture, type ArtifactState } from './content-fixtures'

export { contentExamples } from './content-fixtures'

const documentation: Record<string, { tag: string; source: string; properties: string; events: string; note: string; usage: string }> = {
  'chat-composer': {
    tag: 'lv-chat-composer', source: 'web/components/chat/chat-composer.ts',
    properties: 'value · disabled · pending · running · runId · canContinue · editing · editMessageId · references · pinnedSuggestions · suggestions · suggestionQuery · suggestionRequestId · referenceLimit · acceptedRunId',
    events: 'lv-chat-submit { input, references, editMessageId? } · lv-chat-stop { runId } · lv-chat-edit-cancel { editMessageId } · lv-chat-reference-search { query, requestId } · lv-chat-references-change { references }',
    note: 'Send and stop commands are logged locally. Type @ or use Add context to search deterministic references. Accept draft exercises the public acceptedRunId response; no chat service is connected.',
    usage: '<lv-chat-composer value="Summarize regional revenue" .references=${references} .suggestions=${suggestions} @lv-chat-submit=${onSubmit}></lv-chat-composer>',
  },
  'code-editor': {
    tag: 'lv-code-editor', source: 'web/components/shared/code-editor.ts',
    properties: 'value · language (sql / yaml / json / markdown / text) · disabled · aria-label',
    events: 'lv-code-editor-change { value }',
    note: 'Edit the local document to inspect change events. Disabled sets Monaco to read-only. The production component falls back to basic editing if its editor assets fail to load.',
    usage: '<lv-code-editor .value=${source} language="sql" aria-label="Query editor" @lv-code-editor-change=${onChange}></lv-code-editor>',
  },
  'code-block': {
    tag: 'lv-code-block', source: 'web/components/shared/code-block.ts',
    properties: 'code · language · toolbar · copy · compact · dense · inline · format (SQL) · highlightedLines',
    events: 'No custom events; Copy uses the system clipboard.',
    note: 'Production Shiki highlighting, SQL formatting, and clipboard behavior. Unsupported text languages use the plain-text fallback. An empty source displays the component’s “Loading...” placeholder.',
    usage: '<lv-code-block .code=${source} language="sql" toolbar copy .highlightedLines=${[2, 3]}></lv-code-block>',
  },
  'config-viewer': {
    tag: 'lv-config-viewer', source: 'web/components/shared/config-viewer.ts',
    properties: 'configuration · language (yaml / json) · defaultView (outline / raw)',
    events: 'No custom events; filtering, expansion, source view, and copying are owned by the component.',
    note: 'Filter keys or values, collapse branches, and switch to Source. Invalid and empty fixtures exercise the production parse-error and loading messages.',
    usage: '<lv-config-viewer .configuration=${source} language="yaml" defaultView="outline"></lv-config-viewer>',
  },
  'markdown-view': {
    tag: 'lv-markdown-view', source: 'web/components/shared/markdown-view.ts',
    properties: 'value · compact · emptyText', events: 'No custom events; links use native browser navigation.',
    note: 'Production MarkdownIt rendering and DOMPurify sanitization, with headings, lists, a table, a quote, code, and a local navigation link.',
    usage: '<lv-markdown-view .value=${markdown} emptyText="No notes yet."></lv-markdown-view>',
  },
  'visual-artifact': {
    tag: 'lv-visual-artifact', source: 'web/components/shared/visual-artifact.ts',
    properties: 'type · artifactId · payload (VisualizationEnvelope) · explorerHref',
    events: 'lv-visual-action · lv-visualization-observation (bubbled from the visualization host)',
    note: 'A production artifact wraps the chart host with an optional navigation action and row-limit notice. The navigation action points to the local chart example. Selection interactions are omitted from this informational fixture.',
    usage: '<lv-visual-artifact type="bar" artifact-id="regional-revenue" .payload=${envelope} .explorerHref=${"#charts/bar"}></lv-visual-artifact>',
  },
}

export class PlaygroundContent extends LitElement {
  @property() example = 'code-editor'
  @state() private language = 'sql'
  @state() private source = codeFixtures.sql!
  @state() private readOnly = false
  @state() private compact = false
  @state() private dense = false
  @state() private inline = false
  @state() private format = false
  @state() private toolbar = true
  @state() private copy = true
  @state() private highlight = false
  @state() private empty = false
  @state() private configState = 'valid'
  @state() private configView = 'outline'
  @state() private artifactState: ArtifactState = 'ready'
  @state() private explorerLink = true
  @state() private artifact = createArtifactFixture('ready')
  @state() private composerState = 'ready'
  @state() private composerDraft = 'Summarize revenue by region.'
  @state() private references: ChatContextReference[] = []
  @state() private suggestions: ChatContextReference[] = []
  @state() private suggestionQuery = ''
  @state() private suggestionRequestId = 0
  @state() private acceptedRunId = ''
  @state() private composerRevision = 0
  private acceptedRunSequence = 0
  @state() private logs: string[] = []

  getExampleCode() {
    const composer = this.renderRoot.querySelector<HTMLElement & { getDraft(): string }>('lv-chat-composer')
    const inputs = new Map<Element, Record<string, unknown>>()
    if (composer) inputs.set(composer, { value: composer.getDraft() })
    return previewCode(this, [
      "import '../web/components/shared/code-editor'",
      "import '../web/components/shared/code-block'",
      "import '../web/components/shared/config-viewer'",
      "import '../web/components/shared/markdown-view'",
      "import '../web/components/shared/visual-artifact'",
      "import '../web/components/dashboard/visual-modal'",
      "import '../web/components/chat/chat-composer'",
    ], [], inputs)
  }

  getExampleState() {
    return {
      language: this.language,
      source: this.source,
      readOnly: this.readOnly,
      compact: this.compact,
      dense: this.dense,
      inline: this.inline,
      format: this.format,
      toolbar: this.toolbar,
      copy: this.copy,
      highlight: this.highlight,
      empty: this.empty,
      configState: this.configState,
      configView: this.configView,
      artifactState: this.artifactState,
      explorerLink: this.explorerLink,
      composerState: this.composerState,
      composerDraft: this.renderRoot.querySelector<HTMLElement & { getDraft(): string }>('lv-chat-composer')?.getDraft() ?? this.composerDraft,
      referenceIds: this.references.map(reference => referenceIdentity(reference)),
    }
  }

  async restoreExampleState(value: Record<string, unknown>) {
    await this.updateComplete
    const { referenceIds, ...controls } = readState(value, this.getExampleState(), { language: Object.keys(codeFixtures), configState: ['valid', 'invalid', 'empty'], configView: ['outline', 'raw'], artifactState: ['ready', 'limited', 'loading', 'empty', 'error', 'unavailable', 'unsupported'], composerState: ['ready', 'disabled', 'pending', 'running', 'editing', 'continue'], referenceIds: composerReferences.map(reference => referenceIdentity(reference)) })
    Object.assign(this, controls)
    this.references = composerReferences.filter(reference => referenceIds.includes(referenceIdentity(reference)))
    this.artifact = createArtifactFixture(this.artifactState)
    this.composerRevision++
  }

  static styles = [settingsLayoutStyles, css`
    :host { display: block; min-width: 0; color: var(--lv-fg-default); font: var(--lv-type-body); }
    * { box-sizing: border-box; }
    .stack { display: grid; gap: var(--base-size-20); }
    .controls { display: flex; flex-wrap: wrap; align-items: end; gap: var(--base-size-12); padding: var(--base-size-12); background: var(--lv-bg-panel-muted); border: var(--lv-border-muted); border-radius: var(--lv-radius-default); }
    .controls label { display: grid; gap: var(--base-size-4); font: var(--lv-type-caption); }
    .controls label.check { display: flex; align-items: center; min-height: var(--control-medium-size); }
    input[type=checkbox] { accent-color: var(--lv-bg-accent); }
    .preview { min-width: 0; min-height: 15rem; padding: var(--base-size-24); border: var(--lv-border-muted); border-radius: var(--lv-radius-large); background: var(--lv-bg-panel); }
    .artifact { height: var(--playground-preview-height, 420px); }
    lv-visual-artifact { display: block; height: 100%; }
    .composer-preview { padding-top: 16rem; }
    lv-chat-composer { display: block; max-width: 50rem; margin-inline: auto; }
    lv-code-editor, lv-code-block, lv-config-viewer, lv-markdown-view { min-width: 0; max-width: 100%; }
    .note, dd, .empty-log { color: var(--lv-fg-muted); }
    p { margin: 0; }
    dl { display: grid; gap: var(--base-size-12); margin: 0; }
    dt { margin-bottom: var(--base-size-4); font-weight: var(--base-text-weight-semibold); }
    dd { margin: 0; overflow-wrap: anywhere; }
    code, pre { font-family: var(--fontStack-monospace); font-size: .8rem; overflow-wrap: anywhere; }
    pre { max-height: 22rem; overflow: auto; white-space: pre-wrap; margin: 0; padding: var(--base-size-12); background: var(--lv-bg-panel-muted); border-radius: var(--lv-radius-default); }
    details summary { margin-bottom: var(--base-size-12); cursor: pointer; }
    .log { display: grid; gap: var(--base-size-12); border-top: var(--lv-border-muted); padding-top: var(--base-size-20); }
    .log-heading { display: flex; align-items: center; justify-content: space-between; gap: var(--base-size-12); }
    h3 { font: var(--lv-type-section-title); margin: 0; }
    :host([preview-only]) .controls, :host([preview-only]) .note, :host([preview-only]) .documentation, :host([preview-only]) .usage, :host([preview-only]) .log { display: none; }
    :host([preview-only]) .preview { border: 0; border-radius: 0; }
    @media (max-width: 500px) { .preview { padding: var(--base-size-12); } }
  `, exampleChromeStyles]

  protected willUpdate(changed: Map<PropertyKey, unknown>) {
    if (changed.has('example')) this.reset()
  }

  private reset() {
    this.language = this.example === 'config-viewer' ? 'yaml' : 'sql'
    this.source = codeFixtures[this.language]!
    this.readOnly = this.compact = this.dense = this.inline = this.format = this.highlight = this.empty = false
    this.toolbar = this.copy = this.explorerLink = true
    this.configState = 'valid'
    this.configView = 'outline'
    this.artifactState = 'ready'
    this.artifact = createArtifactFixture('ready')
    this.composerState = 'ready'
    this.composerDraft = 'Summarize revenue by region.'
    this.references = []
    this.suggestions = []
    this.suggestionQuery = ''
    this.suggestionRequestId = 0
    this.acceptedRunId = ''
    this.composerRevision += 1
    this.logs = []
  }

  private record = (event: CustomEvent) => {
    this.logs = [`${event.type}  ${JSON.stringify(event.detail)}`, ...this.logs].slice(0, 12)
    this.dispatchEvent(new CustomEvent('playground-event', { bubbles: true, composed: true, detail: { name: event.type, detail: event.detail } }))
  }

  private select(label: string, value: string, options: string[], change: (value: string) => void) {
    return html`<label>${label}<select class="settings-input" aria-label=${label} .value=${value} @change=${(event: Event) => change((event.target as HTMLSelectElement).value)}>${options.map((option) => html`<option value=${option}>${option}</option>`)}</select></label>`
  }

  private check(label: string, value: boolean, change: (value: boolean) => void) {
    return html`<label class="check"><input type="checkbox" .checked=${value} @change=${(event: Event) => change((event.target as HTMLInputElement).checked)}>${label}</label>`
  }

  private renderControls() {
    const code = this.example === 'code-editor' || this.example === 'code-block'
    return html`<div class="controls" aria-label="Content fixture controls">
      ${code || this.example === 'config-viewer' ? this.select('Language', this.language,
        this.example === 'code-editor' ? ['sql', 'yaml', 'json', 'markdown', 'text'] : this.example === 'code-block' ? ['sql', 'yaml', 'json', 'shell', 'toon', 'text'] : ['yaml', 'json'],
        (value) => { this.language = value; this.source = codeFixtures[value]! }) : nothing}
      ${this.example === 'code-editor' ? this.check('Read-only / disabled', this.readOnly, (value) => { this.readOnly = value }) : nothing}
      ${code || this.example === 'markdown-view' ? this.check('Empty content', this.empty, (value) => { this.empty = value }) : nothing}
      ${this.example === 'code-block' || this.example === 'markdown-view' ? this.check('Compact', this.compact, (value) => { this.compact = value }) : nothing}
      ${this.example === 'code-block' ? html`
        ${this.check('Toolbar', this.toolbar, (value) => { this.toolbar = value })}
        ${this.check('Copy', this.copy, (value) => { this.copy = value })}
        ${this.check('Dense', this.dense, (value) => { this.dense = value })}
        ${this.check('Inline', this.inline, (value) => { this.inline = value })}
        ${this.language === 'sql' ? this.check('Format SQL', this.format, (value) => { this.format = value }) : nothing}
        ${this.check('Highlight lines 2–3', this.highlight, (value) => { this.highlight = value })}
      ` : nothing}
      ${this.example === 'config-viewer' ? html`
        ${this.select('Content state', this.configState, ['valid', 'invalid', 'empty'], (value) => { this.configState = value })}
        ${this.select('Initial view', this.configView, ['outline', 'raw'], (value) => { this.configView = value })}
      ` : nothing}
      ${this.example === 'visual-artifact' ? html`
        ${this.select('Artifact state', this.artifactState, ['ready', 'limited', 'loading', 'empty', 'error', 'unavailable', 'unsupported'], (value) => { this.artifactState = value as ArtifactState; this.artifact = createArtifactFixture(this.artifactState) })}
        ${this.check('Explorer link', this.explorerLink, (value) => { this.explorerLink = value })}
      ` : nothing}
      ${this.example === 'chat-composer' ? html`
        ${this.select('Composer state', this.composerState, ['ready', 'disabled', 'pending', 'running', 'editing', 'continue'], (value) => { this.composerState = value; this.composerDraft = value === 'continue' ? '' : 'Summarize revenue by region.'; this.acceptedRunId = ''; this.composerRevision += 1 })}
        ${this.check('Attached context', this.references.length > 0, (value) => { this.references = value ? [composerReferences[0]!] : [] })}
        <button class="settings-button" @click=${() => { this.acceptedRunId = `local-accepted-${++this.acceptedRunSequence}`; this.composerDraft = ''; this.composerState = 'ready' }}>Accept draft</button>
      ` : nothing}
      <button class="settings-button" @click=${this.reset}>Reset</button>
    </div>`
  }

  private renderExample() {
    switch (this.example) {
      case 'code-editor': return html`<lv-code-editor .value=${this.empty ? '' : this.source} .language=${this.language} ?disabled=${this.readOnly} aria-label="Playground code editor" @lv-code-editor-change=${(event: CustomEvent<{ value: string }>) => { this.source = event.detail.value; this.empty = !this.source; this.record(event) }}></lv-code-editor>`
      case 'code-block': return html`<lv-code-block .code=${this.empty ? '' : this.source} .language=${this.language} ?toolbar=${this.toolbar} ?copy=${this.copy} ?compact=${this.compact} ?dense=${this.dense} ?inline=${this.inline} ?format=${this.format} .highlightedLines=${this.highlight ? [2, 3] : []}></lv-code-block>`
      case 'config-viewer': return html`<lv-config-viewer .configuration=${this.configState === 'empty' ? '' : this.configState === 'invalid' ? 'model: [unterminated' : this.language === 'json' ? configurationJSON : configurationYAML} .language=${this.language} .defaultView=${this.configView}></lv-config-viewer>`
      case 'markdown-view': return html`<lv-markdown-view .value=${this.empty ? '' : markdownFixture} ?compact=${this.compact} emptyText="No notes yet. Add a summary to get started."></lv-markdown-view>`
      case 'visual-artifact': return html`<div class="artifact"><lv-visual-artifact .type=${this.artifactState === 'unsupported' ? '' : 'bar'} artifact-id="regional-revenue" .payload=${this.artifactState === 'unavailable' ? undefined : this.artifact} .explorerHref=${this.explorerLink ? '#charts/bar' : ''} @lv-visual-action=${this.record} @lv-visualization-observation=${this.record}></lv-visual-artifact></div><lv-visual-modal></lv-visual-modal>`
      case 'chat-composer': return html`<div class="composer-preview">${keyed(this.composerRevision, html`<lv-chat-composer
        .value=${this.composerDraft}
        ?disabled=${this.composerState === 'disabled'} ?pending=${this.composerState === 'pending'}
        ?running=${this.composerState === 'running'} .runId=${this.composerState === 'running' ? 'local-run-1' : ''}
        .canContinue=${this.composerState === 'continue'} ?editing=${this.composerState === 'editing'}
        .editMessageId=${this.composerState === 'editing' ? 'local-message-1' : ''}
        .acceptedRunId=${this.acceptedRunId} .referenceLimit=${3}
        .references=${this.references} .pinnedSuggestions=${[composerReferences[0]!]}
        .suggestions=${this.suggestions} .suggestionQuery=${this.suggestionQuery} .suggestionRequestId=${this.suggestionRequestId}
        @lv-chat-submit=${this.record}
        @lv-chat-stop=${(event: CustomEvent) => { this.composerState = 'ready'; this.record(event) }}
        @lv-chat-edit-cancel=${(event: CustomEvent) => { this.composerDraft = ''; this.composerState = 'ready'; this.record(event) }}
        @lv-chat-references-change=${(event: CustomEvent<ChatReferencesChangeDetail>) => { this.references = event.detail.references; this.record(event) }}
        @lv-chat-reference-search=${(event: CustomEvent<ChatReferenceSearchDetail>) => { this.suggestionQuery = event.detail.query; this.suggestionRequestId = event.detail.requestId; this.suggestions = composerReferences.filter((reference) => matchesReferenceQuery(reference, event.detail.query)); this.record(event) }}
      ></lv-chat-composer>`)}</div>`
      default: return html`<p>Choose a content component from the navigation.</p>`
    }
  }

  render() {
    const doc = documentation[this.example]
    return html`<div class="stack">
      ${this.renderControls()}
      <section class="preview" part="preview" aria-label="Interactive content preview">${keyed(this.example, this.renderExample())}</section>
      ${exampleDetails(html`
      ${doc ? html`<p class="note">${doc.note}</p><dl class="documentation">
        <div><dt>Component</dt><dd><code>&lt;${doc.tag}&gt;</code></dd></div>
        <div><dt>Production source</dt><dd><code>${doc.source}</code></dd></div>
        <div><dt>Properties & variants</dt><dd>${doc.properties}</dd></div>
        <div><dt>Public events</dt><dd>${doc.events}</dd></div>
      </dl><details class="usage"><summary>Usage</summary><pre>${`import '../${doc.source.replace(/\.ts$/, '')}'\n\nhtml\`${doc.usage}\``}</pre></details>` : nothing}
      <section class="log" aria-label="Public event log"><div class="log-heading"><h3>Public event log</h3><button class="settings-button" ?disabled=${!this.logs.length} @click=${() => { this.logs = [] }}>Clear log</button></div>
        ${this.logs.length ? html`<pre aria-live="polite">${this.logs.join('\n\n')}</pre>` : nothing}
      </section>
      `)}
    </div>`
  }
}

if (!customElements.get('playground-content')) customElements.define('playground-content', PlaygroundContent)
