import { LitElement, css, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import { Braces, Search } from 'lucide'
import type { AdminAgentToolSignal } from '../../generated/signals'
import { lucideIcon } from '../shared/lucide-icons'

type SchemaObject = Record<string, unknown>
type SchemaTab = 'fields' | 'json' | 'output'

type SchemaField = {
  path: string
  type: string
  required: boolean
  description: string
}

type ParsedSchema =
  | { kind: 'fields'; fields: SchemaField[] }
  | { kind: 'empty' }
  | { kind: 'unsupported' }

class AgentTools extends LitElement {
  @property({ attribute: false }) tools: AdminAgentToolSignal[] = []
  @state() private query = ''
  @state() private selectedName = ''
  @state() private tab: SchemaTab = 'fields'

  static styles = css`
    :host {
      display: block;
      min-width: 0;
      color: var(--lv-fg-default);
      font-family: var(--fontStack-system);
    }

    .catalog {
      display: grid;
      height: min(42rem, calc(100svh - 12rem));
      min-height: 28rem;
      min-width: 0;
      grid-template-rows: auto minmax(0, 1fr);
      overflow: hidden;
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
    }

    .toolbar {
      display: flex;
      align-items: center;
      gap: var(--base-size-8);
      border-bottom: var(--lv-border-muted);
      padding: var(--base-size-8);
    }

    .search {
      display: flex;
      min-width: min(100%, 22rem);
      align-items: center;
      gap: var(--base-size-8);
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      padding: 0 var(--base-size-8);
      color: var(--lv-fg-muted);
    }

    .search svg {
      width: var(--base-size-16);
      height: var(--base-size-16);
      flex: 0 0 var(--base-size-16);
    }

    input {
      width: 100%;
      min-width: 0;
      border: 0;
      background: transparent;
      padding: var(--base-size-8) 0;
      color: var(--lv-fg-default);
      font: var(--lv-type-body-compact);
      outline: 0;
    }

    input::placeholder {
      color: var(--lv-fg-muted);
    }

    .count {
      margin-left: auto;
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .body {
      display: grid;
      min-width: 0;
      min-height: 0;
      grid-template-columns: minmax(12rem, 16rem) minmax(0, 1fr);
    }

    .list {
      min-width: 0;
      min-height: 0;
      overflow: auto;
      border-right: var(--lv-border-muted);
    }

    .fields table {
      width: 100%;
      border-spacing: 0;
      border-collapse: collapse;
      font: var(--lv-type-body);
    }

    .fields th {
      background: var(--lv-bg-panel-muted);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      text-align: left;
      text-transform: uppercase;
    }

    .fields th,
    .fields td {
      border-bottom: var(--lv-border-muted);
      padding: var(--base-size-8) var(--base-size-12);
      vertical-align: top;
    }

    .fields tbody tr:last-child td {
      border-bottom: 0;
    }

    .tool-list {
      display: grid;
      padding: var(--base-size-4);
    }

    .tool-button {
      display: block;
      width: 100%;
      min-width: 0;
      border: 0;
      border-radius: var(--lv-radius-default);
      background: transparent;
      padding: var(--base-size-8);
      color: var(--lv-fg-default);
      cursor: pointer;
      font: inherit;
      text-align: left;
    }

    .tool-button:hover,
    .tool-button:focus-visible,
    .tool-button.is-selected {
      background: var(--lv-bg-panel-muted);
    }

    .tool-button:focus-visible {
      outline: 2px solid var(--lv-fg-accent);
      outline-offset: -2px;
    }

    code {
      font: var(--lv-type-code-block);
    }

    .tool-button code,
    .name code {
      color: var(--lv-fg-default);
      font-weight: var(--base-text-weight-semibold);
    }

    .description,
    .summary,
    .empty {
      color: var(--lv-fg-muted);
    }

    .description,
    .summary {
      line-height: var(--base-text-lineHeight-snug);
    }

    .required-count,
    .required-flag {
      display: inline-flex;
      align-items: center;
      border-radius: var(--lv-radius-full);
      background: var(--lv-bg-panel-muted);
      padding: var(--base-size-2) var(--base-size-8);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      white-space: nowrap;
    }

    .required-flag.is-required {
      background: var(--lv-bg-accent-muted);
      color: var(--lv-fg-accent);
    }

    .detail {
      display: grid;
      min-width: 0;
      min-height: 0;
      grid-template-rows: auto minmax(0, 1fr);
      align-content: start;
    }

    .detail-header {
      display: grid;
      gap: var(--base-size-8);
      border-bottom: var(--lv-border-muted);
      padding: var(--base-size-12);
    }

    .detail-title {
      display: flex;
      min-width: 0;
      align-items: center;
      gap: var(--base-size-8);
    }

    .detail-title svg {
      width: var(--base-size-16);
      height: var(--base-size-16);
      color: var(--lv-fg-muted);
    }

    .detail-title code {
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
      font-weight: var(--base-text-weight-semibold);
    }

    .detail-description {
      margin: 0;
      color: var(--lv-fg-muted);
      font: var(--lv-type-body-snug);
    }

    .detail-meta {
      display: flex;
      flex-wrap: wrap;
      gap: var(--base-size-8);
    }

    .tabs {
      display: inline-flex;
      width: fit-content;
      overflow: hidden;
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel-muted);
      padding: 2px;
    }

    .tabs button {
      border: 0;
      border-radius: calc(var(--lv-radius-default) - 2px);
      background: transparent;
      padding: var(--base-size-6) var(--lv-space-control);
      color: var(--lv-fg-muted);
      cursor: pointer;
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-medium);
    }

    .tabs button.is-active {
      background: var(--lv-bg-panel);
      color: var(--lv-fg-default);
      box-shadow: var(--shadow-inset);
    }

    .tabs button:focus-visible {
      outline: 2px solid var(--lv-fg-accent);
      outline-offset: 2px;
    }

    .detail-body {
      min-width: 0;
      min-height: 0;
      overflow: auto;
    }

    .empty {
      margin: 0;
      padding: var(--base-size-16);
      font: var(--lv-type-body);
    }

    .json {
      margin: 0;
      overflow: auto;
      background: var(--lv-bg-control);
      padding: var(--base-size-16);
      color: var(--lv-fg-default);
      font: var(--lv-type-code-block);
      white-space: pre;
    }

    @media (max-width: 840px) {
      .body {
        grid-template-rows: auto minmax(0, 1fr);
        grid-template-columns: 1fr;
      }

      .list {
        max-height: 12rem;
        border-right: 0;
        border-bottom: var(--lv-border-muted);
      }
    }
  `

  render() {
    const tools = this.filteredTools
    const selected = this.selectedTool(tools)
    return html`
      <div class="catalog">
        <div class="toolbar">
          <label class="search">
            ${lucideIcon(Search, { size: 16, strokeWidth: 2 })}
            <input
              type="search"
              aria-label="Search tools"
              placeholder="Search tools"
              .value=${this.query}
              @input=${this.updateQuery}
            >
          </label>
          <span class="count">${tools.length} ${tools.length === 1 ? 'tool' : 'tools'}</span>
        </div>
        ${tools.length === 0 ? html`<p class="empty">No tools match the current search.</p>` : html`
          <div class="body">
            ${this.renderToolList(tools, selected)}
            ${selected ? this.renderToolDetail(selected) : html`<p class="empty">Select a tool to inspect its input payload.</p>`}
          </div>
        `}
      </div>
    `
  }

  private renderToolList(tools: ToolView[], selected: ToolView | null) {
    return html`
      <div class="list">
        <div class="tool-list" role="listbox" aria-label="Agent tools">
          ${tools.map((tool) => html`
            <button
              class=${tool.name === selected?.name ? 'tool-button is-selected' : 'tool-button'}
              type="button"
              role="option"
              aria-selected=${String(tool.name === selected?.name)}
              @click=${() => this.selectTool(tool.name)}
            >
              <code>${tool.name}</code>
            </button>
          `)}
        </div>
      </div>
    `
  }

  private renderToolDetail(tool: ToolView) {
    return html`
      <div class="detail">
        <div class="detail-header">
          <div class="detail-title">
            ${lucideIcon(Braces, { size: 16, strokeWidth: 2 })}
            <code>${tool.name}</code>
          </div>
          ${tool.description ? html`<p class="detail-description">${tool.description}</p>` : nothing}
          <div class="detail-meta">
            <span class="required-count">${tool.effect}</span>
            <span class="required-count">${tool.requiredCount} required</span>
            <span class="required-count">${tool.summary}</span>
            ${tool.defaultsSummary ? html`<span class="required-count">Defaults: ${tool.defaultsSummary}</span>` : nothing}
          </div>
          <div class="tabs" role="tablist" aria-label="Tool schema view">
            ${this.renderTab('fields', 'Fields')}
            ${this.renderTab('json', 'JSON')}
            ${this.renderTab('output', 'Output')}
          </div>
        </div>
        <div class="detail-body">
          ${this.tab === 'json' ? this.renderJSON(tool.inputSchema) : this.tab === 'output' ? this.renderJSON(tool.outputSchema) : this.renderFields(tool)}
        </div>
      </div>
    `
  }

  private renderTab(tab: SchemaTab, label: string) {
    return html`
      <button
        class=${this.tab === tab ? 'is-active' : ''}
        type="button"
        role="tab"
        aria-selected=${String(this.tab === tab)}
        @click=${() => { this.tab = tab }}
      >${label}</button>
    `
  }

  private renderFields(tool: ToolView) {
    if (tool.parsed.kind === 'empty') return html`<p class="empty">No input</p>`
    if (tool.parsed.kind === 'unsupported') return html`<p class="empty">Schema is only available as JSON.</p>`
    return html`
      <div class="fields">
        <table>
          <thead>
            <tr>
              <th>Field</th>
              <th>Type</th>
              <th>Required</th>
              <th>Description</th>
            </tr>
          </thead>
          <tbody>
            ${tool.parsed.fields.map((field) => html`
              <tr>
                <td><code>${field.path}</code></td>
                <td><code>${field.type}</code></td>
                <td><span class=${field.required ? 'required-flag is-required' : 'required-flag'}>${field.required ? 'Yes' : 'No'}</span></td>
                <td class="description">${field.description || '-'}</td>
              </tr>
            `)}
          </tbody>
        </table>
      </div>
    `
  }

  private renderJSON(schema: SchemaObject) {
    return html`<pre class="json"><code>${JSON.stringify(schema, null, 2)}</code></pre>`
  }

  private updateQuery(event: Event): void {
    this.query = (event.target as HTMLInputElement).value
  }

  private selectTool(name: string): void {
    this.selectedName = name
    this.tab = 'fields'
  }

  private selectedTool(tools: ToolView[]): ToolView | null {
    if (tools.length === 0) return null
    return tools.find((tool) => tool.name === this.selectedName) ?? tools[0]
  }

  private get filteredTools(): ToolView[] {
    const views = this.tools.map(toolView)
    const query = this.query.trim().toLowerCase()
    if (!query) return views
    return views.filter((tool) => tool.searchText.includes(query))
  }
}

type ToolView = {
  name: string
  description: string
  effect: string
  defaultsSummary: string
  inputSchema: SchemaObject
  outputSchema: SchemaObject
  parsed: ParsedSchema
  summary: string
  requiredCount: number
  searchText: string
}

function toolView(tool: AdminAgentToolSignal): ToolView {
  const parsed = parseSchema(tool.inputSchema ?? {})
  const fieldPaths = parsed.kind === 'fields' ? parsed.fields.map((field) => field.path) : []
  return {
    name: tool.name,
    description: tool.description,
    effect: tool.effect || 'read',
    defaultsSummary: Object.entries(tool.defaults ?? {}).map(([name, value]) => `${name}=${String(value)}`).join(', '),
    inputSchema: tool.inputSchema ?? {},
    outputSchema: tool.outputSchema ?? {},
    parsed,
    summary: inputSummary(parsed),
    requiredCount: parsed.kind === 'fields' ? parsed.fields.filter((field) => field.required).length : 0,
    searchText: [tool.name, tool.description, tool.effect, ...fieldPaths].join(' ').toLowerCase(),
  }
}

function inputSummary(parsed: ParsedSchema): string {
  if (parsed.kind === 'empty') return 'No input'
  if (parsed.kind === 'unsupported') return 'JSON schema'
  const fields = parsed.fields.map((field) => field.path)
  if (fields.length <= 3) return fields.join(', ')
  return `${fields.slice(0, 3).join(', ')} +${fields.length - 3}`
}

function parseSchema(schema: SchemaObject): ParsedSchema {
  const fields: SchemaField[] = []
  const result = flattenSchema(schema, '', false, fields, { root: schema, refs: new Set() })
  if (result === 'unsupported') return { kind: 'unsupported' }
  if (fields.length === 0) return { kind: 'empty' }
  return { kind: 'fields', fields }
}

type SchemaParseContext = {
  root: SchemaObject
  refs: Set<string>
}

function flattenSchema(schema: unknown, prefix: string, required: boolean, fields: SchemaField[], context: SchemaParseContext): 'ok' | 'unsupported' {
  const resolved = resolveSchema(schema, context)
  if (!resolved || hasUnsupportedComposition(resolved)) return 'unsupported'
  if (resolved.type === 'array') return flattenArraySchema(resolved, prefix, required, fields, context)
  if (isObjectSchema(resolved)) return flattenObjectSchema(resolved, prefix, required, fields, context)
  if (prefix) fields.push(schemaField(prefix, resolved, required, context))
  return 'ok'
}

function flattenObjectSchema(schema: SchemaObject, prefix: string, required: boolean, fields: SchemaField[], context: SchemaParseContext): 'ok' | 'unsupported' {
  const properties = schema.properties
  if (properties === undefined) {
    if (prefix) fields.push(schemaField(prefix, schema, required, context))
    return 'ok'
  }
  if (!isSchemaObject(properties)) return 'unsupported'
  const requiredFields = stringSet(schema.required)
  for (const [name, child] of Object.entries(properties)) {
    const path = prefix ? `${prefix}.${name}` : name
    const childRequired = requiredFields.has(name)
    if (flattenSchema(child, path, childRequired, fields, context) === 'unsupported') return 'unsupported'
  }
  return 'ok'
}

function flattenArraySchema(schema: SchemaObject, prefix: string, required: boolean, fields: SchemaField[], context: SchemaParseContext): 'ok' | 'unsupported' {
  if (!prefix) return 'unsupported'
  const items = resolveSchema(schema.items, context)
  if (!items || hasUnsupportedComposition(items)) {
    fields.push(schemaField(prefix, schema, required, context))
    return 'ok'
  }
  if (isObjectSchema(items) && isSchemaObject(items.properties)) {
    return flattenObjectSchema(items, `${prefix}[]`, required, fields, context)
  }
  fields.push(schemaField(prefix, schema, required, context))
  return 'ok'
}

function schemaField(path: string, schema: SchemaObject, required: boolean, context: SchemaParseContext): SchemaField {
  return {
    path,
    type: schemaType(schema, context),
    required,
    description: typeof schema.description === 'string' ? schema.description : '',
  }
}

function schemaType(schema: SchemaObject, context: SchemaParseContext): string {
  if (Array.isArray(schema.enum)) return `enum: ${schema.enum.map(String).join(' | ')}`
  const type = schema.type
  if (type === 'array') return `array<${arrayItemType(schema.items, context)}>`
  if (isObjectSchema(schema)) return objectType(schema, context)
  if (Array.isArray(type)) return type.map(String).join(' | ')
  if (typeof type === 'string') return type
  return 'any'
}

function arrayItemType(items: unknown, context: SchemaParseContext): string {
  const resolved = resolveSchema(items, context)
  if (!resolved || hasUnsupportedComposition(resolved)) return 'any'
  if (Array.isArray(resolved.enum)) return `enum: ${resolved.enum.map(String).join(' | ')}`
  if (isObjectSchema(resolved)) return 'object'
  return schemaType(resolved, context)
}

function objectType(schema: SchemaObject, context: SchemaParseContext): string {
  const additionalProperties = schema.additionalProperties
  if (additionalProperties === true) return 'object<string, any>'
  if (isSchemaObject(additionalProperties)) {
    const resolved = resolveSchema(additionalProperties, context)
    if (!resolved || hasUnsupportedComposition(resolved)) return 'object<string, any>'
    if (isObjectSchema(resolved)) return 'object<string, object>'
    return `object<string, ${schemaType(resolved, context)}>`
  }
  return 'object'
}

function resolveSchema(schema: unknown, context: SchemaParseContext): SchemaObject | null {
  if (!isSchemaObject(schema)) return null
  const ref = schema.$ref
  if (typeof ref !== 'string') return schema
  if (!ref.startsWith('#/')) return null
  if (context.refs.has(ref)) return null
  context.refs.add(ref)
  const resolved = schemaAtPointer(context.root, ref)
  const nested = resolveSchema(resolved, context)
  context.refs.delete(ref)
  if (!nested) return null
  return { ...nested, ...withoutRef(schema) }
}

function schemaAtPointer(root: SchemaObject, ref: string): unknown {
  return ref
    .slice(2)
    .split('/')
    .reduce<unknown>((current, segment) => {
      if (!isSchemaObject(current)) return undefined
      return current[segment.replace(/~1/g, '/').replace(/~0/g, '~')]
    }, root)
}

function withoutRef(schema: SchemaObject): SchemaObject {
  const { $ref: _ref, ...rest } = schema
  return rest
}

function isObjectSchema(schema: SchemaObject): boolean {
  if (schema.type === 'object' || schema.properties !== undefined) return true
  const additionalProperties = schema.additionalProperties
  return additionalProperties === true || isSchemaObject(additionalProperties)
}

function hasUnsupportedComposition(schema: SchemaObject): boolean {
  return Boolean(schema.oneOf || schema.anyOf || schema.allOf)
}

function stringSet(value: unknown): Set<string> {
  return new Set(Array.isArray(value) ? value.filter((item): item is string => typeof item === 'string') : [])
}

function isSchemaObject(value: unknown): value is SchemaObject {
  return Boolean(value && typeof value === 'object' && !Array.isArray(value))
}

if (!customElements.get('lv-agent-tools')) customElements.define('lv-agent-tools', AgentTools)
