import { html } from 'lit'
import { CheckCircle2, Circle, Clock3, XCircle } from 'lucide'
import type { EntityListRowAction } from '../shared/entity-list'
import { lucideIcon } from '../shared/lucide-icons'

export function commandLoadingLabel(action: string): string {
  return action === 'cancel' ? 'Cancelling pipeline run…' : 'Queuing pipeline run…'
}

export function capitalize(value: string): string {
  return value ? value.charAt(0).toUpperCase() + value.slice(1) : '—'
}

export function pipelineStatusLabel(status: string): string {
  return status === 'prepared' ? 'Finalizing' : capitalize(status)
}

export function shortFailureReason(error: string): string {
  const firstLine = error.split(/\r?\n/, 1)[0].trim()
  return firstLine.length > 100 ? `${firstLine.slice(0, 97)}…` : firstLine
}

export function formatDateTime(value: string | undefined): string {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  const elapsed = date.getTime() - Date.now()
  const minutes = Math.round(Math.abs(elapsed) / 60_000)
  if (minutes < 1) return elapsed >= 0 ? 'Now' : 'Just now'
  if (minutes < 60) return elapsed >= 0 ? `In ${minutes} min` : `${minutes} min ago`
  const hours = Math.round(minutes / 60)
  if (hours < 24) return elapsed >= 0 ? `In ${hours} hr` : `${hours} hr ago`
  return new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeZone: 'UTC' }).format(date) + ' UTC'
}

export function formatExactDateTime(value: string | undefined): string {
  if (!value) return ''
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short', timeZone: 'UTC' }).format(date) + ' UTC'
}

export function runDetailValue(row: Record<string, unknown>, key: string): string {
  const value = row[key]
  if (value == null || value === '' || value === '-') return '—'
  return String(value)
}

export function runDetailFact(label: string, value: string, code = false) {
  return html`<div class="run-detail-fact"><span>${label}</span>${code ? html`<code>${value}</code>` : html`<strong>${value}</strong>`}</div>`
}

export function formatRunDetailDate(value: unknown): string {
  const normalized = value == null || value === '' || value === '-' ? '' : String(value)
  if (!normalized) return '—'
  const date = new Date(normalized)
  return Number.isNaN(date.getTime()) ? normalized : new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'medium', timeZone: 'UTC' }).format(date) + ' UTC'
}

export function formatRunListDate(value: unknown): string {
  const normalized = value == null || value === '' || value === '-' ? '' : String(value)
  if (!normalized) return 'Not started'
  const date = new Date(normalized)
  return Number.isNaN(date.getTime()) ? normalized : new Intl.DateTimeFormat(undefined, { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit', hourCycle: 'h23', timeZone: 'UTC' }).format(date) + ' UTC'
}

export function runStatusTone(status: string): 'success' | 'danger' | 'attention' | 'muted' {
  if (status === 'succeeded') return 'success'
  if (status === 'failed' || status === 'cancelled') return 'danger'
  if (status === 'queued' || status === 'running' || status === 'prepared') return 'attention'
  return 'muted'
}

export function runStatusIcon(status: string) {
  if (status === 'succeeded') return lucideIcon(CheckCircle2, { size: 16, strokeWidth: 2 })
  if (status === 'failed' || status === 'cancelled') return lucideIcon(XCircle, { size: 16, strokeWidth: 2 })
  if (status === 'queued' || status === 'running' || status === 'prepared') return lucideIcon(Clock3, { size: 16, strokeWidth: 2 })
  return lucideIcon(Circle, { size: 16, strokeWidth: 2 })
}

export function firstRunListValue(...values: unknown[]): string {
  const value = values.find((candidate) => candidate != null && candidate !== '' && candidate !== '-')
  return value == null ? '—' : String(value)
}

export function pipelineRunActionIcon(icon: string): EntityListRowAction['icon'] {
  if (icon === 'details') return 'details'
  if (icon === 'cancel') return 'cancel'
  if (icon === 'refresh') return 'refresh'
  return 'play'
}
