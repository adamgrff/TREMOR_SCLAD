import { type FormEvent, useEffect, useRef, useState } from 'react'
import Header from '../../components/Header/Header'
import ReceivingHistory from './ReceivingHistory'
import { useTheme } from '../../hooks/useTheme'
import './ReceivingPage.css'

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

type PlacementResponse = {
  id: number
  cellCode: string
  createdAt: string
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
  id: number
  status: 'active' | 'completed'
  pendingItems: Array<{
    name: string
    sku: string
    quantity: number
  }>
  placedItems: LatestReceivingItem[]
}

type CreateReceivingSessionResponse = {
  id: number
  status: 'active'
}

type InitialReceivingData =
  | { type: 'session'; session: ReceivingSessionDetails }
  | { type: 'latest'; receiving: LatestReceivingResponse }

const API_BASE_URL = 'http://127.0.0.1:8080/api'
const ACTIVE_SESSION_STORAGE_KEY = 'tremor.receiving.activeSessionId'

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
    hour: '2-digit',
    minute: '2-digit',
  }).format(new Date(value))
}

async function createReceivingSession() {
  const response = await fetch(`${API_BASE_URL}/receiving/sessions`, {
    method: 'POST',
  })

  if (!response.ok) {
    throw new Error(`Не удалось создать сессию: ${response.status}`)
  }

  return (await response.json()) as CreateReceivingSessionResponse
}

async function loadInitialReceivingData(): Promise<InitialReceivingData> {
  const storedSessionID = Number(
    localStorage.getItem(ACTIVE_SESSION_STORAGE_KEY),
  )

  if (Number.isSafeInteger(storedSessionID) && storedSessionID > 0) {
    const response = await fetch(
      `${API_BASE_URL}/receiving/sessions/${storedSessionID}`,
    )

    if (response.ok) {
      const session = (await response.json()) as ReceivingSessionDetails
      return { type: 'session', session }
    }

    if (response.status !== 404) {
      throw new Error(`Не удалось восстановить сессию: ${response.status}`)
    }

    localStorage.removeItem(ACTIVE_SESSION_STORAGE_KEY)
  } else if (localStorage.getItem(ACTIVE_SESSION_STORAGE_KEY)) {
    localStorage.removeItem(ACTIVE_SESSION_STORAGE_KEY)
  }

  const latestResponse = await fetch(
    `${API_BASE_URL}/receiving/sessions/latest`,
  )

  if (latestResponse.ok) {
    const receiving = (await latestResponse.json()) as LatestReceivingResponse
    return { type: 'latest', receiving }
  }

  if (latestResponse.status !== 404) {
    throw new Error(`Не удалось загрузить историю: ${latestResponse.status}`)
  }

  const createdSession = await createReceivingSession()
  localStorage.setItem(
    ACTIVE_SESSION_STORAGE_KEY,
    String(createdSession.id),
  )

  return {
    type: 'session',
    session: {
      id: createdSession.id,
      status: createdSession.status,
      pendingItems: [],
      placedItems: [],
    },
  }
}

