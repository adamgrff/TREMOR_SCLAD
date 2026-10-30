import { type FormEvent, useEffect, useRef, useState } from 'react'
import { useLocation } from 'react-router'
import type { Theme } from '../../components/Header/Header'
import ReceivingHistory from './ReceivingHistory'
import ProductCatalog from './ProductCatalog'
import ReceivingQuantityDialog from './ReceivingQuantityDialog'
import ReceivingScrollProgress from './ReceivingScrollProgress'
import './ReceivingPage.css'
import { API_BASE_URL } from '../../config/api'
import { ReceivingScanCommands, ReceivingCommandRejected, ReceivingRecoveryOrderUnknown, ReceivingJournalUnavailable, type ReceivingAction, readActiveReceivingSession, rememberActiveReceivingSession } from './receivingScanCommands'

type ReceivingState = 'start' | 'scanned' | 'placed' | 'finished'
type LastScanKind = 'product' | 'cell' | 'receiving' | null

type Product = {
  name: string
  code: string
}

type ProductLookupResponse = {
  name: string
  sku: string
}

type ReceivingItem = Product & {
  quantity: number
}

type PlacedItem = ReceivingItem & {
  cell: string
  time: string
  placementId: string
}

type LatestReceivingItem = {
  placementId: number
  cellCode: string
  createdAt: string
  name: string
  sku: string
  quantity: number
}

type LatestReceivingResponse = {
  id: number
  items: LatestReceivingItem[]
}

type ReceivingSessionDetails = {
  pendingRevision: string
  id: number
  status: 'active' | 'completed'
  pendingItems: Array<{
    name: string
    sku: string
    quantity: number
  }>
  placedItems: LatestReceivingItem[]
}

type ActiveSession = { id: number; startedAt: string; pendingQuantity: number; placementCount: number }

type InitialReceivingData =
  | { type: 'empty' }
  | { type: 'choose'; sessions: ActiveSession[] }
  | { type: 'session'; session: ReceivingSessionDetails }
  | { type: 'latest'; receiving: LatestReceivingResponse }

function getUnitWord(count: number) {
  const lastTwoDigits = count % 100

  if (lastTwoDigits >= 11 && lastTwoDigits <= 14) {
    return 'единиц'
  }

  switch (count % 10) {
    case 1:
      return 'единица'
    case 2:
    case 3:
    case 4:
      return 'единицы'
    default:
      return 'единиц'
  }
}

function formatPlacementTime(value: string) {
  return new Intl.DateTimeFormat('ru-RU', {
    day: '2-digit', month: '2-digit', year: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  }).format(new Date(value))
}

async function loadReceivingSession(id: number): Promise<ReceivingSessionDetails> {
  const response = await fetch(`${API_BASE_URL}/receiving/sessions/${id}`, { cache: 'no-store' })
  if (!response.ok) throw new Error('Не удалось подтвердить состояние приёмки на сервере')
  const session = await response.json() as ReceivingSessionDetails
  return validateReceivingSession(session, id)
}

function validateReceivingSession(session: ReceivingSessionDetails, id: number) {
  if (session.id !== id || !['active', 'completed'].includes(session.status)
    || typeof session.pendingRevision !== 'string' || !/^(0|[1-9][0-9]*)$/.test(session.pendingRevision)
    || !Array.isArray(session.pendingItems) || !Array.isArray(session.placedItems)) throw new Error('Некорректный ответ приёмки')
  return session
}

