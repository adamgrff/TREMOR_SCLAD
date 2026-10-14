import type { ProblemHistoryEvent } from './OrderProblemDialog'

export default function groupProblemHistory<T extends ProblemHistoryEvent>(history: T[]) {
  const groups = new Map<string, { key: string; latest: T; previous: T[] }>()
  for (const event of [...history].sort((a, b) => Date.parse(a.createdAt) - Date.parse(b.createdAt))) {
    const key = JSON.stringify([event.kind ?? '', event.operationId ?? '', event.title, event.message, event.outcome])
    const group = groups.get(key)
    if (group) {
      group.previous.push(group.latest)
      group.latest = event
    } else groups.set(key, { key, latest: event, previous: [] })
  }
  return [...groups.values()].sort((a, b) => Date.parse(a.latest.createdAt) - Date.parse(b.latest.createdAt))
}
