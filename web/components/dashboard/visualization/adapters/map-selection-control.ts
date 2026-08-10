import type { VisualizationEnvelope } from '../../../../generated/visualization'
import type { OptimisticInteractionCommand } from '../../interaction-selection'
import { clearInteractionCommand, interactionOptions, type InteractionOption } from '../interaction-command'

let nextControlID = 0

export class MapSelectionControl {
  readonly element: HTMLElement
  readonly #button: HTMLButtonElement
  readonly #panel: HTMLElement
  readonly #search: HTMLInputElement
  readonly #listbox: HTMLElement
  readonly #clear: HTMLButtonElement
  readonly #dispatch: (command: OptimisticInteractionCommand) => void
  #envelope?: VisualizationEnvelope
  #options: InteractionOption[] = []
  #activeIndex = 0

  constructor(dispatch: (command: OptimisticInteractionCommand) => void) {
    this.#dispatch = dispatch
    this.element = document.createElement('div')
    this.element.dataset.mapSelectionControl = ''
    this.element.style.cssText = 'position:absolute;left:8px;top:8px;z-index:3;font:var(--lv-type-caption)'

    const style = document.createElement('style')
    style.textContent = '.lv-map-selection-option:focus-visible{outline:2px solid var(--lv-line-accent,#0969da);outline-offset:-2px;background:var(--lv-accent-subtle,#ddf4ff)}'

    this.#button = document.createElement('button')
    this.#button.type = 'button'
    this.#button.textContent = 'Select map data'
    this.#button.setAttribute('aria-haspopup', 'listbox')
    this.#button.setAttribute('aria-expanded', 'false')
    styleButton(this.#button)

    this.#panel = document.createElement('div')
    this.#panel.id = `lv-map-selection-${++nextControlID}`
    this.#button.setAttribute('aria-controls', this.#panel.id)
    this.#panel.hidden = true
    this.#panel.style.cssText = 'margin-top:4px;width:min(280px,calc(100vw - 32px));max-height:min(320px,70vh);padding:8px;border:1px solid var(--lv-line-default,#d0d7de);border-radius:8px;background:var(--lv-bg-panel,#fff);box-shadow:0 8px 24px rgba(31,35,40,.16);color:var(--lv-fg-default,#1f2328)'

    this.#search = document.createElement('input')
    this.#search.type = 'search'
    this.#search.placeholder = 'Search map data'
    this.#search.setAttribute('aria-label', 'Search map data')
    this.#search.style.cssText = 'box-sizing:border-box;width:100%;height:30px;padding:4px 8px;border:1px solid var(--lv-line-default,#d0d7de);border-radius:6px;background:var(--lv-bg-panel,#fff);color:inherit;font:inherit'

    this.#listbox = document.createElement('div')
    this.#listbox.setAttribute('role', 'listbox')
    this.#listbox.setAttribute('aria-label', 'Map data')
    this.#listbox.style.cssText = 'max-height:220px;margin:6px -4px;overflow:auto'

    this.#clear = document.createElement('button')
    this.#clear.type = 'button'
    this.#clear.textContent = 'Clear map selection'
    styleButton(this.#clear)
    this.#clear.style.width = '100%'

    this.#panel.append(this.#search, this.#listbox, this.#clear)
    this.element.append(style, this.#button, this.#panel)
    this.#button.addEventListener('click', this.#toggle)
    this.#search.addEventListener('input', this.#handleSearch)
    this.#search.addEventListener('keydown', this.#handleSearchKeydown)
    this.#listbox.addEventListener('keydown', this.#handleListboxKeydown)
    this.#listbox.addEventListener('click', this.#handleOptionClick)
    this.#clear.addEventListener('click', this.#handleClear)
  }

  update(envelope: VisualizationEnvelope): void {
    const root = this.element.getRootNode()
    const active = root instanceof ShadowRoot ? root.activeElement : document.activeElement
    const restoreOptionFocus = active instanceof Element && this.#listbox.contains(active)
    this.#envelope = envelope
    this.#options = interactionOptions(envelope)
    this.#activeIndex = Math.min(this.#activeIndex, Math.max(0, this.#filtered().length - 1))
    const interaction = envelope.spec.interactions.find((candidate) => candidate.kind === 'select')
    if (interaction?.mode === 'multiple') this.#listbox.setAttribute('aria-multiselectable', 'true')
    else this.#listbox.removeAttribute('aria-multiselectable')
    const selectedCount = this.#options.filter((option) => option.selected).length
    this.#button.textContent = selectedCount > 0 ? `Select map data (${selectedCount})` : 'Select map data'
    this.#clear.disabled = envelope.selection.length === 0
    this.#renderOptions()
    if (restoreOptionFocus) queueMicrotask(() => this.#focusOption(this.#activeIndex))
  }

  dispose(): void {
    this.#button.removeEventListener('click', this.#toggle)
    this.#search.removeEventListener('input', this.#handleSearch)
    this.#search.removeEventListener('keydown', this.#handleSearchKeydown)
    this.#listbox.removeEventListener('keydown', this.#handleListboxKeydown)
    this.#listbox.removeEventListener('click', this.#handleOptionClick)
    this.#clear.removeEventListener('click', this.#handleClear)
    this.element.remove()
  }

  #filtered(): InteractionOption[] {
    const query = this.#search.value.trim().toLocaleLowerCase()
    return query ? this.#options.filter((option) => option.label.toLocaleLowerCase().includes(query)) : this.#options
  }

  #renderOptions(): void {
    const options = this.#filtered()
    this.#listbox.replaceChildren()
    if (options.length === 0) {
      const empty = document.createElement('div')
      empty.textContent = 'No matching map data'
      empty.style.cssText = 'padding:8px;color:var(--lv-fg-muted,#57606a)'
      this.#listbox.append(empty)
      return
    }
    options.forEach((option, index) => {
      const item = document.createElement('div')
      item.dataset.optionIndex = String(index)
      item.className = 'lv-map-selection-option'
      item.setAttribute('role', 'option')
      item.setAttribute('aria-selected', String(option.selected))
      item.tabIndex = index === this.#activeIndex ? 0 : -1
      item.textContent = option.label
      item.style.cssText = `cursor:pointer;padding:6px 8px;border-radius:5px;outline:none;${option.selected ? 'background:var(--lv-accent-subtle,#ddf4ff);font-weight:600' : ''}`
      this.#listbox.append(item)
    })
  }

  #open(): void {
    this.#panel.hidden = false
    this.#button.setAttribute('aria-expanded', 'true')
    this.#search.focus()
  }

  #close(): void {
    this.#panel.hidden = true
    this.#button.setAttribute('aria-expanded', 'false')
    this.#button.focus()
  }

