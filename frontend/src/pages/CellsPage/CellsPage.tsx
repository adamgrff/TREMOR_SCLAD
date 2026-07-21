import { useState } from 'react'

import Header, { type Theme } from '../../components/Header/Header'
import './CellsPage.css'

function CellsPage() {
  const [theme, setTheme] = useState<Theme>('light')

  function toggleTheme() {
    setTheme((currentTheme) =>
      currentTheme === 'light' ? 'dark' : 'light',
    )
  }

  return (
    <main className={`cellsPage cellsPage--${theme}`}>
      <Header theme={theme} onToggleTheme={toggleTheme} />

      <section className="cellsPage__content">
        <h1>СТРАНИЦА ЯЧЕЕК</h1>
      </section>
    </main>
  )
}

export default CellsPage