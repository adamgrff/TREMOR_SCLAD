export type HistoryProcess = 'receiving' | 'cells' | 'assembly' | 'ozon'
export type HistoryReference = { type: 'order' | 'sku' | 'cell'; value: string }
export type HomeHistoryEvent = {
  id: string
  occurredAt: string
  process: HistoryProcess
  event: string
  description: string
  references: HistoryReference[]
}

export const historyProcessLabels: Record<HistoryProcess, string> = {
  receiving: 'ПРИЁМКА', cells: 'ЯЧЕЙКИ', assembly: 'СБОРКА', ozon: 'OZON',
}

export function formatHistoryReference(reference: HistoryReference): string {
  return reference.type === 'order' ? `№${reference.value}` : reference.type === 'sku' ? `SKU ${reference.value}` : reference.value
}

export function parseHomeHistory(value: unknown): HomeHistoryEvent[] {
  if (!value || typeof value !== 'object' || !('events' in value) || !Array.isArray(value.events)) throw new Error('Invalid history')
  const ids = new Set<string>()
  return value.events.map((item: unknown) => {
    if (!item || typeof item !== 'object') throw new Error('Invalid event')
    const event = item as HomeHistoryEvent
    if (typeof event.id !== 'string' || !event.id || ids.has(event.id)
      || typeof event.occurredAt !== 'string' || !Number.isFinite(Date.parse(event.occurredAt))
      || !Object.hasOwn(historyProcessLabels, event.process)
      || typeof event.event !== 'string' || typeof event.description !== 'string' || !Array.isArray(event.references)
      || event.references.some(ref => !ref || !['order', 'sku', 'cell'].includes(ref.type) || typeof ref.value !== 'string' || !ref.value)) throw new Error('Invalid event')
    ids.add(event.id)
    return event
  })
}

export function sortHomeHistory(events: HomeHistoryEvent[], order: 'asc' | 'desc'): HomeHistoryEvent[] {
  return [...events].sort((a, b) => {
    const difference = Date.parse(a.occurredAt) - Date.parse(b.occurredAt) || (a.id < b.id ? -1 : a.id > b.id ? 1 : 0)
    return order === 'asc' ? difference : -difference
  })
}

const historyDateFormat = new Intl.DateTimeFormat('ru-RU', {
  timeZone: 'Asia/Krasnoyarsk', day: '2-digit', month: '2-digit', year: 'numeric', hour: '2-digit', minute: '2-digit',
})
export const formatHistoryTime = (value: string) => historyDateFormat.format(new Date(value))
