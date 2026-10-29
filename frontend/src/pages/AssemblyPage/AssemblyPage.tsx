import { emptyAssemblyState } from './assemblyState'
import { getQueueOrders } from './assemblyQueue'
import { useEffect, useRef, useState, type FormEvent } from 'react'
import Header from '../../components/Header/Header'
import ErrorTooltip from '../../components/ErrorTooltip/ErrorTooltip'
import ActiveAssembly, { type ActiveAssemblyState, type AssemblyProblemSnapshot, type AssemblyPreviewOrder } from './ActiveAssembly'
import useAssemblyDraft from './useAssemblyDraft'
import ProblemOrders from './ProblemOrders'
import ProblemArchive from './ProblemArchive'
import Disassembly, { type DisassemblySession } from './Disassembly'
import OrderProblemDialog, { type OrderProblemDetails, type SavedOrderProblem } from './OrderProblemDialog'
import { useTheme } from '../../hooks/useTheme'
import '../ReceivingPage/ReceivingPage.css'
import '../ReceivingPage/ReceivingHistory.css'
import './AssemblyPage.css'
import { API_BASE_URL } from '../../config/api'
const formatOrderTime = (value: string) => new Date(value).toLocaleTimeString('ru-RU', { timeZone: 'Asia/Krasnoyarsk', hour: '2-digit', minute: '2-digit' })
const formatOrderDate = (value: string) => new Date(value).toLocaleDateString('ru-RU', { timeZone: 'Asia/Krasnoyarsk' })

type PreviewOrder = AssemblyPreviewOrder & { createdAt: string; status: string; stockReady?: boolean; stockMessage?: string }
type AssemblyWorkspace = {
  disassemblySessions?: DisassemblySession[]
  schemaVersion: 1
  activeOrder: PreviewOrder | null
  activeState: ActiveAssemblyState
  problemOrders: Array<{ problemId?: string; order: PreviewOrder; problem: SavedOrderProblem; status: 'problem'; assembly?: AssemblyProblemSnapshot }>
  completedOrders: PreviewOrder[]
}
const initialWorkspace: AssemblyWorkspace = { schemaVersion: 1, activeOrder: null, activeState: emptyAssemblyState, problemOrders: [], completedOrders: [] }

function revealQueueSearchRow(container: HTMLDivElement, row: HTMLTableRowElement, search: HTMLFormElement) {
  let frame = 0
  let stopped = false
  let scrolling = false
  let stableFrames = 0
  let scrollStableFrames = 0
  let previousScrollTop = container.scrollTop
  let geometry: number[] = []
  const observer = new ResizeObserver(() => { stableFrames = 0; schedule() })

  function stop() {
    stopped = true
    cancelAnimationFrame(frame)
    observer.disconnect()
    container.removeEventListener('wheel', cancel)
    container.removeEventListener('touchstart', cancel)
    container.removeEventListener('pointerdown', cancel)
    container.removeEventListener('keydown', cancel)
  }
  function cancel() {
    if (stopped) return
    if (scrolling) container.scrollTo({ top: container.scrollTop, behavior: 'instant' })
    stop()
  }
  function schedule() {
    if (!stopped && !frame) frame = requestAnimationFrame(check)
  }
  function check() {
    frame = 0
    if (!container.isConnected || !row.isConnected || !container.contains(row)) { stop(); return }
    const containerRect = container.getBoundingClientRect()
    const rowRect = row.getBoundingClientRect()
    // Rects include CSS zoom; scrollTop and clientHeight use layout pixels.
    const scale = containerRect.height / container.offsetHeight
    if (!Number.isFinite(scale) || scale <= 0) { stop(); return }
    const nextGeometry = [containerRect.top, containerRect.height, rowRect.height, rowRect.top + container.scrollTop * scale]
    const changed = nextGeometry.some((value, index) => geometry[index] === undefined || Math.abs(value - geometry[index]) > 0.5)
    geometry = nextGeometry
    // Wait for actual transitions/reversals and committed layout, not a 220ms timer.
    if (changed || search.getAnimations({ subtree: true }).some(animation => animation.pending || animation.playState === 'running')) {
      stableFrames = 0
      schedule()
      return
    }
    if (++stableFrames < 2) { schedule(); return }
    if (scrolling) {
      const moved = Math.abs(container.scrollTop - previousScrollTop) > 0.1
      previousScrollTop = container.scrollTop
      scrollStableFrames = moved ? 0 : scrollStableFrames + 1
      if (scrollStableFrames < 2) { schedule(); return }
      scrolling = false
    }
    const visibleTop = containerRect.top + container.clientTop * scale
    const visibleBottom = visibleTop + container.clientHeight * scale
    let delta = 0
    if (rowRect.top < visibleTop - 0.5 || rowRect.height > visibleBottom - visibleTop) {
      delta = (rowRect.top - visibleTop) / scale - 1
    } else if (rowRect.bottom > visibleBottom + 0.5) {
      delta = (rowRect.bottom - visibleBottom) / scale + 1
    }
    const top = Math.max(0, Math.min(container.scrollHeight - container.clientHeight, container.scrollTop + delta))
    // Also terminate at an unreachable/clamped edge (e.g. an oversized row).
    if (delta === 0 || Math.abs(top - container.scrollTop) < 1) { stop(); return }
    previousScrollTop = container.scrollTop
    scrollStableFrames = 0
    scrolling = true
    container.scrollTo({ top, behavior: window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'instant' : 'smooth' })
    schedule()
  }
  observer.observe(container)
  observer.observe(row)
  container.addEventListener('wheel', cancel, { passive: true })
  container.addEventListener('touchstart', cancel, { passive: true })
  container.addEventListener('pointerdown', cancel)
  container.addEventListener('keydown', cancel)
  schedule()
  return cancel
}

