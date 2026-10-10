import type { ProblemHistoryEvent } from './OrderProblemDialog'

export default function groupProblemHistory(history: ProblemHistoryEvent[]) {
  const groups = new Map<string, { key: string; latest: ProblemHistoryEvent; previous: ProblemHistoryEvent[] }>()
  for (const event of [...history].sort((a, b) => Date.parse(a.createdAt) - Date.parse(b.createdAt))) {
    const key = JSON.stringify([event.title, event.message, event.outcome])
    const group = groups.get(key)
    if (group) {
      group.previous.push(group.latest)
      group.latest = event
    } else groups.set(key, { key, latest: event, previous: [] })
  }
  return [...groups.values()].sort((a, b) => Date.parse(a.latest.createdAt) - Date.parse(b.latest.createdAt))
}
