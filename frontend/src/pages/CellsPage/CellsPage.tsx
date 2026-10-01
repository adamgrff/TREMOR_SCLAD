import { useEffect, useRef, useState } from 'react'
import { Link } from 'react-router'
import { API_BASE_URL } from '../../config/api'

import Header from '../../components/Header/Header'
import CreateRackDialog from './CreateRackDialog'
import { useTheme } from '../../hooks/useTheme'
import './CellsPage.css'

type ProductCategory = string

type RackConfig = {
  id: number
  name: string
  rows: string[][]
}

type CellItem = {
  id: string
  sku: string
  name: string
  quantity: number
}

type CellLoadStatus =
  | 'idle'
  | 'loading'
  | 'success'
  | 'error'

async function getCellContents(
  cellName: string,
  categoryId: number,
): Promise<CellItem[]> {
  const response = await fetch(
    `${API_BASE_URL}/cells/${encodeURIComponent(cellName)}?categoryId=${categoryId}`,
  )

  if (!response.ok) {
    throw new Error(
      `Не удалось загрузить ячейку: ${response.status}`,
    )
  }

  return (await response.json()) as CellItem[]
}

function CellsPage() {
  const { theme, toggleTheme } = useTheme('light')
  const [racks, setRacks] = useState<RackConfig[]>([])
  const [rackError, setRackError] = useState('')
  const [rackReloadKey, setRackReloadKey] = useState(0)
  const productCategories = racks.map((rack) => rack.name)

  const [selectedCategory, setSelectedCategory] =
    useState<ProductCategory>('TREMOR')
  const currentRack = racks.find((rack) => rack.name === selectedCategory)
  const categoryId = currentRack?.id

  useEffect(() => {
    const controller = new AbortController()
    void fetch(`${API_BASE_URL}/racks`, { signal: controller.signal })
      .then(async (response) => {
        if (!response.ok) throw new Error('Не удалось загрузить стеллажи')
        const data = await response.json() as RackConfig[]
        if (controller.signal.aborted) return
        setRacks(data)
        setRackError('')
        setSelectedCategory((value) => data.some((rack) => rack.name === value) ? value : data[0]?.name ?? '')
      })
      .catch(() => { if (!controller.signal.aborted) setRackError('Не удалось загрузить стеллажи') })
    return () => controller.abort()
  }, [rackReloadKey])

  const [isProductsOpen, setIsProductsOpen] = useState(false)
  const [isCreateRackOpen, setIsCreateRackOpen] = useState(false)

  const [selectedCell, setSelectedCell] =
    useState<string | null>(null)

  const [selectedCellItems, setSelectedCellItems] =
    useState<CellItem[]>([])

  const [cellLoadStatus, setCellLoadStatus] =
    useState<CellLoadStatus>('idle')

  const [cellReloadKey, setCellReloadKey] = useState(0)
  const cellTableRef = useRef<HTMLTableElement>(null)

  useEffect(() => {
    const table = cellTableRef.current
    if (!table) return
    function limitListHeight() {
      if (!table) return
      const rows = Array.from(table.tBodies[0]?.rows ?? []).slice(0, 5)
      const height = (table.tHead?.offsetHeight ?? 0) + rows.reduce((sum, row) => sum + row.offsetHeight, 0) + 1
      table.parentElement?.style.setProperty('--cell-list-height', `${height}px`)
    }
    const observer = new ResizeObserver(limitListHeight)
    observer.observe(table)
    limitListHeight()
    return () => observer.disconnect()
  }, [selectedCellItems, cellLoadStatus])

  useEffect(() => {
    if (!selectedCell) {
      return
    }

    const previousOverflow = document.body.style.overflow

    function handleKeyDown(event: KeyboardEvent) {
      if (event.key === 'Escape') {
        setSelectedCell(null)
      }
    }

    document.body.style.overflow = 'hidden'
    window.addEventListener('keydown', handleKeyDown)

    return () => {
      document.body.style.overflow = previousOverflow
      window.removeEventListener('keydown', handleKeyDown)
    }
  }, [selectedCell])

  useEffect(() => {
    if (!selectedCell || categoryId === undefined) {
      setSelectedCellItems([])
      setCellLoadStatus('idle')
      return
    }

    const cellName = selectedCell
    const rackId = categoryId

    let isCancelled = false

    async function loadCellContents() {
      setSelectedCellItems([])
      setCellLoadStatus('loading')

      try {
        const items = await getCellContents(cellName, rackId)

        if (isCancelled) {
          return
        }

        setSelectedCellItems(items)
        setCellLoadStatus('success')
      } catch {
        if (isCancelled) {
          return
        }

        setCellLoadStatus('error')
      }
    }

    void loadCellContents()

    return () => {
      isCancelled = true
    }
  }, [selectedCell, cellReloadKey, categoryId])

  function selectCategory(category: ProductCategory) {
    setSelectedCategory(category)
    setIsProductsOpen(false)
    setSelectedCell(null)
  }

  function openCellModal(cellName: string) {
    setSelectedCell(cellName)
  }

  function closeCellModal() {
    setSelectedCell(null)
  }

  function retryLoadCellContents() {
    if (!selectedCell) {
      return
    }

    setCellReloadKey((currentValue) => currentValue + 1)
  }

  return (
    <main className={`cellsPage cellsPage--${theme}`}>
      <Header theme={theme} onToggleTheme={toggleTheme} />

      <section className="cellsPage__content">
        <div className="cellsPage__toolbar">
          <Link className="cellsPage__homeButton" to="/">
            ГЛАВНАЯ
          </Link>

          <div className="cellsPage__products">
            <button
              className="cellsPage__productsButton"
              type="button"
              aria-haspopup="menu"
              aria-expanded={isProductsOpen}
              onClick={() => {
                setIsProductsOpen((currentValue) => !currentValue)
              }}
            >
              <span>{selectedCategory}</span>

              <span
                className={`cellsPage__productsArrow ${isProductsOpen
                    ? 'cellsPage__productsArrow--open'
                    : ''
                  }`}
                aria-hidden="true"
              >
                ▼
              </span>
            </button>

            {isProductsOpen && (
              <div
                className="cellsPage__productsMenu"
                role="menu"
                aria-label="Категории товаров"
              >
                {productCategories.map((category) => (
                  <button
                    className={`cellsPage__productsItem ${selectedCategory === category
                        ? 'cellsPage__productsItem--active'
                        : ''
                      }`}
                    type="button"
                    role="menuitem"
                    key={category}
                    onClick={() => selectCategory(category)}
                  >
                    {category}
                  </button>
                ))}
                <button className="cellsPage__productsItem cellsPage__addRack" type="button" role="menuitem" onClick={() => { setIsProductsOpen(false); setIsCreateRackOpen(true) }}>ДОБАВИТЬ +</button>
              </div>
            )}
          </div>
        </div>

        <div className="cellsPage__workspace">
          {rackError && <p role="alert">{rackError} <button type="button" onClick={() => setRackReloadKey((value) => value + 1)}>Повторить</button></p>}
          {currentRack && currentRack.rows.length > 0 ? (
            <div
              className="cellsPage__rack"
              aria-label={`Стеллаж категории ${selectedCategory}`}
            >
              {currentRack.rows.map((row, rowIndex) => (
                <div
                  className="cellsPage__rackRow"
                  key={`row-${rowIndex + 1}`}
                >
                  {row.map((cellName) => (
                    <button
                      className="cellsPage__cell"
                      type="button"
                      key={cellName}
                      aria-label={`Открыть ячейку ${cellName}`}
                      onClick={() => openCellModal(cellName)}
                    >
                      <span className="cellsPage__cellName">
                        <span className="cellsPage__cellLetter">
                          {cellName.slice(0, 1)}
                        </span>

                        <span className="cellsPage__cellNumber">
                          {cellName.slice(1)}
                        </span>
                      </span>
                    </button>
                  ))}
                </div>
              ))}
            </div>
          ) : (
            <div className="cellsPage__emptyState">
              <span>{selectedCategory}</span>
              <p>Стеллаж для этой категории ещё не добавлен</p>
            </div>
          )}
        </div>
      </section>

      {isCreateRackOpen && <CreateRackDialog onClose={() => setIsCreateRackOpen(false)} onCreated={(rack) => { setRacks((values) => [...values, rack]); setSelectedCategory(rack.name); setSelectedCell(null); setIsCreateRackOpen(false) }} />}

      {selectedCell && (
        <div
          className="cellsPage__modalOverlay"
          onMouseDown={(event) => {
            if (event.target === event.currentTarget) {
              closeCellModal()
            }
          }}
        >
          <section
            className="cellsPage__cellModal"
            role="dialog"
            aria-modal="true"
            aria-labelledby="cell-modal-title"
          >
            <header className="cellsPage__cellModalHeader">
              <button
                className="cellsPage__cellModalBack"
                type="button"
                onClick={closeCellModal}
              >
                ‹ НАЗАД
              </button>

              <h2
                className="cellsPage__cellModalTitle"
                id="cell-modal-title"
              >
                ЯЧЕЙКА {selectedCell}
              </h2>
            </header>

            <div className="cellsPage__cellModalContent">
              {cellLoadStatus === 'loading' && (
                <p className="cellsPage__cellModalEmpty">
                  Загрузка содержимого
                  <span className="cellsPage__symbolText">...</span>
                </p>
              )}

              {cellLoadStatus === 'error' && (
                <div className="cellsPage__cellModalError">
                  <p className="cellsPage__cellModalEmpty">
                    Не удалось загрузить содержимое ячейки
                  </p>

                  <button
                    className="cellsPage__cellModalRetry"
                    type="button"
                    onClick={retryLoadCellContents}
                  >
                    ПОВТОРИТЬ
                  </button>
                </div>
              )}

              {cellLoadStatus === 'success' &&
                (selectedCellItems.length > 0 ? (
                  <div className="cellsPage__cellTableScroll">
                    <table className="cellsPage__cellTable" ref={cellTableRef}>
                      <thead><tr><th scope="col">ТОВАР/АРТИКУЛ</th><th scope="col">КОЛ-ВО</th></tr></thead>
                      <tbody>
                        {selectedCellItems.map((item) => (
                          <tr key={item.id}>
                            <td>
                              {item.name}
                              <small className="cellsPage__cellTableSku">{item.sku}</small>
                            </td>
                            <td>{item.quantity}</td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                ) : (
                  <p className="cellsPage__cellModalEmpty">
                    Ячейка пуста
                  </p>
                ))}
            </div>
          </section>
        </div>
      )}
    </main>
  )
}

export default CellsPage
