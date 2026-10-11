import { emptyAssemblyState } from './assemblyState'
import { useEffect, useRef, useState, type FormEvent } from 'react'
import Header from '../../components/Header/Header'
import ActiveAssembly, { type ActiveAssemblyState, type AssemblyProblemSnapshot, type AssemblyPreviewOrder } from './ActiveAssembly'
import useAssemblyDraft from './useAssemblyDraft'
import checkProblemStock from './checkProblemStock'
import createProblemHistory from './createProblemHistory'
import ProblemOrders from './ProblemOrders'
import OrderProblemDialog, { type OrderProblemDetails, type SavedOrderProblem } from './OrderProblemDialog'
import { useTheme } from '../../hooks/useTheme'
import '../ReceivingPage/ReceivingPage.css'
import '../ReceivingPage/ReceivingHistory.css'
import './AssemblyPage.css'

const previewOrders: Array<AssemblyPreviewOrder & { time: string; createdAt: string; status: string }> = [
  { number: '024831-0001', time: '10:24', createdAt: '2026-10-07T10:24:00+07:00', status: 'Готов к сборке', items: [
    { name: 'TREMOR Classic', sku: 'TR-CLASSIC', quantity: 3, stock: 18, cellStocks: { A1: 1, C2: 17 }, cells: 'A1 ×1 · C2 ×2' },
    { name: 'TREMOR Black', sku: 'TR-BLACK', quantity: 2, stock: 12, cellStocks: { A1: 4, B3: 8 }, cells: 'A1 ×1 · B3 ×1' },
    { name: 'TREMOR White', sku: 'TR-WHITE', quantity: 1, stock: 8, cellStocks: { A4: 8 }, cells: 'A4 ×1' },
  ] },
  { number: '024832-0001', time: '10:31', createdAt: '2026-10-07T10:31:00+07:00', status: 'Готов к сборке', items: [
    { name: 'TREMOR Black', sku: 'TR-BLACK', quantity: 2, stock: 12, cellStocks: { A1: 4, B3: 8 }, cells: 'A1 ×1 · B3 ×1' },
    { name: 'TREMOR Classic', sku: 'TR-CLASSIC', quantity: 1, stock: 18, cellStocks: { A1: 1, C2: 17 }, cells: 'A1 ×1' },
  ] },
  { number: '024833-0001', time: '10:45', createdAt: '2026-10-07T10:45:00+07:00', status: 'Готов к сборке', items: [
    { name: 'TREMOR White', sku: 'TR-WHITE', quantity: 4, stock: 2, cellStocks: { A4: 2 }, cells: 'A4 ×2' },
  ] },
]

type PreviewOrder = typeof previewOrders[number]
type AssemblyWorkspace = {
  schemaVersion: 1
  activeOrder: PreviewOrder | null
  activeState: ActiveAssemblyState
  problemOrders: Array<{ problemId?: string; order: PreviewOrder; problem: SavedOrderProblem; status: 'problem'; assembly?: AssemblyProblemSnapshot }>
  completedOrders: PreviewOrder[]
}
const initialWorkspace: AssemblyWorkspace = { schemaVersion: 1, activeOrder: null, activeState: emptyAssemblyState, problemOrders: [], completedOrders: [] }

