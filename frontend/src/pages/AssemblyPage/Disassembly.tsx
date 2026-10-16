import { useEffect, useRef, useState, type FormEvent } from 'react'
import useScanErrorFeedback from './useScanErrorFeedback'
import './Disassembly.css'

export type DisassemblyItem = { id: string; sku: string; warehouseSku: string; name: string; cell: string; cellId: number; warehouse: string; operationId: string; returned: boolean; returnable: boolean; returnedAt?: string }
export type DisassemblySession = { id: string; number: string; reason: string; status: 'waiting' | 'active' | 'completed'; startedAt?: string | null; items: DisassemblyItem[]; pendingItemId: string | null; lastScan: string; log: string; error: boolean }
const reasons: Record<string, string> = { cancelled_order: 'Отмена заказа', damaged_product: 'Повреждённый товар', invalid_order: 'Ошибка данных заказа', other: 'Физический разбор' }
const sessionStatus = (session: DisassemblySession) => session.status === 'completed' ? 'Завершён' : session.status === 'active' ? 'Активен' : session.startedAt || session.pendingItemId ? 'Отложен' : 'Ещё не начат'
type CellSummary = { cell: string; cellId: number; warehouse: string; total: number; returned: number }
type ProductSummary = { sku: string; name: string; warehouseSku: string; total: number; returned: number; returnable: boolean; cells: CellSummary[] }
export default function Disassembly({ sessions, blocked, onAction }: { sessions: DisassemblySession[]; blocked: boolean; onAction: (sessionId: string, action: 'start' | 'scan' | 'finish' | 'pause', code?: string) => Promise<boolean> }) {
  const [scan, setScan] = useState('')
  const inputRef = useRef<HTMLInputElement>(null)
  const formRef = useRef<HTMLFormElement>(null)
  const restoreFocus = useRef(false)
  const { signalError, overlay } = useScanErrorFeedback()
  const open = sessions.filter(s => s.status !== 'completed')
  const active = open.find(s => s.status === 'active')
  useEffect(() => { setScan('') }, [active?.id, active?.pendingItemId])
  const pending = active?.items.find(item => item.id === active.pendingItemId)
  const returned = active?.items.filter(item => item.returned).length ?? 0
  const done = !!active && returned === active.items.length && !pending
  const step = done ? null : pending ? 1 : 0
  const previousStep = useRef(step)
  const [outgoingStep, setOutgoingStep] = useState<number | null>(null)
  useEffect(() => {
    if (previousStep.current === step) return
    setOutgoingStep(previousStep.current)
    previousStep.current = step
    const timer = window.setTimeout(() => setOutgoingStep(null), 600)
    return () => window.clearTimeout(timer)
  }, [step])
  useEffect(() => {
    if (blocked || !restoreFocus.current) return
    restoreFocus.current = false
    if (!done && (document.activeElement === document.body || formRef.current?.contains(document.activeElement))) inputRef.current?.focus({ preventScroll: true })
  }, [blocked, done, active?.pendingItemId])
  const groups = active?.items.reduce<ProductSummary[]>((result, item) => {
    let group = result.find(g => g.sku === item.sku)
    if (!group) {
      group = { name: item.name, sku: item.sku, warehouseSku: item.warehouseSku, total: 0, returned: 0, returnable: true, cells: [] }
      result.push(group)
    }
    let cell = group.cells.find(c => c.cellId === item.cellId)
    if (!cell) {
      cell = { cell: item.cell, cellId: item.cellId, warehouse: item.warehouse, total: 0, returned: 0 }
      group.cells.push(cell)
    }
    group.total++; cell.total++
    if (item.returned) { group.returned++; cell.returned++ }
    group.returnable &&= item.returnable
    return result
  }, []) ?? []
  async function submit(event: FormEvent) {
    event.preventDefault()
    if (!active || blocked || !scan.trim() || done) return
    const code = scan.trim(); setScan(''); restoreFocus.current = true
    if (!await onAction(active.id, 'scan', code)) signalError()
  }
  return <section className="receivingPage__panel disassembly" aria-labelledby="disassembly-title">
    {overlay}
    <div className="disassembly__header">
      <div><div className="disassembly__title"><h2 id="disassembly-title" className="receivingPage__panelTitle">РАЗБОР</h2><span className="disassembly__count">{open.length}</span></div><p className="assemblyQueue__subtitle">Возврат отобранных товаров в исходные ячейки</p></div>
    </div>
    <div className="disassembly__sectionHeading"><h3>Очередь разборов</h3></div>
    <div className="disassembly__queue"><table><thead><tr><th>Заказ / причина</th><th>Было собрано</th><th>Требуется вернуть</th><th>Уже возвращено</th><th>Статус</th><th aria-label="Действие"/></tr></thead><tbody>
      {open.map(s => {
        const quantity = s.items.filter(i => i.returned).length
        const paused = !!(s.startedAt || s.pendingItemId)
        return <tr key={s.id} className={s.status === 'active' ? 'disassembly__queueRow--active' : undefined}>
          <td><strong>{s.number}</strong><small>{reasons[s.reason] ?? 'Физический разбор'}</small></td>
          <td>{s.items.length} шт.</td><td>{s.items.length} шт.<small>Осталось {s.items.length - quantity}</small></td><td>{quantity} шт.</td>
          <td><span className={`disassembly__status disassembly__status--${s.status === 'waiting' && paused ? 'paused' : s.status}`}>{sessionStatus(s)}</span></td>
          <td><button className="receivingHistory__button" type="button" disabled={blocked || s.status === 'active' || !!active} onClick={() => void onAction(s.id, 'start')}>{s.status === 'active' ? 'Активен' : paused ? 'Продолжить' : 'Начать разбор'}</button></td>
        </tr>
      })}
      {!open.length && <tr><td colSpan={6} className="disassembly__emptyQueue">Заказов на разбор пока нет.</td></tr>}
    </tbody></table></div>
    <div className="disassembly__body">
      <div className="disassembly__left">
    <div className="disassembly__activeHeader">
      <div><h3>{active ? `Активный разбор · ${active.number}` : 'Активный разбор'}</h3><p>{active ? reasons[active.reason] ?? 'Физический разбор' : 'Начните или продолжите заказ из очереди'}</p></div>
      <div className={`disassembly__progress${done ? ' disassembly__progress--complete' : ''}`}><span>Возвращено <strong>{returned} / {active?.items.length ?? 0}</strong></span><progress value={returned} max={active?.items.length || 1} aria-label="Прогресс физического возврата"/></div>
    </div>
        <dl className="disassembly__metrics"><div><dt>Было собрано</dt><dd>{active ? active.items.length : '—'} <small>шт.</small></dd></div><div><dt>Требуется вернуть</dt><dd>{active ? active.items.length : '—'} <small>шт.</small></dd></div><div><dt>Уже возвращено</dt><dd>{active ? returned : '—'} <small>шт.</small></dd></div></dl>
        <div className="disassembly__items"><table><thead><tr><th>Товар / SKU</th><th>Возвращено / Нужно</th><th>Исходные ячейки</th></tr></thead><tbody>{groups.map(g => <tr key={g.sku} className={g.returned === g.total ? 'disassembly__row--done' : undefined}>
          <td><strong>{g.name}</strong><small>{g.warehouseSku}{g.warehouseSku !== g.sku && ` · Ozon: ${g.sku}`}</small>{!g.returnable && <small className="disassembly__error">Повреждён: зачисление запрещено</small>}</td>
          <td><span className="disassembly__productCount">{g.returned} / {g.total}{g.returned === g.total && <span className="disassembly__check" aria-label="Возврат завершён">✓</span>}</span><progress value={g.returned} max={g.total} aria-label={`Возврат ${g.name}`}/></td>
          <td><div className="disassembly__cells">{g.cells.map(c => <div key={c.cellId} className={c.returned === c.total ? 'disassembly__cell--done' : undefined}><span><strong>{c.cell} · CELL:{c.cellId}</strong><small>{c.warehouse}</small></span><span>{c.returned} / {c.total}</span></div>)}</div></td>
        </tr>)}{!active && <tr><td colSpan={3} className="disassembly__emptyItems">Товары и исходные ячейки появятся после начала разбора</td></tr>}</tbody></table></div>
      </div>
      <div className="disassembly__scanner">
        <div className={`disassembly__steps${outgoingStep !== null ? ' disassembly__steps--transition' : ''}`} aria-label="Порядок возврата">{['1 · Товар', '2 · Исходная ячейка'].map((label, index) => <span key={label} className="disassembly__stepWrap">{index > 0 && <span className="disassembly__stepArrow" aria-hidden="true">→</span>}<span className={`disassembly__step${index === step ? ' disassembly__step--current' : index === outgoingStep ? ' disassembly__step--outgoing' : ''}`} aria-current={index === step ? 'step' : undefined}><svg aria-hidden="true"><rect pathLength="1" x="1.25" y="1.25"/></svg>{label}</span></span>)}</div>
        <h3>{done ? 'ВСЕ ЕДИНИЦЫ ВОЗВРАЩЕНЫ' : pending ? 'СКАНИРУЙТЕ ЯЧЕЙКУ' : 'СКАНИРУЙТЕ ТОВАР'}</h3>
        <div className="disassembly__target"><small>Текущий товар</small><strong>{pending?.name ?? (done ? 'Все товары возвращены' : 'Определится после сканирования')}</strong><div className="disassembly__expected"><small>Ожидаемая исходная ячейка</small><span>{pending ? `${pending.cell} · CELL:${pending.cellId}` : '—'}</span>{pending && <small>{pending.warehouse}</small>}</div></div>
        {!done && <form ref={formRef} onSubmit={event => void submit(event)}><input ref={inputRef} aria-label={pending ? 'Код исходной ячейки' : 'Код возвращаемого товара'} value={scan} placeholder={pending ? 'Код исходной ячейки' : 'Код товара'} disabled={blocked || !active} onChange={event => setScan(event.target.value)} /><button type="submit" className="receivingHistory__button" disabled={blocked || !active || !scan.trim()}>Подтвердить</button></form>}
        <div className="disassembly__last"><span>Последний скан</span><strong>{active?.lastScan ?? '—'}</strong></div>
        <p role="status" className={`disassembly__log${active?.error ? ' disassembly__error' : ''}`}>{active?.log || (active ? 'Сканируйте товар из списка.' : 'Выберите заказ в очереди разборов.')}</p>
        <div className="disassembly__footer"><button type="button" className="receivingHistory__button" disabled={blocked || !active} onClick={() => { if (active) void onAction(active.id, 'pause') }}>Отложить</button><button type="button" className="receivingHistory__button disassembly__finish" disabled={blocked || !done} onClick={() => { if (active) void onAction(active.id, 'finish') }}>Завершить разбор</button></div>
      </div>
    </div>
  </section>
}
