import { css } from 'lit'

// Shared title, saved-state, and settings-popover styling for the builder toolbar.
export const dashboardBuilderToolbarStyles = css`
    .title-wrap {
      position: relative;
      min-width: 0;
      margin-right: auto;
    }

    .dashboard-metadata {
      position: relative;
      z-index: 5;
    }

    .dashboard-metadata summary {
      display: grid;
      box-sizing: border-box;
      place-items: center;
      width: var(--control-medium-size);
      height: var(--control-medium-size);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      color: var(--lv-fg-muted);
      cursor: pointer;
      list-style: none;
    }

    .dashboard-metadata summary:hover { background: var(--lv-bg-control-hover); }
    .dashboard-metadata summary:focus-visible { outline: 2px solid var(--lv-fg-accent); outline-offset: 2px; }

    .dashboard-metadata summary::-webkit-details-marker {
      display: none;
    }

    .dashboard-metadata-form {
      position: absolute;
      top: 100%;
      right: 0;
      box-sizing: border-box;
      display: grid;
      width: min(22rem, calc(100vw - var(--base-size-16)));
      gap: var(--base-size-8);
      margin-top: var(--base-size-6);
      padding: var(--base-size-12);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      box-shadow: var(--lv-shadow-floating-sm);
    }

    .appearance-control {
      position: relative;
      flex: 0 0 auto;
    }

    .appearance-trigger {
      display: grid;
      width: var(--control-medium-size);
      min-height: var(--control-medium-size);
      place-items: center;
      padding: 0;
      border-color: var(--display-purple-borderColor-muted, var(--lv-border-muted));
      background: var(--display-purple-bgColor-muted, var(--lv-bg-panel-muted));
      color: var(--display-purple-fgColor, var(--lv-fg-default));
    }

    .appearance-trigger.appearance-color-gray { border-color: var(--display-gray-borderColor-muted, var(--lv-border-muted)); background: var(--display-gray-bgColor-muted); color: var(--display-gray-fgColor); }
    .appearance-trigger.appearance-color-blue { border-color: var(--display-blue-borderColor-muted, var(--lv-border-muted)); background: var(--display-blue-bgColor-muted); color: var(--display-blue-fgColor); }
    .appearance-trigger.appearance-color-green { border-color: var(--display-green-borderColor-muted, var(--lv-border-muted)); background: var(--display-green-bgColor-muted); color: var(--display-green-fgColor); }
    .appearance-trigger.appearance-color-yellow { border-color: var(--display-yellow-borderColor-muted, var(--lv-border-muted)); background: var(--display-yellow-bgColor-muted); color: var(--display-yellow-fgColor); }
    .appearance-trigger.appearance-color-orange { border-color: var(--display-orange-borderColor-muted, var(--lv-border-muted)); background: var(--display-orange-bgColor-muted); color: var(--display-orange-fgColor); }
    .appearance-trigger.appearance-color-red { border-color: var(--display-red-borderColor-muted, var(--lv-border-muted)); background: var(--display-red-bgColor-muted); color: var(--display-red-fgColor); }
    .appearance-trigger.appearance-color-purple { border-color: var(--display-purple-borderColor-muted, var(--lv-border-muted)); background: var(--display-purple-bgColor-muted); color: var(--display-purple-fgColor); }
    .appearance-trigger.appearance-color-pink { border-color: var(--display-pink-borderColor-muted, var(--lv-border-muted)); background: var(--display-pink-bgColor-muted); color: var(--display-pink-fgColor); }
    .appearance-trigger.appearance-color-coral { border-color: var(--display-coral-borderColor-muted, var(--lv-border-muted)); background: var(--display-coral-bgColor-muted); color: var(--display-coral-fgColor); }

    .appearance-popover {
      position: absolute;
      z-index: 5;
      top: calc(100% + var(--base-size-8));
      left: 0;
      width: min(22.5rem, calc(100vw - var(--base-size-16)));
    }

    .title {
      margin: 0;
      overflow: hidden;
      font: var(--lv-type-section-title);
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .meta {
      display: flex;
      align-items: center;
      gap: var(--base-size-6);
      margin-top: var(--base-size-2);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      white-space: nowrap;
    }

    .meta::before {
      width: var(--base-size-6);
      height: var(--base-size-6);
      border-radius: var(--lv-radius-full);
      background: var(--lv-fg-muted);
      content: '';
    }

    .meta[data-state='dirty']::before,
    .meta[data-state='saving']::before,
    .meta[data-state='error']::before {
      background: var(--lv-fg-warning);
    }

    .meta[data-state='saved']::before {
      background: var(--lv-fg-success);
    }

`
