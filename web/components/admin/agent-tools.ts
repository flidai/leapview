import { LitElement, css, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import type { AdminAgentToolSignal } from '../../generated/signals'
import type { EntityListColumn, EntityListItem } from '../shared/entity-list'
import '../shared/drawer'
import '../shared/entity-list'

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

const toolColumns: EntityListColumn[] = [
  { id: 'name', label: 'Tool', width: '24%' },
  { id: 'description', label: 'Description', width: '46%' },
  { id: 'impact', label: 'Impact', width: '14%' },
  { id: 'inputs', label: 'Inputs', width: '16%' },
]

const toolGroupOrder = ['Catalog', 'Data & queries', 'Dashboards', 'Documentation', 'Other']

class AgentTools extends LitElement {
  @property({ attribute: false }) tools: AdminAgentToolSignal[] = []
  @state() private selectedName = ''
  @state() private tab: SchemaTab = 'fields'

  static styles = css`
    :host {
      display: block;
      min-width: 0;
      color: var(--lv-fg-default);
      font-family: var(--fontStack-system);
    }

    code {
      font: var(--lv-type-code-block);
    }

    .drawer-title {
      display: grid;
      min-width: 0;
      gap: var(--base-size-4);
    }

    .drawer-title h2,
    .drawer-title p {
      margin: 0;
    }

    .drawer-title h2 {
      overflow-wrap: anywhere;
      font: var(--lv-type-section-title);
    }

    .drawer-title p {
      color: var(--lv-fg-muted);
      font: var(--lv-type-body-compact);
      line-height: var(--base-text-lineHeight-snug);
    }

    .drawer-body {
      display: grid;
      min-width: 0;
      gap: var(--base-size-20);
    }

    .facts {
      display: grid;
      grid-template-columns: repeat(2, minmax(0, 1fr));
      gap: var(--base-size-12) var(--base-size-16);
      margin: 0;
    }

    .fact {
      display: grid;
      min-width: 0;
      gap: var(--base-size-2);
    }

    .fact dt {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .fact dd {
      min-width: 0;
      margin: 0;
      overflow-wrap: anywhere;
      font: var(--lv-type-body-compact);
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

    .schema {
      min-width: 0;
    }

    .fields {
      overflow-x: auto;
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
    }

    .fields table {
      width: 100%;
      min-width: 34rem;
      border-spacing: 0;
      border-collapse: collapse;
      font: var(--lv-type-body-compact);
    }

    .fields th {
      background: var(--lv-bg-panel-muted);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      text-align: left;
    }

    .fields th,
    .fields td {
      border-bottom: var(--lv-border-muted);
      padding: var(--base-size-8) var(--lv-space-control);
      vertical-align: top;
    }

    .fields tbody tr:last-child td {
      border-bottom: 0;
    }

    .description,
    .empty {
      color: var(--lv-fg-muted);
    }

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

    .empty {
      margin: 0;
      font: var(--lv-type-body);
    }

    .json {
      margin: 0;
      max-width: 100%;
      overflow: auto;
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-control);
      padding: var(--base-size-12);
      color: var(--lv-fg-default);
      font: var(--lv-type-code-block);
      white-space: pre-wrap;
      overflow-wrap: anywhere;
    }

    @media (max-width: 44rem) {
      .facts {
        grid-template-columns: minmax(0, 1fr);
      }
    }
  `

  render() {
    const tools = this.tools.map(toolView).sort(compareTools)
    const selected = tools.find((tool) => tool.name === this.selectedName)
    return html`
      <section aria-label="Agent tool catalog">
        <lv-entity-list
          .items=${tools.map(toolListItem)}
          .columns=${toolColumns}
          client-filter
          row-action="open"
          group-by="group"
          list-label="Agent tools"
          search-placeholder="Search tools"
          empty-text="No tools are available."
          @lv-entity-list-row-action=${this.openTool}
        ></lv-entity-list>
        ${selected ? this.renderToolDrawer(selected) : nothing}
      </section>
    `
  }

  private renderToolDrawer(tool: ToolView) {
    return html`
      <lv-drawer open size="wide" label="Tool details" .modal=${false} @lv-drawer-close=${this.closeTool}>
        <div slot="title" class="drawer-title">
          <h2><code>${tool.name}</code></h2>
          <p>${tool.description || 'No description provided.'}</p>
        </div>
        <div class="drawer-body">
          <dl class="facts">
            ${toolFact('Impact', effectLabel(tool.effect))}
            ${toolFact('Category', tool.group)}
            ${toolFact('Required inputs', String(tool.requiredCount))}
            ${toolFact('Input', tool.summary)}
            ${toolFact('Defaults', tool.defaultsSummary || 'None')}
          </dl>
          <div class="tabs" role="tablist" aria-label="Tool schema view">
            ${this.renderTab('fields', 'Fields')}
            ${this.renderTab('json', 'Input JSON')}
            ${this.renderTab('output', 'Output')}
          </div>
          <div class="schema">
          ${this.tab === 'json' ? this.renderJSON(tool.inputSchema) : this.tab === 'output' ? this.renderJSON(tool.outputSchema) : this.renderFields(tool)}
          </div>
        </div>
      </lv-drawer>
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

  private readonly openTool = (event: CustomEvent<{ action?: string, item?: EntityListItem }>): void => {
    if (event.detail?.action !== 'open' || !event.detail.item?.id) return
    this.selectedName = event.detail.item.id
    this.tab = 'fields'
  }

  private readonly closeTool = (): void => {
    this.selectedName = ''
    this.tab = 'fields'
  }
}

type ToolView = {
  name: string
  description: string
  effect: string
  tags: string[]
  group: string
  defaultsSummary: string
  inputSchema: SchemaObject
  outputSchema: SchemaObject
  parsed: ParsedSchema
  summary: string
  inputCountLabel: string
  requiredCount: number
  searchText: string
}

function toolListItem(tool: ToolView): EntityListItem {
  return {
    id: tool.name,
    title: tool.name,
    icon: 'none',
    group: tool.group,
    columns: {
      description: tool.description || 'No description provided.',
      impact: effectLabel(tool.effect),
      inputs: tool.inputCountLabel,
      _search: tool.searchText,
    },
    sortValues: {
      impact: tool.effect,
      inputs: tool.parsed.kind === 'fields' ? tool.parsed.fields.length : 0,
    },
  }
}

function toolFact(label: string, value: string) {
  return html`<div class="fact"><dt>${label}</dt><dd>${value}</dd></div>`
}

function effectLabel(effect: string): string {
  switch (effect.trim().toLowerCase()) {
    case 'read': return 'Read-only'
    case 'write': return 'Changes draft'
    case 'destructive': return 'Destructive'
    default: return sentenceCase(effect)
  }
}

function sentenceCase(value: string): string {
  const normalized = value.trim().replaceAll('_', ' ')
  return normalized ? normalized[0].toUpperCase() + normalized.slice(1) : 'Unknown'
}

function toolGroup(tags: string[]): string {
  const values = new Set(tags.map((tag) => tag.trim().toLowerCase()))
  const primary = tags[0]?.trim().toLowerCase()
  if (primary === 'dashboard') return 'Dashboards'
  if (primary === 'documentation') return 'Documentation'
  if (primary === 'catalog') return 'Catalog'
  if (primary === 'semantic-model' || primary === 'analytics') return 'Data & queries'
  if (values.has('semantic-model') || values.has('analytics') || values.has('visualization')) return 'Data & queries'
  if (values.has('dashboard')) return 'Dashboards'
  if (values.has('catalog')) return 'Catalog'
  if (values.has('documentation')) return 'Documentation'
  return 'Other'
}

function compareTools(left: ToolView, right: ToolView): number {
  const group = toolGroupOrder.indexOf(left.group) - toolGroupOrder.indexOf(right.group)
  return group || left.name.localeCompare(right.name)
}

function toolView(tool: AdminAgentToolSignal): ToolView {
  const parsed = parseSchema(tool.inputSchema ?? {})
  const fieldPaths = parsed.kind === 'fields' ? parsed.fields.map((field) => field.path) : []
  const tags = tool.tags ?? []
  const requiredCount = parsed.kind === 'fields' ? parsed.fields.filter((field) => field.required).length : 0
  return {
    name: tool.name,
    description: tool.description,
    effect: tool.effect || 'read',
    tags,
    group: toolGroup(tags),
    defaultsSummary: Object.entries(tool.defaults ?? {}).map(([name, value]) => `${name}=${String(value)}`).join(', '),
    inputSchema: tool.inputSchema ?? {},
    outputSchema: tool.outputSchema ?? {},
    parsed,
    summary: inputSummary(parsed),
    inputCountLabel: inputCount(parsed, requiredCount),
    requiredCount,
    searchText: [tool.name, tool.description, tool.effect, ...tags, ...fieldPaths].join(' ').toLowerCase(),
  }
}

function inputCount(parsed: ParsedSchema, requiredCount: number): string {
  if (parsed.kind === 'empty') return 'No input'
  if (parsed.kind === 'unsupported') return 'JSON schema'
  const count = parsed.fields.length
  const fields = `${count} ${count === 1 ? 'field' : 'fields'}`
  return requiredCount ? `${fields} · ${requiredCount} required` : fields
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
