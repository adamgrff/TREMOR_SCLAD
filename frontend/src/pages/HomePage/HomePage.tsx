import { useState } from 'react'
import { useNavigate } from 'react-router'
import '../../App.css'

import Header, { type Theme } from '../../components/Header/Header'

import iconReceiving from '../../assets/home/icon-receiving.svg'
import iconCells from '../../assets/home/icon-cells.svg'
import iconAssembly from '../../assets/home/icon-assembly.svg'

import iconReceivingBlack from '../../assets/home/icon-receiving-black.svg'
import iconCellsBlack from '../../assets/home/icon-cells-black.svg'
import iconAssemblyBlack from '../../assets/home/icon-assembly-black.svg'

import manualModeLight from '../../assets/home/manual-mode-light.svg'
import manualModeBlack from '../../assets/home/manual-mode-black.svg'

type MainAction = {
  title: string
  description: string
  icon: string
  lightIcon: string
  path?: string
}

const mainActions: MainAction[] = [
  {
    title: 'ПРИЕМКА',
    description: 'Добавление товара на склад',
    icon: iconReceiving,
    lightIcon: iconReceivingBlack,
  },
  {
    title: 'ЯЧЕЙКИ',
    description: 'Страница схемы ячеек и их содержимого',
    icon: iconCells,
    lightIcon: iconCellsBlack,
    path: '/cells',
},
  {
    title: 'СБОРКА',
    description: 'Страница с выдачей и сборкой заказов',
    icon: iconAssembly,
    lightIcon: iconAssemblyBlack,
  },
]

const warehouseStats = {
  operations: 'x',
  collector: 'x',
  products: 'x',
}

function HomePage() {
  const navigate = useNavigate()
  const [theme, setTheme] = useState<Theme>('dark')

  const isLightTheme = theme === 'light'

  function toggleTheme() {
    setTheme((currentTheme) => (currentTheme === 'dark' ? 'light' : 'dark'))
  }

  return (
    <main className={`homePage homePage--${theme}`}>
      <Header theme={theme} onToggleTheme={toggleTheme} />

      <section className="hero" aria-labelledby="home-title">
        <p className="hero__eyebrow">СКЛАДСКАЯ СИСТЕМА</p>

        <h1 className="hero__title" id="home-title">
          TREMOR SCLAD
        </h1>

        <div className="mainActions" aria-label="Основные действия">
          {mainActions.map((action) => (
            <button
              className="mainActionButton"
              type="button"
              key={action.title}
              onClick={() => {
                if (action.path) {
                  navigate(action.path)
                }
              }}
            >
              <img
                className="mainActionButton__icon"
                src={isLightTheme ? action.lightIcon : action.icon}
                alt=""
                aria-hidden="true"
              />
              <span className="mainActionButton__title">{action.title}</span>
              <span className="mainActionButton__description">
                {action.description}
              </span>
            </button>
          ))}
        </div>

        <button
          className="manualModeButton"
          type="button"
          aria-label="Ручной режим"
        >
          <img
            className="manualModeButton__image"
            src={isLightTheme ? manualModeLight : manualModeBlack}
            alt=""
            aria-hidden="true"
          />
        </button>
      </section>

      <footer className="statsPanel" aria-label="Статистика склада">
        <span className="statsPanel__today">
          СЕГОДНЯ<span className="statsPanel__colon">:</span>
        </span>

        <span>
          {warehouseStats.operations} ОПЕРАЦИЙ
        </span>

        <span className="statsPanel__separator">•</span>

        <span>
         {warehouseStats.collector} В КОЛЛЕКТОРЕ
        </span>

        <span className="statsPanel__separator">•</span>

        <span>
          {warehouseStats.products} ТОВАРОВ НА СКЛАДЕ
        </span>
      </footer>
    </main>
  )
}

export default HomePage