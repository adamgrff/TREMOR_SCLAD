import { useState } from 'react'
import { Link } from 'react-router'

import Header from '../../components/Header/Header'
import { useTheme } from '../../hooks/useTheme'
import './CellsPage.css'

type ProductCategory = 'TREMOR' | 'КОТЛЕТКИ' | 'ВАЗЫ'

type RackConfig = {
  rows: string[][]
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

function CellsPage() {
  const { theme, toggleTheme } = useTheme('light')

  const [selectedCategory, setSelectedCategory] =
    useState<ProductCategory>('TREMOR')

  const [isProductsOpen, setIsProductsOpen] = useState(false)

  const currentRack = rackConfigs[selectedCategory]

  function selectCategory(category: ProductCategory) {
    setSelectedCategory(category)
    setIsProductsOpen(false)
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
    </main>
  )
}

export default CellsPage