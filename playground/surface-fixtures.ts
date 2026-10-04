export const surfaceExamples = [
  { id: 'drawer', label: 'Drawer' },
  { id: 'identity', label: 'Avatars, brand & icons' },
  { id: 'toast-region', label: 'Notification stack' },
  { id: 'one-time-secret', label: 'One-time secret' },
  { id: 'empty-state', label: 'Empty states' },
  { id: 'page-header', label: 'Page header' },
  { id: 'breadcrumb', label: 'Breadcrumbs' },
  { id: 'settings', label: 'Settings sections' },
  { id: 'entity-detail', label: 'Entity detail' },
  { id: 'icon-picker', label: 'Dashboard icon picker' },
  { id: 'appearance', label: 'Dashboard appearance' },
  { id: 'report-footer', label: 'Report footer & zoom' },
]

export const surfaceDocs: Record<string, { component: string; source: string; inputs: string; events: string; note: string }> = {
  drawer: { component: '<lv-drawer>', source: 'web/components/shared/drawer.ts', inputs: 'open · modal · closeOnOutside · size · label · title/subtitle/body slots', events: 'lv-drawer-close', note: 'The real drawer owns Escape, outside clicks, its modal backdrop and keyboard focus cycle. This fixture owns open state and restores focus to the trigger.' },
  identity: { component: '<lv-user-avatar> · <lv-brand-mark> · fieldTypeIcon() / lucideIcon()', source: 'web/components/shared/user-avatar.ts · brand-mark.ts · field-type-icon.ts · lucide-icons.ts', inputs: 'Avatar: name · imageUrl · size. Brand: large attribute · --lv-brand-mark-size. Field icon: semantic type string.', events: 'No component events', note: 'Initials and fallback handling use the production avatar. Brand and field icons use production Lucide assets and inherit their surrounding color.' },
  'toast-region': { component: '<lv-toast-region>', source: 'web/components/shared/toast.ts', inputs: 'show({ message, tone, durationMs, action }) → id · dismiss(id)', events: 'lv-toast-action · lv-toast-dismiss from contained production toasts', note: 'The real region owns its four-entry stack, timers and pointer/focus pause behavior. Notifications with an action remain until dismissed or acted on. Leaving the example disposes the region and timers.' },
  'one-time-secret': { component: '<lv-one-time-secret>', source: 'web/components/shared/one-time-secret.ts', inputs: 'secret · message · copyLabel', events: 'No public component events; the component owns clipboard success/error state.', note: 'The displayed value is a deterministic dummy. Copy uses the real browser clipboard; browser permission failures show the production error.' },
  'empty-state': { component: 'renderEmptyState() + emptyStateStyles', source: 'web/components/shared/empty-state.ts', inputs: 'title · description · icon · actions · role', events: 'Native action clicks, owned by the caller', note: 'Production empty-state helper with empty, no-results, error and action-complete variants.' },
  'page-header': { component: 'renderPageHeader() + pageHeaderStyles', source: 'web/components/shared/page-header.ts', inputs: 'title · detail · eyebrow · actions', events: 'Native action clicks, owned by the caller', note: 'The helper owns responsive header and action layout; fixture actions update only local state.' },
  breadcrumb: { component: 'renderBreadcrumb() + breadcrumbStyles', source: 'web/components/shared/breadcrumb.ts', inputs: 'items: label, href/current, prefix · accessible navigation label', events: 'Native link clicks', note: 'Current-page semantics, separator icons, optional resource glyphs and long-label truncation come from the production helper.' },
  settings: { component: 'renderSettingsSection / Row / Actions', source: 'web/components/shared/settings-layout.ts · settings-field-styles.ts', inputs: 'section appearance · row layout · label/control IDs · content', events: 'Native form input and submit events', note: 'Real settings helper markup and shared styles. Save and Reset only change local fixture values.' },
  'entity-detail': { component: 'renderEntityDetail() + entityDetailStyles', source: 'web/components/shared/entity-detail.ts', inputs: 'title · subtitle · avatar · badges · actions · notice · sections', events: 'Native links and action clicks', note: 'The real entity-detail header, identity, badges and facts layout wraps dummy account data.' },
  'icon-picker': { component: '<lv-dashboard-icon-picker>', source: 'web/components/app/dashboard-icon-picker.ts', inputs: 'icon · color · label', events: 'lv-dashboard-appearance-select { icon?, color? }', note: 'Search, virtual scrolling, icon selection and color choices use the complete generated production Lucide catalog. Selection is acknowledged locally.' },
  appearance: { component: '<lv-dashboard-appearance-editor>', source: 'web/components/project/dashboard-appearance-editor.ts', inputs: 'appearance: icon, color, revision · label · assetID', events: 'lv-dashboard-appearance-change { icon, color }', note: 'The editor owns its popover and optimistic save state. The fixture responds with a new appearance property instead of sending a command to a server.' },
  'report-footer': { component: '<lv-report-footer> · <lv-report-zoom>', source: 'web/components/dashboard/report-footer.ts · report-view-controls.ts', inputs: 'status: loading, lastUpdated, error · document lv-report-zoom-state', events: 'lv-report-zoom-command { layout?, mode?, scale? }', note: 'Production footer and zoom controls. A small local page illustrates layout and scale; the fixture responds through the public document zoom-state event without saving preferences.' },
}

export const demoSecret = 'lv_demo_not_a_real_secret_0123456789abcdef'
