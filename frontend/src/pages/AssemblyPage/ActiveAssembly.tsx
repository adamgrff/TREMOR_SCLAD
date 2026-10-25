import { useEffect, useLayoutEffect, useRef, useState, type FormEvent } from 'react'
import OrderProblemDialog, { type OrderProblemDetails } from './OrderProblemDialog'
import useScanErrorFeedback from './useScanErrorFeedback'

export type WarehousePlanEntry = { cellId: number; cell: string; warehouse: string; scanCode: string; required: number; picked: number; available: number }
export type AssemblyPreviewOrder = { source?: 'ozon' | 'preview'; number: string; suggestedCellId?: number | null; items: Array<{ name: string; sku: string; warehouseSku?: string; ozonSku?: string; productId?: number; quantity: number; stock: number; cells: string; cellStocks: Record<string, number>; pickingPlan?: WarehousePlanEntry[] }> }
export type PreviewPick = { sku: string; cell: string; cellId?: number; operationId?: string; pickId?: string; occurredAt?: string }
export type AssemblyProblemSnapshot = { details: OrderProblemDetails; picks: PreviewPick[]; cell: string | null; shipmentConfirmed: boolean }
export type ActiveAssemblyState = { picks: PreviewPick[]; confirmedCell: string | null; confirmedCellId?: number | null; shipmentConfirmed: boolean; lastScan: string; log: string; error: boolean }


// Presentation of existing scan states only; no scan state or transition timers are introduced.
function AssemblyStepIndicator({ step, shipmentConfirmed }: { step: 'cell' | 'product' | 'shipment'; shipmentConfirmed: boolean }) {
  const activeIndex = shipmentConfirmed ? 3 : step === 'cell' ? 0 : step === 'product' ? 1 : 2
  const row = useRef<HTMLDivElement>(null)
  const capsules = useRef<Array<HTMLSpanElement | null>>([])
  const contours = useRef<Array<SVGRectElement | null>>([])
  const previous = useRef(activeIndex)
  const [widths, setWidths] = useState([0, 0, 0])
  useLayoutEffect(() => {
    let disposed = false
    function measure() {
      if (disposed || !row.current || !row.current.clientWidth) return
      const scale = parseFloat(getComputedStyle(document.documentElement).zoom) || 1
      const next = capsules.current.map(capsule => (capsule?.getBoundingClientRect().width ?? 0) / scale)
      setWidths(current => next.every((width, index) => Math.abs(width - current[index]) < .05) ? current : next)
      // Reveal only within this horizontal strip, without moving focus or the page.
      const current = capsules.current[Math.min(activeIndex, 2)]
      if (!current) return
      const left = current.offsetLeft, right = left + current.offsetWidth
      if (left < row.current.scrollLeft) row.current.scrollLeft = left
      else if (right > row.current.scrollLeft + row.current.clientWidth) row.current.scrollLeft = right - row.current.clientWidth
    }
    const resize = new ResizeObserver(measure)
    if (row.current) resize.observe(row.current)
    for (const capsule of capsules.current) if (capsule) resize.observe(capsule)
    measure()
    void document.fonts.ready.then(measure)
    return () => { disposed = true; resize.disconnect() }
  }, [activeIndex])
  useLayoutEffect(() => {
    const changed = previous.current !== activeIndex
    previous.current = activeIndex
    const motion = matchMedia('(prefers-reduced-motion: reduce)')
    if (!changed || document.documentElement.dataset.theme !== 'dark' || motion.matches) return
    const contour = contours.current[activeIndex]
    if (!contour) return
    const animation = contour.animate([{ strokeDashoffset: 1 }, { strokeDashoffset: 0 }], { duration: 300, easing: 'cubic-bezier(.2,0,0,1)' })
    const reduce = () => { if (motion.matches) animation.cancel() }
    motion.addEventListener('change', reduce)
    return () => { motion.removeEventListener('change', reduce); animation.cancel() }
  }, [activeIndex])
  const labels = ['1 · Ячейка', '2 · Товар', '3 · Отправление']
  return <div ref={row} className="ds-v1 ui-v1-steps" role="list" aria-label="Этапы сканирования" tabIndex={0}>
    {labels.map((label, index) => {
      const status = index < activeIndex ? 'completed' : index === activeIndex ? 'active' : 'future'
      return <span className="ui-v1-steps__group" key={label}>
        <span ref={element => { capsules.current[index] = element }} className="ui-v1-steps__capsule" role="listitem" data-state={status} aria-current={status === 'active' ? 'step' : undefined} aria-label={`${label}: ${status === 'active' ? 'текущий этап' : status === 'completed' ? 'пройден' : 'предстоит'}`}>
          <svg className="ui-v1-steps__contour" aria-hidden="true" width={widths[index]} height="46">
            <rect className="ui-v1-steps__gray" x="1" y="1" width={Math.max(0, widths[index] - 2)} height="44" rx="22" />
            <rect ref={element => { contours.current[index] = element }} className="ui-v1-steps__blue" x="1" y="1" width={Math.max(0, widths[index] - 2)} height="44" rx="22" pathLength="1" strokeDasharray="1" />
          </svg>
          <span>{label}</span>
        </span>
        {index < 2 && <span className="ui-v1-steps__connector" data-completed={index < activeIndex} aria-hidden="true">
          <svg className="ui-v1-steps__arrow" width="20" height="20" viewBox="0 0 20 20"><path d="M4 10h12m-5-5 5 5-5 5" /></svg>
          <span className="ui-v1-steps__dot" />
        </span>}
      </span>
    })}
  </div>
}

