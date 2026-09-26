import { type FormEvent, useState } from 'react'
import Header from '../../components/Header/Header'
import { useTheme } from '../../hooks/useTheme'
import './ReceivingPage.css'

type ReceivingState = 'start' | 'scanned' | 'placed' | 'finished'
type LastScanKind = 'product' | 'cell' | null

type Product = {
  name: string
  code: string
}

type ReceivingItem = Product & {
  quantity: number
}

type PlacedItem = ReceivingItem & {
  cell: string
  time: string
  placementId: string
}

const productCatalog: Product[] = [
  { name: 'Ручка TREMOR', code: 'TRM-001' },
  { name: 'Комплект TREMOR', code: 'TRM-002' },
]

const cellCatalog = ['A-01']

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

function getCurrentTime() {
  return new Intl.DateTimeFormat('ru-RU', {
    hour: '2-digit',
    minute: '2-digit',
  }).format(new Date())
}

function ReceivingPage() {
  const { theme, toggleTheme } = useTheme('light')

  const [testState, setTestState] = useState<ReceivingState>('start')
  const [currentItems, setCurrentItems] = useState<ReceivingItem[]>([])
  const [placedItems, setPlacedItems] = useState<PlacedItem[]>([])
  const [scanCode, setScanCode] = useState('')
  const [scanMessage, setScanMessage] = useState('')
  const [lastScannedName, setLastScannedName] = useState('')
  const [lastScanKind, setLastScanKind] = useState<LastScanKind>(null)

  const hasScannedItems = testState === 'scanned' && currentItems.length > 0
  const isPlaced = testState === 'placed'

  const isFinished = testState === 'finished'
  const canFinish =
    placedItems.length > 0 && currentItems.length === 0 && !isFinished

  const scannedTotal = currentItems.reduce(
    (total, item) => total + item.quantity,
    0,
  )

  const latestPlacementId =
    placedItems[placedItems.length - 1]?.placementId
  const canUndoPlacement = Boolean(latestPlacementId)

  const lastScanText = lastScannedName || 'Сканов ещё не было'

  const lastScanHint =
    lastScanKind === 'product'
      ? 'Последний отсканированный товар'
      : lastScanKind === 'cell'
        ? 'Последнее размещение выполнено'
        : 'Первый товар появится здесь'

  function handleUndoLastPlacement() {
    if (!latestPlacementId) return

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
      let mergedItems = [...items]

      for (const restoredItem of restoredItems) {
        const existingItem = mergedItems.find(
          (item) => item.code === restoredItem.code,
        )

        if (existingItem) {
          mergedItems = mergedItems.map((item) =>
            item.code === restoredItem.code
              ? { ...item, quantity: item.quantity + restoredItem.quantity }
              : item,
          )
        } else {
          mergedItems = [...mergedItems, restoredItem]
        }
      }

      return mergedItems
    })

    setTestState('scanned')
    setLastScannedName(restoredItems[restoredItems.length - 1].name)
    setLastScanKind('product')
    setScanMessage('Последнее размещение отменено; товары возвращены в группу')
  }

  function handleFinishReceiving() {
    if (!canFinish) return

    setTestState('finished')
    setScanMessage('Приёмка завершена')
    setScanCode('')
  }

  function handleProductScan(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (isFinished) return

    const code = scanCode.trim().toUpperCase()
    if (!code) return

    const cell = cellCatalog.find(
      (item) => item.toUpperCase() === code,
    )

    if (cell) {
      if (!hasScannedItems) {
        setScanMessage('Сначала отсканируйте товары')
        setScanCode('')
        return
      }

      const placementId = Date.now().toString()
      const time = getCurrentTime()

      const newPlacedItems = currentItems.map((item) => ({
        ...item,
        cell,
        time,
        placementId,
      }))

      setPlacedItems((items) => [...items, ...newPlacedItems])
      setCurrentItems([])
      setTestState('placed')
      setLastScannedName(`Ячейка ${cell}`)
      setLastScanKind('cell')
      setScanMessage(`Группа размещена в ячейке ${cell}`)
      setScanCode('')
      return
    }

    const product = productCatalog.find(
      (item) => item.code.toUpperCase() === code,
    )

    if (!product) {
      setScanMessage(`Код ${code} не найден`)
      setScanCode('')
      return
    }

    setCurrentItems((items) => {
      const existingItem = items.find((item) => item.code === product.code)

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
    setTestState('scanned')
    setScanMessage(`Добавлен товар: ${product.name}`)
    setScanCode('')
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
                disabled={isFinished}
              >
                ДОБАВИТЬ
              </button>
            </form>

            {scanMessage && (
              <p className="receivingPage__scanMessage" role="status">
                {scanMessage}
              </p>
            )}
          </div>

          <span className="receivingPage__scanStatus">
            {isFinished ? 'Приёмка ЗАВЕРШЕНА' : 'Сканирование АКТИВНО'}
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
                disabled={!hasScannedItems || isFinished}
                onClick={() => {
                  setCurrentItems([])
                  setTestState('start')
                  setScanMessage('')
                }}
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

              <p className="receivingPage__contextValue">
                {hasScannedItems ? 'A-01' : '—'}
              </p>

              <p className="receivingPage__contextHint">
                {hasScannedItems
                  ? 'Группа готова к размещению'
                  : 'Рекомендация появится после сканирования товара.'}
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
                    ? 'Товары размещены в ячейке A-01'
                    : 'В этой приёмке пока ничего не размещено'}
              </p>
            </div>

            <div className="receivingPage__placedActions">
              <button
                className="receivingPage__undoButton"
                type="button"
                disabled={!canUndoPlacement || isFinished}
                onClick={handleUndoLastPlacement}
              >
                Отменить последнее действие
              </button>

              <button
                className="receivingPage__finishButton"
                type="button"
                disabled={!canFinish || isFinished}
                onClick={handleFinishReceiving}
              >
                {isFinished ? 'ПРИЁМКА ЗАВЕРШЕНА' : 'ЗАВЕРШИТЬ ПРИЕМКУ'}
              </button>
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
                    История размещения появится после сканирования QR ячейки
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </section>
      </section>
    </main>
  )
}

export default ReceivingPage