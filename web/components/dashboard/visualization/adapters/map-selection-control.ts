import { ChevronDown, ChevronLeft, CircleDashed, LassoSelect, MapPin, MousePointer2, SquareDashed, Trash2, type IconNode } from 'lucide'
import type { VisualizationEnvelope, VisualizationSpatialSelectionGesture } from '../../../../generated/visualization'
import type { OptimisticInteractionCommand } from '../../interaction-selection'
import { clearInteractionCommand, interactionOptions, type InteractionOption } from '../interaction-command'
import type { MapSpatialSelectionControl } from './maplibre/spatial-selection-control'

let nextControlID = 0

export class MapSelectionControl {
  readonly element: HTMLElement
  readonly #button: HTMLButtonElement
  readonly #panel: HTMLElement
  readonly #tools: HTMLElement
  readonly #dataPane: HTMLElement
  readonly #dataHeading: HTMLElement
  readonly #back: HTMLButtonElement
  readonly #search: HTMLInputElement
  readonly #status: HTMLElement
  readonly #listbox: HTMLElement
  readonly #clear: HTMLButtonElement
  readonly #dispatch: (command: OptimisticInteractionCommand) => void
  readonly #refine: (center: readonly [number, number], zoom: number) => void
  readonly #onOpenChange: (open: boolean) => void
  #envelope?: VisualizationEnvelope
  #options: InteractionOption[] = []
  #activeIndex = 0
  #spatial?: MapSpatialSelectionControl