export default function ActiveAssembly({ order, state, onChange, onComplete, onProblem, onServerAction, blocked, busy = false }: {
  order: AssemblyPreviewOrder | null
  state: ActiveAssemblyState
  onChange: (state: ActiveAssemblyState) => Promise<boolean>
  blocked: boolean
  busy?: boolean
  onComplete: () => void
  onProblem: (details: OrderProblemDetails) => Promise<boolean>
  onServerAction?: (action: 'scan' | 'undo', code?: string) => Promise<boolean>
}) {
  const { picks, confirmedCell, shipmentConfirmed, lastScan, log, error } = state
  const [scan, setScan] = useState('')
  const [problemOpen, setProblemOpen] = useState(false)
  const inputRef = useRef<HTMLInputElement>(null)
  const restoreScanFocus = useRef(false)
  useEffect(() => {
    if (blocked || !restoreScanFocus.current) return
    restoreScanFocus.current = false
    if (!shipmentConfirmed && (document.activeElement === document.body || document.activeElement === inputRef.current)) inputRef.current?.focus({ preventScroll: true })
  }, [blocked, shipmentConfirmed])
  async function saveScanState(next: ActiveAssemblyState) {
    restoreScanFocus.current = true
    await onChange(next)
  }
  const { signalError, overlay } = useScanErrorFeedback()
  const plans = order?.items.flatMap((item) => item.cells.split('·').flatMap((entry) => {
    const match = entry.trim().match(/^(.+?)\s*×\s*(\d+)$/)
    return match ? [{ sku: item.sku, name: item.name, cell: match[1].trim(), required: Number(match[2]), stock: item.cellStocks[match[1].trim()] ?? 0 }] : []
  })) ?? []
  const realPlans = order?.items.flatMap(item => (item.pickingPlan ?? []).map(plan => ({ ...plan, sku: item.sku, warehouseSku: item.warehouseSku, name: item.name, stock: plan.available + plan.picked }))) ?? []
  const effectivePlans = order?.source === 'ozon' ? realPlans : plans.map(plan => ({ ...plan, cellId: undefined, scanCode: plan.cell, warehouse: '' }))
  const count = (sku: string, cell?: string, cellId?: number) => picks.filter((pick) => pick.sku === sku && (cellId !== undefined ? pick.cellId === cellId : !cell || pick.cell === cell)).length
  const remaining = effectivePlans.filter((plan) => count(plan.sku, plan.cell, plan.cellId) < plan.required && plan.stock - count(plan.sku, plan.cell, plan.cellId) > 0)
  const fallbackPlan = [...remaining].sort((a, b) => (a.stock - count(a.sku, a.cell, a.cellId)) - (b.stock - count(b.sku, b.cell, b.cellId)) || a.cell.localeCompare(b.cell) || (a.cellId ?? 0) - (b.cellId ?? 0))[0]
  const expectedPlan = order?.source === 'ozon' ? remaining.find(plan => plan.cellId === order.suggestedCellId) ?? fallbackPlan : fallbackPlan
  const expectedCell = expectedPlan?.cell
  const required = order?.items.reduce((sum, item) => sum + item.quantity, 0) ?? 0
  const collected = picks.length
  const allCollected = required > 0 && collected === required
  const step = allCollected ? 'shipment' : confirmedCell ? 'product' : 'cell'
  const cellItems = effectivePlans.filter((plan) => order?.source === 'ozon' ? plan.cellId === state.confirmedCellId : plan.cell === confirmedCell)
  const activeSku = cellItems.find((item) => count(item.sku, item.cell, item.cellId) < item.required)?.sku ?? expectedPlan?.sku
  const progress = required ? collected / required * 100 : 0
  const heading = step === 'cell' ? 'СКАНИРУЙТЕ ЯЧЕЙКУ' : step === 'product' ? 'СКАНИРУЙТЕ ТОВАР' : 'СКАНИРУЙТЕ ОТПРАВЛЕНИЕ'

  async function submitScan(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!order || blocked) return
    const code = scan.trim()
    setScan('')
    if (onServerAction) {
      restoreScanFocus.current = true
      const success = await onServerAction('scan', code)
      if (!success) signalError()
      return
    }
    const next = { ...state, lastScan: code, error: false }
    const fail = async (message: string) => { signalError(); await saveScanState({ ...state, error: true, log: message }) }
    if (step === 'cell') {
      if (code !== expectedCell) return fail(`Неверная ячейка. Ожидается ${expectedCell}.`)
      next.confirmedCell = code
      next.log = `Ячейка ${code} подтверждена. Сканируйте любой товар из списка.`
    } else if (step === 'product') {
      const item = cellItems.find((position) => position.sku === code.toUpperCase() && count(position.sku, position.cell) < position.required)
      if (!item) return fail('Товар не требуется из этой ячейки или уже полностью собран.')
      const nextPicks = [...picks, { sku: item.sku, cell: item.cell }]
      next.picks = nextPicks
      const cellComplete = cellItems.every((position) => nextPicks.filter((pick) => pick.sku === position.sku && pick.cell === position.cell).length >= position.required)
      if (cellComplete) next.confirmedCell = null
      next.log = `${item.name}: подтверждена 1 шт. из ${item.cell}.${cellComplete ? ' Подбор из ячейки завершён.' : ''}`
    } else {
      if (code !== order.number) return fail('Неверный код отправления. Проверьте этикетку Ozon.')
      next.shipmentConfirmed = true
      next.log = 'Коробка подтверждена. Можно завершить сборку.'
    }
    await saveScanState(next)
  }
  async function undoPick() {
    const last = picks[picks.length - 1]
    if (!last || blocked) return
    setScan('')
    if (onServerAction) { await onServerAction('undo'); return }
    await saveScanState({ ...state, picks: picks.slice(0, -1), confirmedCell: last.cell, shipmentConfirmed: false, error: false, log: `Отменён последний отбор: ${last.sku}, 1 шт. из ${last.cell}.` })
  }

  return <section className="receivingPage__panel activeAssembly ds-v1 ui-v1-window" aria-labelledby="active-assembly-title">
    {overlay}
    <div className="activeAssembly__header">
      <h2 className={`receivingPage__panelTitle ds-v1 ${order ? 'ui-v1-type-column' : 'ui-v1-page-title'}`} id="active-assembly-title">{order ? `ЗАКАЗ № ${order.number}` : 'АКТИВНАЯ СБОРКА'}</h2>
      <div className={`activeAssembly__progress ds-v1 ui-v1-progress${progress === 100 ? ' activeAssembly__progress--complete ui-v1-progress--complete' : ''}`} role="progressbar" aria-label="Прогресс сборки заказа" aria-valuemin={0} aria-valuemax={required || 1} aria-valuenow={collected} aria-valuetext={`Собрано ${collected} из ${required} единиц`}>
        <div className="activeAssembly__progressFill ui-v1-progress__fill" style={{ width: `${progress}%` }} />
      </div>
    </div>
    {!order ? <div className="activeAssembly__empty ds-v1 ui-v1-feedback ui-v1-feedback--empty">Выберите заказ в очереди и нажмите «Начать сборку».</div> : <>
      <div className="activeAssembly__workspace">
        <div className="activeAssembly__tableScroll" role="region" aria-label="Товары активной сборки" tabIndex={0}>
          <table className="activeAssembly__table ds-v1 ui-v1-table">
            <colgroup><col /><col style={{ width: '160px' }} /><col style={{ width: '160px' }} /></colgroup>
            <thead><tr><th>Товар / SKU</th><th>Нужно</th><th>Собрано</th></tr></thead>
            <tbody>{order.items.map((item) => {
              const done = count(item.sku) >= item.quantity
              return <tr key={item.sku} className={done ? 'activeAssembly__row--done' : undefined}>
                <td><strong>{item.name}</strong><span>{item.warehouseSku && item.warehouseSku !== item.sku ? `${item.warehouseSku} · Ozon: ${item.sku}` : item.sku}</span></td><td>{item.quantity} шт.</td>
                <td><div className="activeAssembly__collectedValue">{count(item.sku)} шт.{done && <span className="activeAssembly__check" aria-label="Позиция полностью собрана">✓</span>}</div></td>
              </tr>
            })}</tbody>
          </table>
        </div>
        <div className="activeAssembly__scanPanel ds-v1 ui-v1-control-panel">
          <AssemblyStepIndicator step={step} shipmentConfirmed={shipmentConfirmed} />
          <div className="activeAssembly__scanHeading">
            <h3 className="receivingPage__panelTitle ds-v1 ui-v1-type-section" id="active-assembly-scan-heading">{heading}</h3>
            {step === 'cell' && <span className="activeAssembly__cellBadge" title={expectedCell || undefined}>{expectedCell}</span>}
          </div>
          <form onSubmit={(event) => void submitScan(event)}>
            <input ref={inputRef} value={scan} onChange={(event) => setScan(event.target.value)} id="active-assembly-scan" aria-labelledby="active-assembly-scan-heading" className="receivingPage__scanInput activeAssembly__scanInput ds-v1 ui-v1-input" placeholder={step === 'cell' ? 'Код ячейки' : step === 'product' ? 'Код товара' : 'Код отправления'} autoComplete="off" disabled={shipmentConfirmed || blocked} />
          </form>
          {step === 'product' && <ul className="activeAssembly__cellItems" aria-label={`Товары из ячейки ${confirmedCell}`}>
            {cellItems.map((item) => {
              const done = count(item.sku, item.cell, item.cellId) >= item.required
              return <li key={item.sku} className={done ? 'activeAssembly__cellItem activeAssembly__cellItem--done' : 'activeAssembly__cellItem'}>
                <div><strong className="ds-v1 ui-v1-type-primary ui-v1-type-semibold">{item.name}</strong><span className="ds-v1 ui-v1-type-secondary">{item.sku}</span></div>
                <span className="activeAssembly__cellCount">{count(item.sku, item.cell, item.cellId)}/{item.required}</span>
                {done && <span className="activeAssembly__check" aria-label="Позиция собрана">✓</span>}
              </li>
            })}
          </ul>}
          {step === 'shipment' && <div className={`activeAssembly__shipment${shipmentConfirmed ? ' activeAssembly__shipment--confirmed' : ''}`}>
            {shipmentConfirmed ? <><strong className="ds-v1 ui-v1-feedback ui-v1-feedback--success">✓ КОРОБКА ПОДТВЕРЖДЕНА</strong><p className="ds-v1 ui-v1-type-secondary">Отправление: {order.number}</p></> : <p className="ds-v1 ui-v1-type-primary">Приклейте этикетку Ozon и отсканируйте код отправления.</p>}
          </div>}
          <div className="activeAssembly__lastScan"><h3 className="ds-v1 ui-v1-type-secondary">Последний успешный скан</h3><p className="ds-v1 ui-v1-type-primary ui-v1-type-semibold">{lastScan}</p></div>
        </div>
      </div>
      <div className="activeAssembly__footer">
        <div className={`activeAssembly__scanLog ds-v1 ui-v1-feedback${error ? ' activeAssembly__scanLog--error ui-v1-feedback--error' : ''}`} role="status">{log}</div>
        <div className="activeAssembly__actions">
          <button className="receivingPage__undoButton ds-v1 ui-v1-action ui-v1-action--negative" aria-busy={busy} type="button" disabled={picks.length === 0 || blocked} onClick={() => void undoPick()}>Отмена</button>
          <button className="assemblyQueue__problem ds-v1 ui-v1-action ui-v1-action--negative" type="button" aria-busy={busy} disabled={blocked} onClick={() => setProblemOpen(true)}>Проблема</button>
          <button className="receivingPage__finishButton assemblyQueue__start ds-v1 ui-v1-action ui-v1-action--primary" type="button" aria-busy={busy} disabled={!shipmentConfirmed || !allCollected || blocked} onClick={onComplete}>Завершить сборку</button>
        </div>
      </div>
      {problemOpen && <OrderProblemDialog blocked={blocked} positions={order.items} initialPositionSku={activeSku ?? order.items[0]?.sku} onCancel={() => setProblemOpen(false)} onConfirm={(details) => { void onProblem(details).then((saved) => { if (saved) setProblemOpen(false) }) }} />}
    </>}
  </section>
}
