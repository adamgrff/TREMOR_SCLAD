import { useEffect, useRef, useState, type FormEvent } from 'react'
import useScanErrorFeedback from './useScanErrorFeedback'
import './Disassembly.css'

export type DisassemblyItem = { id: string; sku: string; warehouseSku: string; name: string; cell: string; cellId: number; warehouse: string; operationId: string; returned: boolean; returnable: boolean; returnedAt?: string }
export type DisassemblySession = { id: string; number: string; reason: string; status: 'waiting' | 'active' | 'completed'; startedAt?: string | null; items: DisassemblyItem[]; pendingItemId: string | null; lastScan: string; log: string; error: boolean }
const reasons: Record<string, string> = { cancelled_order: 'Отмена заказа', damaged_product: 'Повреждённый товар', invalid_order: 'Ошибка данных заказа', other: 'Физический разбор' }
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
  useEffect(() => {
    if (blocked || !restoreFocus.current) return
    restoreFocus.current = false
    if (!done && (document.activeElement === document.body || formRef.current?.contains(document.activeElement))) inputRef.current?.focus({ preventScroll: true })
  }, [blocked, done, active?.pendingItemId])
  const groups = active?.items.reduce<Array<{ key: string; name: string; sku: string; warehouseSku: string; cell: string; cellId: number; warehouse: string; total: number; returned: number; returnable: boolean }>>((result, item) => {
    const key = `${item.sku}:${item.cellId}`
    let group = result.find(g => g.key === key)
    if (!group) { group = { key, name: item.name, sku: item.sku, warehouseSku: item.warehouseSku, cell: item.cell, cellId: item.cellId, warehouse: item.warehouse, total: 0, returned: 0, returnable: true }; result.push(group) }
    group.total++; if (item.returned) group.returned++; group.returnable &&= item.returnable
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
    <div className="disassembly__header"><h2 id="disassembly-title" className="receivingPage__panelTitle">РАЗБОР{active ? ` · ${active.number}` : ''}</h2>{active && <div className="disassembly__progress"><progress value={returned} max={active.items.length} aria-label="Прогресс физического возврата"/><span>{returned} / {active.items.length} шт.</span></div>}</div>
    <p className="assemblyQueue__subtitle">Передача и запуск не меняют остатки. Возврат одной единицы — после сканов товара и исходной ячейки.</p>
    {open.length > 0 && <div className="disassembly__queue"><table><thead><tr><th>Заказ / причина</th><th>Возвращено / Всего</th><th>Состояние</th><th/></tr></thead><tbody>{open.map(s => <tr key={s.id}><td><strong>{s.number}</strong><small>{reasons[s.reason] ?? 'Физический разбор'}</small></td><td>{s.items.filter(i => i.returned).length} / {s.items.length} шт.</td><td>{s.status === 'active' ? 'В разборе' : 'Ожидает разбора'}</td><td><button className="receivingHistory__button" type="button" disabled={blocked || s.status === 'active' || !!active} onClick={() => void onAction(s.id, 'start')}>{s.status === 'active' ? 'Активный разбор' : s.startedAt || s.pendingItemId ? 'Продолжить разбор' : 'Начать разбор'}</button></td></tr>)}</tbody></table></div>}
    {active ? <div className="disassembly__body">
      <div className="disassembly__items"><table><thead><tr><th>Товар / SKU</th><th>Исходная ячейка</th><th>Возвращено / Всего</th></tr></thead><tbody>{groups.map(g => <tr key={g.key} className={g.returned === g.total ? 'activeAssembly__row--done' : undefined}><td><strong>{g.name}</strong><small>{g.warehouseSku}{g.warehouseSku !== g.sku && ` · Ozon: ${g.sku}`}</small>{!g.returnable && <small className="disassembly__error">Повреждён: зачисление запрещено</small>}</td><td><strong>{g.cell} · CELL:{g.cellId}</strong><small>{g.warehouse}</small></td><td>{g.returned} / {g.total}{g.returned === g.total && <span className="activeAssembly__check" aria-label="Возврат завершён"> ✓</span>}</td></tr>)}</tbody></table></div>
      <div className="disassembly__scanner"><h3>{done ? 'ВСЕ ЕДИНИЦЫ ВОЗВРАЩЕНЫ' : pending ? 'СКАНИРУЙТЕ ЯЧЕЙКУ' : 'СКАНИРУЙТЕ ТОВАР'}</h3>
        {pending && <p className="disassembly__target"><strong>{pending.name}</strong><span>{pending.cell} · CELL:{pending.cellId}</span><small>{pending.warehouse}</small></p>}
        {!done && <form ref={formRef} onSubmit={event => void submit(event)}><input ref={inputRef} aria-label={pending ? 'Код исходной ячейки' : 'Код возвращаемого товара'} value={scan} placeholder={pending ? 'Код исходной ячейки' : 'Код товара'} disabled={blocked} onChange={event => setScan(event.target.value)} /><button type="submit" className="receivingHistory__button" disabled={blocked || !scan.trim()}>Подтвердить</button></form>}
        <p className="disassembly__last">Последний скан: <strong>{active.lastScan}</strong></p>
        <div className="disassembly__footer"><p role="status" className={active.error ? 'disassembly__error' : ''}>{active.log || 'Сканируйте товар из списка.'}</p><button type="button" className="receivingHistory__button" disabled={blocked} onClick={() => void onAction(active.id, 'pause')}>Отложить</button><button type="button" className="receivingHistory__button" disabled={blocked || !done} onClick={() => void onAction(active.id, 'finish')}>Завершить разбор</button></div>
      </div>
    </div> : !open.length && <p className="problemOrders__empty">Заказов на разбор пока нет.</p>}
  </section>
}