export default function AssemblyPage() {
  const { theme, toggleTheme } = useTheme('dark')
  const [archiveOpen, setArchiveOpen] = useState(false)
  const [selectedNumber, setSelectedNumber] = useState('')
  const draft = useAssemblyDraft(initialWorkspace)
  const [orders, setOrders] = useState<PreviewOrder[]>([])
  const [loadingOrders, setLoadingOrders] = useState(false)
  const [ordersError, setOrdersError] = useState('')
  const [refreshOrders, setRefreshOrders] = useState(0)
  useEffect(() => {
    const controller = new AbortController()
    setLoadingOrders(true)
    void fetch(`${API_BASE_URL}/assembly/orders`, { signal: controller.signal, cache: 'no-store' }).then(async response => {
      if (!response.ok) throw new Error('Не удалось загрузить заказы из БД.')
      const data = await response.json() as { orders: PreviewOrder[] }
      if (!controller.signal.aborted) { setOrders(data.orders); setOrdersError('') }
    }).catch(reason => { if (!controller.signal.aborted) setOrdersError(reason instanceof Error ? reason.message : 'Не удалось загрузить очередь.') })
      .finally(() => { if (!controller.signal.aborted) setLoadingOrders(false) })
    return () => controller.abort()
  }, [refreshOrders, draft.state])
  const { activeOrder, activeState, problemOrders } = draft.state
  const [problemDialogOpen, setProblemDialogOpen] = useState(false)
  const [checkingStock, setCheckingStock] = useState(false)
  const stockCheckRef = useRef(false)
  const queueOrders = getQueueOrders(orders, draft.state)
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
  const cancelSearchScrollRef = useRef<(() => void) | null>(null)

  useEffect(() => () => cancelSearchScrollRef.current?.(), [])

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
    cancelSearchScrollRef.current?.()
    const found = queueOrders.find((order) => order.number === searchQuery.trim())
    if (!found) {
      setSearchMessage(searchQuery.trim() ? 'Заказ не найден' : 'Введите номер заказа')
      return
    }
    setSearchMessage('')
    setSelectedNumber(found.number)
    const row = orderRowsRef.current.get(found.number)
    const container = ordersScrollRef.current
    if (row && container && searchRef.current) {
      cancelSearchScrollRef.current = revealQueueSearchRow(container, row, searchRef.current)
    }
  }
  const selected = queueOrders.find((order) => order.number === selectedNumber) ?? queueOrders[0]
  const total = selected?.items.reduce((sum, item) => sum + item.quantity, 0) ?? 0
  const shortageItems = selected?.items.filter((item) => item.stock < item.quantity) ?? []
  const firstShortage = shortageItems[0]
  async function confirmProblem(problem: OrderProblemDetails) {
    if (!selected || draft.blocked) return
    const saved = await draft.createProblem({ source: 'queue', orderNumber: selected.number, details: problem })
    if (saved) {
      setSelectedNumber(queueOrders.find((order) => order.number !== selected.number)?.number ?? '')
      setProblemDialogOpen(false)
    }
  }

  async function returnToAssembly(number: string, problemId?: string) {
    if (draft.blocked || activeOrder || stockCheckRef.current) return false
    const entry = problemOrders.find((item) => item.order.number === number && item.problem.resolutionStatus !== 'returned' && (!problemId || item.problemId === problemId))
    return entry?.problemId ? draft.problemAction(entry.problemId, 'return') : false
  }

  async function refreshProblemStock(number: string, problemId?: string): Promise<string> {
    if (draft.blocked || stockCheckRef.current) return 'Дождитесь завершения сохранения.'
    const entry = problemOrders.find((item) => item.order.number === number && item.problem.type === 'insufficient_stock' && item.problem.resolutionStatus !== 'returned' && (!problemId || item.problemId === problemId))
    if (!entry || entry.problem.type !== 'insufficient_stock') return 'Проверка остатков не устраняет этот тип проблемы.'
    stockCheckRef.current = true
    setCheckingStock(true)
    try {
      if (!entry.problemId) return 'Перезагрузите страницу для восстановления ID проблемы.'
      await draft.problemAction(entry.problemId, 'check-stock')
      return draft.getActionMessage() ? `${number}: ${draft.getActionMessage()}` : 'Проверка не завершена. Проверьте сообщение сохранения и нажмите «Повторить».'
    } finally {
      stockCheckRef.current = false
      setCheckingStock(false)
    }
  }

  return (
    <main className={`receivingPage receivingPage--${theme} assemblyPage`}>
      <Header logoMode="navigation" theme={theme} onToggleTheme={toggleTheme} />
      <div className="receivingPage__content">
        {(!draft.ready || draft.saving || draft.error) && <div className={`assemblyPersistenceStatus ds-v1 ui-v1-feedback${draft.error ? ' ui-v1-feedback--error' : ''}`} role="status">
          {draft.error || (draft.saving ? 'Сохранение сборки…' : 'Восстановление сборки…')}
          {draft.error && <button type="button" className="receivingHistory__button ds-v1 ui-v1-button" onClick={() => void draft.retry()}>Повторить</button>}
        </div>}
        {draft.actionNotice && <div className="assemblyPersistenceStatus ds-v1 ui-v1-feedback" role="status">{draft.actionNotice}</div>}
        {ordersError && <div className="assemblyPersistenceStatus ds-v1 ui-v1-feedback ui-v1-feedback--error" role="status">{ordersError}<button className="ds-v1 ui-v1-button" type="button" onClick={() => setRefreshOrders(value => value + 1)}>Повторить</button></div>}
        {activeOrder && activeOrder.source !== 'ozon' && <div className="assemblyPersistenceStatus ds-v1 ui-v1-feedback" role="status">Историческая демонстрационная сборка сохранена. Она не выполняет реальные списания.</div>}
        <section className="receivingPage__panel assemblyQueue ds-v1 ui-v1-window" aria-label="Очередь заказов">
          <div className="assemblyQueue__list">
            <div className="assemblyQueue__heading">
              <h1 className="receivingPage__panelTitle ds-v1 ui-v1-page-title">ОЧЕРЕДЬ ЗАКАЗОВ</h1>
              <div className="assemblyQueue__listActions">
                <form ref={searchRef} className={`assemblyQueue__search ds-v1${searchOpen ? ' assemblyQueue__search--open' : ''}`} onSubmit={searchOrder}>
                  <div className="assemblyQueue__searchField">
                    <div className="assemblyQueue__searchInputClip">
                    <input ref={searchInputRef} className="ds-v1 ui-v1-input" aria-label="Номер заказа для поиска" placeholder="Номер заказа" value={searchQuery} tabIndex={searchOpen ? 0 : -1} onChange={(event) => { cancelSearchScrollRef.current?.(); setSearchQuery(event.target.value); setSearchMessage('') }} onKeyDown={(event) => { if (event.key === 'Escape') { setSearchOpen(false); searchRef.current?.querySelector('button')?.focus() } }} />
                    </div>
                    {searchOpen && <span className="assemblyQueue__searchMessage" role="status">{searchMessage}</span>}
                  </div>
                  <button className="receivingHistory__button receivingHistory__iconButton assemblyQueue__searchButton ds-v1 ui-v1-button ui-v1-icon-button" type="submit" aria-label="Поиск по номеру заказа" title="Поиск по номеру заказа" aria-expanded={searchOpen}>
                    <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" aria-hidden="true"><circle cx="10.5" cy="10.5" r="6.5" /><path d="m16 16 5 5" /></svg>
                  </button>
                </form>
                <button className="receivingHistory__button receivingHistory__iconButton ds-v1 ui-v1-button ui-v1-icon-button" type="button" aria-label="Обновить заказы" title="Обновить заказы" disabled={loadingOrders} aria-busy={loadingOrders} onClick={() => setRefreshOrders(value => value + 1)}>
                  <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                    <path d="M20 7v5h-5M20 12a8 8 0 1 0-2.3 5.7" />
                  </svg>
                </button>
                <span className="assemblyQueue__count ds-v1 ui-v1-counter">{queueOrders.length}</span>
              </div>
            </div>
            <p className="assemblyQueue__subtitle ds-v1 ui-v1-type-secondary">Выберите заказ для сборки</p>
            <div ref={ordersScrollRef} className="assemblyQueue__ordersScroll" role="region" aria-label="Список заказов" tabIndex={0}>
              <table className="assemblyQueue__orders ds-v1 ui-v1-table">
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
                  <td><span className="assemblyQueue__time"><span>{formatOrderTime(order.createdAt)}</span><span className={`assemblyQueue__wait assemblyQueue__wait--${waitLevel}`} aria-label={`Ожидание: ${minutes} мин.`} title={`С момента поступления: ${minutes} мин.`}>{minutes}</span></span><span className="assemblyQueue__date">{formatOrderDate(order.createdAt)}</span></td>
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
                <h2 className="receivingPage__panelTitle ds-v1 ui-v1-type-column">ЗАКАЗ № {selected.number}</h2>
                <p className="assemblyQueue__subtitle ds-v1 ui-v1-type-secondary">Содержимое заказа</p>
              </div>
              <dl className="assemblyQueue__parameters">
              <div><dt className="ds-v1 ui-v1-type-meta">Источник</dt><dd className="ds-v1 ui-v1-type-primary ui-v1-type-semibold">Ozon</dd></div>
              <div><dt className="ds-v1 ui-v1-type-meta">Поступил</dt><dd className="ds-v1 ui-v1-type-primary ui-v1-type-semibold">{formatOrderDate(selected.createdAt)} - {formatOrderTime(selected.createdAt)}</dd></div>
              <div><dt className="ds-v1 ui-v1-type-meta">Позиций</dt><dd className="ds-v1 ui-v1-type-primary ui-v1-type-semibold">{selected.items.length}</dd></div>
              <div><dt className="ds-v1 ui-v1-type-meta">Единиц</dt><dd className="ds-v1 ui-v1-type-primary ui-v1-type-semibold">{total}</dd></div>
              </dl>
            </div>
            <div className="assemblyQueue__tableScroll">
              <table className="assemblyQueue__contents ds-v1 ui-v1-table">
                <colgroup><col /><col className="assemblyQueue__neededColumn" /><col className="assemblyQueue__stockColumn" /><col className="assemblyQueue__cellsColumn" /></colgroup>
                <thead><tr><th>Товар / SKU</th><th>Нужно</th><th>В наличии</th><th>Ячейки подбора</th></tr></thead>
                <tbody>{selected.items.map((item) => (
                  <tr key={item.sku} className={item.stock < item.quantity ? 'assemblyQueue__shortageRow' : undefined}><td><strong>{item.name}</strong><span>{item.sku}</span></td>
                    <td>{item.quantity} шт.</td><td>{item.stock} шт.{item.stock < item.quantity && <ErrorTooltip className="assemblyQueue__shortageIcon" label={`Недостаточно товара: не хватает ${item.quantity - item.stock} шт.`} text={`Не хватает ${item.quantity - item.stock} шт.`} />}</td><td>{item.cells}</td></tr>
                ))}</tbody>
              </table>
            </div>
            <div className="assemblyQueue__footer">
              <div className="assemblyQueue__shortageMessage ds-v1 ui-v1-feedback ui-v1-feedback--error" id="assembly-shortage" role="status">
                {selected.stockReady === false && !firstShortage && <p>{selected.stockMessage}</p>}
                {firstShortage && <><p>Недостаточно товара для начала сборки.</p><p>{firstShortage.name}: требуется {firstShortage.quantity}, доступно {firstShortage.stock}.{shortageItems.length > 1 && ` И ещё ${shortageItems.length - 1} проблемных позиций.`}</p></>}
              </div>
              <div className="assemblyQueue__footerActions">
                <button className="assemblyQueue__problem ds-v1 ui-v1-action ui-v1-action--negative" type="button" disabled={draft.blocked || checkingStock} aria-busy={draft.saving} onClick={() => setProblemDialogOpen(true)}>Проблема</button>
                <button className="receivingPage__finishButton assemblyQueue__start ds-v1 ui-v1-action ui-v1-action--primary" type="button" disabled={selected.stockReady === false || shortageItems.length > 0 || activeOrder !== null || draft.blocked || checkingStock} aria-busy={draft.saving} aria-describedby={firstShortage ? 'assembly-shortage' : undefined} onClick={() => { if (selected && !activeOrder && shortageItems.length === 0) void draft.assemblyAction('start', { orderNumber: selected.number }) }}>Начать сборку</button>
              </div>
            </div>
            </> : <p className="assemblyQueue__subtitle ds-v1 ui-v1-type-secondary">Выберите заказ из очереди</p>}
          </div>
        </section>
        <ActiveAssembly key={activeOrder?.number ?? 'idle'} order={activeOrder} state={activeState} busy={draft.saving} blocked={draft.blocked || checkingStock || activeOrder?.source !== 'ozon'} onChange={() => Promise.resolve(false)} onServerAction={(action, code) => draft.assemblyAction(action, code ? { code } : {})} onComplete={() => { if (activeOrder && !draft.blocked) void draft.assemblyAction('finish') }} onProblem={(details) => activeOrder && !draft.blocked ? draft.createProblem({ source: 'assembly', orderNumber: activeOrder.number, details }) : Promise.resolve(false)} />
        <ProblemOrders onArchive={() => setArchiveOpen(true)} orders={problemOrders} blocked={draft.blocked || checkingStock} assemblyBusy={activeOrder !== null} onReturn={returnToAssembly} onStockCheck={refreshProblemStock} onMap={draft.mapProduct} onTransfer={async (problemId) => { await draft.problemAction(problemId, 'transfer-disassembly'); return draft.getActionMessage() || 'Передача не завершена. Проверьте сообщение сервера.' }} onOzonAction={async (problemId, action) => { await draft.problemAction(problemId, action); return draft.getActionMessage() || 'Действие не завершено. Проверьте сообщение сервера.' }} />
        {archiveOpen && <ProblemArchive refreshToken={draft.state} onClose={() => setArchiveOpen(false)} />}
        <Disassembly sessions={draft.state.disassemblySessions ?? []} blocked={draft.blocked || checkingStock} onAction={draft.disassemblyAction} />
      </div>
      {problemDialogOpen && selected && <OrderProblemDialog blocked={draft.blocked || checkingStock} positions={selected.items} initialPositionSku={firstShortage?.sku ?? selected.items[0]?.sku} initialType={shortageItems.length > 0 ? 'insufficient_stock' : ''} onCancel={() => setProblemDialogOpen(false)} onConfirm={(problem) => void confirmProblem(problem)} />}
    </main>
  )
}