  readonly #toggle = () => { this.#panel.hidden ? this.#open() : this.#close() }
  readonly #handleSearch = () => { this.#activeIndex = 0; this.#renderOptions() }
  readonly #handleSearchKeydown = (event: KeyboardEvent) => {
    if (event.key === 'Escape') { event.preventDefault(); this.#close(); return }
    if (event.key === 'ArrowDown') { event.preventDefault(); this.#focusOption(0) }
  }
  readonly #handleListboxKeydown = (event: KeyboardEvent) => {
    const options = this.#filtered()
    if (options.length === 0) return
    if (event.key === 'Escape') { event.preventDefault(); this.#close(); return }
    if (event.key === 'ArrowDown') this.#activeIndex = Math.min(options.length - 1, this.#activeIndex + 1)
    else if (event.key === 'ArrowUp') this.#activeIndex = Math.max(0, this.#activeIndex - 1)
    else if (event.key === 'Home') this.#activeIndex = 0
    else if (event.key === 'End') this.#activeIndex = options.length - 1
    else if (event.key === 'Enter' || event.key === ' ') {
      event.preventDefault()
      const option = options[this.#activeIndex]
      if (option) this.#dispatch(option.command)
      return
    } else return
    event.preventDefault()
    this.#renderOptions()
    this.#focusOption(this.#activeIndex)
  }
  readonly #handleOptionClick = (event: MouseEvent) => {
    const item = (event.target as HTMLElement).closest<HTMLElement>('[data-option-index]')
    const option = item ? this.#filtered()[Number(item.dataset.optionIndex)] : undefined
    if (option) this.#dispatch(option.command)
  }
  readonly #handleClear = () => {
    if (!this.#envelope || this.#envelope.selection.length === 0) return
    const command = clearInteractionCommand(this.#envelope)
    if (command) this.#dispatch(command)
  }
  #focusOption(index: number): void {
    this.#activeIndex = index
    this.#renderOptions()
    this.#listbox.querySelector<HTMLElement>(`[data-option-index="${index}"]`)?.focus()
  }
}

function styleButton(button: HTMLButtonElement): void {
  button.style.cssText = 'min-height:30px;padding:4px 9px;border:1px solid var(--lv-line-default,#d0d7de);border-radius:6px;background:var(--lv-bg-panel,#fff);color:var(--lv-fg-default,#1f2328);font:inherit;font-weight:600;cursor:pointer;box-shadow:0 1px 2px rgba(31,35,40,.08)'
}
