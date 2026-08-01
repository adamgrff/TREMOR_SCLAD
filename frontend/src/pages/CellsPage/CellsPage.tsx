import { useEffect, useState } from 'react'
import { Link } from 'react-router'

import Header from '../../components/Header/Header'
import { useTheme } from '../../hooks/useTheme'
import './CellsPage.css'

type ProductCategory = 'TREMOR' | 'КОТЛЕТКИ' | 'ВАЗЫ'

type RackConfig = {
  rows: string[][]
}

type CellItem = {
  id: string
  name: string
  quantity: number
}

const productCategories: ProductCategory[] = [
  'TREMOR',
  'КОТЛЕТКИ',
  'ВАЗЫ',
]

const rackConfigs: Record<ProductCategory, RackConfig | null> = {
  TREMOR: {
    rows: [
      ['A1', 'A2', 'A3'],
      ['B1', 'B2', 'B3'],
      ['C1', 'C2', 'C3'],
    ],
  },

  КОТЛЕТКИ: null,
  ВАЗЫ: null,
}

/*
  Временные данные.

  Позже этот объект заменим запросом к backend API.
  Например: GET /api/cells/A1
*/
const cellContents: Record<string, CellItem[]> = {
  A1: [
    {
      id: 'a1-product-1',
      name: 'Ручка TREMOR Classic',
      quantity: 12,
    },
    {
      id: 'a1-product-2',
      name: 'Ручка TREMOR Pro',
      quantity: 7,
    },
    {
      id: 'a1-product-3',
      name: 'Комплект креплений',
      quantity: 24,
    },
  ],

  A2: [
    {
      id: 'a2-product-1',
      name: 'Ручка TREMOR Mini',
      quantity: 5,
    },
  ],

  B1: [
    {
      id: 'b1-product-1',
      name: 'Запасные болты',
      quantity: 42,
    },
  ],
}

function CellsPage() {
  const { theme, toggleTheme } = useTheme('light')

  const [selectedCategory, setSelectedCategory] =
    useState<ProductCategory>('TREMOR')

  const [isProductsOpen, setIsProductsOpen] = useState(false)

  const [selectedCell, setSelectedCell] =
    useState<string | null>(null)
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

  const currentRack = rackConfigs[selectedCategory]

  const selectedCellItems = selectedCell
    ? cellContents[selectedCell] ?? []
    : []

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
                className={`cellsPage__productsArrow ${
                  isProductsOpen
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
                    className={`cellsPage__productsItem ${
                      selectedCategory === category
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
              </div>
            )}
          </div>
        </div>

        <div className="cellsPage__workspace">
          {currentRack ? (
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
                <span className="cellsPage__symbolText">‹</span>
                {' '}
                НАЗАД
              </button>

              <h2
                className="cellsPage__cellModalTitle"
                id="cell-modal-title"
              >
                ЯЧЕЙКА{' '}

                <span className="cellsPage__cellModalCode">
                  <span className="cellsPage__cellModalLetter">
                    {selectedCell.slice(0, 1)}
                  </span>

                  <span className="cellsPage__cellModalNumber">
                    {selectedCell.slice(1)}
                  </span>
                </span>
              </h2>
            </header>

            <div className="cellsPage__cellModalContent">
              <h3 className="cellsPage__cellModalSubtitle">
                ТОВАР<span className="cellsPage__symbolText">:</span>
              </h3>

              {selectedCellItems.length > 0 ? (
                <ul className="cellsPage__cellItems">
                  {selectedCellItems.map((item) => (
                    <li
                      className="cellsPage__cellItem"
                      key={item.id}
                    >
                      <span className="cellsPage__cellItemName">
                        {item.name}
                      </span>

                      <span
                        className="cellsPage__cellItemDots"
                        aria-hidden="true"
                      />

                      <span className="cellsPage__cellItemQuantity">
                        <span className="cellsPage__cellItemNumber">
                          {item.quantity}
                        </span>{' '}
                        ШТ<span className="cellsPage__symbolText">.</span>
                      </span>
                    </li>
                  ))}
                </ul>
              ) : (
                <p className="cellsPage__cellModalEmpty">
                  Ячейка пуста
                </p>
              )}
            </div>
          </section>
        </div>
      )}
    </main>
  )
}

export default CellsPage