  constructor(
    dispatch: (command: OptimisticInteractionCommand) => void,
    onOpenChange: (open: boolean) => void = () => undefined,
    refine: (center: readonly [number, number], zoom: number) => void = () => undefined,
  ) {
    this.#dispatch = dispatch
    this.#onOpenChange = onOpenChange
    this.#refine = refine
    this.element = document.createElement('div')
    this.element.dataset.mapSelectionControl = ''
    this.element.style.cssText = 'position:absolute;left:8px;top:8px;z-index:6;font:var(--lv-type-caption)'

    const style = document.createElement('style')
    style.textContent = '.lv-map-selection-option:focus-visible{outline:2px solid var(--lv-line-accent,#0969da);outline-offset:-2px;background:var(--lv-bg-accent-muted,var(--lv-bg-control-hover,#ddf4ff));color:var(--lv-fg-default,#1f2328)}[data-map-selection-clear]:disabled{cursor:not-allowed;opacity:.55;box-shadow:none}'

    this.#button = document.createElement('button')
    this.#button.type = 'button'
    this.#button.dataset.mapSelectionTrigger = ''
    this.#button.setAttribute('aria-haspopup', 'menu')
    this.#button.setAttribute('aria-expanded', 'false')
    styleButton(this.#button)
    setButtonContent(this.#button, MousePointer2, 'Select', true)

    this.#panel = document.createElement('div')
    this.#panel.id = `lv-map-selection-${++nextControlID}`
    this.#button.setAttribute('aria-controls', this.#panel.id)
    this.#panel.hidden = true
    this.#panel.style.cssText = 'margin-top:4px;width:max-content;max-width:calc(100vw - 32px);max-height:min(320px,70vh);padding:6px;border:1px solid var(--lv-line-default,#d0d7de);border-radius:8px;background:var(--lv-bg-panel,#fff);box-shadow:0 8px 24px rgba(31,35,40,.16);color:var(--lv-fg-default,#1f2328)'

    this.#tools = document.createElement('div')
    this.#tools.setAttribute('role', 'menu')
    this.#tools.setAttribute('aria-label', 'Map selection tools')

    this.#dataPane = document.createElement('div')
    this.#dataPane.hidden = true

    const dataHeader = document.createElement('div')
    dataHeader.style.cssText = 'display:flex;align-items:center;gap:4px;margin-bottom:6px'
    this.#back = document.createElement('button')
    this.#back.type = 'button'
    this.#back.setAttribute('aria-label', 'Back to selection tools')
    this.#back.title = 'Back to selection tools'
    this.#back.style.cssText = 'display:grid;place-items:center;width:28px;height:28px;padding:0;border:0;border-radius:5px;background:transparent;color:inherit;cursor:pointer'
    this.#back.append(iconElement(ChevronLeft, 16))
    this.#dataHeading = document.createElement('strong')
    this.#dataHeading.style.cssText = 'font-weight:var(--base-text-weight-semibold)'
    dataHeader.append(this.#back, this.#dataHeading)

    this.#search = document.createElement('input')
    this.#search.type = 'search'
    this.#search.placeholder = 'Search map data'
    this.#search.setAttribute('aria-label', 'Search map data')
    this.#search.style.cssText = 'box-sizing:border-box;width:100%;height:30px;padding:4px 8px;border:1px solid var(--lv-line-default,#d0d7de);border-radius:6px;background:var(--lv-bg-panel,#fff);color:inherit;font:inherit'

    this.#status = document.createElement('div')
    this.#status.hidden = true
    this.#status.setAttribute('role', 'status')
    this.#status.style.cssText = 'margin-top:6px;padding:6px 8px;border:1px solid var(--lv-line-accent,#0969da);border-radius:6px;background:var(--lv-bg-accent-muted,var(--lv-bg-control-hover,#ddf4ff));color:var(--lv-fg-default,#1f2328);font-weight:var(--base-text-weight-medium);overflow:hidden;text-overflow:ellipsis;white-space:nowrap'

    this.#listbox = document.createElement('div')
    this.#listbox.setAttribute('role', 'listbox')
    this.#listbox.setAttribute('aria-label', 'Map data')
    this.#listbox.style.cssText = 'max-height:220px;margin:6px -4px;overflow:auto'

    this.#clear = document.createElement('button')
    this.#clear.type = 'button'
    this.#clear.dataset.mapSelectionClear = ''
    this.#clear.textContent = 'Clear selected points'
    styleButton(this.#clear)
    this.#clear.style.width = '100%'

    this.#dataPane.append(dataHeader, this.#search, this.#status, this.#listbox, this.#clear)
    this.#panel.append(this.#tools, this.#dataPane)
    this.element.append(style, this.#button, this.#panel)
    this.#button.addEventListener('click', this.#toggle)
    this.#button.addEventListener('keydown', this.#handleTriggerKeydown)
    this.#back.addEventListener('click', this.#handleBack)
    this.#search.addEventListener('input', this.#handleSearch)
    this.#search.addEventListener('keydown', this.#handleSearchKeydown)
    this.#listbox.addEventListener('keydown', this.#handleListboxKeydown)
    this.#tools.addEventListener('keydown', this.#handleToolsKeydown)
    this.#listbox.addEventListener('click', this.#handleOptionClick)
    this.#clear.addEventListener('click', this.#handleClear)
    document.addEventListener('pointerdown', this.#handleOutsidePointerDown, true)
  }

  get open(): boolean { return !this.#panel.hidden }

  close(): void { this.#close() }

  setSpatialSelectionControl(control: MapSpatialSelectionControl | undefined): void {
    this.#spatial = control
    this.#syncSelectionState()
    this.#renderTools()
  }

  update(envelope: VisualizationEnvelope, options: InteractionOption[] = interactionOptions(envelope)): void {
    const root = this.element.getRootNode()
    const active = typeof ShadowRoot !== 'undefined' && root instanceof ShadowRoot ? root.activeElement : document.activeElement
    const restoreOptionFocus = typeof Element !== 'undefined' && active instanceof Element && this.#listbox.contains(active)
    const activeToolIndex = typeof Element !== 'undefined' && active instanceof Element && this.#tools.contains(active)
      ? [...this.#tools.querySelectorAll('[role="menuitem"]')].indexOf(active)
      : -1
    this.#envelope = envelope
    this.#options = options
    this.#syncMode()
    this.#activeIndex = Math.min(this.#activeIndex, Math.max(0, this.#filtered().length - 1))
    const interaction = envelope.spec.interactions.find((candidate) => candidate.kind === 'select')
    if (interaction?.mode === 'multiple') this.#listbox.setAttribute('aria-multiselectable', 'true')
    else this.#listbox.removeAttribute('aria-multiselectable')
    this.#syncSelectionState()
    this.#renderOptions()
    if (restoreOptionFocus) queueMicrotask(() => this.#focusOption(this.#activeIndex))
    else if (activeToolIndex >= 0) {
      queueMicrotask(() => this.#tools.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')[activeToolIndex]?.focus())
    }
  }

  dispose(): void {
    this.#button.removeEventListener('click', this.#toggle)
    this.#button.removeEventListener('keydown', this.#handleTriggerKeydown)
    this.#back.removeEventListener('click', this.#handleBack)
    this.#search.removeEventListener('input', this.#handleSearch)
    this.#search.removeEventListener('keydown', this.#handleSearchKeydown)
    this.#listbox.removeEventListener('keydown', this.#handleListboxKeydown)
    this.#tools.removeEventListener('keydown', this.#handleToolsKeydown)
    this.#listbox.removeEventListener('click', this.#handleOptionClick)
    this.#clear.removeEventListener('click', this.#handleClear)
    document.removeEventListener('pointerdown', this.#handleOutsidePointerDown, true)
    this.element.remove()
  }

  #filtered(): InteractionOption[] {
    const query = this.#search.value.trim().toLocaleLowerCase()
    return query ? this.#options.filter((option) => option.label.toLocaleLowerCase().includes(query)) : this.#options
  }

  #isRefinementMode(): boolean {
    return this.#options.length > 0 && this.#options.every((option) => option.refinement && !option.command)
  }

  #syncMode(): void {
    const refinement = this.#isRefinementMode()
    this.#search.hidden = refinement
    if (refinement) this.#search.value = ''
    this.#dataHeading.textContent = refinement ? 'Zoom to points' : 'Visible points'
    this.#listbox.setAttribute('aria-label', refinement ? 'Map areas to zoom into' : 'Visible map points')
  }

  #renderTools(): void {
    const items: HTMLElement[] = []
    items.push(this.#toolButton(
      this.#isRefinementMode() ? 'Zoom to points' : 'Select visible points',
      MapPin,
      () => this.#showDataPane(),
    ))
    for (const gesture of this.#spatial?.availableGestures ?? []) {
      items.push(this.#toolButton(spatialGestureLabel(gesture), spatialGestureIcon(gesture), () => {
        this.#spatial?.toggleGesture(gesture)
        this.#syncSelectionState()
        this.#close()
      }, this.#spatial?.activeGesture === gesture))
    }
    const pointCount = this.#selectedCount()
    const areaCount = this.#spatial?.selectedAreaCount ?? 0
    if (pointCount > 0 || areaCount > 0) {
      const separator = document.createElement('div')
      separator.setAttribute('role', 'separator')
      separator.style.cssText = 'height:1px;margin:5px 4px;background:var(--lv-line-muted,var(--lv-line-default,#d0d7de))'
      items.push(separator)
      if (pointCount > 0) items.push(this.#toolButton(`Clear ${pointCount} selected ${pointCount === 1 ? 'point' : 'points'}`, Trash2, this.#handleClear))
      if (areaCount > 0) items.push(this.#toolButton(`Clear ${areaCount} selected ${areaCount === 1 ? 'area' : 'areas'}`, Trash2, () => {
        this.#spatial?.clearSelections()
        this.#syncSelectionState()
        this.#close()
      }))
    }
    this.#tools.replaceChildren(...items)
  }

  #toolButton(label: string, icon: IconNode, activate: () => void, active = false): HTMLButtonElement {
    const button = document.createElement('button')
    button.type = 'button'
    button.setAttribute('role', 'menuitem')
    if (active) button.setAttribute('aria-current', 'true')
    button.style.cssText = `display:flex;align-items:center;gap:9px;width:100%;min-height:32px;padding:5px 8px;border:0;border-radius:5px;background:${active ? 'var(--lv-bg-accent-muted,var(--lv-bg-control-hover,#ddf4ff))' : 'transparent'};color:inherit;font:inherit;font-weight:${active ? 'var(--base-text-weight-semibold)' : 'var(--base-text-weight-medium)'};text-align:left;cursor:pointer`
    button.append(iconElement(icon, 16), document.createTextNode(label))
    button.addEventListener('click', (event) => { event.stopPropagation(); activate() })
    return button
  }

  #renderOptions(): void {
    const options = this.#filtered()
    this.#listbox.replaceChildren()
    if (options.length === 0) {
      const empty = document.createElement('div')
      empty.textContent = this.#envelope?.dataState.kind === 'spatial_tiled' && this.#search.value.trim() === ''
        ? 'Zoom in to select visible map points'
        : 'No matching map data'
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
      const marker = document.createElement('span')
      marker.textContent = option.selected ? '✓' : ''
      marker.setAttribute('aria-hidden', 'true')
      marker.style.cssText = 'width:14px;color:var(--lv-fg-link,#0969da);font-weight:var(--base-text-weight-semibold)'
      const label = document.createElement('span')
      label.textContent = option.label
      label.style.cssText = 'min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap'
      item.append(marker, label)
      item.style.cssText = `display:flex;align-items:center;gap:6px;cursor:pointer;padding:6px 8px;border-radius:5px;outline:none;${option.selected ? 'background:var(--lv-bg-accent-muted,var(--lv-bg-control-hover,#ddf4ff));color:var(--lv-fg-default,#1f2328);box-shadow:inset 3px 0 var(--lv-line-accent,#0969da);font-weight:var(--base-text-weight-medium)' : ''}`
      this.#listbox.append(item)
    })
  }

  #open(): void {
    this.#panel.hidden = false
    this.#button.setAttribute('aria-expanded', 'true')
    this.#onOpenChange(true)
    if (this.#hasSpatialTools()) this.#showTools()
    else this.#showDataPane()
  }

  #close(): void {
    if (this.#panel.hidden) return
    this.#panel.hidden = true
    this.#button.setAttribute('aria-expanded', 'false')
    this.#button.setAttribute('aria-haspopup', this.#hasSpatialTools() ? 'menu' : 'listbox')
    this.#onOpenChange(false)
    this.#button.focus()
  }

  readonly #toggle = () => { this.#panel.hidden ? this.#open() : this.#close() }
  readonly #handleTriggerKeydown = (event: KeyboardEvent) => {
    if (event.key === 'Escape' && this.open) {
      event.preventDefault()
      this.#close()
      return
    }
    if (!this.#hasSpatialTools() || (event.key !== 'ArrowDown' && event.key !== 'ArrowUp')) return
    event.preventDefault()
    if (!this.open) this.#open()
    const items = this.#tools.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')
    items[event.key === 'ArrowUp' ? items.length - 1 : 0]?.focus()
  }
  readonly #handleBack = () => this.#showTools()
  readonly #handleSearch = () => { this.#activeIndex = 0; this.#renderOptions() }
  readonly #handleSearchKeydown = (event: KeyboardEvent) => {
    if (event.key === 'Escape') { event.preventDefault(); this.#exitDataPane(); return }
    if (event.key === 'ArrowDown') { event.preventDefault(); this.#focusOption(0) }
  }
  readonly #handleListboxKeydown = (event: KeyboardEvent) => {
    const options = this.#filtered()
    if (options.length === 0) return
    if (event.key === 'Escape') { event.preventDefault(); this.#exitDataPane(); return }
    if (event.key === 'ArrowDown') this.#activeIndex = Math.min(options.length - 1, this.#activeIndex + 1)
    else if (event.key === 'ArrowUp') this.#activeIndex = Math.max(0, this.#activeIndex - 1)
    else if (event.key === 'Home') this.#activeIndex = 0
    else if (event.key === 'End') this.#activeIndex = options.length - 1
    else if (event.key === 'Enter' || event.key === ' ') {
      event.preventDefault()
      const option = options[this.#activeIndex]
      if (option) this.#activate(option)
      return
    } else return
    event.preventDefault()
    this.#renderOptions()
    this.#focusOption(this.#activeIndex)
  }
  readonly #handleToolsKeydown = (event: KeyboardEvent) => {
    const items = [...this.#tools.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')].filter((item) => !item.disabled)
    const target = event.target as Element | null
    const current = typeof target?.closest === 'function' ? target.closest<HTMLButtonElement>('[role="menuitem"]') : null
    const currentIndex = current ? items.indexOf(current) : -1
    let nextIndex: number | undefined
    if (event.key === 'ArrowDown') nextIndex = currentIndex < 0 ? 0 : (currentIndex + 1) % items.length
    else if (event.key === 'ArrowUp') nextIndex = currentIndex < 0 ? items.length - 1 : (currentIndex - 1 + items.length) % items.length
    else if (event.key === 'Home') nextIndex = 0
    else if (event.key === 'End') nextIndex = items.length - 1
    else if (event.key === 'Escape') {
      event.preventDefault()
      event.stopPropagation()
      this.#close()
      return
    } else return
    if (nextIndex === undefined || items.length === 0) return
    event.preventDefault()
    items[nextIndex]?.focus()
  }
  readonly #handleOptionClick = (event: MouseEvent) => {
    const item = (event.target as HTMLElement).closest<HTMLElement>('[data-option-index]')
    const option = item ? this.#filtered()[Number(item.dataset.optionIndex)] : undefined
    if (option) this.#activate(option)
  }
  readonly #handleOutsidePointerDown = (event: PointerEvent) => {
    if (this.#panel.hidden || event.composedPath().includes(this.element)) return
    this.#close()
  }
  readonly #handleClear = () => {
    if (!this.#envelope) return
    const command = clearInteractionCommand(this.#envelope)
    if (command) {
      this.#options = this.#options.map((option) => ({ ...option, selected: false }))
      this.#syncSelectionState()
      this.#renderOptions()
      this.#dispatch(command)
    }
    this.#close()
  }
  #activate(option: InteractionOption): void {
    if (option.command) {
      const selected = option.command.toggle ? !option.selected : true
      this.#options = this.#options.map((candidate) => ({
        ...candidate,
        selected: candidate.key === option.key ? selected : option.command?.toggle ? candidate.selected : false,
      }))
      this.#syncSelectionState()
      this.#renderOptions()
      this.#dispatch(option.command)
    }
    else if (option.refinement) {
      this.#refine(option.refinement.center, option.refinement.zoom)
      this.#close()
    }
  }
  #syncSelectionState(): void {
    const selected = this.#options.filter((option) => option.selected)
    const canonicalSelectionCount = this.#envelope?.selection.length ?? 0
    const selectedCount = Math.max(selected.length, canonicalSelectionCount)
    const activeGesture = this.#spatial?.activeGesture
    if (this.#panel.hidden) this.#button.setAttribute('aria-haspopup', this.#hasSpatialTools() ? 'menu' : 'listbox')
    setButtonContent(
      this.#button,
      activeGesture ? spatialGestureIcon(activeGesture) : MousePointer2,
      activeGesture
        ? spatialGestureActiveLabel(activeGesture)
        : selectedCount > 0
          ? `Points (${selectedCount})`
          : this.#hasSpatialTools() ? 'Select' : this.#isRefinementMode() ? 'Zoom to points' : 'Select points',
      true,
    )
    this.#clear.disabled = selectedCount === 0
    this.#clear.hidden = selectedCount === 0
    this.#clear.style.display = selectedCount === 0 ? 'none' : 'flex'
    this.#status.hidden = selectedCount === 0
    this.#status.textContent = selectedCount === 0
      ? ''
      : selected.length === 0
        ? `${selectedCount} selected map ${selectedCount === 1 ? 'point' : 'points'}`
      : `Selected: ${selected.slice(0, 2).map((option) => option.label).join(', ')}${selected.length > 2 ? ` +${selected.length - 2} more` : ''}`
    this.#status.title = selected.map((option) => option.label).join(', ')
    this.#renderTools()
  }

  #selectedCount(): number {
    return Math.max(this.#options.filter((option) => option.selected).length, this.#envelope?.selection.length ?? 0)
  }

  #hasSpatialTools(): boolean {
    return (this.#spatial?.availableGestures.length ?? 0) > 0
  }

  #exitDataPane(): void {
    if (this.#hasSpatialTools()) this.#showTools()
    else this.#close()
  }

  #showTools(): void {
    this.#dataPane.hidden = true
    this.#tools.hidden = false
    this.#panel.style.width = 'max-content'
    this.#button.setAttribute('aria-haspopup', 'menu')
    this.#renderTools()
    queueMicrotask(() => this.#tools.querySelector<HTMLButtonElement>('[role="menuitem"]')?.focus())
  }

  #showDataPane(): void {
    this.#tools.hidden = true
    this.#dataPane.hidden = false
    this.#panel.style.width = 'min(280px,calc(100vw - 32px))'
    this.#button.setAttribute('aria-haspopup', 'listbox')
    this.#back.hidden = !this.#hasSpatialTools()
    this.#back.style.display = this.#hasSpatialTools() ? 'grid' : 'none'
    if (this.#isRefinementMode()) this.#focusOption(0)
    else this.#search.focus()
  }
  #focusOption(index: number): void {
    this.#activeIndex = index
    this.#renderOptions()
    this.#listbox.querySelector<HTMLElement>(`[data-option-index="${index}"]`)?.focus()
  }
}

