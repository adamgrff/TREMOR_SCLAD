import { Link } from 'react-router'

import './Header.css'

import OzonSyncStatus, { type OzonSyncState } from './OzonSyncStatus'
import iconLogin from '../../assets/home/icon-login.svg'

export type Theme = 'dark' | 'light'

type HeaderProps = {
  theme: Theme
  onToggleTheme: () => void
  ozonSyncState?: OzonSyncState
  onOzonSync?: () => Promise<void>
}

function Header({ theme, onToggleTheme, ozonSyncState, onOzonSync }: HeaderProps) {
  const isLightTheme = theme === 'light'

  return (
    <header className={`header header--${theme}`}>
      <OzonSyncStatus state={ozonSyncState} onSync={onOzonSync} />

      <Link className="logo" to="/" aria-label="Главная страница">
        <span>TREMOR</span>
        <span>СКЛАД</span>
      </Link>

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
