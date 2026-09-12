/** DOM surface shared by browser-backed tests when custom elements are created by tag name. */
interface TestDomElement extends HTMLElement {
  readonly shadowRoot: ShadowRoot | null
  readonly updateComplete: Promise<unknown>
}
