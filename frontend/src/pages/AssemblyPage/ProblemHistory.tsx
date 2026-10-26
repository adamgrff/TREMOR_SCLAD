import { useEffect, useId, useLayoutEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import groupProblemHistory from './groupProblemHistory'
import type { ProblemHistoryEvent } from './OrderProblemDialog'

export type HistoryRecord = ProblemHistoryEvent & { id?: string; ordinal?: number; recordedAt?: string; occurredAt?: string }
export function ProblemTime({ value, seconds = false }: { value?: string | null; seconds?: boolean }) {
  const date = value ? new Date(value) : null
  if (!date || Number.isNaN(date.getTime())) return <span className="problemOrders__muted">Время неизвестно</span>
  return <time className="problemOrders__time" dateTime={value!} title={value!}><span className="assemblyQueue__time">{date.toLocaleTimeString('ru-RU', { timeZone: 'Asia/Krasnoyarsk', hour: '2-digit', minute: '2-digit', ...(seconds ? { second: '2-digit' as const } : {}) })}</span><span className="assemblyQueue__date">{date.toLocaleDateString('ru-RU', { timeZone: 'Asia/Krasnoyarsk' })}</span></time>
}

// Only the existing repeated-event surface is portalled; grouping stays in groupProblemHistory.
function EventHistoryPopover({ previous, technical }: { previous: HistoryRecord[]; technical: boolean }) {
  const trigger = useRef<HTMLButtonElement>(null)
  const popup = useRef<HTMLDivElement>(null)
  const list = useRef<HTMLDivElement>(null)
  const leaveTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const enterList = useRef(false)
  const suppressFocus = useRef(false)
  const [dark, setDark] = useState(() => typeof document !== 'undefined' && document.documentElement.dataset.theme === 'dark')
  const [open, setOpen] = useState(false)
  const id = useId()
  function clearLeave() { if (leaveTimer.current !== null) { clearTimeout(leaveTimer.current); leaveTimer.current = null } }
  function show() { clearLeave(); setOpen(true) }
  function leave() {
    clearLeave()
    leaveTimer.current = setTimeout(() => {
      if (trigger.current?.matches(':hover') || popup.current?.matches(':hover') || trigger.current === document.activeElement || popup.current?.contains(document.activeElement)) return
      setOpen(false)
    }, 500)
  }
  useEffect(() => {
    const observer = new MutationObserver(() => { clearLeave(); setOpen(false); setDark(document.documentElement.dataset.theme === 'dark') })
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
    return () => { observer.disconnect(); clearLeave() }
  }, [])
  useLayoutEffect(() => {
    if (!dark || !open) return
    function position() {
      const anchor = trigger.current, surface = popup.current, scroll = list.current
      if (!anchor || !surface || !scroll) return
      const scale = parseFloat(getComputedStyle(document.documentElement).zoom) || 1
      const a = anchor.getBoundingClientRect(), inset = 12 * scale, gap = 8 * scale
      const box = surface.getBoundingClientRect()
      const chrome = box.height - scroll.getBoundingClientRect().height
      const above = a.top - inset - gap, below = innerHeight - inset - a.bottom - gap
      const wanted = chrome + Math.min(260 * scale, scroll.scrollHeight * scale)
      const up = above >= wanted || (below < wanted && above > below)
      scroll.style.maxHeight = `${Math.max(0, Math.min(260, (Math.min(up ? above : below, innerHeight - 2 * inset) - chrome) / scale))}px`
      const sized = surface.getBoundingClientRect()
      const x = Math.max(inset, Math.min(innerWidth - inset - sized.width, a.right - sized.width))
      const y = Math.max(inset, Math.min(innerHeight - inset - sized.height, up ? a.top - gap - sized.height : a.bottom + gap))
      surface.style.left = `${x / scale}px`
      surface.style.top = `${y / scale}px`
    }
    function escape(event: KeyboardEvent) {
      if (event.key !== 'Escape') return
      event.preventDefault(); event.stopPropagation(); clearLeave(); setOpen(false)
      suppressFocus.current = true
      trigger.current?.focus({ preventScroll: true })
      suppressFocus.current = false
    }
    const resize = new ResizeObserver(position)
    if (trigger.current) resize.observe(trigger.current)
    if (popup.current) resize.observe(popup.current)
    position()
    if (enterList.current) { enterList.current = false; list.current?.focus({ preventScroll: true }) }
    window.addEventListener('resize', position)
    window.addEventListener('scroll', position, true)
    document.addEventListener('keydown', escape, true)
    return () => { resize.disconnect(); window.removeEventListener('resize', position); window.removeEventListener('scroll', position, true); document.removeEventListener('keydown', escape, true) }
  }, [dark, open])
  function focusList() {
    enterList.current = true
    show()
    if (open) { enterList.current = false; list.current?.focus({ preventScroll: true }) }
  }
  if (!dark) {
    return <span className="problemOrders__repeats"><button type="button" className="problemOrders__repeatBadge" aria-label={`Предыдущие одинаковые сообщения: ${previous.length}`}>+{previous.length}</button><span className="problemOrders__repeatPopup" role="tooltip"><strong className="problemOrders__repeatTitle">Предыдущие сообщения · {previous.length}</strong><span className="problemOrders__repeatList">{previous.map((repeat, index) => <span className="problemOrders__repeatEntry" key={repeat.id ?? `${repeat.createdAt}-${index}`}><ProblemTime value={repeat.createdAt} seconds/><span className={`problemOrders__checkResult problemOrders__checkResult--${repeat.outcome}`}><strong>{repeat.title}</strong><span>{repeat.message}</span>{technical && <small>{repeat.id}</small>}</span></span>)}</span></span></span>
  }
  return <span className="problemOrders__repeats">
    <button ref={trigger} type="button" className="problemOrders__repeatBadge ds-v1 ui-v1-event-history__trigger" aria-label={`Предыдущие одинаковые сообщения: ${previous.length}`} aria-expanded={open} aria-controls={open ? id : undefined}
      onMouseEnter={show} onMouseLeave={leave} onFocus={() => { if (!suppressFocus.current) show() }} onBlur={leave} onClick={focusList}
      onKeyDown={event => { if (event.key === 'ArrowDown' || (event.key === 'Tab' && !event.shiftKey && open)) { event.preventDefault(); focusList() } }}>+{previous.length}</button>
    {open && createPortal(<div ref={popup} id={id} role="region" aria-labelledby={`${id}-title`} className="ds-v1 ui-v1-event-history" onMouseEnter={clearLeave} onMouseLeave={leave} onFocus={clearLeave} onBlur={leave}>
      <strong id={`${id}-title`} className="ui-v1-event-history__title">Предыдущие сообщения · {previous.length}</strong>
      <div ref={list} className="ui-v1-event-history__list" role="list" tabIndex={0} aria-label="Предыдущие сообщения" onKeyDown={event => {
        if (event.key !== 'Tab') return
        event.preventDefault()
        if (event.shiftKey) { trigger.current?.focus({ preventScroll: true }); return }
        const controls = [...document.querySelectorAll<HTMLElement>('button, a[href], input, select, textarea, summary, [tabindex]')].filter(element => !popup.current?.contains(element) && element.tabIndex >= 0 && !element.matches(':disabled') && element.getClientRects().length > 0 && getComputedStyle(element).visibility === 'visible')
        const next = controls[controls.indexOf(trigger.current!) + 1]
        setOpen(false); clearLeave()
        if (next) next.focus({ preventScroll: true })
        else { suppressFocus.current = true; trigger.current?.focus({ preventScroll: true }); suppressFocus.current = false }
      }}>
        {previous.map((repeat, index) => <div className="ui-v1-event-history__entry" role="listitem" key={repeat.id ?? `${repeat.createdAt}-${index}`}>
          <ProblemTime value={repeat.createdAt} seconds/>
          <span className={`ui-v1-event-history__result ui-v1-event-history__result--${repeat.outcome}`}><strong>{repeat.title}</strong><span>{repeat.message}</span>{technical && <small>{repeat.id}</small>}</span>
        </div>)}
      </div>
    </div>, document.body)}
  </span>
}

export default function ProblemHistory({ events, fullByDefault = false }: { events: HistoryRecord[]; fullByDefault?: boolean }) {
  const [full, setFull] = useState(fullByDefault)
  const [technical, setTechnical] = useState(false)
  const rows = full ? events.map((event, index) => ({ key: event.id ?? String(index), latest: event, previous: [] as HistoryRecord[] })) : groupProblemHistory(events)
  return <div className="problemHistory ds-v1 ui-v1-history-text">
    <div className="problemHistory__tools"><button type="button" className="receivingHistory__button ds-v1 ui-v1-button" aria-pressed={full} onClick={() => setFull(!full)}>{full ? 'Свернуть повторы' : 'Показать все события'}</button>{events.some(e => e.id) && <label className="ds-v1 ui-v1-type-meta"><input type="checkbox" checked={technical} onChange={e => setTechnical(e.target.checked)} /> ID и время записи</label>}<span className="ds-v1 ui-v1-type-meta">{events.length} событий</span></div>
    <ol className="problemOrders__history problemOrders__checks" aria-label="История создания и решения проблемы" aria-live="polite">{rows.map(({ key, latest: event, previous }) => <li key={key}>
      <ProblemTime value={event.createdAt} seconds/>
      <span className={`problemOrders__checkResult problemOrders__checkResult--${event.outcome}`}><strong className="ds-v1 ui-v1-type-primary ui-v1-type-semibold">{event.title}</strong><span className="ds-v1 ui-v1-type-primary">{event.message}</span>{technical && <small className="ds-v1 ui-v1-type-meta">Событие № {(event.ordinal ?? 0) + 1} · {event.kind ?? 'legacy'} · {event.id}<br/>Записано сервером: {event.recordedAt ? new Date(event.recordedAt).toLocaleString('ru-RU', { timeZone: 'Asia/Krasnoyarsk' }) : 'неизвестно'}</small>}</span>
      {previous.length > 0 && <EventHistoryPopover previous={previous} technical={technical} />}
    </li>)}</ol>
    {!events.length && <p className="problemOrders__muted ds-v1 ui-v1-type-secondary">Событий для этой записи не сохранено.</p>}
  </div>
}
