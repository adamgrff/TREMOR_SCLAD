import Header from '../../components/Header/Header'
import { useTheme } from '../../hooks/useTheme'
import './ReceivingPage.css'

type ReceivingState = 'start' | 'scanned' | 'placed'

const testState: ReceivingState = 'placed'

const scannedItems = [
  { name: 'Ручка TREMOR', code: 'TRM-001', quantity: 2 },
  { name: 'Комплект TREMOR', code: 'TRM-002', quantity: 1 },
]

const placedItems = [
  { name: 'Ручка TREMOR', quantity: 2, cell: 'A-01', time: '14:32' },
  { name: 'Комплект TREMOR', quantity: 1, cell: 'A-01', time: '14:32' },
]

function ReceivingPage() {
  const { theme, toggleTheme } = useTheme('light')

  const hasScannedItems = testState === 'scanned'
  const isPlaced = testState === 'placed'

  const scannedTotal = scannedItems.reduce(
    (total, item) => total + item.quantity,
    0,
  )

  const lastScanText = hasScannedItems
    ? scannedItems[scannedItems.length - 1].name
    : isPlaced
      ? `Ячейка ${placedItems[0].cell}`
      : 'Сканов ещё не было'

  const lastScanHint = hasScannedItems
    ? 'Последний отсканированный товар'
    : isPlaced
      ? 'Последнее размещение выполнено'
      : 'Первый товар появится здесь'

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
          </div>

          <span className="receivingPage__scanStatus">
            Сканирование АКТИВНО
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
                disabled
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
                  scannedItems.map((item) => (
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
                  ? `${scannedTotal} единицы`
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
                {isPlaced
                  ? 'Товары размещены в ячейке A-01'
                  : 'В этой приёмке пока ничего не размещено'}
              </p>
            </div>

            <div className="receivingPage__placedActions">
              <button
                className="receivingPage__undoButton"
                type="button"
                disabled
              >
                Отменить последнее действие
              </button>

              <button
                className="receivingPage__finishButton"
                type="button"
                disabled
              >
                ЗАВЕРШИТЬ ПРИЕМКУ
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
              {isPlaced ? (
                placedItems.map((item) => (
                  <tr key={`${item.name}-${item.cell}-${item.time}`}>
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