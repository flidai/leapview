import type { ServiceAccountSecretSignal, ServiceAccountSignal } from '../../generated/signals'
import type { EntityListColumn, EntityListItem } from '../shared/entity-list'

export type ServiceSecretExpirationPreset = '30' | '60' | '90' | 'custom'

export function serviceAccountListItems(accounts: ServiceAccountSignal[], busy: boolean): EntityListItem[] {
  return accounts.map((account) => ({
    id: account.id,
    href: `/admin/service-accounts/${encodeURIComponent(account.id)}`,
    title: account.displayName || account.id,
    description: account.id,
    icon: 'application',
    iconTreatment: 'plain',
    columns: {
      status: account.disabledAt ? 'Disabled' : 'Active',
      created: serviceAccountDateLabel(account.createdAt),
      updated: serviceAccountDateLabel(account.updatedAt),
      actions: '',
    },
    columnTitles: {
      created: account.createdAt || '',
      updated: account.updatedAt || '',
    },
    sortValues: {
      created: account.createdAt || '',
      updated: account.updatedAt || '',
    },
    actions: [{ label: 'Manage credentials', action: 'select', icon: 'details', disabled: busy }],
  }))
}

export function serviceAccountListColumns(): EntityListColumn[] {
  return [
    { id: 'name', label: 'Service account', width: '42%' },
    { id: 'status', label: 'Status', width: '16%', render: 'status' },
    { id: 'created', label: 'Created', width: '18%', render: 'datetime' },
    { id: 'updated', label: 'Updated', width: '18%', render: 'datetime' },
    { id: 'actions', label: 'Actions', width: '6%', align: 'right', sortable: false, render: 'actions' },
  ]
}

export function serviceSecretListItems(secrets: ServiceAccountSecretSignal[], busy: boolean): EntityListItem[] {
  return secrets.map((secret) => {
    const expired = Boolean(secret.expiresAt && Date.parse(secret.expiresAt) <= Date.now())
    const status = secret.revokedAt ? 'Revoked' : expired ? 'Expired' : 'Active'
    return {
      id: secret.id,
      title: secret.name || secret.id,
      description: secret.id,
      icon: 'key',
      iconTreatment: 'plain',
      columns: {
        created: serviceAccountDateLabel(secret.createdAt),
        expires: secret.expiresAt ? serviceAccountDateLabel(secret.expiresAt) : 'Never',
        status,
        actions: '',
      },
      columnTitles: {
        created: secret.createdAt || '',
        expires: secret.expiresAt || '',
      },
      sortValues: {
        created: secret.createdAt || '',
        expires: secret.expiresAt || '',
      },
      actions: [{ label: secret.revokedAt ? 'Credential revoked' : 'Revoke credential', action: 'revoke', icon: 'cancel', disabled: busy || Boolean(secret.revokedAt) }],
    }
  })
}

export function serviceSecretListColumns(): EntityListColumn[] {
  return [
    { id: 'name', label: 'Credential', width: '38%' },
    { id: 'created', label: 'Created', width: '20%', render: 'datetime' },
    { id: 'expires', label: 'Expires', width: '20%', render: 'datetime' },
    { id: 'status', label: 'Status', width: '16%', render: 'status' },
    { id: 'actions', label: 'Actions', width: '6%', align: 'right', sortable: false, render: 'actions' },
  ]
}

export function serviceAccountDateLabel(value?: string): string {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.valueOf())) return value
  return new Intl.DateTimeFormat(undefined, { month: 'short', day: 'numeric', year: 'numeric' }).format(date)
}

export function serviceSecretExpirationOptions(): Array<{ value: ServiceSecretExpirationPreset, label: string }> {
  return ([30, 60, 90] as const).map((days) => ({
    value: String(days) as ServiceSecretExpirationPreset,
    label: `${days} days (${serviceEndOfDayInDays(days).toLocaleDateString(undefined, { month: 'short', day: 'numeric', year: 'numeric' })})`,
  })).concat([
    { value: 'custom', label: 'Custom date' },
  ])
}

export function serviceEndOfDayInDays(days: number): Date {
  const date = new Date()
  date.setDate(date.getDate() + days)
  date.setHours(23, 59, 59, 999)
  return date
}

export function serviceDateInputValueInDays(days: number): string {
  const date = serviceEndOfDayInDays(days)
  const year = date.getFullYear()
  const month = String(date.getMonth() + 1).padStart(2, '0')
  const day = String(date.getDate()).padStart(2, '0')
  return `${year}-${month}-${day}`
}