function styleButton(button: HTMLButtonElement): void {
  button.style.cssText = 'display:flex;align-items:center;gap:6px;min-height:30px;padding:4px 9px;border:1px solid var(--lv-line-default,#d0d7de);border-radius:6px;background:var(--lv-bg-panel,#fff);color:var(--lv-fg-default,#1f2328);font:inherit;font-weight:var(--base-text-weight-medium);cursor:pointer;box-shadow:0 1px 2px rgba(31,35,40,.08)'
}

function setButtonContent(button: HTMLButtonElement, icon: IconNode, label: string, chevron = false): void {
  const text = document.createElement('span')
  text.dataset.mapSelectionLabel = ''
  text.textContent = label
  button.replaceChildren(iconElement(icon, 16), text)
  if (chevron) button.append(iconElement(ChevronDown, 13))
}

function iconElement(icon: IconNode, size: number): SVGSVGElement {
  const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg')
  svg.setAttribute('width', String(size)); svg.setAttribute('height', String(size)); svg.setAttribute('viewBox', '0 0 24 24')
  svg.setAttribute('fill', 'none'); svg.setAttribute('stroke', 'currentColor'); svg.setAttribute('stroke-width', '2')
  svg.setAttribute('stroke-linecap', 'round'); svg.setAttribute('stroke-linejoin', 'round'); svg.setAttribute('aria-hidden', 'true')
  for (const [tag, attributes] of icon) {
    const child = document.createElementNS('http://www.w3.org/2000/svg', tag)
    for (const [name, value] of Object.entries(attributes)) if (value !== undefined) child.setAttribute(name, String(value))
    svg.append(child)
  }
  return svg
}

function spatialGestureLabel(gesture: VisualizationSpatialSelectionGesture): string {
  if (gesture === 'box') return 'Draw box selection'
  if (gesture === 'lasso') return 'Draw lasso selection'
  return 'Draw radius selection'
}

function spatialGestureActiveLabel(gesture: VisualizationSpatialSelectionGesture): string {
  if (gesture === 'box') return 'Box select'
  if (gesture === 'lasso') return 'Lasso select'
  return 'Radius select'
}

function spatialGestureIcon(gesture: VisualizationSpatialSelectionGesture): IconNode {
  if (gesture === 'box') return SquareDashed
  if (gesture === 'lasso') return LassoSelect
  return CircleDashed
}
