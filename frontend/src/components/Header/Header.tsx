import { Link } from 'react-router'

import './Header.css'

import iconCalendar from '../../assets/home/icon-calendar.svg'
import iconLogin from '../../assets/home/icon-login.svg'

export type Theme = 'dark' | 'light'

type HeaderProps = {
  theme: Theme
  onToggleTheme: () => void
}

const navItems = ['РЕВИЗИЯ', 'МАГАЗИНЫ', 'КОЛЛЕКТОР', 'ИСТОРИЯ']

function Header({ theme, onToggleTheme }: HeaderProps) {
  const isLightTheme = theme === 'light'

  return (
    <header className={`header header--${theme}`}>
      <button
        className="headerIconButton"
        type="button"
        aria-label="Открыть календарь ревизий"
      >
        <img
          className="headerIcon"
          src={iconCalendar}
          alt=""
          aria-hidden="true"
        />
      </button>

      <nav className="nav nav--left" aria-label="Левая навигация">
        {navItems.slice(0, 2).map((item) => (
          <button className="navButton" type="button" key={item}>
            {item}
          </button>
        ))}
      </nav>

      <Link className="logo" to="/" aria-label="Главная страница">
        <span>TREMOR</span>
        <span>СКЛАД</span>
      </Link>

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
          onClick={onToggleTheme}
        >
          <span className="themeToggle__thumb">
            {isLightTheme ? '☾' : '☀'}
          </span>
        </button>

        <button
          className="loginButton"
          type="button"
          aria-label="Войти в аккаунт"
        >
          <img
            className="headerIcon"
            src={iconLogin}
            alt=""
            aria-hidden="true"
          />
        </button>
      </div>
    </header>
  )
}

export default Header