async function loadInitialReceivingData(): Promise<InitialReceivingData> {
  const stored = readActiveReceivingSession()
  if (stored && /^[1-9][0-9]*$/.test(stored) && Number.isSafeInteger(Number(stored))) {
    const response = await fetch(`${API_BASE_URL}/receiving/sessions/${stored}`, { cache: 'no-store' })
    if (response.ok) {
      const session = validateReceivingSession(await response.json() as ReceivingSessionDetails, Number(stored))
      if (session.status === 'active') return { type: 'session', session }
    } else if (response.status !== 404) throw new Error('Не удалось восстановить приёмку на сервере. Нажмите «Повторить».')
  }
  const response = await fetch(`${API_BASE_URL}/receiving/sessions/active`, { cache: 'no-store' })
  if (!response.ok) throw new Error('Не удалось загрузить активные приёмки. Нажмите «Повторить».')
  const sessions = await response.json() as ActiveSession[]
  if (!Array.isArray(sessions) || sessions.some(item => !Number.isSafeInteger(item.id) || item.id <= 0
    || !Number.isFinite(Date.parse(item.startedAt)) || !Number.isSafeInteger(item.pendingQuantity) || item.pendingQuantity < 0
    || !Number.isSafeInteger(item.placementCount) || item.placementCount < 0)) throw new Error('Некорректный список активных приёмок')
  if (sessions.length > 1) return { type: 'choose', sessions }
  if (sessions.length === 1) return { type: 'session', session: await loadReceivingSession(sessions[0].id) }
  const latest = await fetch(`${API_BASE_URL}/receiving/sessions/latest`, { cache: 'no-store' })
  if (latest.ok) return { type: 'latest', receiving: await latest.json() as LatestReceivingResponse }
  if (latest.status !== 404) throw new Error('Не удалось загрузить последнюю приёмку. Нажмите «Повторить».')
  return { type: 'empty' }
}