function ReceivingPage() {
  const { theme, toggleTheme } = useTheme('light')
  const initialDataPromise = useRef<Promise<InitialReceivingData> | null>(null)

  const [receivingState, setReceivingState] =
    useState<ReceivingState>('start')
  const [sessionId, setSessionId] = useState<number | null>(null)
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
  const [isClearingItems, setIsClearingItems] = useState(false)
  const [isCreatingSession, setIsCreatingSession] = useState(false)
  const [isLoadingHistory, setIsLoadingHistory] = useState(true)
  const [historyRevision, setHistoryRevision] = useState(0)

  const isFinished = receivingState === 'finished'
  const isPlaced = receivingState === 'placed'
  const isScanBusy = isLookingUpProduct || isCheckingCell
  const isBusy =
    isScanBusy ||
    isUndoing ||
    isFinishing ||
    isClearingItems ||
    isCreatingSession ||
    isLoadingHistory

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

  async function handleUndoLastPlacement() {
    if (!latestPlacementId || isBusy || isFinished) return

    setIsUndoing(true)

    try {
      const response = await fetch(
        `${API_BASE_URL}/receiving/placements/${encodeURIComponent(latestPlacementId)}/undo`,
        { method: 'POST' },
      )

      if (!response.ok) {
        setScanMessage(
          response.status === 409
            ? 'Не удалось отменить размещение: остаток в ячейке изменился'
            : 'Не удалось отменить размещение в базе',
        )
        return
      }

      const lastPlacedGroup = placedItems.filter(
        (item) => item.placementId === latestPlacementId,
      )

      const restoredItems: ReceivingItem[] = lastPlacedGroup.map(
        ({ name, code, quantity }) => ({ name, code, quantity }),
      )

      setPlacedItems((items) =>
        items.filter((item) => item.placementId !== latestPlacementId),
      )

      setCurrentItems((items) => {
        const mergedItems = new Map(
          items.map((item) => [item.code, { ...item }]),
        )

        for (const restoredItem of restoredItems) {
          const existingItem = mergedItems.get(restoredItem.code)

          if (existingItem) {
            existingItem.quantity += restoredItem.quantity
          } else {
            mergedItems.set(restoredItem.code, restoredItem)
          }
        }

        return [...mergedItems.values()]
      })

      setReceivingState(
        restoredItems.length > 0 ? 'scanned' : 'start',
      )
      setLastScannedName(
        restoredItems[restoredItems.length - 1]?.name ?? '',
      )
      setLastScanKind(restoredItems.length > 0 ? 'product' : null)
      setScanMessage(
        'Последнее размещение отменено; товары возвращены в группу',
      )
    } catch {
      setScanMessage('Не удалось связаться с API при отмене размещения')
    } finally {
      setIsUndoing(false)
    }
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

    setSessionId(session.status === 'active' ? session.id : null)
    if (session.status === 'active') {
      localStorage.setItem(ACTIVE_SESSION_STORAGE_KEY, String(session.id))
    } else {
      localStorage.removeItem(ACTIVE_SESSION_STORAGE_KEY)
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
    const restoredItems: PlacedItem[] = receiving.items.map((item) => ({
      name: item.name,
      code: item.sku,
      quantity: item.quantity,
      cell: item.cellCode,
      time: formatPlacementTime(item.createdAt),
      placementId: String(item.placementId),
    }))

    setSessionId(null)
    localStorage.removeItem(ACTIVE_SESSION_STORAGE_KEY)
    setCurrentItems([])
    setPlacedItems(restoredItems)
    setReceivingState('finished')
    setLastScannedName(`Приёмка №${receiving.id}`)
    setLastScanKind('receiving')
    setScanMessage(`Загружена завершённая приёмка №${receiving.id}`)
  }

  useEffect(() => {
    let ignoreResult = false
    initialDataPromise.current ??= loadInitialReceivingData()

    void initialDataPromise.current
      .then((initialData) => {
        if (ignoreResult) return

        if (initialData.type === 'session') {
          restoreSession(initialData.session)
        } else {
          restoreLatestReceiving(initialData.receiving)
        }
      })
      .catch(() => {
        if (!ignoreResult) {
          setScanMessage('Не удалось загрузить приёмку из базы')
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
      const response = await fetch(
        `${API_BASE_URL}/receiving/finish`,
        {
          method: 'POST',
          headers: {
            'Content-Type': 'application/json',
          },
          body: JSON.stringify({ sessionId, placementIds }),
        },
      )

      if (response.status === 409) {
        setScanMessage(
          'Не удалось завершить приёмку: одно из размещений изменилось',
        )
        return
      }

      if (!response.ok) {
        throw new Error(`Ошибка API: ${response.status}`)
      }

      setSessionId(null)
      localStorage.removeItem(ACTIVE_SESSION_STORAGE_KEY)
      setReceivingState('finished')
      setScanCode('')
      setScanMessage('Приёмка завершена и сохранена в базе')
      setHistoryRevision((revision) => revision + 1)
    } catch {
      setScanMessage(
        'Не удалось завершить приёмку. Проверьте, что backend запущен.',
      )
    } finally {
      setIsFinishing(false)
    }
  }

  async function handleStartNewReceiving() {
    if (isBusy) return

    setIsCreatingSession(true)

    try {
      const session = await createReceivingSession()

      localStorage.setItem(
        ACTIVE_SESSION_STORAGE_KEY,
        String(session.id),
      )
      setSessionId(session.id)
      setCurrentItems([])
      setPlacedItems([])
      setScanCode('')
      setScanMessage(`Новая приёмка №${session.id} начата`)
      setLastScannedName('')
      setLastScanKind(null)
      setReceivingState('start')
    } catch {
      setScanMessage('Не удалось начать новую приёмку. Проверьте backend.')
    } finally {
      setIsCreatingSession(false)
    }
  }

  async function handleClearPendingItems() {
    if (sessionId === null || isBusy || currentItems.length === 0) return

    setIsClearingItems(true)

    try {
      const response = await fetch(
        `${API_BASE_URL}/receiving/sessions/${sessionId}/items`,
        { method: 'DELETE' },
      )

      if (!response.ok) {
        throw new Error(`Ошибка API: ${response.status}`)
      }

      setCurrentItems([])
      setReceivingState(placedItems.length > 0 ? 'placed' : 'start')
      setScanMessage('Группа очищена и удалена из черновика')
    } catch {
      setScanMessage('Не удалось очистить группу в базе')
    } finally {
      setIsClearingItems(false)
    }
  }

  async function handleProductScan(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()

    if (isFinished || isBusy) return
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
        const response = await fetch(
          `${API_BASE_URL}/receiving/placements`,
          {
            method: 'POST',
            headers: {
              'Content-Type': 'application/json',
            },
            body: JSON.stringify({
              sessionId,
              cellCode: cell,
              items: currentItems.map((item) => ({
                sku: item.code,
                quantity: item.quantity,
              })),
            }),
          },
        )

        if (response.status === 404) {
          setScanMessage(
            `Ячейка ${cell} или товар не найдены в базе`,
          )
          return
        }

        if (!response.ok) {
          throw new Error(`Ошибка API: ${response.status}`)
        }

        const placement =
          (await response.json()) as PlacementResponse

        const placementId = String(placement.id)
        const time = formatPlacementTime(placement.createdAt)

        const newPlacedItems = currentItems.map((item) => ({
          ...item,
          cell: placement.cellCode,
          time,
          placementId,
        }))

        setPlacedItems((items) => [...items, ...newPlacedItems])
        setCurrentItems([])
        setReceivingState('placed')
        setLastScannedName(`Ячейка ${placement.cellCode}`)
        setLastScanKind('cell')
        setScanMessage(
          `Группа размещена в ячейке ${placement.cellCode}`,
        )
      } catch {
        setScanMessage(
          'Не удалось проверить ячейку. Проверьте, что backend запущен.',
        )
      } finally {
        setIsCheckingCell(false)
      }

      return
    }

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

      const saveResponse = await fetch(
        `${API_BASE_URL}/receiving/sessions/${sessionId}/items`,
        {
          method: 'POST',
          headers: {
            'Content-Type': 'application/json',
          },
          body: JSON.stringify({ sku: product.code, quantity: 1 }),
        },
      )

      if (!saveResponse.ok) {
        if (saveResponse.status === 409) {
          setScanMessage('Приёмка уже завершена. Начните новую приёмку.')
          return
        }
        throw new Error(`Не удалось сохранить товар: ${saveResponse.status}`)
      }

      setCurrentItems((items) => {
        const existingItem = items.find(
          (item) => item.code === product.code,
        )

        if (existingItem) {
          return items.map((item) =>
            item.code === product.code
              ? { ...item, quantity: item.quantity + 1 }
              : item,
          )
        }

        return [...items, { ...product, quantity: 1 }]
      })

      setLastScannedName(product.name)
      setLastScanKind('product')
      setReceivingState('scanned')
      setScanMessage(`Добавлен товар: ${product.name}`)
    } catch {
      setScanMessage(
        'Не удалось связаться с API. Проверьте, что backend запущен.',
      )
    } finally {
      setIsLookingUpProduct(false)
    }
  }

  return (
    <main className={`receivingPage receivingPage--${theme}`}>
      <Header theme={theme} onToggleTheme={toggleTheme} />

      <section
        className="receivingPage__content"
        aria-label="Приёмка товаров"
      >
        <div className="receivingPage__scanPanel">
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
            <h1 className="receivingPage__scanTitle">
              СКАНИРУЙТЕ ТОВАР
            </h1>

            <p className="receivingPage__scanHint">
              КОД ТОВАРА БУДЕТ РАСПОЗНАН АВТОМАТИЧЕСКИ
            </p>

            <form
              className="receivingPage__scanForm"
              onSubmit={handleProductScan}
            >
              <input
                className="receivingPage__scanInput"
                aria-label="Код товара или ячейки"
                autoComplete="off"
                autoFocus
                disabled={isBusy || isFinished}
                placeholder="Введите или отсканируйте код"
                value={scanCode}
                onChange={(event) => {
                  setScanCode(event.target.value)
                  setScanMessage('')
                }}
              />

              <button
                className="receivingPage__scanSubmit"
                type="submit"
                disabled={isBusy || isFinished}
              >
                {isLookingUpProduct || isCheckingCell
                  ? 'ПРОВЕРЯЮ...'
                  : 'ДОБАВИТЬ'}
              </button>
            </form>

            {scanMessage && (
              <p className="receivingPage__scanMessage" role="status">
                {scanMessage}
              </p>
            )}
          </div>

          <span className="receivingPage__scanStatus">
            {isFinished
              ? 'Приёмка ЗАВЕРШЕНА'
              : 'Сканирование АКТИВНО'}
          </span>
        </div>

        <div className="receivingPage__workspace">
          <section
            className="receivingPage__panel receivingPage__pending"
            aria-labelledby="receiving-pending-title"
          >
            <div className="receivingPage__panelHeader">
              <div>
                <h2
                  className="receivingPage__panelTitle"
                  id="receiving-pending-title"
                >
                  ОЖИДАЮТ ЯЧЕЙКУ
                </h2>

                <p className="receivingPage__panelSubtitle">
                  {hasScannedItems
                    ? 'Товары готовы к размещению'
                    : 'Группа пуста - можно продолжать сканирование'}
                </p>
              </div>

              <button
                className="receivingPage__clearButton"
                type="button"
                disabled={!hasScannedItems || isBusy || isFinished}
                onClick={handleClearPendingItems}
              >
                ОЧИСТИТЬ ГРУППУ
              </button>
            </div>

            <table className="receivingPage__pendingTable">
              <thead>
                <tr>
                  <th scope="col">ТОВАР</th>
                  <th scope="col">КОД</th>
                  <th scope="col">КОЛИЧЕСТВО</th>
                </tr>
              </thead>

              <tbody>
                {hasScannedItems ? (
                  currentItems.map((item) => (
                    <tr key={item.code}>
                      <td>{item.name}</td>
                      <td>{item.code}</td>
                      <td>{item.quantity}</td>
                    </tr>
                  ))
                ) : (
                  <tr>
                    <td
                      className="receivingPage__pendingEmpty"
                      colSpan={3}
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
            className="receivingPage__panel receivingPage__context"
            aria-label="Сведения о сканировании"
          >
            <section className="receivingPage__contextSection">
              <h2 className="receivingPage__panelTitle">
                ПОСЛЕДНИЙ СКАН
              </h2>

              <p className="receivingPage__contextValue">
                {lastScanText}
              </p>

              <p className="receivingPage__contextHint">
                {lastScanHint}
              </p>
            </section>

            <section className="receivingPage__contextSection">
              <h3 className="receivingPage__contextLabel">
                Рекомендуемая ячейка
              </h3>

              <p className="receivingPage__contextValue">—</p>

              <p className="receivingPage__contextHint">
                Автоматический подбор ячейки пока не подключён.
              </p>
            </section>

            <section className="receivingPage__contextSection">
              <h3 className="receivingPage__contextLabel">
                Текущая группа
              </h3>

              <p className="receivingPage__contextValue">
                {hasScannedItems
                  ? `${scannedTotal} ${getUnitWord(scannedTotal)}`
                  : '0 единиц'}
              </p>

              <p className="receivingPage__contextHint">
                {hasScannedItems
                  ? 'Отсканируйте QR ячейки'
                  : 'Сначала отсканируйте товар'}
              </p>
            </section>
          </aside>
        </div>

        <section
          className="receivingPage__panel receivingPage__placed"
          aria-labelledby="receiving-placed-title"
        >
          <div className="receivingPage__placedHeader">
            <div>
              <h2
                className="receivingPage__panelTitle"
                id="receiving-placed-title"
              >
                УЖЕ РАЗМЕЩЕНО
              </h2>

              <p className="receivingPage__placedHint">
                {isFinished
                  ? 'Приёмка завершена'
                  : placedItems.length > 0
                    ? placedCellsDescription
                    : 'В этой приёмке пока ничего не размещено'}
              </p>
            </div>

            <div className="receivingPage__placedActions">
              <button
                className="receivingPage__undoButton"
                type="button"
                disabled={
                  !canUndoPlacement || isBusy || isFinished
                }
                onClick={handleUndoLastPlacement}
              >
                Отменить последнее действие
              </button>

              {isFinished ? (
                <button
                  className="receivingPage__finishButton"
                  type="button"
                  disabled={isBusy}
                  onClick={handleStartNewReceiving}
                >
                  НОВАЯ ПРИЁМКА
                </button>
              ) : (
                <button
                  className="receivingPage__finishButton"
                  type="button"
                  disabled={!canFinish}
                  onClick={handleFinishReceiving}
                >
                  {isFinishing ? 'СОХРАНЯЮ...' : 'ЗАВЕРШИТЬ ПРИЕМКУ'}
                </button>
              )}
            </div>
          </div>

          <table className="receivingPage__placedTable">
            <thead>
              <tr>
                <th scope="col">ТОВАР</th>
                <th scope="col">КОЛИЧЕСТВО</th>
                <th scope="col">ЯЧЕЙКА</th>
                <th scope="col">ВРЕМЯ</th>
              </tr>
            </thead>

            <tbody>
              {placedItems.length > 0 ? (
                placedItems.map((item) => (
                  <tr key={`${item.placementId}-${item.code}`}>
                    <td>{item.name}</td>
                    <td>{item.quantity}</td>
                    <td>{item.cell}</td>
                    <td>{item.time}</td>
                  </tr>
                ))
              ) : (
                <tr>
                  <td
                    className="receivingPage__placedEmpty"
                    colSpan={4}
                  >
                    Размещения этой приёмки появятся после сканирования QR ячейки
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </section>
        <ReceivingHistory revision={historyRevision} />
      </section>
    </main>
  )
}

export default ReceivingPage
