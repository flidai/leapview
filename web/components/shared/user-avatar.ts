import { LitElement, css, html } from 'lit'
import { property } from 'lit/decorators.js'

type UserAvatarSize = 'small' | 'medium'

class LeapViewUserAvatar extends LitElement {
  @property() name = ''
  @property({ attribute: 'image-url' }) imageUrl = ''
  @property({ reflect: true }) size: UserAvatarSize = 'small'

  static styles = css`
    :host {
      --lv-user-avatar-size: var(--control-xsmall-size);
      display: inline-grid;
      width: var(--lv-user-avatar-size);
      height: var(--lv-user-avatar-size);
      flex: 0 0 auto;
      vertical-align: middle;
    }

    :host([size='medium']) {
      --lv-user-avatar-size: var(--control-large-size);
    }

    .avatar {
      display: grid;
      width: 100%;
      height: 100%;
      box-sizing: border-box;
      place-items: center;
      overflow: hidden;
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-full);
      background: var(--bgColor-neutral-muted, var(--lv-bg-panel-muted));
      color: var(--fgColor-default, var(--lv-fg-default));
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-semibold);
      letter-spacing: 0;
    }

    :host([size='medium']) .avatar {
      font: var(--lv-type-body);
      font-weight: var(--base-text-weight-semibold);
    }

    img {
      width: 100%;
      height: 100%;
      object-fit: cover;
    }
  `

  render() {
    return html`<span class="avatar">${this.imageUrl
      ? html`<img src=${this.imageUrl} alt="">`
      : userInitials(this.name)}</span>`
  }
}

function userInitials(value: string): string {
  const parts = value.trim().split(/\s+/).filter(Boolean)
  return (parts.length > 1 ? parts[0][0] + parts[parts.length - 1][0] : parts[0]?.slice(0, 2) ?? '?').toUpperCase()
}

if (!customElements.get('lv-user-avatar')) customElements.define('lv-user-avatar', LeapViewUserAvatar)

export { LeapViewUserAvatar, userInitials }