export default function AssemblyPage() {
  const { theme, toggleTheme } = useTheme('dark')
  const [selectedNumber, setSelectedNumber] = useState(previewOrders[0].number)
  const draft = useAssemblyDraft(initialWorkspace)
  const { activeOrder, activeState, problemOrders, completedOrders } = draft.state
  const [problemDialogOpen, setProblemDialogOpen] = useState(false)
  const [checkingStock, setCheckingStock] = useState(false)
  const stockCheckRef = useRef(false)
  const queueOrders = previewOrders.filter((order) => order.number !== activeOrder?.number && !completedOrders.some((completed) => completed.number === order.number) && !problemOrders.some((problemOrder) => problemOrder.order.number === order.number && problemOrder.problem.resolutionStatus !== 'returned'))
  const [currentTime, setCurrentTime] = useState(() => Date.now())
  useEffect(() => {
    const updateTime = () => setCurrentTime(Date.now())
    const timer = window.setInterval(updateTime, 1000)
    document.addEventListener('visibilitychange', updateTime)
    return () => {
      window.clearInterval(timer)
      document.removeEventListener('visibilitychange', updateTime)
    }
  }, [])
  const [searchOpen, setSearchOpen] = useState(false)
  const [searchQuery, setSearchQuery] = useState('')
  const [searchMessage, setSearchMessage] = useState('')
  const searchRef = useRef<HTMLFormElement>(null)
  const searchInputRef = useRef<HTMLInputElement>(null)
  const ordersScrollRef = useRef<HTMLDivElement>(null)
  const orderRowsRef = useRef(new Map<string, HTMLTableRowElement>())

  useEffect(() => {
    if (!searchOpen) return
    searchInputRef.current?.focus()
    searchInputRef.current?.select()
    function closeOutside(event: PointerEvent) {
      if (event.target instanceof Node && !searchRef.current?.contains(event.target)) setSearchOpen(false)
    }
    document.addEventListener('pointerdown', closeOutside)
    return () => document.removeEventListener('pointerdown', closeOutside)
  }, [searchOpen])

  function searchOrder(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!searchOpen) {
      setSearchOpen(true)
      setSearchMessage('')
      return
    }
    const found = queueOrders.find((order) => order.number === searchQuery.trim())
    if (!found) {
      setSearchMessage(searchQuery.trim() ? 'Заказ не найден' : 'Введите номер заказа')
      return
    }
    setSearchMessage('')
    setSelectedNumber(found.number)
    const row = orderRowsRef.current.get(found.number)
    const container = ordersScrollRef.current
    if (row && container) {
      const offset = row.getBoundingClientRect().top - container.getBoundingClientRect().top
      container.scrollTo({ top: container.scrollTop + offset, behavior: window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'instant' : 'smooth' })
    }
  }
  const selected = queueOrders.find((order) => order.number === selectedNumber) ?? queueOrders[0]
  const total = selected?.items.reduce((sum, item) => sum + item.quantity, 0) ?? 0
  const shortageItems = selected?.items.filter((item) => item.stock < item.quantity) ?? []
  const firstShortage = shortageItems[0]
  async function confirmProblem(problem: OrderProblemDetails) {
    if (!selected || draft.blocked) return
    const createdAt = new Date().toISOString()
    const savedProblem: SavedOrderProblem = {
      ...problem,
      createdAt,
      history: createProblemHistory(selected, problem, createdAt),
      resolutionStatus: 'awaiting',
      shortages: shortageItems.map((item) => ({ sku: item.sku, name: item.name, required: item.quantity, available: item.stock, missing: item.quantity - item.stock, cells: item.cells })),
    }
    const saved = await draft.save({ ...draft.state, problemOrders: [...problemOrders, { order: selected, problem: savedProblem, status: 'problem' }] })
    if (saved) {
      setSelectedNumber(queueOrders.find((order) => order.number !== selected.number)?.number ?? '')
      setProblemDialogOpen(false)
    }
  }

  async function returnToAssembly(number: string) {
    if (draft.blocked || activeOrder || stockCheckRef.current) return false
    const related = problemOrders.filter((item) => item.order.number === number && item.problem.resolutionStatus !== 'returned')
    const entry = related.find((item) => item.assembly) ?? related[0]
    if (!entry || related.some((item) => item.problem.resolutionStatus !== 'resolved' || ['cancelled_order', 'ozon_error'].includes(item.problem.type) || item.problem.requiresDisassembly) || completedOrders.some((order) => order.number === number)) return false
    const restored: ActiveAssemblyState = entry.assembly ? {
      picks: entry.assembly.picks,
      confirmedCell: null,
      shipmentConfirmed: entry.assembly.shipmentConfirmed,
      lastScan: '—',
      log: 'Заказ возвращён в сборку после решения проблемы. Сохранённый прогресс восстановлен.',
      error: false,
    } : { ...emptyAssemblyState }
    const returnedAt = new Date().toISOString()
    return draft.save({
      ...draft.state,
      activeOrder: entry.order,
      activeState: restored,
      problemOrders: problemOrders.map((item) => related.includes(item) ? { ...item, problem: { ...item.problem, resolutionStatus: 'returned', returnedAt, history: [...(item.problem.history ?? createProblemHistory(item.order, item.problem, item.problem.createdAt, item.assembly)), { createdAt: returnedAt, kind: 'return', title: 'Заказ возвращён в сборку', message: 'Сохранённый прогресс восстановлен.', outcome: 'success' }] } } : item),
    })
  }

  async function refreshProblemStock(number: string, problemId?: string): Promise<string> {
    if (draft.blocked || stockCheckRef.current) return 'Дождитесь завершения сохранения.'
    const entry = problemOrders.find((item) => item.order.number === number && item.problem.type === 'insufficient_stock' && item.problem.resolutionStatus !== 'returned' && (!problemId || item.problemId === problemId))
    if (!entry || entry.problem.type !== 'insufficient_stock') return 'Проверка остатков не устраняет этот тип проблемы.'
    stockCheckRef.current = true
    setCheckingStock(true)
    try {
      const history = entry.problem.history?.some((event) => event.kind === 'creation') ? entry.problem.history : [...createProblemHistory(entry.order, entry.problem, entry.problem.createdAt, entry.assembly), ...(entry.problem.history ?? [])]
      const result = await checkProblemStock(entry.order, { ...entry.problem, history }, entry.assembly)
      const saved = await draft.save({ ...draft.state, problemOrders: problemOrders.map((item) => item === entry ? {
        ...item,
        order: { ...entry.order, items: result.order.items },
        problem: result.problem,
        ...(entry.assembly && result.problem.resolutionStatus === 'resolved' ? { assembly: { ...entry.assembly, cell: null } } : {}),
      } : item) })
      return saved ? `${number}: ${result.message}` : 'Результат проверки не сохранён. Нажмите «Повторить» в сообщении сохранения.'
    } finally {
      stockCheckRef.current = false
      setCheckingStock(false)
    }
  }

  return (
    <main className={`receivingPage receivingPage--${theme} assemblyPage`}>
      <Header theme={theme} onToggleTheme={toggleTheme} />
      <div className="receivingPage__content">
        {(!draft.ready || draft.saving || draft.error) && <div className="assemblyPersistenceStatus" role="status">
          {draft.error || (draft.saving ? 'Сохранение сборки…' : 'Восстановление сборки…')}
          {draft.error && <button type="button" className="receivingHistory__button" onClick={() => void draft.retry()}>Повторить</button>}
        </div>}
        <section className="receivingPage__panel assemblyQueue" aria-label="Очередь заказов">
          <div className="assemblyQueue__list">
            <div className="assemblyQueue__heading">
              <h1 className="receivingPage__panelTitle">ОЧЕРЕДЬ ЗАКАЗОВ</h1>
              <div className="assemblyQueue__listActions">
                <form ref={searchRef} className={`assemblyQueue__search${searchOpen ? ' assemblyQueue__search--open' : ''}`} onSubmit={searchOrder}>
                  <div className="assemblyQueue__searchField">
                    <div className="assemblyQueue__searchInputClip">
                    <input ref={searchInputRef} aria-label="Номер заказа для поиска" placeholder="Номер заказа" value={searchQuery} tabIndex={searchOpen ? 0 : -1} onChange={(event) => { setSearchQuery(event.target.value); setSearchMessage('') }} onKeyDown={(event) => { if (event.key === 'Escape') { setSearchOpen(false); searchRef.current?.querySelector('button')?.focus() } }} />
                    </div>
                    {searchOpen && <span className="assemblyQueue__searchMessage" role="status">{searchMessage}</span>}
                  </div>
                  <button className="receivingHistory__button receivingHistory__iconButton assemblyQueue__searchButton" type="submit" aria-label="Поиск по номеру заказа" title="Поиск по номеру заказа" aria-expanded={searchOpen}>
                    <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" aria-hidden="true"><circle cx="10.5" cy="10.5" r="6.5" /><path d="m16 16 5 5" /></svg>
                  </button>
                </form>
                <button className="receivingHistory__button receivingHistory__iconButton" type="button" aria-label="Обновить заказы" title="Обновить заказы" aria-disabled="true">
                  <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                    <path d="M20 7v5h-5M20 12a8 8 0 1 0-2.3 5.7" />
                  </svg>
                </button>
                <span className="assemblyQueue__count">{queueOrders.length}</span>
              </div>
            </div>
            <p className="assemblyQueue__subtitle">Выберите заказ для сборки</p>
            <div ref={ordersScrollRef} className="assemblyQueue__ordersScroll" role="region" aria-label="Список заказов" tabIndex={0}>
              <table className="assemblyQueue__orders">
              <colgroup><col className="assemblyQueue__orderColumn" /><col className="assemblyQueue__timeColumn" /><col className="assemblyQueue__quantityColumn" /></colgroup>
              <thead><tr><th>Заказ / статус</th><th>Время / дата</th><th>Поз. / шт.</th></tr></thead>
              <tbody>{queueOrders.map((order) => {
                const minutes = Math.max(0, Math.floor((currentTime - Date.parse(order.createdAt)) / 60000))
                const waitLevel = minutes <= 10 ? 'green' : minutes <= 20 ? 'yellow' : minutes <= 30 ? 'orange' : 'red'
                return (
                <tr key={order.number} ref={(row) => { if (row) orderRowsRef.current.set(order.number, row); else orderRowsRef.current.delete(order.number) }} className={order.number === selected.number ? 'assemblyQueue__selected' : ''} onClick={() => setSelectedNumber(order.number)}>
                  <td><button type="button" aria-pressed={order.number === selected.number} onClick={() => setSelectedNumber(order.number)}>
                    <strong>{order.number}</strong><span>{order.status}</span>
                  </button></td>
                  <td><span className="assemblyQueue__time"><span>{order.time}</span><span className={`assemblyQueue__wait assemblyQueue__wait--${waitLevel}`} aria-label={`Ожидание: ${minutes} мин.`} title={`С момента поступления: ${minutes} мин.`}>{minutes}</span></span><span className="assemblyQueue__date">07.10.2026</span></td>
                  <td>{order.items.length} / {order.items.reduce((sum, item) => sum + item.quantity, 0)}</td>
                </tr>
              )})}{queueOrders.length === 0 && <tr><td colSpan={3}>В очереди нет заказов</td></tr>}</tbody>
              </table>
            </div>
          </div>
          <div className="assemblyQueue__details">
            {selected ? <>
            <div className="assemblyQueue__detailsHeader">
              <div className="assemblyQueue__orderHeading">
                <h2 className="receivingPage__panelTitle">ЗАКАЗ № {selected.number}</h2>
                <p className="assemblyQueue__subtitle">Содержимое заказа</p>
              </div>
              <dl className="assemblyQueue__parameters">
              <div><dt>Источник</dt><dd>Ozon</dd></div>
              <div><dt>Поступил</dt><dd>07.10.2026 - {selected.time}</dd></div>
              <div><dt>Позиций</dt><dd>{selected.items.length}</dd></div>
              <div><dt>Единиц</dt><dd>{total}</dd></div>
              </dl>
            </div>
            <div className="assemblyQueue__tableScroll">
              <table className="assemblyQueue__contents">
                <colgroup><col /><col className="assemblyQueue__neededColumn" /><col className="assemblyQueue__stockColumn" /><col className="assemblyQueue__cellsColumn" /></colgroup>
                <thead><tr><th>Товар / SKU</th><th>Нужно</th><th>В наличии</th><th>Ячейки подбора</th></tr></thead>
                <tbody>{selected.items.map((item) => (
                  <tr key={item.sku} className={item.stock < item.quantity ? 'assemblyQueue__shortageRow' : undefined}><td><strong>{item.name}</strong><span>{item.sku}</span></td>
                    <td>{item.quantity} шт.</td><td>{item.stock} шт.{item.stock < item.quantity && <i className="assemblyQueue__shortageIcon" aria-label={`Недостаточно товара: не хватает ${item.quantity - item.stock} шт.`} title={`Не хватает ${item.quantity - item.stock} шт.`}>!</i>}</td><td>{item.cells}</td></tr>
                ))}</tbody>
              </table>
            </div>
            <div className="assemblyQueue__footer">
              <div className="assemblyQueue__shortageMessage" id="assembly-shortage" role="status">
                {firstShortage && <><p>Недостаточно товара для начала сборки.</p><p>{firstShortage.name}: требуется {firstShortage.quantity}, доступно {firstShortage.stock}.{shortageItems.length > 1 && ` И ещё ${shortageItems.length - 1} проблемных позиций.`}</p></>}
              </div>
              <div className="assemblyQueue__footerActions">
                <button className="assemblyQueue__problem" type="button" disabled={draft.blocked || checkingStock} onClick={() => setProblemDialogOpen(true)}>Проблема</button>
                <button className="receivingPage__finishButton assemblyQueue__start" type="button" disabled={shortageItems.length > 0 || activeOrder !== null || draft.blocked || checkingStock} aria-describedby={firstShortage ? 'assembly-shortage' : undefined} onClick={() => { if (selected && !activeOrder && shortageItems.length === 0) void draft.save({ ...draft.state, activeOrder: selected, activeState: emptyAssemblyState }) }}>Начать сборку</button>
              </div>
            </div>
            </> : <p className="assemblyQueue__subtitle">Выберите заказ из очереди</p>}
          </div>
        </section>
        <ActiveAssembly key={activeOrder?.number ?? 'idle'} order={activeOrder} state={activeState} blocked={draft.blocked || checkingStock} onChange={(next) => draft.save({ ...draft.state, activeState: next })} onComplete={() => { if (activeOrder && !draft.blocked) void draft.save({ ...draft.state, completedOrders: [...completedOrders, activeOrder], activeOrder: null, activeState: emptyAssemblyState }) }} onProblem={(assembly) => {
          if (!activeOrder) return
          const createdAt = new Date().toISOString()
          void draft.save({ ...draft.state, problemOrders: [...problemOrders, { order: activeOrder, status: 'problem', assembly, problem: { ...assembly.details, shortages: [], createdAt, resolutionStatus: 'awaiting', history: createProblemHistory(activeOrder, assembly.details, createdAt, assembly) } }], activeOrder: null, activeState: emptyAssemblyState })
        }} />
        <ProblemOrders orders={problemOrders} blocked={draft.blocked || checkingStock} assemblyBusy={activeOrder !== null} onReturn={returnToAssembly} onStockCheck={refreshProblemStock} />
      </div>
      {problemDialogOpen && selected && <OrderProblemDialog initialType={shortageItems.length > 0 ? 'insufficient_stock' : ''} onCancel={() => setProblemDialogOpen(false)} onConfirm={(problem) => void confirmProblem(problem)} />}
    </main>
  )
}
