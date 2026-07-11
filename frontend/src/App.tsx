import { useState } from 'react'
import './App.css'
import iconReceiving from './assets/home/icon-receiving.svg'
import iconCells from './assets/home/icon-cells.svg'
import iconAssembly from './assets/home/icon-assembly.svg'
import iconCalendar from './assets/home/icon-calendar.svg'
import iconLogin from './assets/home/icon-login.svg'

type Theme = 'dark' | 'light'

const navItems = ['РЕВИЗИЯ', 'МАГАЗИНЫ', 'КОЛЛЕКТОР', 'ИСТОРИЯ']

const mainActions = [
  {
    title: 'ПРИЕМКА',
    description: 'Добавление товара на склад',
    icon: iconReceiving,
  },
  {
    title: 'ЯЧЕЙКИ',
    description: 'Страница схемы ячеек и их содержимого',
    icon: iconCells,
  },
  {
    title: 'СБОРКА',
    description: 'Страница с выдачей и сборкой заказов',
    icon: iconAssembly,
  },
]

const warehouseStats = {
  operations: 'x',
  collector: 'x',
  products: 'x',
}

function App() {
  const [theme, setTheme] = useState<Theme>('dark')

  const isLightTheme = theme === 'light'

  function toggleTheme() {
    setTheme((currentTheme) => (currentTheme === 'dark' ? 'light' : 'dark'))
  }

  return (
    <main className={`homePage homePage--${theme}`}>
      <header className="header">
        <button
          className="headerIconButton"
          type="button"
          aria-label="Открыть календарь ревизий"
        >
          <img className="headerIcon" src={iconCalendar} alt="" aria-hidden="true" />
        </button>

        <nav className="nav nav--left" aria-label="Левая навигация">
          {navItems.slice(0, 2).map((item) => (
            <button className="navButton" type="button" key={item}>
              {item}
            </button>
          ))}
        </nav>

        <a className="logo" href="/" aria-label="Главная страница">
          <span>TREMOR</span>
          <span>СКЛАД</span>
        </a>

        <nav className="nav nav--right" aria-label="Правая навигация">
          {navItems.slice(2).map((item) => (
            <button className="navButton" type="button" key={item}>
              {item}
            </button>
          ))}
        </nav>

        <div className="headerActions">
          <button
            className="themeToggle"
            type="button"
            aria-label="Переключить тему"
            aria-pressed={isLightTheme}
            onClick={toggleTheme}
          >
            <span className="themeToggle__thumb">
              {isLightTheme ? '☾' : '☀'}
            </span>
          </button>

          <button className="loginButton" type="button" aria-label="Войти в аккаунт">
            <img className="headerIcon" src={iconLogin} alt="" aria-hidden="true" />
          </button>
        </div>
      </header>

      <section className="hero" aria-labelledby="home-title">
        <p className="hero__eyebrow">СКЛАДСКАЯ СИСТЕМА</p>

        <h1 className="hero__title" id="home-title">
          TREMOR SCLAD
        </h1>

        <div className="mainActions" aria-label="Основные действия">
          {mainActions.map((action) => (
            <button className="mainActionButton" type="button" key={action.title}>
              <img className="mainActionButton__icon" src={action.icon} alt="" aria-hidden="true" />
              <span className="mainActionButton__title">{action.title}</span>
              <span className="mainActionButton__description">
                {action.description}
              </span>
            </button>
          ))}
        </div>

        <button className="manualModeButton" type="button">
          РУЧНОЙ РЕЖИМ
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

export default App