function ReceivingPage({ theme }: { theme: Theme }) {
  const location = useLocation()
  useEffect(() => {
    if (location.hash === '#receiving-scan') document.getElementById('receiving-scan')?.scrollIntoView({ block: 'start' })
  }, [location.hash])
  const initialDataPromise = useRef<Promise<InitialReceivingData> | null>(null)
  const scanCommands = useRef<ReceivingScanCommands | null>(null)
  scanCommands.current ??= new ReceivingScanCommands()
  const scanInFlight = useRef(false)
  const [manualRecovery, setManualRecovery] = useState(false)
  const [journalUnavailable, setJournalUnavailable] = useState(false)
  const [activeSessions, setActiveSessions] = useState<ActiveSession[]>([])
  const [scanRecoveryNeeded, setScanRecoveryNeeded] = useState(false)

  const [receivingState, setReceivingState] =
    useState<ReceivingState>('start')
  const [sessionId, setSessionId] = useState<number | null>(null)
  const [pendingRevision, setPendingRevision] = useState<string | null>(null)
  const [quantityItem, setQuantityItem] = useState<ReceivingItem | null>(null)
  const [currentItems, setCurrentItems] = useState<ReceivingItem[]>([])
  const [placedItems, setPlacedItems] = useState<PlacedItem[]>([])
  const [scanCode, setScanCode] = useState('')
  const [scanMessage, setScanMessage] = useState('')
  const [lastScannedName, setLastScannedName] = useState('')
  const [lastScanKind, setLastScanKind] = useState<LastScanKind>(null)
  const [isLookingUpProduct, setIsLookingUpProduct] = useState(false)
  const [isCheckingCell, setIsCheckingCell] = useState(false)
  const [isUndoing, setIsUndoing] = useState(false)
  const [isFinishing, setIsFinishing] = useState(false)
  const [isRemovingItem, setIsRemovingItem] = useState(false)
  const [isCreatingSession, setIsCreatingSession] = useState(false)
  const [isLoadingHistory, setIsLoadingHistory] = useState(true)
  const [historyRevision, setHistoryRevision] = useState(0)
  const [catalogRevision, setCatalogRevision] = useState(0)
  const [completedSessionId, setCompletedSessionId] = useState<number | null>(null)

  const isFinished = receivingState === 'finished'
  const isPlaced = receivingState === 'placed'
  const isScanBusy = isLookingUpProduct || isCheckingCell
  const isBusy =
    isScanBusy ||
    isUndoing ||
    isFinishing ||
    isRemovingItem ||
    isCreatingSession ||
    isLoadingHistory ||
    scanRecoveryNeeded || journalUnavailable || activeSessions.length > 0

  const hasScannedItems =
    receivingState === 'scanned' && currentItems.length > 0

  const scannedTotal = currentItems.reduce(
    (total, item) => total + item.quantity,
    0,
  )

  const latestPlacementId =
    placedItems[placedItems.length - 1]?.placementId
  const canUndoPlacement = Boolean(latestPlacementId)

  const canFinish =
    sessionId !== null &&
    placedItems.length > 0 &&
    currentItems.length === 0 &&
    !isFinished &&
    !isBusy

  const placedCellCodes = [
    ...new Set(placedItems.map((item) => item.cell)),
  ]

  const placedCellsDescription =
    placedCellCodes.length === 1
      ? `Товары размещены в ячейке ${placedCellCodes[0]}`
      : `Товары размещены в ячейках ${placedCellCodes.join(', ')}`

  const lastScanText = lastScannedName || 'Сканов ещё не было'

  const lastScanHint =
    lastScanKind === 'product'
      ? 'Последний отсканированный товар'
      : lastScanKind === 'cell'
        ? 'Последнее размещение выполнено'
        : lastScanKind === 'receiving'
          ? 'Последняя приёмка загружена из базы'
          : 'Первый товар появится здесь'

  async function performReceivingAction(action: ReceivingAction) {
    if (scanInFlight.current) return false
    scanInFlight.current = true
    try {
      await scanCommands.current!.execute(action, async id => { restoreSession(await loadReceivingSession(id)) })
      setScanRecoveryNeeded(false)
      setHistoryRevision(value => value + 1)
      setCatalogRevision(value => value + 1)
      return true
    } catch (reason) {
      setManualRecovery(reason instanceof ReceivingRecoveryOrderUnknown)
      setScanRecoveryNeeded(!(reason instanceof ReceivingCommandRejected))
      setScanMessage(reason instanceof Error ? reason.message : 'Результат операции не определён. Нажмите «Повторить».')
      return false
    } finally { scanInFlight.current = false }
  }

  async function handleUndoLastPlacement() {
    if (!latestPlacementId || sessionId === null || isBusy || isFinished) return
    setIsUndoing(true)
    try {
      if (await performReceivingAction({ kind: 'undo-placement', sessionId, placementId: Number(latestPlacementId) })) {
        setScanMessage('Последнее размещение отменено; товары возвращены в группу')
      }
    } finally { setIsUndoing(false) }
  }

  function restoreSession(session: ReceivingSessionDetails) {
    const restoredPendingItems: ReceivingItem[] = session.pendingItems.map(
      (item) => ({
        name: item.name,
        code: item.sku,
        quantity: item.quantity,
      }),
    )
    const restoredPlacedItems: PlacedItem[] = session.placedItems.map(
      (item) => ({
        name: item.name,
        code: item.sku,
        quantity: item.quantity,
        cell: item.cellCode,
        time: formatPlacementTime(item.createdAt),
        placementId: String(item.placementId),
      }),
    )

    setActiveSessions([])
    setPendingRevision(session.pendingRevision)
    setSessionId(session.status === 'active' ? session.id : null)
    setCompletedSessionId(session.status === 'completed' ? session.id : null)
    if (session.status === 'active') {
      rememberActiveReceivingSession(session.id)
    } else {
      rememberActiveReceivingSession(null)
    }

    setCurrentItems(restoredPendingItems)
    setPlacedItems(restoredPlacedItems)
    setReceivingState(
      session.status === 'completed'
        ? 'finished'
        : restoredPendingItems.length > 0
          ? 'scanned'
          : restoredPlacedItems.length > 0
            ? 'placed'
            : 'start',
    )

    const latestPlacedItem =
      restoredPlacedItems[restoredPlacedItems.length - 1]
    const latestPendingItem =
      restoredPendingItems[restoredPendingItems.length - 1]

    if (session.status === 'completed') {
      setLastScannedName(`Приёмка №${session.id}`)
      setLastScanKind('receiving')
      setScanMessage(`Загружена завершённая приёмка №${session.id}`)
    } else if (latestPendingItem) {
      setLastScannedName(latestPendingItem.name)
      setLastScanKind('product')
      setScanMessage(`Восстановлена активная приёмка №${session.id}`)
    } else if (latestPlacedItem) {
      setLastScannedName(`Ячейка ${latestPlacedItem.cell}`)
      setLastScanKind('cell')
      setScanMessage(`Восстановлена активная приёмка №${session.id}`)
    } else {
      setLastScannedName('')
      setLastScanKind(null)
      setScanMessage(`Новая приёмка №${session.id} готова к сканированию`)
    }
  }

  function restoreLatestReceiving(receiving: LatestReceivingResponse) {
    setActiveSessions([])
    setPendingRevision(null)
    setQuantityItem(null)
    setScanCode('')
    setCompletedSessionId(receiving.id)
    const restoredItems: PlacedItem[] = receiving.items.map((item) => ({
      name: item.name,
      code: item.sku,
      quantity: item.quantity,
      cell: item.cellCode,
      time: formatPlacementTime(item.createdAt),
      placementId: String(item.placementId),
    }))

    setSessionId(null)
    rememberActiveReceivingSession(null)
    setCurrentItems([])
    setPlacedItems(restoredItems)
    setReceivingState('finished')
    setLastScannedName(`Приёмка №${receiving.id}`)
    setLastScanKind('receiving')
    setScanMessage(`Загружена завершённая приёмка №${receiving.id}`)
  }

  useEffect(() => {
    let ignoreResult = false
    initialDataPromise.current ??= recoverInitialReceivingData()

    void initialDataPromise.current
      .then((initialData) => {
        if (ignoreResult) return

        applyInitialReceivingData(initialData)
      })
      .catch((reason: unknown) => {
        if (!ignoreResult) {
          setScanRecoveryNeeded(true)
          setManualRecovery(reason instanceof ReceivingRecoveryOrderUnknown)
          setScanMessage(reason instanceof Error ? reason.message : 'Не удалось подтвердить состояние приёмки или прочитать команды сканирования. Новые действия заблокированы. Нажмите «Повторить».')
        }
      })
      .finally(() => {
        if (!ignoreResult) {
          setIsLoadingHistory(false)
        }
      })

    return () => {
      ignoreResult = true
    }
  }, [])

  async function handleFinishReceiving() {
    if (!canFinish || sessionId === null) return

    const placementIds = [
      ...new Set(placedItems.map((item) => Number(item.placementId))),
    ]

    if (
      placementIds.length === 0 ||
      placementIds.some((id) => !Number.isSafeInteger(id) || id <= 0)
    ) {
      setScanMessage('Не удалось определить размещения для завершения')
      return
    }

    setIsFinishing(true)
    try {
      if (await performReceivingAction({ kind: 'finish', sessionId, placementIds })) {
        setScanCode('')
        setScanMessage('Приёмка завершена и сохранена в базе')
      }
    } finally { setIsFinishing(false) }
  }

  async function handleStartNewReceiving() {
    if (isBusy) return
    setIsCreatingSession(true)
    try {
      if (await performReceivingAction({ kind: 'create-session' })) {
        setScanCode('')
        setScanMessage('Новая приёмка начата')
      }
    } finally { setIsCreatingSession(false) }
  }

  function applyInitialReceivingData(initial: InitialReceivingData) {
    if (initial.type === 'session') restoreSession(initial.session)
    else if (initial.type === 'latest') restoreLatestReceiving(initial.receiving)
    else {
      setSessionId(null)
      setPendingRevision(null)
      setCompletedSessionId(null)
      setQuantityItem(null)
      setCurrentItems([])
      setPlacedItems([])
      setReceivingState('start')
      setScanCode('')
      setLastScannedName('')
      setLastScanKind(null)
      rememberActiveReceivingSession(null)
      setActiveSessions(initial.type === 'choose' ? initial.sessions : [])
      setScanMessage(initial.type === 'choose'
        ? 'Выберите активную приёмку для продолжения.'
        : 'Активных приёмок нет. Нажмите «Новая приёмка», чтобы начать.')
    }
  }

  async function recoverInitialReceivingData(): Promise<InitialReceivingData> {
    let recovered: InitialReceivingData | null = null
    try {
      await scanCommands.current!.recover(async id => {
        recovered = { type: 'session', session: await loadReceivingSession(id) }
      })
      setJournalUnavailable(false)
    } catch (reason) {
      if (!(reason instanceof ReceivingJournalUnavailable)) throw reason
      setJournalUnavailable(true)
    }
    return recovered ?? await loadInitialReceivingData()
  }

  async function selectReceivingSession(id: number) {
    setIsLoadingHistory(true)
    try {
      const session = await loadReceivingSession(id)
      if (session.status !== 'active') { applyInitialReceivingData(await loadInitialReceivingData()); return }
      restoreSession(session)
    } catch (reason) {
      setScanMessage(reason instanceof Error ? reason.message : 'Не удалось загрузить приёмку')
    } finally { setIsLoadingHistory(false) }
  }

  async function recoverScan() {
    if (manualRecovery || scanInFlight.current || isScanBusy || isLoadingHistory) return
    scanInFlight.current = true
    setIsLookingUpProduct(true)
    try {
      const initial = await recoverInitialReceivingData()
      applyInitialReceivingData(initial)
      setScanRecoveryNeeded(false)
      setScanCode('')
      setHistoryRevision(value => value + 1)
      setCatalogRevision(value => value + 1)
      if (initial.type === 'session') setScanMessage('Результат подтверждён. Данные приёмки загружены с сервера; можно продолжать.')
    } catch (reason) {
      setScanRecoveryNeeded(true)
      setManualRecovery(reason instanceof ReceivingRecoveryOrderUnknown)
      setScanMessage(reason instanceof Error ? reason.message : 'Результат сканирования не определён. Новые действия заблокированы.')
    } finally { scanInFlight.current = false; setIsLookingUpProduct(false) }
  }

  async function refreshReceivingSession() {
    if (sessionId === null) return
    restoreSession(await loadReceivingSession(sessionId))
  }

  async function handleRemovePendingItem(code: string) {
    if (sessionId === null || pendingRevision === null || isBusy || currentItems.length === 0) return
    setIsRemovingItem(true)
    try {
      if (await performReceivingAction({ kind: 'delete-item', sessionId, sku: code, expectedRevision: pendingRevision })) {
        setScanMessage(`Товар ${code} удалён из ожидающих размещения`)
      }
    } finally { setIsRemovingItem(false) }
  }

  async function handleProductScan(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()

    if (scanRecoveryNeeded) { await recoverScan(); return }
    if (isFinished || isBusy || scanInFlight.current) return
    if (sessionId === null) {
      setScanMessage('Активная приёмка не загружена. Перезагрузите страницу.')
      return
    }

    const code = scanCode.trim().toUpperCase()
    if (!code) return

    const isCellCode = /^[A-Z]-?\d+$/.test(code)

    if (isCellCode) {
      const cell = code.replace('-', '')

      if (!hasScannedItems) {
        setScanMessage('Сначала отсканируйте товары')
        setScanCode('')
        return
      }

      setIsCheckingCell(true)
      setScanCode('')
      try {
        if (await performReceivingAction({ kind: 'place', sessionId, cellCode: cell,
          items: currentItems.map(item => ({ sku: item.code, quantity: item.quantity })) })) {
          setLastScannedName(`Ячейка ${cell}`)
          setLastScanKind('cell')
          setScanMessage(`Группа размещена в ячейке ${cell}`)
        }
      } finally { setIsCheckingCell(false) }

      return
    }

    scanInFlight.current = true
    setIsLookingUpProduct(true)
    setScanCode('')

    try {
      const response = await fetch(
        `${API_BASE_URL}/products/${encodeURIComponent(code)}`,
      )

      if (response.status === 404) {
        setScanMessage(`Код ${code} не найден`)
        return
      }

      if (!response.ok) {
        throw new Error(`Ошибка API: ${response.status}`)
      }

      const productData =
        (await response.json()) as ProductLookupResponse

      const product: Product = {
        name: productData.name,
        code: productData.sku,
      }

      try {
        await scanCommands.current!.scan(sessionId, product.code, async (id) => {
          restoreSession(await loadReceivingSession(id))
        })
      } catch (reason) {
        setScanRecoveryNeeded(!(reason instanceof ReceivingCommandRejected))
        setManualRecovery(reason instanceof ReceivingRecoveryOrderUnknown)
        setScanMessage(reason instanceof Error ? reason.message : 'Результат сканирования не определён. Нажмите «Повторить».')
        return
      }

      setLastScannedName(product.name)
      setLastScanKind('product')
      setScanMessage(`Добавлен товар: ${product.name}`)
    } catch {
      setScanMessage(
        'Не удалось связаться с API. Проверьте, что backend запущен.',
      )
    } finally {
      scanInFlight.current = false
      setIsLookingUpProduct(false)
    }
  }

  return (
    <main className={`receivingPage receivingPage--${theme}`}>
      <div className="pageHeaderSpace" aria-hidden="true" />
      <ReceivingScrollProgress />

      <section
        className="receivingPage__content"
        aria-label="Приёмка товаров"
      >
        <div className="receivingPage__scanPanel ds-v1 ui-v1-control-panel" id="receiving-scan">
          <svg
            className="receivingPage__scanIcon"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            strokeWidth="2"
            strokeLinecap="round"
            strokeLinejoin="round"
            aria-hidden="true"
          >
            <path d="M4 8V6a2 2 0 0 1 2-2h2" />
            <path d="M16 4h2a2 2 0 0 1 2 2v2" />
            <path d="M20 16v2a2 2 0 0 1-2 2h-2" />
            <path d="M8 20H6a2 2 0 0 1-2-2v-2" />
            <path d="M3 12h18M8 8h8M8 16h8" />
          </svg>

          <div className="receivingPage__scanText">
            <h1 className="receivingPage__scanTitle ds-v1 ui-v1-type-section">
              СКАНИРУЙТЕ ТОВАР
            </h1>


            <form
              className="receivingPage__scanForm"
              onSubmit={handleProductScan}
            >
              <input
                className="receivingPage__scanInput ds-v1 ui-v1-input"
                aria-label="Код товара или ячейки"
                autoComplete="off"
                autoFocus
                disabled={isBusy || isFinished || sessionId === null}
                placeholder="Введите или отсканируйте код"
                value={scanCode}
                onChange={(event) => {
                  setScanCode(event.target.value)
                  setScanMessage('')
                }}
              />

              <button
                className="receivingPage__scanSubmit ds-v1 ui-v1-action ui-v1-action--primary"
                aria-busy={isScanBusy}
                type="submit"
                disabled={manualRecovery || isScanBusy || isLoadingHistory || (!scanRecoveryNeeded && (isBusy || isFinished || sessionId === null))}
              >
                {isLookingUpProduct || isCheckingCell
                  ? 'ПРОВЕРЯЮ...'
                  : manualRecovery ? 'ТРЕБУЕТСЯ ПРОВЕРКА' : scanRecoveryNeeded ? 'ПОВТОРИТЬ' : 'ДОБАВИТЬ'}
              </button>
            </form>

            {scanMessage && (
              <p className="receivingPage__scanMessage ds-v1 ui-v1-type-secondary" role="status">
                {scanMessage}
              </p>
            )}
          </div>

          <span className="receivingPage__scanStatus">
            {isFinished
              ? 'Приёмка ЗАВЕРШЕНА'
              : isBusy || sessionId === null ? 'Сканирование недоступно' : 'Сканирование АКТИВНО'}
          </span>
        </div>

        {journalUnavailable && <p role="status" className="receivingPage__scanMessage ds-v1 ui-v1-type-secondary">Журнал команд недоступен. Доступен только просмотр. Для восстановления записи включите хранилище и обновите страницу.</p>}
        {activeSessions.length > 0 && <section className="receivingPage__panel ds-v1 ui-v1-window" aria-label="Выбор активной приёмки">
          <h2 className="receivingPage__panelTitle ds-v1 ui-v1-type-section">АКТИВНЫЕ ПРИЁМКИ</h2>
          {activeSessions.map(session => <p key={session.id}><button type="button" className="receivingHistory__button ds-v1 ui-v1-button" disabled={isLoadingHistory || scanRecoveryNeeded} onClick={() => void selectReceivingSession(session.id)}>Приёмка №{session.id} · {formatPlacementTime(session.startedAt)} · ожидают: {session.pendingQuantity} · размещений: {session.placementCount}</button></p>)}
        </section>}
        <div className="receivingPage__workspace">
          <section
            className="receivingPage__panel receivingPage__pending ds-v1 ui-v1-window"
            aria-labelledby="receiving-pending-title"
          >
            <div className="receivingPage__panelHeader">
              <div>
                <h2
                  className="receivingPage__panelTitle ds-v1 ui-v1-page-title"
                  id="receiving-pending-title"
                >
                  ОЖИДАЮТ ЯЧЕЙКУ
                </h2>

                <p className="receivingPage__panelSubtitle ds-v1 ui-v1-type-secondary">
                  {hasScannedItems
                    ? 'Товары готовы к размещению'
                    : 'Группа пуста - можно продолжать сканирование'}
                </p>
              </div>

            </div>

            <table className="receivingPage__pendingTable ds-v1 ui-v1-table">
              <thead>
                <tr>
                  <th scope="col">ТОВАР</th>
                  <th scope="col">КОД</th>
                  <th scope="col">КОЛИЧЕСТВО</th>
                  <th scope="col" aria-label="Действия" />
                </tr>
              </thead>

              <tbody>
                {hasScannedItems ? (
                  currentItems.map((item) => (
                    <tr key={item.code}>
                      <td>{item.name}</td>
                      <td>{item.code}</td>
                      <td><button type="button" className="receivingHistory__button ds-v1 ui-v1-button" disabled={isBusy || isFinished} aria-label={`Изменить количество ${item.code}`} onClick={() => setQuantityItem(item)}>{item.quantity} ✎</button></td>
                      <td><button type="button" className="receivingHistory__button receivingHistory__deleteButton receivingHistory__iconButton ds-v1 ui-v1-button ui-v1-icon-button ui-v1-button--delete" disabled={isBusy || isFinished} aria-label={`Удалить ${item.code} из ожидающих`} title="Удалить товар" onClick={() => void handleRemovePendingItem(item.code)}>
                        <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="M3 6h18M9 6V4h6v2M5 6l1 14h12l1-14M10 10v6M14 10v6" /></svg>
                      </button></td>
                    </tr>
                  ))
                ) : (
                  <tr>
                    <td
                      className="receivingPage__pendingEmpty ds-v1 ui-v1-type-secondary"
                      colSpan={4}
                    >
                      {isPlaced
                        ? 'Группа размещена. Сканируйте следующие товары'
                        : 'Отсканированные товары появятся здесь'}
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </section>

          <aside
            className="receivingPage__panel receivingPage__context ds-v1 ui-v1-control-panel"
            aria-label="Сведения о сканировании"
          >
            <section className="receivingPage__contextSection">
              <h2 className="receivingPage__panelTitle ds-v1 ui-v1-type-section">
                ПОСЛЕДНИЙ СКАН
              </h2>

              <p className="receivingPage__contextValue ds-v1 ui-v1-type-primary ui-v1-type-semibold">
                {lastScanText}
              </p>

              <p className="receivingPage__contextHint ds-v1 ui-v1-type-secondary">
                {lastScanHint}
              </p>
            </section>

            <section className="receivingPage__contextSection">
              <h3 className="receivingPage__contextLabel ds-v1 ui-v1-type-secondary">
                Рекомендуемая ячейка
              </h3>

              <p className="receivingPage__contextValue ds-v1 ui-v1-type-primary ui-v1-type-semibold">—</p>

              <p className="receivingPage__contextHint ds-v1 ui-v1-type-secondary">
                Автоматический подбор ячейки пока не подключён.
              </p>
            </section>

            <section className="receivingPage__contextSection">
              <h3 className="receivingPage__contextLabel ds-v1 ui-v1-type-secondary">
                Текущая группа
              </h3>

              <p className="receivingPage__contextValue ds-v1 ui-v1-type-primary ui-v1-type-semibold">
                {hasScannedItems
                  ? `${scannedTotal} ${getUnitWord(scannedTotal)}`
                  : '0 единиц'}
              </p>

              <p className="receivingPage__contextHint ds-v1 ui-v1-type-secondary">
                {hasScannedItems
                  ? 'Отсканируйте QR ячейки'
                  : 'Сначала отсканируйте товар'}
              </p>
            </section>
          </aside>
        </div>

        <section
          className="receivingPage__panel receivingPage__placed ds-v1 ui-v1-window"
          id="receiving-placed"
          aria-labelledby="receiving-placed-title"
        >
          <div className="receivingPage__placedHeader">
            <div>
              <h2
                className="receivingPage__panelTitle ds-v1 ui-v1-page-title"
                id="receiving-placed-title"
              >
                УЖЕ РАЗМЕЩЕНО
              </h2>

              <p className="receivingPage__placedHint ds-v1 ui-v1-type-secondary">
                {isFinished
                  ? 'Приёмка завершена'
                  : placedItems.length > 0
                    ? placedCellsDescription
                    : 'В этой приёмке пока ничего не размещено'}
              </p>
            </div>

            <div className="receivingPage__placedActions">
              <button
                className="receivingPage__undoButton ds-v1 ui-v1-action ui-v1-action--negative"
                aria-busy={isUndoing}
                type="button"
                disabled={
                  !canUndoPlacement || isBusy || isFinished
                }
                onClick={handleUndoLastPlacement}
              >
                Отменить последнее действие
              </button>

              {isFinished || (sessionId === null && activeSessions.length === 0) ? (
                <button
                  className="receivingPage__finishButton ds-v1 ui-v1-action ui-v1-action--primary"
                  type="button"
                  disabled={isBusy}
                  aria-busy={isCreatingSession}
                  onClick={handleStartNewReceiving}
                >
                  НОВАЯ ПРИЁМКА
                </button>
              ) : (
                <button
                  className="receivingPage__finishButton receivingPage__finishAction ds-v1 ui-v1-action ui-v1-action--primary"
                  aria-busy={isFinishing}
                  type="button"
                  disabled={!canFinish}
                  onClick={handleFinishReceiving}
                >
                  {isFinishing ? 'СОХРАНЯЮ...' : 'ЗАВЕРШИТЬ ПРИЕМКУ'}
                </button>
              )}
            </div>
          </div>

          <table className="receivingPage__placedTable ds-v1 ui-v1-table">
            <thead>
              <tr>
                <th scope="col">ТОВАР</th>
                <th scope="col">ЯЧЕЙКА</th>
                <th scope="col">КОЛИЧЕСТВО</th>
                <th scope="col">ВРЕМЯ</th>
                <th scope="col" aria-label="Отступ" />
              </tr>
            </thead>

            <tbody>
              {placedItems.length > 0 ? (
                placedItems.map((item) => (
                  <tr key={`${item.placementId}-${item.code}`}>
                    <td>{item.name}</td>
                    <td>{item.cell}</td>
                    <td>{item.quantity}</td>
                    <td>{item.time}</td>
                    <td />
                  </tr>
                ))
              ) : (
                <tr>
                  <td
                    className="receivingPage__placedEmpty ds-v1 ui-v1-type-secondary"
                    colSpan={5}
                  >
                    Размещения этой приёмки появятся после сканирования QR ячейки
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </section>
        <ReceivingHistory readOnly={isBusy} revision={historyRevision} onDeleted={(id) => {
          if (id === completedSessionId) {
            setCompletedSessionId(null)
            setPlacedItems([])
            setLastScannedName('')
            setLastScanKind(null)
            setScanMessage(`Приёмка №${id} удалена из истории. Остатки товаров сохранены.`)
          }
        }} />
        <ProductCatalog sessionId={sessionId} disabled={isBusy || isFinished || sessionId === null} revision={catalogRevision} onReceivingChanged={refreshReceivingSession} />
        {quantityItem && sessionId !== null && <ReceivingQuantityDialog sessionId={sessionId} product={{ name: quantityItem.name, sku: quantityItem.code, quantity: quantityItem.quantity }} onClose={() => setQuantityItem(null)} onSaved={refreshReceivingSession} />}
      </section>
    </main>
  )
}

export default ReceivingPage
