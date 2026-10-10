import { useRef, useState, type FormEvent } from 'react'
import OrderProblemDialog, { type OrderProblemDetails } from './OrderProblemDialog'
import useScanErrorFeedback from './useScanErrorFeedback'

export type AssemblyPreviewOrder = { number: string; items: Array<{ name: string; sku: string; quantity: number; stock: number; cells: string; cellStocks: Record<string, number> }> }
export type PreviewPick = { sku: string; cell: string }
export type AssemblyProblemSnapshot = { details: OrderProblemDetails; picks: PreviewPick[]; cell: string | null; shipmentConfirmed: boolean }

export default function ActiveAssembly({ order, onComplete, onProblem }: {
  order: AssemblyPreviewOrder | null
  onComplete: () => void
  onProblem: (snapshot: AssemblyProblemSnapshot) => void
}) {
  const [picks, setPicks] = useState<PreviewPick[]>([])
  const [confirmedCell, setConfirmedCell] = useState<string | null>(null)
  const [shipmentConfirmed, setShipmentConfirmed] = useState(false)
  const [scan, setScan] = useState('')
  const [lastScan, setLastScan] = useState('—')
  const [log, setLog] = useState('')
  const [error, setError] = useState(false)
  const [problemOpen, setProblemOpen] = useState(false)
  const inputRef = useRef<HTMLInputElement>(null)
  const { signalError, overlay } = useScanErrorFeedback()
  const plans = order?.items.flatMap((item) => item.cells.split('·').flatMap((entry) => {
    const match = entry.trim().match(/^(.+?)\s*×\s*(\d+)$/)
    return match ? [{ sku: item.sku, name: item.name, cell: match[1].trim(), required: Number(match[2]), stock: item.cellStocks[match[1].trim()] ?? 0 }] : []
  })) ?? []
  const count = (sku: string, cell?: string) => picks.filter((pick) => pick.sku === sku && (!cell || pick.cell === cell)).length
  const remaining = plans.filter((plan) => count(plan.sku, plan.cell) < plan.required && plan.stock - count(plan.sku, plan.cell) > 0)
  const expectedCell = [...remaining].sort((a, b) => (a.stock - count(a.sku, a.cell)) - (b.stock - count(b.sku, b.cell)) || a.cell.localeCompare(b.cell, 'ru', { numeric: true }))[0]?.cell
  const required = order?.items.reduce((sum, item) => sum + item.quantity, 0) ?? 0
  const collected = picks.length
  const allCollected = required > 0 && collected === required
  const step = allCollected ? 'shipment' : confirmedCell ? 'product' : 'cell'
  const cellItems = plans.filter((plan) => plan.cell === confirmedCell)
  const activeSku = cellItems.find((item) => count(item.sku, item.cell) < item.required)?.sku ?? remaining.find((item) => item.cell === expectedCell)?.sku
  const progress = required ? collected / required * 100 : 0
  const heading = step === 'cell' ? 'СКАНИРУЙТЕ ЯЧЕЙКУ' : step === 'product' ? 'СКАНИРУЙТЕ ТОВАР' : 'СКАНИРУЙТЕ ОТПРАВЛЕНИЕ'

  function submitScan(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!order) return
    const code = scan.trim()
    setScan('')
    const fail = (message: string) => { setError(true); setLog(message); signalError(); inputRef.current?.focus() }
    if (step === 'cell') {
      if (code !== expectedCell) return fail(`Неверная ячейка. Ожидается ${expectedCell}.`)
      setConfirmedCell(code)
      setLog(`Ячейка ${code} подтверждена. Сканируйте любой товар из списка.`)
    } else if (step === 'product') {
      const item = cellItems.find((position) => position.sku === code.toUpperCase() && count(position.sku, position.cell) < position.required)
      if (!item) return fail('Товар не требуется из этой ячейки или уже полностью собран.')
      const nextPicks = [...picks, { sku: item.sku, cell: item.cell }]
      setPicks(nextPicks)
      const cellComplete = cellItems.every((position) => nextPicks.filter((pick) => pick.sku === position.sku && pick.cell === position.cell).length >= position.required)
      if (cellComplete) setConfirmedCell(null)
      setLog(`${item.name}: подтверждена 1 шт. из ${item.cell}.${cellComplete ? ' Подбор из ячейки завершён.' : ''}`)
    } else {
      if (code !== order.number) return fail('Неверный код отправления. Проверьте этикетку Ozon.')
      setShipmentConfirmed(true)
      setLog('Коробка подтверждена. Можно завершить сборку.')
    }
    setLastScan(code)
    setError(false)
    inputRef.current?.focus()
  }
  function undoPick() {
    const last = picks[picks.length - 1]
    if (!last) return
    setPicks(picks.slice(0, -1))
    setConfirmedCell(last.cell)
    setShipmentConfirmed(false)
    setScan('')
    setError(false)
    setLog(`Отменён последний отбор: ${last.sku}, 1 шт. из ${last.cell}.`)
    inputRef.current?.focus()
  }

  return <section className="receivingPage__panel activeAssembly" aria-labelledby="active-assembly-title">
    {overlay}
    <div className="activeAssembly__header">
      <h2 className="receivingPage__panelTitle" id="active-assembly-title">{order ? `ЗАКАЗ № ${order.number}` : 'АКТИВНАЯ СБОРКА'}</h2>
      <div className={`activeAssembly__progress${progress === 100 ? ' activeAssembly__progress--complete' : ''}`} role="progressbar" aria-label="Прогресс сборки заказа" aria-valuemin={0} aria-valuemax={required || 1} aria-valuenow={collected} aria-valuetext={`Собрано ${collected} из ${required} единиц`}>
        <div className="activeAssembly__progressFill" style={{ width: `${progress}%` }} />
      </div>
    </div>
    {!order ? <div className="activeAssembly__empty">Выберите заказ в очереди и нажмите «Начать сборку».</div> : <>
      <div className="activeAssembly__workspace">
        <div className="activeAssembly__tableScroll" role="region" aria-label="Товары активной сборки" tabIndex={0}>
          <table className="activeAssembly__table">
            <colgroup><col /><col style={{ width: '160px' }} /><col style={{ width: '160px' }} /></colgroup>
            <thead><tr><th>Товар / SKU</th><th>Нужно</th><th>Собрано</th></tr></thead>
            <tbody>{order.items.map((item) => {
              const done = count(item.sku) >= item.quantity
              return <tr key={item.sku} className={done ? 'activeAssembly__row--done' : undefined}>
                <td><strong>{item.name}</strong><span>{item.sku}</span></td><td>{item.quantity} шт.</td>
                <td><div className="activeAssembly__collectedValue">{count(item.sku)} шт.{done && <span className="activeAssembly__check" aria-label="Позиция полностью собрана">✓</span>}</div></td>
              </tr>
            })}</tbody>
          </table>
        </div>
        <div className="activeAssembly__scanPanel">
          <div className="activeAssembly__scanHeading">
            <label className="receivingPage__panelTitle" htmlFor="active-assembly-scan">{heading}</label>
            {step === 'cell' && <span className="activeAssembly__cellBadge">{expectedCell}</span>}
          </div>
          <form onSubmit={submitScan}>
            <input ref={inputRef} value={scan} onChange={(event) => setScan(event.target.value)} id="active-assembly-scan" className="receivingPage__scanInput activeAssembly__scanInput" placeholder={step === 'cell' ? 'Код ячейки' : step === 'product' ? 'Код товара' : 'Код отправления'} autoComplete="off" disabled={shipmentConfirmed} />
          </form>
          {step === 'product' && <ul className="activeAssembly__cellItems" aria-label={`Товары из ячейки ${confirmedCell}`}>
            {cellItems.map((item) => {
              const done = count(item.sku, item.cell) >= item.required
              return <li key={item.sku} className={done ? 'activeAssembly__cellItem activeAssembly__cellItem--done' : 'activeAssembly__cellItem'}>
                <div><strong>{item.name}</strong><span>{item.sku}</span></div>
                <span className="activeAssembly__cellCount">{count(item.sku, item.cell)}/{item.required}</span>
                {done && <span className="activeAssembly__check" aria-label="Позиция собрана">✓</span>}
              </li>
            })}
          </ul>}
          {step === 'shipment' && <div className={`activeAssembly__shipment${shipmentConfirmed ? ' activeAssembly__shipment--confirmed' : ''}`}>
            {shipmentConfirmed ? <><strong>✓ КОРОБКА ПОДТВЕРЖДЕНА</strong><p>Отправление: {order.number}</p></> : <p>Приклейте этикетку Ozon и отсканируйте код отправления.</p>}
          </div>}
          <div className="activeAssembly__lastScan"><h3>Последний успешный скан</h3><p>{lastScan}</p></div>
        </div>
      </div>
      <div className="activeAssembly__footer">
        <div className={`activeAssembly__scanLog${error ? ' activeAssembly__scanLog--error' : ''}`} role="status">{log}</div>
        <div className="activeAssembly__actions">
          <button className="receivingPage__undoButton" type="button" disabled={picks.length === 0} onClick={undoPick}>Отмена</button>
          <button className="assemblyQueue__problem" type="button" onClick={() => setProblemOpen(true)}>Проблема</button>
          <button className="receivingPage__finishButton assemblyQueue__start" type="button" disabled={!shipmentConfirmed || !allCollected} onClick={onComplete}>Завершить сборку</button>
        </div>
      </div>
      {problemOpen && <OrderProblemDialog positions={order.items} initialPositionSku={activeSku} onCancel={() => setProblemOpen(false)} onConfirm={(details) => { setProblemOpen(false); onProblem({ details, picks, cell: confirmedCell, shipmentConfirmed }) }} />}
    </>}
  </section>
}
