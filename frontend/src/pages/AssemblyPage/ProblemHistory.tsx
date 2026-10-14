import { useState } from 'react'
import groupProblemHistory from './groupProblemHistory'
import type { ProblemHistoryEvent } from './OrderProblemDialog'

export type HistoryRecord = ProblemHistoryEvent & { id?: string; ordinal?: number; recordedAt?: string; occurredAt?: string }
export function ProblemTime({ value, seconds = false }: { value?: string | null; seconds?: boolean }) {
  const date = value ? new Date(value) : null
  if (!date || Number.isNaN(date.getTime())) return <span className="problemOrders__muted">Время неизвестно</span>
  return <time className="problemOrders__time" dateTime={value!} title={value!}><span className="assemblyQueue__time">{date.toLocaleTimeString('ru-RU', { timeZone: 'Asia/Krasnoyarsk', hour: '2-digit', minute: '2-digit', ...(seconds ? { second: '2-digit' as const } : {}) })}</span><span className="assemblyQueue__date">{date.toLocaleDateString('ru-RU', { timeZone: 'Asia/Krasnoyarsk' })}</span></time>
}

export default function ProblemHistory({ events, fullByDefault = false }: { events: HistoryRecord[]; fullByDefault?: boolean }) {
  const [full, setFull] = useState(fullByDefault)
  const [technical, setTechnical] = useState(false)
  const rows = full ? events.map((event, index) => ({ key: event.id ?? String(index), latest: event, previous: [] as HistoryRecord[] })) : groupProblemHistory(events)
  return <div className="problemHistory">
    <div className="problemHistory__tools"><button type="button" className="receivingHistory__button" aria-pressed={full} onClick={() => setFull(!full)}>{full ? 'Свернуть повторы' : 'Показать все события'}</button>{events.some(e => e.id) && <label><input type="checkbox" checked={technical} onChange={e => setTechnical(e.target.checked)} /> ID и время записи</label>}<span>{events.length} событий</span></div>
    <ol className="problemOrders__history problemOrders__checks" aria-label="История создания и решения проблемы" aria-live="polite">{rows.map(({ key, latest: event, previous }) => <li key={key}>
      <ProblemTime value={event.createdAt} seconds/>
      <span className={`problemOrders__checkResult problemOrders__checkResult--${event.outcome}`}><strong>{event.title}</strong><span>{event.message}</span>{technical && <small>Событие № {(event.ordinal ?? 0) + 1} · {event.kind ?? 'legacy'} · {event.id}<br/>Записано сервером: {event.recordedAt ? new Date(event.recordedAt).toLocaleString('ru-RU', { timeZone: 'Asia/Krasnoyarsk' }) : 'неизвестно'}</small>}</span>
      {previous.length > 0 && <span className="problemOrders__repeats"><button type="button" className="problemOrders__repeatBadge" aria-label={`Предыдущие одинаковые сообщения: ${previous.length}`}>+{previous.length}</button><span className="problemOrders__repeatPopup" role="tooltip"><strong className="problemOrders__repeatTitle">Предыдущие сообщения · {previous.length}</strong><span className="problemOrders__repeatList">{previous.map((repeat, index) => <span className="problemOrders__repeatEntry" key={repeat.id ?? `${repeat.createdAt}-${index}`}><ProblemTime value={repeat.createdAt} seconds/><span className={`problemOrders__checkResult problemOrders__checkResult--${repeat.outcome}`}><strong>{repeat.title}</strong><span>{repeat.message}</span>{technical && <small>{repeat.id}</small>}</span></span>)}</span></span></span>}
    </li>)}</ol>
    {!events.length && <p className="problemOrders__muted">Событий для этой записи не сохранено.</p>}
  </div>
}
